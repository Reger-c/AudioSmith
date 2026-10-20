package voicedesign

import (
	"strings"
	"testing"

	"github.com/hehehai/AudioSmith/internal/text"
)

// refTextBounds are the sizes of 10–14 s of everyday speech per language
// (characters for Chinese and Japanese, else words), and words that talk
// about the recording itself.
var refTextBounds = map[string]struct {
	lo, hi int
	chars  bool
	meta   []string
}{
	"zh": {45, 60, true, []string{"录音", "参考", "克隆", "声音"}},
	"en": {30, 40, false, []string{"record", "clone", "voice"}},
	"ja": {55, 80, true, []string{"録音", "声", "クローン"}},
	"ko": {18, 30, false, []string{"녹음", "목소리", "복제"}},
	"de": {26, 38, false, []string{"aufnahme", "stimme", "klon"}},
	"fr": {30, 42, false, []string{"enregistr", "voix", "clon"}},
	"es": {30, 40, false, []string{"grabac", "voz", "clon"}},
	"it": {30, 40, false, []string{"registra", "voce", "clon"}},
	"pt": {30, 40, false, []string{"grava", "voz", "clon"}},
	"ru": {24, 34, false, []string{"запис", "голос", "клон"}},
}

// TestReferenceTexts: every language Qwen3-TTS speaks has 3–4 texts per
// register of 10–14 s, lively (a question or an exclamation), without
// fillers, and none talks about the recording.
func TestReferenceTexts(t *testing.T) {
	fillers := map[string][]string{
		"zh": {"嗯", "呃", "啊", "呀", "嘛", "哈", "啦", "呢", "哎", "……"},
		"en": {" um", " uh", "like,"}, "ja": {"えーと", "えっと", "あのー", "まあ"}, "ko": {"음,", "어,", "그러니까"},
		"de": {"äh", "ähm", "halt "}, "fr": {"euh", "bah", "ben "}, "es": {" eh,", "pues", "este,"},
		"it": {"ehm", "cioè", "allora,"}, "pt": {" né", "tipo,", " é..."}, "ru": {"ну,", " э,", "вот,"},
	}
	if len(refTexts) != len(refTextBounds) {
		t.Errorf("%d languages", len(refTexts))
	}
	for lang, b := range refTextBounds {
		if RefLang(lang) != lang {
			t.Errorf("%s: RefLang %s", lang, RefLang(lang))
		}
		for _, reg := range []string{RegisterAdult, RegisterChild} {
			ts := refTexts[lang][reg]
			if len(ts) < 3 || len(ts) > 4 {
				t.Errorf("%s/%s: %d texts", lang, reg, len(ts))
			}
			for _, s := range ts {
				w, c := text.Count(s)
				if n := map[bool]int{true: c, false: w}[b.chars]; n < b.lo || n > b.hi {
					t.Errorf("%s/%s: %d words, %d characters: %s", lang, reg, w, c, s)
				}
				for _, f := range fillers[lang] {
					if strings.Contains(strings.ToLower(s), f) {
						t.Errorf("%s/%s: filler %q: %s", lang, reg, f, s)
					}
				}
				if !strings.ContainsAny(s, "?？!！") {
					t.Errorf("%s/%s: flat: %s", lang, reg, s)
				}
				for _, meta := range b.meta {
					if strings.Contains(strings.ToLower(s), meta) {
						t.Errorf("%s/%s: talks about %q: %s", lang, reg, meta, s)
					}
				}
				if RefTextLang(s) != lang {
					t.Errorf("%s: RefTextLang %q", s, RefTextLang(s))
				}
			}
		}
	}
	// Regions and scripts take their language's texts; others English.
	for in, want := range map[string]string{"zh-Hans": "zh", "zh_TW": "zh", "ja-JP": "ja", "pt-BR": "pt", "DE": "de", "hi": "en", "": "en"} {
		if got := RefLang(in); got != want {
			t.Errorf("RefLang(%q) = %q", in, got)
		}
	}
	if ReferenceTexts("ko", RegisterChild)[0] != refTexts["ko"][RegisterChild][0] || ReferenceTexts("hi", RegisterAdult)[0] != refTexts["en"][RegisterAdult][0] {
		t.Error("texts by language")
	}
	if RefTextLang("Something else.") != "" {
		t.Error("RefTextLang of another text")
	}
	if ConversationalStyle("zh") != "说话随意自然，像日常聊天，不是在朗读。" || !strings.HasPrefix(ConversationalStyle("en"), "Speaking casually") ||
		ConversationalStyle("de") != ConversationalStyle("en") {
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
