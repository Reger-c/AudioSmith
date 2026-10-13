package translate

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/hehehai/AudioSmith/internal/llm"
)

func TestGlossaryMergeAndMatch(t *testing.T) {
	manual := Glossary{{Source: " Acme ", Target: "ACME"}, {Source: "", Target: "x"}, {Source: "Neo", Target: "尼奥"}}
	auto := Glossary{{Source: "acme", Target: "阿克米"}, {Source: "Matrix", Target: "矩阵"}}
	g := manual.Merge(auto)
	if len(g) != 3 || g[0].Target != "ACME" || g[0].Auto || g[2].Source != "Matrix" || !g[2].Auto {
		t.Fatalf("%+v", g)
	}
	// An automatic term listed first still loses to a manual one.
	g2 := Glossary{{Source: "Neo", Target: "auto", Auto: true}, {Source: "neo", Target: "manual"}}.Normalize()
	if len(g2) != 1 || g2[0].Target != "manual" {
		t.Fatal(g2)
	}
	cat := Term{Source: "cat", Target: "gato"}
	if cat.Count("Cat, category, bobcat, cat.") != 2 {
		t.Fatal(cat.Count("Cat, category, bobcat, cat."))
	}
	apple := Term{Source: "Apple", Target: "苹果公司", CaseSensitive: true}
	if apple.Count("an apple from Apple") != 1 {
		t.Fatal("case sensitive")
	}
	zh := Term{Source: "黑客帝国", Target: "The Matrix"}
	if zh.Count("我看了黑客帝国两遍，黑客帝国很好看") != 2 {
		t.Fatal("cjk")
	}
	mixed := Term{Source: "GPU", Target: "显卡"}
	if mixed.Count("用GPU跑模型") != 1 {
		t.Fatal("latin term inside CJK text")
	}
	rel := Glossary{{Source: "York", Target: "a"}, {Source: "New York", Target: "b"}, {Source: "Paris", Target: "c"}}.Relevant("I love New York")
	if len(rel) != 2 || rel[0].Source != "New York" {
		t.Fatal(rel)
	}
}

func TestGlossaryExtract(t *testing.T) {
	m := &model{handle: func(sys, user string) (string, error) {
		if !strings.Contains(sys, "already in the glossary") || !strings.Contains(sys, "- Neo") {
			return "", fmt.Errorf("existing terms not listed")
		}
		return mustJSON(map[string]any{"terms": []map[string]any{
			{"source": "Morpheus", "target": "墨菲斯", "note": "character", "count": 1},
			{"source": "Zion", "target": "锡安", "count": 9},
			{"source": "Trinity", "target": "崔妮蒂"},
			{"source": "neo", "target": "dup of existing"},
			{"source": "Oracle", "target": "先知"}, // not in the transcript
		}}), nil
	}}
	x := &Extractor{Router: router(m)}
	cands, usage, err := x.Extract(context.Background(), "en", "zh-Hans", segs("Morpheus took Neo to Zion.", "Trinity said Zion is safe. Zion!", ""), Glossary{{Source: "Neo", Target: "尼奥"}}, "")
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, c := range cands {
		got = append(got, fmt.Sprintf("%s=%s×%d", c.Source, c.Target, c.Count))
	}
	if strings.Join(got, ",") != "Zion=锡安×3,Morpheus=墨菲斯×1,Trinity=崔妮蒂×1" || !cands[0].Auto || usage.TotalTokens != 15 {
		t.Fatalf("%v", got)
	}
	if _, _, err := (&Extractor{}).Extract(context.Background(), "en", "fr", segs("x"), nil, ""); Code(err) != CodeNoLLM {
		t.Fatal(err)
	}
}

