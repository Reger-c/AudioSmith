package main

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/hehehai/AudioSmith/internal/catalog"
	"github.com/hehehai/AudioSmith/internal/core"
	"github.com/hehehai/AudioSmith/internal/dictation"
	"github.com/hehehai/AudioSmith/internal/i18n"
	"github.com/hehehai/AudioSmith/internal/icons"
	"github.com/hehehai/AudioSmith/internal/lang"
	"github.com/hehehai/AudioSmith/internal/services"
	"github.com/hehehai/AudioSmith/internal/settings"
)

var (
	transcribeIcon = icons.Subtitles
	micIcon        = icons.Microphone3
)

// clock shows seconds as "1:05" or "1:02:05".
func clock(s float64) string {
	n := int(max(s, 0))
	if n >= 3600 {
		return fmt.Sprintf("%d:%02d:%02d", n/3600, n/60%60, n%60)
	}
	return fmt.Sprintf("%d:%02d", n/60, n%60)
}

// ago is how long ago t was, for lists.
func ago(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return i18n.T("Just now")
	case d < time.Hour:
		return i18n.T("%d min ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return i18n.T("%d h ago", int(d.Hours()))
	}
	return i18n.DateTime(t)
}

func (a *app) transcribePage(c *ui.Context) {
	t := c.Theme()
	page := ui.Column(c.Key("transcribe-page")).Grow(1).MinHeight(0).AlignItems(ui.Stretch)
	if files := page.DroppedFiles(); files != nil {
		a.transcribeDropped(files)
	}
	page.Children(func() {
		header(c, i18n.T("Transcribe"), func() { a.transcribeToolbar(c) })
		ui.Row(c).Grow(1).MinHeight(0).AlignItems(ui.Stretch).Children(func() {
			ui.Column(c.Key("tr-history")).Width(px(t, 290)).Shrink(0).Background(t.Surface).Children(func() {
				a.transcriptHistory(c)
			})
			ui.Box(c).Width(1).Background(t.Border)
			ui.Scroll(c.Key("tr-main")).Grow(1).MinWidth(0).Children(func() {
				ui.Column(c).Padding(px(t, 20)).Gap(px(t, 14)).Children(func() {
					a.transcriptStatus(c)
					if a.trSel == "" {
						a.transcribeSources(c)
					} else {
						a.transcriptDetail(c)
					}
				})
			})
		})
	})
}

// transcribeToolbar: mode, model and language.
func (a *app) transcribeToolbar(c *ui.Context) {
	t := c.Theme()
	busy := a.trJob != nil || a.trMic.phase != ""
	mode := ui.Segmented(c.Key("tr-mode"), &a.trMode, i18n.T("Fast"), i18n.T("Accurate")).Label(i18n.T("Transcription quality")).Disabled(busy).
		Tooltip(i18n.T("Fast uses a smaller model without word timings. Accurate uses the best model, then times every word."))
	if mode.Changed() {
		m := settings.TranscribeAccurate
		if a.trMode == 0 {
			m = settings.TranscribeFast
		}
		a.updateSettings(func(s *settings.Settings) { s.Transcribe.Mode = m })
	}
	e := a.trModel(a.trMode == 0)
	chip := e.Label
	if a.trMode == 1 {
		if al := trAligner(e); al.Repo != "" {
			chip += " + " + i18n.T("word timings")
		}
	}
	ui.Text(c, chip).FontSize(px(t, 12)).TextColor(t.TextMuted).SingleLine().Ellipsis("…").MaxWidth(px(t, 260))
	a.trLanguageButton(c, e)
}

