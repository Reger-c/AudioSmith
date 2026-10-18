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

var refTexts map[string]map[string][]string

func init() {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(refTextsJSON, &raw); err != nil {
		panic("voicedesign: reftexts.json: " + err.Error()) // caught by tests
	}
	refTexts = map[string]map[string][]string{}
	for lang, v := range raw {
		if strings.HasPrefix(lang, "_") {
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

// RefLang is the language of the reference texts for speech in lang:
// Chinese for Chinese, English otherwise.
func RefLang(lang string) string {
	if strings.HasPrefix(strings.ToLower(lang), "zh") {
		return "zh"
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

// Register is the register of the reference texts for a voice with
// attributes a.
func (a Attributes) Register() string {
	if a.Age == "child" {
		return RegisterChild
	}
	return RegisterAdult
}
