package services

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hehehai/AudioSmith/internal/core"
	"github.com/hehehai/AudioSmith/internal/dsp"
	"github.com/hehehai/AudioSmith/internal/dsp/wav"
	"github.com/hehehai/AudioSmith/internal/engine/proto"
)

const qwenASR = "mlx-community/Qwen3-ASR-0.6B-4bit"

// TestCleanTranscription: Qwen3-ASR's " The." (and "嗯。") after the last
// sentence of audio longer than 30 s goes, as a segment of its own or at
// the end of the last one; shorter audio, other models and real endings
// stay.
func TestCleanTranscription(t *testing.T) {
	seg := func(s ...string) []proto.Segment {
		var out []proto.Segment
		for i, x := range s {
			out = append(out, proto.Segment{Start: float64(30 * i), End: float64(30 * (i + 1)), Text: x})
		}
		return out
	}
	for _, c := range []struct {
		name, model, text string
		secs              float64
		segs              []proto.Segment
		want              string
		wantSegs          int
	}{
		{"en tail segment", qwenASR, "They should keep their phones charged. The.", 47, seg("They should keep their phones charged.", "The."), "They should keep their phones charged.", 1},
		{"zh tail segment", qwenASR, "很快就没兴趣了。 The.", 62, seg("很快就没兴趣了。", "The."), "很快就没兴趣了。", 1},
		{"zh filler", qwenASR, "增加到了一百一十三人。 嗯。", 44, seg("增加到了一百一十三人。", "嗯。"), "增加到了一百一十三人。", 1},
		{"in the last segment", qwenASR, "Keep them charged. The.", 40, seg("Hello.", "Keep them charged. The."), "Keep them charged.", 2},
		{"short audio", qwenASR, "Keep them charged. The.", 20, seg("Keep them charged. The."), "Keep them charged. The.", 1},
		{"whisper", "mlx-community/whisper-large-v3-turbo-4bit", "Keep them charged. The.", 40, seg("Keep them charged.", "The."), "Keep them charged. The.", 2},
		{"a real ending", qwenASR, "We went to the Other Place. Then the.", 40, seg("We went to the Other Place. Then the."), "We went to the Other Place. Then the.", 1},
		{"a word, not a tail", qwenASR, "Read the word: The.", 40, seg("Read the word: The."), "Read the word: The.", 1},
		{"only the tail", qwenASR, "The.", 40, seg("The."), "The.", 1},
	} {
		tr := proto.TranscribeResult{Text: c.text, Segments: c.segs, AudioSeconds: c.secs}
		changed := CleanTranscription(c.model, &tr)
		if tr.Text != c.want || len(tr.Segments) != c.wantSegs || changed != (c.want != c.text) {
			t.Errorf("%s: %q %d segments (changed %v)", c.name, tr.Text, len(tr.Segments), changed)
		}
		if n := len(tr.Segments); n > 0 && c.want != c.text && strings.HasSuffix(tr.Segments[n-1].Text, "The.") {
			t.Errorf("%s: last segment %q", c.name, tr.Segments[n-1].Text)
		}
	}
}

// TestASRPrompt: Qwen3-ASR gets the vocabulary up to 30 s of audio only;
// Whisper always.
func TestASRPrompt(t *testing.T) {
	for _, c := range []struct {
		model string
		secs  float64
		want  string
	}{
		{qwenASR, 5, "Kenobi"}, {qwenASR, 30, "Kenobi"}, {qwenASR, 30.5, ""}, {qwenASR, 600, ""}, {qwenASR, 0, ""},
		{"qwen3-asr-0.6b-4bit", 45, ""}, {"mlx-community/whisper-large-v3-turbo-4bit", 600, "Kenobi"},
	} {
		if got := ASRPrompt(c.model, "Kenobi", c.secs); got != c.want {
			t.Errorf("ASRPrompt(%s, %g) = %q", c.model, c.secs, got)
		}
	}
}

// TestAlignWindows: audio up to 270 s is aligned at once; longer audio
// in windows of whole segments of at most 240 s, cut between segments,
// covering it all.
func TestAlignWindows(t *testing.T) {
	segs := func(n int, each float64) []proto.Segment {
		var out []proto.Segment
		for i := range n {
			out = append(out, proto.Segment{Start: float64(i) * each, End: float64(i+1)*each - 0.5, Text: fmt.Sprintf("s%d", i)})
		}
		return out
	}
	if w := alignWindows(segs(9, 30), 270); w != nil {
		t.Errorf("270 s: %v", w)
	}
	w := alignWindows(segs(20, 30), 600)
	if len(w) != 3 || w[0].start != 0 || w[len(w)-1].end != 600 {
		t.Fatalf("600 s: %+v", w)
	}
	for i, x := range w {
		if x.end-x.start > AlignWindowSeconds+1 || i > 0 && x.start != w[i-1].end {
			t.Errorf("window %d: %+v", i, x)
		}
	}
	if !strings.HasPrefix(w[1].text, "s8 ") || w[1].start != 239.75 {
		t.Errorf("second window %+v", w[1])
	}
	// One segment for the whole file: nothing to cut at.
	if w := alignWindows([]proto.Segment{{Start: 0, End: 600, Text: "all"}}, 600); w != nil {
		t.Errorf("one segment: %v", w)
	}
}

