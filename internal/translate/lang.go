package translate

import (
	"strings"

	"golang.org/x/text/language"

	"github.com/hehehai/AudioSmith/internal/lang"
)

// Lang is a parsed BCP-47 tag: the table language plus script and region.
type Lang struct {
	Tag    string // as given, canonicalised by x/text ("zh-Hant-TW")
	Code   string // internal/lang code ("zh")
	Name   string // English name ("Chinese")
	Native string // endonym ("中文"), may be ""
	Script string // explicit or usual script ("Hant")
	Region string // explicit region ("TW"), "" when none
	RTL    bool
}

// parseLang parses tag; ok is false for unknown languages.
func parseLang(tag string) (Lang, bool) {
	tag = strings.TrimSpace(tag)
	if tag == "" {
		return Lang{}, false
	}
	code := lang.Canonical(tag)
	if code == "" {
		return Lang{}, false
	}
	l, _ := lang.Lookup(code)
	out := Lang{Tag: tag, Code: code, Name: l.Name, Native: l.Native, Script: l.Script, RTL: l.RTL}
	if t, err := language.Parse(tag); err == nil {
		out.Tag = t.String()
		_, s, r := t.Raw()
		if sc := s.String(); sc != "Zzzz" && sc != "" {
			if _, conf := t.Script(); conf == language.Exact {
				out.Script = sc
			}
		}
		if _, conf := t.Region(); conf == language.Exact {
			out.Region = r.String()
		}
	}
	// zh-TW / zh-HK / zh-MO imply Traditional script.
	if out.Code == "zh" && out.Script == "Hans" && (out.Region == "TW" || out.Region == "HK" || out.Region == "MO") && !strings.Contains(strings.ToLower(tag), "hans") {
		out.Script = "Hant"
	}
	return out, true
}

// Variant is the dialect key used for prompt notes and MT codes:
// "zh-Hans", "zh-Hant", "pt-BR", "es-MX", "en-GB"… or the bare code.
func (l Lang) Variant() string {
	switch l.Code {
	case "zh":
		if l.Script == "Hant" {
			return "zh-Hant"
		}
		return "zh-Hans"
	case "sr":
		if l.Script == "Latn" {
			return "sr-Latn"
		}
		return "sr"
	}
	if l.Region != "" {
		return l.Code + "-" + l.Region
	}
	return l.Code
}

// regionNames covers the regions dialect choices use.
var regionNames = map[string]string{
	"US": "United States", "GB": "United Kingdom", "AU": "Australia", "CA": "Canada", "IN": "India", "IE": "Ireland", "NZ": "New Zealand", "ZA": "South Africa",
	"MX": "Mexico", "ES": "Spain", "AR": "Argentina", "CO": "Colombia", "CL": "Chile", "PE": "Peru", "419": "Latin America",
	"BR": "Brazil", "PT": "Portugal", "AO": "Angola", "MZ": "Mozambique",
	"FR": "France", "BE": "Belgium", "CH": "Switzerland",
	"CN": "Mainland China", "TW": "Taiwan", "HK": "Hong Kong", "MO": "Macau", "SG": "Singapore", "MY": "Malaysia",
	"DE": "Germany", "AT": "Austria", "NL": "Netherlands",
	"SA": "Saudi Arabia", "EG": "Egypt", "MA": "Morocco", "AE": "United Arab Emirates", "LB": "Lebanon", "SY": "Syria", "IQ": "Iraq", "JO": "Jordan",
}

// Describe is the language as written for prompts: "Spanish (Mexico)",
// "Chinese (Traditional, Taiwan)", "Japanese".
func (l Lang) Describe() string {
	var q []string
	if l.Code == "zh" || l.Code == "sr" {
		switch l.Script {
		case "Hant":
			q = append(q, "Traditional")
		case "Hans":
			q = append(q, "Simplified")
		case "Latn":
			q = append(q, "Latin script")
		case "Cyrl":
			q = append(q, "Cyrillic script")
		}
	}
	if l.Region != "" {
		if n, ok := regionNames[l.Region]; ok {
			q = append(q, n)
		} else {
			q = append(q, l.Region)
		}
	}
	name := l.Name
	if name == "" {
		name = l.Tag
	}
	if len(q) == 0 {
		return name
	}
	return name + " (" + strings.Join(q, ", ") + ")"
}

// Unspaced reports whether the language is written without spaces between words.
func (l Lang) Unspaced() bool {
	switch l.Code {
	case "zh", "ja", "th", "lo", "km", "my", "bo", "yue", "wuu":
		return true
	}
	return false
}
