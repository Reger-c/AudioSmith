package voicedesign

import (
	"fmt"
	"slices"
	"strings"
	"sync"
	"unicode"
)

// Target names one way of feeding a recipe to an engine.
type Target string

const (
	// TargetQwen3Design: Qwen3-TTS VoiceDesign takes a free-form English or
	// Chinese description in its voice field.
	TargetQwen3Design Target = "qwen3-voicedesign"
	// TargetOmniVoice: OmniVoice takes comma-separated attribute tags from
	// a closed vocabulary, one per category; other text is ignored.
	TargetOmniVoice Target = "omnivoice"
	// TargetQwen3CustomVoice: Qwen3-TTS CustomVoice takes "Speaker,
	// instruction": a named speaker plus a style/emotion instruction.
	TargetQwen3CustomVoice Target = "qwen3-customvoice"
)

// Vocabulary is how an engine reads the voice field.
type Vocabulary string

const (
	VocabFreeform Vocabulary = "freeform" // the description as written + explicit picks
	VocabTags     Vocabulary = "tags"     // attribute tags only
	VocabSpeaker  Vocabulary = "speaker"  // a fixed speaker + delivery instruction
)

// Options tune a lowering.
type Options struct {
	// Lang is the language of the generated voice text: "en", "zh" or ""
	// (auto: the description's language, else zh for a dialect, else en).
	Lang string
	// Speaker forces a named speaker (CustomVoice).
	Speaker string
	// Speakers limits the choice to the speakers the model has (CustomVoice;
	// empty = all known speakers).
	Speakers []string
}

// Dropped is a value an engine can't express.
type Dropped struct {
	Value
	Reason string `json:"reason"`
}

// Lowered is a recipe in an engine's terms.
type Lowered struct {
	Target  Target    `json:"target"`
	Voice   string    `json:"voice"`             // the voice/instruct field; "" = the model's auto voice
	Lang    string    `json:"lang"`              // language of Voice: "en" or "zh"
	Speaker string    `json:"speaker,omitempty"` // CustomVoice: the speaker chosen
	Seed    int64     `json:"seed"`
	Used    []Value   `json:"used,omitempty"`    // values that made it into Voice
	Dropped []Dropped `json:"dropped,omitempty"` // values the engine can't express
	// DescriptionDropped: the engine ignores free text, so only the mapped
	// attributes count (UI: "This engine only understands the details").
	DescriptionDropped bool `json:"descriptionDropped,omitempty"`
}

// Engine is one row of the lowering table. New design-capable engines add
// a row with Register.
type Engine struct {
	Target     Target
	Label      string
	Vocabulary Vocabulary
	// Match reports whether a catalogue model (its engine family and repo)
	// uses this lowering.
	Match func(family, repo string) bool
	Lower func(r Recipe, o Options) Lowered
}

var (
	engMu   sync.RWMutex
	engines = []Engine{
		{
			Target: TargetQwen3Design, Label: "Qwen3-TTS VoiceDesign", Vocabulary: VocabFreeform,
			Match: func(f, repo string) bool {
				return f == "qwen3_tts" && strings.Contains(strings.ToLower(repo), "voicedesign")
			},
			Lower: lowerFreeform,
		},
		{
			Target: TargetOmniVoice, Label: "OmniVoice", Vocabulary: VocabTags,
			Match: func(f, _ string) bool { return f == "omnivoice" },
			Lower: lowerOmniVoice,
		},
		{
			Target: TargetQwen3CustomVoice, Label: "Qwen3-TTS CustomVoice", Vocabulary: VocabSpeaker,
			Match: func(f, repo string) bool {
				return f == "qwen3_tts" && strings.Contains(strings.ToLower(repo), "customvoice")
			},
			Lower: lowerCustomVoice,
		},
	}
)

// Register adds or replaces (same Target) a lowering.
func Register(e Engine) {
	engMu.Lock()
	defer engMu.Unlock()
	for i := range engines {
		if engines[i].Target == e.Target {
			engines[i] = e
			return
		}
	}
	engines = append(engines, e)
}

// Engines lists the lowering table.
func Engines() []Engine {
	engMu.RLock()
	defer engMu.RUnlock()
	return slices.Clone(engines)
}

