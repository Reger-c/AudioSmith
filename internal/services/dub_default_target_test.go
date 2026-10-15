package services

import (
	"slices"
	"testing"
	"time"

	"github.com/hehehai/AudioSmith/internal/dub"
)

// createDefault makes a project with dub.New's default target (no choice).
func (e *dubEnv) createDefault(t *testing.T, edit func(*dub.Project)) *dub.Project {
	t.Helper()
	p := dub.New("", time.Now())
	p.Title = "Interview"
	p.Source = dub.Source{Kind: dub.SourceFile, Path: e.src, Name: "interview.wav"}
	p.Settings.Model = "tts"
	p.Settings.Review = false
	if edit != nil {
		edit(p)
	}
	p, err := e.dubs.Create(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// A dub nobody chose a target for is made in Chinese.
func TestDubDefaultTarget(t *testing.T) {
	e := newDubEnv(t, 2)
	p := e.createDefault(t, nil)
	if !p.TargetsAuto || !slices.Equal(p.Targets, []string{dub.DefaultTarget}) {
		t.Fatalf("new %v auto %v", p.Targets, p.TargetsAuto)
	}
	p = e.run(t, p.ID, RunRequest{Through: dub.StageMix})
	if p.Detected != "en" || !slices.Equal(p.Targets, []string{"zh-Hans"}) || p.TargetSwitched != "" {
		t.Errorf("detected %q targets %v switched %q", p.Detected, p.Targets, p.TargetSwitched)
	}
	if p.Track("zh-Hans").File == "" || p.Run != nil {
		t.Errorf("track %+v run %+v", p.Track("zh-Hans"), p.Run)
	}
}

// A Chinese video with the default target is dubbed into English, even
// though the run was planned (in Chinese) before the transcript.
func TestDubDefaultTargetSwitchesForChinese(t *testing.T) {
	e := newDubEnv(t, 2)
	p := e.createDefault(t, func(p *dub.Project) { p.SourceLang = "zh" })
	if got := e.dubs.Plan(p, RunRequest{Through: dub.StageMix}); !slices.Contains(got, "translate:zh-Hans") {
		t.Fatalf("plan %v", got)
	}
	p = e.run(t, p.ID, RunRequest{Through: dub.StageMix})
	if !slices.Equal(p.Targets, []string{"en"}) || p.TargetSwitched != "zh-Hans" || !p.TargetsAuto {
		t.Fatalf("targets %v switched %q auto %v", p.Targets, p.TargetSwitched, p.TargetsAuto)
	}
	if p.Track("en").File == "" || p.Track("zh-Hans").File != "" || p.Run != nil {
		t.Errorf("tracks %v run %+v", p.Tracks, p.Run)
	}
	for k := range p.Stages {
		if k == dub.StageKey(dub.StageTranslate, "zh-Hans") || k == dub.StageKey(dub.StageMix, "zh-Hans") {
			t.Errorf("stage %s ran in the replaced language", k)
		}
	}
	if !p.AllTranslated() || p.Segments[0].Lines["en"].Text == "" {
		t.Errorf("lines %+v", p.Segments[0].Lines)
	}
}

// A target someone chose is used as it is, even in the spoken language.
func TestDubChosenTargetIsKept(t *testing.T) {
	e := newDubEnv(t, 2)
	p := e.createDefault(t, func(p *dub.Project) {
		p.SourceLang = "zh"
		p.ChooseTargets([]string{"zh-Hans"})
	})
	p = e.run(t, p.ID, RunRequest{Through: dub.StageReferences})
	if !slices.Equal(p.Targets, []string{"zh-Hans"}) || p.TargetsAuto || p.TargetSwitched != "" {
		t.Errorf("targets %v auto %v switched %q", p.Targets, p.TargetsAuto, p.TargetSwitched)
	}
	// So is an older project's (no targetsAuto in its document).
	old := e.create(t, func(p *dub.Project) { p.TargetsAuto = false; p.Targets = []string{"es"}; p.SourceLang = "es" })
	old = e.run(t, old.ID, RunRequest{Through: dub.StageReferences})
	if !slices.Equal(old.Targets, []string{"es"}) {
		t.Errorf("old project's targets %v", old.Targets)
	}
}

func TestBatchDefaultTarget(t *testing.T) {
	e := newDubEnv(t, 2)
	bs := e.dubs.BatchSettings(ctx)
	if !bs.TargetsAuto || !slices.Equal(bs.Targets, []string{dub.DefaultTarget}) {
		t.Fatalf("defaults %+v", bs)
	}
	ids, errs := e.dubs.Enqueue(ctx, []string{e.src}, bs)
	if len(ids) != 1 || len(errs) != 0 {
		t.Fatalf("%v %v", ids, errs)
	}
	p, _ := e.dubs.Get(ctx, ids[0])
	if !p.TargetsAuto || !slices.Equal(p.Targets, []string{dub.DefaultTarget}) {
		t.Errorf("batch project %v auto %v", p.Targets, p.TargetsAuto)
	}
	for _, j := range must(e.dubs.Active(ctx, p.ID)) {
		e.wait(t, j.ID)
	}
	// Saved settings keep the default while nobody changed the targets.
	bs.KeepBackground = false
	if err := e.dubs.SetBatchSettings(ctx, bs); err != nil {
		t.Fatal(err)
	}
	if got := e.dubs.BatchSettings(ctx); !got.TargetsAuto || got.KeepBackground {
		t.Errorf("saved %+v", got)
	}
	// Settings saved before the default changed are a choice.
	if err := e.dubs.cfg.Documents.Put(ctx, batchSettingsDoc, []byte(`{"targets":["es"],"keepBackground":true}`)); err != nil {
		t.Fatal(err)
	}
	got := e.dubs.BatchSettings(ctx)
	if got.TargetsAuto || !slices.Equal(got.Targets, []string{"es"}) {
		t.Errorf("old settings %+v", got)
	}
	ids, _ = e.dubs.Enqueue(ctx, []string{e.src}, got)
	if p, _ := e.dubs.Get(ctx, ids[0]); p.TargetsAuto {
		t.Error("chosen batch targets marked default")
	}
	for _, j := range must(e.dubs.Active(ctx, ids[0])) {
		e.wait(t, j.ID)
	}
}

func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}
