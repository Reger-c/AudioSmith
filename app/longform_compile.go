package main

// Compiling a longform page's document as it changes, and handing it to
// the draft service. A small document is compiled in the frame it
// changed in; a large one (a novel's manuscript) once typing pauses, on a
// goroutine, with its draft: compiling 200,000 characters and encoding
// them twice took 14 ms a key on the UI thread (review #102).

import (
	"encoding/json"
	"hash/fnv"
	"slices"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/hehehai/AudioSmith/internal/core"
	"github.com/hehehai/AudioSmith/internal/services"
)

// lfDeferBytes is how large a document's text is before it compiles once
// typing pauses rather than at every change.
const lfDeferBytes = 20000

// lfCompileDelay is the pause in typing before a large document compiles.
const lfCompileDelay = 300 * time.Millisecond

// lfCompile is a page's deferred compile.
type lfCompile struct {
	compiled bool // the document was compiled since it was set (setLfDoc)
	// deferring: the document was large when last compiled, so the page
	// compiles once typing pauses; its results, and bookCast's, may be of
	// an older text than the editor's: the manuscript's at version.
	deferring bool
	version   uint64
	due       time.Time    // when the pending compile runs; zero for none
	waiting   bool         // a timer is set to look at due
	gen       atomic.Int64 // bumped as the document changes
	mu        sync.Mutex   // held by the compile running off the UI thread
	rev       int          // bumped as a compile's results land
	// The lines and the hash of their JSON as the fingerprint last
	// encoded them (a story's lines, which are many).
	fpLines []services.StoryLine
	fpHash  uint64
	fpOK    bool
}

// lfDeferAlways defers the compile without a window, for the frame
// benchmarks (tests compile at once otherwise).
var lfDeferAlways bool

// autosaveLf hands a page's document to the draft service when it
// changed, and recompiles it: at once when it is small, else once typing
// pauses, off the UI thread.
func (a *app) autosaveLf(p *lfPage) {
	fp := a.lfFingerprint(p)
	if fp == p.fp {
		return
	}
	p.fp = fp
	if p.lfc.compiled && p.lfc.deferring {
		p.lfc.gen.Add(1)
		p.lfc.due = time.Now().Add(lfCompileDelay)
		if !p.lfc.waiting {
			p.lfc.waiting = true
			a.lfWake(p, lfCompileDelay)
		}
		return
	}
	// A compile still running off the UI thread is outdated.
	p.lfc.gen.Add(1)
	p.lfc.due = time.Time{}
	p.lfc.mu.Lock()
	defer p.lfc.mu.Unlock()
	a.compileLf(p)
	a.bookDialogueDefault(p)
	a.saveLfDraft(p)
}

// lfTextSize is how much text a document has to compile.
func lfTextSize(d *services.LongformDoc) int {
	n := len(d.Script)
	for _, l := range d.Lines {
		n += len(l.Text)
	}
	return n
}

// lfWake looks at a page's pending compile after d.
func (a *app) lfWake(p *lfPage, d time.Duration) {
	time.AfterFunc(d, func() {
		a.post(func() {
			if p.lfc.due.IsZero() {
				p.lfc.waiting = false
				return
			}
			if left := time.Until(p.lfc.due); left > 0 {
				a.lfWake(p, left)
				return
			}
			p.lfc.waiting = false
			a.lfCompileNow(p)
		})
	})
}

// lfCompileNow compiles a page's document and saves its draft on a
// goroutine, from a copy of the document; the results land on the UI
// thread unless the document changed again meanwhile.
func (a *app) lfCompileNow(p *lfPage) {
	p.lfc.due = time.Time{}
	gen := p.lfc.gen.Load()
	d := lfDocCopy(&p.doc)
	resolve := a.lfResolver(&d)
	project, kind, key, rev, version := p.project, p.kind, p.draftKey(), p.scriptRev, p.edVersion
	draft := lfDraft{Version: 1, Project: p.project, Saved: p.savedJSON, Render: p.render, Auto: p.auto}
	tail := "|" + d.Render + "|" + p.project + "|" + p.render + "|" + strconv.FormatBool(p.auto)
	last := p.draftJSON
	drafts := a.lib.svc.Drafts
	a.background(func() {
		p.lfc.mu.Lock()
		defer p.lfc.mu.Unlock()
		if p.lfc.gen.Load() != gen {
			return // changed again: the newer compile follows
		}
		spec, probs, blocks := services.CompileBook(&d, resolve)
		spec.Project = project
		stats := lfStats(kind, &d)
		var cast []string
		if kind == core.ProjectAudiobook {
			cast = castNamesOf(d.Script) // bookCast
		}
		js := docJSON(&d) + tail
		if js != last {
			draft.Doc = d
			drafts.Save(key, draft)
		}
		a.post(func() {
			if p.lfc.gen.Load() != gen {
				return
			}
			p.spec, p.problems, p.blocks, p.stats = spec, probs, blocks, stats
			p.draftJSON = js
			p.lfc.version = version
			p.lfc.deferring = a.lfDefers(&d)
			if cast != nil {
				p.cast, p.castRev = cast, rev
			}
			p.lfc.rev++
			a.bookDialogueDefault(p)
		})
	})
}

