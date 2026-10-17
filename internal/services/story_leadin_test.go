package services

import (
	"context"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/hehehai/AudioSmith/internal/core"
	"github.com/hehehai/AudioSmith/internal/dsp/dsptest"
	"github.com/hehehai/AudioSmith/internal/dsp/wav"
	"github.com/hehehai/AudioSmith/internal/engine/proto"
	"github.com/hehehai/AudioSmith/internal/jobs"
	"github.com/hehehai/AudioSmith/internal/text"
)

func TestLeadPrefix(t *testing.T) {
	l := func(who, text string) LineSpec {
		return LineSpec{Character: who, Spans: []SpanSpec{{Voice: who, Character: who, Text: text}}}
	}
	lines := []LineSpec{
		l("kid", "妈妈，我想领养一只小猫，可以吗？"),
		l("mom", "你能一直照顾它吗？"),
		l("kid", "我能！我每天喂它。"),
		l("", "妈妈笑了。"),
		l("mom", "好吧。"),
		l("kid", "太好了！"),
	}
	for i, want := range []string{"", "", "妈妈，我想领养一只小猫，可以吗？", "", "你能一直照顾它吗？", "我能！我每天喂它。"} {
		if got := leadPrefix(lines, i); got != want {
			t.Errorf("line %d: lead-in %q, want %q", i+1, got, want)
		}
	}
	// A turn takes none; nor a line whose character spoke in another voice.
	lines[5].Turn = true
	if leadPrefix(lines, 5) != "" {
		t.Error("turn")
	}
	lines[5].Turn = false
	lines[2].Spans[0].Voice = "other"
	if leadPrefix(lines, 5) != "" {
		t.Error("voice")
	}
	for _, c := range []struct{ in, want string }{
		{"好吧", "好吧。"},
		{"Fine. I will look after it every single day and every night, I promise you, Mum.", "I promise you, Mum."},
		{"领养是好事，可是养猫要花很多时间照顾它。你能一直照顾它吗？", "你能一直照顾它吗？"},
		{strings.Repeat("长", 40) + "。", ""},
	} {
		if got := leadTail(c.in); got != c.want {
			t.Errorf("leadTail(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// timedWords times the characters (or words) of sentences said one after the
// other with a pause between them.
func timedWords(start, pause float64, sentences ...string) []proto.Word {
	var out []proto.Word
	at := start
	for _, s := range sentences {
		var units []string
		if cjkShare(s) >= 0.3 {
			for _, r := range s {
				if leadUnits(string(r)) != "" {
					units = append(units, string(r))
				}
			}
		} else {
			units = strings.Fields(s)
		}
		w, c := text.Count(s)
		d := text.Estimate(w, c).Seconds() / float64(len(units))
		for _, u := range units {
			out = append(out, proto.Word{Text: strings.Trim(u, ".,?!"), Start: at, End: at + d})
			at += d
		}
		at += pause
	}
	return out
}

func TestLeadCut(t *testing.T) {
	w := timedWords(0.1, 0.35, "你能照顾它吗？", "我能！")
	from, to, err := leadCut(w, "你能照顾它吗？", "你能照顾它吗？我能！")
	if err != nil || math.Abs(to-from-0.35) > 1e-6 {
		t.Fatalf("cut %v–%v %v", from, to, err)
	}
	if _, _, err := leadCut(w, "你能照顾它吗？", "你能照顾它吗？我不能！"); err != errLeadWords {
		t.Errorf("other words: %v", err)
	}
	// The line far shorter than its share at the lead-in's pace.
	short := timedWords(0.1, 0.3, "Will you look after it every day?", "Yes, I promise, every single day.")
	for i := 7; i < len(short); i++ {
		short[i].Start, short[i].End = short[6].End+0.3+float64(i-7)*0.02, short[6].End+0.3+float64(i-6)*0.02
	}
	if _, _, err := leadCut(short, "Will you look after it every day?", "Will you look after it every day? Yes, I promise, every single day."); err != errLeadLength {
		t.Errorf("short: %v", err)
	}
	// The cut falls in the quietest part between them.
	b := dsptest.Concat(dsptest.Sine(24000, 1, 220, -12, 1, 0), dsptest.Silence(24000, 1, 0.2), dsptest.Sine(24000, 1, 220, -12, 1, 0))
	if at := float64(leadSplit(b, 1.05, 1.15)) / 24000; at < 1.0 || at > 1.2 {
		t.Errorf("split at %.3f", at)
	}
}

// fakeAnalysis is the engine's stt.align (the fake synth's timing) and
// stt.transcribe (heard).
type fakeAnalysis struct {
	mu     sync.Mutex
	calls  []string
	heard  func(audio string) string
	pauses float64
}

func (f *fakeAnalysis) executor(method string) jobs.Executor {
	return jobs.ExecutorFunc(func(ctx context.Context, job jobs.Job, _ func(jobs.Progress), _ func(json.RawMessage)) (json.RawMessage, error) {
		f.mu.Lock()
		f.calls = append(f.calls, method)
		f.mu.Unlock()
		switch method {
		case proto.MethodSTTAlign:
			var p proto.AlignParams
			json.Unmarshal(job.Params, &p)
			if p.Model != "m/aligner" || p.ModelPath != "/models/m/aligner" {
				return nil, &jobs.Error{Code: proto.CodeBadRequest, Message: "model " + p.Model}
			}
			return json.Marshal(proto.AlignResult{Words: timedWords(0.1, 0.35, fakeSentences(p.Text)...)})
		case proto.MethodSTTTranscribe:
			var p proto.TranscribeParams
			json.Unmarshal(job.Params, &p)
			return json.Marshal(proto.TranscribeResult{Text: f.heard(p.Audio)})
		}
		return nil, &jobs.Error{Code: proto.CodeBadRequest, Message: method}
	})
}

func (f *fakeAnalysis) count(method string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		if c == method {
			n++
		}
	}
	return n
}

// A character's line is said after the end of their previous line and
// cut from it; a cut the recognizer does not hear is made as usual, and
// both are remembered.
func TestLeadInRender(t *testing.T) {
	e := newLongformEnv(t, t.TempDir(), false)
	defer e.close()
	e.synth.pauses = true
	fa := &fakeAnalysis{heard: func(audio string) string { return "Yes, I promise." }}
	r := e.svc.Longform.render
	r.engine, r.aligner, r.recognizer = fa.executor, "m/aligner", "m/asr"
	spec := LongformSpec{Kind: core.ProjectStory, Title: "Kitten", Model: "m/kokoro", Lang: "en", Seed: 7,
		Voices: map[string]core.Take{"kid": {Voice: "af_bella"}, "mom": {Voice: "af_heart"}},
		Chapters: []ChapterSpec{{Lines: []LineSpec{
			{ID: "a", Character: "Kid", Spans: []SpanSpec{{Voice: "kid", Character: "Kid", Text: "Can we get one today?"}}},
			{ID: "b", Character: "Mom", Spans: []SpanSpec{{Voice: "mom", Character: "Mom", Text: "Will you look after it every day?"}}},
			{ID: "c", Character: "Kid", Spans: []SpanSpec{{Voice: "kid", Character: "Kid", Text: "Yes, I promise."}}},
		}}},
		Production: DefaultStoryProduction(), Audition: true}
	spec.Production.LeadIn = true
	run := func() ChapterResult {
		t.Helper()
		rr, err := e.svc.Longform.Start(ctx, spec)
		if err != nil {
			t.Fatal(err)
		}
		_, body := e.wait(t, rr.ID)
		if st := body.Chapters[0].State; st != "done" && st != "cached" {
			t.Fatalf("chapter %+v", body.Chapters[0])
		}
		res, err := ReadChapter(body.Chapters[0].File)
		if err != nil {
			t.Fatal(err)
		}
		return res
	}
	res := run()
	if got := strings.Join(e.synth.texts(), " | "); got != "Can we get one today? | Will you look after it every day? | Can we get one today? Yes, I promise." {
		t.Fatalf("calls %q", got)
	}
	if res.LeadIns != 1 || res.LeadInsFailed != 0 || fa.count(proto.MethodSTTAlign) != 1 || fa.count(proto.MethodSTTTranscribe) != 1 {
		t.Fatalf("result %+v, calls %v", res, fa.calls)
	}
	// Line c is only "Yes, I promise.": its estimate, not the lead-in's.
	w, c := text.Count("Yes, I promise.")
	if d := res.Lines[2].End - res.Lines[2].Start; math.Abs(d-text.Estimate(w, c).Seconds()) > 0.12 {
		t.Errorf("line c lasts %.3f s", d)
	}
	buf, _, _ := wav.ReadFile(res.Output)
	if buf.Frames() == 0 {
		t.Fatal("no audio")
	}

	// Again: the cut is cached, nothing is aligned again.
	e.synth.reset()
	os.Remove(res.Output)
	res = run()
	if len(e.synth.texts()) != 0 || fa.count(proto.MethodSTTAlign) != 1 || res.LeadIns != 1 {
		t.Errorf("again: calls %q, %+v", e.synth.texts(), res)
	}

	// The recognizer hears something else: the line is made as usual.
	fa.heard = func(string) string { return "Can we get one today? Yes." }
	e.synth.reset()
	os.RemoveAll(filepath.Join(e.dir, "cache", "longform"))
	res = run()
	if res.LeadIns != 0 || res.LeadInsFailed != 1 {
		t.Fatalf("failed cut %+v", res)
	}
	if got := strings.Join(e.synth.texts(), " | "); !strings.HasSuffix(got, "Can we get one today? Yes, I promise. | Yes, I promise.") {
		t.Errorf("calls %q", got)
	}
	// The failure is remembered.
	aligned := fa.count(proto.MethodSTTAlign)
	e.synth.reset()
	os.Remove(res.Output)
	res = run()
	if fa.count(proto.MethodSTTAlign) != aligned || res.LeadInsFailed != 1 {
		t.Errorf("failure retried: %+v", res)
	}

	// Without an aligner, or with LeadIn off, lines are made as usual.
	r.engine = nil
	e.synth.reset()
	os.RemoveAll(filepath.Join(e.dir, "cache", "longform"))
	if res = run(); res.LeadIns+res.LeadInsFailed != 0 || len(e.synth.texts()) != 3 {
		t.Errorf("no aligner: %+v %q", res, e.synth.texts())
	}
	spec.Production.LeadIn = false
	e.synth.reset()
	os.RemoveAll(filepath.Join(e.dir, "cache", "longform"))
	if res = run(); res.LeadIns+res.LeadInsFailed != 0 || len(e.synth.texts()) != 3 {
		t.Errorf("off: %+v %q", res, e.synth.texts())
	}
}
