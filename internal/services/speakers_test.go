package services

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/hehehai/AudioSmith/internal/dsp"
	"github.com/hehehai/AudioSmith/internal/dsp/wav"
	"github.com/hehehai/AudioSmith/internal/engine/proto"
)

func TestSpeakerWindows(t *testing.T) {
	if w := speakerWindows(300, 600, 60); len(w) != 1 || w[0] != (speakerWindow{0, 300}) {
		t.Errorf("short: %v", w)
	}
	if w := speakerWindows(600, 600, 60); len(w) != 1 {
		t.Errorf("10 minutes: %v", w)
	}
	w := speakerWindows(1500, 600, 60)
	want := []speakerWindow{{0, 540}, {480, 1020}, {960, 1500}}
	if !slices.Equal(w, want) {
		t.Errorf("25 minutes: %v", w)
	}
	for _, secs := range []float64{601, 1200, 3600, 7322.5} {
		w := speakerWindows(secs, 600, 60)
		if w[0].start != 0 || w[len(w)-1].end != secs {
			t.Errorf("%v: %v", secs, w)
		}
		for i, x := range w {
			if x.end-x.start > 600+1e-9 {
				t.Errorf("%v: window %d is %.1f s", secs, i, x.end-x.start)
			}
			if i > 0 && math.Abs(w[i-1].end-x.start-60) > 1e-9 {
				t.Errorf("%v: overlap %d is %.1f s", secs, i, w[i-1].end-x.start)
			}
		}
	}
}

// turn is who says what when, in file time.
type turn struct {
	start, end float64
	who        string
	text       string
}

// speakersAudio writes a 16 kHz WAV of seconds with a tone in every turn
// (speech-level), silence elsewhere.
func speakersAudio(t *testing.T, seconds float64, turns []turn) string {
	t.Helper()
	b := dsp.New(16000, 1, int(seconds*16000))
	for i := range b.Samples {
		x := float64(i) / 16000
		v := 0.0005 * math.Sin(2*math.Pi*50*x)
		for _, tu := range turns {
			if x >= tu.start && x < tu.end {
				v = 0.3 * math.Sin(2*math.Pi*180*x)
			}
		}
		b.Samples[i] = float32(v)
	}
	path := filepath.Join(t.TempDir(), "speech.wav")
	if err := wav.WriteFile(path, b, wav.PCM16, wav.Options{}); err != nil {
		t.Fatal(err)
	}
	return path
}

// fakeMoss answers like MOSS-Transcribe-Diarize over the windows of
// seconds: the turns whose middle falls in window k, in window time,
// labelled S01, S02… in order of appearance in the window (as the model
// numbers them per pass), plus extra segments.
func fakeMoss(seconds float64, turns []turn, extra func(k int) []proto.Segment, calls *[]int) func(k, n int, p *proto.TranscribeParams, out *proto.TranscribeResult) error {
	return func(k, n int, p *proto.TranscribeParams, out *proto.TranscribeResult) error {
		*calls = append(*calls, k)
		w := speakerWindows(seconds, speakersWindow, speakersOverlap)[k]
		labels := map[string]string{}
		var texts []string
		for _, tu := range turns {
			if mid := (tu.start + tu.end) / 2; mid < w.start || mid >= w.end {
				continue
			}
			if labels[tu.who] == "" {
				labels[tu.who] = fmt.Sprintf("S%02d", len(labels)+1)
			}
			out.Segments = append(out.Segments, proto.Segment{Start: tu.start - w.start, End: tu.end - w.start, Text: tu.text, Speaker: labels[tu.who]})
			texts = append(texts, tu.text)
		}
		if extra != nil {
			out.Segments = append(out.Segments, extra(k)...)
		}
		out.Model, out.Language, out.Text, out.Seconds = p.Model, "en", strings.Join(texts, " "), 1
		return nil
	}
}

