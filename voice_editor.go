package main

// The profile editor: the right pane of /voices for one voice. Name,
// transcript, style, language and tags; the portrait; the reference (play,
// replace); "Try this voice" with a lock to the take it makes; the
// ownership statement; where the voice was used; export.

import (
	"context"
	"slices"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/hehehai/AudioSmith/internal/catalog"
	"github.com/hehehai/AudioSmith/internal/core"
	"github.com/hehehai/AudioSmith/internal/i18n"
)

// voiceEdit is the editor's state for one voice.
type voiceEdit struct {
	id   string
	orig core.Voice // as stored when last loaded or saved

	name, transcript, style, language, tags string

	usage  *core.VoiceUsage
	saving bool

	tryOpen  bool
	tryText  string
	tryModel string // catalogue label

	unlockOpen, revokeOpen bool
	consentOpen            bool
	consent                micState

	exportOpen    bool
	exportRef     bool
	exportLicence string
}

func newVoiceEdit(v core.Voice) *voiceEdit {
	e := &voiceEdit{id: v.ID, exportRef: true, tryText: i18n.T("Hello — this is a test of this voice.")}
	e.load(v)
	return e
}

func (e *voiceEdit) load(v core.Voice) {
	e.orig = v
	e.name, e.transcript, e.style, e.language = v.Name, v.RefText, v.Style, v.Language
	e.tags = strings.Join(v.Tags, ", ")
}

// refresh takes a newer stored profile, keeping the fields being edited.
func (e *voiceEdit) refresh(v core.Voice) {
	old := e.orig
	keep := func(cur *string, was, now string) {
		if *cur == was {
			*cur = now
		}
	}
	keep(&e.name, old.Name, v.Name)
	keep(&e.transcript, old.RefText, v.RefText)
	keep(&e.style, old.Style, v.Style)
	keep(&e.language, old.Language, v.Language)
	keep(&e.tags, strings.Join(old.Tags, ", "), strings.Join(v.Tags, ", "))
	e.orig = v
}

// edited is the profile with the editor's fields.
func (e *voiceEdit) edited() core.Voice {
	v := e.orig
	v.Name, v.RefText, v.Style, v.Language = strings.TrimSpace(e.name), strings.TrimSpace(e.transcript), strings.TrimSpace(e.style), strings.TrimSpace(e.language)
	v.Tags = nil
	for _, tag := range strings.FieldsFunc(e.tags, func(r rune) bool { return r == ',' || r == '，' }) {
		if tag = strings.TrimSpace(tag); tag != "" && !slices.Contains(v.Tags, tag) {
			v.Tags = append(v.Tags, tag)
		}
	}
	return v
}

func (e *voiceEdit) dirty() bool {
	v, o := e.edited(), e.orig
	return v.Name != o.Name || v.RefText != o.RefText || v.Style != o.Style || v.Language != o.Language || !slices.Equal(v.Tags, o.Tags)
}

// loadUsage reads where a voice was used.
func (a *app) loadUsage(id string) {
	svc := a.lib.svc.Voices
	a.background(func() {
		u, err := svc.Usage(context.Background(), id)
		a.post(func() {
			if err == nil && a.edit != nil && a.edit.id == id {
				a.edit.usage = &u
			}
		})
	})
}

// saveVoiceEdit saves the editor's fields.
func (a *app) saveVoiceEdit() {
	e := a.edit
	if e == nil || e.saving {
		return
	}
	v := e.edited()
	if v.Name == "" {
		a.toast(i18n.T("Give the voice a name."))
		return
	}
	e.saving = true
	svc := a.lib.svc.Voices
	a.background(func() {
		saved, err := svc.Save(context.Background(), v)
		a.post(func() {
			if a.edit == e {
				e.saving = false
			}
			if err != nil {
				a.toast(i18n.T("Could not save the voice profile: %v", err))
				return
			}
			if a.edit == e {
				e.load(saved)
			}
			a.toast(i18n.T("Saved"))
		})
	})
}

// voiceAction runs a change of a voice's files in the background and
// reports it.
func (a *app) voiceAction(fn func(ctx context.Context, svc core.VoiceService) (core.Voice, error), ok string) {
	svc := a.lib.svc.Voices
	a.background(func() {
		_, err := fn(context.Background(), svc)
		a.post(func() {
			if err != nil {
				a.toast(i18n.T("Could not change the voice: %v", err))
				return
			}
			if ok != "" {
				a.toast(ok)
			}
		})
	})
}

