package voicedesign

import (
	_ "embed"
	"encoding/json"
	"strings"
)

// The reference texts (reftexts.json, thread #83): what a designed or
// built-in voice says to make its reference, the clip that clone and
// dialogue models copy it from. A voice's sample keeps its greeting for
// previews; the reference is longer (10–14 s) and livelier, because those
// models copy a reference's intonation and pace (#81).
//
//go:embed reftexts.json
var refTextsJSON []byte

var (
	refTexts map[string]map[string][]string
	refStyle map[string]string
)

func init() {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(refTextsJSON, &raw); err != nil {
		panic("voicedesign: reftexts.json: " + err.Error()) // caught by tests
	}
	refTexts = map[string]map[string][]string{}
	if err := json.Unmarshal(raw["style"], &refStyle); err != nil {
		panic("voicedesign: reftexts.json: style: " + err.Error())
	}
	for lang, v := range raw {
		if strings.HasPrefix(lang, "_") || lang == "style" {
			continue
		}
		var m map[string][]string
		if err := json.Unmarshal(v, &m); err != nil {
			panic("voicedesign: reftexts.json: " + lang + ": " + err.Error())
		}
		refTexts[lang] = m
	}
}

// Registers of the reference texts.
const (
	RegisterAdult = "adult"
	RegisterChild = "child"
)

// RefLang is the language of the reference texts for speech in lang: its
// own when reftexts.json has texts in it (zh, en, ja, ko, de, fr, es, it,
// pt, ru: the languages Qwen3-TTS speaks), English otherwise.
func RefLang(lang string) string {
	l := strings.ToLower(strings.TrimSpace(lang))
	if i := strings.IndexAny(l, "-_"); i >= 0 {
		l = l[:i]
	}
	if _, ok := refTexts[l]; ok {
		return l
	}
	return "en"
}

// ReferenceTexts are the reference texts for speech in lang, in register
// (RegisterChild for a child's voice, else RegisterAdult).
func ReferenceTexts(lang, register string) []string {
	m := refTexts[RefLang(lang)]
	if ts := m[register]; len(ts) > 0 {
		return append([]string(nil), ts...)
	}
	return append([]string(nil), m[RegisterAdult]...)
}

// RefTextLang is the language of reference text s ("" when s is not one
// of them): what a reference made from it says, for the recognizer.
func RefTextLang(s string) string {
	s = strings.TrimSpace(s)
	for lang, m := range refTexts {
		for _, ts := range m {
			for _, t := range ts {
				if t == s {
					return lang
				}
			}
		}
	}
	return ""
}

// ConversationalStyle is the delivery a voice's reference takes ask for,
// in lang (Chinese, else English: the languages descriptions and
// instructions are written in): casual, like an everyday chat, not read
// (#81's group D, which the user preferred by ear).
func ConversationalStyle(lang string) string {
	if s, ok := refStyle[RefLang(lang)]; ok {
		return s
	}
	return refStyle["en"]
}

// WithStyle is a free-form voice description (or a speaker's
// instruction) with style added.
func WithStyle(desc, style string) string {
	desc = strings.TrimSpace(desc)
	switch {
	case desc == "":
		return style
	case strings.HasSuffix(desc, "。") || strings.HasSuffix(desc, ".") || strings.HasSuffix(desc, "，") || strings.HasSuffix(desc, ","):
		if TextLang(style) == "en" && !strings.HasSuffix(desc, " ") {
			return desc + " " + style
		}
		return desc + style
	case TextLang(style) == "zh":
		return desc + "。" + style
	}
	return desc + ". " + style
}

// Register is the register of the reference texts for a voice with
// attributes a.
func (a Attributes) Register() string {
	if a.Age == "child" {
		return RegisterChild
	}
	return RegisterAdult
}
