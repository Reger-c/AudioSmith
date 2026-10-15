package services

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hehehai/AudioSmith/internal/core"
	"github.com/hehehai/AudioSmith/internal/dsp"
	"github.com/hehehai/AudioSmith/internal/dsp/dsptest"
)

// legacyChapter is a story chapter as documents made before pacing render it.
func legacyChapter() ChapterParams {
	return ChapterParams{Render: "r", Model: "m/kokoro", Lang: "en", Seed: 7,
		Voices: map[string]core.Take{"narrator": {Voice: "af_heart"}, "mara": {Voice: "af_bella"}, "tom": {Voice: "am_adam"}},
		Lines: []LineSpec{
			{ID: "a", Spans: []SpanSpec{{Voice: "narrator", Text: "Night fell on the old lighthouse."}}},
			{ID: "b", Character: "Mara", Spans: []SpanSpec{{Voice: "mara", Character: "Mara", Text: "Is anyone there?"}}},
			{ID: "c", Character: "Mara", Spans: []SpanSpec{{Voice: "mara", Character: "Mara", Text: "Hello? [pause 300ms] Tom?"}}, PauseAfter: 600},
			{ID: "d", Character: "Tom", Spans: []SpanSpec{{Voice: "tom", Character: "Tom", Text: "Up here."}, {Voice: "mara", Character: "Mara", Text: "Oh."}}},
			{ID: "e", Character: "Tom", Paragraph: true, Spans: []SpanSpec{{Voice: "tom", Character: "Tom", Text: "Come up..."}}},
		},
		Production: Production{LineGapMs: 400, ParagraphGapMs: 800}, Output: "/tmp/x.wav"}
}

func chapterKeyOf(t *testing.T, p ChapterParams, models func(string) ModelInfo) (string, []chapterStep) {
	t.Helper()
	r := &renderer{cacheDir: filepath.Join(t.TempDir(), "cache"), models: models, logf: t.Logf}
	steps, key, _, err := chapterRenderer{r}.program(context.Background(), &p, r.model(p.Model), "")
	if err != nil {
		t.Fatal(err)
	}
	return key, steps
}

// legacyKey is legacyChapter's chapter key as the renderer before pacing
// (f171c86) made it: its renders are reused, its chunks come from the
// cache.
const legacyKey = "237e884ef5a5ad80d6f6180a4df4909dc21d4b39595604b2930e83576a549dee"

func TestOldStoriesRenderAsBefore(t *testing.T) {
	key, steps := chapterKeyOf(t, legacyChapter(), kokoroInfo)
	if key != legacyKey {
		t.Errorf("a story without pacing has key %s, want %s (as before pacing)", key, legacyKey)
	}
	var gaps []string
	for _, s := range steps {
		if s.turn != nil {
			t.Error("a story without pacing merges lines")
		}
		if s.chunk == nil {
			gaps = append(gaps, fmt.Sprint(s.silence))
		}
	}
	if got := strings.Join(gaps, " "); got != "0.4 0.4 0.3 0.6 0.15 0.8" {
		t.Errorf("silences %s", got)
	}

	// A version 1 document compiles with no pacing, scene or direction.
	var d LongformDoc
	v1 := `{"version":1,"kind":"story","model":"m/kokoro","voice":"speaker:af_heart","lines":[{"id":"a","text":"Hi there.","character":"c1"}],
		"cast":[{"id":"c1","name":"Mara","voice":"speaker:af_bella"}],"production":{"lineGapMs":400,"paragraphGapMs":800}}`
	if err := json.Unmarshal([]byte(v1), &d); err != nil {
		t.Fatal(err)
	}
	spec, probs := Compile(&d, testVoices())
	if len(probs) > 0 || spec.Production.Pacing != PacingFixed || spec.Scene != "" {
		t.Fatalf("v1: %+v %+v", spec, probs)
	}
	l := spec.Chapters[0].Lines[0]
	if l.Direction != "" || l.Persona != "" || l.Intensity != 0 {
		t.Errorf("v1 line %+v", l)
	}
	// New stories pace as a dialogue; new audiobooks keep the fixed gaps.
	if NewLongformDoc(core.ProjectStory, "m").Production.Pacing != PacingDialogue ||
		NewLongformDoc(core.ProjectAudiobook, "m").Production.Pacing != PacingFixed || LongformVersion != 2 {
		t.Error("new documents' pacing")
	}
}

