package services

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hehehai/AudioSmith/internal/core"
	"github.com/hehehai/AudioSmith/internal/dsp"
	"github.com/hehehai/AudioSmith/internal/dsp/wav"
	"github.com/hehehai/AudioSmith/internal/engine/proto"
	"github.com/hehehai/AudioSmith/internal/jobs"
	"github.com/hehehai/AudioSmith/internal/store/sqlitestore"
)

func words(spec string) []proto.Word {
	// "hello@0-0.5 there@0.6-1"
	var out []proto.Word
	for _, f := range strings.Fields(spec) {
		var w proto.Word
		name, times, _ := strings.Cut(f, "@")
		a, b, _ := strings.Cut(times, "-")
		w.Text = name
		w.Start, w.End = atof(a), atof(b)
		out = append(out, w)
	}
	return out
}

func atof(s string) float64 {
	var f float64
	json.Unmarshal([]byte(s), &f)
	return f
}

func TestBuildSegmentsEnglish(t *testing.T) {
	segs := []proto.Segment{{Start: 0, End: 30, Text: " Hello there. General Kenobi! You are a bold one."}}
	ws := words("hello@0-0.4 there@0.5-0.9 general@1.2-1.6 kenobi@1.7-2.2 you@4-4.1 are@4.1-4.2 a@4.25-4.3 bold@4.3-4.6 one@4.6-5")
	got := BuildSegments(segs, ws, 30)
	if len(got) != 3 {
		t.Fatalf("%d segments: %+v", len(got), got)
	}
	if got[0].Text != "Hello there." || got[0].Start != 0 || got[0].End != 0.9 || len(got[0].Words) != 2 || got[0].Words[1].Text != "there." {
		t.Errorf("first %+v", got[0])
	}
	if got[1].Text != "General Kenobi!" || got[1].Start != 1.2 || got[1].End != 2.2 {
		t.Errorf("second %+v", got[1])
	}
	if got[2].Text != "You are a bold one." || got[2].Start != 4 || got[2].End != 5 {
		t.Errorf("third %+v", got[2])
	}
	tr := core.Transcript{Segments: got}
	if tr.Text() != "Hello there. General Kenobi! You are a bold one." {
		t.Errorf("text %q", tr.Text())
	}
}

func TestBuildSegmentsChinese(t *testing.T) {
	segs := []proto.Segment{{Start: 0, End: 5, Text: "你好，世界。今天天气很好"}}
	var ws []proto.Word
	for i, r := range []rune("你好世界今天天气很好") {
		ws = append(ws, proto.Word{Text: string(r), Start: float64(i) * 0.3, End: float64(i)*0.3 + 0.25})
	}
	got := BuildSegments(segs, ws, 0)
	if len(got) != 2 || got[0].Text != "你好，世界。" || got[1].Text != "今天天气很好" || len(got[0].Words) != 4 || got[0].Words[1].Text != "好，" {
		t.Fatalf("%+v", got)
	}
	if math.Abs(got[1].Start-1.2) > 1e-9 || math.Abs(got[1].End-2.95) > 1e-9 {
		t.Errorf("times %+v", got[1])
	}
}

func TestBuildSegmentsUnmatched(t *testing.T) {
	// The aligner skipped "um" and spelled "3.5" as two words; a pause cuts.
	segs := []proto.Segment{{Start: 0, End: 10, Text: "It costs um 3.5 dollars and that is it"}}
	ws := words("it@0-0.2 costs@0.3-0.6 3@1-1.1 5@1.2-1.4 dollars@1.5-2 and@5-5.2 that@5.3-5.5 is@5.6-5.7 it@5.8-6")
	got := BuildSegments(segs, ws, 10)
	if len(got) != 2 || got[0].Text != "It costs um 3.5 dollars" || got[1].Text != "and that is it" {
		t.Fatalf("%+v", got)
	}
	um := got[0].Words[2]
	if um.Text != "um" || um.Start < 0.6 || um.End > 1 || um.Start >= um.End {
		t.Errorf("um %+v", um)
	}
	if w := got[0].Words[3]; w.Text != "3.5" || w.Start != 1 || w.End != 1.4 {
		t.Errorf("3.5 %+v", w)
	}
}

func TestBuildSegmentsWithoutWords(t *testing.T) {
	segs := []proto.Segment{{Start: 0, End: 30, Text: " One. "}, {Start: 30, End: 42, Text: "  "}, {Start: 30, End: 50, Text: "Two."}}
	got := BuildSegments(segs, nil, 45)
	if len(got) != 2 || got[0].Text != "One." || got[1].End != 45 {
		t.Fatalf("%+v", got)
	}
}

