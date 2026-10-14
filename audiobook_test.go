package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hehehai/AudioSmith/internal/core"
	"github.com/hehehai/AudioSmith/internal/engine/proto"
	"github.com/hehehai/AudioSmith/internal/jobs"
	"github.com/hehehai/AudioSmith/internal/services"
)

func TestAudiobookManuscriptCastAndWarnings(t *testing.T) {
	a, _, tt := newTestApp(t)
	tt.SetSize(1100, 1400)
	a.navigate("/audiobook")
	p := a.lfBook
	tt.Frame()
	if err := tt.Click("Load sample"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !strings.Contains(p.doc.Script, "# The Night Ferry") || p.ed.Buffer.String() != p.doc.Script || p.doc.Voice == "" {
		t.Fatalf("sample %q, voice %q", p.doc.Script[:40], p.doc.Voice)
	}
	if !tt.HasText("2 chapters · 72 words · 0:37 est. runtime") && !hasAnyText(tt.Texts(), "2 chapters · ") {
		t.Errorf("stats: %q", tt.Texts())
	}
	// The cast comes from the manuscript's [voice:] names.
	if !tt.HasText("Ines") || !tt.HasText("Tomas") || p.doc.VoiceCast["Ines"] == "" {
		t.Errorf("cast: %+v %q", p.doc.VoiceCast, tt.Texts())
	}
	if len(p.spec.Chapters) != 2 || a.lfBlocker(p) != "" {
		t.Fatalf("plan %d chapters, blocker %q", len(p.spec.Chapters), a.lfBlocker(p))
	}
	// The markup toolbar inserts at the caret.
	p.ed.Select(0, 0)
	if err := tt.Click("Pause"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !strings.HasPrefix(p.doc.Script, "[pause 500ms]") {
		t.Errorf("pause: %q", p.doc.Script[:30])
	}
	p.ed.Select(0, 0)
	tt.Click("Voice")
	tt.Frame()
	if !strings.HasPrefix(p.doc.Script, "[voice:NAME]") || p.ed.SelectedText() != "NAME" {
		t.Errorf("voice: %q, selected %q", p.doc.Script[:30], p.ed.SelectedText())
	}
	// Warnings: an unknown name, a tag read aloud, an empty chapter.
	a.setManuscript(p, "# One\n\n[voice:Nobody]Hello [boop] there.\n\n# Empty\n\n# Two\n\nThe end.")
	tt.Frame()
	tt.Frame()
	for _, want := range []string{"Before you create", "Unknown voice “Nobody”: the default voice reads it.", "Unrecognised tag [boop]: it will be read aloud.", "Chapter “Empty” has no spoken text."} {
		if !hasAnyText(tt.Texts(), want) {
			t.Errorf("no %q in %q", want, tt.Texts())
		}
	}
	tt.Click("Dismiss")
	tt.Frame()
	if tt.HasText("Before you create") {
		t.Error("dismissed")
	}
	// A word listed twice blocks Generate.
	p.doc.Lexicon = []services.LexiconEntry{{Word: "Harrow", Say: "Harro"}, {Word: "harrow", Say: "Haro"}}
	p.open["lexicon"] = true
	tt.Frame()
	if msg := a.lfBlocker(p); !strings.Contains(msg, "listed twice") || !hasAnyText(tt.Texts(), "Keep one pronunciation per word") {
		t.Errorf("duplicate: %q", msg)
	}
	p.doc.Lexicon = p.doc.Lexicon[:1]
	// No narrator voice: Generate says so.
	p.doc.Voice = ""
	tt.Frame()
	if msg := a.lfBlocker(p); !strings.Contains(msg, "default voice") {
		t.Errorf("no voice: %q", msg)
	}
}

func TestAudiobookImportRenderAndReadAlong(t *testing.T) {
	a, eng, tt := newTestApp(t)
	synth := useLfSynth(a)
	tt.SetSize(1100, 1400)
	a.navigate("/audiobook")
	p := a.lfBook
	f := filepath.Join(t.TempDir(), "book.txt")
	os.WriteFile(f, []byte("Chapter 1\n\nIt was a cold night. The wind howled.\n\nChapter 2\n\nMorning came at last.\n"), 0o644)
	a.importBookFrom(p, f)
	tt.Frame()
	if !strings.Contains(p.doc.Script, "# Chapter 1") || !strings.Contains(p.doc.Script, "# Chapter 2") {
		t.Fatalf("imported %q", p.doc.Script)
	}
	p.doc.Voice = "speaker:af_heart"
	p.doc.Book.Loudness = services.LoudnessACX
	p.doc.Title = "Cold Night"
	tt.Frame()

	// Audition chapter 2.
	a.startLf(p, "chapter:1")
	tt.Frame()
	jl := lfJobs(eng)
	if len(jl) != 1 {
		t.Fatalf("audition %+v", jl)
	}
	if cp := chapterParams(t, jl[0]); cp.Title != "Chapter 2" || cp.Loudness != "" {
		t.Errorf("audition %+v", cp)
	}
	playLongform(t, a, eng)
	tt.Frame()
	if st, _ := a.auditionState(p, "chapter:1"); st != "done" {
		t.Errorf("audition %s", st)
	}

	// (The import's toast covers the button in the tester's window.)
	if !tt.HasText("Create audiobook") || a.lfBlocker(p) != "" {
		t.Fatalf("blocker %q", a.lfBlocker(p))
	}
	a.startLf(p, "")
	tt.Frame()
	if !tt.HasText("Synthesizing chapter 1 of 2") && !tt.HasText("Waiting for the engine…") {
		t.Errorf("status %q", tt.Texts())
	}
	synth.texts = nil
	playLongform(t, a, eng)
	tt.Frame()
	r := a.lfRenders[p.render]
	if r == nil || r.rec.State != core.RenderDone {
		t.Fatalf("render %+v", r)
	}
	// Chapter 2 was auditioned with the same settings but no loudness:
	// its chunks come from the cache.
	if len(synth.texts) != 1 || !strings.HasPrefix(synth.texts[0], "It was a cold night") {
		t.Errorf("made %q", synth.texts)
	}
	tt.Frame()
	for _, want := range []string{"Audiobook ready", "Follow along", "ACX: every chapter passes"} {
		if !hasAnyText(tt.Texts(), want) {
			t.Errorf("no %q: %q", want, tt.Texts())
		}
	}
	if len(p.ra.chapters) != 2 || len(p.ra.chapters[0].Paras) != 1 || len(p.ra.chapters[0].Paras[0].Words) != 8 {
		t.Fatalf("read-along %+v", p.ra.chapters)
	}
	w := p.ra.chapters[0].Paras[0].Words
	if w[0].Text != "It" || w[0].Start < 0.5 || w[7].End <= w[0].End {
		t.Errorf("words %+v", w)
	}
	// A click on a word plays the chapter from there.
	if err := tt.Click("howled."); err != nil {
		t.Fatal(err)
	}
	if a.pl.Current() != raPlayerID(r.rec.ID, 0) {
		t.Errorf("playing %q", a.pl.Current())
	}
	a.pl.Stop()

	// Align words: stt.align with the chapter's text, then the aligner's times.
	if !tt.HasText("Align words") {
		t.Fatalf("%q", tt.Texts())
	}
	a.alignChapter(p, r, 0) // (the toast covers the button in the tester's window)
	al := eng.lastOf(t, proto.MethodSTTAlign)
	var ap proto.AlignParams
	if err := lfJSONUnmarshal(al.Spec.Params, &ap); err != nil || ap.Audio != p.ra.chapters[0].File || !strings.Contains(ap.Text, "cold night") {
		t.Fatalf("align %+v %v", ap, err)
	}
	words := make([]proto.Word, 8)
	for i := range words {
		words[i] = proto.Word{Text: "w", Start: float64(i), End: float64(i) + 0.5}
	}
	eng.succeed(al.ID, proto.AlignResult{Words: words})
	tt.Frame()
	if ch := p.ra.chapters[0]; !ch.Aligned || ch.Paras[0].Words[3].Start != 3 {
		t.Errorf("aligned %+v", ch.Paras[0].Words)
	}
	if !tt.HasText("Word timing comes from the forced aligner.") {
		t.Errorf("%q", tt.Texts())
	}
	_ = jobs.StateSucceeded
}

func TestWordsOf(t *testing.T) {
	got := wordsOf("Hello, world! 你好，世界。 Ok")
	want := []string{"Hello,", "world!", "你", "好，", "世", "界。", "Ok"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("%q", got)
	}
}