func TestPolish(t *testing.T) {
	cases := []struct{ in, lang, want string }{
		{"um so i think, uh, we should ship it", "en", "So I think, we should ship it."},
		{"  hello   world .  this is great ", "en", "Hello world. This is great."},
		{"i'm sure i'd go. visit example.com today", "en", "I'm sure I'd go. Visit example.com today."},
		{"use iPhone and macOS daily", "en", "Use iPhone and macOS daily."},
		{"Is it done?", "en", "Is it done?"},
		{"嗯，我觉得 这个 方案 可以,我们明天 开会", "zh", "我觉得这个方案可以，我们明天开会。"},
		{"呃 用Python写一个脚本", "zh", "用Python写一个脚本。"},
		{"价格是3.5元", "zh", "价格是3.5元。"},
		{"我们用 GPU 训练", "", "我们用 GPU 训练。"},
		{"", "en", ""},
	}
	for _, c := range cases {
		if got := Polish(c.in, c.lang, PolishOptions{}); got != c.want {
			t.Errorf("Polish(%q) = %q, want %q", c.in, got, c.want)
		}
		if got := Polish(c.want, c.lang, PolishOptions{}); got != c.want {
			t.Errorf("not idempotent: %q → %q", c.want, got)
		}
	}
	if got := Polish("用Python写3个脚本", "zh", PolishOptions{CJKLatinSpace: true}); got != "用 Python 写 3 个脚本。" {
		t.Error(got)
	}
	if got := Polish("um ok", "en", PolishOptions{KeepFillers: true, NoTerminal: true}); got != "Um ok" {
		t.Error(got)
	}
}

func TestCleanup(t *testing.T) {
	m := &model{handle: func(sys, user string) (string, error) {
		if !strings.Contains(sys, "no wait") || !strings.Contains(sys, "Kubernetes") {
			return "", fmt.Errorf("prompt lacks en cues or vocabulary")
		}
		return `{"text": "Let's meet on Friday to discuss the Kubernetes cluster."}`, nil
	}}
	c := &Cleaner{Router: router(m)}
	res := c.Clean(context.Background(), CleanupRequest{
		Text: "um let's meet on thursday no wait friday to discuss the kubernetes cluster", Lang: "en",
		Vocabulary: []string{"Kubernetes", "kubernetes", " "},
	})
	if !res.LLM || res.Fallback != "" || res.Text != "Let's meet on Friday to discuss the Kubernetes cluster." || len(res.Warnings) != 0 {
		t.Fatalf("%+v", res)
	}
	if res.Polished != "Let's meet on thursday no wait friday to discuss the kubernetes cluster." {
		t.Fatalf("polished %q", res.Polished)
	}

	// The model answered instead of cleaning.
	m.handle = func(string, string) (string, error) {
		return `{"text": "The capital of France is Paris, a city known for the Eiffel Tower and great food."}`, nil
	}
	res = c.Clean(context.Background(), CleanupRequest{Text: "what is the capital of france", Lang: "en", Vocabulary: []string{"Kubernetes"}})
	if res.LLM || res.Fallback != FallbackDiverged || res.Text != "What is the capital of france." {
		t.Fatalf("%+v", res)
	}

	// Chinese self-correction.
	m.handle = func(sys, user string) (string, error) {
		if !strings.Contains(sys, "我是说") {
			return "", fmt.Errorf("zh cues missing")
		}
		return `{"text": "我们周五开会。"}`, nil
	}
	res = c.Clean(context.Background(), CleanupRequest{Text: "嗯我们周四开会，不对，我是说周五开会"})
	if !res.LLM || res.Text != "我们周五开会。" {
		t.Fatalf("%+v", res)
	}

	// Budget exceeded.
	start := time.Now()
	res = (&Cleaner{Router: blockingRouter(), Budget: 50 * time.Millisecond}).Clean(context.Background(), CleanupRequest{Text: "this is long enough to clean up", Lang: "en"})
	if res.Fallback != FallbackTimeout || time.Since(start) > time.Second || res.Text != "This is long enough to clean up." {
		t.Fatalf("%+v after %v", res, time.Since(start))
	}

	// No router / too short / disabled.
	if r := (&Cleaner{}).Clean(context.Background(), CleanupRequest{Text: "hello there friend", Lang: "en"}); r.Fallback != FallbackNoLLM || r.Text != "Hello there friend." {
		t.Fatal(r)
	}
	if r := c.Clean(context.Background(), CleanupRequest{Text: "ok", Lang: "en"}); r.Fallback != FallbackTooShort {
		t.Fatal(r)
	}
	if r := c.Clean(context.Background(), CleanupRequest{Text: "long enough text here", NoLLM: true}); r.Fallback != FallbackDisabled {
		t.Fatal(r)
	}
}

