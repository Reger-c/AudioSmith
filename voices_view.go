package main

import (
	"context"
	"slices"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/hehehai/AudioSmith/internal/core"
	"github.com/hehehai/AudioSmith/internal/i18n"
	"github.com/hehehai/AudioSmith/internal/lang"
)

// shownVoices are the library's voices that match the page's search and
// kind filter, by name. The search is in memory: names, styles,
// transcripts, descriptions and tags.
func (a *app) shownVoices() []core.Voice {
	q := strings.ToLower(strings.TrimSpace(a.voiceQuery))
	var out []core.Voice
	for _, v := range a.voices {
		switch a.voiceKind {
		case 1:
			if v.Kind != core.VoiceCloned {
				continue
			}
		case 2:
			if v.Kind != core.VoiceDesigned {
				continue
			}
		}
		if q != "" && !voiceMatches(&v, q) {
			continue
		}
		out = append(out, v)
	}
	return out
}

func voiceMatches(v *core.Voice, q string) bool {
	for _, s := range append([]string{v.Name, v.Style, v.RefText, v.Design, v.Language}, v.Tags...) {
		if strings.Contains(strings.ToLower(s), q) {
			return true
		}
	}
	return false
}

// voicesPage is /voices: the saved voices, and the profile editor of the
// one chosen.
func (a *app) voicesPage(c *ui.Context) {
	t := c.Theme()
	header(c, i18n.T("Voices"), func() {
		if ui.Button(c.Key("voices-import"), i18n.T("Import…")).Tooltip(i18n.T("Import a .asvoice or .ovsvoice file")).Clicked() {
			a.chooseVoiceFile()
		}
		if ui.Button(c.Key("voices-design"), i18n.T("Design a voice")).Clicked() {
			a.navigate("/design")
		}
		if ui.PrimaryButton(c.Key("voices-new"), i18n.T("New voice from a clip")).Clicked() {
			a.startClone("")
		}
	})
	body := ui.Row(c.Key("voices-body")).Grow(1).MinHeight(0).AlignItems(ui.Stretch)
	if files := body.DroppedFiles(); files != nil {
		a.voicesDropped(files)
	}
	body.Children(func() {
		ui.Column(c).Grow(1).MinWidth(0).Children(func() {
			a.voicesToolbar(c)
			a.voicesGrid(c)
		})
		if a.edit != nil {
			ui.Box(c).Width(1).Background(t.Border)
			ui.Scroll(c.Key("voice-editor")).Width(px(t, 380)).Shrink(0).Background(t.Surface).Children(func() {
				a.voiceEditor(c)
			})
		}
	})
	a.voiceDeleteAlert(c)
	a.personaDialogs(c)
}

func (a *app) voicesToolbar(c *ui.Context) {
	t := c.Theme()
	ui.Row(c).Gap(px(t, 10)).AlignItems(ui.Center).Padding(px(t, 12), px(t, 20), px(t, 4)).Children(func() {
		ui.TextInput(c.Key("voices-search"), &a.voiceQuery).Placeholder(i18n.T("Search voices")).Label(i18n.T("Search voices")).Grow(1).MaxWidth(px(t, 320))
		ui.Segmented(c.Key("voices-kind"), &a.voiceKind, i18n.T("All"), i18n.T("Cloned"), i18n.T("Designed")).Label(i18n.T("Kind"))
		ui.Box(c).Grow(1)
		ui.Text(c, i18n.N(len(a.voices), "%d voice", "%d voices", len(a.voices))).FontSize(px(t, 12)).TextColor(t.TextMuted)
	})
}

func (a *app) voicesGrid(c *ui.Context) {
	t := c.Theme()
	vs := a.shownVoices()
	if len(vs) == 0 {
		ui.Column(c).Grow(1).Center().Gap(px(t, 8)).Padding(px(t, 24)).Children(func() {
			switch {
			case !a.voicesLoaded:
				ui.Text(c, i18n.T("Loading…")).TextColor(t.TextMuted)
			case len(a.voices) > 0:
				ui.Text(c, i18n.T("No matches")).FontSize(px(t, 15)).Bold()
			default:
				ui.Text(c, i18n.T("No saved voices yet")).FontSize(px(t, 15)).Bold()
				ui.Text(c, i18n.T("Upload or record a clip and save it as a voice, design one from a description, or pick one in the gallery.")).
					TextColor(t.TextMuted).TextAlign(ui.Center).MaxWidth(px(t, 420))
				ui.Row(c).Gap(px(t, 8)).Children(func() {
					if ui.Button(c.Key("empty-gallery"), i18n.T("Open the gallery")).Clicked() {
						a.navigate("/gallery")
					}
					if ui.Button(c.Key("empty-design"), i18n.T("Design a voice")).Clicked() {
						a.navigate("/design")
					}
					if ui.PrimaryButton(c.Key("empty-clone"), i18n.T("New voice from a clip")).Clicked() {
						a.startClone("")
					}
				})
			}
		})
		return
	}
	ui.GridView(c.Key("voices-grid"), &a.voiceGrid, len(vs), px(t, 250), px(t, 84), func(i int) {
		v := vs[i]
		a.voiceCard(c, &v)
	}).Grow(1).Padding(px(t, 8), px(t, 16)).Gap(px(t, 10))
}

