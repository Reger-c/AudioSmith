package main

// The Dub page (/dub): a stepper (Source → Transcribe → Translate → Cast
// → Generate → Export) whose step shows its panel on the left, and the
// workspace on the right: banners (an interrupted run, errors with their
// fix, review checkpoints, stage warnings), the A/B player and video
// preview, the timeline lane, and the transcript editor. The footer says
// what runs and offers the step's next action.

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/hehehai/AudioSmith/internal/core"
	"github.com/hehehai/AudioSmith/internal/dub"
	"github.com/hehehai/AudioSmith/internal/i18n"
	"github.com/hehehai/AudioSmith/internal/icons"
	"github.com/hehehai/AudioSmith/internal/jobs"
	"github.com/hehehai/AudioSmith/internal/services"
	"github.com/hehehai/AudioSmith/internal/settings"
	"github.com/hehehai/AudioSmith/internal/translate"
)

var (
	dubIcon   = icons.VideoFramePlayHorizontal
	batchIcon = icons.LayersMinimalistic
)

// dubTargetLangs are the languages the pickers offer first (any BCP-47
// code can be typed too).
var dubTargetLangs = []string{"es", "es-MX", "fr", "fr-CA", "de", "it", "pt", "pt-BR", "zh-Hans", "zh-Hant", "yue", "ja", "ko",
	"en", "en-GB", "ru", "uk", "pl", "nl", "sv", "da", "nb", "fi", "cs", "el", "tr", "ar", "he", "fa", "hi", "bn", "ur", "ta", "te",
	"id", "ms", "vi", "th", "fil", "sw", "ro", "hu"}

// dubDialects are the dialect variants offered for a language.
var dubDialects = map[string][]string{
	"es": {"es-ES", "es-MX", "es-AR", "es-CO", "es-CL"}, "pt": {"pt-BR", "pt-PT"}, "en": {"en-US", "en-GB", "en-AU", "en-IN"},
	"fr": {"fr-FR", "fr-CA", "fr-BE"}, "de": {"de-DE", "de-AT", "de-CH"}, "ar": {"ar-EG", "ar-SA", "ar-MA"}, "nl": {"nl-NL", "nl-BE"},
}

// dubPage is /dub: the stepper, the workspace, and the step's panel on
// the right (a drawer when the window is narrow); the footer says what
// runs and holds the step's one primary action.
func (a *app) dubPage(c *ui.Context) {
	t := c.Theme()
	k := tok(t)
	a.dubShortcuts(c)
	if a.dubPlaying() {
		c.After(100 * time.Millisecond) // the playhead moves
		a.dubVideoSync(false)
	}
	title := i18n.T("Dubbing")
	if a.dub != nil && a.dub.Title != "" {
		title = i18n.T("Dubbing") + " · " + a.dub.Title
	}
	width := px(t, inspectorWidth+20)
	inline, shown := a.dubSide.layout(a, c, width, px(t, 480))
	w := a.pageWidth(c)
	if inline && shown {
		w -= width
	}
	a.dubNarrow = w < px(t, 560)
	// Narrow, before the transcript: the workspace has nothing to show
	// yet, so the step's panel takes its place.
	embed := a.dub != nil && !inline && len(a.dub.Segments) == 0
	if embed {
		shown = false
	}
	pageHeader(c, title, func() { a.dubHeaderActions(c, shown, embed) })
	if a.dub == nil {
		a.dubEmpty(c)
		a.dubConfirmDialog(c)
		return
	}
	a.dubStepper(c)
	ui.Box(c).Height(1).Background(k.Line).Shrink(0)
	ui.Row(c).Grow(1).MinHeight(0).AlignItems(ui.Stretch).Children(func() {
		// The workspace fills the page; when the window is too short for
		// its notices, player, timeline and some lines, it scrolls.
		_, winH := c.Size()
		avail := winH - titleBarHeight - px(t, statusBarHeight) - px(t, 40) - px(t, 52) - 4
		ui.Scroll(c.Key("dub-work")).Grow(1).MinWidth(0).Children(func() {
			ui.Column(c).MinHeight(avail).Padding(px(t, sp3), px(t, sp6), px(t, sp2)).Gap(px(t, sp2)).Children(func() {
				a.dubBanners(c)
				if embed {
					ui.Column(c.Key("dub-embed")).Gap(px(t, sp3)).MaxWidth(px(t, 520)).Children(func() { a.dubStepPanel(c) })
					return
				}
				a.dubWorkspace(c)
			})
		}).ClipX()
		a.dubSide.view(c, "dub-side", i18n.T("Step settings"), inline, shown, false, width, func() {
			ui.Column(c).Padding(px(t, sp4), px(t, sp4), px(t, sp5)).Gap(px(t, sp3)).Children(func() { a.dubStepPanel(c) })
		})
	})
	ui.Box(c).Height(1).Background(k.Line).Shrink(0)
	a.dubFooter(c)
	if a.dubGloss.open {
		ui.Modal(c.Key("dub-gloss-modal"), &a.dubGloss.open, func() { a.dubGlossaryPanel(c) })
	}
	if a.dubPaste.open {
		ui.Modal(c.Key("dub-paste-modal"), &a.dubPaste.open, func() { a.dubPastePanel(c) })
	}
	a.dubConfirmDialog(c)
}

// dubStepPanel is the panel of the step shown.
func (a *app) dubStepPanel(c *ui.Context) {
	switch a.dubStep {
	case dubStepSource:
		a.dubSourcePanel(c)
	case dubStepTranscribe:
		a.dubTranscribePanel(c)
	case dubStepTranslate:
		a.dubTranslatePanel(c)
	case dubStepCast:
		a.dubCastPanel(c)
	case dubStepGenerate:
		a.dubGeneratePanel(c)
	case dubStepExport:
		a.dubExportPanel(c)
	}
}

// dubHeaderActions: Projects ▾, New dub, the step panel's toggle, and ⋯
// (Create Story, Delete, Batch).
func (a *app) dubHeaderActions(c *ui.Context, shown, embed bool) {
	projects := i18n.T("Projects")
	var b ui.Element
	if a.dubNarrow {
		b = iconButton(c.Key("dub-projects"), icons.FolderWithFiles, projects)
	} else {
		b = dropButton(c.Key("dub-projects"), icons.FolderWithFiles, projects)
	}
	b.Menu(func(m *ui.Menu) {
		if len(a.dubList) == 0 {
			m.Item(i18n.T("No dubs yet")).Disabled(true)
		}
		for _, r := range a.dubList {
			id := r.ID
			label := firstOf(r.Title, i18n.T("Untitled dub")) + " · " + dubPhaseText(dub.Phase(r.Phase))
			if m.Item(label).Checked(a.dub != nil && a.dub.ID == id).Chosen() {
				a.openDub(id)
			}
		}
	})
	if a.dub != nil {
		newDub := i18n.T("New dub")
		if a.dubNarrow {
			b = iconButton(c.Key("dub-new"), icons.AddCircle, newDub)
		} else {
			b = ghostIconButton(c.Key("dub-new"), icons.AddCircle, newDub).Label(newDub)
		}
		if b.Clicked() {
			a.dubStopPlayer()
			a.dub = nil
			a.updateSettings(func(s *settings.Settings) { s.Dubbing.Open = "" })
			return
		}
	}
	if a.dub != nil && !embed {
		a.dubSide.toggle(c.Key("dub-panel"), icons.PanelRight, shown, i18n.T("Show the step settings"), i18n.T("Hide the step settings"))
	}
	moreMenu(c.Key("dub-more-actions"), "", func(m *ui.Menu) {
		if a.dub != nil {
			if m.Item(i18n.T("Create Story")).Disabled(len(a.dub.Segments) == 0).Chosen() {
				a.dubAskStory()
			}
			if m.Item(i18n.T("Delete…")).Chosen() {
				a.dubConfirm, a.dubConfirmOpen = "delete:"+a.dub.ID, true
			}
			m.Separator()
		}
		if m.Item(i18n.T("Batch dubbing")).Chosen() {
			a.navigate("/batch")
		}
	})
}