// TestTranscribeSpeakersLinksWindows: three people over a long recording
// (windows of 20 s overlapping by 6 s here; 10 minutes by 1 in the app)
// keep one label each through every window, though each window numbers
// them in its own order; the overlap is transcribed once.
func TestTranscribeSpeakersLinksWindows(t *testing.T) {
	speakersWindow, speakersOverlap = 20, 6
	defer func() { speakersWindow, speakersOverlap = SpeakersWindowSeconds, SpeakersOverlapSeconds }()
	var turns []turn
	who := []string{"ana", "bo", "ana", "cy", "bo", "cy", "ana", "bo", "cy", "cy", "bo", "ana", "bo", "ana", "cy"}
	at := 0.5
	for i, w := range who {
		turns = append(turns, turn{at, at + 2.4, w, fmt.Sprintf("line %d", i)})
		at += 3.1
	}
	seconds := at + 0.5
	path := speakersAudio(t, seconds, turns)
	var calls []int
	res, err := TranscribeSpeakers(context.Background(), fakeMoss(seconds, turns, nil, &calls),
		proto.TranscribeParams{Model: "vanch007/mlx-MOSS-Transcribe-Diarize-8bit", Audio: path}, seconds)
	if err != nil {
		t.Fatal(err)
	}
	if len(calls) < 3 {
		t.Fatalf("%d windows for %.0f s", len(calls), seconds)
	}
	if len(res.Segments) != len(turns) {
		t.Fatalf("%d segments, want %d: %+v", len(res.Segments), len(turns), res.Segments)
	}
	names := map[string]string{} // person → label
	for i, s := range res.Segments {
		tu := turns[i]
		if s.Text != tu.text || math.Abs(s.Start-tu.start) > 0.01 {
			t.Fatalf("segment %d: %+v, want %+v", i, s, tu)
		}
		if names[tu.who] == "" {
			names[tu.who] = s.Speaker
		}
		if s.Speaker != names[tu.who] {
			t.Errorf("segment %d (%s): %s, earlier %s", i, tu.who, s.Speaker, names[tu.who])
		}
	}
	if names["ana"] != "S1" || names["bo"] != "S2" || names["cy"] != "S3" {
		t.Errorf("labels %v", names)
	}
	if res.Language != "en" || !strings.HasPrefix(res.Text, "line 0 line 1") || math.Abs(res.AudioSeconds-seconds) > 0.01 {
		t.Errorf("result %q %q %v", res.Language, res.Text, res.AudioSeconds)
	}
	// The window files are gone.
	if m, _ := filepath.Glob(strings.TrimSuffix(path, ".wav") + ".speakers*"); len(m) != 0 {
		t.Errorf("left %v", m)
	}
}

// TestTranscribeSpeakersSilenceAndReplies: a window without speech is not
// sent; the chat answers MOSS gives silence, a copy of the vocabulary and
// untagged text between windows are dropped.
func TestTranscribeSpeakersSilenceAndReplies(t *testing.T) {
	speakersWindow, speakersOverlap = 20, 6
	defer func() { speakersWindow, speakersOverlap = SpeakersWindowSeconds, SpeakersOverlapSeconds }()
	// Speech, 30 s of silence (a window of its own), speech.
	turns := []turn{{1, 4, "ana", "Hello there."}, {5, 8, "bo", "Hi."}, {44, 47, "bo", "Back again."}, {48, 51, "ana", "Welcome back."}}
	seconds := 52.0
	path := speakersAudio(t, seconds, turns)
	extra := func(k int) []proto.Segment {
		return []proto.Segment{
			{Start: 9, End: 9.5, Text: "I'm sorry, I can't assist with that request.", Speaker: "S01"},
			{Start: 9.5, End: 10, Text: "AudioSmith, MLX", Speaker: "S02"},
			{Start: 10, End: 11, Text: "stray words"},
		}
	}
	var calls []int
	res, err := TranscribeSpeakers(context.Background(), fakeMoss(seconds, turns, extra, &calls),
		proto.TranscribeParams{Model: "moss-transcribe-diarize-8bit", Audio: path, Prompt: "AudioSmith, MLX"}, seconds)
	if err != nil {
		t.Fatal(err)
	}
	wins := speakerWindows(seconds, 20, 6)
	if len(wins) != 4 || !slices.Equal(calls, []int{0, 3}) { // windows 1 and 2 are silent
		t.Errorf("windows %v, called %v", wins, calls)
	}
	var texts, who []string
	for _, s := range res.Segments {
		texts, who = append(texts, s.Text), append(who, s.Speaker)
	}
	if !slices.Equal(texts, []string{"Hello there.", "Hi.", "Back again.", "Welcome back."}) {
		t.Errorf("texts %q", texts)
	}
	// Across the silence nothing links the windows: by elimination only
	// when one speaker is left on each side, so here they may get new
	// labels, but never a wrong one within a window.
	if who[0] != "S1" || who[1] != "S2" || who[2] == who[3] {
		t.Errorf("speakers %v", who)
	}
}