// EngineFor finds the lowering for a catalogue model; false when the
// model can't design voices.
func EngineFor(family, repo string) (Engine, bool) {
	engMu.RLock()
	defer engMu.RUnlock()
	for _, e := range engines {
		if e.Match != nil && e.Match(family, repo) {
			return e, true
		}
	}
	return Engine{}, false
}

// Lower turns a recipe into the voice field for target.
func Lower(t Target, r Recipe, o Options) (Lowered, error) {
	engMu.RLock()
	var e *Engine
	for i := range engines {
		if engines[i].Target == t {
			e = &engines[i]
		}
	}
	engMu.RUnlock()
	if e == nil {
		return Lowered{}, fmt.Errorf("voicedesign: no lowering for %q", t)
	}
	r = r.Normalize()
	l := e.Lower(r, o)
	l.Target, l.Seed = t, r.Seed
	return l, nil
}

// TextLang guesses whether a text is Chinese ("zh") or not ("en"): more
// Han characters than Latin letters.
func TextLang(s string) string {
	han, latin := 0, 0
	for _, r := range s {
		switch {
		case unicode.Is(unicode.Han, r):
			han++
		case r < 0x250 && unicode.IsLetter(r):
			latin++
		}
	}
	if han > 0 && han*2 >= latin {
		return "zh"
	}
	return "en"
}

func pickLang(r Recipe, o Options) string {
	switch o.Lang {
	case "en", "zh":
		return o.Lang
	}
	if r.Description != "" {
		return TextLang(r.Description)
	}
	if r.Attrs.Dialect != "" {
		return "zh"
	}
	return "en"
}

func allValues(a Attributes) []Value { return a.Diff(Attributes{}) }

// ---- Qwen3-TTS VoiceDesign: free-form ----

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

// ---- OmniVoice: attribute tags ----

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

// ---- Qwen3-TTS CustomVoice: named speaker + instruction ----

// Speaker is one of Qwen3-TTS CustomVoice's named speakers, described by
// the attributes it is closest to (from the public model card).
type Speaker struct {
	ID    string
	Lang  string // native language
	Attrs Attributes
	About Text
}

// CustomVoiceSpeakers lists the Qwen3-TTS CustomVoice speakers. The 0.6B
// and 1.7B models share them; some quantised builds ship fewer (see
// Options.Speakers).
var CustomVoiceSpeakers = []Speaker{
	{"Vivian", "zh", Attributes{Gender: "female", Age: "young_adult", Styles: []string{"bright"}}, Text{"Bright young female voice", "明亮的年轻女声"}},
	{"Serena", "zh", Attributes{Gender: "female", Age: "young_adult", Styles: []string{"warm", "soft"}}, Text{"Warm, gentle young female voice", "温暖柔和的年轻女声"}},
	{"Uncle_Fu", "zh", Attributes{Gender: "male", Age: "middle_aged", Pitch: "low", Styles: []string{"smooth"}}, Text{"Seasoned male voice with a low, mellow timbre", "低沉醇厚的成熟男声"}},
	{"Dylan", "zh", Attributes{Gender: "male", Age: "young_adult", Dialect: "beijing"}, Text{"Young Beijing male voice", "北京口音的年轻男声"}},
	{"Eric", "zh", Attributes{Gender: "male", Age: "young_adult", Dialect: "sichuan", Styles: []string{"energetic"}}, Text{"Lively Chengdu (Sichuan) male voice", "活泼的成都男声"}},
	{"Ryan", "en", Attributes{Gender: "male", Age: "young_adult", Styles: []string{"energetic"}}, Text{"Dynamic English male voice", "富有活力的英语男声"}},
	{"Aiden", "en", Attributes{Gender: "male", Age: "young_adult", Accent: "american", Styles: []string{"bright"}}, Text{"Sunny American male voice", "阳光的美式英语男声"}},
	{"Ono_Anna", "ja", Attributes{Gender: "female", Age: "young_adult", Styles: []string{"playful"}}, Text{"Playful Japanese female voice", "俏皮的日语女声"}},
	{"Sohee", "ko", Attributes{Gender: "female", Age: "young_adult", Styles: []string{"warm"}}, Text{"Warm Korean female voice", "温暖的韩语女声"}},
}