// dubEmpty is the page without a project: drop a file or paste a link.
func (a *app) dubEmpty(c *ui.Context) {
	t := c.Theme()
	k := tok(t)
	ui.Scroll(c.Key("dub-empty")).Grow(1).Children(func() {
		ui.Column(c).Padding(px(t, sp4), px(t, sp6), px(t, sp6)).Gap(px(t, sp4)).AlignItems(ui.Center).Children(func() {
			zone := ui.Column(c.Key("dub-drop")).FillWidth().MaxWidth(px(t, 560)).Padding(px(t, sp6)).Gap(px(t, sp3)).AlignItems(ui.Center).
				Radius(px(t, radiusCard)).Border(1, k.Line)
			if files := zone.DroppedFiles(); len(files) > 0 {
				a.dubDropped(files)
			}
			if zone.FileDragOver() {
				zone.Border(2, k.Accent)
			}
			zone.Children(func() {
				ui.Icon(c, dubIcon.Linear).Size(px(t, 36), px(t, 36)).TextColor(k.Text3)
				ui.Text(c, i18n.T("Video and audio dubbing")).FontSize(px(t, fsSection)).FontWeight(600).TextAlign(ui.Center)
				caption(c, i18n.T("Drop a video or audio file here")).TextAlign(ui.Center)
				ui.Text(c, "MP4 · MOV · M4V · MKV · WEBM · MP3 · WAV · M4A · FLAC").FontSize(px(t, fsMicro)).TextColor(k.Text3).TextAlign(ui.Center)
				if primaryButton(c.Key("dub-choose"), i18n.T("Choose file…")).Clicked() {
					a.chooseDubSource()
				}
				ui.Row(c).Gap(px(t, 6)).AlignItems(ui.Center).FillWidth().MaxWidth(px(t, 480)).Children(func() {
					ui.TextInput(c.Key("dub-url"), &a.dubURL).Placeholder(i18n.T("…or paste a YouTube or video link")).Label(i18n.T("Video link")).Grow(1).MinWidth(0)
					if ui.Button(c.Key("dub-url-go"), i18n.T("Import")).Disabled(strings.TrimSpace(a.dubURL) == "").Clicked() {
						a.dubImportURL()
					}
				})
				ui.Column(c).FillWidth().MaxWidth(px(t, 480)).Children(func() {
					collapsible(c.Key("dub-url-more"), i18n.T("Link options"), &a.dubURLMore, nil, func() {
						ui.Checkbox(c.Key("dub-captions"), &a.dubCaptions, i18n.T("Use the video's captions when it has them (written by people)"))
						ui.Row(c).Gap(px(t, 6)).AlignItems(ui.Center).Children(func() {
							label := i18n.T("Sign-in cookies (cookies.txt)…")
							if a.dubCookies != "" {
								label = shortPath(a.dubCookies)
							}
							if ui.Button(c.Key("dub-cookies"), label).Shrink(1).MinWidth(0).Clicked() {
								a.dubChooseCookies()
							}
							if a.dubCookies != "" && iconButton(c.Key("dub-cookies-x"), icons.Close, i18n.T("Remove the cookies")).Clicked() {
								a.dubCookies = ""
							}
							infoTip(c.Key("dub-cookies-tip"), i18n.T("Cookies are used for this download only and never saved."))
						})
					})
				})
				if a.dubErr != "" {
					ui.Column(c).FillWidth().Children(func() {
						notice(c, "dub-url-err", noticeError, a.dubErr, "", func() {
							if a.dubErrCode == dub.CodeNoYtDlp && ui.Button(c.Key("dub-url-settings"), i18n.T("Open Settings → Dubbing")).Clicked() {
								a.navigate("/settings/dubbing")
							}
						}, nil)
					})
				}
			})
			ui.Row(c).Gap(px(t, sp2)).Wrap().Justify(ui.Center).Children(func() {
				for i, s := range []string{i18n.T("Upload & transcribe"), i18n.T("Translate"), i18n.T("Generate the dub")} {
					if i > 0 {
						iconMark(c, icons.AltArrowRight, k.Text3, "")
					}
					ui.Text(c, strconv.Itoa(i+1)+" · "+s).FontSize(px(t, fsCaption)).TextColor(k.Text2)
				}
			})
			if len(a.dubList) > 0 {
				ui.Column(c.Key("dub-recent")).FillWidth().MaxWidth(px(t, 560)).Gap(px(t, 2)).Children(func() {
					sectionTitle(c, i18n.T("Recent dubs"))
					for _, r := range a.dubList[:min(len(a.dubList), 8)] {
						id := r.ID
						row := ui.Row(c.Key("recent-"+id)).Gap(px(t, sp2)).AlignItems(ui.Center).Padding(px(t, 6), px(t, sp2)).Radius(px(t, radiusControl))
						if row.Hovered() {
							row.Background(k.Hover)
						}
						row.Children(func() {
							iconMark(c, dubIcon, k.Text2, "")
							ui.Column(c).Grow(1).MinWidth(0).Children(func() {
								truncText(c, firstOf(r.Title, i18n.T("Untitled dub")))
								meta := []string{dubPhaseText(dub.Phase(r.Phase))}
								if r.Duration > 0 {
									meta = append(meta, tlClock(r.Duration))
								}
								for _, l := range r.Languages {
									meta = append(meta, dubLangName(l))
								}
								truncText(c, strings.Join(meta, " · ")).FontSize(px(t, fsMicro)).TextColor(k.Text2)
							})
							if ghostButton(c.Key("open"), i18n.T("Open")).Clicked() {
								a.openDub(id)
							}
						})
					}
				})
			}
		})
	})
}

// dubChooseCookies picks a cookies.txt for the next URL import.
func (a *app) dubChooseCookies() {
	go func() {
		paths, err := dialogOpen(a, i18n.T("Choose a cookies.txt export"), "txt")
		if err != nil || len(paths) == 0 {
			return
		}
		a.post(func() { a.dubCookies = paths[0] })
	}()
}

// --- the stepper ------------------------------------------------------------------------

// dubStepState is a step's state for the stepper.
func (a *app) dubStepState(step int) (done, running, failed bool) {
	p := a.dub
	running = false
	for _, j := range a.dubJobs {
		st := dubStageOfKind(j.Kind)
		switch {
		case step == dubStepSource && (st == dub.StageFetch || st == dub.StageIngest),
			step == dubStepTranscribe && (st == dub.StageSeparate || st == dub.StageTranscribe || st == dub.StageReferences),
			step == dubStepTranslate && st == dub.StageTranslate,
			step == dubStepGenerate && (st == dub.StageSynthesize || st == dub.StageMix || st == dub.StageQC):
			running = true
		}
	}
	for k, st := range p.Stages {
		if st.Status != dub.StatusFailed {
			continue
		}
		s := dub.Stage(strings.Split(k, ":")[0])
		switch {
		case step == dubStepSource && (s == dub.StageFetch || s == dub.StageIngest),
			step == dubStepTranscribe && (s == dub.StageSeparate || s == dub.StageTranscribe || s == dub.StageReferences),
			step == dubStepTranslate && s == dub.StageTranslate,
			step == dubStepGenerate && (s == dub.StageSynthesize || s == dub.StageMix):
			failed = true
		}
	}
	switch step {
	case dubStepSource:
		done = p.Source.Path != "" && p.Media.Audio != ""
	case dubStepTranscribe:
		done = len(p.Segments) > 0
	case dubStepTranslate:
		done = p.AllTranslated()
	case dubStepCast:
		done = len(p.Segments) > 0 && p.Stage(dub.StageReferences, "").Status != ""
	case dubStepGenerate:
		done = p.TracksReady()
	case dubStepExport:
		done = a.dubExport.last != ""
	}
	return done, running, failed
}

// dubStepper is the row of steps: a quiet row where done steps show a
// check, the current one is on the pressed face, and the rest are grey.
// When the workspace is narrow only the current step shows its name.
func (a *app) dubStepper(c *ui.Context) {
	t := c.Theme()
	k := tok(t)
	names := []string{i18n.T("Source"), i18n.T("Transcribe"), i18n.T("Translate"), i18n.T("Cast"), i18n.T("Generate"), i18n.T("Export")}
	compact := a.dubNarrow || a.pageWidth(c) < px(t, 760)
	ui.Row(c.Key("dub-steps")).Gap(px(t, 2)).Padding(px(t, 2), px(t, sp5), px(t, sp2)).AlignItems(ui.Center).FocusGroup(ui.Horizontal).Shrink(0).ClipX().Children(func() {
		for i, name := range names {
			done, running, failed := a.dubStepState(i)
			cur := a.dubStep == i
			label := i18n.T("Step %d: %s", i+1, name)
			b := ghostBase(c.Key("step-"+strconv.Itoa(i))).Padding(px(t, 5), px(t, sp2)).Gap(px(t, 6)).AlignItems(ui.Center).Label(label).Tooltip(name).Shrink(0)
			if cur {
				b.Background(k.Pressed)
			}
			b.Children(func() {
				col := k.Text3
				switch {
				case cur:
					col = k.Text
				case done:
					col = k.Text2
				}
				ui.Box(c).Size(px(t, 16), px(t, 16)).Center().Shrink(0).Children(func() {
					switch {
					case running:
						ui.Spinner(c).Size(px(t, 14), px(t, 14))
					case failed:
						iconMark(c, icons.DangerCircle, k.Danger, "")
					case done:
						iconMark(c, icons.CheckCircle, k.AccentInk, "")
					default:
						ui.Text(c, strconv.Itoa(i+1)).FontSize(px(t, fsMicro)).FontWeight(600).TextColor(col)
					}
				})
				if cur || !compact {
					txt := ui.Text(c, name).SingleLine().TextColor(col)
					if cur {
						txt.FontWeight(600)
					}
				}
			})
			if b.Clicked() {
				a.dubCommitEdit()
				a.dubStep = i
			}
			if i < len(names)-1 {
				ui.Icon(c, icons.AltArrowRight.Linear).Size(px(t, 12), px(t, 12)).TextColor(k.Text3).Shrink(0)
			}
		}
	})
}

// --- banners --------------------------------------------------------------------------------

