package voicedesign

// Lowering a recipe for Qwen3-TTS VoiceDesign: a free-form English or
// Chinese description; split from lower.go.

import "strings"

func lowerFreeform(r Recipe, o Options) Lowered {
	lang := pickLang(r, o)
	if r.Description == "" {
		return Lowered{Voice: Sentence(r.Attrs, lang), Lang: lang, Used: allValues(r.Attrs)}
	}
	// The description as written, plus the explicit picks. When a pick
	// contradicts the description (it says "deep", the user picked high),
	// sending both would confuse the model: the attributes are written out
	// in full instead, and the words the parser couldn't map follow as
	// context so nothing the user wrote is lost.
	lang = TextLang(r.Description)
	parsed := Parse(r.Description)
	picks := r.Attrs.Diff(parsed.Attrs)
	if len(picks) == 0 && len(parsed.Attrs.Diff(r.Attrs)) == 0 {
		return Lowered{Voice: r.Description, Lang: lang, Used: allValues(r.Attrs)}
	}
	if len(parsed.Attrs.Diff(r.Attrs)) == 0 {
		// only additions: append them
		var extra Attributes
		for _, p := range picks {
			extra, _, _ = extra.With(p.Category, p.ID)
		}
		return Lowered{Voice: joinSentences(r.Description, Sentence(extra, lang), lang), Lang: lang, Used: allValues(r.Attrs)}
	}
	voice := Sentence(r.Attrs, lang)
	if terms := parsed.Unmatched; len(terms) > 0 {
		words := make([]string, len(terms))
		for i, t := range terms {
			words[i] = t.Text
		}
		if lang == "zh" {
			voice = joinSentences(voice, "补充："+strings.Join(words, "、")+"。", lang)
		} else {
			voice = joinSentences(voice, "Context: "+strings.Join(words, ", ")+".", lang)
		}
	}
	return Lowered{Voice: voice, Lang: lang, Used: allValues(r.Attrs)}
}

func joinSentences(a, b, lang string) string {
	a, b = strings.TrimSpace(a), strings.TrimSpace(b)
	if a == "" || b == "" {
		return a + b
	}
	if r := []rune(a); !strings.ContainsRune(".!?。！？", r[len(r)-1]) {
		if lang == "zh" {
			a += "。"
		} else {
			a += "."
		}
	}
	if lang == "zh" {
		return a + b
	}
	return a + " " + b
}

// Sentence describes attributes as one natural sentence (two at most) in
// English or Chinese, the way Qwen3-TTS VoiceDesign expects. Auto
// attributes give "".
func Sentence(a Attributes, lang string) string {
	a = a.Canonical()
	if a.IsAuto() {
		return ""
	}
	if lang == "zh" {
		return sentenceZH(a)
	}
	return sentenceEN(a)
}

func phrase(c Category, id, lang string) string {
	op, _ := Lookup(c, id)
	if lang == "zh" {
		return op.Phrase.Zh
	}
	return op.Phrase.En
}

func nounEN(a Attributes) (noun, pronoun string) {
	g := map[string]int{"male": 0, "female": 1, "": 2}[a.Gender]
	table := map[string][3]string{
		"child":       {"young boy", "young girl", "child"},
		"teen":        {"teenage boy", "teenage girl", "teenager"},
		"young_adult": {"young man", "young woman", "young adult"},
		"middle_aged": {"middle-aged man", "middle-aged woman", "middle-aged person"},
		"elderly":     {"elderly man", "elderly woman", "elderly person"},
		"":            {"man", "woman", ""},
	}
	pronoun = [3]string{"He", "She", "They"}[g]
	return table[a.Age][g], pronoun
}

func joinEN(xs []string) string {
	switch len(xs) {
	case 0:
		return ""
	case 1:
		return xs[0]
	case 2:
		return xs[0] + " and " + xs[1]
	}
	return strings.Join(xs[:len(xs)-1], ", ") + " and " + xs[len(xs)-1]
}

func article(w string) string {
	if w != "" && strings.ContainsRune("aeiouAEIOU", rune(w[0])) {
		return "An"
	}
	return "A"
}

