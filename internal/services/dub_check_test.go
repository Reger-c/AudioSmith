package services

import (
	"encoding/json"
	"os"
	"slices"
	"testing"

	"github.com/hehehai/AudioSmith/internal/core"
	"github.com/hehehai/AudioSmith/internal/dub"
	"github.com/hehehai/AudioSmith/internal/engine/proto"
)

// synthCalls are the tts.synthesize requests, in order.
func (e *dubEnv) synthCalls() []proto.SynthesizeParams {
	var out []proto.SynthesizeParams
	for _, c := range e.eng.Calls {
		if c.Method == proto.MethodTTSSynthesize {
			var p proto.SynthesizeParams
			json.Unmarshal(c.Params, &p)
			out = append(out, p)
		}
	}
	return out
}

// checkCalls counts the stt.transcribe requests of made lines (the
// synthesize stage's check).
func (e *dubEnv) checkCalls() int {
	return len(e.sttModelsAll()) - len(e.sttModels())
}

func (e *dubEnv) sttModelsAll() []string {
	var out []string
	for _, c := range e.eng.Calls {
		if c.Method == proto.MethodSTTTranscribe {
			out = append(out, "")
		}
	}
	return out
}

// TestDubCrossLingualClone (thread #90): a line in another language than
// its speaker's clip clones from the clip alone when the model can (no
// transcript), and is checked; a same-language dub keeps the transcript
// and is not checked; a model that cannot keeps it and is checked.
func TestDubCrossLingualClone(t *testing.T) {
	for _, c := range []struct {
		name      string
		target    string
		audioOnly bool
		refText   bool
		checked   bool
	}{
		{"en→es, audio only", "es", true, false, true},
		{"en→es, transcript needed", "es", false, true, true},
		{"en→en", "en", true, true, false},
	} {
		e := newDubEnv(t, 6)
		e.dubs.cfg.Caps = func(string) DubModelCaps { return DubModelCaps{Clone: true, Speed: true, CloneAudioOnly: c.audioOnly} }
		p := e.create(t, func(p *dub.Project) { p.ChooseTargets([]string{c.target}) })
		p = e.run(t, p.ID, RunRequest{Through: dub.StageMix})
		calls := e.synthCalls()
		if len(calls) != 6 {
			t.Errorf("%s: %d lines made", c.name, len(calls))
		}
		for _, sp := range calls {
			if sp.RefAudio == "" || (sp.RefText != "") != c.refText {
				t.Errorf("%s: synthesize ref %q text %q", c.name, sp.RefAudio, sp.RefText)
			}
		}
		if n := e.checkCalls(); (n == 6) != c.checked || (n != 0) != c.checked {
			t.Errorf("%s: %d lines checked", c.name, n)
		}
		tr := p.Track(c.target)
		for id, r := range tr.Lines {
			if (r.Spoken != nil) != c.checked || r.Spoken != nil && !r.Spoken.OK() {
				t.Errorf("%s: line %s spoken %+v", c.name, id, r.Spoken)
			}
		}
		if st := p.Stage(dub.StageSynthesize, c.target); st.Status != dub.StatusDone || len(st.Warnings) != 0 {
			t.Errorf("%s: stage %+v", c.name, st)
		}
	}
}

// TestDubLineCheck (thread #90): a checked line heard in the wrong
// language is made once more and the better take kept; one that stays
// wrong is kept, flagged and warned about, never silently.
func TestDubLineCheck(t *testing.T) {
	e := newDubEnv(t, 6)
	e.dubs.cfg.Caps = func(string) DubModelCaps { return DubModelCaps{Clone: true, Speed: true} }
	e.eng.HeardOnce["[es] Hello there."] = "ハローゼア"    // the clip's language once
	e.eng.Heard["[es] Goodbye."] = "Sayonara mata ne" // garbled every time
	p := e.create(t, nil)
	p = e.run(t, p.ID, RunRequest{Through: dub.StageMix})
	calls := e.synthCalls()
	if len(calls) != 8 {
		t.Fatalf("%d lines made, want 6 and 2 again", len(calls))
	}
	// Made again with the transcript (the model clones only with it) and
	// another seed.
	for _, sp := range calls[6:] {
		if sp.RefText == "" || sp.Seed == nil || calls[0].Seed != nil && *sp.Seed == *calls[0].Seed {
			t.Errorf("again: ref text %q seed %v", sp.RefText, sp.Seed)
		}
	}
	tr := p.Track("es")
	for _, s := range p.Segments {
		r := tr.Lines[s.ID]
		if r.File == "" || r.Spoken == nil {
			t.Fatalf("line %q: %+v", s.Text, r)
		}
		if _, err := os.Stat(e.dubs.Path(p.ID, r.File)); err != nil {
			t.Errorf("line %q: %v", s.Text, err)
		}
		bad := s.Text == "Goodbye."
		if r.Spoken.OK() == bad {
			t.Errorf("line %q: spoken %+v", s.Text, r.Spoken)
		}
	}
	st := p.Stage(dub.StageSynthesize, "es")
	if !slices.Contains(st.Warnings, DubWarnLinesUnclear) {
		t.Errorf("stage %+v", st)
	}
	left, _ := os.ReadDir(e.dubs.Path(p.ID, "renders/es"))
	for _, f := range left {
		if n := f.Name(); len(n) > 6 && n[len(n)-6:] == ".first" {
			t.Errorf("left over %s", n)
		}
	}
}

func TestRetryTake(t *testing.T) {
	icl := core.Take{RefAudio: "ref.wav", RefText: "それと", Seed: 5, Render: &core.TakeRender{Bits: 32}}
	if got := retryTake(icl, DubModelCaps{CloneAudioOnly: true}); got.RefText != "" || got.Seed != 5 || got.RefAudio != "ref.wav" {
		t.Errorf("audio only: %+v", got)
	}
	got := retryTake(icl, DubModelCaps{})
	if got.RefText != icl.RefText || got.Seed == 5 || !got.Render.SeedPinned || icl.Render.SeedPinned {
		t.Errorf("seed: %+v %+v", got, got.Render)
	}
	if got := retryTake(core.Take{RefAudio: "ref.wav"}, DubModelCaps{CloneAudioOnly: true}); got.Seed == 0 {
		t.Errorf("clip alone already: %+v", got)
	}
}