// dubBanners: an interrupted run, the last error, review checkpoints and
// stage warnings, each a one-line notice.
func (a *app) dubBanners(c *ui.Context) {
	p := a.dub
	if a.dubInterrupted() {
		notice(c, "b-recover", noticeWarn, i18n.T("The last run stopped before it finished (the app quit, or a step failed)."), "", func() {
			if ui.Button(c.Key("b-resume"), i18n.T("Continue the run")).Clicked() {
				a.dubResume()
			}
		}, func() { a.dubSet(func(p *dub.Project) { p.Run = nil }) })
	}
	if a.dubErr != "" {
		notice(c, "b-error", noticeError, a.dubErr, "", func() {
			if a.dubNeed != nil {
				e := *a.dubNeed
				if secondaryIconButton(c.Key("b-download"), icons.DownloadMinimalistic, i18n.T("Download")).Label(i18n.T("Download")).Clicked() {
					a.download(e)
					a.dubErr, a.dubNeed = "", nil
				}
			}
			switch a.dubErrCode {
			case services.DubCodeNoTranslator:
				if ui.Button(c.Key("b-llm"), i18n.T("Set up writing help")).Clicked() {
					a.navigate("/settings/writing")
				}
			case dub.CodeNoYtDlp:
				if ui.Button(c.Key("b-ytdlp"), i18n.T("Open Settings → Dubbing")).Clicked() {
					a.navigate("/settings/dubbing")
				}
			}
		}, func() { a.dubErr, a.dubErrCode, a.dubNeed = "", "", nil })
	}
	busy := a.dubBusy()
	if p.Settings.Review && !busy {
		switch {
		case len(p.Segments) > 0 && len(p.Tracks) == 0 && !dubAnyTranslated(p) && !a.dubDismissed["transcript"]:
			notice(c, "b-transcript", noticeInfo, i18n.T("Transcript ready — fix recognition errors now: every translation starts from it."), "", func() {
				if ui.Button(c.Key("b-go-translate"), i18n.T("Translate →")).Clicked() {
					a.dubStep = dubStepTranslate
					a.dubTranslate(false)
				}
			}, func() { a.dubDismissed["transcript"] = true })
		case p.AllTranslated() && !p.TracksReady() && !a.dubDismissed["translated"]:
			notice(c, "b-translated", noticeInfo, i18n.T("Translations ready — skim them before generating."), "", func() {
				if ui.Button(c.Key("b-go-generate"), i18n.T("Generate dub →")).Clicked() {
					a.dubStep = dubStepGenerate
					a.dubGenerate()
				}
			}, func() { a.dubDismissed["translated"] = true })
		}
	}
	if p.TracksReady() && !busy && !a.dubDismissed["done"] {
		n := a.dubStaleCount(a.dubLang)
		text := i18n.T("Dub complete — review the timing; changed lines regenerate alone.")
		kind := noticeOK
		if n > 0 {
			text = i18n.N(n, "%d line changed since it was generated.", "%d lines changed since they were generated.", n)
			kind = noticeInfo
		}
		notice(c, "b-done", kind, text, "", nil, func() { a.dubDismissed["done"] = true })
	}
	// Stage warnings: the first, with all of them in its ⓘ; × dismisses
	// them all.
	var codes []string
	for _, k := range sortedKeys(p.Stages) {
		for _, w := range p.Stages[k].Warnings {
			if !slices.Contains(codes, w) && !a.dubDismissed["w:"+w] {
				codes = append(codes, w)
			}
		}
	}
	if len(codes) > 0 {
		text := dubWarningText(codes[0])
		more := ""
		if len(codes) > 1 {
			text = i18n.N(len(codes)-1, "%[2]s (+%[1]d more note)", "%[2]s (+%[1]d more notes)", len(codes)-1, text)
			var all []string
			for _, w := range codes {
				all = append(all, "• "+dubWarningText(w))
			}
			more = strings.Join(all, "\n")
		}
		notice(c, "b-warn", noticeWarn, text, more, nil, func() {
			for _, w := range codes {
				a.dubDismissed["w:"+w] = true
			}
		})
	}
}

// --- the workspace ---------------------------------------------------------------------------

// dubWorkspace: the player and video, the timeline, the editor.
func (a *app) dubWorkspace(c *ui.Context) {
	t := c.Theme()
	p := a.dub
	if len(p.Segments) == 0 {
		ui.Column(c.Key("dub-wait")).Grow(1).Center().Gap(px(t, sp3)).Padding(px(t, sp6)).Children(func() {
			k := tok(t)
			if a.dubBusy() {
				ui.Spinner(c)
				caption(c, a.dubStatus()).TextAlign(ui.Center)
				return
			}
			ui.Icon(c, icons.Subtitles.Linear).Size(px(t, 36), px(t, 36)).TextColor(k.Text3)
			if p.Source.Path == "" && p.Source.URL == "" {
				caption(c, i18n.T("Choose the video or audio to dub in the Source step.")).TextAlign(ui.Center)
				return
			}
			caption(c, i18n.T("The transcript shows here once the source is transcribed.")).TextAlign(ui.Center)
		})
		return
	}
	a.dubPlayerBar(c)
	if p.Source.Duration > 0 {
		a.dubTimeline(c)
	}
	a.dubToolbar(c)
	a.dubLines(c)
}

// dubPlayerBar: play, A/B, time, the video.
func (a *app) dubPlayerBar(c *ui.Context) {
	t := c.Theme()
	k := tok(t)
	p := a.dub
	pl := &a.dubPlayer
	if p.Source.HasVideo && a.dubVideo.show {
		if a.dubVideo.wv != nil && a.dubVideo.project == p.ID {
			ui.WebView(c, a.dubVideo.wv).Height(px(t, 220)).Radius(px(t, radiusControl))
		} else {
			ui.Column(c.Key("dub-video-off")).Height(px(t, 120)).Center().Radius(px(t, radiusControl)).Background(k.Subtle).Children(func() {
				caption(c, i18n.T("The video preview shows in the app window."))
			})
		}
	}
	ui.Row(c.Key("dub-player")).Gap(px(t, sp2)).AlignItems(ui.Center).Shrink(0).Children(func() {
		if playButton(c.Key("dub-play"), a.dubPlaying(), false, 32).Label(i18n.T("Play or pause")).Tooltip(i18n.T("Play or pause (Space)")).
			Disabled(pl.loading || p.Media.Audio == "").Clicked() {
			a.dubTogglePlay()
		}
		tracks := []string{dubOrig}
		names := []string{i18n.T("Original")}
		for _, l := range p.Targets {
			if len(p.Track(l).Lines) > 0 {
				tracks = append(tracks, l)
				names = append(names, i18n.T("Dub · %s", dubLangName(l)))
			}
		}
		if len(tracks) > 1 {
			i := max(slices.Index(tracks, pl.track), 0)
			if a.dubNarrow {
				cur := names[i]
				if ui.Select(c.Key("dub-ab-select"), &cur, names).Label(i18n.T("Track")).Shrink(1).MinWidth(0).Changed() {
					a.dubSwitchTrack(tracks[slices.Index(names, cur)])
				}
			} else {
				sel := i
				if ui.Segmented(c.Key("dub-ab"), &sel, names...).Label(i18n.T("Track")).Shrink(1).MinWidth(0).Changed() || sel != i {
					a.dubSwitchTrack(tracks[sel])
				}
			}
		}
		ui.Text(c, tlClock(a.dubPosition())+" / "+tlClock(p.Source.Duration)).Font(monoFont).FontSize(px(t, fsMicro)).TextColor(k.Text2).SingleLine().Shrink(0)
		ui.Spacer(c)
		if p.Source.HasVideo {
			show := a.dubVideo.show
			if ui.Toggle(c.Key("dub-video"), &show, i18n.T("Video")).Changed() {
				a.dubVideo.show = show
				if show {
					a.dubEnsureVideo()
				}
			}
		}
	})
}

// dubTimeline is the timeline lane of the open project.
func (a *app) dubTimeline(c *ui.Context) {
	p := a.dub
	m := tlModel{Duration: p.Source.Duration, Peaks: a.dubPeaks, Onsets: p.Media.Onsets, Playhead: a.dubPosition()}
	over := map[string]bool{}
	for _, id := range p.Overlaps() {
		over[id] = true
		if i := p.Index(id); i+1 < len(p.Segments) {
			over[p.Segments[i+1].ID] = true
		}
	}
	for i, s := range p.Segments {
		flag := false
		for _, b := range a.dubBadges(s) {
			if b[1] == "warning" || b[1] == "danger" {
				flag = true
			}
		}
		m.Blocks = append(m.Blocks, tlBlock{ID: s.ID, Start: s.Start, End: s.End, Label: strconv.Itoa(i + 1), Overlap: over[s.ID], Muted: s.Volume == 0, Flag: flag})
	}
	if a.dubCur != "" {
		a.dubTL.sel = a.dubCur
	}
	locked := a.dubLocked()
	timelineLane(c, "dub-tl", &a.dubTL, m, tlEvents{
		Seek:   a.dubSeek,
		Select: func(id string) { a.dubCur = id; a.dubScrollTo(id) },
		Change: func(id string, start, end float64, clamp bool) {
			if !locked {
				a.dubSetTiming(id, start, end, clamp)
			}
		},
		Nudge: func(id, edge string, delta float64) {
			if !locked {
				a.dubNudge(id, edge, delta)
			}
		},
		Delete: func(id string) {
			if !locked {
				a.dubDelete(id)
			}
		},
		Play: a.dubPlayRange,
	})
	if len(over) > 0 {
		t := c.Theme()
		ui.Row(c.Key("dub-overlap")).Gap(px(t, 6)).AlignItems(ui.Center).Children(func() {
			iconMark(c, icons.DangerCircle, t.Danger, "")
			caption(c, i18n.T("Some lines overlap the next: both play at once.")).TextColor(t.Danger)
		})
	}
}