func sentenceEN(a Attributes) string {
	var adjs []string
	whisper := false
	for _, s := range a.Styles {
		if s == "whisper" {
			whisper = true
			continue
		}
		adjs = append(adjs, phrase(Style, s, "en"))
	}
	noun, pronoun := nounEN(a)
	var first string
	var with []string
	if a.Pitch != "" {
		with = append(with, "a "+phrase(Pitch, a.Pitch, "en")+" voice")
	}
	if a.Accent != "" {
		with = append(with, phrase(Accent, a.Accent, "en"))
	}
	if noun != "" {
		head := noun
		if len(adjs) > 0 {
			head = strings.Join(adjs, ", ") + " " + noun
		}
		first = article(head) + " " + head
		if len(with) > 0 {
			first += " with " + joinEN(with)
		}
	} else {
		// no gender/age: talk about the voice itself
		head := "voice"
		pre := adjs
		if a.Pitch != "" {
			pre = append([]string{phrase(Pitch, a.Pitch, "en")}, adjs...)
		}
		if len(pre) > 0 {
			head = strings.Join(pre, ", ") + " voice"
		}
		first = article(head) + " " + head
		if a.Accent != "" {
			first += " with " + phrase(Accent, a.Accent, "en")
		}
		pronoun = "It"
	}
	if a.Dialect != "" {
		first += ", speaking Mandarin with " + phrase(Dialect, a.Dialect, "en")
	}
	first += "."
	var how []string
	if a.Pace != "" {
		how = append(how, phrase(Pace, a.Pace, "en"))
	}
	if whisper {
		how = append(how, "in a whisper")
	}
	speak, sound := " speaks ", " sounds "
	if pronoun == "They" {
		speak, sound = " speak ", " sound "
	}
	switch {
	case len(how) > 0 && a.Emotion != "":
		return first + " " + pronoun + speak + strings.Join(how, ", ") + ", sounding " + phrase(Emotion, a.Emotion, "en") + "."
	case len(how) > 0:
		return first + " " + pronoun + speak + strings.Join(how, ", ") + "."
	case a.Emotion != "":
		return first + " " + pronoun + sound + phrase(Emotion, a.Emotion, "en") + "."
	}
	return first
}

func nounZH(a Attributes) string {
	g := map[string]int{"male": 0, "female": 1, "": 2}[a.Gender]
	table := map[string][3]string{
		"child":       {"小男孩", "小女孩", "儿童"},
		"teen":        {"少年", "少女", "青少年"},
		"young_adult": {"青年男性", "青年女性", "年轻人"},
		"middle_aged": {"中年男性", "中年女性", "中年人"},
		"elderly":     {"老年男性", "老年女性", "老人"},
		"":            {"男性", "女性", ""},
	}
	return table[a.Age][g]
}

func sentenceZH(a Attributes) string {
	var parts []string
	if n := nounZH(a); n != "" {
		parts = append(parts, "一位"+n)
	}
	if a.Pitch != "" {
		parts = append(parts, "声音"+phrase(Pitch, a.Pitch, "zh"))
	}
	var st []string
	whisper := false
	for _, s := range a.Styles {
		if s == "whisper" {
			whisper = true
			continue
		}
		st = append(st, phrase(Style, s, "zh"))
	}
	if len(st) > 0 {
		lead := "音色"
		if len(parts) == 0 && a.Pitch == "" {
			lead = "声音"
		}
		parts = append(parts, lead+strings.Join(st, "、"))
	}
	if a.Accent != "" {
		parts = append(parts, "带有"+phrase(Accent, a.Accent, "zh"))
	}
	if a.Dialect != "" {
		p := phrase(Dialect, a.Dialect, "zh")
		if strings.HasSuffix(p, "话") {
			parts = append(parts, "说"+p)
		} else {
			parts = append(parts, "带"+p)
		}
	}
	if whisper {
		parts = append(parts, "用耳语般的声音轻声说话")
	}
	if a.Pace != "" {
		parts = append(parts, phrase(Pace, a.Pace, "zh"))
	}
	if a.Emotion != "" {
		parts = append(parts, "语气"+phrase(Emotion, a.Emotion, "zh"))
	}
	return strings.Join(parts, "，") + "。"
}
