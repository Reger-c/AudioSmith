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
	if (Attributes{Age: "child"}).Register() != RegisterChild || (Attributes{}).Register() != RegisterAdult {
		t.Error("registers")
	}
}
