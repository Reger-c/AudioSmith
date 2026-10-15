package main

// Stories → AI director: the writing-help language model (by default the
// local Qwen3 4B) reads the story and proposes its scene, a persona per
// character and a direction per line (translate.StoryDirector); the page
// fills them in, where the user edits them. The API's update_longform
// (direct: fill | all) runs it too.

import (
	"context"
	"errors"
	"strings"

	"github.com/hehehai/AudioSmith/internal/catalog"
	"github.com/hehehai/AudioSmith/internal/core"
	"github.com/hehehai/AudioSmith/internal/i18n"
	"github.com/hehehai/AudioSmith/internal/llm"
	"github.com/hehehai/AudioSmith/internal/services"
	"github.com/hehehai/AudioSmith/internal/translate"
	"github.com/hehehai/AudioSmith/internal/voicedesign"
)

// storyScript is a story as the director reads it: its spoken lines, and
// its characters with the voices they are cast with (among voices).
func storyScript(d *services.LongformDoc, voices []core.Voice) translate.StoryScript {
	s := translate.StoryScript{Title: d.Title, Scene: d.Scene}
	names := map[string]string{}
	zh := strings.HasPrefix(d.Language, "zh") || d.Language == "" && i18n.Locale() == "zh-Hans"
	for _, m := range d.Cast {
		names[m.ID] = m.Name
		ref := m.Voice
		if ref == "" {
			ref = d.Voice
		}
		s.Cast = append(s.Cast, translate.StoryRole{Name: m.Name, Persona: m.Persona, Voice: voiceHint(ref, voices, zh)})
	}
	for _, l := range d.Lines {
		if l.Chapter || strings.TrimSpace(l.Text) == "" {
			continue
		}
		s.Lines = append(s.Lines, translate.StoryScriptLine{Speaker: names[l.Character], Text: l.Text, Direction: l.Direction})
	}
	return s
}

// voiceHint describes a voice reference for the director: a speaker's
// description, a designed voice's, else the voice's name.
func voiceHint(ref string, voices []core.Voice, zh bool) string {
	pick := func(t voicedesign.Text) string {
		if zh && t.Zh != "" {
			return t.Zh
		}
		return t.En
	}
	if id, ok := strings.CutPrefix(ref, services.SpeakerRef); ok {
		for _, sp := range voicedesign.CustomVoiceSpeakers {
			if strings.EqualFold(sp.ID, id) {
				return pick(sp.About)
			}
		}
		return id
	}
	for _, v := range voices {
		if v.ID == ref {
			if strings.TrimSpace(v.Design) != "" {
				return v.Design
			}
			return v.Name
		}
	}
	return ""
}

// applyDirection writes the director's proposal into d: everything, or
// (fill) only the scene, personas and directions still empty. It returns
// how many lines got a direction.
func applyDirection(d *services.LongformDoc, dir translate.StoryDirection, fill bool) int {
	if dir.Scene != "" && (!fill || strings.TrimSpace(d.Scene) == "") {
		d.Scene = dir.Scene
	}
	for _, r := range dir.Cast {
		for i := range d.Cast {
			m := &d.Cast[i]
			if strings.EqualFold(strings.TrimSpace(m.Name), strings.TrimSpace(r.Name)) && r.Persona != "" &&
				(!fill || strings.TrimSpace(m.Persona) == "") {
				m.Persona = r.Persona
			}
		}
	}
	// The script's line n is the n-th spoken line.
	var spoken []int
	for i, l := range d.Lines {
		if !l.Chapter && strings.TrimSpace(l.Text) != "" {
			spoken = append(spoken, i)
		}
	}
	n := 0
	for _, ld := range dir.Lines {
		if ld.N < 1 || ld.N > len(spoken) {
			continue
		}
		l := &d.Lines[spoken[ld.N-1]]
		if fill && strings.TrimSpace(l.Direction) != "" {
			continue
		}
		l.Direction, l.Intensity = ld.Direction, ld.Intensity
		if ld.Direction != "" {
			n++
		}
	}
	return n
}

// storyDirected says whether a story has any scene, persona or direction.
func storyDirected(d *services.LongformDoc) bool {
	if strings.TrimSpace(d.Scene) != "" {
		return true
	}
	for _, m := range d.Cast {
		if strings.TrimSpace(m.Persona) != "" {
			return true
		}
	}
	for _, l := range d.Lines {
		if strings.TrimSpace(l.Direction) != "" {
			return true
		}
	}
	return false
}

// noDirectorText says the director has no language model.
func noDirectorText() string {
	return i18n.T("The AI director needs a language model: download one in Settings → Writing help (%s runs on this Mac).", modelRefID(catalog.DefaultLLM))
}

// directorError is the page's message for a director that failed.
func directorError(err error) string {
	switch c := translate.Code(err); {
	case c == translate.CodeNoLLM || c == llm.CodeNoRoute:
		return noDirectorText()
	case c == llm.CodeTimeout || errors.Is(err, context.DeadlineExceeded):
		return i18n.T("The language model took too long. Try again, or direct a shorter story.")
	case c == llm.CodeInvalidJSON || c == llm.CodeSchema:
		return i18n.T("The language model's answer could not be read. Try again.")
	}
	return i18n.T("The AI director failed: %v", err)
}

// directStory runs the AI director over a page's story in the background
// and fills in what it proposes (all: replacing what is set), with Undo.
func (a *app) directStory(p *lfPage, all bool) {
	if p.directing {
		return
	}
	if len(storyScript(&p.doc, nil).Lines) == 0 {
		a.toast(i18n.T("Nothing to direct: add lines first."))
		return
	}
	if !a.llmCan(llm.SkillDirectionParse) {
		a.toast(noDirectorText())
		return
	}
	script := storyScript(&p.doc, a.voices)
	router := a.llmRouter
	p.directing = true
	a.background(func() {
		dir, err := (&translate.StoryDirector{Router: router}).Direct(context.Background(), script)
		a.post(func() {
			p.directing = false
			if err != nil {
				a.toast(directorError(err))
				return
			}
			before := p.doc
			before.Lines = append([]services.StoryLine(nil), p.doc.Lines...)
			before.Cast = append([]services.CastMember(nil), p.doc.Cast...)
			n := applyDirection(&p.doc, dir, !all)
			p.open["scene"] = true
			a.toastUndo(i18n.N(n, "The AI director directed %d line", "The AI director directed %d lines", n), func() {
				p.doc.Scene, p.doc.Lines, p.doc.Cast = before.Scene, before.Lines, before.Cast
			})
		})
	})
}
