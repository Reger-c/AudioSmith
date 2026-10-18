package main

import (
	"image/png"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/hehehai/AudioSmith/internal/catalog"
	"github.com/hehehai/AudioSmith/internal/dub"
	"github.com/hehehai/AudioSmith/internal/engine/proto"
	"github.com/hehehai/AudioSmith/internal/hub"
	"github.com/hehehai/AudioSmith/internal/i18n"
	"github.com/hehehai/AudioSmith/internal/services"
	"github.com/hehehai/AudioSmith/internal/settings"
)

// dlgZh are the Chinese lines of dubtest's six-line conversation.
var dlgZh = []string{"嘿，你好啊。", "你今天怎么样？", "我很好，谢谢你。", "那就好，听你这么说真开心。", "明天见。", "再见！"}

// newDialogueDubTest is a two-speaker dub into Chinese on a 24 GB Mac
// with the dialogue model downloaded, transcribed and translated (the
// lines pasted), on the Cast step.
func newDialogueDubTest(t *testing.T) *dubTest {
	t.Helper()
	d := newDubTest(t, 6)
	a := d.a
	a.updateSettings(func(s *settings.Settings) { s.Dubbing.Targets = []string{"zh-Hans"} })
	a.sys = &sysInfo{Memory: 24 << 30}
	e := dubDialogueEntries()[0]
	m := a.model(e)
	m.known, m.local.State = true, hub.Downloaded
	// Its files in the model cache (the pipeline asks the hub).
	cache := modelCache(a.prefs)
	put := func(repo, rev string, files []catalog.File) {
		for _, f := range files {
			p := filepath.Join(hub.RepoDir(cache, repo), "snapshots", rev, filepath.FromSlash(f.Path))
			os.MkdirAll(filepath.Dir(p), 0o755)
			if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	put(e.Repo, e.Revision, e.Files)
	for _, x := range e.Aux {
		put(x.Repo, x.Revision, x.Files)
	}
	a.dubSourceFile(d.src)
	a.dubStart()
	a.dubSet(func(p *dub.Project) {
		for i := range p.Segments {
			p.Segments[i].Lines = map[string]dub.Line{"zh-Hans": {Text: dlgZh[i], Edited: true}}
		}
	})
	a.dubLang, a.dubStep = "zh-Hans", dubStepCast
	d.tt.Frame()
	return d
}

// The Cast step's switch: on for a new dub with what it found, off for an
// old one; greyed with the reason on an 8 GB Mac, with per-line voice
// match, for a Japanese dub; the download where it is missing; hidden
// with one speaker.
func TestDubDialogueSwitch(t *testing.T) {
	d := newDialogueDubTest(t)
	a, tt := d.a, d.tt
	if !a.dub.Settings.DialogueOn() || !tt.HasText("Multi-speaker dialogue") || !tt.HasText("1 conversation · 6 lines · Dialogue · Light") {
		t.Fatalf("on: %q", tt.Texts())
	}
	a.dub.SourceLang = "en"
	tt.Frame()
	if !tt.HasText("In conversations: their English voice speaks Chinese (Simplified)") && !slices.ContainsFunc(tt.Texts(), func(s string) bool { return strings.HasPrefix(s, "In conversations: their") }) {
		t.Errorf("speaker note: %q", tt.Texts())
	}
	m := a.model(entryFor(dubDialogueEntries()[0].Repo))
	m.local.State = hub.Missing
	tt.Frame()
	if !slices.ContainsFunc(tt.Texts(), func(s string) bool { return strings.Contains(s, "is not downloaded") }) {
		t.Errorf("download: %q", tt.Texts())
	}
	m.local.State = hub.Downloaded
	a.sys = &sysInfo{Memory: 8 << 30}
	tt.Frame()
	if !tt.HasText("Needs 16 GB of memory; this Mac has 8 GB.") || a.dubDialogue().on {
		t.Errorf("8 GB: %q", tt.Texts())
	}
	a.sys = &sysInfo{Memory: 24 << 30}
	a.dubSettings(func(s *dub.Settings) { s.VoiceMatch = dub.MatchLine })
	tt.Frame()
	if !tt.HasText("Per-line voice match clones each line from its own clip: it cannot go with multi-speaker dialogue.") {
		t.Errorf("per line: %q", tt.Texts())
	}
	a.dubSettings(func(s *dub.Settings) { s.VoiceMatch = dub.MatchConsistent })
	a.dubSet(func(p *dub.Project) { p.ChooseTargets([]string{"zh-Hans", "ja"}) })
	tt.Frame()
	if !slices.ContainsFunc(tt.Texts(), func(s string) bool { return strings.Contains(s, ": line by line; the multi-speaker models") }) || !a.dubDialogue().on {
		t.Errorf("zh + ja: %q", tt.Texts())
	}
	a.dubLang = "ja"
	a.dubSet(func(p *dub.Project) { p.ChooseTargets([]string{"ja"}) })
	tt.Frame()
	if dl := a.dubDialogue(); dl.on || dl.blocked == "" {
		t.Errorf("ja: %+v", dl)
	}
	// Off by the switch; an old project.
	a.dubLang = "zh-Hans"
	a.dubSet(func(p *dub.Project) { p.ChooseTargets([]string{"zh-Hans"}) })
	a.dubSettings(func(s *dub.Settings) { s.Dialogue = dub.DialogueOff })
	tt.Frame()
	if a.dub.Settings.Dialogue != dub.DialogueOff || tt.HasText("1 conversation · 6 lines · Dialogue · Light") {
		t.Errorf("off: %q %q", a.dub.Settings.Dialogue, tt.Texts())
	}
	// One speaker: no switch.
	a.dubSet(func(p *dub.Project) {
		for i := range p.Segments {
			p.Segments[i].Speaker = "S1"
		}
	})
	tt.Frame()
	if tt.HasText("Multi-speaker dialogue") {
		t.Error("the switch shows for one speaker")
	}
}

// Generating records the conversation with the dialogue model: the line
// list's chips; a line kept out goes line by line; 整段重录 records it
// again with another seed; the footer counts; an audition plays a line
// recorded; the generate step's note and picker section.
func TestDubDialogueLines(t *testing.T) {
	d := newDialogueDubTest(t)
	a, tt := d.a, d.tt
	a.dubGenerate()
	tt.Frame()
	p := a.dub
	if n := d.fe.Count(proto.MethodTTSDialogue); n != 1 || d.fe.Count(proto.MethodTTSSynthesize) != 0 {
		t.Fatalf("%d dialogue requests, %d syntheses", n, d.fe.Count(proto.MethodTTSSynthesize))
	}
	for _, s := range p.Segments {
		if r := p.Track("zh-Hans").Lines[s.ID]; r.Via != dub.ViaDialogue || r.Dialogue != 1 {
			t.Errorf("%s: %+v", s.ID, r)
		}
	}
	a.dubStep = dubStepGenerate
	tt.Frame()
	if !tt.HasText("Conversation 1") || !slices.ContainsFunc(tt.Texts(), func(s string) bool { return strings.HasPrefix(s, "Multi-speaker dialogue is on:") }) {
		t.Errorf("chips and note: %q", tt.Texts())
	}
	if path, in := a.dubDialogueHeard(p.Segments[0].ID, "zh-Hans"); !in || path == "" {
		t.Errorf("audition of a recorded line: %q %v", path, in)
	}
	// Kept out: line by line, the rest again together.
	last := p.Segments[5].ID
	a.dubSetLine(last, "dialogue", func(s *dub.Segment) { s.NoDialogue = true })
	tt.Frame()
	if a.dubFooterCount() != i18n.N(5, "%d line in conversations", "%d lines in conversations", 5) {
		t.Errorf("footer %q", a.dubFooterCount())
	}
	a.dubGenerate()
	if d.fe.Count(proto.MethodTTSDialogue) != 2 || d.fe.Count(proto.MethodTTSSynthesize) != 1 {
		t.Errorf("kept out: %d dialogue, %d syntheses", d.fe.Count(proto.MethodTTSDialogue), d.fe.Count(proto.MethodTTSSynthesize))
	}
	if r := a.dub.Track("zh-Hans").Lines[last]; r.Via != "" || r.Dialogue != 0 {
		t.Errorf("kept out %+v", r)
	}
	// 整段重录.
	a.dubRetake("zh-Hans", a.dub.Segments[0].ID)
	tt.Frame()
	if d.fe.Count(proto.MethodTTSDialogue) != 3 || a.dub.Track("zh-Hans").DialogueTakes[a.dub.Segments[0].ID] != 1 {
		t.Errorf("retake: %d requests, takes %v", d.fe.Count(proto.MethodTTSDialogue), a.dub.Track("zh-Hans").DialogueTakes)
	}
	// The generate step's picker lists the dialogue model.
	if dl := a.dubDialoguePick(a.dubDialogue()); dl == nil || !dl.On || dl.Cur.Family != "soulx_podcast" || len(dl.Models) != 1 {
		t.Errorf("picker section %+v", dl)
	}
	// A line made line by line in its place, flagged.
	a.dubSet(func(p *dub.Project) {
		tr := p.Track("zh-Hans")
		r := tr.Lines[p.Segments[2].ID]
		r.Via, r.FallbackReason, r.Flag, r.Heard = dub.ViaFallback, services.DialogueHeard, dub.FlagHeard, "我很不好"
		tr.Lines[p.Segments[2].ID] = r
		p.PutTrack(tr)
	})
	tt.Frame()
	if !tt.HasText("Conversation 1 · line by line") || !tt.HasText("Made line by line, and still not heard clearly (“我很不好”): listen to it.") {
		t.Errorf("flagged: %q", tt.Texts())
	}
}

// dubFooterCount is the footer's conversation count.
func (a *app) dubFooterCount() string { return a.dubDialogueCount() }

// TestSnapshotsDubDialogue renders thread #89: the Cast step's switch (on
// with what it found and a speaker's cross-language note, not downloaded,
// greyed on an 8 GB Mac, a Chinese and a Japanese track), the line list
// after generating (chips, bars, a line made line by line and flagged,
// the footer's count) and the Generate step's picker with its
// conversations section. Chinese and English at 1100×720 and 820×520
// @125 %:
//
//	AUDIOSMITH_SNAPSHOTS=/tmp/shots go test -run TestSnapshotsDubDialogue .
func TestSnapshotsDubDialogue(t *testing.T) { runSnapshots(t, snapshotsDubDialogue) }

func snapshotsDubDialogue(t *testing.T, out string) {
	t.Cleanup(func() { i18n.SetLocale("en") })
	for _, cfg := range []b2Config{
		{"1100-zh", 1100, 720, 1, false, "zh-Hans"},
		{"min125-zh", 820, 520, 1.25, false, "zh-Hans"},
		{"1100-en", 1100, 720, 1, false, "en"},
		{"min125-en", 820, 520, 1.25, false, "en"},
	} {
		i18n.SetLocale("en")
		d := newDialogueDubTest(t)
		a, tt := d.a, d.tt
		a.updateSettings(func(s *settings.Settings) {
			s.General.Language = cfg.lang
			s.Appearance.Scale = cfg.scale
		})
		tt.SetSize(cfg.w, cfg.h)
		tt.SetScale(2)
		a.dubSet(func(p *dub.Project) { p.SourceLang = "en" })
		dl := a.model(entryFor(dubDialogueEntries()[0].Repo))
		shoot := func(name string, fn func()) {
			fn()
			a.toasts = nil
			tt.Frame()
			tt.Frame()
			tt.Frame()
			f, err := os.Create(filepath.Join(out, name+"-"+cfg.name+".png"))
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			if err := png.Encode(f, tt.Image()); err != nil {
				t.Fatal(err)
			}
		}
		shoot("dlg-dub-cast-on", func() {})
		shoot("dlg-dub-cast-needdl", func() { dl.local.State = hub.Missing })
		dl.local.State = hub.Downloaded
		shoot("dlg-dub-cast-8gb", func() { a.sys = &sysInfo{Memory: 8 << 30} })
		a.sys = &sysInfo{Memory: 24 << 30}
		shoot("dlg-dub-cast-zh-ja", func() { a.dubSet(func(p *dub.Project) { p.ChooseTargets([]string{"zh-Hans", "ja"}) }) })
		shoot("dlg-dub-cast-ja", func() {
			a.dubLang = "ja"
			a.dubSet(func(p *dub.Project) { p.ChooseTargets([]string{"ja"}) })
		})
		a.dubLang = "zh-Hans"
		a.dubSet(func(p *dub.Project) { p.ChooseTargets([]string{"zh-Hans"}) })
		shoot("dlg-dub-cast-off", func() { a.dubSettings(func(s *dub.Settings) { s.Dialogue = dub.DialogueOff }) })
		a.dubSettings(func(s *dub.Settings) { s.Dialogue = dub.DialogueAuto })
		d.fe.Garble[dlgZh[2]] = 2
		d.fe.Heard[dlgZh[2]] = "我很不好"
		a.dubGenerate()
		shoot("dlg-dub-lines", func() {
			a.dubStep = dubStepGenerate
			a.dubCur = a.dub.Segments[2].ID
		})
		shoot("dlg-dub-picker", func() {
			if a.pickerOpen == nil {
				a.pickerOpen, a.pickerAll, a.pickerFind = map[string]bool{}, map[string]bool{}, map[string]string{}
			}
			a.pickerOpen["gen-model"] = true
		})
		a.pickerOpen["gen-model"] = false
	}
}
