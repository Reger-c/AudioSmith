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
	width := px(t, 280)
	inline, shown := a.trSide.layout(a, c, width, px(t, 480))
	w := a.pageWidth(c)
	if inline && shown {
		w -= width
	}
	a.trNarrow = w < px(t, 560)
	a.trTight = a.pageWidth(c) < px(t, 760)
	page := ui.Column(c.Key("transcribe-page")).Grow(1).MinHeight(0).AlignItems(ui.Stretch)
	if files := page.DroppedFiles(); files != nil {
		a.transcribeDropped(files)
	}
	page.Children(func() {
		pageHeader(c, i18n.T("Transcribe"), func() { a.transcribeToolbar(c, shown) })
		ui.Row(c).Grow(1).MinHeight(0).AlignItems(ui.Stretch).Children(func() {
			a.trSide.view(c, "tr-history", i18n.T("History"), inline, shown, true, width, func() {
				ui.Column(c).Grow(1).MinHeight(0).Children(func() { a.transcriptHistory(c, inline) })
			})
			ui.Scroll(c.Key("tr-main")).Grow(1).MinWidth(0).Children(func() {
				ui.Column(c).Padding(px(t, sp2), px(t, sp6), px(t, sp6)).Gap(px(t, sp3)).MaxWidth(px(t, 920)).Children(func() {
					a.transcriptStatus(c)
					if a.trSel == "" {
						a.transcribeSources(c)
					} else {
						a.transcriptDetail(c)
					}
				})
			}).ClipX()
		})
	})
}

// transcribeToolbar: the history toggle, mode, language, and New
// transcript (the page's primary while a transcript shows).
func (a *app) transcribeToolbar(c *ui.Context, shown bool) {
	busy := a.trJob != nil || a.trMic.phase != ""
	e := a.trModel(a.trMode == 0)
	mode := ui.Segmented(c.Key("tr-mode"), &a.trMode, i18n.T("Fast"), i18n.T("Accurate")).Label(i18n.T("Transcription quality")).Disabled(busy).Shrink(0).
		Tooltip(i18n.T("Fast uses a smaller model without word timings. Accurate uses the best model, then times every word."))
	if mode.Changed() {
		m := settings.TranscribeAccurate
		if a.trMode == 0 {
			m = settings.TranscribeFast
		}
		a.updateSettings(func(s *settings.Settings) { s.Transcribe.Mode = m })
	}
	a.trLanguageButton(c, e)
	a.trSide.toggle(c.Key("tr-list-toggle"), icons.PanelLeft, shown, i18n.T("Show the transcripts"), i18n.T("Hide the transcripts"))
	if a.trSel != "" {
		label := i18n.T("New transcript")
		var b ui.Element
		if a.trNarrow || a.trTight {
			b = primaryButton(c.Key("tr-new"), "").Label(label).Tooltip(label).Children(func() {
				ui.Icon(c, icons.AddCircle.Linear).Size(px(c.Theme(), 16), px(c.Theme(), 16))
			})
		} else {
			b = primaryIconButton(c.Key("tr-new"), icons.AddCircle, label).Label(label)
		}
		if b.Clicked() {
			a.selectTranscript("")
		}
	}
}

// trModelLine names the mode's model: "Whisper large-v3 turbo + word
// timings".
func (a *app) trModelLine() string {
	e := a.trModel(a.trMode == 0)
	s := e.Label
	if a.trMode == 1 {
		if al := trAligner(e); al.Repo != "" {
			s += " + " + i18n.T("word timings")
		}
	}
	return s
}