// voiceCard is one saved voice: a click opens its editor.
func (a *app) voiceCard(c *ui.Context, v *core.Voice) {
	t := c.Theme()
	selected := a.edit != nil && a.edit.id == v.ID
	card := ui.Row(c.Key("voice-"+v.ID)).Grow(1).Gap(px(t, 10)).AlignItems(ui.Center).Padding(px(t, 10)).Radius(10).
		Border(1, t.Border).Background(t.Surface)
	if selected {
		card.Border(2, t.Accent)
	}
	hovered := card.Hovered()
	card.Children(func() {
		open := ui.ButtonBase(c.Key("open")).Grow(1).MinWidth(0).Label(v.Name)
		if open.Clicked() {
			a.openVoiceEditor(v.ID)
		}
		open.Children(func() {
			ui.Row(c).Gap(px(t, 10)).AlignItems(ui.Center).Grow(1).MinWidth(0).Children(func() {
				ui.Avatar(c, v.Name, a.portrait(v))
				ui.Column(c).Grow(1).MinWidth(0).Gap(px(t, 2)).Children(func() {
					ui.Row(c).Gap(px(t, 6)).AlignItems(ui.Center).Children(func() {
						ui.Text(c, v.Name).Bold().SingleLine().Ellipsis("…").Shrink(1).MinWidth(0)
						voiceBadges(c, v)
					})
					ui.Text(c, voiceMeta(v)).FontSize(px(t, 11)).TextColor(t.TextMuted).SingleLine().Ellipsis("…")
				})
			})
		})
		label := i18n.T("Preview")
		if a.voicePlaying(v) {
			label = i18n.T("Stop")
		}
		if j := a.voiceJob("sample", v.ID); j.running() {
			ui.Spinner(c).Label(i18n.T("Rendering the sample…"))
		} else if hovered || a.voicePlaying(v) || selected {
			if ui.Button(c.Key("play"), label).Clicked() {
				a.playVoice(v)
			}
		}
	})
}

// voiceBadges are a voice's Verified and Locked badges.
func voiceBadges(c *ui.Context, v *core.Voice) {
	t := c.Theme()
	if v.Verified() {
		ui.Badge(c, i18n.T("Verified")).Background(t.Success.Alpha(0.18)).TextColor(t.Success)
	} else if v.Consent.State == core.ConsentIncluded {
		ui.Badge(c, i18n.T("Consent included")).Tooltip(i18n.T("The imported file says the speaker consented. It is not verified here."))
	}
	if v.Locked {
		ui.Badge(c, i18n.T("Locked")).Tooltip(i18n.T("The voice's identity is locked to a take and a seed"))
	}
}

// voiceMeta is a card's second line: kind, language, date.
func voiceMeta(v *core.Voice) string {
	parts := []string{voiceKindText(v.Kind)}
	if v.Language != "" {
		parts = append(parts, languageName(v.Language))
	}
	parts = append(parts, i18n.Date(v.CreatedAt))
	return strings.Join(parts, " · ")
}

// languageName names a BCP-47 code in the UI's language when the table
// knows it.
func languageName(code string) string {
	if l, ok := lang.Lookup(code); ok {
		return l.NameIn(i18n.Locale())
	}
	return code
}

// voiceLanguages are the language codes a voice may say it speaks.
func voiceLanguages() []string {
	return []string{"en", "zh", "ja", "ko", "es", "fr", "de", "it", "pt", "ru", "hi", "ar", "nl", "pl", "tr", "vi", "id", "th", "yue"}
}

// voicesDropped imports dropped voice files; a dropped clip starts a new
// voice from it.
func (a *app) voicesDropped(paths []string) {
	for _, p := range paths {
		if isVoiceFile(p) {
			a.importVoice(p)
		}
	}
	for _, p := range paths {
		if !isVoiceFile(p) && !isPortraitPath(p) {
			a.startClone("")
			a.cloneLoad(p, baseName(p), false)
			return
		}
	}
}

// askDeleteVoice asks before deleting a voice.
func (a *app) askDeleteVoice(id string) {
	a.deleteVoice, a.deleteOpen = id, true
}

func (a *app) voiceDeleteAlert(c *ui.Context) {
	v := a.findVoice(a.deleteVoice)
	if v == nil {
		a.deleteOpen = false
		return
	}
	if ui.AlertDialog(c, &a.deleteOpen, i18n.T("Delete “%s”?", v.Name), i18n.T("Its reference, samples and portrait are deleted too. Takes made with it stay. This cannot be undone."),
		i18n.T("Cancel"), i18n.T("Delete voice")) == 1 {
		a.deleteVoiceNow(v.ID)
	}
}

// deleteVoiceNow deletes a voice in the background; VoiceChanged updates
// the views.
func (a *app) deleteVoiceNow(id string) {
	if a.pl.Current() == "voice:"+id {
		a.pl.Stop()
	}
	svc := a.lib.svc.Voices
	a.background(func() {
		err := svc.Delete(context.Background(), id)
		a.post(func() {
			if err != nil {
				a.toast(i18n.T("Could not delete: %v", err))
			} else {
				a.toast(i18n.T("Voice deleted"))
			}
		})
	})
}

// openVoiceEditor opens the voice library with v's editor.
func (a *app) openVoiceEditor(id string) {
	v := a.findVoice(id)
	if v == nil {
		return
	}
	if a.edit == nil || a.edit.id != id {
		a.edit = newVoiceEdit(*v)
		a.loadUsage(id)
	}
	if i := slices.IndexFunc(a.shownVoices(), func(x core.Voice) bool { return x.ID == id }); i < 0 {
		a.voiceQuery, a.voiceKind = "", 0
	}
	a.navigate("/voices")
}
