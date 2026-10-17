package services

import (
	"context"
	"fmt"
	"math"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/hehehai/AudioSmith/internal/core"
)

func TestEmotionInstruct(t *testing.T) {
	for _, c := range []struct {
		emotion   string
		intensity int
		own       string
		cont, zh  bool
		want      string
	}{
		{"", 2, "", false, true, ""},
		{"steady", 1, "", false, true, "语气平稳"},
		{"steady", 0, "", true, true, "语气平稳，接着说"},
		{"hurt", 0, "", false, true, "平静，带点委屈"},
		{"hurt", 1, "", true, true, "略带委屈，接着说"},
		{"委屈", 2, "", false, true, "委屈"},
		{"hurt", 3, "", false, true, "非常委屈，带哭腔"},
		{"hurt", 3, "", true, true, "非常委屈，带哭腔"}, // the tail does not fit
		{"scared", 3, "", false, true, "非常害怕，声音发抖"},
		{"hurt", 2, "撒娇。", true, true, "撒娇，接着说"},
		{"hurt", 2, strings.Repeat("长", 30), false, true, strings.Repeat("长", 10)},
		{"excited", 3, "", false, false, "very excited, fast"},
		{"hesitant", 1, "", true, false, "slightly hesitant, carrying on"},
		{"steady", 2, "", false, false, "calm, even"},
	} {
		if got := EmotionInstruct(c.emotion, c.intensity, c.own, c.cont, c.zh); got != c.want {
			t.Errorf("EmotionInstruct(%q, %d, %q, %v, %v) = %q, want %q", c.emotion, c.intensity, c.own, c.cont, c.zh, got, c.want)
		}
	}
	// Every Chinese instruction stays within the plan's 10 characters.
	for _, e := range Emotions {
		for i := range 4 {
			for _, cont := range []bool{false, true} {
				if s := EmotionInstruct(e, i, "", cont, true); utf8.RuneCountInString(s) > instructEmotionMax {
					t.Errorf("%s %d: %q", e, i, s)
				}
			}
		}
	}
	if NormEmotion("非常委屈") != EmotionHurt || NormEmotion("Anxious") != EmotionWorried || NormEmotion("xyz") != "" || NormEmotion("") != "" {
		t.Error("NormEmotion")
	}
}

func TestEmotionGaps(t *testing.T) {
	line := func(e string, i int, text string) LineSpec {
		return LineSpec{Character: "a", Emotion: e, Intensity: i, Spans: []SpanSpec{{Text: text}}}
	}
	plain := line("", 0, "好。")
	if g := emotionGap(0.3, plain, plain); g != 0.3 {
		t.Errorf("no emotion: %v", g)
	}
	excited, sad := line(EmotionExcited, 3, "太好了！"), line(EmotionSad, 3, "我知道了。")
	if g := emotionGap(0.3, plain, excited); g >= 0.3*0.85 {
		t.Errorf("an excited answer comes quicker: %v", g)
	}
	if g := emotionGap(0.3, plain, sad); g <= 0.3*1.15 {
		t.Errorf("a sad answer waits: %v", g)
	}
	if a, b := emotionGap(0.3, plain, line(EmotionSad, 1, "嗯。")), emotionGap(0.3, plain, sad); a >= b {
		t.Errorf("intensity scales the pause: %v %v", a, b)
	}
	if g := emotionGap(0.1, line(EmotionHesitant, 3, "我……"), line(EmotionHesitant, 3, "嗯。")); g > 0.1 {
		t.Errorf("a cut-in is never lengthened: %v", g)
	}
	p := line(EmotionSad, 3, "好。")
	p.PauseAfter = 500
	if g := emotionGap(0.5, p, sad); g != 0.5 {
		t.Errorf("a line's own pause wins: %v", g)
	}
	if g := emotionGap(0.05, line(EmotionExcited, 3, "啊！"), excited); g != emotionGapMin {
		t.Errorf("floor: %v", g)
	}
	if math.Abs(EmotionGapFactor(EmotionSteady, 3)-1) > 1e-9 || EmotionGapFactor("", 2) != 1 {
		t.Error("steady pauses as the pacing")
	}
}

func beatsString(b []EmotionBeat) string {
	var parts []string
	for _, x := range b {
		s := fmt.Sprintf("%s:%s%d", x.Who, x.Emotion, x.Intensity)
		if x.Turn {
			s += "!"
		}
		parts = append(parts, s)
	}
	return strings.Join(parts, " ")
}