// trLanguageButton picks the language spoken, or detection.
func (a *app) trLanguageButton(c *ui.Context, e catalog.Entry) {
	t := c.Theme()
	p := &a.trLang
	btn := ui.Button(c.Key("tr-lang"), langName(a.prefs.Transcribe.Language)+" ▾").Tooltip(i18n.T("Language spoken"))
	if btn.Clicked() {
		p.open = !p.open
	}
	ui.Popover(c, btn, &p.open, func() {
		langs := lang.All()
		if q := strings.TrimSpace(p.query); q != "" {
			langs = lang.Search(q)
		}
		opts := lang.Annotate(langs, sttLanguages(e))
		if len(sttLanguages(e)) == 0 {
			for i := range opts {
				opts[i].Supported = true
			}
		}
		opts = sortSupported(opts)
		choose := func(code string) {
			p.open = false
			a.updateSettings(func(s *settings.Settings) { s.Transcribe.Language = code })
		}
		ui.Column(c.Key("tr-lang-picker")).Width(px(t, 300)).Gap(px(t, 6)).Padding(px(t, 6)).Children(func() {
			ui.TextInput(c.Key("tr-lang-search"), &p.query).Placeholder(i18n.T("Search 650 languages")).Label(i18n.T("Search languages"))
			if ui.Button(c.Key("tr-lang-auto"), i18n.T("Auto-detect")).Clicked() {
				choose("")
			}
			ui.List(c.Key("tr-lang-list"), &p.list, len(opts)).Height(px(t, 280)).ItemKey(func(i int) any { return opts[i].Code }).Rows(func(r ui.ListRow) {
				o := opts[r.Index]
				row := ui.ButtonBase(c.Key("tr-lang-"+o.Code)).Padding(px(t, 4), px(t, 8)).Radius(5).Disabled(!o.Supported).Label(o.NameIn(i18n.Locale()))
				if o.Supported && row.Hovered() {
					row.Background(t.SurfaceHover)
				}
				if o.Supported && row.Clicked() {
					choose(o.Code)
				}
				row.Children(func() {
					color := t.Text
					if !o.Supported {
						color = t.TextMuted
					}
					ui.Text(c, o.Flag()).Width(px(t, 22))
					ui.Text(c, o.NameIn(i18n.Locale())).TextColor(color).Grow(1).SingleLine().Ellipsis("…")
					ui.Text(c, o.Code).FontSize(px(t, 11)).TextColor(t.TextMuted)
				})
			})
		})
	})
}

// sortSupported puts the languages a model speaks first.
func sortSupported(opts []lang.Option) []lang.Option {
	out := make([]lang.Option, 0, len(opts))
	for _, o := range opts {
		if o.Supported {
			out = append(out, o)
		}
	}
	for _, o := range opts {
		if !o.Supported {
			out = append(out, o)
		}
	}
	return out
}

// transcriptHistory is the list on the left.
func (a *app) transcriptHistory(c *ui.Context) {
	t := c.Theme()
	list := a.shownTranscripts()
	ui.Column(c).Padding(px(t, 12)).Gap(px(t, 8)).Children(func() {
		ui.Row(c).Gap(px(t, 6)).AlignItems(ui.Center).Children(func() {
			ui.Text(c, i18n.T("History")).Bold().Grow(1)
			ui.Text(c, i18n.N(len(a.trList), "%d transcript", "%d transcripts", len(a.trList))).FontSize(px(t, 11)).TextColor(t.TextMuted)
		})
		if ui.Button(c.Key("tr-new"), i18n.T("New transcript")).Disabled(a.trSel == "").Clicked() {
			a.selectTranscript("")
		}
		if len(a.trList) > 0 {
			if ui.TextInput(c.Key("tr-search"), &a.trQuery).Placeholder(i18n.T("Search transcripts")).Label(i18n.T("Search transcripts")).Changed() {
				a.searchTranscripts()
			}
		}
	})
	ui.Divider(c)
	if len(list) == 0 {
		ui.Column(c).Grow(1).Center().Padding(px(t, 16)).Gap(px(t, 4)).Children(func() {
			if len(a.trList) == 0 {
				ui.Text(c, i18n.T("No transcripts yet")).Bold()
				ui.Text(c, i18n.T("Transcribe a file or a recording, or dictate anywhere with %s.", dictation.GlyphsOf(a.prefs.Dictation.Shortcut))).
					FontSize(px(t, 12)).TextColor(t.TextMuted).TextAlign(ui.Center)
			} else {
				ui.Text(c, i18n.T("No matching transcripts")).Bold()
				ui.Text(c, i18n.T("Try a different search.")).FontSize(px(t, 12)).TextColor(t.TextMuted)
			}
		})
	} else {
		ui.List(c.Key("tr-list"), &a.trListUI, len(list)).Grow(1).MinHeight(0).ItemKey(func(i int) any { return list[i].ID }).Rows(func(r ui.ListRow) {
			tr := list[r.Index]
			row := ui.ButtonBase(c.Key("tr-item-"+tr.ID)).Padding(px(t, 8), px(t, 12)).Label(transcriptTitle(tr))
			switch {
			case a.trSel == tr.ID:
				row.Background(t.Accent.Alpha(0.15))
			case row.Hovered():
				row.Background(t.SurfaceHover)
			}
			if row.Clicked() {
				a.selectTranscript(tr.ID)
			}
			row.Children(func() {
				ui.Column(c).Grow(1).MinWidth(0).Gap(px(t, 2)).Children(func() {
					ui.Row(c).Gap(px(t, 6)).AlignItems(ui.Center).Children(func() {
						if tr.Source == core.SourceDictation {
							ui.Icon(c, micIcon.Linear).TextColor(t.TextMuted)
						}
						ui.Text(c, transcriptTitle(tr)).FontSize(px(t, 12)).Bold().SingleLine().Ellipsis("…").Grow(1).MinWidth(0)
					})
					ui.Text(c, orDash(tr.Preferred())).FontSize(px(t, 12)).MaxLines(2).Ellipsis("…")
					meta := ago(tr.CreatedAt)
					if tr.Language != "" {
						meta += " · " + langName(tr.Language)
					}
					meta += " · " + clock(tr.Duration)
					ui.Text(c, meta).FontSize(px(t, 11)).TextColor(t.TextMuted).SingleLine().Ellipsis("…")
				})
			})
		})
	}
	if len(a.trList) > 0 {
		ui.Divider(c)
		ui.Row(c).Padding(px(t, 8), px(t, 12)).Children(func() {
			if ui.Button(c.Key("tr-export-all"), i18n.T("Export all")).Tooltip(i18n.T("Every transcript in one text file")).Clicked() {
				a.exportAllTranscripts()
			}
		})
	}
}