// dubScrollTo shows a line in the list.
func (a *app) dubScrollTo(id string) {
	if i := a.dub.Index(id); i >= 0 {
		a.dubList2.ScrollIntoView(i)
	}
}

// --- the footer ------------------------------------------------------------------------------

// dubStatus says what runs.
func (a *app) dubStatus() string {
	j := a.dubRunning()
	if j.ID == "" {
		return ""
	}
	st := dubStageName(dubStageOfKind(j.Kind))
	var p dubJobParams
	dubJSON(j.Params, &p)
	if p.Lang != "" {
		st = dubLangName(p.Lang) + " · " + st
	}
	if j.Progress.Message != "" && strings.Contains(j.Progress.Message, "/") {
		st += " " + j.Progress.Message
	}
	if j.State != jobs.StateRunning {
		st = i18n.T("Waiting: %s", st)
	}
	return st
}

type dubJobParams struct {
	Lang string `json:"lang"`
}

// dubStartLabel names the first run's button.
func (a *app) dubStartLabel() string {
	if a.dub.Settings.Review {
		return i18n.T("Transcribe")
	}
	return i18n.T("Dub it")
}

// dubFooter: the status and progress, Cancel, and the step's action —
// the page's one primary button (Export on the Export step, else the
// pipeline's next step).
func (a *app) dubFooter(c *ui.Context) {
	t := c.Theme()
	p := a.dub
	ui.Row(c.Key("dub-footer")).Height(px(t, 52)).Shrink(0).Gap(px(t, sp2)).AlignItems(ui.Center).Padding(0, px(t, sp6)).Children(func() {
		if a.dubBusy() {
			j := a.dubRunning()
			ui.Spinner(c)
			ui.Text(c, a.dubStatus()).FontSize(px(t, fsCaption)).SingleLine().Ellipsis("…").MinWidth(0).Shrink(1)
			if j.Progress.Fraction > 0 {
				ui.Progress(c, j.Progress.Fraction).Width(px(t, 160)).Shrink(1)
			}
			ui.Spacer(c)
			if ui.Button(c.Key("dub-cancel"), i18n.T("Cancel")).Clicked() {
				a.dubCancel()
			}
			return
		}
		phase := dubPhaseText(p.Phase())
		if a.dubStep == dubStepExport {
			ex := &a.dubExport
			blocked, ready := a.dubExportBlocked(ex.format), a.dubExportReady()
			caption(c, phase).SingleLine().Ellipsis("…").MinWidth(0).Shrink(1)
			ui.Spacer(c)
			if ex.job != "" {
				ui.Spinner(c)
			}
			b := primaryIconButton(c.Key("dx-go"), icons.Export, i18n.T("Export…")).Label(i18n.T("Export…")).Disabled(blocked != "" || ready != "" || ex.job != "")
			if why := firstOf(blocked, ready); why != "" {
				b.Tooltip(why)
			}
			if b.Clicked() {
				a.dubDoExport()
			}
			return
		}
		caption(c, phase).SingleLine().Ellipsis("…").MinWidth(0).Shrink(1)
		ui.Spacer(c)
		switch {
		case len(p.Segments) == 0:
			if primaryButton(c.Key("dub-start"), a.dubStartLabel()).Disabled(p.Source.Path == "" && p.Source.URL == "").Clicked() {
				a.dubStart()
			}
		case !p.AllTranslated():
			if primaryButton(c.Key("dub-translate"), dubTranslateLabel(p)).Clicked() {
				a.dubTranslate(false)
			}
		default:
			n := a.dubStaleCount(a.dubLang)
			label := i18n.N(len(p.Targets), "Generate dub", "Generate %d dubs", len(p.Targets))
			if p.TracksReady() {
				if n == 0 {
					ui.Row(c).Gap(px(t, 6)).AlignItems(ui.Center).Children(func() {
						iconMark(c, icons.CheckCircle, t.Success, "")
						caption(c, i18n.T("Every line is up to date.")).SingleLine()
					})
					return
				}
				label = i18n.N(n, "Regenerate %d changed", "Regenerate %d changed", n)
			}
			if primaryButton(c.Key("dub-generate"), label).Clicked() {
				a.dubGenerate()
			}
		}
	})
}

func dubTranslateLabel(p *dub.Project) string {
	if len(p.Targets) > 1 {
		return i18n.N(len(p.Targets), "Translate", "Translate into %d languages", len(p.Targets))
	}
	return i18n.T("Translate")
}

// --- step panels -------------------------------------------------------------------------------

// dubPanelTitle is a panel's title.
func dubPanelTitle(c *ui.Context, title string) {
	sectionHeading(c, title)
}

// dubSourcePanel: the source, its language and speakers.
func (a *app) dubSourcePanel(c *ui.Context) {
	t := c.Theme()
	p := a.dub
	locked := a.dubBusy()
	dubPanelTitle(c, i18n.T("Source"))
	if p.Source.Path == "" && p.Source.URL == "" {
		zone := ui.Column(c.Key("src-drop")).Padding(px(t, sp4)).Gap(px(t, sp2)).AlignItems(ui.Center).Radius(px(t, radiusCard)).Border(1, tok(t).Line)
		if files := zone.DroppedFiles(); len(files) > 0 {
			a.dubDropped(files)
		}
		zone.Children(func() {
			caption(c, i18n.T("Drop a video or audio file here")).TextAlign(ui.Center)
			if ui.Button(c.Key("src-choose"), i18n.T("Choose file…")).Clicked() {
				a.chooseDubSource()
			}
		})
	} else {
		fillCard(c.Key("src-info")).Gap(px(t, 4)).Padding(px(t, sp3)).Children(func() {
			name := p.Source.Name
			if name == "" {
				name = p.Source.URL
			}
			ui.Row(c).Gap(px(t, 6)).AlignItems(ui.Center).Children(func() {
				ic := icons.Soundwave
				if p.Source.HasVideo {
					ic = dubIcon
				}
				iconMark(c, ic, tok(t).Text2, "")
				ui.Text(c, name).FontWeight(600).MaxLines(2).Ellipsis("…").Grow(1).MinWidth(0).Tooltip(name)
				if iconButton(c.Key("src-remove"), icons.TrashBinMinimalistic, i18n.T("Remove source")).Disabled(locked).Clicked() {
					if len(p.Segments) > 0 {
						a.dubConfirm, a.dubConfirmOpen = "remove", true
					} else {
						a.dubRemoveSource()
					}
				}
			})
			var meta []string
			if p.Source.Kind == dub.SourceURL {
				meta = append(meta, p.Source.URL)
			}
			if p.Source.Duration > 0 {
				meta = append(meta, tlClock(p.Source.Duration))
			}
			if v := p.Source.Video; v != nil {
				meta = append(meta, fmt.Sprintf("%d×%d %s", v.Width, v.Height, strings.ToUpper(v.Codec)))
			} else if p.Source.Path != "" && !p.Source.HasVideo && p.Media.Audio != "" {
				meta = append(meta, i18n.T("Audio only"))
			}
			if p.Source.Size > 0 {
				meta = append(meta, fmt.Sprintf("%.1f MB", float64(p.Source.Size)/1e6))
			}
			caption(c, strings.Join(meta, " · ")).MaxLines(2).Ellipsis("…")
			if p.Source.Kind == dub.SourceURL && p.Source.WantCaptions {
				caption(c, i18n.T("The video's captions are used when it has them.")).TextColor(tok(t).Text3)
			}
		})
	}
	// The spoken language.
	ui.Field(c, i18n.T("Spoken language"), func() {
		names := []string{i18n.T("Auto-detect")}
		ids := []string{""}
		for _, l := range dubTargetLangs {
			names = append(names, dubLangName(l))
			ids = append(ids, l)
		}
		if p.SourceLang != "" && !slices.Contains(ids, p.SourceLang) {
			names, ids = append(names, dubLangName(p.SourceLang)), append(ids, p.SourceLang)
		}
		cur := names[max(slices.Index(ids, p.SourceLang), 0)]
		if ui.Select(c.Key("src-lang"), &cur, names).Label(i18n.T("Spoken language")).Disabled(locked).Changed() {
			l := ids[slices.Index(names, cur)]
			a.dubSet(func(p *dub.Project) { p.SourceLang = l })
		}
	})
	if p.Detected != "" && p.SourceLang == "" {
		caption(c, i18n.T("Detected: %s", dubLangName(p.Detected)))
	}
	fieldInfo(c, i18n.T("Speakers"), i18n.T("How many people speak? Leave it on Auto to detect them; the speaker model tells up to 4 apart."), func() {
		names := []string{i18n.T("Auto-detect")}
		for i := 1; i <= dub.MaxSpeakers; i++ {
			names = append(names, strconv.Itoa(i))
		}
		cur := names[min(max(p.Speakers, 0), dub.MaxSpeakers)]
		if ui.Select(c.Key("src-speakers"), &cur, names).Label(i18n.T("Speakers")).Disabled(locked).Changed() {
			n := slices.Index(names, cur)
			a.dubSet(func(p *dub.Project) { p.Speakers = n })
		}
	})
	review := p.Settings.Review
	if dubSwitch(c, "src-review", i18n.T("Review after each step"), i18n.T("Stops after the transcript and after the translation, so they can be checked first."), &review, false) {
		a.dubSettings(func(s *dub.Settings) { s.Review = review })
	}
	if ghostIconButton(c.Key("src-import"), icons.Import, i18n.T("Import subtitles (SRT/VTT)…")).Label(i18n.T("Import subtitles (SRT/VTT)…")).Disabled(locked || p.Source.Path == "" && p.Source.URL == "").
		Tooltip(i18n.T("Use a subtitle file as the transcript instead of recognising speech")).Clicked() {
		a.dubImportSubtitles()
	}
}

