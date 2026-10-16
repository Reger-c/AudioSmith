package voicedesign

// Lowering a recipe for OmniVoice: attribute tags from its closed
// vocabulary; split from lower.go.

import "strings"

func lowerOmniVoice(r Recipe, o Options) Lowered {
	a := r.Attrs
	l := Lowered{DescriptionDropped: r.Description != ""}
	// Accent tags exist only in English form and dialect tags only in
	// Chinese, so they decide the tag language; else the caller's choice.
	lang := "en"
	switch {
	case a.Accent != "":
	case a.Dialect != "":
		if op, _ := Lookup(Dialect, a.Dialect); op.Omni.Zh != "" {
			lang = "zh"
		}
	case o.Lang == "zh":
		lang = "zh"
	}
	var tags []string
	add := func(c Category, id string) {
		op, _ := Lookup(c, id)
		tag := op.Omni.En
		if lang == "zh" {
			tag = op.Omni.Zh
		}
		if tag == "" {
			l.Dropped = append(l.Dropped, Dropped{Value{c, id}, "not in OmniVoice's vocabulary"})
			return
		}
		tags = append(tags, tag)
		l.Used = append(l.Used, Value{c, id})
	}
	for _, c := range []Category{Gender, Age, Pitch} {
		if v := a.Value(c); v != "" {
			add(c, v)
		}
	}
	for _, s := range a.Styles {
		add(Style, s)
	}
	if a.Accent != "" {
		add(Accent, a.Accent)
	}
	if a.Dialect != "" {
		add(Dialect, a.Dialect)
	}
	for _, c := range []Category{Pace, Emotion} {
		if v := a.Value(c); v != "" {
			l.Dropped = append(l.Dropped, Dropped{Value{c, v}, "not in OmniVoice's vocabulary"})
		}
	}
	sep := ", "
	if lang == "zh" {
		sep = "，"
	}
	l.Voice, l.Lang = strings.Join(tags, sep), lang
	return l
}

// ParseOmniVoiceTags reads an OmniVoice instruct ("female, young adult,
// british accent" or "女，青年，四川话") back into attributes; unknown
// tags are returned as they are. Used to import voices from tools that
// store the tag string.
func ParseOmniVoiceTags(s string) (Attributes, []string) {
	var a Attributes
	var unknown []string
	fields := strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == '，' || r == '、' || r == ';' })
	for _, f := range fields {
		f = strings.ToLower(strings.TrimSpace(f))
		if f == "" || f == "none" {
			continue
		}
		found := false
		for _, info := range categoryTable {
			for _, op := range info.Options {
				if (op.Omni.En != "" && f == op.Omni.En) || (op.Omni.Zh != "" && f == op.Omni.Zh) {
					a, _, _ = a.With(info.ID, op.ID)
					found = true
				}
			}
		}
		if !found {
			unknown = append(unknown, strings.TrimSpace(f))
		}
	}
	return a, unknown
}
