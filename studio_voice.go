package main

// The Studio inspector's own voice section: the voice (a built-in speaker
// or a style prompt) and, for models that clone, a reference clip and its
// transcript. It shows when no voice from the library is picked (the
// voice library's picker, voicePicker in voices.go, comes first, and
// studioUsesSavedVoice hides this section while it supplies the voice).

import (
	"path/filepath"

	"github.com/egoist/mygo/ui"
	"github.com/hehehai/AudioSmith/internal/catalog"
	"github.com/hehehai/AudioSmith/internal/i18n"
)

func (a *app) studioVoiceSection(c *ui.Context, p catalog.Entry) {
	t := c.Theme()
	custom := a.modelLabel == catalog.CustomLabel
	voiceLabel := i18n.T("Voice")
	if p.Has(catalog.CapVoiceStyle) {
		voiceLabel = i18n.T("Voice / style")
	}
	if voices := p.VoiceIDs(); len(voices) > 0 || custom || p.Has(catalog.CapVoiceStyle) {
		ui.Field(c, voiceLabel, func() {
			ui.Autocomplete(c.Key("voice"), &a.voice, voices).Placeholder(i18n.T("Model default")).Label(voiceLabel)
		})
	}
	if p.Has(catalog.CapClone) {
		ui.Divider(c)
		ui.Text(c, i18n.T("Voice cloning")).Bold()
		ui.Row(c).Gap(px(t, 8)).AlignItems(ui.Center).Children(func() {
			name := i18n.T("No reference clip")
			if a.refAudio != "" {
				name = filepath.Base(a.refAudio)
			}
			ui.Text(c, name).FontSize(px(t, 12)).Grow(1).MinWidth(0).SingleLine().Ellipsis("…")
			if ui.Button(c.Key("choose-ref"), i18n.T("Choose…")).Clicked() {
				a.chooseRefAudio()
			}
			if a.refAudio != "" && ui.Button(c.Key("clear-ref"), i18n.T("Clear")).Clicked() {
				a.refAudio, a.refText = "", ""
			}
		})
		ui.Field(c, i18n.T("Reference transcript"), func() {
			ui.TextArea(c.Key("ref-text"), &a.refText).Lines(2, 5).Placeholder(i18n.T("What the reference clip says")).Label(i18n.T("Reference transcript"))
		})
	}
}