func TestTokenizePunctuation(t *testing.T) {
	toks := tokenize("¿Qué tal? — bien, «gracias».")
	var texts []string
	for _, x := range toks {
		texts = append(texts, x.text)
	}
	if strings.Join(texts, "|") != "¿Qué|tal?|—bien,|«gracias»." {
		t.Errorf("%q", texts)
	}
}

// fakeEngineCalls answers the transcript job's engine calls.
type fakeEngineCalls struct {
	mu       sync.Mutex
	calls    []string
	params   map[string]json.RawMessage
	text     string
	segments []proto.Segment
	align    []proto.Word
	alignErr *jobs.Error
}

func (f *fakeEngineCalls) executor(method string) jobs.Executor {
	return jobs.ExecutorFunc(func(ctx context.Context, job jobs.Job, report func(jobs.Progress), _ func(json.RawMessage)) (json.RawMessage, error) {
		f.mu.Lock()
		f.calls = append(f.calls, method)
		f.params[method] = job.Params
		f.mu.Unlock()
		switch method {
		case proto.MethodMediaDecode:
			var p proto.DecodeParams
			json.Unmarshal(job.Params, &p)
			b := dsp.Buffer{Rate: p.SampleRate, Channels: 1, Samples: make([]float32, p.SampleRate*2)}
			wav.WriteFile(p.Output.Path, b, wav.PCM16, wav.Options{})
			return json.Marshal(proto.DecodeResult{AudioOut: proto.AudioOut{Output: p.Output.Path, SampleRate: p.SampleRate, Channels: 1, AudioSeconds: 2}})
		case proto.MethodSTTTranscribe:
			report(jobs.Progress{Stage: "transcribing", Fraction: 0.5})
			return json.Marshal(proto.TranscribeResult{Model: "m", Text: f.text, Language: "en", Segments: f.segments, AudioSeconds: 2, Seconds: 0.2})
		case proto.MethodSTTAlign:
			if f.alignErr != nil {
				return nil, f.alignErr
			}
			return json.Marshal(proto.AlignResult{Model: "a", Words: f.align, AudioSeconds: 2})
		}
		return nil, errors.New("unexpected " + method)
	})
}

func newTranscriptTest(t *testing.T) (*Transcripts, *fakeEngineCalls, *fakeScheduler, *[]core.TranscriptChanged) {
	t.Helper()
	st, err := sqlitestore.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	bus := core.NewBus()
	var evs []core.TranscriptChanged
	var mu sync.Mutex
	core.On(bus, func(ev core.TranscriptChanged) { mu.Lock(); evs = append(evs, ev); mu.Unlock() })
	eng := &fakeEngineCalls{params: map[string]json.RawMessage{}, text: "Hello there.", segments: []proto.Segment{{Start: 0, End: 2, Text: "Hello there."}},
		align: words("hello@0.1-0.5 there@0.6-1.1")}
	sched := &fakeScheduler{bus: bus, jobs: map[string]jobs.Job{}}
	svc := NewTranscripts(TranscriptsConfig{Store: st.Transcripts(), Bus: bus, Scheduler: sched, Engine: eng.executor,
		ModelPath: func(_ context.Context, repo string) (string, error) {
			if repo == "missing" {
				return "", core.ErrNotDownloaded
			}
			return "/snap/" + repo, nil
		}})
	return svc, eng, sched, &evs
}

func writeWAV(t *testing.T, path string, rate int, seconds float64) {
	t.Helper()
	b := dsp.Buffer{Rate: rate, Channels: 2, Samples: make([]float32, int(float64(rate)*seconds)*2)}
	if err := wav.WriteFile(path, b, wav.PCM16, wav.Options{}); err != nil {
		t.Fatal(err)
	}
}

func runJob(t *testing.T, svc *Transcripts, sched *fakeScheduler, id string) (TranscribeResult, error) {
	t.Helper()
	j, _ := sched.Get(ctx, id)
	var stages []string
	raw, err := svc.Executor().Run(ctx, j, func(p jobs.Progress) { stages = append(stages, p.Stage) }, nil)
	if err != nil {
		return TranscribeResult{}, err
	}
	var res TranscribeResult
	if err := json.Unmarshal(raw, &res); err != nil {
		t.Fatal(err)
	}
	return res, nil
}