// transcriptStatus shows the transcription running, or why the last one
// failed.
func (a *app) transcriptStatus(c *ui.Context) {
	t := c.Theme()
	if j := a.trJob; j != nil {
		c.After(500 * time.Millisecond)
		ui.Column(c.Key("tr-job")).Gap(px(t, 8)).Padding(px(t, 14)).Radius(10).Border(1, t.Border).Background(t.Surface).Children(func() {
			ui.Row(c).Gap(px(t, 8)).AlignItems(ui.Center).Children(func() {
				ui.Spinner(c)
				ui.Text(c, i18n.T("Transcribing %s", j.title)).Bold().Grow(1).SingleLine().Ellipsis("…")
				ui.Text(c, formatDuration(time.Since(j.started).Seconds())).FontSize(px(t, 12)).TextColor(t.TextMuted)
				if ui.Button(c.Key("tr-cancel"), i18n.T("Cancel")).Disabled(j.cancelling).Clicked() {
					a.cancelTranscription()
				}
			})
			ui.Progress(c, j.fraction).Label(j.stage)
			ui.Text(c, j.stage).FontSize(px(t, 12)).TextColor(t.TextMuted)
		})
	}
	if a.trErr != "" {
		ui.Column(c.Key("tr-error")).Gap(px(t, 8)).Padding(px(t, 14)).Radius(10).Border(1, t.Danger).Children(func() {
			ui.Text(c, i18n.T("Transcription failed")).Bold().TextColor(t.Danger)
			ui.Text(c, a.trErr).FontSize(px(t, 12)).Selectable()
			ui.Row(c).Gap(px(t, 8)).Children(func() {
				if e := a.trNeed; e != nil {
					m := a.model(*e)
					label := i18n.T("Download %s", e.Label)
					if m.downloading {
						label = i18n.T("Downloading…")
					}
					if primaryButton(c.Key("tr-download"), label).Disabled(m.downloading).Clicked() {
						a.download(*e)
					}
				}
				if r := a.trRetry; r != nil {
					if ui.Button(c.Key("tr-retry"), i18n.T("Retry")).Clicked() {
						a.queueTranscription(*r, true)
					}
				}
				if ui.Button(c.Key("tr-dismiss"), i18n.T("Dismiss")).Clicked() {
					a.trErr, a.trRetry, a.trNeed = "", nil, nil
				}
			})
		})
	}
}

