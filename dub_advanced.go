package main

// The Dub page's 「高级」 (thread #42, phase E): each step's panel keeps
// what most dubs need in view and folds the rest at its foot, in one
// section the whole page opens or closes (remembered): how many speakers
// and stopping to review (Source), more languages and the translator's
// options (Translate), the voice match (Cast), the voice model's
// parameters (Generate), a WAV's bit depth (Export).

import (
	"github.com/egoist/mygo/ui"
	"github.com/hehehai/AudioSmith/internal/dub"
	"github.com/hehehai/AudioSmith/internal/i18n"
)

// dubAdvanced is step's 「高级」 section.
func (a *app) dubAdvanced(c *ui.Context, step int, locked bool, body func()) {
	a.advancedSection(c, "dub", a.dubChanges(step), locked, func() { a.dubRestoreStep(step) }, body)
}

// dubChanges names what step's advanced settings change from a new
// dub's.
func (a *app) dubChanges(step int) []string {
	p := a.dub
	if p == nil {
		return nil
	}
	s, def := p.Settings, dub.DefaultSettings()
	var out []string
	add := func(on bool, name string) {
		if on {
			out = append(out, name)
		}
	}
	switch step {
	case dubStepSource:
		add(p.Speakers != 0, i18n.T("Speakers"))
		add(!s.Review, i18n.T("Review after each step"))
	case dubStepTranslate:
		add(firstOf(s.Translator, "llm") != def.Translator, i18n.T("Translator"))
		add(s.Quality != def.Quality && s.Quality != "", i18n.T("Quality"))
		add(s.AutoGlossary != def.AutoGlossary, i18n.T("Auto glossary"))
		add(s.SuggestShorter != def.SuggestShorter, i18n.T("Suggest shorter lines"))
		add(s.Formality != def.Formality, i18n.T("Register"))
		add(s.Style != "", i18n.T("Style notes"))
	case dubStepCast:
		add(s.VoiceMatch == dub.MatchLine, i18n.T("Voice match"))
	case dubStepGenerate:
		caps := dubCaps(s.Model)
		add(s.Steps != 0 && caps.Steps, i18n.T("Steps"))
		add(s.Guidance != 0 && caps.Guidance, i18n.T("Guidance"))
		add(s.Speed != 0 && caps.Speed, i18n.T("Speed"))
		add(s.Instruct != "" && caps.Instruct, i18n.T("Voice style"))
	case dubStepExport:
		add(a.dubExport.bits != 1, i18n.T("Bit depth"))
	}
	return out
}

// dubRestoreStep brings step's advanced settings back to a new dub's (the
// languages to dub into stay: they are the user's choice, not a tuning).
func (a *app) dubRestoreStep(step int) {
	def := dub.DefaultSettings()
	switch step {
	case dubStepSource:
		a.dubSet(func(p *dub.Project) { p.Speakers = 0 })
		a.dubSettings(func(s *dub.Settings) { s.Review = def.Review })
	case dubStepTranslate:
		a.dubSettings(func(s *dub.Settings) {
			s.Translator, s.Quality, s.AutoGlossary, s.SuggestShorter = def.Translator, def.Quality, def.AutoGlossary, def.SuggestShorter
			s.Formality, s.Style = def.Formality, ""
		})
	case dubStepCast:
		a.dubSettings(func(s *dub.Settings) { s.VoiceMatch = def.VoiceMatch })
	case dubStepGenerate:
		a.dubSettings(func(s *dub.Settings) { s.Steps, s.Guidance, s.Speed, s.Instruct = 0, 0, 0, "" })
	case dubStepExport:
		a.dubExport.bits = 1
	}
}