func TestSmoothEmotions(t *testing.T) {
	b := func(who, e string, i int, turn bool) EmotionBeat {
		return EmotionBeat{Who: who, Emotion: e, Intensity: i, Turn: turn}
	}
	// Jumps of one character are cut to one step; another character's
	// lines in between do not count; a turn may jump.
	got := SmoothEmotions([]EmotionBeat{
		b("kid", "pleading", 1, false), b("mom", "worried", 1, false), b("kid", "pleading", 3, false),
		b("mom", "tender", 1, false), b("kid", "sad", 3, true), b("kid", "happy", 3, false),
		b("mom", "", 0, false), b("kid", "steady", 3, false), b("mom", "oops", 2, false),
		b("kid", "steady", 0, false), b("mom", "steady", 0, false), b("kid", "happy", 0, false),
	})
	want := "kid:pleading1 mom:worried1 kid:pleading2 mom:tender1 kid:sad3! kid:happy1 mom:steady0 kid:steady1 mom:steady1 kid:steady0 mom:steady0 kid:happy0"
	if s := beatsString(got); s != want {
		t.Errorf("smoothed\n %s\nwant\n %s", s, want)
	}
	// Sparse emotion: few peaks and strong lines, the turns kept first.
	var many []EmotionBeat
	for i := range 16 {
		many = append(many, b([]string{"a", "b"}[i%2], "angry", 3, i == 9))
	}
	got = SmoothEmotions(many)
	strong, peaks, turns := 0, 0, 0
	for _, x := range got {
		strong += btoi(x.Intensity >= 2)
		peaks += btoi(x.Intensity == 3)
		turns += btoi(x.Turn)
	}
	if peaks > 2 || strong > 7 || turns != 1 || got[9].Intensity != 3 {
		t.Errorf("sparse: %s", beatsString(got))
	}
	// The user's beats stay as they are; the others follow them.
	got = SmoothEmotions([]EmotionBeat{{Who: "a", Emotion: "hurt", Intensity: 3, Fixed: true}, b("a", "steady", 0, false), b("a", "happy", 3, false)})
	if s := beatsString(got); s != "a:hurt3 a:steady1 a:happy2" {
		t.Errorf("fixed: %s", s)
	}
	// No swing from pleasant to unpleasant without a turn.
	got = SmoothEmotions([]EmotionBeat{b("a", "happy", 2, false), b("x", "steady", 0, false), b("a", "angry", 2, false)})
	if got[2].Intensity != 1 {
		t.Errorf("swing: %s", beatsString(got))
	}
	if len(SmoothEmotions(nil)) != 0 {
		t.Error("nil")
	}
}

// A story directed by emotion compiles each steady line into its
// character's baseline, sends a short instruction (no persona, no scene)
// and paces its gaps by emotion; an undirected story is unchanged.
func TestEmotionDirectedStory(t *testing.T) {
	d := NewLongformDoc(core.ProjectStory, "m/cv")
	d.Scene = "傍晚，客厅"
	d.Cast = []CastMember{{ID: "k", Name: "孩子", Voice: "speaker:Vivian", Persona: "七岁女孩", Baseline: EmotionExcited},
		{ID: "m", Name: "妈妈", Voice: "speaker:Serena"}}
	d.Lines = []StoryLine{
		{ID: "1", Character: "k", Text: "妈妈，我想养猫。", Emotion: EmotionPleading, Intensity: 2},
		{ID: "2", Character: "m", Text: "你能照顾它吗？", Emotion: "worried", Intensity: 1},
		{ID: "3", Character: "k", Text: "我能！", Emotion: EmotionSteady, Intensity: 2},
		{ID: "4", Character: "m", Text: "那好吧。", Emotion: EmotionSteady, Turn: true, Direction: "无奈地笑"},
	}
	spec, probs := Compile(&d, testVoices())
	if len(probs) > 0 {
		t.Fatal(probs)
	}
	ls := spec.Chapters[0].Lines
	if ls[2].Emotion != EmotionExcited || ls[2].Intensity != 1 || ls[3].Emotion != EmotionSteady || !ls[3].Turn || ls[3].Direction != "无奈地笑" {
		t.Fatalf("lines %+v", ls)
	}
	cv := func(string) ModelInfo {
		return ModelInfo{Traits: TraitsFor("qwen3_tts", []string{"sampling", "voiceStyle", "instruct", "language"}), Revision: "r"}
	}
	p := ChapterParams{Render: "r", Model: "m/cv", Lang: "zh", Seed: 7, Voices: spec.Voices, Lines: ls, Production: spec.Production,
		Scene: spec.Scene, Output: "/tmp/x.wav"}
	_, steps := chapterKeyOf(t, p, cv)
	var voices []string
	var gaps []float64
	for _, s := range steps {
		if s.chunk != nil {
			voices = append(voices, s.chunk.params.Voice)
		} else {
			gaps = append(gaps, s.silence)
		}
	}
	if got := strings.Join(voices, " | "); got != "Vivian, 央求 | Serena, 略带担心 | Vivian, 略带兴奋 | Serena, 无奈地笑" {
		t.Errorf("instructions %s", got)
	}
	if len(gaps) != 3 || gaps[0] <= 0.22 || gaps[1] >= 0.22 {
		t.Errorf("gaps %v (the pleading line's answer waits, the excited answer comes quicker)", gaps)
	}
	if LineInstruct(ls, 2, spec.Scene) != "略带兴奋" || LineInstruct([]LineSpec{{Direction: "小声", Spans: []SpanSpec{{Text: "好。"}}}}, 0, "") != "这一句：小声。" {
		t.Error("LineInstruct")
	}
	p.Production.NoEmotionGaps = true
	_, steps = chapterKeyOf(t, p, cv)
	if steps[1].silence != 0.32 {
		t.Errorf("NoEmotionGaps: %v", steps[1].silence)
	}
	// Chatterbox hears the intensity as its exaggeration.
	cb := func(string) ModelInfo {
		return ModelInfo{Traits: TraitsFor("chatterbox", []string{"sampling", "clone", "exaggeration"}), Revision: "r"}
	}
	if ContinuityOf(cb("").Traits) != ContinuityIntensity {
		t.Error("continuity")
	}
	_, steps = chapterKeyOf(t, p, cb)
	if e := steps[0].chunk.params.Exaggeration; e == nil || *e != 0.7 || steps[0].chunk.params.Voice != "Vivian" {
		t.Errorf("exaggeration %v voice %q", e, steps[0].chunk.params.Voice)
	}
}

