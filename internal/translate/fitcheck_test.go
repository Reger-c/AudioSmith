package translate

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

// TestFitRejected uses Qwen3 4B's English → Chinese Fast translations and
// Autofit lines recorded by thread #44.
func TestFitRejected(t *testing.T) {
	zh, _ := parseLang("zh-Hans")
	en, _ := parseLang("en")
	for _, c := range []struct {
		orig, cand string
		tgt        Lang
		budget     float64
		want       string
	}{
		{"山上的雪融化，水位几乎一夜之间就上涨了。", "山上的雪化了，水 overnight 就涨起来。", zh, 14, fitRejectMixed},
		{"你是怎么熬过那一年的？", "How熬过去？", zh, 8, fitRejectMixed},
		{"The soup needs to be simmered for eight hours, and the noodles must be hand-pulled.", "The soup needs to be熬 eight hours, noodles hand-pulled.", en, 16, fitRejectMixed},
		{"这发生在2021年。我们至今还谈论它。", "至今还谈。", zh, 14, fitRejectTooShort},
		// Not caught: a fragment of about the slot's length (#44's
		// 「2021年至今还谈。」 for a 2.6 s slot); the rewrite prompt must do.
		{"这发生在2021年。我们至今还谈论它。", "2021年至今还谈。", zh, 11, ""},
		{"那么，过去十年里发生了什么变化？", "", zh, 10, fitRejectEmpty},
		// Accepted: good shortenings, names and terms the translation had.
		{"那么，过去十年里发生了什么变化？", "过去十年变了什么？", zh, 10, ""},
		{"你是怎么熬过那一年的？", "那一年怎么熬过来的？", zh, 8, ""},
		{"我们用AudioSmith生成了配音，然后发到了B站上。", "我们用AudioSmith配音，发到了B站。", zh, 14, ""},
		{"I started learning at sixteen, and now it's almost sixty years.", "I started at sixteen; it's been almost sixty years.", en, 12, ""},
		{"A regular next to me said he's been eating here since primary school.", "A regular said he's eaten here since he was a kid.", en, 12, ""},
	} {
		if got := fitRejected(c.orig, c.cand, c.tgt, c.budget); got != c.want {
			t.Errorf("%q → %q: %q, want %q", c.orig, c.cand, got, c.want)
		}
	}
}

// TestLLMAutofitLocal: with a local model, Autofit's first pass is a plain
// translation (no budgets), overflowing lines are rewritten, and a broken
// rewrite keeps the full translation.
func TestLLMAutofitLocal(t *testing.T) {
	full := map[string]string{
		"1": "山上的雪融化，水位几乎一夜之间就上涨了，农民们都很高兴。",
		"2": "那么，过去十年里发生了什么变化？我们一起来看看吧。",
		"3": "好。",
	}
	short := map[string]string{"1": "山上的雪化了，水 overnight 就涨起来。", "2": "过去十年，变化不少。"}
	var fitIDs []string
	m := &model{local: true}
	m.handle = func(sys, user string) (string, error) {
		var lines []lineJSON
		switch {
		case strings.HasPrefix(sys, "You are the dialogue translator"):
			if strings.Contains(sys, "maxSeconds") {
				return "", fmt.Errorf("budgets shown to a local model")
			}
			jsonAfterNoT(user, "Lines to translate:\n", &lines)
			return linesReply(lines, func(l lineJSON) string { return full[l.ID] }), nil
		case strings.HasPrefix(sys, "You tighten dubbed dialogue"):
			jsonAfterNoT(user, "Lines to shorten:\n", &lines)
			for _, l := range lines {
				fitIDs = append(fitIDs, l.ID)
			}
			return linesReply(lines, func(l lineJSON) string { return short[l.ID] }), nil
		}
		return "", fmt.Errorf("unexpected %q", sys[:30])
	}
	tr := &LLM{Router: router(m)}
	res, err := tr.Translate(context.Background(), Request{Source: "en", Target: "zh-Hans", Quality: Autofit, Segments: []Segment{
		{ID: "1", Text: "Snow on the mountains melts, and the water rises almost overnight.", Slot: 2.5},
		{ID: "2", Text: "So what changed in the last ten years?", Slot: 2.2},
		{ID: "3", Text: "Okay.", Slot: 1},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(fitIDs, ",") != "1,2" {
		t.Fatalf("rewritten %v", fitIDs)
	}
	s1, s2, s3 := res.Segments[0], res.Segments[1], res.Segments[2]
	if s1.Text != full["1"] || s1.Draft != "" || !hasWarning(s1.Warnings, WarnFitFailed) {
		t.Errorf("mixed rewrite accepted: %+v", s1)
	}
	if s2.Text != short["2"] || s2.Draft != full["2"] || hasWarning(s2.Warnings, WarnFitFailed) {
		t.Errorf("good rewrite: %+v", s2)
	}
	if s3.Text != "好。" || len(s3.Warnings) != 0 {
		t.Errorf("short line: %+v", s3)
	}
}

func hasWarning(ws []string, w string) bool {
	for _, x := range ws {
		if x == w {
			return true
		}
	}
	return false
}
