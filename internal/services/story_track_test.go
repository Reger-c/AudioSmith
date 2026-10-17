package services

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hehehai/AudioSmith/internal/core"
	"github.com/hehehai/AudioSmith/internal/engine/proto"
)

func TestTrackGroups(t *testing.T) {
	l := func(who, text string, turn bool) LineSpec {
		return LineSpec{Character: who, Turn: turn, Spans: []SpanSpec{{Voice: who, Character: who, Text: text}}}
	}
	lines := []LineSpec{
		l("kid", "我想养猫。", false), l("mom", "你能照顾它吗？", false), l("kid", "我能！", false),
		l("", "妈妈笑了。", false), l("mom", "好吧。", false), l("kid", "太好了！", true), l("kid", "谢谢妈妈！", false),
		l("mom", "去吧。", false), l("dad", "嗯。", false),
	}
	got := fmt.Sprint(trackGroups(lines, ModelInfo{Traits: TraitsFor("qwen3_tts", nil)}))
	// The kid's track breaks at the turn (line 6); dad has one line; narration is no track.
	if got != "[[0 2] [1 4 7] [5 6]]" {
		t.Errorf("groups %s", got)
	}
	// The length limit.
	var long []LineSpec
	for range 12 {
		long = append(long, l("kid", strings.Repeat("长", 30)+"。", false))
	}
	for _, g := range trackGroups(long, ModelInfo{Traits: TraitsFor("qwen3_tts", nil)}) {
		if len(g)*30 > trackMaxCJK {
			t.Errorf("group of %d lines", len(g))
		}
	}
	if e, i := trackFeeling(LineSpec{Baseline: EmotionTender, Emotion: EmotionSad, Intensity: 3}); e != EmotionTender || i != 1 {
		t.Errorf("baseline %s %d", e, i)
	}
	if e, i := trackFeeling(LineSpec{Baseline: EmotionTender, Emotion: EmotionSad, Intensity: 3, Turn: true}); e != EmotionSad || i != 3 {
		t.Errorf("turn %s %d", e, i)
	}
}

func TestTrackRanges(t *testing.T) {
	parts := []string{"我想养猫。", "我能！", "谢谢妈妈！"}
	w := timedWords(0.1, 0.35, parts...)
	rg, err := trackRanges(w, parts)
	if err != nil || len(rg) != 3 || rg[1][0] <= rg[0][1] {
		t.Fatalf("ranges %v %v", rg, err)
	}
	if _, err := trackRanges(w, []string{"我想养猫。", "我能！", "谢谢爸爸！"}); err != errLeadWords {
		t.Errorf("words: %v", err)
	}
	w[3].End = w[3].Start + 3 // the first line's last word lasts 3 s
	if _, err := trackRanges(w, parts); err != errLeadLength && err != errLeadOrder {
		t.Errorf("length: %v", err)
	}
}

// A character's lines are said in one go and cut back into the
// dialogue; a track the recognizer does not hear is made line by line.
func TestTrackRender(t *testing.T) {
	e := newLongformEnv(t, t.TempDir(), false)
	defer e.close()
	e.synth.pauses = true
	var heard []string
	fa := &fakeAnalysis{heard: func(string) string {
		if len(heard) == 0 {
			return ""
		}
		h := heard[0]
		heard = heard[1:]
		return h
	}}
	r := e.svc.Longform.render
	r.engine, r.aligner, r.recognizer = fa.executor, "m/aligner", "m/asr"
	spec := LongformSpec{Kind: core.ProjectStory, Title: "Kitten", Model: "m/kokoro", Lang: "en", Seed: 7,
		Voices: map[string]core.Take{"kid": {Voice: "af_bella"}, "mom": {Voice: "af_heart"}},
		Chapters: []ChapterSpec{{Lines: []LineSpec{
			{ID: "a", Character: "Kid", Spans: []SpanSpec{{Voice: "kid", Character: "Kid", Text: "Can we get one today?"}}},
			{ID: "b", Character: "Mom", Spans: []SpanSpec{{Voice: "mom", Character: "Mom", Text: "Will you look after it every day?"}}},
			{ID: "c", Character: "Kid", Spans: []SpanSpec{{Voice: "kid", Character: "Kid", Text: "Yes, I promise."}}},
			{ID: "d", Character: "Mom", Spans: []SpanSpec{{Voice: "mom", Character: "Mom", Text: "All right"}}},
		}}},
		Production: DefaultStoryProduction(), Audition: true}
	spec.Production.Tracks = true
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
	heard = []string{"Can we get one today?", "Yes, I promise.", "Will you look after it every day?", "All right."}
	res := run()
	if got := strings.Join(e.synth.texts(), " | "); got != "Can we get one today? Yes, I promise. | Will you look after it every day? All right." {
		t.Fatalf("calls %q", got)
	}
	if res.Tracks != 2 || res.TrackLines != 4 || res.TracksFailed != 0 || fa.count(proto.MethodSTTTranscribe) != 4 {
		t.Fatalf("result %+v", res)
	}
	// Lines in dialogue order, each its own length; the sidecar keeps the text.
	for i := 1; i < 4; i++ {
		if res.Lines[i].Start <= res.Lines[i-1].End {
			t.Errorf("lines %+v", res.Lines)
		}
	}
	if res.Lines[3].Spans[0].Text != "All right" {
		t.Errorf("text %q", res.Lines[3].Spans[0].Text)
	}
	// The recognizer hears the kid's second line wrong: that track is made line by line.
	os.RemoveAll(filepath.Join(e.dir, "cache", "longform"))
	e.synth.reset()
	heard = []string{"Can we get one today?", "Yes.", "Will you look after it every day?", "All right."}
	res = run()
	if res.Tracks != 1 || res.TracksFailed != 1 {
		t.Fatalf("failed track %+v", res)
	}
	if got := strings.Join(e.synth.texts(), " | "); got != "Can we get one today? Yes, I promise. | Can we get one today? | Will you look after it every day? All right. | Yes, I promise." {
		t.Errorf("calls %q", got)
	}
}