// dubTranscribePanel: the preparation stages, speakers and their names.
func (a *app) dubTranscribePanel(c *ui.Context) {
	t := c.Theme()
	p := a.dub
	dubPanelTitle(c, i18n.T("Transcribe"))
	for _, st := range []dub.Stage{dub.StageIngest, dub.StageSeparate, dub.StageTranscribe, dub.StageReferences} {
		s := p.Stage(st, "")
		ui.Row(c.Key("stage-" + string(st))).Gap(px(t, sp2)).AlignItems(ui.Center).Children(func() {
			ui.Box(c).Size(px(t, 14), px(t, 14)).Center().Shrink(0).Children(func() {
				switch s.Status {
				case dub.StatusDone:
					iconMark(c, icons.CheckCircle, t.Success, "")
				case dub.StatusSkipped:
					iconMark(c, icons.DangerTriangle, t.Warning, "")
				case dub.StatusFailed:
					iconMark(c, icons.DangerCircle, t.Danger, "")
				case dub.StatusRunning:
					ui.Spinner(c).Size(px(t, 12), px(t, 12))
				default:
					statusDot(c, tok(t).G[5])
				}
			})
			ui.Text(c, dubStageTitle(st)).FontSize(px(t, fsCaption)).Grow(1).MinWidth(0)
		})
		if s.Status == dub.StatusFailed && s.Message != "" {
			caption(c, s.Message).TextColor(t.Danger).Padding(0, 0, 0, px(t, 22))
		}
	}
	if len(p.Segments) > 0 {
		sectionTitle(c, i18n.T("Speakers"))
		for _, sp := range p.SpeakerLabels() {
			name := p.SpeakerName(sp)
			n := 0
			for _, s := range p.Segments {
				if s.Speaker == sp {
					n++
				}
			}
			ui.Row(c.Key("spk-" + sp)).Gap(px(t, 6)).AlignItems(ui.Center).Children(func() {
				ui.Text(c, sp).FontSize(px(t, fsMicro)).TextColor(tok(t).Text3).Width(px(t, 22)).Shrink(0)
				in := ui.TextInput(c.Key("name"), &name).Label(i18n.T("Name of %s", sp)).Grow(1).MinWidth(0)
				if in.Changed() {
					a.dubRenameSpeaker(sp, name)
				}
				tagChip(c, i18n.N(n, "%d line", "%d lines", n))
			})
		}
		ui.Column(c).Gap(px(t, 2)).Children(func() {
			if ghostIconButton(c.Key("tr-again"), icons.Restart, i18n.T("Transcribe again")).Label(i18n.T("Transcribe again")).Disabled(a.dubBusy()).
				Tooltip(i18n.T("Recognise the speech again: edits to the transcript are lost")).Clicked() {
				a.dubConfirm, a.dubConfirmOpen = "transcribe", true
			}
			if ghostIconButton(c.Key("tr-mirror"), icons.Pulse2, i18n.T("Mirror source delivery")).Label(i18n.T("Mirror source delivery")).Disabled(a.dubLocked()).
				Tooltip(i18n.T("Give lines that stand out a direction (whispered, quick…) from how they are said")).Clicked() {
				a.dubMirror()
			}
			if ghostIconButton(c.Key("tr-cleanup"), icons.MagicStick3, i18n.T("Clean up fragments")).Label(i18n.T("Clean up fragments")).Disabled(a.dubLocked()).Clicked() {
				a.dubCleanup()
			}
		})
	}
}

// dubStageTitle names a stage in the Transcribe panel.
func dubStageTitle(s dub.Stage) string {
	switch s {
	case dub.StageIngest:
		return i18n.T("Read the source")
	case dub.StageSeparate:
		return i18n.T("Separate voices and background")
	case dub.StageTranscribe:
		return i18n.T("Recognise speech and speakers")
	case dub.StageReferences:
		return i18n.T("Pick each speaker's voice sample")
	}
	return string(s)
}