func TestTurnGaps(t *testing.T) {
	line := func(who, voice, text string) LineSpec {
		return LineSpec{Character: who, Spans: []SpanSpec{{Voice: voice, Text: text}}}
	}
	kid, mom := "Kid", "Mom"
	g, ok := GapsOf(PacingDialogue)
	if !ok {
		t.Fatal("no dialogue preset")
	}
	if _, ok := GapsOf(PacingFixed); ok {
		t.Error("the fixed pacing has turn gaps")
	}
	for _, c := range []struct {
		prev, cur LineSpec
		turn      string
		ms        int
	}{
		{line(kid, "v", "我想领养一只小猫。"), line(kid, "v", "妈妈，可以吗？"), TurnSame, g.Same},
		{line(kid, "v", "妈妈，可以吗？"), line(mom, "w", "你能照顾它吗？"), TurnQuestion, g.Question},
		{line(kid, "v", "Can I keep it?”"), line(mom, "w", "Maybe."), TurnQuestion, g.Question},
		{line(mom, "w", "领养是好事。"), line(kid, "v", "我能！"), TurnStatement, g.Statement},
		{line(mom, "w", "你要负责，不能……"), line(kid, "v", "我会的！"), TurnTrailing, g.Trailing},
		{line(mom, "w", "Wait—"), line(kid, "v", "No!"), TurnTrailing, g.Trailing},
		{line(mom, "w", "I said... [sigh]"), line(kid, "v", "Fine."), TurnTrailing, g.Trailing},
		{line("", "n", "妈妈笑了。"), line(kid, "v", "太好了！"), TurnNarration, g.Narration},
		{line(kid, "v", "太好了！"), line("", "n", "她跳了起来。"), TurnNarration, g.Narration},
		{line(kid, "v", "One."), LineSpec{Character: kid, Paragraph: true, Spans: []SpanSpec{{Voice: "v", Text: "Two."}}}, TurnParagraph, g.Paragraph},
		// The same character in another voice is a new turn.
		{line(kid, "v", "One."), line(kid, "x", "Two."), TurnStatement, g.Statement},
	} {
		if got := TurnOf(c.prev, c.cur); got != c.turn {
			t.Errorf("%q → %q: turn %s, want %s", c.prev.Spans[0].Text, c.cur.Spans[0].Text, got, c.turn)
		}
		if got := TurnGap(g, c.prev, c.cur); math.Abs(got-float64(c.ms)/1000) > 1e-9 {
			t.Errorf("%q → %q: gap %g", c.prev.Spans[0].Text, c.cur.Spans[0].Text, got)
		}
	}
	// A pause after the line wins.
	p := line(kid, "v", "Hm?")
	p.PauseAfter = 900
	if got := TurnGap(g, p, line(mom, "w", "Yes.")); got != 0.9 {
		t.Errorf("pause after: %g", got)
	}
	// Presets order: dialogue < reading < relaxed for every turn; answers to
	// questions quicker than to statements; interruptions quickest.
	d, r, x := turnGaps[PacingDialogue], turnGaps[PacingReading], turnGaps[PacingRelaxed]
	for _, f := range []func(TurnGaps) int{
		func(g TurnGaps) int { return g.Same }, func(g TurnGaps) int { return g.Question }, func(g TurnGaps) int { return g.Statement },
		func(g TurnGaps) int { return g.Trailing }, func(g TurnGaps) int { return g.Narration }, func(g TurnGaps) int { return g.Paragraph },
	} {
		if !(f(d) < f(r) && f(r) < f(x)) {
			t.Errorf("presets not ordered: %d %d %d", f(d), f(r), f(x))
		}
	}
	for _, p := range []TurnGaps{d, r, x} {
		if !(p.Trailing < p.Question && p.Question < p.Statement && p.Statement < p.Narration && p.Narration < p.Paragraph) {
			t.Errorf("turns not ordered: %+v", p)
		}
	}
}

