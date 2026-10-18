package dub

import "testing"

// TestCheckSpoken: thread #90's lines, as Qwen3-ASR heard them with the
// language forced to the target's.
func TestCheckSpoken(t *testing.T) {
	cases := []struct {
		lang, text, heard string
		ok                bool
		foreign           string
	}{
		{"zh", "差不多了。我还是找不到我的雨衣。", "差不多了，我还是找不到我的雨衣。", true, ""},
		{"zh", "真的吗？", "真的吗", true, ""},
		// Japanese heard as Japanese although asked for Chinese.
		{"zh", "带点零食就行。我姐姐在给大家做三明治。", "大田前司上司、ワセジェザケダガーゾ三名氏。", false, "kana"},
		// Japanese heard as Chinese nonsense: only the error rate tells.
		{"zh", "我好像上个月把它落在你车里了。", "瓦霍山上古埃帕塔罗杰尼查里拉。", false, ""},
		{"zh", "嘿，明天出门的行李你收拾好了吗？", "메일 짐은 다 쌌어?", false, "hangul"},
		{"en", "Almost. I still can't find my rain jacket.", "Almost, I still can't find my rain jacket.", true, ""},
		{"en", "Really?", "本当に？", false, "han"},
		{"ja", "本当に？", "本当に", true, ""},
		{"ko", "정말?", "정말", true, ""},
		// A brand name in Latin letters is fine in Chinese.
		{"zh", "我的 iPhone 没电了。", "我的iPhone没电了。", true, ""},
	}
	for _, c := range cases {
		s := CheckSpoken(c.lang, c.text, c.heard)
		if s.OK() != c.ok || s.Foreign != c.foreign {
			t.Errorf("%s %q heard %q: %+v ok %v, want ok %v foreign %q", c.lang, c.text, c.heard, s, s.OK(), c.ok, c.foreign)
		}
	}
	if ForeignScript("xx", "本当に") != "" {
		t.Error("unknown language: no verdict")
	}
}

func TestSpokenWorse(t *testing.T) {
	good, drift, foreign := Spoken{Drift: 0.1}, Spoken{Drift: 0.6}, Spoken{Drift: 0.2, Foreign: "kana"}
	if !drift.Worse(good) || good.Worse(drift) || !foreign.Worse(drift) || drift.Worse(foreign) || good.Worse(good) {
		t.Error("order: wrong script, then the error rate")
	}
}
