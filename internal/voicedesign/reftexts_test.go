package voicedesign

import (
	"strings"
	"testing"

	"github.com/hehehai/AudioSmith/internal/text"
)

// TestReferenceTexts: every register has 3–4 texts of 10–14 s (Chinese
// 45–60 characters, English 30–40 words), lively (a question or an
// exclamation), and none talks about the recording.
func TestReferenceTexts(t *testing.T) {
	for _, lang := range []string{"zh", "en"} {
		for _, reg := range []string{RegisterAdult, RegisterChild} {
			ts := ReferenceTexts(lang, reg)
			if len(ts) < 3 || len(ts) > 4 {
				t.Errorf("%s/%s: %d texts", lang, reg, len(ts))
			}
			for _, s := range ts {
				w, c := text.Count(s)
				if lang == "zh" && (c < 45 || c > 60) || lang == "en" && (w < 30 || w > 40) {
					t.Errorf("%s/%s: %d words, %d characters: %s", lang, reg, w, c, s)
				}
				if strings.ContainsAny(s, "嗯呃啊呀嘛哈啦呢哎") || strings.Contains(s, "……") || strings.Contains(strings.ToLower(s), " um") || strings.Contains(strings.ToLower(s), " uh") {
					t.Errorf("%s/%s: fillers: %s", lang, reg, s)
				}
				if !strings.ContainsAny(s, "?？!！") {
					t.Errorf("%s/%s: flat: %s", lang, reg, s)
				}
				for _, meta := range []string{"录音", "参考", "克隆", "声音", "record", "clone", "voice"} {
					if strings.Contains(strings.ToLower(s), meta) {
						t.Errorf("%s/%s: talks about %q: %s", lang, reg, meta, s)
					}
				}
			}
		}
	}
	if ReferenceTexts("zh-Hans", RegisterAdult)[0] != ReferenceTexts("zh", RegisterAdult)[0] || RefLang("ja") != "en" {
		t.Error("languages")
	}
	if ConversationalStyle("zh") != "说话随意自然，像日常聊天，不是在朗读。" || !strings.HasPrefix(ConversationalStyle("en"), "Speaking casually") {
		t.Error("style")
	}
	if got := WithStyle("一位年轻妈妈，声音温柔", ConversationalStyle("zh")); got != "一位年轻妈妈，声音温柔。说话随意自然，像日常聊天，不是在朗读。" {
		t.Errorf("zh: %q", got)
	}
	if got := WithStyle("A warm young mother.", ConversationalStyle("en")); got != "A warm young mother. Speaking casually and naturally, like in an everyday chat, not reading aloud." {
		t.Errorf("en: %q", got)
	}
	if (Attributes{Age: "child"}).Register() != RegisterChild || (Attributes{}).Register() != RegisterAdult {
		t.Error("registers")
	}
}