// setVoicePortrait makes an image a saved voice's portrait.
func (a *app) setVoicePortrait(id, path string) {
	a.background(func() {
		png, err := portraitPNG(path)
		a.post(func() {
			if err != nil {
				a.toast(portraitErrorText(err))
				return
			}
			v := a.findVoice(id)
			if v == nil {
				return
			}
			cur := *v
			a.voiceAction(func(ctx context.Context, svc core.VoiceService) (core.Voice, error) {
				return svc.SaveFiles(ctx, cur, core.VoiceFiles{Portrait: png})
			}, "")
		})
	})
}

// consentStatement is the statement a user reads to verify a voice is theirs.
func consentStatement() string {
	return i18n.T("I confirm that this voice is my own, and I consent to AudioSmith cloning it on my behalf.")
}

func (a *app) voiceEditor(c *ui.Context) {
	t := c.Theme()
	e := a.edit
	v := a.findVoice(e.id)
	if v == nil {
		return
	}
	if e.consent.phase != "" {
		c.After(100 * time.Millisecond)
	}
	ui.Column(c.Key("editor-"+v.ID)).Padding(px(t, 16)).Gap(px(t, 14)).Children(func() {
		ui.Row(c).Gap(px(t, 8)).AlignItems(ui.Center).Children(func() {
			ui.Text(c, i18n.T("Edit voice profile")).Bold().Grow(1)
			if ui.Button(c.Key("editor-close"), i18n.T("Close")).Clicked() {
				a.edit = nil
			}
		})
		ui.Row(c).Gap(px(t, 12)).AlignItems(ui.Center).Children(func() {
			id := v.ID
			a.portraitPicker(c, "editor-portrait", v.Name, a.portrait(v), func(p string) { a.setVoicePortrait(id, p) }, func() {
				cur := *v
				a.voiceAction(func(ctx context.Context, svc core.VoiceService) (core.Voice, error) {
					return svc.SaveFiles(ctx, cur, core.VoiceFiles{RemovePortrait: true})
				}, "")
			})
			ui.Column(c).Grow(1).MinWidth(0).Gap(px(t, 4)).Children(func() {
				ui.Text(c, v.Name).FontSize(px(t, 15)).Bold().SingleLine().Ellipsis("…")
				ui.Row(c).Gap(px(t, 6)).Wrap().Children(func() { voiceBadges(c, v) })
				ui.Text(c, voiceKindText(v.Kind)+" · "+i18n.DateTime(v.CreatedAt)).FontSize(px(t, 11)).TextColor(t.TextMuted)
			})
		})
		a.editorAudio(c, v)
		a.editorFields(c, v)
		a.editorTry(c, v)
		a.editorLock(c, v)
		a.editorConsent(c, v)
		a.editorUsage(c, v)
		a.editorExport(c, v)
		ui.Divider(c)
		ui.Row(c).Gap(px(t, 8)).AlignItems(ui.Center).Children(func() {
			if ui.Button(c.Key("editor-delete"), i18n.T("Delete")).TextColor(t.Danger).Clicked() {
				a.askDeleteVoice(v.ID)
			}
			ui.Box(c).Grow(1)
			if ui.Button(c.Key("editor-studio"), i18n.T("Use in Studio")).Clicked() {
				a.useVoiceInStudio(v)
			}
			if ui.PrimaryButton(c.Key("editor-save"), i18n.T("Save")).Disabled(!e.dirty() || e.saving).Clicked() {
				a.saveVoiceEdit()
			}
		})
	})
	a.editorAlerts(c, v)
}