// dubTranslatePanel: target languages, the translator and its options,
// the glossary and pasting.
func (a *app) dubTranslatePanel(c *ui.Context) {
	t := c.Theme()
	p := a.dub
	locked := a.dubBusy()
	dubPanelTitle(c, i18n.T("Translate"))
	// The primary target.
	pick := func(key, label, cur string, set func(string)) {
		names := make([]string, 0, len(dubTargetLangs)+1)
		ids := slices.Clone(dubTargetLangs)
		if cur != "" && !slices.Contains(ids, cur) {
			ids = append(ids, cur)
		}
		for _, l := range ids {
			names = append(names, dubLangName(l))
		}
		v := names[max(slices.Index(ids, cur), 0)]
		ui.Field(c, label, func() {
			if ui.Select(c.Key(key), &v, names).Label(label).Disabled(locked).Changed() {
				set(ids[slices.Index(names, v)])
			}
		})
	}
	pick("tl-primary", i18n.T("Dub into"), p.Primary(), func(l string) {
		a.dubSet(func(p *dub.Project) {
			if len(p.Targets) == 0 {
				p.Targets = []string{l}
			} else {
				p.Targets[0] = l
				p.Targets = slices.Compact(p.Targets)
			}
		})
		a.dubLang = l
	})
	// Dialects.
	base, _, _ := strings.Cut(p.Primary(), "-")
	if ds, ok := dubDialects[base]; ok {
		opts := append([]string{base}, ds...)
		labels := make([]string, len(opts))
		for i, d := range opts {
			labels[i] = i18n.T("Default")
			if d != base {
				labels[i] = d
			}
		}
		cur := labels[max(slices.Index(opts, p.Primary()), 0)]
		ui.Field(c, i18n.T("Dialect"), func() {
			if ui.Select(c.Key("tl-dialect"), &cur, labels).Label(i18n.T("Dialect")).Disabled(locked).Changed() {
				dd := opts[slices.Index(labels, cur)]
				a.dubSet(func(p *dub.Project) { p.Targets[0] = dd })
				a.dubLang = dd
			}
		})
	}
	// More languages.
	ui.Text(c, i18n.T("More languages"))
	ui.Row(c.Key("tl-more")).Gap(px(t, 4)).Wrap().AlignItems(ui.Center).Children(func() {
		for _, l := range p.Targets[min(1, len(p.Targets)):] {
			ll := l
			ui.Row(c.Key("chip-"+l)).Gap(px(t, 2)).AlignItems(ui.Center).Padding(px(t, 2), px(t, 2), px(t, 2), px(t, sp2)).Radius(px(t, radiusControl)).Background(tok(t).Hover).Children(func() {
				ui.Text(c, dubLangName(ll)).FontSize(px(t, fsCaption)).SingleLine()
				if iconButton(c.Key("x"), icons.Close, i18n.T("Remove %s", dubLangName(ll))).Size(px(t, 22), px(t, 22)).Disabled(locked).Clicked() {
					a.dubSet(func(p *dub.Project) { p.Targets = slices.DeleteFunc(p.Targets, func(x string) bool { return x == ll }) })
				}
			})
		}
		dropButton(c.Key("tl-add"), icons.AddCircle, i18n.T("Add")).Shrink(0).Menu(func(m *ui.Menu) {
			for _, l := range dubTargetLangs {
				if slices.Contains(p.Targets, l) {
					continue
				}
				if m.Item(dubLangName(l)).Disabled(locked).Chosen() {
					ll := l
					a.dubSet(func(p *dub.Project) { p.Targets = append(p.Targets, ll) })
				}
			}
		})
	})
	// The translator.
	engines := []string{"llm", "deepl", "google", "microsoft"}
	names := []string{i18n.T("Language model"), "DeepL", "Google Cloud Translation", "Microsoft Translator"}
	cur := names[max(slices.Index(engines, p.Settings.Translator), 0)]
	ui.Field(c, i18n.T("Translator"), func() {
		if ui.Select(c.Key("tl-engine"), &cur, names).Label(i18n.T("Translator")).Disabled(locked).Changed() {
			e := engines[slices.Index(names, cur)]
			a.dubSettings(func(s *dub.Settings) { s.Translator = e })
		}
	})
	ok, name := a.dubTranslatorReady(p.Settings)
	switch {
	case ok && name != "":
		caption(c, i18n.T("Uses %s", name))
	case !ok:
		fillCard(c.Key("tl-needs")).Gap(px(t, sp2)).Padding(px(t, sp3)).Children(func() {
			ui.Row(c).Gap(px(t, 6)).Children(func() {
				iconMark(c, icons.DangerTriangle, t.Warning, "")
				caption(c, i18n.T("Translating needs a translation engine: a local model (Ollama, LM Studio) or a provider in Settings → Writing help.")).TextColor(tok(t).Text).Grow(1).MinWidth(0)
			})
			if ui.Button(c.Key("tl-setup"), i18n.T("Set up writing help")).Clicked() {
				a.navigate("/settings/writing")
			}
		})
	}
	if p.Settings.Translator != "llm" && p.Settings.Translator != "" {
		caption(c, i18n.T("The service's API key is kept in the Keychain as “mt.%s”.", p.Settings.Translator)).TextColor(tok(t).Text3)
	}
	if p.Settings.Translator == "llm" || p.Settings.Translator == "" {
		quals := []translate.Quality{translate.Fast, translate.Autofit, translate.Cinematic}
		qi := max(slices.Index(quals, p.Settings.Quality), 0)
		fieldInfo(c, i18n.T("Quality"), i18n.T("Fast is one literal pass; Autofit rewrites lines that would not fit their time; Cinematic drafts, critiques and adapts for natural phrasing."), func() {
			if segmentedFit(c.Key("tl-quality"), &qi, i18n.T("Fast"), i18n.T("Autofit"), i18n.T("Cinematic")).Label(i18n.T("Quality")).Disabled(locked).Changed() {
				q := quals[qi]
				a.dubSettings(func(s *dub.Settings) { s.Quality = q })
			}
		})
		auto := p.Settings.AutoGlossary
		if dubSwitch(c, "tl-autogloss", i18n.T("Auto glossary"), "", &auto, locked) {
			a.dubSettings(func(s *dub.Settings) { s.AutoGlossary = auto })
		}
	}
	shorter := p.Settings.SuggestShorter
	if dubSwitch(c, "tl-shorter", i18n.T("Suggest shorter lines"), "", &shorter, locked) {
		a.dubSettings(func(s *dub.Settings) { s.SuggestShorter = shorter })
	}
	forms := []translate.Formality{translate.FormalityAuto, translate.FormalityFormal, translate.FormalityInformal}
	fnames := []string{i18n.T("Automatic"), i18n.T("Formal"), i18n.T("Informal")}
	fcur := fnames[max(slices.Index(forms, p.Settings.Formality), 0)]
	ui.Field(c, i18n.T("Register"), func() {
		if ui.Select(c.Key("tl-formality"), &fcur, fnames).Label(i18n.T("Register")).Disabled(locked).Changed() {
			f := forms[slices.Index(fnames, fcur)]
			a.dubSettings(func(s *dub.Settings) { s.Formality = f })
		}
	})
	style := p.Settings.Style
	ui.Field(c, i18n.T("Style notes"), func() {
		if ui.TextArea(c.Key("tl-style"), &style).Lines(2, 6).Placeholder(i18n.T("Tone, audience, how freely to adapt…")).Label(i18n.T("Style notes")).Disabled(locked).Changed() {
			v := []rune(style)
			v = v[:min(len(v), translate.MaxStyle)]
			a.dubSettings(func(s *dub.Settings) { s.Style = string(v) })
		}
	})
	ui.Column(c).Gap(px(t, 2)).Children(func() {
		if ghostIconButton(c.Key("tl-gloss"), icons.TextSquare, i18n.T("Glossary (%d)", len(p.Glossary))).Label(i18n.T("Glossary (%d)", len(p.Glossary))).Clicked() {
			a.dubGloss.open = !a.dubGloss.open
		}
		if ghostIconButton(c.Key("tl-paste"), icons.Copy, i18n.T("Paste translation")).Label(i18n.T("Paste translation")).Disabled(len(p.Segments) == 0).Clicked() {
			a.dubPaste.open = !a.dubPaste.open
			if a.dubLang == "" {
				a.dubLang = p.Primary()
			}
		}
		if ghostIconButton(c.Key("tl-again"), icons.Restart, i18n.T("Translate everything again")).Label(i18n.T("Translate everything again")).Disabled(locked || len(p.Segments) == 0).
			Tooltip(i18n.T("Lines you wrote yourself are kept")).Clicked() {
			a.dubTranslate(true)
		}
	})
}

// dubCastPanel: the casting board.
func (a *app) dubCastPanel(c *ui.Context) {
	t := c.Theme()
	p := a.dub
	locked := a.dubLocked()
	ui.Row(c).Gap(px(t, 6)).AlignItems(ui.Center).Children(func() {
		dubPanelTitle(c, i18n.N(len(p.SpeakerLabels()), "Cast · %d speaker", "Cast · %d speakers", len(p.SpeakerLabels())))
		infoTip(c.Key("cast-tip"), i18n.T("Drag a voice onto a speaker, or pick one from the list. Auto clones each speaker's own voice."))
	})
	choices := a.dubVoiceChoices()
	// The chips.
	ui.Row(c.Key("cast-chips")).Gap(px(t, 4)).Wrap().Children(func() {
		for _, v := range choices {
			ui.Text(c.Key("chip-"+v.ref), v.label).FontSize(px(t, fsCaption)).TextColor(tok(t).Text2).SingleLine().Padding(px(t, 3), px(t, sp2)).
				Radius(px(t, radiusControl)).Background(tok(t).Hover).Cursor(ui.CursorGrab).Drag(dubVoiceChip{ref: v.ref})
		}
	})
	if len(p.SpeakerLabels()) == 0 {
		caption(c, i18n.T("Speakers appear once the source is transcribed."))
	}
	refs := make([]string, len(choices))
	labels := make([]string, len(choices))
	for i, v := range choices {
		refs[i], labels[i] = v.ref, v.label
		if v.group != "" {
			labels[i] = v.group + " · " + v.label
		}
	}
	for _, sp := range p.SpeakerLabels() {
		card := fillCard(c.Key("cast-" + sp)).Gap(px(t, 6)).Padding(px(t, sp3))
		if _, over := ui.DragOver[dubVoiceChip](card); over {
			card.Border(2, t.Accent)
		}
		if chip, ok := ui.Drop[dubVoiceChip](card); ok && !locked {
			a.dubCast(sp, chip.ref)
		}
		card.Children(func() {
			ui.Row(c).Gap(px(t, 6)).AlignItems(ui.Center).Children(func() {
				ui.Text(c, p.SpeakerName(sp)).FontWeight(600).Grow(1).MinWidth(0).SingleLine().Ellipsis("…")
				if r, ok := p.References[sp]; ok {
					info := i18n.T("Own voice: %s–%s", tlClock(r.Start), tlClock(r.End))
					if r.Enhanced {
						info += " · " + i18n.T("cleaned")
					}
					ui.Text(c, info).FontSize(px(t, fsMicro)).TextColor(tok(t).Text2).SingleLine().Ellipsis("…").MinWidth(0).Shrink(1).Tooltip(info)
					path := a.dubSvc.Path(p.ID, r.File)
					id := "dubref:" + p.ID + ":" + sp
					if playToggle(c.Key("ref-play"), a.pl.Playing(id), i18n.T("Hear the voice sample"), i18n.T("Hear the voice sample")).Clicked() {
						a.dubStopPlayer()
						if err := a.pl.Toggle(id, path); err != nil {
							a.toast(i18n.T("Cannot play: %v", err))
						}
					}
				}
			})
			if _, ok := p.References[sp]; !ok && p.Stage(dub.StageReferences, "").Status == dub.StatusDone {
				ui.Row(c).Gap(px(t, 6)).Children(func() {
					iconMark(c, icons.DangerTriangle, t.Warning, "")
					caption(c, i18n.T("Says too little to clone: uses the default voice unless cast.")).TextColor(t.Warning).Grow(1).MinWidth(0)
				})
			}
			cur := p.Cast[sp]
			v := labels[max(slices.Index(refs, cur), 0)]
			if ui.Select(c.Key("voice"), &v, labels).Label(i18n.T("Voice of %s", p.SpeakerName(sp))).Disabled(locked).Changed() {
				a.dubCast(sp, refs[slices.Index(labels, v)])
			}
		})
	}
	match := 0
	if p.Settings.VoiceMatch == dub.MatchLine {
		match = 1
	}
	fieldInfo(c, i18n.T("Voice match"), i18n.T("Consistent clones each speaker from one sample; Per line clones each line from its own audio (follows the delivery, less stable)."), func() {
		if segmentedFit(c.Key("cast-match"), &match, i18n.T("Consistent"), i18n.T("Per line")).Label(i18n.T("Voice match")).Changed() {
			m := dub.MatchConsistent
			if match == 1 {
				m = dub.MatchLine
			}
			a.dubSettings(func(s *dub.Settings) { s.VoiceMatch = m })
		}
	})
}

