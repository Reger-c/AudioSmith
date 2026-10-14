package dub

import "strings"

// Story is a dub turned into a Stories script ("Create Story"): one
// character per speaker, one line per segment (merged segments split
// back by speaker), in one language. Thread #18's Stories page consumes
// it: VoiceRefs are as its own (a saved voice's id, "speaker:<id>" for a
// model's speaker); a voice Stories cannot use (an automatic clone, a
// design or gallery description) is left "" and the character keeps its
// Name, so the page's cast applies.
type Story struct {
	Title      string           `json:"title"`
	Lang       string           `json:"lang"`
	Characters []StoryCharacter `json:"characters"`
	Lines      []StoryLine      `json:"lines"`
}

// StoryCharacter is a speaker as a character.
type StoryCharacter struct {
	Name  string `json:"name"`
	Voice string `json:"voice,omitempty"`
}

// StoryLine is a segment as a line.
type StoryLine struct {
	Character string  `json:"character"`
	Text      string  `json:"text"`
	Voice     string  `json:"voice,omitempty"` // the line's own voice
	Direction string  `json:"direction,omitempty"`
	Speed     float64 `json:"speed,omitempty"`
	Start     float64 `json:"start"` // where it was in the dub (for ordering, notes)
}

// StoryVoice reports whether a VoiceRef is one Stories can use.
func StoryVoice(ref string) string {
	switch {
	case ref == "", ref == VoiceAuto, ref == VoiceDefault, strings.HasPrefix(ref, DesignPrefix), strings.HasPrefix(ref, GalleryPrefix):
		return ""
	}
	return ref
}

// ToStory converts the project into a Story in lang ("" = the source).
func (p *Project) ToStory(lang string) Story {
	st := Story{Title: p.Title, Lang: lang}
	if lang == "" {
		st.Lang = p.SourceLanguage()
	}
	seen := map[string]bool{}
	addChar := func(label string) string {
		name := p.SpeakerName(label)
		if !seen[label] {
			seen[label] = true
			st.Characters = append(st.Characters, StoryCharacter{Name: name, Voice: StoryVoice(p.Cast[label])})
		}
		return name
	}
	for _, s := range p.Segments {
		text := s.Text
		if lang != "" {
			text = p.TextFor(s, lang)
		}
		if strings.TrimSpace(text) == "" {
			continue
		}
		// Merged lines go back to their speakers (source text only: a
		// translation does not keep the offsets).
		if len(s.Parts) > 0 && lang == "" {
			r := []rune(text)
			from, speaker := 0, s.Speaker
			for _, pt := range append(s.Parts, Part{Offset: len(r)}) {
				to := min(max(pt.Offset, from), len(r))
				if t := strings.TrimSpace(string(r[from:to])); t != "" {
					st.Lines = append(st.Lines, StoryLine{Character: addChar(speaker), Text: t, Voice: StoryVoice(s.Voice),
						Direction: s.Direction, Speed: s.Speed, Start: s.Start})
				}
				from, speaker = to, pt.Speaker
			}
			continue
		}
		st.Lines = append(st.Lines, StoryLine{Character: addChar(s.Speaker), Text: strings.TrimSpace(text), Voice: StoryVoice(s.Voice),
			Direction: s.Direction, Speed: s.Speed, Start: s.Start})
	}
	return st
}

// VoiceRefs of a cast (Project.Cast, Segment.Voice).
const (
	// VoiceAuto ("" in the cast) clones the speaker's own voice.
	VoiceAuto = "auto"
	// VoiceDefault is the model's default voice.
	VoiceDefault = "default"
	// DesignPrefix: "design:<description>", a voice made from words.
	DesignPrefix = "design:"
	// GalleryPrefix: "gallery:<archetype id>", a Voice Gallery voice.
	GalleryPrefix = "gallery:"
	// SpeakerPrefix: "speaker:<id>", one of the model's own speakers.
	SpeakerPrefix = "speaker:"
)
