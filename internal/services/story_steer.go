package services

// Steering by punctuation (experimental, Production.Steer;
// plan/story-emotion-context-plan.md §6 G): the voice models take much of
// a line's feeling from its punctuation, so the text a line is
// synthesized with follows its emotion: a calm line loses its "！", a
// hesitant one pauses ("……"), a sad one trails off, an excited one ends
// with "！", a pleading Chinese line may end with "嘛". Only punctuation
// and a few sentence-final particles ever change (steerValid checks it);
// what the story shows, the read-along and the stems keep the line as
// written.

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/hehehai/AudioSmith/internal/text"
)

// steerParticles are the sentence-final particles steering may add.
const steerParticles = "嘛呀啊呢吧哦"

// steerLoud are the emotions whose "！" stays even when faint.
var steerLoud = map[string]bool{EmotionExcited: true, EmotionAngry: true, EmotionSurprised: true, EmotionScared: true}

// SteerText is the text a line with an emotion at an intensity is
// synthesized with (its markup without tags), or text itself when
// nothing changes or the line has tags.
func SteerText(line, emotion string, intensity int) string {
	emotion = NormEmotion(emotion)
	if emotion == "" || strings.ContainsAny(line, "[]") || !hasSpeech(line) {
		return line
	}
	zh := cjkShare(line) >= 0.3
	s := strings.TrimRightFunc(line, unicode.IsSpace)
	// The closing quotes and brackets after the last punctuation stay.
	body := strings.TrimRightFunc(s, func(r rune) bool { return strings.ContainsRune("\"'”’」』）)", r) })
	tail := s[len(body):]
	calm := intensity == 0 || intensity <= 1 && !steerLoud[emotion]
	if calm {
		body = strings.NewReplacer("！？", "？", "？！", "？", "!?", "?", "?!", "?", "！", "。", "!", ".").Replace(body)
	}
	end := func(to string) {
		r, n := utf8.DecodeLastRuneInString(body)
		switch {
		case strings.ContainsRune("。！.!", r):
			body = body[:len(body)-n] + to
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			body += to
		}
	}
	switch {
	case emotion == EmotionHesitant:
		switch {
		case zh && strings.Contains(body, "，"):
			body = strings.Replace(body, "，", "……", 1)
		case !zh && strings.Contains(body, ", "):
			body = strings.Replace(body, ", ", "... ", 1)
		case zh:
			end("……")
		default:
			end("...")
		}
	case (emotion == EmotionSad || emotion == EmotionHurt) && intensity >= 2:
		if zh {
			end("……")
		} else {
			end("...")
		}
	case steerLoud[emotion] && intensity >= 2 && emotion != EmotionScared:
		if zh {
			end("！")
		} else {
			end("!")
		}
	case emotion == EmotionPleading && intensity >= 2 && zh:
		r, n := utf8.DecodeLastRuneInString(body)
		before, _ := utf8.DecodeLastRuneInString(body[:len(body)-n])
		if strings.ContainsRune("。！", r) && !strings.ContainsRune(steerParticles+"吗", before) && text.IsCJK(before) {
			body = body[:len(body)-n] + "嘛" + string(r)
		}
	}
	out := body + tail
	if !steerValid(line, out) {
		return line
	}
	return out
}

// steerValid says whether edited differs from orig only in punctuation
// and added sentence-final particles: the words are the same.
func steerValid(orig, edited string) bool {
	strip := func(s string) []rune {
		var out []rune
		for _, r := range s {
			if unicode.IsLetter(r) || unicode.IsDigit(r) {
				out = append(out, r)
			}
		}
		return out
	}
	o, e := strip(orig), strip(edited)
	i := 0
	for _, r := range e {
		if i < len(o) && o[i] == r {
			i++
			continue
		}
		if !strings.ContainsRune(steerParticles, r) {
			return false
		}
	}
	return i == len(o)
}

// steerLines are lines with their spans' text steered by their emotions,
// and how many lines changed.
func steerLines(lines []LineSpec) ([]LineSpec, int) {
	out := make([]LineSpec, len(lines))
	n := 0
	for i, l := range lines {
		out[i] = l
		if l.Emotion == "" {
			continue
		}
		spans := make([]SpanSpec, len(l.Spans))
		changed := false
		for j, sp := range l.Spans {
			spans[j] = sp
			if t := SteerText(sp.Text, l.Emotion, l.Intensity); t != sp.Text {
				spans[j].Text, changed = t, true
			}
		}
		out[i].Spans = spans
		if changed {
			n++
		}
	}
	return out, n
}

// The "steadier delivery" preset of stories (Production.Temperature,
// TopP, CharacterSeeds): sampling tighter than the Studio's (Qwen3-TTS
// 0.7 / 0.8), so a line is less often overplayed, and a seed per
// character, so a render made again sounds the same. Thread #78's sweep
// (8 lines × 3 seeds, 0.6B CustomVoice): 0.6 / 0.75 varied least between
// seeds (pitch SD 1.05 st, 0.7 / 0.8: 1.57) with as much pitch movement
// inside a line; 0.55 / 0.7 and 0.4 / 0.6 were no steadier.
const (
	StoryTemperature = 0.6
	StoryTopP        = 0.75
)

// Steadier reports whether a production has the steadier-delivery
// preset; SetSteadier sets or clears it.
func (p Production) Steadier() bool {
	return p.Temperature == StoryTemperature && p.TopP == StoryTopP && p.CharacterSeeds
}

func (p *Production) SetSteadier(on bool) {
	p.Temperature, p.TopP, p.CharacterSeeds = 0, 0, false
	if on {
		p.Temperature, p.TopP, p.CharacterSeeds = StoryTemperature, StoryTopP, true
	}
}