// dubVoiceChip is a voice dragged onto a speaker.
type dubVoiceChip struct{ ref string }

// dubGeneratePanel: the model, timing, background and voice controls.
func (a *app) dubGeneratePanel(c *ui.Context) {
	t := c.Theme()
	p := a.dub
	locked := a.dubBusy()
	dubPanelTitle(c, i18n.T("Generate"))
	models := dubTTSModels()
	names := make([]string, len(models))
	cur := ""
	for i, e := range models {
		names[i] = e.Label
		if !e.Has("clone") {
			names[i] += " · " + i18n.T("no cloning")
		}
		if ready, known := a.modelOnDisk(e); known && !ready {
			names[i] += " · " + i18n.T("not downloaded")
		}
		if e.Repo == p.Settings.Model {
			cur = names[i]
		}
	}
	if cur == "" && len(names) > 0 {
		cur = names[0]
	}
	ui.Field(c, i18n.T("Voice model"), func() {
		if ui.Select(c.Key("gen-model"), &cur, names).Label(i18n.T("Voice model")).Disabled(locked).Changed() {
			repo := models[slices.Index(names, cur)].Repo
			a.dubSettings(func(s *dub.Settings) { s.Model = repo })
		}
	})
	caps := dubCaps(p.Settings.Model)
	if caps.Duration {
		caption(c, i18n.T("This model speaks each line in exactly its time: the best lip sync.")).TextColor(tok(t).Text3)
	}
	// Timing.
	timings := dub.Timings
	tnames := []string{i18n.T("Concise"), i18n.T("Smart fit"), i18n.T("Stretch video"), i18n.T("Lip sync")}
	tcur := tnames[max(slices.Index(timings, p.Settings.Timing), 0)]
	fieldInfo(c, i18n.T("Timing"), dubTimingHelp(p.Settings.Timing), func() {
		if ui.Select(c.Key("gen-timing"), &tcur, tnames).Label(i18n.T("Timing")).Disabled(locked).Changed() {
			tm := timings[slices.Index(tnames, tcur)]
			a.dubSettings(func(s *dub.Settings) { s.Timing = tm })
		}
	})
	keep := p.Settings.KeepBackground
	if dubSwitch(c, "gen-bg", i18n.T("Keep background (music and effects)"), "", &keep, locked) {
		a.dubSettings(func(s *dub.Settings) { s.KeepBackground = keep })
	}
	summary := ""
	if p.Settings.Steps != 0 || p.Settings.Guidance != 0 || p.Settings.Speed != 0 || p.Settings.Instruct != "" {
		summary = i18n.T("Changed")
	}
	collapsible(c.Key("gen-voice"), i18n.T("Voice controls"), &a.dubVoiceCtl, func() {
		if summary != "" {
			caption(c, summary).SingleLine()
		}
	}, func() { a.dubVoiceControls(c, caps, locked) })
	sectionTitle(c, i18n.T("Tracks"))
	// Tracks.
	for _, l := range p.Targets {
		tr := p.Track(l)
		state := i18n.T("not generated")
		if tr.File != "" {
			state = tlClock(tr.Seconds)
			if n := a.dubStaleCount(l); n > 0 {
				state += " · " + i18n.N(n, "%d changed", "%d changed", n)
			}
			if tr.QC != nil && !tr.QCStale() {
				state += " · " + i18n.N(tr.QC.Flagged, "%d to re-listen", "%d to re-listen", tr.QC.Flagged)
			}
		} else if n := len(tr.Lines); n > 0 {
			state = i18n.N(n, "%d line rendered", "%d lines rendered", n)
		}
		ui.Row(c.Key("track-" + l)).Gap(px(t, 6)).AlignItems(ui.Center).Children(func() {
			ui.Text(c, dubLangName(l)).Grow(1).MinWidth(0).SingleLine().Ellipsis("…")
			ui.Text(c, state).FontSize(px(t, fsMicro)).TextColor(tok(t).Text2).SingleLine().Ellipsis("…").MinWidth(0).Shrink(1).Tooltip(state)
		})
	}
	// Verify is in the editor's toolbar.
}

// dubVoiceControls are the Generate step's voice controls: steps,
// guidance, speed and style, as the model takes them.
func (a *app) dubVoiceControls(c *ui.Context, caps services.DubModelCaps, locked bool) {
	p := a.dub
	if caps.Steps {
		steps := float64(p.Settings.Steps)
		if steps == 0 {
			steps = 32
		}
		ui.Field(c, i18n.T("Steps: %d", int(steps)), func() {
			if ui.StepSlider(c.Key("gen-steps"), &steps, 8, 64, 1).Label(i18n.T("Steps")).Disabled(locked).Changed() {
				v := int(steps)
				a.dubSettings(func(s *dub.Settings) { s.Steps = v })
			}
		})
	}
	if caps.Guidance {
		g := p.Settings.Guidance
		if g == 0 {
			g = 2
		}
		ui.Field(c, i18n.T("Guidance: %.1f", g), func() {
			if ui.StepSlider(c.Key("gen-cfg"), &g, 0, 4, 0.1).Label(i18n.T("Guidance")).Disabled(locked).Changed() {
				v := dubRound(g)
				a.dubSettings(func(s *dub.Settings) { s.Guidance = v })
			}
		})
	}
	speed := p.Settings.Speed
	if speed == 0 {
		speed = 1
	}
	ui.Field(c, i18n.T("Speed: %.2f×", speed), func() {
		if ui.StepSlider(c.Key("gen-speed"), &speed, 0.5, 2, 0.05).Label(i18n.T("Speed")).Disabled(locked || !caps.Speed).Changed() {
			v := dubRound(speed)
			if v == 1 {
				v = 0
			}
			a.dubSettings(func(s *dub.Settings) { s.Speed = v })
		}
	})
	if caps.Instruct {
		style := p.Settings.Instruct
		ui.Field(c, i18n.T("Voice style"), func() {
			if ui.TextInput(c.Key("gen-style"), &style).Placeholder(i18n.T("e.g. calm documentary narrator")).Label(i18n.T("Voice style")).Disabled(locked).Changed() {
				v := strings.TrimSpace(style)
				a.dubSettings(func(s *dub.Settings) { s.Instruct = v })
			}
		})
	}
	if ghostIconButton(c.Key("gen-reset"), icons.Restart, i18n.T("Reset voice controls")).Label(i18n.T("Reset voice controls")).Disabled(locked).Clicked() {
		a.dubSettings(func(s *dub.Settings) { s.Steps, s.Guidance, s.Speed, s.Instruct = 0, 0, 0, "" })
	}
}

// dubTimingHelp explains a timing strategy.
func dubTimingHelp(tm dub.Timing) string {
	switch tm {
	case dub.TimingConcise:
		return i18n.T("Lines are never sped up: a line too long for its time is flagged for a shorter rewrite.")
	case dub.TimingSmart:
		return i18n.T("A line may run into the pause after it; only what is left is sped up (at most 1.25×).")
	case dub.TimingStretchVideo:
		return i18n.T("As Smart fit, sped up at most 1.1×; the video slows under lines that still do not fit (needs an engine update: until then those lines overflow).")
	}
	return i18n.T("Each line is fitted to its time: sped up (at most 1.5×) or slowed slightly, pitch kept; what still does not fit is cut.")
}

// --- glossary and paste panels ------------------------------------------------------------