// editorAudio plays the voice's reference or sample, and replaces it.
func (a *app) editorAudio(c *ui.Context, v *core.Voice) {
	t := c.Theme()
	ui.Row(c.Key("editor-audio")).Gap(px(t, 8)).AlignItems(ui.Center).Children(func() {
		what := i18n.T("Reference clip")
		switch {
		case v.Locked && v.Sample != "":
			what = i18n.T("Locked take")
		case v.Kind == core.VoiceDesigned && v.Sample != "":
			what = i18n.T("Voice sample")
		case v.Kind == core.VoiceDesigned:
			what = i18n.T("No sample yet")
		}
		if v.Kind == core.VoiceCloned && !v.Locked && v.RefSeconds > 0 {
			what += " · " + i18n.T("%.1f s", v.RefSeconds)
		}
		ui.Text(c, what).FontSize(px(t, 12)).TextColor(t.TextMuted).Grow(1)
		if j := a.voiceJob("sample", v.ID); j.running() {
			ui.Spinner(c)
			ui.Text(c, j.status).FontSize(px(t, 12))
		} else {
			label := i18n.T("Play")
			switch {
			case a.voicePlaying(v):
				label = i18n.T("Stop")
			case a.voiceAudio(v) == "":
				label = i18n.T("Preview voice")
			}
			if ui.Button(c.Key("editor-play"), label).Clicked() {
				a.playVoice(v)
			}
		}
		if v.Kind == core.VoiceCloned && ui.Button(c.Key("editor-replace"), i18n.T("Replace reference…")).Clicked() {
			a.startClone(v.ID)
		}
		if v.Kind == core.VoiceDesigned && ui.Button(c.Key("editor-design"), i18n.T("Open in Design")).Clicked() {
			a.openDesignFor(v)
		}
	})
	if j := a.voiceJob("sample", v.ID); j != nil && j.err != "" {
		ui.Text(c, j.err).FontSize(px(t, 12)).TextColor(t.Danger)
	}
}

func (a *app) editorFields(c *ui.Context, v *core.Voice) {
	e := a.edit
	ui.Field(c, i18n.T("Profile name"), func() {
		ui.TextInput(c.Key("editor-name"), &e.name).Label(i18n.T("Profile name"))
	})
	if v.Kind == core.VoiceCloned {
		ui.Field(c, i18n.T("Transcript"), func() {
			ui.TextArea(c.Key("editor-transcript"), &e.transcript).Lines(2, 5).
				Placeholder(i18n.T("(Optional) What the clip says. Leave it empty to let the engine listen.")).Label(i18n.T("Transcript"))
		})
		a.styleField(c, "editor-style", &e.style)
	} else if v.Design != "" {
		ui.Field(c, i18n.T("Description"), func() {
			ui.Text(c, v.Design).FontSize(px(c.Theme(), 12)).Selectable()
		})
	}
	ui.Field(c, i18n.T("Language"), func() {
		ui.Autocomplete(c.Key("editor-language"), &e.language, voiceLanguages()).Placeholder(i18n.T("Auto")).Label(i18n.T("Language"))
	})
	ui.Field(c, i18n.T("Tags"), func() {
		ui.TextInput(c.Key("editor-tags"), &e.tags).Placeholder(i18n.T("e.g. narration, warm")).Label(i18n.T("Tags"))
	})
}

// tryModels are the models the voice can be tried with.
func tryModels(v *core.Voice) []catalog.Entry {
	var out []catalog.Entry
	for _, e := range voiceCatalog().ForTask(catalog.TaskTTS) {
		if ok, _ := voiceUsable(v, e); ok && v.Kind != core.VoiceBuiltin {
			out = append(out, e)
		}
	}
	return out
}

// editorTry is "Try this voice": a phrase, a model, a take; the take can
// become the voice's locked identity.
func (a *app) editorTry(c *ui.Context, v *core.Voice) {
	t := c.Theme()
	e := a.edit
	ui.Collapsible(c.Key("editor-try"), i18n.T("Try this voice"), &e.tryOpen, func() {
		ms := tryModels(v)
		if len(ms) == 0 {
			ui.Text(c, i18n.T("No model can speak with this voice.")).FontSize(px(t, 12)).TextColor(t.TextMuted)
			return
		}
		labels := make([]string, len(ms))
		for i, m := range ms {
			labels[i] = m.Label
		}
		if !slices.Contains(labels, e.tryModel) {
			e.tryModel = labels[0]
			if p := a.preset(); slices.Contains(labels, p.Label) {
				e.tryModel = p.Label
			}
		}
		ui.Column(c).Gap(px(t, 8)).Children(func() {
			ui.TextArea(c.Key("try-text"), &e.tryText).Lines(2, 4).Label(i18n.T("Test phrase"))
			ui.Row(c).Gap(px(t, 8)).AlignItems(ui.Center).Children(func() {
				ui.Select(c.Key("try-model"), &e.tryModel, labels).Label(i18n.T("Model")).Grow(1)
				j := a.voiceJob("try", v.ID)
				if ui.PrimaryButton(c.Key("try-go"), i18n.T("Generate preview")).Disabled(j.running() || strings.TrimSpace(e.tryText) == "").Clicked() {
					a.tryVoice(v.ID)
				}
			})
			j := a.voiceJob("try", v.ID)
			switch {
			case j == nil:
			case j.running():
				ui.Progress(c, j.fraction).Label(j.status)
				ui.Text(c, j.status).FontSize(px(t, 12)).TextColor(t.TextMuted)
			case j.err != "":
				ui.Text(c, j.err).FontSize(px(t, 12)).TextColor(t.Danger).Selectable()
				if j.needDownload != nil && ui.Button(c.Key("try-download"), i18n.T("Download")).Clicked() {
					a.download(*j.needDownload)
				}
			default:
				take := j.take
				ui.Row(c).Gap(px(t, 8)).AlignItems(ui.Center).Children(func() {
					label := i18n.T("Play")
					if a.pl.Playing(take.ID) {
						label = i18n.T("Stop")
					}
					if ui.Button(c.Key("try-play"), label).Clicked() {
						a.play(take)
					}
					ui.Text(c, i18n.T("%.1f s · seed %d", take.AudioSeconds, take.Seed)).FontSize(px(t, 12)).TextColor(t.TextMuted).Grow(1)
					if v.LockTake != take.ID && ui.Button(c.Key("try-lock"), i18n.T("Lock voice identity")).
						Tooltip(i18n.T("Every take with this voice reuses this take's audio and seed")).Clicked() {
						a.lockVoice(v.ID, take.ID)
					}
				})
			}
		})
	})
}