// blockingBackend waits for its context.
type blockingBackend struct{}

func (blockingBackend) Info() llm.BackendInfo { return llm.BackendInfo{ID: "block", Local: true} }
func (blockingBackend) Chat(ctx context.Context, _ llm.Request) (llm.Response, error) {
	<-ctx.Done()
	return llm.Response{}, ctx.Err()
}
func (blockingBackend) Models(context.Context) ([]llm.ModelInfo, error) { return nil, nil }

func blockingRouter() *llm.Router {
	reg := llm.NewRegistry(nil, nil)
	reg.Register("block", blockingBackend{})
	r := &llm.Router{Registry: reg}
	r.SetRouting(llm.Routing{Default: []llm.Route{{Provider: "block"}}})
	return r
}

func TestDirectionKeywords(t *testing.T) {
	d := ParseDirection("More urgent, a bit surprised")
	if strings.Join(d.Emotions, ",") != "urgent,surprised" || d.Rate <= 0 || d.Pitch <= 0 || d.Source != "keywords" {
		t.Fatalf("%+v", d)
	}
	if d.Instruct != "Deliver the line urgent and slightly surprised." {
		t.Fatalf("instruct %q", d.Instruct)
	}
	if d.TranslateHint != "short, clipped sentences" {
		t.Fatal(d.TranslateHint)
	}
	d = ParseDirection("less angry, very calm and slower")
	if len(d.Emotions) != 1 || d.Emotions[0] != "calm" || d.Energy >= 0 || d.Rate >= -0.2 {
		t.Fatalf("%+v", d)
	}
	if !strings.Contains(d.Instruct, "less angry") || !strings.Contains(d.Instruct, "very calm") {
		t.Fatal(d.Instruct)
	}
	d = ParseDirection("whisper it, kind of sad")
	if d.Emotions[0] != "whisper" || d.Energy > -0.5 || !strings.Contains(d.Instruct, "slightly sad") {
		t.Fatalf("%+v", d)
	}
	// Chinese: 快乐 is happy, not "fast"; 一点 after a cue weakens it; 不要 negates.
	d = ParseDirection("语速快一点，有点生气，但是很快乐")
	if strings.Join(d.Emotions, ",") != "happy,angry" || math.Abs(d.Rate-(0.1+0.05+0.075)) > 0.01 {
		t.Fatalf("%+v", d)
	}
	d = ParseDirection("不要太大声，温柔一些")
	if d.Energy >= 0 || len(d.Emotions) != 1 || d.Emotions[0] != "tender" {
		t.Fatalf("%+v", d)
	}
	// Unknown words pass through as instruct text.
	d = ParseDirection("like a pirate captain")
	if len(d.Emotions) != 0 || d.Instruct != "like a pirate captain" || d.Rate != 0 {
		t.Fatalf("%+v", d)
	}
	// Clamping.
	d = ParseDirection("very very fast, extremely quick, super rapid, really faster")
	if d.Rate != 0.5 {
		t.Fatal(d.Rate)
	}
}

