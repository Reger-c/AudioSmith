package services

import (
	"encoding/json"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/hehehai/AudioSmith/internal/dub"
	"github.com/hehehai/AudioSmith/internal/engine/proto"
	"github.com/hehehai/AudioSmith/internal/translate"
)

const testMoss = "vanch007/mlx-MOSS-Transcribe-Diarize-8bit"

func mossModels() *DubModels {
	return &DubModels{STT: "stt", Align: "align", Diarize: "diar", Separate: "sep", Enhance: "dfn",
		Speakers: testMoss, SpeakersLanguages: []string{"zh", "en", "ja"}}
}

// sttModels are the models stt.transcribe was called with to transcribe
// the source, in order (not the synthesize stage's check of made lines).
func (e *dubEnv) sttModels() []string {
	var out []string
	for _, c := range e.eng.Calls {
		if c.Method == proto.MethodSTTTranscribe {
			var p proto.TranscribeParams
			json.Unmarshal(c.Params, &p)
			if !strings.Contains(p.Audio, string(filepath.Separator)+"renders"+string(filepath.Separator)) {
				out = append(out, p.Model)
			}
		}
	}
	return out
}

// TestDubSpeakersModel (thread #48): with the speakers model downloaded,
// it transcribes and labels the lines; Sortformer does not run; the
// aligner still times the words; the user's glossary terms are its
// vocabulary.
func TestDubSpeakersModel(t *testing.T) {
	e := newDubEnv(t, 6)
	e.eng.Speakers = 3
	e.models = mossModels()
	p := e.create(t, func(p *dub.Project) {
		p.Glossary = translate.Glossary{{Source: "AudioSmith", Target: "AudioSmith"}, {Source: "auto term", Target: "x", Auto: true}}
	})
	p = e.run(t, p.ID, RunRequest{Through: dub.StageTranscribe})
	if got := e.sttModels(); !slices.Equal(got, []string{testMoss}) {
		t.Errorf("transcribed with %v", got)
	}
	if n := e.eng.Count(dub.MethodDiarize); n != 0 {
		t.Errorf("diarize.run called %d times", n)
	}
	if n := e.eng.Count(proto.MethodSTTAlign); n != 1 {
		t.Errorf("stt.align called %d times", n)
	}
	var tp proto.TranscribeParams
	for _, c := range e.eng.Calls {
		var q proto.TranscribeParams
		if c.Method == proto.MethodSTTTranscribe && json.Unmarshal(c.Params, &q) == nil && q.Model == testMoss {
			tp = q
		}
	}
	if tp.Prompt != "AudioSmith" || tp.Words {
		t.Errorf("params %+v", tp)
	}
	var who []string
	for _, s := range p.Segments {
		who = append(who, s.Speaker)
		if len(s.Words) == 0 {
			t.Errorf("segment %q has no word times", s.Text)
		}
	}
	if !slices.Equal(who, []string{"S1", "S2", "S3", "S1", "S2", "S3"}) {
		t.Errorf("speakers %v", who)
	}
	st := p.Stage(dub.StageTranscribe, "")
	if st.Status != dub.StatusDone || len(st.Warnings) != 0 {
		t.Errorf("stage %+v", st)
	}
}

// TestDubSpeakersModelMissing: not downloaded, the dub falls back to the
// default recognition and Sortformer, and says the better model exists.
func TestDubSpeakersModelMissing(t *testing.T) {
	e := newDubEnv(t, 4)
	e.models = mossModels()
	e.notDown = []string{testMoss}
	p := e.create(t, nil)
	p = e.run(t, p.ID, RunRequest{Through: dub.StageTranscribe})
	if got := e.sttModels(); !slices.Equal(got, []string{"stt"}) {
		t.Errorf("transcribed with %v", got)
	}
	if n := e.eng.Count(dub.MethodDiarize); n != 1 {
		t.Errorf("diarize.run called %d times", n)
	}
	if st := p.Stage(dub.StageTranscribe, ""); st.Status != dub.StatusDone || !slices.Contains(st.Warnings, DubWarnSpeakersModel) {
		t.Errorf("stage %+v", st)
	}
	if labels := p.SpeakerLabels(); !slices.Equal(labels, []string{"S1", "S2"}) {
		t.Errorf("speakers %v", labels)
	}
}

// TestDubSpeakersModelNotUsed: one speaker, or a language the speakers
// model does not know, keeps the default recognition.
func TestDubSpeakersModelNotUsed(t *testing.T) {
	for name, edit := range map[string]func(*dub.Project){
		"one speaker": func(p *dub.Project) { p.Speakers = 1 },
		"language":    func(p *dub.Project) { p.SourceLang = "sw" },
	} {
		e := newDubEnv(t, 3)
		e.models = mossModels()
		p := e.create(t, edit)
		e.run(t, p.ID, RunRequest{Through: dub.StageTranscribe})
		if got := e.sttModels(); !slices.Equal(got, []string{"stt"}) {
			t.Errorf("%s: transcribed with %v", name, got)
		}
	}
	m := mossModels()
	for lang, want := range map[string]string{"": testMoss, "auto": testMoss, "zh-Hans": testMoss, "EN": testMoss, "sw": ""} {
		if got := m.SpeakersFor(lang); got != want {
			t.Errorf("SpeakersFor(%q) = %q", lang, got)
		}
	}
	if got := (DubModels{}).SpeakersFor("en"); got != "" {
		t.Errorf("no speakers model: %q", got)
	}
}