func TestTranscribeJob(t *testing.T) {
	svc, eng, sched, evs := newTranscriptTest(t)
	dir := t.TempDir()
	in := filepath.Join(dir, "rec.wav")
	writeWAV(t, in, 48000, 1.5)
	req, id, err := svc.Transcribe(ctx, TranscribeRequest{Input: in, Source: core.SourceRecording, RemoveInput: true, Mode: "accurate",
		Model: "stt", Aligner: "align", Language: "", Prompt: "Kenobi"})
	if err != nil || req.ID == "" {
		t.Fatal(err)
	}
	j, _ := sched.Get(ctx, id)
	if j.Kind != KindTranscribe || j.Lane != jobs.LaneGPU || j.Owner != TranscriptOwner+req.ID || !j.Idempotent {
		t.Fatalf("job %+v", j)
	}
	res, err := runJob(t, svc, sched, id)
	if err != nil {
		t.Fatal(err)
	}
	tr := res.Transcript
	if tr.ID != req.ID || tr.Text() != "Hello there." || tr.Aligner != "align" || tr.Language != "en" || tr.Duration != 2 ||
		len(tr.Segments) != 1 || len(tr.Segments[0].Words) != 2 || tr.Segments[0].Start != 0.1 || tr.Raw != "Hello there." {
		t.Fatalf("transcript %+v", tr)
	}
	// A WAV is decoded in Go: no media.decode; the audio is 16 kHz mono.
	if strings.Join(eng.calls, ",") != "stt.transcribe,stt.align" {
		t.Errorf("calls %v", eng.calls)
	}
	var tp proto.TranscribeParams
	json.Unmarshal(eng.params[proto.MethodSTTTranscribe], &tp)
	if tp.ModelPath != "/snap/stt" || tp.Prompt != "Kenobi" || tp.Audio != svc.AudioPath(tr) {
		t.Errorf("params %+v", tp)
	}
	b, _, err := wav.ReadFile(svc.AudioPath(tr))
	if err != nil || b.Rate != TranscriptRate || b.Channels != 1 {
		t.Errorf("audio %v %d %d", err, b.Rate, b.Channels)
	}
	if _, err := os.Stat(in); !os.IsNotExist(err) {
		t.Error("recording kept")
	}
	saved, err := svc.Get(ctx, req.ID)
	if err != nil || len(saved.Segments[0].Words) != 2 || len(*evs) != 1 {
		t.Fatalf("saved %+v %v %d", saved, err, len(*evs))
	}

	// Edits.
	up, err := svc.Update(ctx, req.ID, func(t *core.Transcript) { t.Clean = "Hi." })
	if err != nil || up.Preferred() != "Hi." || len(*evs) != 2 {
		t.Fatalf("update %+v %v", up, err)
	}
	l, _ := svc.List(ctx, core.TranscriptQuery{Search: "hello"})
	if len(l) != 1 {
		t.Errorf("list %d", len(l))
	}
	if err := svc.Delete(ctx, req.ID); err != nil || !(*evs)[2].Deleted {
		t.Fatal(err)
	}
	if _, err := os.Stat(svc.AudioPath(tr)); !os.IsNotExist(err) {
		t.Error("audio kept after delete")
	}
}

func TestTranscribeVideoAndAlignFailure(t *testing.T) {
	svc, eng, sched, _ := newTranscriptTest(t)
	eng.alignErr = &jobs.Error{Code: proto.CodeOOM, Message: "out of memory"}
	in := filepath.Join(t.TempDir(), "talk.mov")
	os.WriteFile(in, []byte("moov"), 0o644)
	req, id, err := svc.Transcribe(ctx, TranscribeRequest{Input: in, Title: "talk.mov", Mode: "accurate", Model: "stt", Aligner: "align"})
	if err != nil {
		t.Fatal(err)
	}
	res, err := runJob(t, svc, sched, id)
	if err != nil {
		t.Fatal(err)
	}
	if eng.calls[0] != proto.MethodMediaDecode || res.AlignError == nil || res.AlignError.Code != proto.CodeOOM ||
		len(res.Warnings) != 1 || res.Transcript.Aligner != "" || res.Transcript.SourcePath != in || res.Transcript.Source != core.SourceFile {
		t.Fatalf("%v %+v", eng.calls, res)
	}
	if _, err := os.Stat(in); err != nil {
		t.Error("the user's file was removed")
	}
	_ = req
}