func TestTurnEmotion(t *testing.T) {
	l := func(e string, i int, text string) LineSpec {
		return LineSpec{Emotion: e, Intensity: i, Spans: []SpanSpec{{Text: text}}}
	}
	if e, own, i := turnEmotion([]LineSpec{l("hurt", 1, "可以吗？"), l("hurt", 2, "好吗？")}); e != "hurt" || own != "" || i != 2 {
		t.Errorf("same: %s %q %d", e, own, i)
	}
	e, own, i := turnEmotion([]LineSpec{l("hurt", 1, "可以吗？"), l("pleading", 3, "求你了！")})
	if e != "hurt" || own != "先略带委屈，再非常央求，急切" || i != 3 {
		t.Errorf("two: %s %q %d", e, own, i)
	}
	if got := EmotionInstruct(e, i, own, false, true); got != own {
		t.Errorf("a merged turn's wording is kept whole: %q", got)
	}
	if e, _, _ := turnEmotion([]LineSpec{l("", 0, "a"), l("", 0, "b")}); e != "" {
		t.Error("none")
	}
}

// v1DirectedChapter is a paced story directed by the first AI director
// (directions and personas, no emotions) on Qwen3-TTS CustomVoice.
func v1DirectedChapter() ChapterParams {
	return ChapterParams{Render: "r", Model: "m/cv", Lang: "zh", Seed: 7, Scene: "傍晚，客厅",
		Voices: map[string]core.Take{"k": {Voice: "Vivian"}, "m": {Voice: "Serena"}},
		Lines: []LineSpec{
			{ID: "1", Character: "kid", Persona: "七岁女孩", Direction: "撒娇央求", Intensity: 2, Spans: []SpanSpec{{Voice: "k", Character: "kid", Text: "妈妈，我想养猫。"}}},
			{ID: "2", Character: "kid", Spans: []SpanSpec{{Voice: "k", Character: "kid", Text: "可以吗？"}}},
			{ID: "3", Character: "mom", Persona: "温柔的妈妈", Direction: "犹豫", Intensity: 1, Spans: []SpanSpec{{Voice: "m", Character: "mom", Text: "你能照顾它吗？"}}},
			{ID: "4", Character: "kid", Persona: "七岁女孩", Direction: "急切保证", Intensity: 3, Spans: []SpanSpec{{Voice: "k", Character: "kid", Text: "我能！"}}},
		},
		Production: DefaultStoryProduction(), Output: "/tmp/x.wav"}
}

func cvInfo(string) ModelInfo {
	return ModelInfo{Traits: TraitsFor("qwen3_tts", []string{"sampling", "voiceStyle", "instruct", "language"}), Revision: "r",
		Defaults: map[string]ParamDefaults{"": {Temperature: 0.7, TopP: 0.8}}}
}

// v1Key is v1DirectedChapter's chapter key as main made it before
// emotions and tracks existed (c433681): a story directed by the first
// director renders exactly as before, its chunks from the cache, even
// with the aligner present (tracks need emotions).
const v1Key = "a740aae26eb0fc81e66bdd74b8fe9e6d26db0d3f241c1f45c36805b0844dd2a3"

func TestV1DirectedStoriesRenderAsBefore(t *testing.T) {
	p := v1DirectedChapter()
	r := &renderer{cacheDir: filepath.Join(t.TempDir(), "cache"), models: cvInfo, logf: t.Logf,
		engine: (&fakeAnalysis{}).executor, aligner: "m/aligner", recognizer: "m/asr"}
	steps, key, _, err := chapterRenderer{r}.program(context.Background(), &p, r.model(p.Model), "")
	if err != nil {
		t.Fatal(err)
	}
	if key != v1Key {
		t.Errorf("a story of the first director has key %s, want %s (as before)", key, v1Key)
	}
	for _, s := range steps {
		if s.track != nil {
			t.Error("tracked without emotions")
		}
	}
}