// tryVoice generates the test phrase with the chosen model.
func (a *app) tryVoice(id string) {
	e := a.edit
	v := a.findVoice(id)
	if e == nil || v == nil {
		return
	}
	m, ok := voiceCatalog().ByLabel(e.tryModel)
	if !ok {
		return
	}
	t := core.Take{Text: strings.TrimSpace(e.tryText), Model: m.Repo}
	a.fillVoice(&t, v, m)
	a.voiceGenerate("try", id, t, func() { a.tryVoice(id) })
}

func (a *app) lockVoice(id, takeID string) {
	a.voiceAction(func(ctx context.Context, svc core.VoiceService) (core.Voice, error) {
		return svc.Lock(ctx, id, takeID)
	}, i18n.T("Voice locked: every take reuses the same audio and seed."))
}

func (a *app) editorLock(c *ui.Context, v *core.Voice) {
	t := c.Theme()
	if !v.Locked {
		return
	}
	ui.Row(c.Key("editor-lock")).Gap(px(t, 8)).AlignItems(ui.Center).Padding(px(t, 10)).Radius(8).Background(t.Background).Children(func() {
		ui.Text(c, i18n.T("This voice is reproducible: every take uses the same locked audio and seed (%d).", v.Seed)).FontSize(px(t, 12)).Grow(1)
		if ui.Button(c.Key("editor-unlock"), i18n.T("Unlock")).Clicked() {
			a.edit.unlockOpen = true
		}
	})
}

// editorConsent is the voice ownership section: the statement, recorded.
func (a *app) editorConsent(c *ui.Context, v *core.Voice) {
	t := c.Theme()
	e := a.edit
	ui.Collapsible(c.Key("editor-consent"), i18n.T("Voice ownership"), &e.consentOpen, func() {
		a.loadDevices() // once
		ui.Column(c).Gap(px(t, 8)).Children(func() {
			switch {
			case v.Verified():
				ui.Text(c, i18n.T("You recorded the consent statement on %s.", i18n.Date(v.Consent.At))).FontSize(px(t, 12))
				ui.Text(c, "“"+v.Consent.Statement+"”").FontSize(px(t, 12)).TextColor(t.TextMuted)
				if ui.Button(c.Key("consent-revoke"), i18n.T("Revoke")).Clicked() {
					e.revokeOpen = true
				}
				return
			case v.Consent.State == core.ConsentIncluded:
				ui.Text(c, i18n.T("The imported file says the speaker consented. Record the statement yourself to verify the voice here.")).
					FontSize(px(t, 12)).TextColor(t.Warning)
			}
			ui.Text(c, i18n.T("Record yourself reading the statement below to show the voice is yours. Verification will be needed for sharing and agent features, never for making speech on this Mac.")).
				FontSize(px(t, 12)).TextColor(t.TextMuted)
			ui.Text(c, "“"+consentStatement()+"”").Bold()
			id, statement, loc := v.ID, consentStatement(), i18n.Locale()
			ui.Column(c).AlignItems(ui.Center).Gap(px(t, 6)).Children(func() {
				a.micControls(c, &e.consent, func(path string) {
					a.voiceAction(func(ctx context.Context, svc core.VoiceService) (core.Voice, error) {
						return svc.SetConsent(ctx, id, core.ConsentRecording{Audio: path, Statement: statement, Locale: loc})
					}, i18n.T("Voice ownership verified"))
				})
			})
		})
	})
}