func TestTranscribeRefusals(t *testing.T) {
	svc, eng, sched, _ := newTranscriptTest(t)
	if _, _, err := svc.Transcribe(ctx, TranscribeRequest{Input: "/x.wav", Model: "missing"}); !errors.Is(err, core.ErrNotDownloaded) {
		t.Errorf("model: %v", err)
	}
	if _, _, err := svc.Transcribe(ctx, TranscribeRequest{Input: "/x.wav", Model: "stt", Aligner: "missing"}); !errors.Is(err, core.ErrNotDownloaded) || !strings.Contains(err.Error(), "aligner") {
		t.Errorf("aligner: %v", err)
	}
	if _, _, err := svc.Transcribe(ctx, TranscribeRequest{Model: "stt"}); !errors.Is(err, core.ErrInvalid) {
		t.Errorf("no input: %v", err)
	}
	// A missing input fails the job with file_not_found.
	_, id, _ := svc.Transcribe(ctx, TranscribeRequest{Input: "/nope/x.wav", Model: "stt"})
	_, err := runJob(t, svc, sched, id)
	var je *jobs.Error
	if !errors.As(err, &je) || je.Code != proto.CodeFileNotFound {
		t.Errorf("missing input: %v", err)
	}
	// No engine: retryable.
	svc.engine = func(string) jobs.Executor { return nil }
	in := filepath.Join(t.TempDir(), "a.wav")
	writeWAV(t, in, 16000, 0.5)
	_, id, _ = svc.Transcribe(ctx, TranscribeRequest{Input: in, Model: "stt"})
	_, err = runJob(t, svc, sched, id)
	if !errors.As(err, &je) || je.Code != CodeEngineUnavailable || !je.Retryable {
		t.Errorf("no engine: %v", err)
	}
	_ = eng
}

func sampleTranscript() core.Transcript {
	at := time.Date(2026, 10, 13, 9, 30, 0, 0, time.UTC)
	return core.Transcript{ID: "t1", CreatedAt: at, Title: "talk.m4a", Language: "en", Duration: 9, Model: "m",
		Speakers: map[string]string{"S1": "Ana"},
		Segments: []core.TranscriptSegment{
			{Start: 0.5, End: 2, Text: "Hello there.", Speaker: "S1", Words: []core.TranscriptWord{{Text: "Hello", Start: 0.5, End: 1}, {Text: "there.", Start: 1.1, End: 2}}},
			{Start: 3, End: 6, Text: "Edited line.", Edited: true, Words: []core.TranscriptWord{{Text: "old", Start: 3, End: 6}}},
		}}
}

func TestExport(t *testing.T) {
	tr := sampleTranscript()
	txt, _ := Export(tr, ExportTXT)
	if string(txt) != "Hello there. Edited line.\n" {
		t.Errorf("txt %q", txt)
	}
	tr.Clean = "Hi. Edited."
	if txt, _ := Export(tr, ExportTXT); string(txt) != "Hi. Edited.\n" {
		t.Errorf("clean txt %q", txt)
	}
	srt, err := Export(tr, ExportSRT)
	if err != nil || !strings.HasPrefix(string(srt), "1\n00:00:00,500 --> ") || !strings.Contains(string(srt), "Edited line.") {
		t.Errorf("srt %q %v", srt, err)
	}
	vtt, _ := Export(tr, ExportVTT)
	if !strings.HasPrefix(string(vtt), "WEBVTT") || !strings.Contains(string(vtt), "<v Ana>Hello there.") {
		t.Errorf("vtt %q", vtt)
	}
	js, _ := Export(tr, ExportJSON)
	var back struct {
		Text     string `json:"text"`
		Clean    string `json:"clean"`
		Segments []struct {
			Speaker string `json:"speaker"`
			Words   []struct{ Text string }
		}
	}
	if err := json.Unmarshal(js, &back); err != nil || back.Text != "Hello there. Edited line." || back.Clean != "Hi. Edited." ||
		back.Segments[0].Speaker != "Ana" || len(back.Segments[0].Words) != 2 || back.Segments[1].Words != nil {
		t.Errorf("json %s %v", js, err)
	}
	if _, err := Export(tr, "doc"); err == nil {
		t.Error("unknown format")
	}
	all := ExportAll([]core.Transcript{tr, {Segments: []core.TranscriptSegment{{Text: "Two"}}}}, func(time.Time) string { return "D" })
	if string(all) != "[D] (en) talk.m4a\nHi. Edited.\n\n---\n\n[D]\nTwo\n" {
		t.Errorf("all %q", all)
	}
}