// TestTranscribeSpeakersShort: audio of up to 10 minutes goes in one call
// (the file itself); silence makes no call at all.
func TestTranscribeSpeakersShort(t *testing.T) {
	turns := []turn{{0.5, 2, "ana", "你好。"}, {2.5, 4, "bo", "你好，很高兴见到你。"}}
	path := speakersAudio(t, 5, turns)
	var got []string
	call := func(k, n int, p *proto.TranscribeParams, out *proto.TranscribeResult) error {
		got = append(got, p.Audio)
		out.Language = "zh"
		out.Segments = []proto.Segment{{Start: 0.5, End: 2, Text: "你好。", Speaker: "S01"}, {Start: 2.5, End: 4, Text: "你好，很高兴见到你。", Speaker: "S02"},
			{Start: 4.2, End: 5, Text: "I'm sorry, I can't assist with that request."}}
		return nil
	}
	res, err := TranscribeSpeakers(context.Background(), call, proto.TranscribeParams{Model: "m", Audio: path}, 5)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got, []string{path}) || res.Text != "你好。你好，很高兴见到你。" || len(res.Segments) != 2 || res.Segments[1].Speaker != "S2" || res.Language != "zh" {
		t.Errorf("calls %v, result %+v", got, res)
	}
	silent := speakersAudio(t, 5, nil)
	got = nil
	res, err = TranscribeSpeakers(context.Background(), call, proto.TranscribeParams{Model: "m", Audio: silent, Language: "en"}, 5)
	if err != nil || len(got) != 0 || len(res.Segments) != 0 || res.Language != "en" {
		t.Errorf("silence: calls %v, %+v, %v", got, res, err)
	}
	// A file Go cannot read goes to the engine whole.
	got = nil
	if _, err := TranscribeSpeakers(context.Background(), call, proto.TranscribeParams{Model: "m", Audio: path + ".m4a"}, 900); err != nil || len(got) != 1 {
		t.Errorf("other format: %v, %v", got, err)
	}
}

func TestPromptEchoAndReplies(t *testing.T) {
	terms := hotwords("AudioSmith、MLX\nAPI latency; MLX")
	if !slices.Equal(terms, []string{"audiosmith", "mlx", "api latency"}) {
		t.Fatalf("terms %q", terms)
	}
	for text, want := range map[string]bool{
		"AudioSmith, MLX":             true,
		"audiosmith、api latency、MLX。": true,
		"AudioSmith":                  false, // one term may be said
		"AudioSmith runs MLX":         false,
		"We use AudioSmith, MLX.":     false,
	} {
		if isPromptEcho(text, terms) != want {
			t.Errorf("echo %q: %v", text, !want)
		}
	}
	for text, want := range map[string]bool{
		"I'm sorry, I can't assist with that request.": true,
		"I cannot assist with this.":                   true,
		"热词提示：AudioSmith":                              true,
		"I can assist you tomorrow.":                   false,
		"今天天气很好。":                                      false,
	} {
		if isAssistantReply(text) != want {
			t.Errorf("reply %q: %v", text, !want)
		}
	}
	if !IsSpeakersModel("vanch007/mlx-MOSS-Transcribe-Diarize-8bit") || !IsSpeakersModel("moss-transcribe-diarize-8bit") || IsSpeakersModel("mlx-community/Qwen3-ASR-0.6B-4bit") {
		t.Error("IsSpeakersModel")
	}
}

func TestLinkLabelsByElimination(t *testing.T) {
	prev := &linkedWindow{end: 100, labels: map[string]string{"S01": "1", "S02": "2"},
		segments: []proto.Segment{{Start: 90, End: 99, Speaker: "S01"}}}
	// S01 here speaks with the previous S01 in the overlap; S02 here was
	// silent there: the one left on each side.
	own := []proto.Segment{{Start: 91, End: 98, Speaker: "S02"}, {Start: 120, End: 125, Speaker: "S01"}}
	got := linkLabels(prev, own, 90)
	if got["S02"] != "1" || got["S01"] != "2" {
		t.Errorf("labels %v", got)
	}
	// Two left on a side: no guess.
	prev.labels["S03"] = "3"
	got = linkLabels(prev, own, 90)
	if got["S02"] != "1" || got["S01"] != "" {
		t.Errorf("labels %v", got)
	}
}