// TestTranscribeLongQwenASR: a 10-minute file on Qwen3-ASR gets no
// vocabulary, loses the hallucinated " The." and is aligned in three
// windows whose words land at their own times; the window files go.
func TestTranscribeLongQwenASR(t *testing.T) {
	svc, eng, sched, _ := newTranscriptTest(t)
	const secs = 600.0
	var segs []proto.Segment
	var texts []string
	for i := range 20 {
		s := fmt.Sprintf("Sentence number %d is here.", i)
		segs, texts = append(segs, proto.Segment{Start: float64(i) * 30, End: float64(i+1) * 30, Text: s}), append(texts, s)
	}
	segs = append(segs, proto.Segment{Start: 599, End: 600, Text: "The."})
	eng.text, eng.segments, eng.align, eng.seconds = strings.Join(texts, " ")+" The.", segs, nil, secs
	in := filepath.Join(t.TempDir(), "talk.wav")
	if err := wav.WriteFile(in, dsp.Buffer{Rate: TranscriptRate, Channels: 1, Samples: make([]float32, int(secs)*TranscriptRate)}, wav.PCM16, wav.Options{}); err != nil {
		t.Fatal(err)
	}
	_, id, err := svc.Transcribe(ctx, TranscribeRequest{Input: in, Source: core.SourceFile, Mode: "accurate", Model: qwenASR, Aligner: "align", Prompt: "Kenobi"})
	if err != nil {
		t.Fatal(err)
	}
	res, err := runJob(t, svc, sched, id)
	if err != nil {
		t.Fatal(err)
	}
	var tp proto.TranscribeParams
	json.Unmarshal(eng.params[proto.MethodSTTTranscribe], &tp)
	if tp.Prompt != "" {
		t.Errorf("prompt %q sent with 10 minutes of audio", tp.Prompt)
	}
	tr := res.Transcript
	if strings.Contains(tr.Raw, "The.") || strings.Contains(tr.Text(), "The.") || tr.Aligner != "align" {
		t.Errorf("transcript %q / %q", tr.Raw, tr.Text())
	}
	if len(eng.aligns) != 3 {
		t.Fatalf("%d align calls", len(eng.aligns))
	}
	for i, a := range eng.aligns {
		if a.Model != "align" || a.ModelPath != "/snap/align" || !strings.Contains(a.Audio, ".align") {
			t.Errorf("align %d: %+v", i, a)
		}
		if _, err := os.Stat(a.Audio); !os.IsNotExist(err) {
			t.Errorf("window %d kept", i)
		}
	}
	var last float64
	n := 0
	for _, s := range tr.Segments {
		for _, w := range s.Words {
			if w.Start < last-1e-6 || w.End > secs {
				t.Fatalf("word %+v after %g", w, last)
			}
			last = w.Start
			n++
		}
	}
	if n != 20*5 || last < 570 {
		t.Errorf("%d words, the last at %g", n, last)
	}
	// "Sentence number 8" starts the second window, at 240 s.
	for _, s := range tr.Segments {
		if strings.HasPrefix(s.Text, "Sentence number 8 ") && (s.Start < 235 || s.Start > 245) {
			t.Errorf("segment %q at %g", s.Text, s.Start)
		}
	}
}

// TestSTTForLanguage: a dub or workflow picks the other model for a
// language the first does not list; detection and listed languages stay.
func TestSTTForLanguage(t *testing.T) {
	m := DubModels{STT: "q", WordTimes: false, STTLanguages: []string{"zh", "en", "ja"}, OtherSTT: "w", OtherWordTimes: true}
	for lang, want := range map[string]string{"": "q", "auto": "q", "zh": "q", "zh-Hans": "q", "EN-us": "q", "ja": "q", "sw": "w", "uk-UA": "w"} {
		if got, words := m.STTFor(lang); got != want || words != (want == "w") {
			t.Errorf("STTFor(%q) = %s %v", lang, got, words)
		}
	}
	if got, _ := (DubModels{STT: "q", STTLanguages: []string{"zh"}}).STTFor("sw"); got != "q" {
		t.Errorf("no other model: %s", got)
	}
	w := WorkflowSTT{Model: "q", Languages: []string{"zh"}, Other: "w"}
	if a, _ := w.For("sw"); a != "w" {
		t.Errorf("workflow: %s", a)
	}
	if a, _ := w.For("zh"); a != "q" {
		t.Errorf("workflow: %s", a)
	}
}