func TestDirector(t *testing.T) {
	m := &model{handle: func(sys, user string) (string, error) {
		if !strings.Contains(sys, "urgent, whisper") && !strings.Contains(sys, "whisper, shout") {
			return "", fmt.Errorf("taxonomy missing")
		}
		return mustJSON(map[string]any{"emotions": []string{"Fearful", "nervous", "bogus", "neutral"}, "instruct": " Speak in a tense whisper. ", "rate": 0.2, "pitch": 1, "energy": -0.7, "translateHint": "short"}), nil
	}}
	d := (&Director{Router: router(m)}).Parse(context.Background(), "害怕地小声说")
	if d.Source != "llm" || strings.Join(d.Emotions, ",") != "fearful,nervous" || d.Instruct != "Speak in a tense whisper." || d.Energy != -0.7 {
		t.Fatalf("%+v", d)
	}
	// Model failure → keywords.
	m.handle = func(string, string) (string, error) { return "", &llm.Error{Code: llm.CodeAuth} }
	d = (&Director{Router: router(m)}).Parse(context.Background(), "a bit happier")
	if d.Source != "keywords" || d.Fallback != llm.CodeAuth {
		t.Fatalf("%+v", d)
	}
	// Out-of-range numbers fail the schema; after the repair turn it falls back.
	m.handle = func(string, string) (string, error) {
		return `{"emotions":[],"instruct":"x","rate":3,"pitch":0,"energy":0}`, nil
	}
	d = (&Director{Router: router(m)}).Parse(context.Background(), "faster")
	if d.Source != "keywords" || d.Fallback != llm.CodeSchema || d.Rate != 0.2 {
		t.Fatalf("%+v", d)
	}
	if d := (&Director{}).Parse(context.Background(), ""); d.Source != "keywords" || d.Instruct != "" {
		t.Fatal(d)
	}
}

func TestUnitsAndFit(t *testing.T) {
	cases := []struct {
		text, lang string
		units      float64
	}{
		{"Hello world", "en", 3},
		{"The table is made of stone", "en", 7},
		{"I like the USA", "en", 3 + 3.6},
		{"你好世界", "zh", 4},
		{"今日はいい天気", "ja", 1.7*4 + 3},
		{"안녕하세요", "ko", 5},
		{"Hola, ¿cómo estás?", "es", 6},
		{"Привет мир", "ru", 3},
		{"नमस्ते", "hi", 3},
		{"2024", "en", 5.2},
		{"Il parle", "fr", 2},
	}
	for _, c := range cases {
		u, _ := Units(c.text, c.lang)
		if math.Abs(u-c.units) > 0.01 {
			t.Errorf("Units(%q, %s) = %.2f, want %.2f", c.text, c.lang, u, c.units)
		}
	}
	// Pauses: comma, sentence breaks (not the final one), [pause], non-verbal; [[override]] is spoken as its respelling.
	u, p := Units("Wait, stop. Now! [pause 500ms] [laughter] [[GIF|jif]]", "en")
	if math.Abs(p-(PauseComma+2*PauseSentence+0.5+PauseNonVerbal)) > 0.001 || u != 4 {
		t.Fatalf("units %.2f pause %.2f", u, p)
	}
	_, p = Units("Done.", "en")
	if p != 0 {
		t.Fatal(p)
	}
	pr := &Predictor{}
	d := pr.Duration("你好世界", "zh", 1)
	if math.Abs(d-4/4.5) > 0.001 {
		t.Fatal(d)
	}
	if d2 := pr.Duration("你好世界", "zh", 2); math.Abs(d2-d/2) > 0.001 {
		t.Fatal(d2)
	}
	f := pr.Fit("你好世界", "zh-Hans", 2, 1)
	if f.Verdict != FitOK || f.Over != 0 {
		t.Fatal(f)
	}
	f = pr.Fit("你好世界", "zh", 0.85, 1)
	if f.Verdict != FitTight {
		t.Fatal(f)
	}
	f = pr.Fit("你好世界你好世界", "zh", 1, 1)
	if f.Verdict != FitOverflow || math.Abs(f.Over-(8/4.5-1)) > 0.01 {
		t.Fatal(f)
	}
	if f := pr.Fit("x", "en", 0, 1); f.Verdict != FitUnknown {
		t.Fatal(f)
	}
	// Overrides and calibration.
	pr2 := &Predictor{Rates: map[string]Rate{"zh": {PerSecond: 9, CharsPerUnit: 1}}}
	if d := pr2.Duration("你好世界你好世界你", "zh", 1); math.Abs(d-1) > 0.001 {
		t.Fatal(d)
	}
	before := pr.RateFor("en").PerSecond
	pr.Calibrate("This sentence has exactly nine syllables in it", "en-US", 1.5, 1) // 9 units in 1.5 s → 6/s
	after := pr.RateFor("en").PerSecond
	if after <= before || math.Abs(after-(0.7*before+0.3*(10.0/1.5))) > 0.5 {
		t.Fatalf("before %.2f after %.2f", before, after)
	}
	pr.ResetCalibration()
	if pr.RateFor("en").PerSecond != before {
		t.Fatal("reset")
	}
	units, chars := pr.Budget("es", 2, 1, 0.2)
	if units != 9 || chars != 23 {
		t.Fatalf("budget %d %d", units, chars)
	}
}