// transcribeSources: a file, or the microphone.
func (a *app) transcribeSources(c *ui.Context) {
	t := c.Theme()
	e := a.trModel(a.trMode == 0)
	if ready, known := a.modelOnDisk(e); known && !ready {
		ui.Row(c.Key("tr-need-model")).Gap(px(t, 10)).AlignItems(ui.Center).Padding(px(t, 12)).Radius(10).Background(t.Warning.Alpha(0.12)).Children(func() {
			ui.Text(c, i18n.T("Transcribing needs %s, which is not downloaded yet.", e.Label)).FontSize(px(t, 12)).Grow(1)
			m := a.model(e)
			label := i18n.T("Download")
			if m.downloading {
				label = i18n.T("Downloading…")
			}
			if ui.Button(c.Key("tr-need-download"), label).Disabled(m.downloading).Clicked() {
				a.download(e)
			}
		})
	}
	ui.Text(c, i18n.T("Turn speech into text: a meeting, an interview, a voice memo or a video. Everything stays on this Mac.")).TextColor(t.TextMuted)
	busy := a.trJob != nil
	ui.Row(c).Gap(px(t, 16)).AlignItems(ui.Stretch).Children(func() {
		zone := ui.ButtonBase(c.Key("tr-upload")).Grow(1).Basis(0).MinHeight(px(t, 180)).Padding(px(t, 16)).
			Radius(12).Border(1, t.Border).BorderStyle(ui.BorderDashed).Label(i18n.T("Choose a file")).Disabled(busy)
		if files := zone.DroppedFiles(); files != nil {
			a.transcribeDropped(files)
		}
		if zone.FileDragOver() {
			zone.Border(2, t.Accent).Background(t.SurfaceHover)
		}
		if zone.Clicked() {
			a.chooseTranscribeFile()
		}
		zone.Children(func() {
			ui.Column(c).Grow(1).Center().Gap(px(t, 6)).Children(func() {
				ui.Text(c, i18n.T("Drop audio or video here, or click to choose")).Bold().TextAlign(ui.Center)
				ui.Text(c, i18n.T("WAV, MP3, M4A, FLAC, OGG, AAC — or a video")).FontSize(px(t, 12)).TextColor(t.TextMuted).TextAlign(ui.Center)
			})
		})
		ui.Column(c.Key("tr-record")).Grow(1).Basis(0).MinHeight(px(t, 180)).Padding(px(t, 16)).Gap(px(t, 8)).
			Radius(12).Border(1, t.Border).AlignItems(ui.Center).Justify(ui.Center).Children(func() {
			a.trRecorder(c, busy)
		})
	})
	ui.Text(c, i18n.T("Dictate into any app with %s: the text is typed where your cursor is. Set it up in Settings → Dictation.",
		dictation.GlyphsOf(a.prefs.Dictation.Shortcut))).FontSize(px(t, 12)).TextColor(t.TextMuted)
}

// transcriptDetail is the transcript shown.
func (a *app) transcriptDetail(c *ui.Context) {
	t := c.Theme()
	tr := a.trDetail
	if tr == nil {
		ui.Row(c).Gap(px(t, 8)).Children(func() {
			ui.Spinner(c)
			ui.Text(c, i18n.T("Reading the transcript…")).TextColor(t.TextMuted)
		})
		return
	}
	ui.Column(c).Gap(px(t, 2)).Children(func() {
		ui.Text(c, transcriptTitle(*tr)).FontSize(px(t, 15)).Bold().SingleLine().Ellipsis("…")
		meta := []string{i18n.DateTime(tr.CreatedAt), langName(tr.Language), clock(tr.Duration), catalog.Builtin().DisplayName(tr.Model)}
		if tr.Mode == settings.TranscribeFast {
			meta = append(meta, i18n.T("Fast"))
		}
		if tr.App != "" {
			meta = append(meta, tr.App)
		}
		ui.Text(c, strings.Join(meta, " · ")).FontSize(px(t, 12)).TextColor(t.TextMuted).SingleLine().Ellipsis("…")
	})
	ui.Row(c).Gap(px(t, 8)).AlignItems(ui.Center).Wrap().Children(func() {
		if ui.Button(c.Key("tr-copy"), i18n.T("Copy")).Clicked() {
			mygoClipboard(c, tr.Preferred())
			a.toast(i18n.T("Copied to the clipboard"))
		}
		exp := ui.Button(c.Key("tr-export"), i18n.T("Export")+" ▾")
		if exp.Clicked() {
			a.trExport = !a.trExport
		}
		ui.Popover(c, exp, &a.trExport, func() {
			ui.Column(c.Key("tr-export-menu")).Padding(px(t, 6)).Gap(px(t, 2)).Width(px(t, 220)).Children(func() {
				for _, f := range services.ExportFormats {
					if ui.Button(c.Key("tr-export-"+f), exportLabel(f)).Clicked() {
						a.exportTranscript(f)
					}
				}
			})
		})
		if ui.Button(c.Key("tr-reuse"), i18n.T("Reuse as script")).Tooltip(i18n.T("Open the text in the Studio")).Clicked() {
			a.reuseTranscript()
		}
		if a.trConfirm == tr.ID {
			if ui.Button(c.Key("tr-delete-yes"), i18n.T("Delete")).Background(t.Danger).Clicked() {
				a.deleteTranscript(tr.ID)
			}
			if ui.Button(c.Key("tr-delete-no"), i18n.T("Keep")).Clicked() {
				a.trConfirm = ""
			}
		} else if ui.Button(c.Key("tr-delete"), i18n.T("Delete…")).Clicked() {
			a.trConfirm = tr.ID
		}
	})
	if a.trWarn != "" {
		ui.Text(c, a.trWarn).FontSize(px(t, 12)).TextColor(t.Warning)
	}
	a.transcriptPlayer(c, tr)
	a.transcriptCleanup(c, tr)
	a.transcriptSegments(c, tr)
}

