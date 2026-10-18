package services

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/hehehai/AudioSmith/internal/dub"
	"github.com/hehehai/AudioSmith/internal/dub/dubtest"
	"github.com/hehehai/AudioSmith/internal/engine/proto"
)

// zhLines are a conversation's Chinese lines (the translation, pasted).
var zhLines = []string{"嘿，明天出门的行李你收拾好了吗？", "差不多了。我还是找不到我的雨衣。", "真的吗？",
	"我好像上个月把它落在你车里了。", "太好了。你来接我的时候能顺便带上吗？", "没问题，我会带上。"}

// dialogueDub is a new two-speaker dub into Chinese, transcribed and
// translated (zhLines), the dialogue model offered.
func dialogueDub(t *testing.T) (*dubEnv, *dub.Project) {
	t.Helper()
	e := newDubEnv(t, 6)
	e.models = dialogueModels()
	p := e.create(t, func(p *dub.Project) { p.ChooseTargets([]string{"zh-Hans"}) })
	p = e.run(t, p.ID, RunRequest{Through: dub.StageReferences})
	return e, e.edit(t, p.ID, func(p *dub.Project) {
		for i, s := range p.Segments {
			p.SetText(s.ID, "zh-Hans", zhLines[i])
		}
	})
}

func (e *dubEnv) edit(t *testing.T, id string, fn func(*dub.Project)) *dub.Project {
	t.Helper()
	p, err := e.dubs.Update(ctx, id, func(p *dub.Project) error {
		fn(p)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// calls are the fake engine's requests of method so far, in order (and
// their positions among all requests).
func (e *dubEnv) calls(method string) ([]json.RawMessage, []int) {
	e.eng.Calls = slices.Clip(e.eng.Calls)
	var out []json.RawMessage
	var at []int
	for i, c := range e.eng.Calls {
		if c.Method == method {
			out, at = append(out, c.Params), append(at, i)
		}
	}
	return out, at
}

func (e *dubEnv) dialogues() []proto.DialogueParams {
	raw, _ := e.calls(proto.MethodTTSDialogue)
	out := make([]proto.DialogueParams, len(raw))
	for i, r := range raw {
		json.Unmarshal(r, &out[i])
	}
	return out
}

// spoken are a request's turns to speak (without codes).
func spoken(d proto.DialogueParams) (texts []string, context int) {
	for _, t := range d.Turns {
		if len(t.Codes) > 0 {
			context++
		} else {
			texts = append(texts, t.Text)
		}
	}
	return texts, context
}

// The conversation is spoken by the dialogue model in one request, each
// voice cloned from 10–14 s of the speaker's own lines (or what they have)
// with the source's transcript; every line is fitted at its source start.
// A line edited is made again alone by continuation; a line moved is only
// fitted again; a voice cast makes the whole stretch again.
func TestDubDialogue(t *testing.T) {
	e, p := dialogueDub(t)
	p = e.run(t, p.ID, RunRequest{Through: dub.StageMix})
	ds := e.dialogues()
	if len(ds) != 1 || len(ds[0].Turns) != 6 || len(ds[0].Speakers) != 2 {
		t.Fatalf("dialogue requests %+v", ds)
	}
	if texts, n := spoken(ds[0]); n != 0 || !slices.Equal(texts, zhLines) {
		t.Errorf("turns %q (%d context)", texts, n)
	}
	s1 := ds[0].Speakers[0]
	r1 := p.References["S1"]
	lines := dub.DialogueRefLines(p.Segments, "S1", &r1)
	if len(lines) < 2 || s1.RefText != dub.DialogueRefText(p.Segments, lines) || !strings.HasPrefix(s1.RefText, "Hello there. ") ||
		!strings.HasSuffix(s1.RefAudio, ".ref.wav") || !fileExists(s1.RefAudio) {
		t.Errorf("S1's reference %+v", s1)
	}
	if n := e.eng.Count(proto.MethodTTSSynthesize); n != 0 {
		t.Errorf("%d lines on the dub's model", n)
	}
	tr := p.Track("zh-Hans")
	if tr.File == "" || len(tr.Lines) != 6 {
		t.Fatalf("track %+v", tr)
	}
	for _, s := range p.Segments {
		r := tr.Lines[s.ID]
		if r.Via != dub.ViaDialogue || r.Dialogue != 1 || r.Err != "" || !fileExists(e.dubs.Path(p.ID, r.File)) || r.Start != s.Start || r.Length <= 0 {
			t.Errorf("%s: %+v", s.ID, r)
		}
	}
	if st := p.Stage(dub.StageSynthesize, "zh-Hans"); st.Status != dub.StatusDone || len(st.Warnings) != 0 {
		t.Errorf("stage %+v", st)
	}
	keys := map[string]string{}
	for id, r := range tr.Lines {
		keys[id] = r.Key
	}

	// One line edited: only it, by continuation after the two before it.
	edited := p.Segments[2].ID
	p = e.edit(t, p.ID, func(p *dub.Project) { p.SetText(edited, "zh-Hans", "真的吗？在哪儿？") })
	p = e.run(t, p.ID, RunRequest{Through: dub.StageMix})
	ds = e.dialogues()
	if len(ds) != 2 {
		t.Fatalf("%d dialogue requests", len(ds))
	}
	if texts, n := spoken(ds[1]); n != 2 || !slices.Equal(texts, []string{"真的吗？在哪儿？"}) {
		t.Errorf("redo %q after %d", texts, n)
	}
	for id, r := range p.Track("zh-Hans").Lines {
		if changed := r.Key != keys[id]; changed != (id == edited) || r.Via != dub.ViaDialogue {
			t.Errorf("%s: changed %v, %+v", id, changed, r)
		}
	}
	if r := p.Track("zh-Hans").Lines[edited]; r.Text != "真的吗？在哪儿？" {
		t.Errorf("edited %+v", r)
	}

	// A line moved: fitted again, nothing made.
	moved := p.Segments[4]
	p = e.edit(t, p.ID, func(p *dub.Project) { p.SetTiming(moved.ID, moved.Start+0.05, moved.End, p.Source.Duration, false) })
	p = e.run(t, p.ID, RunRequest{Through: dub.StageMix})
	if n := len(e.dialogues()); n != 2 || e.eng.Count(proto.MethodTTSSynthesize) != 0 {
		t.Errorf("moving a line made it again (%d requests)", n)
	}
	if r := p.Track("zh-Hans").Lines[moved.ID]; r.Start != moved.Start+0.05 || r.Via != dub.ViaDialogue {
		t.Errorf("moved %+v", r)
	}

	// A voice cast: the whole stretch, the voice saying a reference text
	// on the dub's model first.
	p = e.edit(t, p.ID, func(p *dub.Project) { p.Cast = map[string]string{"S2": "v2"} })
	p = e.run(t, p.ID, RunRequest{Through: dub.StageMix})
	ds = e.dialogues()
	if len(ds) != 3 {
		t.Fatalf("%d dialogue requests", len(ds))
	}
	if texts, n := spoken(ds[2]); n != 0 || len(texts) != 6 || ds[2].Speakers[1].RefText != PortableLine("zh") {
		t.Errorf("after casting: %d turns, %d context, S2 %+v", len(texts), n, ds[2].Speakers[1])
	}
	if n := e.eng.Count(proto.MethodTTSSynthesize); n != 1 {
		t.Errorf("%d syntheses (the reference text once)", n)
	}
	// The old stretch's files went.
	dirs, _ := os.ReadDir(e.dubs.Path(p.ID, "renders/zh-Hans/dlg"))
	if len(dirs) != 1 {
		t.Errorf("%d stretch directories", len(dirs))
	}
}

// A line the dialogue model says wrong is made again by continuation (a
// new seed, the lines before it as context); wrong twice, it is made on
// the dub's model after the stretches, checked, and flagged when it fails
// that check too. Only names a line to make again by continuation.
func TestDubDialogueFallback(t *testing.T) {
	e, p := dialogueDub(t)
	e.eng.Garble[zhLines[2]] = 1
	e.eng.Garble[zhLines[3]] = 2
	e.eng.Heard[zhLines[3]] = "完全不对的话"
	p = e.run(t, p.ID, RunRequest{Through: dub.StageMix})
	ds := e.dialogues()
	if len(ds) != 3 {
		t.Fatalf("%d dialogue requests", len(ds))
	}
	for i, want := range []string{zhLines[2], zhLines[3]} {
		texts, n := spoken(ds[i+1])
		if !slices.Equal(texts, []string{want}) || n != 2+i || *ds[i+1].Seed == *ds[0].Seed {
			t.Errorf("continuation %d: %q after %d, seed %d", i, texts, n, *ds[i+1].Seed)
		}
	}
	_, synth := e.calls(proto.MethodTTSSynthesize)
	_, dlg := e.calls(proto.MethodTTSDialogue)
	if len(synth) != 1 || synth[0] < dlg[len(dlg)-1] {
		t.Errorf("syntheses at %v, dialogue at %v: the dub's model goes after", synth, dlg)
	}
	tr := p.Track("zh-Hans")
	ok, back := tr.Lines[p.Segments[2].ID], tr.Lines[p.Segments[3].ID]
	if ok.Via != dub.ViaDialogue || ok.Flag != "" {
		t.Errorf("made again: %+v", ok)
	}
	if back.Via != dub.ViaFallback || back.FallbackReason != DialogueHeard || back.Dialogue != 1 || back.Flag != dub.FlagHeard || back.Heard != "完全不对的话" {
		t.Errorf("given back: %+v", back)
	}
	st := p.Stage(dub.StageSynthesize, "zh-Hans")
	if !slices.Contains(st.Warnings, DubWarnDialogueFallback) || !slices.Contains(st.Warnings, DubWarnLinesFlagged) {
		t.Errorf("warnings %v", st.Warnings)
	}

	// Asked for again: made again by continuation, other seeds each time.
	id := p.Segments[1].ID
	p = e.run(t, p.ID, RunRequest{Through: dub.StageMix, Only: []string{id}})
	p = e.run(t, p.ID, RunRequest{Through: dub.StageMix, Only: []string{id}})
	ds = e.dialogues()
	if len(ds) != 5 {
		t.Fatalf("%d dialogue requests", len(ds))
	}
	for _, d := range ds[3:] {
		if texts, n := spoken(d); !slices.Equal(texts, []string{zhLines[1]}) || n != 1 {
			t.Errorf("again: %q after %d", texts, n)
		}
	}
	if *ds[3].Seed == *ds[4].Seed || p.Track("zh-Hans").DialogueTakes["line:"+id] != 2 {
		t.Errorf("seeds %d %d, takes %v", *ds[3].Seed, *ds[4].Seed, p.Track("zh-Hans").DialogueTakes)
	}

	// The whole stretch again (整段重录): another take, another seed.
	delete(e.eng.Heard, zhLines[3])
	first := p.Segments[0].ID
	p = e.edit(t, p.ID, func(p *dub.Project) {
		tr := p.Track("zh-Hans")
		tr.DialogueTakes[first]++
		p.PutTrack(tr)
	})
	p = e.run(t, p.ID, RunRequest{Through: dub.StageMix})
	ds = e.dialogues()
	if texts, n := spoken(ds[len(ds)-1]); len(texts) != 6 || n != 0 || *ds[len(ds)-1].Seed == *ds[0].Seed {
		t.Errorf("retake: %d turns, seed %d", len(texts), *ds[len(ds)-1].Seed)
	}
}

// Where the dialogue model does not apply, every line is made line by
// line: the model not downloaded (with a warning), a stretch it cannot
// make (cached: not asked again), per-line voice match, a language it
// does not speak, a line kept out (「这一句不用多人对话」).
func TestDubDialogueOff(t *testing.T) {
	e, p := dialogueDub(t)
	e.notDown = []string{"m/soulx"}
	p = e.run(t, p.ID, RunRequest{Through: dub.StageMix})
	if len(e.dialogues()) != 0 || e.eng.Count(proto.MethodTTSSynthesize) != 6 {
		t.Errorf("not downloaded: %d dialogue requests", len(e.dialogues()))
	}
	if st := p.Stage(dub.StageSynthesize, "zh-Hans"); !slices.Contains(st.Warnings, DubWarnDialogueUnavailable) {
		t.Errorf("warnings %v", st.Warnings)
	}
	for _, r := range p.Track("zh-Hans").Lines {
		if r.Via != "" || r.Dialogue != 0 {
			t.Errorf("line %+v", r)
		}
	}

	// The model fails the stretch: every line on the dub's model, once.
	e.notDown = nil
	e.eng.Fail[proto.MethodTTSDialogue] = proto.CodeInternal
	p = e.run(t, p.ID, RunRequest{Through: dub.StageMix, Force: []dub.Stage{dub.StageSynthesize}})
	for _, r := range p.Track("zh-Hans").Lines {
		if r.Via != dub.ViaFallback || r.FallbackReason != DialogueSegmentFailed || r.Flag != "" {
			t.Errorf("segment failed: %+v", r)
		}
	}
	n := len(e.dialogues())
	p = e.edit(t, p.ID, func(p *dub.Project) { p.SetText(p.Segments[5].ID, "zh-Hans", "没问题。") })
	p = e.run(t, p.ID, RunRequest{Through: dub.StageMix})
	if len(e.dialogues()) != n {
		t.Error("a failed stretch was asked again")
	}
	delete(e.eng.Fail, proto.MethodTTSDialogue)

	// Kept out: the line on the dub's model, the stretch around it.
	p = e.edit(t, p.ID, func(p *dub.Project) {
		p.Segments[5].NoDialogue = true
		tr := p.Track("zh-Hans")
		tr.DialogueTakes = map[string]int{p.Segments[0].ID: 1} // a new take: no failure cached
		p.PutTrack(tr)
	})
	p = e.run(t, p.ID, RunRequest{Through: dub.StageMix})
	if ds := e.dialogues(); len(ds[len(ds)-1].Turns) != 5 {
		t.Errorf("kept out: %d turns", len(ds[len(ds)-1].Turns))
	}
	if r := p.Track("zh-Hans").Lines[p.Segments[5].ID]; r.Via != "" || r.Dialogue != 0 {
		t.Errorf("kept out: %+v", r)
	}

	// Per-line voice match, and Japanese: line by line.
	for _, edit := range []func(*dub.Project){
		func(p *dub.Project) { p.Settings.VoiceMatch = dub.MatchLine },
		func(p *dub.Project) { p.Settings.VoiceMatch = dub.MatchConsistent; p.ChooseTargets([]string{"ja"}) },
	} {
		n := len(e.dialogues())
		p = e.edit(t, p.ID, edit)
		p = e.run(t, p.ID, RunRequest{Through: dub.StageMix})
		if len(e.dialogues()) != n {
			t.Errorf("%v: spoken by the dialogue model", p.Targets)
		}
	}
}

// The translation of a track the dialogue model speaks aims at its rate:
// a little shorter (the speed it assumes, below 1); an old project's as
// before.
func TestDubDialogueTranslationRate(t *testing.T) {
	e := newDubEnv(t, 6)
	e.models = dialogueModels()
	p := e.create(t, func(p *dub.Project) { p.ChooseTargets([]string{"zh-Hans"}) })
	p = e.run(t, p.ID, RunRequest{Through: dub.StageTranslate})
	want := 1 / dub.FitRate(dialogueRates.rate(dialogueModels().Dialogue[0]), 1)
	if e.tr.speed >= 1 || e.tr.speed != want {
		t.Errorf("speed %v, want %v", e.tr.speed, want)
	}
	q := e.create(t, func(p *dub.Project) { p.ChooseTargets([]string{"zh-Hans"}); p.Settings.Dialogue = "" })
	e.run(t, q.ID, RunRequest{Through: dub.StageTranslate})
	if e.tr.speed != 0 {
		t.Errorf("an old project's speed %v", e.tr.speed)
	}
	// The rate learns from the lines it makes.
	m := dialogueModels().Dialogue[0]
	m.Repo = "m/other"
	before := dialogueRates.rate(m)
	dialogueRates.observe(m, 3, 2)
	if after := dialogueRates.rate(m); after <= before || after > 1.6 {
		t.Errorf("rate %v → %v", before, after)
	}
}

// A saved cloned voice is cloned from its clip, a designed voice from its
// reference; a Mac with too little memory makes every line line by line
// (with a warning), as a project without a dialogue model offered.
func TestDubDialogueRefs(t *testing.T) {
	e, p := dialogueDub(t)
	clip := filepath.Join(e.dir, "saved.wav")
	if _, err := dubtest.Speech(clip, 3); err != nil {
		t.Fatal(err)
	}
	e.dubs.cfg.Voice = func(_ context.Context, ref, _, _ string) (dub.VoiceSpec, error) {
		if ref == "cloned" {
			return dub.VoiceSpec{RefAudio: clip, RefText: "My own words."}, nil
		}
		return dub.VoiceSpec{Voice: "a calm voice", CloneRef: clip, CloneRefText: "A designed sample."}, nil
	}
	p = e.edit(t, p.ID, func(p *dub.Project) { p.Cast = map[string]string{"S1": "cloned", "S2": "designed"} })
	p = e.run(t, p.ID, RunRequest{Through: dub.StageMix})
	ds := e.dialogues()
	if len(ds) != 1 || ds[0].Speakers[0].RefText != "My own words." || ds[0].Speakers[1].RefText != "A designed sample." {
		t.Fatalf("references %+v", ds)
	}
	if e.eng.Count(proto.MethodTTSSynthesize) != 0 {
		t.Error("a reference was said first")
	}

	m := dialogueModels()
	m.Dialogue[0].Blocked = DubDialogueMemory
	e.models = m
	if why := m.DialogueBlocked(p, "zh-Hans"); why != DubDialogueMemory {
		t.Errorf("blocked %q", why)
	}
	p = e.run(t, p.ID, RunRequest{Through: dub.StageMix, Force: []dub.Stage{dub.StageSynthesize}})
	if len(e.dialogues()) != 1 || !slices.Contains(p.Stage(dub.StageSynthesize, "zh-Hans").Warnings, DubWarnDialogueUnavailable) {
		t.Errorf("too little memory: %d requests, %v", len(e.dialogues()), p.Stage(dub.StageSynthesize, "zh-Hans").Warnings)
	}
	if why := (DubModels{}).DialogueBlocked(p, "zh-Hans"); why != "model" {
		t.Errorf("no model offered: %q", why)
	}
	p.Settings.Dialogue = dub.DialogueOff
	if why := (DubModels{}).DialogueBlocked(p, "zh-Hans"); why != dub.DialogueWhyOff {
		t.Errorf("off: %q", why)
	}
	if _, ok := m.DialogueFor("m/other"); ok {
		t.Error("an unknown dialogue model")
	}
}