func TestLangParsing(t *testing.T) {
	cases := map[string][2]string{
		"zh-TW":      {"zh-Hant", "Chinese (Traditional, Taiwan)"},
		"zh-Hant-HK": {"zh-Hant", "Chinese (Traditional, Hong Kong)"},
		"zh":         {"zh-Hans", "Chinese (Simplified)"},
		"zh-CN":      {"zh-Hans", "Chinese (Simplified, Mainland China)"},
		"pt-BR":      {"pt-BR", "Portuguese (Brazil)"},
		"es-419":     {"es-419", "Spanish (Latin America)"},
		"en":         {"en", "English"},
		"cmn":        {"zh-Hans", "Chinese (Simplified)"},
	}
	for tag, want := range cases {
		l, ok := parseLang(tag)
		if !ok || l.Variant() != want[0] || l.Describe() != want[1] {
			t.Errorf("%s: %q %q", tag, l.Variant(), l.Describe())
		}
	}
	if _, ok := parseLang("xx-nope"); ok {
		t.Error("unknown accepted")
	}
	if n := langNotes("notes:", mustLang("es-AR")); !strings.Contains(n, "neutral international Spanish") {
		t.Error("es-AR should fall back to es notes:", n)
	}
	if n := langNotes("notes:", mustLang("zh-HK")); !strings.Contains(n, "Hong Kong") {
		t.Error(n)
	}
}

func mustLang(tag string) Lang { l, _ := parseLang(tag); return l }

func TestPromptsRender(t *testing.T) {
	for _, name := range []string{"translate", "reflect", "adapt", "fit", "glossary", "cleanup", "direction"} {
		for _, suffix := range []string{".system", ".user"} {
			if !has(name + suffix) {
				t.Errorf("missing template %s%s", name, suffix)
			}
		}
	}
	d := promptData{Source: mustLang("en"), SourceKnown: true, Target: mustLang("ja"), Lines: []lineIn{{ID: "1", Text: "Hi <b>&"}}, Formality: FormalityFormal}
	d.TargetNotes = langNotes("notes:", d.Target)
	msgs, err := messages("translate", d)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(msgs[0].Content, "politeness") || !strings.Contains(msgs[0].Content, "formal / polite") || !strings.Contains(msgs[1].Content, `"Hi <b>&"`) {
		t.Fatalf("%s\n---\n%s", msgs[0].Content, msgs[1].Content)
	}
	var lines []lineIn
	raw := msgs[1].Content[strings.Index(msgs[1].Content, "["):]
	if err := json.Unmarshal([]byte(raw), &lines); err != nil || lines[0].Text != "Hi <b>&" {
		t.Fatal(err, raw)
	}
}

func TestPolishLanguageScoped(t *testing.T) {
	if got := Polish("er kommt morgen", "de", PolishOptions{}); got != "Er kommt morgen." {
		t.Error(got)
	}
	if got := Polish("嗯 um 我们用 uh Python", "zh", PolishOptions{}); got != "我们用 Python。" {
		t.Error(got)
	}
}

func TestLLMFatalStops(t *testing.T) {
	m := &model{handle: func(string, string) (string, error) { return "", &llm.Error{Code: llm.CodeAuth} }}
	res, err := (&LLM{Router: router(m)}).Translate(context.Background(), Request{Target: "it", Segments: segs("a", "b", "c")})
	if llm.Code(err) != llm.CodeAuth || len(m.history()) != 1 || len(res.Segments) != 3 {
		t.Fatalf("%v after %d calls", err, len(m.history()))
	}
}