func exportLabel(f string) string {
	switch f {
	case services.ExportTXT:
		return i18n.T("Text (.txt)")
	case services.ExportSRT:
		return i18n.T("Subtitles (.srt)")
	case services.ExportVTT:
		return i18n.T("Web subtitles (.vtt)")
	case services.ExportJSON:
		return i18n.T("JSON with word timings (.json)")
	}
	return f
}

// transcriptPlayer plays the audio kept with the transcript.
func (a *app) transcriptPlayer(c *ui.Context, tr *core.Transcript) {
	t := c.Theme()
	if tr.Audio == "" || tr.Duration <= 0 {
		return
	}
	id := trPlayID(tr.ID)
	pos := max(a.trPosition(), 0)
	ui.Row(c.Key("tr-player")).Gap(px(t, 10)).AlignItems(ui.Center).Padding(px(t, 10)).Radius(10).Background(t.Surface).Children(func() {
		label := i18n.T("Play")
		if a.pl.Playing(id) {
			label = i18n.T("Pause")
		}
		if primaryButton(c.Key("tr-play"), label).Width(px(t, 72)).Clicked() {
			a.toggleTranscriptPlayback()
		}
		seek := pos
		if ui.Slider(c.Key("tr-seek"), &seek, 0, tr.Duration).Grow(1).Label(i18n.T("Position")).Changed() {
			a.playTranscriptAt(seek)
		}
		ui.Text(c, clock(pos)+" / "+clock(tr.Duration)).FontSize(px(t, 12)).Font("monospace").TextColor(t.TextMuted)
	})
}

// transcriptCleanup: the clean-up, its original, and the buttons that make it.
func (a *app) transcriptCleanup(c *ui.Context, tr *core.Transcript) {
	t := c.Theme()
	cleaning := a.trClean != nil && a.trClean.id == tr.ID
	ui.Row(c.Key("tr-clean-bar")).Gap(px(t, 8)).AlignItems(ui.Center).Wrap().Children(func() {
		if ui.Button(c.Key("tr-polish"), i18n.T("Polish")).Disabled(cleaning).
			Tooltip(i18n.T("Remove fillers, fix spacing, capitals and punctuation, without a language model")).Clicked() {
			a.polishTranscript()
		}
		avail, who := a.llmAvailable()
		clean := ui.Button(c.Key("tr-cleanup"), i18n.T("Clean up")).Disabled(cleaning || !avail)
		if avail {
			clean.Tooltip(i18n.T("Remove fillers and apply self-corrections with %s", who))
		} else {
			clean.Tooltip(i18n.T("Set up a language model in Settings → Writing help"))
		}
		if clean.Clicked() {
			a.cleanTranscript()
		}
		if cleaning {
			ui.Spinner(c)
			ui.Text(c, i18n.T("Cleaning up…")).FontSize(px(t, 12)).TextColor(t.TextMuted)
			if ui.Button(c.Key("tr-clean-stop"), i18n.T("Stop")).Clicked() {
				a.trClean.cancel()
			}
		}
		if tr.Clean != "" && !cleaning {
			if ui.Button(c.Key("tr-clean-drop"), i18n.T("Remove the clean-up")).Clicked() {
				a.dropClean()
			}
		}
	})
	if tr.Clean == "" {
		return
	}
	by := i18n.T("Polished")
	if tr.CleanBy != "polish" && tr.CleanBy != "" {
		by = i18n.T("Cleaned up by %s", tr.CleanBy)
	}
	ui.Column(c.Key("tr-clean")).Gap(px(t, 8)).Padding(px(t, 14)).Radius(10).Border(1, t.Border).Children(func() {
		ui.Text(c, by).FontSize(px(t, 12)).Bold().TextColor(t.TextMuted)
		ui.Text(c, tr.Clean).Selectable()
		label := i18n.T("Show the original transcript")
		if a.trOrig {
			label = i18n.T("Hide the original transcript")
		}
		ui.Row(c).Children(func() {
			if ui.Button(c.Key("tr-orig"), label).Clicked() {
				a.trOrig = !a.trOrig
			}
		})
		if a.trOrig {
			ui.Text(c, tr.Text()).FontSize(px(t, 12)).TextColor(t.TextMuted).Selectable()
		}
	})
}