// TestBuildSegmentsSpeakers: model segments keep their speaker; with
// timed words, a change of speaker ends a sentence even without a stop.
func TestBuildSegmentsSpeakers(t *testing.T) {
	segs := []proto.Segment{{Start: 0, End: 2, Text: "你好 我们开始吧", Speaker: "S1"}, {Start: 2.1, End: 3, Text: "好的", Speaker: "S2"}}
	out := BuildSegments(segs, nil, 3)
	if len(out) != 2 || out[0].Speaker != "S1" || out[1].Speaker != "S2" {
		t.Fatalf("without words %+v", out)
	}
	words := []proto.Word{{Text: "你", Start: 0, End: 0.2}, {Text: "好", Start: 0.2, End: 0.4}, {Text: "我", Start: 0.6, End: 0.8}, {Text: "们", Start: 0.8, End: 1},
		{Text: "开", Start: 1, End: 1.2}, {Text: "始", Start: 1.2, End: 1.4}, {Text: "吧", Start: 1.4, End: 1.9}, {Text: "好", Start: 2.1, End: 2.4}, {Text: "的", Start: 2.4, End: 2.9}}
	out = BuildSegments(segs, words, 3)
	if len(out) != 2 || out[0].Text != "你好我们开始吧" || out[0].Speaker != "S1" || out[1].Text != "好的" || out[1].Speaker != "S2" || len(out[1].Words) != 2 {
		t.Errorf("with words %+v", out)
	}
	// Two sentences of one speaker stay two segments of that speaker.
	segs = []proto.Segment{{Start: 0, End: 4, Text: "One. Two.", Speaker: "S2"}}
	words = []proto.Word{{Text: "One", Start: 0, End: 1}, {Text: "Two", Start: 2, End: 3}}
	if out = BuildSegments(segs, words, 4); len(out) != 2 || out[0].Speaker != "S2" || out[1].Speaker != "S2" {
		t.Errorf("one speaker %+v", out)
	}
}

// TestTranscribeJobSpeakers: a transcription with the speakers model
// keeps who says what (S1, S2…) through word alignment, drops the
// model's chat answers, and skips the engine for silence.
func TestTranscribeJobSpeakers(t *testing.T) {
	svc, eng, sched, _ := newTranscriptTest(t)
	in := speakersAudio(t, 4, []turn{{0.2, 1.8, "a", ""}, {2.1, 3.8, "b", ""}})
	eng.text = "Are you ready? Yes."
	eng.seconds = 4
	eng.segments = []proto.Segment{{Start: 0.2, End: 1.8, Text: "Are you ready?", Speaker: "S01"}, {Start: 2.1, End: 3.8, Text: "Yes", Speaker: "S02"},
		{Start: 3.8, End: 4, Text: "I'm sorry, I can't assist with that request."}}
	eng.align = nil // even times
	_, id, err := svc.Transcribe(ctx, TranscribeRequest{Input: in, Mode: "accurate", Model: testMoss, Aligner: "align", Prompt: "Kenobi, Skywalker"})
	if err != nil {
		t.Fatal(err)
	}
	res, err := runJob(t, svc, sched, id)
	if err != nil {
		t.Fatal(err)
	}
	tr := res.Transcript
	var who, texts []string
	for _, s := range tr.Segments {
		who, texts = append(who, s.Speaker), append(texts, s.Text)
	}
	if !slices.Equal(who, []string{"S1", "S2"}) || !slices.Equal(texts, []string{"Are you ready?", "Yes"}) || tr.Model != testMoss {
		t.Errorf("transcript %+v", tr)
	}
	var tp proto.TranscribeParams
	json.Unmarshal(eng.params[proto.MethodSTTTranscribe], &tp)
	if tp.Prompt != "Kenobi, Skywalker" || tp.Audio != svc.AudioPath(tr) {
		t.Errorf("params %+v", tp)
	}
	if len(eng.aligns) != 1 || eng.aligns[0].Text != "Are you ready? Yes" {
		t.Errorf("aligns %+v", eng.aligns)
	}

	// Silence: no engine call, no segments.
	eng.calls = nil
	silent := speakersAudio(t, 3, nil)
	_, id, _ = svc.Transcribe(ctx, TranscribeRequest{Input: silent, Mode: "fast", Model: testMoss})
	res, err = runJob(t, svc, sched, id)
	if err != nil || len(res.Transcript.Segments) != 0 || len(eng.calls) != 0 {
		t.Errorf("silence: %+v, calls %v, %v", res.Transcript, eng.calls, err)
	}
}