// editorUsage says where the voice was used.
func (a *app) editorUsage(c *ui.Context, v *core.Voice) {
	t := c.Theme()
	u := a.edit.usage
	if u == nil || (u.Takes == 0 && u.Segments == 0) {
		return
	}
	ui.Column(c.Key("editor-usage")).Gap(px(t, 4)).Children(func() {
		ui.Text(c, i18n.T("Where this voice has been used")).Bold()
		parts := []string{i18n.N(u.Takes, "%d take", "%d takes", u.Takes)}
		if len(u.Projects) > 0 {
			parts = append(parts, i18n.N(len(u.Projects), "%d project", "%d projects", len(u.Projects)),
				i18n.N(u.Segments, "%d segment", "%d segments", u.Segments))
		}
		ui.Text(c, strings.Join(parts, " · ")).FontSize(px(t, 12)).TextColor(t.TextMuted)
		for _, p := range u.Projects {
			ui.Text(c, p.Title+" · "+i18n.N(p.Segments, "%d segment", "%d segments", p.Segments)).FontSize(px(t, 12))
		}
		if u.Takes > 0 && ui.Button(c.Key("usage-history"), i18n.T("Show in History")).Clicked() {
			a.navigate("/history")
		}
	})
}

// editorExport writes the voice as a .asvoice file.
func (a *app) editorExport(c *ui.Context, v *core.Voice) {
	t := c.Theme()
	e := a.edit
	ui.Collapsible(c.Key("editor-export"), i18n.T("Export voice file"), &e.exportOpen, func() {
		ui.Column(c).Gap(px(t, 8)).Children(func() {
			if v.Kind == core.VoiceCloned {
				ui.Checkbox(c.Key("export-ref"), &e.exportRef, i18n.T("Include the reference clip"))
				ui.Text(c, i18n.T("Off: the file carries only a short preview, and the voice can't be cloned as faithfully.")).FontSize(px(t, 11)).TextColor(t.TextMuted)
			}
			labels, ids := licenceChoices()
			cur := labels[0]
			if i := slices.Index(ids, e.exportLicence); i >= 0 {
				cur = labels[i]
			}
			ui.Field(c, i18n.T("Licence"), func() {
				if ui.Select(c.Key("export-licence"), &cur, labels).Label(i18n.T("Licence")).Changed() {
					e.exportLicence = ids[slices.Index(labels, cur)]
				}
			})
			if note := ncNote(v); note != "" {
				ui.Text(c, note).FontSize(px(t, 12)).TextColor(t.Warning)
			}
			if ui.Button(c.Key("export-go"), i18n.T("Export…")).Clicked() {
				a.exportVoice(v.ID, e.exportRef && v.Kind == core.VoiceCloned, e.exportLicence)
			}
		})
	})
}

// ncNote warns when the voice was made with a model whose weights allow
// only non-commercial use.
func ncNote(v *core.Voice) string {
	for _, repo := range []string{v.CreatedWith, v.Model} {
		if repo == "" {
			continue
		}
		if e, ok := catalog.Builtin().ByRepo(repo); ok && !e.Licence.Commercial {
			return i18n.T("Made with %s, whose licence (%s) allows only non-commercial use: what you make with this voice may be bound by it too.", e.Label, e.Licence.Name)
		}
	}
	return ""
}

func (a *app) editorAlerts(c *ui.Context, v *core.Voice) {
	e := a.edit
	id := v.ID
	if ui.AlertDialog(c, &e.unlockOpen, i18n.T("Unlock this voice?"), i18n.T("Takes will no longer reuse the locked audio and seed, so they may sound a little different each time."),
		i18n.T("Cancel"), i18n.T("Unlock voice")) == 1 {
		a.voiceAction(func(ctx context.Context, svc core.VoiceService) (core.Voice, error) { return svc.Unlock(ctx, id) }, i18n.T("Voice unlocked"))
	}
	if ui.AlertDialog(c, &e.revokeOpen, i18n.T("Revoke the verification?"), i18n.T("The consent recording is deleted. You can record it again at any time."),
		i18n.T("Cancel"), i18n.T("Revoke verification")) == 1 {
		a.voiceAction(func(ctx context.Context, svc core.VoiceService) (core.Voice, error) { return svc.RevokeConsent(ctx, id) }, i18n.T("Verification revoked"))
	}
}