// transcriptSegments lists the sentences: time (click to play from
// there), speaker, text (click to edit), and the timed words of the
// sentence playing (click one to play from it).
func (a *app) transcriptSegments(c *ui.Context, tr *core.Transcript) {
	t := c.Theme()
	pos := a.trPosition()
	speakers := false
	for _, s := range tr.Segments {
		if s.Speaker != "" {
			speakers = true
		}
	}
	ui.Row(c).Gap(px(t, 8)).AlignItems(ui.Center).Children(func() {
		ui.Text(c, i18n.N(len(tr.Segments), "%d segment", "%d segments", len(tr.Segments))).Bold().Grow(1)
		if !speakers {
			ui.Text(c, i18n.T("Speaker labels: not yet")).FontSize(px(t, 11)).TextColor(t.TextMuted).
				Tooltip(i18n.T("Telling speakers apart comes with speaker diarization in a later version."))
		}
	})
	if len(tr.Segments) == 0 {
		ui.Text(c, i18n.T("No speech was found.")).TextColor(t.TextMuted)
		return
	}
	shown := max(a.trShown, trPage)
	// The sentence playing shows, however far it is.
	for i, s := range tr.Segments {
		if pos >= s.Start && pos < s.End && i >= shown {
			shown = (i/trPage + 1) * trPage
		}
	}
	for i, s := range tr.Segments {
		if i >= shown {
			rest := len(tr.Segments) - shown
			if ui.Button(c.Key("tr-more"), i18n.N(min(rest, trPage), "Show %d more segment", "Show %d more segments", min(rest, trPage))).Clicked() {
				a.trShown = shown + trPage
			}
			break
		}
		playing := pos >= 0 && pos >= s.Start && pos < s.End
		row := ui.Row(c.Key(fmt.Sprintf("tr-seg-%d", i))).Gap(px(t, 10)).AlignItems(ui.Start).Padding(px(t, 6), px(t, 8)).Radius(8)
		if playing {
			row.Background(t.Accent.Alpha(0.12))
		}
		row.Children(func() {
			tb := ui.ButtonBase(c.Key("time")).Padding(px(t, 2), px(t, 6)).Radius(5).Label(clock(s.Start)).Tooltip(i18n.T("Play from here"))
			if tb.Hovered() {
				tb.Background(t.SurfaceHover)
			}
			if tb.Clicked() {
				a.playTranscriptAt(s.Start)
			}
			tb.Children(func() { ui.Text(c, clock(s.Start)).Font("monospace").FontSize(px(t, 12)).TextColor(t.Accent) })
			if s.Speaker != "" {
				ui.Badge(c, speakerLabel(*tr, s.Speaker))
			}
			ui.Column(c).Grow(1).MinWidth(0).Gap(px(t, 6)).Children(func() {
				if a.trEdit == i {
					area := ui.TextArea(c.Key("edit"), &a.trEditBuf).Lines(1, 6).Label(i18n.T("Segment text")).AutoFocus()
					if area.Shortcut(0, ui.KeyEscape) {
						a.trEdit = -1
					}
					if area.Shortcut(ui.Cmd, ui.KeyEnter) {
						a.saveSegmentEdit()
					}
					ui.Row(c).Gap(px(t, 6)).Children(func() {
						if primaryButton(c.Key("edit-save"), i18n.T("Save")).Clicked() {
							a.saveSegmentEdit()
						}
						if ui.Button(c.Key("edit-cancel"), i18n.T("Cancel")).Clicked() {
							a.trEdit = -1
						}
					})
					return
				}
				txt := ui.ButtonBase(c.Key("text")).Label(s.Text).Tooltip(i18n.T("Click to edit"))
				if txt.Hovered() {
					txt.Background(t.SurfaceHover)
				}
				if txt.Clicked() {
					a.editSegment(i)
				}
				txt.Children(func() {
					ui.Text(c, s.Text).Grow(1)
					if s.Edited {
						ui.Text(c, i18n.T("edited")).FontSize(px(t, 10)).TextColor(t.TextMuted)
					}
				})
				if playing && len(s.Words) > 0 && !s.Edited {
					ui.Row(c.Key("words")).Gap(px(t, 4)).Wrap().Children(func() {
						for k, w := range s.Words {
							b := ui.ButtonBase(c.Key(k)).Padding(px(t, 1), px(t, 4)).Radius(4).Label(w.Text)
							if pos >= w.Start && pos < w.End {
								b.Background(t.Accent.Alpha(0.3))
							} else if b.Hovered() {
								b.Background(t.SurfaceHover)
							}
							if b.Clicked() {
								a.playTranscriptAt(w.Start)
							}
							b.Children(func() { ui.Text(c, w.Text).FontSize(px(t, 12)) })
						}
					})
				}
			})
		})
	}
	if a.pl.Playing(trPlayID(tr.ID)) {
		c.After(150 * time.Millisecond)
	}
}