func TestMergeGroups(t *testing.T) {
	info := ModelInfo{Traits: TraitsFor("qwen3_tts", nil)}
	line := func(who, text string) LineSpec {
		return LineSpec{Character: who, Spans: []SpanSpec{{Voice: who, Text: text}}}
	}
	lines := []LineSpec{
		line("kid", "我想领养一只小猫。"),                // 0 ┐
		line("kid", "妈妈，可以吗？"),                  // 1 ┘
		line("mom", "领养是好事。"),                   // 2 alone
		line("kid", "我和你一起去。"),                  // 3 ┐
		line("kid", "我用零花钱买猫粮。"),                // 4 │
		line("kid", "我同学小雨家有一只猫，很可爱。"),          // 5 ┘
		line("mom", "那我们先去救助站看看吧，"),             // 6 no sentence end: not merged with 7
		line("mom", "好吗？"),                      // 7
		line("mom", "Hello [pause 300ms] you."), // 8 a pause: alone
		line("mom", "One."),                     // 9 ┐ direction "calm"
		line("mom", "Two."),                     // 10┘ no direction: follows 9
		line("mom", "Three."),                   // 11 another direction
		line("mom", "Four."),                    // 12 another speed
	}
	lines[9].Direction, lines[11].Direction = "calm", "angry"
	lines[12].Speed = 1.2
	got := fmt.Sprint(mergeGroups(lines, info, false))
	if got != "[[0 1] [3 4 5] [9 10]]" {
		t.Errorf("groups %s", got)
	}
	// A pause after a line, a paragraph, and VaryRepeats' repeats break turns.
	lines[0].PauseAfter = 500
	lines[4].Paragraph = true
	lines[10].Repeat = 1
	if got := fmt.Sprint(mergeGroups(lines, info, true)); got != "[[4 5]]" {
		t.Errorf("groups %s", got)
	}
	// At most four lines and the length limit.
	var many []LineSpec
	for range 6 {
		many = append(many, line("a", "这是一句不长不短的台词。"))
	}
	if got := fmt.Sprint(mergeGroups(many, info, false)); got != "[[0 1 2 3] [4 5]]" {
		t.Errorf("groups %s", got)
	}
	long := []LineSpec{line("a", strings.Repeat("长", 70)+"。"), line("a", strings.Repeat("长", 70)+"。")}
	if got := mergeGroups(long, info, false); len(got) != 0 {
		t.Errorf("long lines merged: %v", got)
	}
	if got := joinTurn([]string{"我想去。", "可以吗？"}); got != "我想去。可以吗？" {
		t.Errorf("join %q", got)
	}
	if got := joinTurn([]string{"I want to go.", " Can I?"}); got != "I want to go. Can I?" {
		t.Errorf("join %q", got)
	}
}

// turnAudio is speech-like audio: tones (seconds) separated by pauses.
func turnAudio(parts ...float64) dsp.Buffer {
	var bs []dsp.Buffer
	for i, p := range parts {
		if i%2 == 0 {
			bs = append(bs, dsptest.Sine(24000, 1, 220, -12, p, 0))
		} else {
			bs = append(bs, dsptest.Silence(24000, 1, p))
		}
	}
	return dsptest.Concat(bs...)
}

func TestSplitMerged(t *testing.T) {
	// Two lines: a comma pause inside the first, the sentence pause after it.
	b := turnAudio(1.1, 0.12, 0.9, 0.38, 1.6)
	cuts, ok := splitMerged(b, []float64{2.0, 1.6})
	if !ok || len(cuts) != 1 || math.Abs(float64(cuts[0].start)/24000-2.12) > 0.02 || math.Abs(float64(cuts[0].end)/24000-2.5) > 0.02 {
		t.Fatalf("cuts %v %v", cuts, ok)
	}
	// Three lines, uneven, short pauses inside.
	b = turnAudio(0.6, 0.3, 1.2, 0.07, 0.5, 0.25, 2.0, 0.08, 0.4)
	cuts, ok = splitMerged(b, []float64{0.7, 1.8, 2.4})
	if !ok || len(cuts) != 2 || math.Abs(float64(cuts[0].start)/24000-0.6) > 0.02 || math.Abs(float64(cuts[1].start)/24000-2.67) > 0.02 {
		t.Fatalf("cuts %v %v", cuts, ok)
	}
	// No pause where a line should end: the turn is not split.
	if _, ok := splitMerged(turnAudio(3.5), []float64{1.5, 2}); ok {
		t.Error("split audio without pauses")
	}
	// A pause, but far from where any line could end.
	if _, ok := splitMerged(turnAudio(0.2, 0.4, 3.3), []float64{1.7, 1.8}); ok {
		t.Error("split a 0.2 s first line expected to last 1.7 s")
	}
	if cuts, ok := splitMerged(turnAudio(1), []float64{1}); !ok || len(cuts) != 0 {
		t.Error("one line")
	}
	// Quiet room noise in the pauses still counts as a pause.
	noisy := turnAudio(1.0, 0.3, 1.0)
	n := dsptest.Sine(24000, 1, 3000, -52, noisy.Seconds(), 0)
	for i := range noisy.Samples {
		noisy.Samples[i] += n.Samples[i]
	}
	if _, ok := splitMerged(noisy, []float64{1, 1}); !ok {
		t.Error("room noise hides the pause")
	}
}