// dubGlossaryPanel is the glossary.
func (a *app) dubGlossaryPanel(c *ui.Context) {
	t := c.Theme()
	p := a.dub
	g := &a.dubGloss
	w, h := c.Size()
	ui.Column(c.Key("dub-gloss")).Gap(px(t, sp2)).Width(min(px(t, 620), w-px(t, 64))).MaxHeight(min(px(t, 520), h-px(t, 64))).Children(func() {
		ui.Row(c).Gap(px(t, sp1)).AlignItems(ui.Center).Children(func() {
			sectionHeading(c, i18n.N(len(p.Glossary), "Glossary · %d term", "Glossary · %d terms", len(p.Glossary))).Grow(1)
			if ghostIconButton(c.Key("g-auto"), icons.MagicStick3, i18n.T("Auto")).Label(i18n.T("Auto")).Tooltip(i18n.T("Ask the language model for names and recurring terms")).
				Disabled(g.extracting || len(p.Segments) == 0 || a.llmRouter == nil).Clicked() {
				a.dubExtractTerms()
			}
			if iconButton(c.Key("g-close"), icons.Close, i18n.T("Close")).Clicked() {
				g.open = false
			}
		})
		if len(p.Glossary) == 0 {
			caption(c, i18n.T("No terms yet. Add one below, or Auto."))
		}
		for i, term := range p.Glossary {
			ii := i
			ui.Row(c.Key("term-" + strconv.Itoa(i))).Gap(px(t, 6)).AlignItems(ui.Center).Children(func() {
				if g.editing == ii {
					ui.TextInput(c.Key("es"), &g.eSrc).Label(i18n.T("Source term")).Grow(1)
					ui.TextInput(c.Key("et"), &g.eTgt).Label(i18n.T("Translation")).Grow(1)
					ui.TextInput(c.Key("en"), &g.eNot).Label(i18n.T("Note")).Grow(1)
					if ui.Button(c.Key("save"), i18n.T("Save")).Clicked() {
						a.dubSaveTerm(ii)
					}
					if ghostButton(c.Key("cancel"), i18n.T("Cancel")).Clicked() {
						g.editing = -1
					}
					return
				}
				line := term.Source + " → " + term.Target
				if term.Note != "" {
					line += " · " + term.Note
				}
				truncText(c, line).Grow(1)
				if term.Auto {
					tagChip(c, i18n.T("auto"))
				}
				if iconButton(c.Key("edit"), icons.Pen, i18n.T("Edit")).Clicked() {
					g.editing, g.eSrc, g.eTgt, g.eNot = ii, term.Source, term.Target, term.Note
				}
				if iconButton(c.Key("del"), icons.TrashBinMinimalistic, i18n.T("Delete %s", term.Source)).Clicked() {
					a.dubDeleteTerm(ii)
				}
			})
		}
		ui.Row(c.Key("g-add")).Gap(px(t, 6)).AlignItems(ui.Center).Children(func() {
			ui.TextInput(c.Key("src"), &g.src).Placeholder(i18n.T("Source")).Label(i18n.T("Source term")).Grow(1).MinWidth(0)
			ui.TextInput(c.Key("tgt"), &g.tgt).Placeholder(i18n.T("Translation")).Label(i18n.T("Translation")).Grow(1).MinWidth(0)
			ui.TextInput(c.Key("note"), &g.note).Placeholder(i18n.T("Note (optional)")).Label(i18n.T("Note")).Grow(1).MinWidth(0)
			if primaryButton(c.Key("add"), i18n.T("Add term")).Disabled(strings.TrimSpace(g.src) == "" || strings.TrimSpace(g.tgt) == "").Clicked() {
				a.dubAddTerm()
			}
		})
		caption(c, i18n.T("Terms go into every translation; yours win over auto ones.")).TextColor(tok(t).Text3)
	})
}

// dubPastePanel pastes a translation.
func (a *app) dubPastePanel(c *ui.Context) {
	t := c.Theme()
	ps := &a.dubPaste
	w, _ := c.Size()
	ui.Column(c.Key("dub-paste")).Gap(px(t, sp2)).Width(min(px(t, 620), w-px(t, 64))).Children(func() {
		ui.Row(c).Gap(px(t, sp1)).AlignItems(ui.Center).Children(func() {
			sectionHeading(c, i18n.T("Paste translation · %s", dubLangName(a.dubLang))).Grow(1)
			if ghostIconButton(c.Key("p-file"), icons.Import, i18n.T("Load .srt / .vtt…")).Label(i18n.T("Load .srt / .vtt…")).Clicked() {
				a.dubPasteFile()
			}
			if iconButton(c.Key("p-close"), icons.Close, i18n.T("Close")).Clicked() {
				ps.open = false
			}
		})
		box := ui.TextArea(c.Key("p-text"), &ps.text).Lines(3, 10).Label(i18n.T("Translation to paste")).
			Placeholder(i18n.T("Paste subtitles (SRT/VTT), numbered lines (1. …), or one line per segment."))
		if files := box.DroppedFiles(); len(files) > 0 {
			path := files[0]
			a.background(func() {
				data, err := readFileText(path)
				a.post(func() {
					if err != nil {
						ps.err = err.Error()
						return
					}
					ps.text = data
				})
			})
		}
		a.dubPastePreview()
		if strings.TrimSpace(ps.text) == "" {
			return
		}
		pv := ps.preview
		mode := map[dub.PasteMode]string{dub.PasteTimed: i18n.T("Timestamped"), dub.PasteNumbered: i18n.T("Numbered lines"), dub.PastePlain: i18n.T("Plain lines")}[pv.Mode]
		ui.Row(c).Gap(px(t, 6)).AlignItems(ui.Center).Wrap().Children(func() {
			tagChip(c, mode)
			ui.Text(c, i18n.T("%d of %d segments matched", pv.Matched, len(pv.Rows))).FontSize(px(t, fsCaption))
			if pv.Unused > 0 {
				iconMark(c, icons.DangerTriangle, t.Warning, "")
				caption(c, i18n.N(pv.Unused, "%d pasted line went unused", "%d pasted lines went unused", pv.Unused)).TextColor(t.Warning)
			}
		})
		shown := 0
		for _, r := range pv.Rows {
			if shown >= 8 {
				break
			}
			if r.After == "" {
				continue
			}
			shown++
			caption(c.Key("pv-"+r.ID), firstOf(r.Before, "—")+"  →  "+r.After).MaxLines(2).Ellipsis("…")
		}
		if pv.Matched > shown {
			caption(c, i18n.N(pv.Matched-shown, "+ %d more", "+ %d more", pv.Matched-shown)).TextColor(tok(t).Text3)
		}
		if ps.err != "" {
			caption(c, ps.err).TextColor(t.Danger)
		}
		ui.Row(c).Justify(ui.End).Children(func() {
			if primaryButton(c.Key("p-apply"), i18n.N(pv.Matched, "Apply to %d segment", "Apply to %d segments", pv.Matched)).Disabled(pv.Matched == 0 || a.dubLocked()).Clicked() {
				a.dubApplyPaste()
			}
		})
	})
}

// --- confirmations ------------------------------------------------------------------------------

// dubConfirmDialog asks before removing work.
func (a *app) dubConfirmDialog(c *ui.Context) {
	if !a.dubConfirmOpen {
		return
	}
	what := a.dubConfirm
	var title, msg, yes string
	switch {
	case what == "remove":
		title, msg, yes = i18n.T("Remove the source?"), i18n.T("Its transcript, translations and generated tracks go with it. This cannot be undone."), i18n.T("Remove")
	case what == "transcribe":
		title, msg, yes = i18n.T("Transcribe again?"), i18n.T("The transcript is made again from the audio: your edits, translations and generated tracks are lost."), i18n.T("Transcribe again")
	case what == "story":
		title, msg, yes = i18n.T("Replace the Stories script?"), i18n.T("This replaces the current Stories script and cast. Your dub is not changed."), i18n.T("Replace")
	case strings.HasPrefix(what, "delete:"):
		title, msg, yes = i18n.T("Delete this dub?"), i18n.T("The project and every file made for it are deleted. The source file is kept."), i18n.T("Delete")
	default:
		a.dubConfirmOpen = false
		return
	}
	switch ui.AlertDialog(c.Key("dub-confirm"), &a.dubConfirmOpen, title, msg, yes, i18n.T("Cancel")) {
	case 0:
		a.dubConfirmOpen = false
		switch {
		case what == "remove":
			a.dubRemoveSource()
		case what == "transcribe":
			a.dubRun(services.RunRequest{Through: dub.StageReferences, Force: []dub.Stage{dub.StageTranscribe}})
		case what == "story":
			a.dubToStory()
		case strings.HasPrefix(what, "delete:"):
			a.deleteDub(strings.TrimPrefix(what, "delete:"))
		}
	case 1:
		a.dubConfirmOpen = false
	}
}

// --- Dub → Story ---------------------------------------------------------------------------------

// dubAskStory makes a Stories script of the dub, asking first when the
// Stories page has a script.
func (a *app) dubAskStory() {
	if a.lfStories != nil && !a.lfStories.empty() {
		a.dubConfirm, a.dubConfirmOpen = "story", true
		return
	}
	a.dubToStory()
}

// dubToStory hands the dub to the Stories page: one character per
// speaker (with their cast voice where Stories can use it), one line
// per segment, in the language shown.
func (a *app) dubToStory() {
	p := a.dub
	if p == nil || a.lfStories == nil {
		return
	}
	st := p.ToStory(a.dubLang)
	model := p.Settings.Model
	d := services.NewLongformDoc(core.ProjectStory, model)
	d.Title, d.Language = st.Title, st.Lang
	ids := map[string]string{}
	for i, ch := range st.Characters {
		id := "c" + strconv.Itoa(i+1)
		ids[ch.Name] = id
		d.Cast = append(d.Cast, services.CastMember{ID: id, Name: ch.Name, Voice: ch.Voice})
	}
	for i, l := range st.Lines {
		d.Lines = append(d.Lines, services.StoryLine{ID: "l" + strconv.Itoa(i+1), Text: l.Text, Character: ids[l.Character], Voice: l.Voice, Speed: l.Speed})
	}
	a.setLfDoc(a.lfStories, d, "", "")
	a.navigate("/stories")
	a.toast(i18n.T("The dub is a Stories script now."))
}

// sortedKeys are a map's keys in order.
func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

// dubSwitch is a switch with its label beside it (and a ⓘ with info,
// "" for none); it reports a change.
func dubSwitch(c *ui.Context, key, label, info string, on *bool, disabled bool) bool {
	changed := false
	formRow(c, key+"-row", label, info, func() {
		changed = ui.Switch(c.Key(key), on).Label(label).Disabled(disabled).Changed()
	})
	return changed
}

// dubAnyTranslated reports whether any line has a translation (or failed one).
func dubAnyTranslated(p *dub.Project) bool {
	for _, s := range p.Segments {
		if len(s.Lines) > 0 {
			return true
		}
	}
	return false
}