// speakerLabel is a speaker's name, or its label.
func speakerLabel(t core.Transcript, label string) string {
	if n := t.Speakers[label]; n != "" {
		return n
	}
	return label
}

// trRecorder records for a transcript: Record / Stop, the time and level,
// and the microphone (mono, 16 kHz: what speech models take).
func (a *app) trRecorder(c *ui.Context, busy bool) {
	t := c.Theme()
	m := &a.trMic
	a.trRecEnded()
	switch m.phase {
	case "recording":
		c.After(100 * time.Millisecond)
		if primaryButton(c.Key("tr-rec-stop"), i18n.T("Stop recording")).Background(t.Danger).Clicked() {
			a.trRecStop()
		}
		ui.Text(c, clock(time.Since(m.started).Seconds())).FontSize(px(t, 18)).Bold().Font("monospace")
		ui.Meter(c, float64(levelFraction(m.level)), 0, 1, nil).Width(px(t, 180)).Label(i18n.T("Input level"))
		if inputDetected(m.level) {
			ui.Text(c, i18n.T("Input detected")).FontSize(px(t, 12)).TextColor(t.Success)
		} else {
			ui.Text(c, i18n.T("No input detected")).FontSize(px(t, 12)).TextColor(t.TextMuted)
		}
		return
	case "starting", "stopping":
		ui.Spinner(c)
		label := i18n.T("Starting…")
		if m.phase == "stopping" {
			label = i18n.T("Finishing…")
		}
		ui.Text(c, label).FontSize(px(t, 12)).TextColor(t.TextMuted)
		return
	}
	if primaryButton(c.Key("tr-rec-start"), i18n.T("Record")).Disabled(busy).Clicked() {
		a.trRecStart()
	}
	ui.Text(c, i18n.T("Record a meeting, a lecture or a voice memo.")).FontSize(px(t, 12)).TextColor(t.TextMuted).TextAlign(ui.Center)
	labels, ids := []string{i18n.T("System default")}, []string{""}
	for _, d := range a.clone.devices {
		labels, ids = append(labels, d.Name), append(ids, d.ID)
	}
	if !slices.Contains(labels, a.trDevice) {
		a.trDevice = labels[max(slices.Index(ids, a.prefs.Transcribe.Device), 0)]
	}
	if ui.Select(c.Key("tr-rec-device"), &a.trDevice, labels).Label(i18n.T("Input device")).Width(px(t, 220)).Changed() {
		id := ids[max(slices.Index(labels, a.trDevice), 0)]
		a.updateSettings(func(s *settings.Settings) { s.Transcribe.Device = id })
	}
	if m.err != "" {
		ui.Text(c, m.err).FontSize(px(t, 12)).TextColor(t.Danger).TextAlign(ui.Center)
	}
}