func TestStoryInstruct(t *testing.T) {
	for _, c := range []struct {
		text, scene, persona, dir string
		intensity                 int
		prev, want                string
	}{
		{"我想养猫。", "", "", "", 0, "", ""},
		{"我想养猫。", "傍晚，客厅", "七岁女孩，活泼", "撒娇央求", 3, "", "七岁女孩，活泼。场景：傍晚，客厅。这一句：撒娇央求，情绪饱满强烈。"},
		{"我想养猫。", "", "", "小声", IntensityLight, "", "这一句：小声，情绪克制。"},
		{"我想养猫。", "", "温柔的妈妈。", "", 0, "", "温柔的妈妈。"},
		{"好吧。", "", "", "松口", 2, "犹豫", "对方刚才犹豫。这一句：松口。"},
		{"Can I keep it?", "A rainy evening", "a seven-year-old girl", "pleading", 3, "", "A seven-year-old girl. Scene: A rainy evening. This line: pleading, with strong emotion."},
		{"Fine.", "", "", "giving in", IntensityLight, "", "This line: giving in, restrained."},
	} {
		if got := StoryInstruct(c.text, c.scene, c.persona, c.dir, c.intensity, c.prev); got != c.want {
			t.Errorf("StoryInstruct(%q, %q, %q, %q) = %q, want %q", c.text, c.scene, c.persona, c.dir, got, c.want)
		}
	}
	// Long parts are cut: the instruction stays short.
	long := StoryInstruct("我想养猫。", strings.Repeat("场", 200), strings.Repeat("人", 200), strings.Repeat("情", 200), 2, "")
	if n := len([]rune(long)); n > instructPersona+instructScene+instructDirection+20 {
		t.Errorf("instruction of %d runes: %s", n, long)
	}
	longEn := StoryInstruct("Hello.", strings.Repeat("scene ", 100), strings.Repeat("persona ", 100), strings.Repeat("dir ", 100), 2, "")
	if n := len(longEn); n > (instructPersona+instructScene+instructDirection)*instructLatin+60 {
		t.Errorf("instruction of %d characters", n)
	}
}

func TestDeliveryPerModel(t *testing.T) {
	customVoice := ModelInfo{Traits: TraitsFor("qwen3_tts", []string{"sampling", "voiceStyle", "instruct", "language", "stream"})}
	voiceDesign := ModelInfo{Traits: TraitsFor("qwen3_tts", []string{"sampling", "design", "voiceStyle", "instruct", "language"})}
	base := ModelInfo{Traits: TraitsFor("qwen3_tts", []string{"sampling", "clone", "language", "voicePrompt"})}
	omni := ModelInfo{Traits: TraitsFor("omnivoice", []string{"clone", "design", "voiceStyle", "language", "duration", "speed"})}
	chatterbox := ModelInfo{Traits: TraitsFor("chatterbox", []string{"sampling", "clone", "guidance", "exaggeration"})}
	kokoro := ModelInfo{Traits: TraitsFor("kokoro", []string{"speed"})}
	// An entry that only says voiceStyle (an older catalogue) still takes it.
	styleOnly := ModelInfo{Traits: TraitsFor("qwen3_tts", []string{"voiceStyle"})}
	const ins = "这一句：撒娇。"
	for _, c := range []struct {
		name  string
		info  ModelInfo
		in    core.Take
		voice string
		exag  float64
		level string
	}{
		{"customvoice", customVoice, core.Take{Voice: "Vivian"}, "Vivian, " + ins, 0, ContinuityInstruct},
		{"voicedesign", voiceDesign, core.Take{Voice: "温柔的年轻女声"}, "温柔的年轻女声, " + ins, 0, ContinuityInstruct},
		{"voicedesign auto", voiceDesign, core.Take{}, ins, 0, ContinuityInstruct},
		{"style only", styleOnly, core.Take{Voice: "Serena"}, "Serena, " + ins, 0, ContinuityInstruct},
		{"base clone", base, core.Take{RefAudio: "/r.wav"}, "", 0, ContinuityLines},
		{"voicedesign clone", voiceDesign, core.Take{Voice: "x", VoicePrompt: "/p"}, "x", 0, ContinuityInstruct},
		{"omnivoice", omni, core.Take{Voice: "female, child"}, "female, child", 0, ContinuityLines},
		{"chatterbox", chatterbox, core.Take{RefAudio: "/r.wav"}, "", 0.9, ContinuityIntensity},
		{"chatterbox set", chatterbox, core.Take{RefAudio: "/r.wav", Exaggeration: 0.3}, "", 0.3, ContinuityIntensity},
		{"kokoro", kokoro, core.Take{Voice: "af_heart"}, "af_heart", 0, ContinuityLines},
	} {
		tk := c.in
		withDelivery(&tk, c.info, ins, "撒娇", IntensityStrong)
		if tk.Voice != c.voice || tk.Exaggeration != c.exag {
			t.Errorf("%s: voice %q exaggeration %g", c.name, tk.Voice, tk.Exaggeration)
		}
		if got := ContinuityOf(c.info.Traits); got != c.level {
			t.Errorf("%s: level %s", c.name, got)
		}
	}
	if !TakesInstruct([]string{"instruct"}) || TakesInstruct([]string{"voiceStyle", "clone"}) || TakesInstruct(nil) {
		t.Error("TakesInstruct")
	}
}

