package main

// The stories' 「整场对话」 (whole dialogue) switch, thread #80: a dialogue
// model (catalogue capability "dialogue", SoulX-Podcast) speaks the
// characters' lines a scene at a time, each line hearing the lines before
// it; narration stays on the story's model (services/story_dialogue.go).
// Shown under Performance; explained and off where it cannot run: a Mac
// with less memory than the model needs, or a story in a language it does
// not speak.

import (
	"slices"
	"strings"
	"unicode"

	"github.com/egoist/mygo/ui"
	"github.com/hehehai/AudioSmith/internal/catalog"
	"github.com/hehehai/AudioSmith/internal/hub"
	"github.com/hehehai/AudioSmith/internal/i18n"
	"github.com/hehehai/AudioSmith/internal/icons"
	"github.com/hehehai/AudioSmith/internal/services"
)

// dialogueEntry is the catalogue's dialogue model.
func dialogueEntry() (catalog.Entry, bool) {
	for _, e := range catalog.Builtin().Entries {
		if e.Has(catalog.CapDialogue) {
			return e, true
		}
	}
	return catalog.Entry{}, false
}

// storyLanguage is a story's language: its own, else what its text looks
// like ("zh" with Han, "ja"/"ko" with kana or Hangul, else "en").
func storyLanguage(d *services.LongformDoc) string {
	if d.Language != "" {
		return d.Language
	}
	han := false
	for _, l := range d.Lines {
		for _, r := range l.Text {
			switch {
			case unicode.Is(unicode.Hiragana, r) || unicode.Is(unicode.Katakana, r):
				return "ja"
			case unicode.Is(unicode.Hangul, r):
				return "ko"
			case unicode.Is(unicode.Han, r):
				han = true
			}
		}
	}
	if han {
		return "zh"
	}
	return "en"
}

// dialogueBlocked says why a story cannot use whole dialogue on this Mac,
// "" when it can.
func (a *app) dialogueBlocked(d *services.LongformDoc, e catalog.Entry) string {
	gb := 0
	if a.sys == nil {
		a.loadSysInfo()
	} else {
		gb = a.sys.MemoryGB()
	}
	return dialogueBlockedOn(gb, d, e)
}

// dialogueBlockedOn is dialogueBlocked on a Mac of gb GB (0: unknown).
func dialogueBlockedOn(gb int, d *services.LongformDoc, e catalog.Entry) string {
	if gb > 0 && float64(gb) < e.MinMemoryGB {
		return i18n.T("Needs %g GB of memory; this Mac has %d GB.", e.MinMemoryGB, gb)
	}
	lang := strings.ToLower(storyLanguage(d))
	if !slices.ContainsFunc(e.Languages, func(l catalog.Language) bool { return strings.HasPrefix(lang, l.Code) }) {
		return i18n.T("For Chinese and English stories.")
	}
	return ""
}

// storyDialogue is the whole-dialogue row of the Performance section.
func (a *app) storyDialogue(c *ui.Context, p *lfPage, locked bool) {
	e, ok := dialogueEntry()
	if !ok {
		return
	}
	t := c.Theme()
	k := tok(t)
	d := &p.doc
	blocked := a.dialogueBlocked(d, e)
	on := d.Production.Dialogue && blocked == ""
	title := i18n.T("Whole dialogue")
	formRow(c, "perf-dialogue-row", title, i18n.T("The characters' lines are spoken by %s a scene at a time, each line hearing the lines before it, so they answer each other; narration stays on the story's model. Each character is cloned from their voice (a cloned voice's clip, else a short sample in that voice). Lines it does not say clearly are made again, then on the story's model. Slower than real time.", modelRef(e)), func() {
		if ui.Switch(c.Key("perf-dialogue"), &on).Label(title).Disabled(locked || blocked != "").Changed() {
			d.Production.Dialogue = on
			if on {
				d.Production.DialogueModel = e.Repo
			}
		}
	})
	switch m := a.model(e); {
	case blocked != "":
		caption(c.Key("perf-dialogue-why"), blocked).TextColor(k.Text3)
	case on && m.known && m.local.State != hub.Downloaded:
		ui.Row(c.Key("perf-dialogue-need")).Gap(px(t, sp2)).AlignItems(ui.Center).Wrap().Children(func() {
			caption(c, i18n.T("%s is not downloaded (%s).", modelRef(e), catalog.HumanSize(e.Size))).TextColor(k.Text2).Shrink(1)
			if m.downloading {
				ui.Spinner(c)
			} else if secondaryIconButton(c.Key("perf-dialogue-dl"), icons.DownloadMinimalistic, i18n.T("Download")).Label(i18n.T("Download")).Disabled(locked).Clicked() {
				a.download(e)
			}
		})
	}
}