// trLanguageButton picks the language spoken, or detection.
func (a *app) trLanguageButton(c *ui.Context, e catalog.Entry) {
	t := c.Theme()
	k := tok(t)
	p := &a.trLang
	name := langName(a.prefs.Transcribe.Language)
	var btn ui.Element
	if a.trNarrow || a.trTight {
		btn = iconButton(c.Key("tr-lang"), icons.Global, name)
	} else {
		btn = dropButton(c.Key("tr-lang"), icons.Global, name).MaxWidth(px(t, 180))
	}
	btn.Tooltip(i18n.T("Language spoken"))
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
		_, h := c.Size()
		ui.Column(c.Key("tr-lang-picker")).Width(px(t, 300)).Gap(px(t, sp1)).Padding(px(t, sp1)).Children(func() {
			ui.TextInput(c.Key("tr-lang-search"), &p.query).Placeholder(i18n.T("Search 650 languages")).Label(i18n.T("Search languages"))
			auto := ui.ButtonBase(c.Key("tr-lang-auto")).Padding(px(t, 5), px(t, sp2)).Radius(px(t, radiusControl)).Label(i18n.T("Auto-detect")).Gap(px(t, sp2)).AlignItems(ui.Center)
			if auto.Hovered() {
				auto.Background(k.Hover)
			}
			if auto.Clicked() {
				choose("")
			}
			auto.Children(func() {
				ui.Text(c, i18n.T("Auto-detect")).Grow(1).SingleLine().Ellipsis("…").MinWidth(0)
				if a.prefs.Transcribe.Language == "" {
					iconMark(c, icons.Check, k.AccentInk, "")
				}
			})
			ui.List(c.Key("tr-lang-list"), &p.list, len(opts)).Height(min(px(t, 280), h*0.45)).ItemKey(func(i int) any { return opts[i].Code }).Rows(func(r ui.ListRow) {
				o := opts[r.Index]
				row := ui.ButtonBase(c.Key("tr-lang-"+o.Code)).Padding(px(t, 4), px(t, sp2)).Radius(px(t, radiusControl)).Disabled(!o.Supported).Label(o.NameIn(i18n.Locale())).Gap(px(t, 6)).AlignItems(ui.Center)
				if o.Supported && row.Hovered() {
					row.Background(k.Hover)
				}
				if o.Supported && row.Clicked() {
					choose(o.Code)
				}
				row.Children(func() {
					color := k.Text
					if !o.Supported {
						color = k.Text3
					}
					ui.Text(c, o.Flag()).Width(px(t, 22))
					ui.Text(c, o.NameIn(i18n.Locale())).TextColor(color).Grow(1).SingleLine().Ellipsis("…").MinWidth(0)
					if o.Code == a.prefs.Transcribe.Language {
						iconMark(c, icons.Check, k.AccentInk, "")
					}
					ui.Text(c, o.Code).FontSize(px(t, fsMicro)).TextColor(k.Text3)
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

// transcriptHistory is the list of transcripts (the left panel); inline
// says it is beside the page (else a drawer, which closes on a choice).
func (a *app) transcriptHistory(c *ui.Context, inline bool) {
	t := c.Theme()
	k := tok(t)
	list := a.shownTranscripts()
	ui.Column(c).Padding(px(t, sp3), px(t, sp3), px(t, sp2)).Gap(px(t, sp2)).Shrink(0).Children(func() {
		ui.Row(c).Gap(px(t, 6)).AlignItems(ui.Center).Children(func() {
			ui.Text(c, i18n.T("History")).FontWeight(600).Grow(1).MinWidth(0).SingleLine()
			ui.Text(c, i18n.N(len(a.trList), "%d transcript", "%d transcripts", len(a.trList))).FontSize(px(t, fsMicro)).TextColor(k.Text3).SingleLine()
			if len(a.trList) > 0 && iconButton(c.Key("tr-export-all"), icons.Export, i18n.T("Export all")).Tooltip(i18n.T("Every transcript in one text file")).Clicked() {
				a.exportAllTranscripts()
			}
		})
		if len(a.trList) > 0 {
			ui.Row(c).Children(func() {
				if searchField(c, "tr-search", &a.trQuery, i18n.T("Search transcripts"), 0).Changed() {
					a.searchTranscripts()
				}
			})
		}
	})
	if len(list) == 0 {
		ui.Column(c).Grow(1).Center().Padding(px(t, sp4)).Gap(px(t, sp1)).Children(func() {
			if len(a.trList) == 0 {
				ui.Text(c, i18n.T("No transcripts yet")).FontWeight(600).TextAlign(ui.Center)
				caption(c, i18n.T("Transcribe a file or a recording, or dictate anywhere with %s.", dictation.GlyphsOf(a.prefs.Dictation.Shortcut))).TextAlign(ui.Center)
			} else {
				ui.Text(c, i18n.T("No matching transcripts")).FontWeight(600).TextAlign(ui.Center)
				caption(c, i18n.T("Try a different search.")).TextAlign(ui.Center)
			}
		})
		return
	}
	ui.List(c.Key("tr-list"), &a.trListUI, len(list)).Grow(1).MinHeight(0).Gap(px(t, 2)).ItemKey(func(i int) any { return list[i].ID }).Rows(func(r ui.ListRow) {
		tr := list[r.Index]
		row := ui.ButtonBase(c.Key("tr-item-"+tr.ID)).Padding(px(t, sp2), px(t, sp2)).Margin(0, px(t, sp2)).Radius(px(t, radiusControl)).
			Label(transcriptTitle(tr)).Gap(px(t, sp2)).AlignItems(ui.Start)
		switch {
		case a.trSel == tr.ID:
			row.Background(k.Pressed)
		case row.Hovered():
			row.Background(k.Hover)
		}
		if row.Clicked() {
			a.selectTranscript(tr.ID)
			if !inline {
				a.trSide.open, a.trSide.set = false, true
			}
		}
		row.Children(func() {
			ic := icons.FileText
			if tr.Source == core.SourceDictation {
				ic = micIcon
			}
			ui.Icon(c, ic.Linear).Size(px(t, 14), px(t, 14)).TextColor(k.Text2).Margin(px(t, 2), 0, 0, 0).Shrink(0)
			ui.Column(c).Grow(1).MinWidth(0).Gap(px(t, 2)).Children(func() {
				ui.Text(c, transcriptTitle(tr)).FontSize(px(t, fsCaption)).FontWeight(600).SingleLine().Ellipsis("…").MinWidth(0)
				ui.Text(c, orDash(tr.Preferred())).FontSize(px(t, fsCaption)).TextColor(k.Text2).MaxLines(2).Ellipsis("…")
				meta := ago(tr.CreatedAt)
				if tr.Language != "" {
					meta += " · " + langName(tr.Language)
				}
				meta += " · " + clock(tr.Duration)
				ui.Text(c, meta).FontSize(px(t, fsMicro)).TextColor(k.Text3).SingleLine().Ellipsis("…")
			})
		})
	})
}

// transcriptStatus shows the transcription running, or why the last one
// failed.
func (a *app) transcriptStatus(c *ui.Context) {
	t := c.Theme()
	if j := a.trJob; j != nil {
		c.After(500 * time.Millisecond)
		fillCard(c.Key("tr-job")).Gap(px(t, sp2)).Padding(px(t, sp3), px(t, sp4)).Children(func() {
			ui.Row(c).Gap(px(t, sp2)).AlignItems(ui.Center).Children(func() {
				ui.Spinner(c)
				ui.Text(c, i18n.T("Transcribing %s", j.title)).FontWeight(600).Grow(1).MinWidth(0).SingleLine().Ellipsis("…")
				caption(c, formatDuration(time.Since(j.started).Seconds())).SingleLine()
				if ui.Button(c.Key("tr-cancel"), i18n.T("Cancel")).Disabled(j.cancelling).Clicked() {
					a.cancelTranscription()
				}
			})
			ui.Progress(c, j.fraction).Label(j.stage)
			caption(c, j.stage)
		})
	}
	if a.trErr != "" {
		card(c.Key("tr-error")).Gap(px(t, sp2)).Padding(px(t, sp3), px(t, sp4)).Children(func() {
			ui.Row(c).Gap(px(t, sp2)).AlignItems(ui.Center).Children(func() {
				iconMark(c, icons.DangerCircle, t.Danger, "")
				ui.Text(c, i18n.T("Transcription failed")).FontWeight(600).TextColor(t.Danger).Grow(1).MinWidth(0)
				if iconButton(c.Key("tr-dismiss"), icons.Close, i18n.T("Dismiss")).Clicked() {
					a.trErr, a.trRetry, a.trNeed = "", nil, nil
				}
			})
			caption(c, a.trErr).TextColor(tok(t).Text).Selectable()
			ui.Row(c).Gap(px(t, sp2)).Wrap().Children(func() {
				if e := a.trNeed; e != nil {
					m := a.model(*e)
					label := i18n.T("Download %s", e.Label)
					if m.downloading {
						label = i18n.T("Downloading…")
					}
					if secondaryIconButton(c.Key("tr-download"), icons.DownloadMinimalistic, label).Label(label).Disabled(m.downloading).Clicked() {
						a.download(*e)
					}
				}
				if r := a.trRetry; r != nil {
					if ghostIconButton(c.Key("tr-retry"), icons.Restart, i18n.T("Retry")).Label(i18n.T("Retry")).Clicked() {
						a.queueTranscription(*r, true)
					}
				}
			})
		})
	}
}

// transcribeSources: a file, or the microphone.
func (a *app) transcribeSources(c *ui.Context) {
	t := c.Theme()
	k := tok(t)
	e := a.trModel(a.trMode == 0)
	if ready, known := a.modelOnDisk(e); known && !ready {
		notice(c, "tr-need-model", noticeWarn, i18n.T("Transcribing needs %s, which is not downloaded yet.", e.Label), "", func() {
			m := a.model(e)
			label := i18n.T("Download")
			if m.downloading {
				label = i18n.T("Downloading…")
			}
			if secondaryIconButton(c.Key("tr-need-download"), icons.DownloadMinimalistic, label).Label(label).Disabled(m.downloading).Clicked() {
				a.download(e)
			}
		}, nil)
	}
	ui.Column(c).Gap(px(t, 2)).Children(func() {
		ui.Text(c, i18n.T("Turn speech into text: a meeting, an interview, a voice memo or a video. Everything stays on this Mac.")).TextColor(k.Text2)
		ui.Row(c).Gap(px(t, 6)).AlignItems(ui.Center).Children(func() {
			iconMark(c, icons.Box, k.Text3, "")
			ui.Text(c, a.trModelLine()).FontSize(px(t, fsCaption)).TextColor(k.Text3).SingleLine().Ellipsis("…").MinWidth(0)
		})
	})
	busy := a.trJob != nil
	ui.Row(c).Gap(px(t, sp3)).AlignItems(ui.Stretch).Wrap().Children(func() {
		zone := ui.ButtonBase(c.Key("tr-upload")).Grow(1).Basis(px(t, 240)).MinHeight(px(t, 180)).Padding(px(t, sp4)).
			Radius(px(t, radiusCard)).Border(1, k.G[5]).BorderStyle(ui.BorderDashed).Label(i18n.T("Choose a file")).Disabled(busy)
		if files := zone.DroppedFiles(); files != nil {
			a.transcribeDropped(files)
		}
		switch {
		case zone.FileDragOver():
			zone.Border(2, k.Accent).Background(k.AccentSoft)
		case zone.Hovered():
			zone.Background(k.Subtle)
		}
		if zone.Clicked() {
			a.chooseTranscribeFile()
		}
		zone.Children(func() {
			ui.Column(c).Grow(1).Center().Gap(px(t, sp2)).Children(func() {
				ui.Icon(c, icons.Import.Linear).Size(px(t, 28), px(t, 28)).TextColor(k.Text3)
				ui.Text(c, i18n.T("Drop audio or video here, or click to choose")).FontWeight(600).TextAlign(ui.Center)
				caption(c, i18n.T("WAV, MP3, M4A, FLAC, OGG, AAC — or a video")).TextAlign(ui.Center)
			})
		})
		card(c.Key("tr-record")).Grow(1).Basis(px(t, 240)).MinHeight(px(t, 180)).Gap(px(t, sp2)).AlignItems(ui.Center).Justify(ui.Center).Children(func() {
			a.trRecorder(c, busy)
		})
	})
	caption(c, i18n.T("Dictate into any app with %s: the text is typed where your cursor is. Set it up in Settings → Dictation.",
		dictation.GlyphsOf(a.prefs.Dictation.Shortcut))).TextColor(k.Text3)
}

// transcriptDetail is the transcript shown: its title and actions (Copy,
// Export ▾, Reuse as script, ⋯ Delete), the player, the clean-up and the
// segments.
func (a *app) transcriptDetail(c *ui.Context) {
	t := c.Theme()
	k := tok(t)
	tr := a.trDetail
	if tr == nil {
		ui.Row(c).Gap(px(t, sp2)).Children(func() {
			ui.Spinner(c)
			caption(c, i18n.T("Reading the transcript…"))
		})
		return
	}
	ui.Row(c).Gap(px(t, sp1)).AlignItems(ui.Center).Children(func() {
		ui.Column(c).Gap(px(t, 2)).Grow(1).MinWidth(0).Children(func() {
			sectionHeading(c, transcriptTitle(*tr)).Tooltip(transcriptTitle(*tr))
			meta := []string{i18n.DateTime(tr.CreatedAt), langName(tr.Language), clock(tr.Duration), catalog.Builtin().DisplayName(tr.Model)}
			if tr.Mode == settings.TranscribeFast {
				meta = append(meta, i18n.T("Fast"))
			}
			if tr.App != "" {
				meta = append(meta, tr.App)
			}
			truncText(c, strings.Join(meta, " · ")).FontSize(px(t, fsCaption)).TextColor(k.Text2)
		})
		if iconButton(c.Key("tr-copy"), icons.Copy, i18n.T("Copy")).Clicked() {
			mygoClipboard(c, tr.Preferred())
			a.toast(i18n.T("Copied to the clipboard"))
		}
		var exp ui.Element
		if a.trNarrow {
			exp = iconButton(c.Key("tr-export"), icons.Export, i18n.T("Export"))
		} else {
			exp = dropButton(c.Key("tr-export"), icons.Export, i18n.T("Export")).Shrink(0)
		}
		if exp.Clicked() {
			a.trExport = !a.trExport
		}
		ui.Popover(c, exp, &a.trExport, func() {
			ui.Column(c.Key("tr-export-menu")).Padding(px(t, sp1)).Gap(px(t, 2)).Width(px(t, 240)).Children(func() {
				for _, f := range services.ExportFormats {
					b := ui.ButtonBase(c.Key("tr-export-"+f)).Padding(px(t, 6), px(t, sp2)).Radius(px(t, radiusControl)).Label(exportLabel(f))
					if b.Hovered() {
						b.Background(k.Hover)
					}
					if b.Children(func() { ui.Text(c, exportLabel(f)).SingleLine() }).Clicked() {
						a.exportTranscript(f)
					}
				}
			})
		})
		reuse := i18n.T("Reuse as script")
		var rb ui.Element
		if a.trNarrow {
			rb = iconButton(c.Key("tr-reuse"), icons.Soundwave, reuse)
		} else {
			rb = ghostIconButton(c.Key("tr-reuse"), icons.Soundwave, reuse).Label(reuse).Shrink(0)
		}
		if rb.Tooltip(i18n.T("Open the text in the Studio")).Clicked() {
			a.reuseTranscript()
		}
		moreMenu(c.Key("tr-more"), i18n.T("Transcript actions"), func(m *ui.Menu) {
			if m.Item(i18n.T("Delete…")).Chosen() {
				a.trConfirm = tr.ID
			}
		})
	})
	if a.trConfirm == tr.ID {
		notice(c, "tr-delete-ask", noticeWarn, i18n.T("Delete this transcript?"), "", func() {
			if ghostButton(c.Key("tr-delete-no"), i18n.T("Keep")).Clicked() {
				a.trConfirm = ""
			}
			if recordButton(c.Key("tr-delete-yes"), i18n.T("Delete")).Clicked() {
				a.deleteTranscript(tr.ID)
			}
		}, nil)
	}
	if a.trWarn != "" {
		notice(c, "tr-warn", noticeWarn, a.trWarn, "", nil, nil)
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
	card(c.Key("tr-player")).Row().Gap(px(t, sp3)).AlignItems(ui.Center).Padding(px(t, sp2), px(t, sp3)).Children(func() {
		if playButton(c.Key("tr-play"), a.pl.Playing(id), false, 36).Clicked() {
			a.toggleTranscriptPlayback()
		}
		seek := pos
		if ui.Slider(c.Key("tr-seek"), &seek, 0, tr.Duration).Grow(1).MinWidth(0).Label(i18n.T("Position")).Changed() {
			a.playTranscriptAt(seek)
		}
		ui.Text(c, clock(pos)+" / "+clock(tr.Duration)).FontSize(px(t, fsMicro)).Font(monoFont).TextColor(tok(t).Text2).SingleLine().Shrink(0)
	})
}

// transcriptCleanup: the clean-up, its original, and the buttons that make it.
func (a *app) transcriptCleanup(c *ui.Context, tr *core.Transcript) {
	t := c.Theme()
	k := tok(t)
	cleaning := a.trClean != nil && a.trClean.id == tr.ID
	ui.Row(c.Key("tr-clean-bar")).Gap(px(t, sp1)).AlignItems(ui.Center).Wrap().Children(func() {
		if ghostIconButton(c.Key("tr-polish"), icons.PenNewRound, i18n.T("Polish")).Label(i18n.T("Polish")).Disabled(cleaning).
			Tooltip(i18n.T("Remove fillers, fix spacing, capitals and punctuation, without a language model")).Clicked() {
			a.polishTranscript()
		}
		avail, who := a.llmAvailable()
		clean := ghostIconButton(c.Key("tr-cleanup"), icons.MagicStick3, i18n.T("Clean up")).Label(i18n.T("Clean up")).Disabled(cleaning || !avail)
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
			caption(c, i18n.T("Cleaning up…"))
			if ghostButton(c.Key("tr-clean-stop"), i18n.T("Stop")).Clicked() {
				a.trClean.cancel()
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
	fillCard(c.Key("tr-clean")).Gap(px(t, sp2)).Children(func() {
		ui.Row(c).Gap(px(t, sp1)).AlignItems(ui.Center).Children(func() {
			ui.Text(c, by).FontSize(px(t, fsCaption)).FontWeight(600).TextColor(k.Text2).Grow(1).MinWidth(0).SingleLine().Ellipsis("…")
			if !cleaning && ghostButton(c.Key("tr-clean-drop"), i18n.T("Remove the clean-up")).Clicked() {
				a.dropClean()
			}
		})
		ui.Text(c, tr.Clean).Selectable()
		label := i18n.T("Show the original transcript")
		if a.trOrig {
			label = i18n.T("Hide the original transcript")
		}
		ui.Row(c).Children(func() {
			ic := icons.AltArrowDown
			if a.trOrig {
				ic = icons.AltArrowUp
			}
			if ghostIconButton(c.Key("tr-orig"), ic, label).Label(label).Clicked() {
				a.trOrig = !a.trOrig
			}
		})
		if a.trOrig {
			caption(c, tr.Text()).Selectable()
		}
	})
}

// transcriptSegments lists the sentences: time (click to play from
// there), speaker, text (click to edit), and the timed words of the
// sentence playing (click one to play from it).
func (a *app) transcriptSegments(c *ui.Context, tr *core.Transcript) {
	t := c.Theme()
	k := tok(t)
	pos := a.trPosition()
	speakers := false
	for _, s := range tr.Segments {
		if s.Speaker != "" {
			speakers = true
		}
	}
	ui.Row(c).Gap(px(t, sp2)).AlignItems(ui.Center).Margin(px(t, sp2), 0, 0, 0).Children(func() {
		sectionHeading(c, i18n.N(len(tr.Segments), "%d segment", "%d segments", len(tr.Segments))).Grow(1)
		if !speakers {
			ui.Text(c, i18n.T("Speaker labels: not yet")).FontSize(px(t, fsMicro)).TextColor(k.Text3).SingleLine().
				Tooltip(i18n.T("Telling speakers apart comes with speaker diarization in a later version."))
		}
	})
	if len(tr.Segments) == 0 {
		caption(c, i18n.T("No speech was found."))
		return
	}
	shown := max(a.trShown, trPage)
	// The sentence playing shows, however far it is.
	for i, s := range tr.Segments {
		if pos >= s.Start && pos < s.End && i >= shown {
			shown = (i/trPage + 1) * trPage
		}
	}
	ui.Column(c).Gap(px(t, 2)).Children(func() {
		for i, s := range tr.Segments {
			if i >= shown {
				rest := len(tr.Segments) - shown
				if ghostButton(c.Key("tr-more"), i18n.N(min(rest, trPage), "Show %d more segment", "Show %d more segments", min(rest, trPage))).Clicked() {
					a.trShown = shown + trPage
				}
				break
			}
			playing := pos >= 0 && pos >= s.Start && pos < s.End
			row := ui.Row(c.Key(fmt.Sprintf("tr-seg-%d", i))).Gap(px(t, sp2)).AlignItems(ui.Start).Padding(px(t, 4), px(t, 4)).Radius(px(t, radiusControl))
			if playing {
				row.Background(k.AccentSoft)
			}
			row.Children(func() {
				tb := ui.ButtonBase(c.Key("time")).Padding(px(t, 3), px(t, 6)).Radius(px(t, 6)).Label(clock(s.Start)).Tooltip(i18n.T("Play from here")).Shrink(0)
				if tb.Hovered() {
					tb.Background(k.Hover)
				}
				if tb.Clicked() {
					a.playTranscriptAt(s.Start)
				}
				tb.Children(func() { ui.Text(c, clock(s.Start)).Font(monoFont).FontSize(px(t, fsMicro)).TextColor(k.AccentInk) })
				if s.Speaker != "" {
					tagChip(c, speakerLabel(*tr, s.Speaker)).Margin(px(t, 2), 0, 0, 0)
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
							if ui.Button(c.Key("edit-save"), i18n.T("Save")).Tooltip("⌘↩").Clicked() {
								a.saveSegmentEdit()
							}
							if ghostButton(c.Key("edit-cancel"), i18n.T("Cancel")).Tooltip("Esc").Clicked() {
								a.trEdit = -1
							}
						})
						return
					}
					txt := ui.ButtonBase(c.Key("text")).Label(s.Text).Tooltip(i18n.T("Click to edit")).Padding(px(t, 2), px(t, 4)).Radius(px(t, 6)).Gap(px(t, sp2)).Cursor(ui.CursorText)
					if txt.Hovered() && !playing {
						txt.Background(k.Hover)
					}
					if txt.Clicked() {
						a.editSegment(i)
					}
					txt.Children(func() {
						ui.Text(c, s.Text).Grow(1).MinWidth(0)
						if s.Edited {
							tagChip(c, i18n.T("edited"))
						}
					})
					if playing && len(s.Words) > 0 && !s.Edited {
						ui.Row(c.Key("words")).Gap(px(t, 4)).Wrap().Children(func() {
							for wi, w := range s.Words {
								b := ui.ButtonBase(c.Key(wi)).Padding(px(t, 1), px(t, 4)).Radius(px(t, 4)).Label(w.Text)
								if pos >= w.Start && pos < w.End {
									b.Background(t.Selection)
								} else if b.Hovered() {
									b.Background(k.Hover)
								}
								if b.Clicked() {
									a.playTranscriptAt(w.Start)
								}
								b.Children(func() { ui.Text(c, w.Text).FontSize(px(t, fsCaption)) })
							}
						})
					}
				})
			})
		}
	})
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
	k := tok(t)
	m := &a.trMic
	a.trRecEnded()
	switch m.phase {
	case "recording":
		c.After(100 * time.Millisecond)
		ui.Text(c, clock(time.Since(m.started).Seconds())).FontSize(px(t, fsTitle)).FontWeight(600).Font(monoFont)
		ui.Meter(c, float64(levelFraction(m.level)), 0, 1, nil).Width(px(t, 180)).Label(i18n.T("Input level"))
		if inputDetected(m.level) {
			ui.Row(c).Gap(px(t, 6)).AlignItems(ui.Center).Children(func() {
				statusDot(c, k.Success)
				caption(c, i18n.T("Input detected"))
			})
		} else {
			caption(c, i18n.T("No input detected")).TextColor(k.Text3)
		}
		if recordButton(c.Key("tr-rec-stop"), i18n.T("Stop recording")).Clicked() {
			a.trRecStop()
		}
		return
	case "starting", "stopping":
		ui.Spinner(c)
		label := i18n.T("Starting…")
		if m.phase == "stopping" {
			label = i18n.T("Finishing…")
		}
		caption(c, label)
		return
	}
	ui.Icon(c, micIcon.Linear).Size(px(t, 28), px(t, 28)).TextColor(k.Text3)
	caption(c, i18n.T("Record a meeting, a lecture or a voice memo.")).TextAlign(ui.Center)
	labels, ids := []string{i18n.T("System default")}, []string{""}
	for _, d := range a.clone.devices {
		labels, ids = append(labels, d.Name), append(ids, d.ID)
	}
	if !slices.Contains(labels, a.trDevice) {
		a.trDevice = labels[max(slices.Index(ids, a.prefs.Transcribe.Device), 0)]
	}
	ui.Row(c).Gap(px(t, sp2)).AlignItems(ui.Center).Wrap().Justify(ui.Center).Children(func() {
		if ui.Select(c.Key("tr-rec-device"), &a.trDevice, labels).Label(i18n.T("Input device")).Tooltip(i18n.T("Input device")).Width(px(t, 180)).Shrink(1).MinWidth(px(t, 100)).Changed() {
			id := ids[max(slices.Index(labels, a.trDevice), 0)]
			a.updateSettings(func(s *settings.Settings) { s.Transcribe.Device = id })
		}
		if recordButton(c.Key("tr-rec-start"), i18n.T("Record")).Disabled(busy).Clicked() {
			a.trRecStart()
		}
	})
	if m.err != "" {
		caption(c, m.err).TextColor(t.Danger).TextAlign(ui.Center)
	}
}