// The compiler carries scene, persona and direction; parenthetical
// directions never reach the spoken text (stories.go moves them at import).
func TestCompileDirections(t *testing.T) {
	d := NewLongformDoc(core.ProjectStory, "m/kokoro")
	d.Voice = "speaker:af_heart"
	d.Scene = "  傍晚，客厅  "
	d.Cast = []CastMember{{ID: "c1", Name: "孩子", Voice: "speaker:af_bella", Persona: "七岁女孩"}}
	d.Lines = []StoryLine{
		{ID: "a", Character: "c1", Text: "妈妈，可以吗？", Direction: " 撒娇央求 ", Intensity: 7},
		{ID: "b", Text: "妈妈笑了。", Intensity: 2},
	}
	spec, probs := Compile(&d, testVoices())
	if len(probs) > 0 {
		t.Fatal(probs)
	}
	a, b := spec.Chapters[0].Lines[0], spec.Chapters[0].Lines[1]
	if spec.Scene != "傍晚，客厅" || a.Direction != "撒娇央求" || a.Intensity != IntensityStrong || a.Persona != "七岁女孩" ||
		b.Direction != "" || b.Intensity != 0 || b.Persona != "" || spec.Production.Pacing != PacingDialogue {
		t.Errorf("spec %+v\n%+v\n%+v", spec, a, b)
	}
	if a.Spans[0].Text != "妈妈，可以吗？" {
		t.Errorf("spoken %q", a.Spans[0].Text)
	}
	// Audiobooks have no scene.
	d.Kind = core.ProjectAudiobook
	d.Script = "Hello."
	if spec, _ := Compile(&d, testVoices()); spec.Scene != "" {
		t.Error("audiobook scene")
	}
}