// BestSpeaker picks the CustomVoice speaker closest to the attributes for
// speech in lang ("" = any), among allowed (empty = all).
func BestSpeaker(a Attributes, lang string, allowed []string) Speaker {
	best, bestScore := Speaker{}, -1<<30
	for _, s := range CustomVoiceSpeakers {
		if len(allowed) > 0 && !slices.Contains(allowed, s.ID) {
			continue
		}
		score := 0
		switch {
		case a.Gender == "":
		case a.Gender == s.Attrs.Gender:
			score += 8
		default:
			score -= 8
		}
		if a.Dialect != "" && a.Dialect == s.Attrs.Dialect {
			score += 6
		} else if s.Attrs.Dialect != "" {
			score -= 2 // a dialect speaker only when asked for
		}
		if a.Accent != "" && a.Accent == s.Attrs.Accent {
			score += 3
		}
		want := lang
		if want == "" {
			want = a.Language()
		}
		if want != "" && want == s.Lang {
			score += 4
		}
		if a.Age != "" {
			if a.Age == s.Attrs.Age {
				score += 2
			} else if (a.Age == "elderly" || a.Age == "middle_aged") && s.Attrs.Age == "middle_aged" {
				score += 2
			}
		}
		if a.Pitch != "" && a.Pitch == s.Attrs.Pitch {
			score++
		}
		for _, st := range a.Styles {
			if s.Attrs.HasStyle(st) {
				score++
			}
		}
		if score > bestScore {
			best, bestScore = s, score
		}
	}
	return best
}

func lowerCustomVoice(r Recipe, o Options) Lowered {
	a := r.Attrs
	lang := pickLang(r, o)
	var sp Speaker
	if o.Speaker != "" {
		sp = Speaker{ID: o.Speaker}
		for _, s := range CustomVoiceSpeakers {
			if strings.EqualFold(s.ID, o.Speaker) {
				sp = s
			}
		}
	} else {
		sp = BestSpeaker(a, "", o.Speakers)
	}
	l := Lowered{Speaker: sp.ID, Lang: lang, DescriptionDropped: r.Description != ""}
	// Identity comes from the speaker: report what it doesn't match.
	for _, c := range []Category{Gender, Age, Pitch, Accent, Dialect} {
		if v := a.Value(c); v != "" {
			if sp.Attrs.Value(c) == v {
				l.Used = append(l.Used, Value{c, v})
			} else {
				l.Dropped = append(l.Dropped, Dropped{Value{c, v}, "fixed by the speaker " + sp.ID})
			}
		}
	}
	// Delivery goes into the instruction.
	var styles []string
	whisper := false
	for _, s := range a.Styles {
		l.Used = append(l.Used, Value{Style, s})
		if s == "whisper" {
			whisper = true
			continue
		}
		styles = append(styles, phrase(Style, s, lang))
	}
	if a.Pace != "" {
		l.Used = append(l.Used, Value{Pace, a.Pace})
	}
	if a.Emotion != "" {
		l.Used = append(l.Used, Value{Emotion, a.Emotion})
	}
	var instr string
	if lang == "zh" {
		var parts []string
		if len(styles) > 0 {
			parts = append(parts, "用"+strings.Join(styles, "、")+"的声音说话")
		}
		if whisper {
			parts = append(parts, "轻声耳语")
		}
		if a.Emotion != "" {
			parts = append(parts, "语气"+phrase(Emotion, a.Emotion, "zh"))
		}
		if a.Pace != "" {
			parts = append(parts, phrase(Pace, a.Pace, "zh"))
		}
		if len(parts) > 0 {
			instr = strings.Join(parts, "，") + "。"
		}
	} else {
		var how []string
		if a.Pace != "" {
			how = append(how, phrase(Pace, a.Pace, "en"))
		}
		if whisper {
			how = append(how, "in a whisper")
		}
		if len(styles) > 0 {
			j := joinEN(styles)
			how = append(how, "in "+strings.ToLower(article(j))+" "+j+" voice")
		}
		if a.Emotion != "" {
			how = append(how, "sounding "+phrase(Emotion, a.Emotion, "en"))
		}
		if len(how) > 0 {
			instr = "Speak " + strings.Join(how, ", ") + "."
		}
	}
	l.Voice = sp.ID
	if instr != "" {
		l.Voice += ", " + instr
	}
	return l
}