// stale reports whether the compile's results are of an older
// manuscript than the editor's text at version: their spans may not be
// where the text is now.
func (c *lfCompile) stale(version uint64) bool { return c.deferring && c.version != version }

// flushLongform saves the drafts of pages whose compile is still pending
// (on quit).
func (a *app) flushLongform() {
	for _, p := range []*lfPage{a.lfStories, a.lfBook} {
		if p == nil || p.lfc.due.IsZero() {
			continue
		}
		p.lfc.gen.Add(1)
		p.lfc.due = time.Time{}
		p.lfc.mu.Lock()
		a.saveLfDraft(p)
		p.lfc.mu.Unlock()
	}
}

// lfDocCopy copies a document for another goroutine: the manuscript is
// a string, which no one changes; the rest goes through JSON, so the copy
// shares no slice or map with the page's.
func lfDocCopy(d *services.LongformDoc) services.LongformDoc {
	c := *d
	script := c.Script
	c.Script = ""
	b, err := json.Marshal(c)
	var out services.LongformDoc
	if err != nil || json.Unmarshal(b, &out) != nil {
		out = c
		out.Lines = slices.Clone(c.Lines)
	}
	out.Script = script
	return out
}

// saveLfDraft hands the page's document to the draft service, unless it
// is what was handed last.
func (a *app) saveLfDraft(p *lfPage) {
	d := lfDraft{Version: 1, Doc: p.doc, Project: p.project, Saved: p.savedJSON, Render: p.render, Auto: p.auto}
	if js := docJSON(&p.doc) + "|" + p.doc.Render + "|" + p.project + "|" + p.render + "|" + strconv.FormatBool(p.auto); js != p.draftJSON {
		p.draftJSON = js
		a.lib.svc.Drafts.Save(p.draftKey(), d)
	}
}

// lfFingerprint changes whenever the document, or a voice it may use,
// does: the manuscript by its revision (it is large), a story's lines by
// the JSON of the lines last encoded while they stay equal.
func (a *app) lfFingerprint(p *lfPage) string {
	d := p.doc
	d.Script = ""
	lines := d.Lines
	d.Lines = nil
	h := fnv.New64a()
	b, _ := json.Marshal(d)
	h.Write(b)
	f := &p.lfc
	if !f.fpOK || !slices.Equal(lines, f.fpLines) {
		lh := fnv.New64a()
		lb, _ := json.Marshal(lines)
		lh.Write(lb)
		f.fpLines, f.fpHash, f.fpOK = slices.Clone(lines), lh.Sum64(), true
	}
	h.Write([]byte(strconv.FormatUint(f.fpHash, 36) + "|" + strconv.Itoa(p.scriptRev) + "|" + p.project + "|" + p.render))
	for _, v := range a.voices {
		h.Write([]byte(v.ID + v.UpdatedAt.String()))
	}
	return strconv.FormatUint(h.Sum64(), 36)
}

// compileLf compiles a page's document (stats, problems, the spec).
func (a *app) compileLf(p *lfPage) {
	d := p.doc
	spec, probs, blocks := services.CompileBook(&d, a.lfResolver(&d))
	spec.Project = p.project
	p.spec, p.problems, p.blocks = spec, probs, blocks
	p.stats = lfStats(p.kind, &d)
	p.lfc.compiled, p.lfc.version = true, p.edVersion
	p.lfc.deferring = a.lfDefers(&d)
	p.lfc.rev++
}

// lfDefers reports whether a document is large enough to compile once
// typing pauses (with a window: tests compile at once).
func (a *app) lfDefers(d *services.LongformDoc) bool {
	return (a.win != nil || lfDeferAlways) && lfTextSize(d) >= lfDeferBytes
}

// lfStats are a document's statistics.
func lfStats(kind core.ProjectKind, d *services.LongformDoc) services.ScriptStats {
	if kind == core.ProjectAudiobook {
		return services.StatsOf(d.Script, d.Speed)
	}
	return services.StatsOf(services.StoryScript(d.Lines), d.Speed)
}