// A paced story merges the turns of one character into one call, splits
// them back into lines (sidecar), gaps by turn, and makes a turn line by
// line when its audio has no pause to split at.
func TestPacedStoryRender(t *testing.T) {
	e := newLongformEnv(t, t.TempDir(), false)
	defer e.close()
	e.synth.pauses = true
	spec := LongformSpec{Kind: core.ProjectStory, Title: "Kitten", Model: "m/kokoro", Lang: "en", Seed: 7,
		Voices: map[string]core.Take{"kid": {Voice: "af_bella"}, "mom": {Voice: "af_heart"}},
		Chapters: []ChapterSpec{{Lines: []LineSpec{
			{ID: "a", Character: "Kid", Spans: []SpanSpec{{Voice: "kid", Character: "Kid", Text: "I want a kitten, a small grey one."}}},
			{ID: "b", Character: "Kid", Spans: []SpanSpec{{Voice: "kid", Character: "Kid", Text: "Can we get one today?"}}},
			{ID: "c", Character: "Mom", Spans: []SpanSpec{{Voice: "mom", Character: "Mom", Text: "Will you look after it every day?"}}},
			{ID: "d", Character: "Kid", Spans: []SpanSpec{{Voice: "kid", Character: "Kid", Text: "Yes, I promise."}}},
		}}},
		Production: DefaultStoryProduction(), Audition: true}
	r, err := e.svc.Longform.Start(ctx, spec)
	if err != nil {
		t.Fatal(err)
	}
	_, body := e.wait(t, r.ID)
	if body.Chapters[0].State != "done" {
		t.Fatalf("chapter %+v", body.Chapters[0])
	}
	texts := e.synth.texts()
	if strings.Join(texts, " | ") != "I want a kitten, a small grey one. Can we get one today? | Will you look after it every day? | Yes, I promise." {
		t.Fatalf("calls %q", texts)
	}
	res, err := ReadChapter(body.Chapters[0].File)
	if err != nil {
		t.Fatal(err)
	}
	if res.Turns != 1 || res.TurnsUnsplit != 0 || len(res.Lines) != 4 {
		t.Fatalf("result %+v", res)
	}
	// The fake engine says "I want a kitten, a small grey one." for its
	// estimate, then pauses 0.35 s: line b starts after that pause.
	a, b, c := res.Lines[0], res.Lines[1], res.Lines[2]
	if !(a.Start < a.End && a.End < b.Start && b.Start < b.End && b.End < c.Start) {
		t.Fatalf("lines %+v", res.Lines)
	}
	if gap := b.Start - a.End; gap < 0.3 || gap > 0.42 {
		t.Errorf("pause inside the turn %.3f", gap)
	}
	// Mom answers a question: the question gap.
	if gap := c.Start - b.End; math.Abs(gap-0.22) > 0.03 {
		t.Errorf("question gap %.3f", gap)
	}
	if gap := res.Lines[3].Start - c.End; math.Abs(gap-0.22) > 0.03 {
		t.Errorf("answer gap %.3f", gap)
	}

	// Without pauses in the engine's audio, the turn is made line by line.
	e.synth.pauses = false
	e.synth.reset()
	os.RemoveAll(filepath.Join(e.dir, "cache", "longform"))
	spec.Seed = 8
	r, _ = e.svc.Longform.Start(ctx, spec)
	_, body = e.wait(t, r.ID)
	res, _ = ReadChapter(body.Chapters[0].File)
	if res.Turns != 0 || res.TurnsUnsplit != 1 {
		t.Errorf("result %+v", res)
	}
	texts = e.synth.texts()
	if strings.Join(texts, " | ") != "I want a kitten, a small grey one. Can we get one today? | I want a kitten, a small grey one. | Can we get one today? | Will you look after it every day? | Yes, I promise." {
		t.Errorf("calls %q", texts)
	}
	if gap := res.Lines[1].Start - res.Lines[0].End; math.Abs(gap-0.18) > 0.03 {
		t.Errorf("same-speaker gap %.3f", gap)
	}

	// NoMerge: a call per line.
	e.synth.reset()
	os.RemoveAll(filepath.Join(e.dir, "cache", "longform"))
	spec.Seed = 9
	spec.Production.NoMerge = true
	r, _ = e.svc.Longform.Start(ctx, spec)
	e.wait(t, r.ID)
	if n := len(e.synth.texts()); n != 4 {
		t.Errorf("%d calls without merging", n)
	}
}

// Directions reach an instruct model's voice field (and its chunk key);
// a model without instructions gets the plain voice.
func TestStoryInstructSent(t *testing.T) {
	e := newLongformEnv(t, t.TempDir(), false)
	defer e.close()
	cv := func(string) ModelInfo {
		return ModelInfo{Traits: TraitsFor("qwen3_tts", []string{"sampling", "voiceStyle", "instruct", "language"}), Revision: "r"}
	}
	e.svc.Longform.render.models = cv
	e.synth.pauses = true
	spec := LongformSpec{Kind: core.ProjectStory, Title: "Kitten", Model: "m/cv", Lang: "zh", Seed: 7, Scene: "傍晚，客厅",
		Voices: map[string]core.Take{"kid": {Voice: "Vivian"}},
		Chapters: []ChapterSpec{{Lines: []LineSpec{
			{ID: "a", Character: "孩子", Persona: "七岁女孩", Direction: "撒娇", Intensity: 2, Spans: []SpanSpec{{Voice: "kid", Character: "孩子", Text: "可以吗？"}}},
			{ID: "b", Character: "孩子", Spans: []SpanSpec{{Voice: "kid", Character: "孩子", Text: "好不好？"}}, PauseAfter: 100},
		}}},
		Production: DefaultStoryProduction(), Audition: true}
	r, _ := e.svc.Longform.Start(ctx, spec)
	e.wait(t, r.ID)
	e.synth.mu.Lock()
	calls := e.synth.calls
	e.synth.mu.Unlock()
	if len(calls) != 1 || calls[0].Voice != "Vivian, 七岁女孩。场景：傍晚，客厅。这一句：撒娇。" || calls[0].Text != "可以吗？好不好？" {
		t.Fatalf("calls %+v", calls)
	}
}
