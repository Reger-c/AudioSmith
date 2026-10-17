package services

import (
	"testing"

	"github.com/hehehai/AudioSmith/internal/core"
)

func TestSteerText(t *testing.T) {
	for _, c := range []struct {
		text, emotion string
		intensity     int
		want          string
	}{
		{"我能！我每天喂它。", EmotionSteady, 1, "我能。我每天喂它。"},
		{"我能！我每天喂它。", EmotionExcited, 1, "我能！我每天喂它。"},
		{"我能！我每天喂它。", EmotionExcited, 0, "我能。我每天喂它。"},
		{"真的吗？！", EmotionTender, 1, "真的吗？"},
		{"那……那你不来看我表演了？", EmotionSad, 2, "那……那你不来看我表演了？"},
		{"我不管，我不要演了。", EmotionSad, 3, "我不管，我不要演了……"},
		{"不是，公司让我出差。", EmotionHesitant, 1, "不是……公司让我出差。"},
		{"我还没写。", EmotionHesitant, 2, "我还没写……"},
		{"明天我演小兔子。", EmotionExcited, 2, "明天我演小兔子！"},
		{"「我演小兔子。」", EmotionExcited, 2, "「我演小兔子！」"},
		{"我用零花钱买猫粮。", EmotionPleading, 2, "我用零花钱买猫粮嘛。"},
		{"可以吗？", EmotionPleading, 3, "可以吗？"},
		{"Fine, I will.", EmotionHesitant, 1, "Fine... I will."},
		{"Look, I did it!", EmotionSteady, 0, "Look, I did it."},
		{"It was here.", EmotionSurprised, 2, "It was here!"},
		{"Hi [laugh] there!", EmotionSteady, 0, "Hi [laugh] there!"},
		{"好！", "", 0, "好！"},
	} {
		if got := SteerText(c.text, c.emotion, c.intensity); got != c.want {
			t.Errorf("SteerText(%q, %s, %d) = %q, want %q", c.text, c.emotion, c.intensity, got, c.want)
		}
	}
	for _, c := range []struct {
		a, b string
		ok   bool
	}{
		{"我能！", "我能。", true}, {"我能。", "我能嘛。", true}, {"我能。", "我不能。", false}, {"我能。", "我。", false}, {"Hi.", "Hi!", true},
	} {
		if steerValid(c.a, c.b) != c.ok {
			t.Errorf("steerValid(%q, %q)", c.a, c.b)
		}
	}
}

// Steering changes the synthesis text, never the sidecar's; the
// steadier preset sets the sampling and a seed per character.
func TestSteerAndSteadierProgram(t *testing.T) {
	cv := func(string) ModelInfo {
		return ModelInfo{Traits: TraitsFor("qwen3_tts", []string{"sampling", "voiceStyle", "instruct", "language"}), Revision: "r",
			Defaults: map[string]ParamDefaults{"": {Temperature: 0.7, TopP: 0.8}}}
	}
	p := ChapterParams{Render: "r", Model: "m/cv", Lang: "zh", Seed: 7, Voices: map[string]core.Take{"k": {Voice: "Vivian"}, "m": {Voice: "Serena"}},
		Lines: []LineSpec{
			{Character: "kid", Emotion: EmotionSteady, Intensity: 1, Spans: []SpanSpec{{Voice: "k", Text: "我能！"}}},
			{Character: "mom", Emotion: EmotionSad, Intensity: 3, Spans: []SpanSpec{{Voice: "m", Text: "我知道了。"}}},
		},
		Production: DefaultStoryProduction(), Output: "/tmp/x.wav"}
	p.Production.Steer = true
	p.Production.SetSteadier(true)
	if !p.Production.Steadier() {
		t.Fatal("preset")
	}
	_, steps := chapterKeyOf(t, p, cv)
	var texts []string
	var seeds []int64
	for _, s := range steps {
		if s.chunk != nil {
			texts = append(texts, s.chunk.params.Text)
			seeds = append(seeds, *s.chunk.params.Seed)
			if *s.chunk.params.Temperature != StoryTemperature || *s.chunk.params.TopP != StoryTopP {
				t.Errorf("sampling %v %v", *s.chunk.params.Temperature, *s.chunk.params.TopP)
			}
		}
	}
	if len(texts) != 2 || texts[0] != "我能。" || texts[1] != "我知道了……" {
		t.Errorf("texts %q", texts)
	}
	if seeds[0] == seeds[1] || seeds[0] != characterSeed(7, "kid") || characterSeed(7, "kid") <= 0 {
		t.Errorf("seeds %v", seeds)
	}
	if p.Lines[0].Spans[0].Text != "我能！" {
		t.Error("the line itself changed")
	}
	// A model without sampling keeps its defaults.
	_, steps = chapterKeyOf(t, p, kokoroInfo)
	if steps[0].chunk.params.Temperature != nil {
		t.Error("kokoro sampled")
	}
	p.Production.SetSteadier(false)
	if p.Production.Temperature != 0 || p.Production.CharacterSeeds {
		t.Error("clear")
	}
}
