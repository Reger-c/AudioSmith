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

// dubPage is /dub.
func (a *app) dubPage(c *ui.Context) {
	t := c.Theme()
	a.dubShortcuts(c)
	if a.dubPlaying() {
		c.After(100 * time.Millisecond) // the playhead moves
		a.dubVideoSync(false)
	}
	title := i18n.T("Dubbing")
	if a.dub != nil && a.dub.Title != "" {
		title = i18n.T("Dubbing") + " · " + a.dub.Title
	}
	header(c, title, func() { a.dubHeaderActions(c) })
	if a.dub == nil {
		a.dubEmpty(c)
		a.dubConfirmDialog(c)
		return
	}
	a.dubStepper(c)
	ui.Divider(c)
	ui.Row(c).Grow(1).MinHeight(0).AlignItems(ui.Stretch).Children(func() {
		ui.Scroll(c.Key("dub-side")).Width(px(t, inspectorWidth+40)).Shrink(0).Background(t.Surface).Children(func() {
			ui.Column(c).Padding(px(t, 16)).Gap(px(t, 10)).Children(func() {
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
			})
		})
		ui.Box(c).Width(1).Background(t.Border)
		ui.Column(c).Grow(1).MinWidth(0).Padding(px(t, 12), px(t, 16)).Gap(px(t, 8)).Children(func() {
			a.dubBanners(c)
			a.dubWorkspace(c)
		})
	})
	ui.Divider(c)
	a.dubFooter(c)
	if a.dubGloss.open {
		ui.Modal(c.Key("dub-gloss-modal"), &a.dubGloss.open, func() { a.dubGlossaryPanel(c) })
	}
	if a.dubPaste.open {
		ui.Modal(c.Key("dub-paste-modal"), &a.dubPaste.open, func() { a.dubPastePanel(c) })
	}
	a.dubConfirmDialog(c)
}

// dubHeaderActions: the projects, New, Create Story, Batch.
func (a *app) dubHeaderActions(c *ui.Context) {
	ui.MenuButton(c.Key("dub-projects"), i18n.T("Projects"), func(m *ui.Menu) {
		if len(a.dubList) == 0 {
			m.Item(i18n.T("No dubs yet")).Disabled(true)
		}
		for _, r := range a.dubList {
			id := r.ID
			label := firstOf(r.Title, i18n.T("Untitled dub")) + " · " + dubPhaseText(dub.Phase(r.Phase))
			if m.Item(label).Chosen() {
				a.openDub(id)
			}
		}
	})
	if ui.Button(c.Key("dub-new"), i18n.T("New dub")).Clicked() {
		a.dubStopPlayer()
		a.dub = nil
		a.updateSettings(func(s *settings.Settings) { s.Dubbing.Open = "" })
	}
	if a.dub != nil {
		if ui.Button(c.Key("dub-story"), i18n.T("Create Story")).Tooltip(i18n.T("Turn speakers into characters and lines into a Stories script")).
			Disabled(len(a.dub.Segments) == 0).Clicked() {
			a.dubAskStory()
		}
		if ui.Button(c.Key("dub-delete"), i18n.T("Delete")).Clicked() {
			a.dubConfirm, a.dubConfirmOpen = "delete:"+a.dub.ID, true
		}
	}
	if ui.Button(c.Key("dub-batch"), i18n.T("Batch")).Tooltip(i18n.T("Dub many videos into many languages")).Clicked() {
		a.navigate("/batch")
	}
}

// dubEmpty is the page without a project: drop a file or paste a link.
func (a *app) dubEmpty(c *ui.Context) {
	t := c.Theme()
	ui.Scroll(c.Key("dub-empty")).Grow(1).Children(func() {
		ui.Column(c).Padding(px(t, 24)).Gap(px(t, 16)).AlignItems(ui.Center).Children(func() {
			zone := ui.Column(c.Key("dub-drop")).Width(px(t, 560)).Padding(px(t, 28)).Gap(px(t, 10)).AlignItems(ui.Center).Radius(12).Border(1, t.Border)
			if files := zone.DroppedFiles(); len(files) > 0 {
				a.dubDropped(files)
			}
			if zone.FileDragOver() {
				zone.Border(2, t.Accent)
			}
			zone.Children(func() {
				ui.Icon(c, dubIcon.Linear).Size(px(t, 36), px(t, 36))
				ui.Text(c, i18n.T("Video and audio dubbing")).FontSize(px(t, 17)).Bold()
				ui.Text(c, "MP4 · MOV · M4V · MKV · WEBM · MP3 · WAV · M4A · FLAC").FontSize(px(t, 12)).TextColor(t.TextMuted)
				ui.Text(c, i18n.T("Drop a video or audio file here")).TextColor(t.TextMuted)
				if ui.PrimaryButton(c.Key("dub-choose"), i18n.T("Choose file…")).Clicked() {
					a.chooseDubSource()
				}
				ui.Row(c).Gap(px(t, 6)).AlignItems(ui.Center).Width(px(t, 480)).Children(func() {
					ui.TextInput(c.Key("dub-url"), &a.dubURL).Placeholder(i18n.T("…or paste a YouTube or video link")).Label(i18n.T("Video link")).Grow(1)
					if ui.Button(c.Key("dub-url-go"), i18n.T("Import")).Disabled(strings.TrimSpace(a.dubURL) == "").Clicked() {
						a.dubImportURL()
					}
				})
				ui.Collapsible(c.Key("dub-url-more"), i18n.T("Link options"), &a.dubURLMore, func() {
					ui.Column(c).Gap(px(t, 6)).Children(func() {
						ui.Checkbox(c.Key("dub-captions"), &a.dubCaptions, i18n.T("Use the video's captions when it has them (written by people)"))
						ui.Row(c).Gap(px(t, 6)).AlignItems(ui.Center).Children(func() {
							label := i18n.T("Sign-in cookies (cookies.txt)…")
							if a.dubCookies != "" {
								label = shortPath(a.dubCookies)
							}
							if ui.Button(c.Key("dub-cookies"), label).Clicked() {
								a.dubChooseCookies()
							}
							if a.dubCookies != "" && ui.Button(c.Key("dub-cookies-x"), "×").Label(i18n.T("Remove the cookies")).Clicked() {
								a.dubCookies = ""
							}
						})
						ui.Text(c, i18n.T("Cookies are used for this download only and never saved.")).FontSize(px(t, 11)).TextColor(t.TextMuted)
					})
				})
				if a.dubErr != "" {
					ui.Text(c, a.dubErr).FontSize(px(t, 12)).TextColor(t.Danger)
					if a.dubErrCode == dub.CodeNoYtDlp && ui.Button(c.Key("dub-url-settings"), i18n.T("Open Settings → Dubbing")).Clicked() {
						a.navigate("/settings/dubbing")
					}
				}
			})
			ui.Row(c).Gap(px(t, 18)).Children(func() {
				for i, s := range []string{i18n.T("Upload & transcribe"), i18n.T("Translate"), i18n.T("Generate the dub")} {
					ui.Text(c, strconv.Itoa(i+1)+" · "+s).FontSize(px(t, 12)).TextColor(t.TextMuted)
				}
			})
			if len(a.dubList) > 0 {
				ui.Column(c.Key("dub-recent")).Width(px(t, 560)).Gap(px(t, 4)).Children(func() {
					ui.Text(c, i18n.T("Recent dubs")).Bold()
					for _, r := range a.dubList[:min(len(a.dubList), 8)] {
						id := r.ID
						ui.Row(c.Key("recent-"+id)).Gap(px(t, 8)).AlignItems(ui.Center).Padding(px(t, 6), px(t, 8)).Radius(6).Border(1, t.Border).Children(func() {
							ui.Column(c).Grow(1).MinWidth(0).Children(func() {
								ui.Text(c, firstOf(r.Title, i18n.T("Untitled dub"))).SingleLine()
								meta := []string{dubPhaseText(dub.Phase(r.Phase))}
								if r.Duration > 0 {
									meta = append(meta, tlClock(r.Duration))
								}
								for _, l := range r.Languages {
									meta = append(meta, dubLangName(l))
								}
								ui.Text(c, strings.Join(meta, " · ")).FontSize(px(t, 11)).TextColor(t.TextMuted).SingleLine()
							})
							if ui.Button(c.Key("open"), i18n.T("Open")).Clicked() {
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

// dubStepper is the row of steps.
func (a *app) dubStepper(c *ui.Context) {
	t := c.Theme()
	names := []string{i18n.T("Source"), i18n.T("Transcribe"), i18n.T("Translate"), i18n.T("Cast"), i18n.T("Generate"), i18n.T("Export")}
	ui.Row(c.Key("dub-steps")).Gap(px(t, 4)).Padding(px(t, 8), px(t, 16)).AlignItems(ui.Center).FocusGroup(ui.Horizontal).Children(func() {
		for i, name := range names {
			done, running, failed := a.dubStepState(i)
			mark := strconv.Itoa(i + 1)
			switch {
			case running:
				mark = "…"
			case failed:
				mark = "!"
			case done:
				mark = "✓"
			}
			b := ui.ButtonBase(c.Key("step-"+strconv.Itoa(i))).Padding(px(t, 5), px(t, 10)).Radius(14).Gap(px(t, 6)).AlignItems(ui.Center).
				Label(i18n.T("Step %d: %s", i+1, name))
			if a.dubStep == i {
				b.Background(t.Accent).TextColor(t.AccentText)
			} else if failed {
				b.TextColor(t.Danger)
			} else if !done {
				b.TextColor(t.TextMuted)
			}
			b.Children(func() {
				ui.Text(c, mark).FontSize(px(t, 11)).Bold()
				ui.Text(c, name).FontSize(px(t, 13))
			})
			if b.Clicked() {
				a.dubCommitEdit()
				a.dubStep = i
			}
			if i < len(names)-1 {
				ui.Text(c, "›").TextColor(t.TextMuted)
			}
		}
	})
}

// --- banners --------------------------------------------------------------------------------

// dubBanner is a coloured note with actions.
func dubBanner(c *ui.Context, key, text string, tone ui.Color, actions func()) {
	t := c.Theme()
	ui.Row(c.Key(key)).Gap(px(t, 8)).AlignItems(ui.Center).Padding(px(t, 8), px(t, 10)).Radius(8).Background(tone.Alpha(0.12)).Border(1, tone.Alpha(0.5)).Children(func() {
		ui.Text(c, text).FontSize(px(t, 12)).Grow(1).MinWidth(0)
		if actions != nil {
			actions()
		}
	})
}

// dubBanners: an interrupted run, the last error, review checkpoints and
// stage warnings.
func (a *app) dubBanners(c *ui.Context) {
	t := c.Theme()
	p := a.dub
	if a.dubInterrupted() {
		dubBanner(c, "b-recover", i18n.T("The last run stopped before it finished (the app quit, or a step failed)."), t.Warning, func() {
			if ui.PrimaryButton(c.Key("b-resume"), i18n.T("Continue the run")).Clicked() {
				a.dubResume()
			}
			if ui.Button(c.Key("b-forget"), i18n.T("Dismiss")).Clicked() {
				a.dubSet(func(p *dub.Project) { p.Run = nil })
			}
		})
	}
	if a.dubErr != "" {
		dubBanner(c, "b-error", a.dubErr, t.Danger, func() {
			if a.dubNeed != nil {
				e := *a.dubNeed
				if ui.Button(c.Key("b-download"), i18n.T("Download")).Clicked() {
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
			if ui.Button(c.Key("b-error-x"), "×").Label(i18n.T("Dismiss")).Clicked() {
				a.dubErr, a.dubErrCode, a.dubNeed = "", "", nil
			}
		})
	}
	busy := a.dubBusy()
	if p.Settings.Review && !busy {
		switch {
		case len(p.Segments) > 0 && len(p.Tracks) == 0 && !dubAnyTranslated(p) && !a.dubDismissed["transcript"]:
			dubBanner(c, "b-transcript", i18n.T("Transcript ready — fix recognition errors now: every translation starts from it."), t.Accent, func() {
				if ui.PrimaryButton(c.Key("b-go-translate"), i18n.T("Translate →")).Clicked() {
					a.dubStep = dubStepTranslate
					a.dubTranslate(false)
				}
				if ui.Button(c.Key("b-transcript-x"), "×").Label(i18n.T("Dismiss")).Clicked() {
					a.dubDismissed["transcript"] = true
				}
			})
		case p.AllTranslated() && !p.TracksReady() && !a.dubDismissed["translated"]:
			dubBanner(c, "b-translated", i18n.T("Translations ready — skim them before generating."), t.Accent, func() {
				if ui.PrimaryButton(c.Key("b-go-generate"), i18n.T("Generate dub →")).Clicked() {
					a.dubStep = dubStepGenerate
					a.dubGenerate()
				}
				if ui.Button(c.Key("b-translated-x"), "×").Label(i18n.T("Dismiss")).Clicked() {
					a.dubDismissed["translated"] = true
				}
			})
		}
	}
	if p.TracksReady() && !busy && !a.dubDismissed["done"] {
		n := a.dubStaleCount(a.dubLang)
		text := i18n.T("Dub complete — review the timing; changed lines regenerate alone.")
		if n > 0 {
			text = i18n.N(n, "%d line changed since it was generated.", "%d lines changed since they were generated.", n)
		}
		dubBanner(c, "b-done", text, t.Success, func() {
			if n > 0 && ui.PrimaryButton(c.Key("b-regen"), i18n.N(n, "Regenerate %d changed", "Regenerate %d changed", n)).Clicked() {
				a.dubGenerate()
			}
			if ui.Button(c.Key("b-done-x"), "×").Label(i18n.T("Dismiss")).Clicked() {
				a.dubDismissed["done"] = true
			}
		})
	}
	// Stage warnings: the first, and how many more (expandable).
	var codes []string
	for _, k := range sortedKeys(p.Stages) {
		for _, w := range p.Stages[k].Warnings {
			if !slices.Contains(codes, w) && !a.dubDismissed["w:"+w] {
				codes = append(codes, w)
			}
		}
	}
	if len(codes) > 0 {
		shown := codes[:1]
		if a.dubDismissed["w:all"] {
			shown = codes
		}
		for i, w := range shown {
			ww := w
			dubBanner(c, "b-w-"+w, dubWarningText(w), t.Warning, func() {
				if i == 0 && len(codes) > 1 && !a.dubDismissed["w:all"] {
					if ui.Button(c.Key("more"), i18n.N(len(codes)-1, "%d more note", "%d more notes", len(codes)-1)).Clicked() {
						a.dubDismissed["w:all"] = true
					}
				}
				if ui.Button(c.Key("x"), "×").Label(i18n.T("Dismiss")).Clicked() {
					a.dubDismissed["w:"+ww] = true
				}
			})
		}
	}
}

// --- the workspace ---------------------------------------------------------------------------

// dubWorkspace: the player and video, the timeline, the editor.
func (a *app) dubWorkspace(c *ui.Context) {
	t := c.Theme()
	p := a.dub
	if len(p.Segments) == 0 {
		ui.Column(c.Key("dub-wait")).Grow(1).Center().Gap(px(t, 10)).Padding(px(t, 24)).Radius(10).Border(1, t.Border).Children(func() {
			if a.dubBusy() {
				ui.Spinner(c)
				ui.Text(c, a.dubStatus()).TextColor(t.TextMuted)
				return
			}
			if p.Source.Path == "" && p.Source.URL == "" {
				ui.Text(c, i18n.T("Choose the video or audio to dub in the Source step.")).TextColor(t.TextMuted)
				return
			}
			ui.Text(c, i18n.T("The transcript shows here once the source is transcribed.")).TextColor(t.TextMuted)
			if ui.PrimaryButton(c.Key("dub-start2"), a.dubStartLabel()).Clicked() {
				a.dubStart()
			}
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

// dubPlayerBar: A/B, play, time, the video.
func (a *app) dubPlayerBar(c *ui.Context) {
	t := c.Theme()
	p := a.dub
	pl := &a.dubPlayer
	if p.Source.HasVideo && a.dubVideo.show {
		if a.dubVideo.wv != nil && a.dubVideo.project == p.ID {
			ui.WebView(c, a.dubVideo.wv).Height(px(t, 220)).Radius(8)
		} else {
			ui.Column(c.Key("dub-video-off")).Height(px(t, 120)).Center().Radius(8).Background(t.Surface).Children(func() {
				ui.Text(c, i18n.T("The video preview shows in the app window.")).FontSize(px(t, 12)).TextColor(t.TextMuted)
			})
		}
	}
	ui.Row(c.Key("dub-player")).Gap(px(t, 6)).AlignItems(ui.Center).Wrap().Children(func() {
		label := "▶"
		if a.dubPlaying() {
			label = "❚❚"
		}
		if ui.Button(c.Key("dub-play"), label).Label(i18n.T("Play or pause")).Tooltip(i18n.T("Play or pause (Space)")).Disabled(pl.loading || p.Media.Audio == "").Clicked() {
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
			sel := i
			if ui.Segmented(c.Key("dub-ab"), &sel, names...).Label(i18n.T("Track")).Changed() || sel != i {
				a.dubSwitchTrack(tracks[sel])
			}
		}
		ui.Text(c, tlClock(a.dubPosition())+" / "+tlClock(p.Source.Duration)).FontSize(px(t, 12)).TextColor(t.TextMuted)
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
		ui.Text(c.Key("dub-overlap"), i18n.T("Some lines overlap the next: both play at once.")).FontSize(px(c.Theme(), 11)).TextColor(c.Theme().Danger)
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

// dubFooter: the status and progress, Cancel, and the step's action.
func (a *app) dubFooter(c *ui.Context) {
	t := c.Theme()
	p := a.dub
	ui.Row(c.Key("dub-footer")).Height(px(t, 46)).Shrink(0).Gap(px(t, 8)).AlignItems(ui.Center).Padding(0, px(t, 16)).Children(func() {
		if a.dubBusy() {
			j := a.dubRunning()
			ui.Spinner(c)
			ui.Text(c, a.dubStatus()).FontSize(px(t, 12)).SingleLine().MinWidth(0)
			if j.Progress.Fraction > 0 {
				ui.Progress(c, j.Progress.Fraction).Width(px(t, 160))
			}
			ui.Spacer(c)
			if ui.Button(c.Key("dub-cancel"), i18n.T("Cancel")).Clicked() {
				a.dubCancel()
			}
			return
		}
		ui.Text(c, dubPhaseText(p.Phase())).FontSize(px(t, 12)).TextColor(t.TextMuted)
		ui.Spacer(c)
		switch {
		case len(p.Segments) == 0:
			if ui.PrimaryButton(c.Key("dub-start"), a.dubStartLabel()).Disabled(p.Source.Path == "" && p.Source.URL == "").Clicked() {
				a.dubStart()
			}
		case !p.AllTranslated():
			if ui.PrimaryButton(c.Key("dub-translate"), dubTranslateLabel(p)).Clicked() {
				a.dubTranslate(false)
			}
		default:
			n := a.dubStaleCount(a.dubLang)
			label := i18n.N(len(p.Targets), "Generate dub", "Generate %d dubs", len(p.Targets))
			if p.TracksReady() {
				if n == 0 {
					ui.Text(c, i18n.T("Every line is up to date.")).FontSize(px(t, 12)).TextColor(t.TextMuted)
					return
				}
				label = i18n.N(n, "Regenerate %d changed", "Regenerate %d changed", n)
			}
			if ui.PrimaryButton(c.Key("dub-generate"), label).Clicked() {
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
	ui.Text(c, title).FontSize(px(c.Theme(), 14)).Bold()
}

// dubSourcePanel: the source, its language and speakers.
func (a *app) dubSourcePanel(c *ui.Context) {
	t := c.Theme()
	p := a.dub
	locked := a.dubBusy()
	dubPanelTitle(c, i18n.T("Source"))
	if p.Source.Path == "" && p.Source.URL == "" {
		zone := ui.Column(c.Key("src-drop")).Padding(px(t, 16)).Gap(px(t, 8)).AlignItems(ui.Center).Radius(8).Border(1, t.Border)
		if files := zone.DroppedFiles(); len(files) > 0 {
			a.dubDropped(files)
		}
		zone.Children(func() {
			ui.Text(c, i18n.T("Drop a video or audio file here")).TextColor(t.TextMuted)
			if ui.Button(c.Key("src-choose"), i18n.T("Choose file…")).Clicked() {
				a.chooseDubSource()
			}
		})
	} else {
		ui.Column(c.Key("src-info")).Gap(px(t, 4)).Padding(px(t, 10)).Radius(8).Border(1, t.Border).Children(func() {
			name := p.Source.Name
			if name == "" {
				name = p.Source.URL
			}
			ui.Text(c, name).Bold().MaxLines(2)
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
			ui.Text(c, strings.Join(meta, " · ")).FontSize(px(t, 11)).TextColor(t.TextMuted).MaxLines(2)
			if p.Source.Kind == dub.SourceURL && p.Source.WantCaptions {
				ui.Text(c, i18n.T("The video's captions are used when it has them.")).FontSize(px(t, 11)).TextColor(t.TextMuted)
			}
			ui.Row(c).Gap(px(t, 6)).Children(func() {
				if ui.Button(c.Key("src-remove"), i18n.T("Remove source")).Disabled(locked).Clicked() {
					if len(p.Segments) > 0 {
						a.dubConfirm, a.dubConfirmOpen = "remove", true
					} else {
						a.dubRemoveSource()
					}
				}
			})
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
		ui.Text(c, i18n.T("Detected: %s", dubLangName(p.Detected))).FontSize(px(t, 11)).TextColor(t.TextMuted)
	}
	ui.Field(c, i18n.T("Speakers"), func() {
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
	ui.Text(c, i18n.T("How many people speak? Leave it on Auto to detect them; the speaker model tells up to 4 apart.")).FontSize(px(t, 11)).TextColor(t.TextMuted)
	review := p.Settings.Review
	if dubSwitch(c, "src-review", i18n.T("Review after each step"), &review, false) {
		a.dubSettings(func(s *dub.Settings) { s.Review = review })
	}
	ui.Text(c, i18n.T("Stops after the transcript and after the translation, so they can be checked first.")).FontSize(px(t, 11)).TextColor(t.TextMuted)
	if ui.Button(c.Key("src-import"), i18n.T("Import subtitles (SRT/VTT)…")).Disabled(locked || p.Source.Path == "" && p.Source.URL == "").
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
		mark, col := "·", t.TextMuted
		switch s.Status {
		case dub.StatusDone:
			mark, col = "✓", t.Success
		case dub.StatusSkipped:
			mark, col = "–", t.Warning
		case dub.StatusFailed:
			mark, col = "!", t.Danger
		case dub.StatusRunning:
			mark, col = "…", t.Accent
		}
		ui.Row(c.Key("stage-" + string(st))).Gap(px(t, 6)).AlignItems(ui.Center).Children(func() {
			ui.Text(c, mark).TextColor(col).Bold().Width(px(t, 14))
			ui.Text(c, dubStageTitle(st)).FontSize(px(t, 12))
		})
		if s.Status == dub.StatusFailed && s.Message != "" {
			ui.Text(c, s.Message).FontSize(px(t, 11)).TextColor(t.Danger)
		}
	}
	if len(p.Segments) > 0 {
		ui.Divider(c)
		ui.Text(c, i18n.T("Speakers")).Bold()
		for _, sp := range p.SpeakerLabels() {
			name := p.SpeakerName(sp)
			n := 0
			for _, s := range p.Segments {
				if s.Speaker == sp {
					n++
				}
			}
			ui.Row(c.Key("spk-" + sp)).Gap(px(t, 6)).AlignItems(ui.Center).Children(func() {
				ui.Text(c, sp).FontSize(px(t, 11)).TextColor(t.TextMuted).Width(px(t, 26))
				in := ui.TextInput(c.Key("name"), &name).Label(i18n.T("Name of %s", sp)).Grow(1)
				if in.Changed() {
					a.dubRenameSpeaker(sp, name)
				}
				ui.Text(c, i18n.N(n, "%d line", "%d lines", n)).FontSize(px(t, 11)).TextColor(t.TextMuted)
			})
		}
		ui.Divider(c)
		ui.Row(c).Gap(px(t, 6)).Wrap().Children(func() {
			if ui.Button(c.Key("tr-again"), i18n.T("Transcribe again")).Disabled(a.dubBusy()).Tooltip(i18n.T("Recognise the speech again: edits to the transcript are lost")).Clicked() {
				a.dubConfirm, a.dubConfirmOpen = "transcribe", true
			}
			if ui.Button(c.Key("tr-mirror"), i18n.T("Mirror source delivery")).Disabled(a.dubLocked()).Tooltip(i18n.T("Give lines that stand out a direction (whispered, quick…) from how they are said")).Clicked() {
				a.dubMirror()
			}
			if ui.Button(c.Key("tr-cleanup"), i18n.T("Clean up fragments")).Disabled(a.dubLocked()).Clicked() {
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
		ui.Row(c.Key("tl-dialects")).Gap(px(t, 4)).Wrap().Children(func() {
			for _, d := range append([]string{base}, ds...) {
				label := i18n.T("Default")
				if d != base {
					label = d[len(base)+1:]
				}
				b := ui.Button(c.Key("dialect-"+d), label).Disabled(locked)
				if p.Primary() == d {
					b.Background(t.Accent).TextColor(t.AccentText)
				}
				if b.Clicked() {
					dd := d
					a.dubSet(func(p *dub.Project) { p.Targets[0] = dd })
					a.dubLang = dd
				}
			}
		})
	}
	// More languages.
	ui.Text(c, i18n.T("More languages")).FontSize(px(t, 12))
	ui.Row(c.Key("tl-more")).Gap(px(t, 4)).Wrap().AlignItems(ui.Center).Children(func() {
		for _, l := range p.Targets[min(1, len(p.Targets)):] {
			ll := l
			ui.Row(c.Key("chip-"+l)).Gap(px(t, 2)).AlignItems(ui.Center).Padding(px(t, 2), px(t, 6)).Radius(10).Background(t.Background).Border(1, t.Border).Children(func() {
				ui.Text(c, dubLangName(ll)).FontSize(px(t, 12))
				if ui.Button(c.Key("x"), "×").Label(i18n.T("Remove %s", dubLangName(ll))).Disabled(locked).Clicked() {
					a.dubSet(func(p *dub.Project) { p.Targets = slices.DeleteFunc(p.Targets, func(x string) bool { return x == ll }) })
				}
			})
		}
		ui.MenuButton(c.Key("tl-add"), i18n.T("+ Add"), func(m *ui.Menu) {
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
	ui.Divider(c)
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
		ui.Text(c, i18n.T("Uses %s", name)).FontSize(px(t, 11)).TextColor(t.TextMuted)
	case !ok:
		ui.Column(c.Key("tl-needs")).Gap(px(t, 4)).Padding(px(t, 8)).Radius(6).Background(t.Warning.Alpha(0.12)).Children(func() {
			ui.Text(c, i18n.T("Translating needs a translation engine: a local model (Ollama, LM Studio) or a provider in Settings → Writing help.")).FontSize(px(t, 12))
			if ui.Button(c.Key("tl-setup"), i18n.T("Set up writing help")).Clicked() {
				a.navigate("/settings/writing")
			}
		})
	}
	if p.Settings.Translator != "llm" && p.Settings.Translator != "" {
		ui.Text(c, i18n.T("The service's API key is kept in the Keychain as “mt.%s”.", p.Settings.Translator)).FontSize(px(t, 11)).TextColor(t.TextMuted)
	}
	if p.Settings.Translator == "llm" || p.Settings.Translator == "" {
		quals := []translate.Quality{translate.Fast, translate.Autofit, translate.Cinematic}
		qi := max(slices.Index(quals, p.Settings.Quality), 0)
		ui.Field(c, i18n.T("Quality"), func() {
			if ui.Segmented(c.Key("tl-quality"), &qi, i18n.T("Fast"), i18n.T("Autofit"), i18n.T("Cinematic")).Label(i18n.T("Quality")).Disabled(locked).Changed() {
				q := quals[qi]
				a.dubSettings(func(s *dub.Settings) { s.Quality = q })
			}
		})
		ui.Text(c, i18n.T("Fast is one literal pass; Autofit rewrites lines that would not fit their time; Cinematic drafts, critiques and adapts for natural phrasing.")).
			FontSize(px(t, 11)).TextColor(t.TextMuted)
		auto := p.Settings.AutoGlossary
		if dubSwitch(c, "tl-autogloss", i18n.T("Auto glossary"), &auto, locked) {
			a.dubSettings(func(s *dub.Settings) { s.AutoGlossary = auto })
		}
	}
	shorter := p.Settings.SuggestShorter
	if dubSwitch(c, "tl-shorter", i18n.T("Suggest shorter lines"), &shorter, locked) {
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
	ui.Row(c).Gap(px(t, 6)).Wrap().Children(func() {
		if ui.Button(c.Key("tl-gloss"), i18n.T("Glossary (%d)", len(p.Glossary))).Clicked() {
			a.dubGloss.open = !a.dubGloss.open
		}
		if ui.Button(c.Key("tl-paste"), i18n.T("Paste translation")).Disabled(len(p.Segments) == 0).Clicked() {
			a.dubPaste.open = !a.dubPaste.open
			if a.dubLang == "" {
				a.dubLang = p.Primary()
			}
		}
		if ui.Button(c.Key("tl-again"), i18n.T("Translate everything again")).Disabled(locked || len(p.Segments) == 0).
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
	dubPanelTitle(c, i18n.N(len(p.SpeakerLabels()), "Cast · %d speaker", "Cast · %d speakers", len(p.SpeakerLabels())))
	ui.Text(c, i18n.T("Drag a voice onto a speaker, or pick one from the list. Auto clones each speaker's own voice.")).FontSize(px(t, 11)).TextColor(t.TextMuted)
	choices := a.dubVoiceChoices()
	// The chips.
	ui.Row(c.Key("cast-chips")).Gap(px(t, 4)).Wrap().Children(func() {
		for _, v := range choices {
			chip := ui.Text(c.Key("chip-"+v.ref), v.label).FontSize(px(t, 12)).Padding(px(t, 3), px(t, 8)).Radius(10).Background(t.Background).Border(1, t.Border).
				Drag(dubVoiceChip{ref: v.ref})
			_ = chip
		}
	})
	if len(p.SpeakerLabels()) == 0 {
		ui.Text(c, i18n.T("Speakers appear once the source is transcribed.")).TextColor(t.TextMuted)
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
		card := ui.Column(c.Key("cast-"+sp)).Gap(px(t, 6)).Padding(px(t, 10)).Radius(8).Border(1, t.Border)
		if _, over := ui.DragOver[dubVoiceChip](card); over {
			card.Border(2, t.Accent)
		}
		if chip, ok := ui.Drop[dubVoiceChip](card); ok && !locked {
			a.dubCast(sp, chip.ref)
		}
		card.Children(func() {
			ui.Text(c, p.SpeakerName(sp)).Bold()
			if r, ok := p.References[sp]; ok {
				ui.Row(c).Gap(px(t, 6)).AlignItems(ui.Center).Children(func() {
					info := i18n.T("Own voice: %s–%s", tlClock(r.Start), tlClock(r.End))
					if r.Enhanced {
						info += " · " + i18n.T("cleaned")
					}
					ui.Text(c, info).FontSize(px(t, 11)).TextColor(t.TextMuted).Grow(1)
					path := a.dubSvc.Path(p.ID, r.File)
					id := "dubref:" + p.ID + ":" + sp
					label := "▶"
					if a.pl.Playing(id) {
						label = "❚❚"
					}
					if ui.Button(c.Key("ref-play"), label).Label(i18n.T("Hear the voice sample")).Clicked() {
						a.dubStopPlayer()
						if err := a.pl.Toggle(id, path); err != nil {
							a.toast(i18n.T("Cannot play: %v", err))
						}
					}
				})
			} else if p.Stage(dub.StageReferences, "").Status == dub.StatusDone {
				ui.Text(c, i18n.T("Says too little to clone: uses the default voice unless cast.")).FontSize(px(t, 11)).TextColor(t.Warning)
			}
			cur := p.Cast[sp]
			v := labels[max(slices.Index(refs, cur), 0)]
			if ui.Select(c.Key("voice"), &v, labels).Label(i18n.T("Voice of %s", p.SpeakerName(sp))).Disabled(locked).Changed() {
				a.dubCast(sp, refs[slices.Index(labels, v)])
			}
		})
	}
	ui.Divider(c)
	match := 0
	if p.Settings.VoiceMatch == dub.MatchLine {
		match = 1
	}
	ui.Field(c, i18n.T("Voice match"), func() {
		if ui.Segmented(c.Key("cast-match"), &match, i18n.T("Consistent"), i18n.T("Per line")).Label(i18n.T("Voice match")).Changed() {
			m := dub.MatchConsistent
			if match == 1 {
				m = dub.MatchLine
			}
			a.dubSettings(func(s *dub.Settings) { s.VoiceMatch = m })
		}
	})
	ui.Text(c, i18n.T("Consistent clones each speaker from one sample; Per line clones each line from its own audio (follows the delivery, less stable).")).FontSize(px(t, 11)).TextColor(t.TextMuted)
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
		ui.Text(c, i18n.T("This model speaks each line in exactly its time: the best lip sync.")).FontSize(px(t, 11)).TextColor(t.TextMuted)
	}
	// Timing.
	timings := dub.Timings
	tnames := []string{i18n.T("Concise"), i18n.T("Smart fit"), i18n.T("Stretch video"), i18n.T("Lip sync")}
	tcur := tnames[max(slices.Index(timings, p.Settings.Timing), 0)]
	ui.Field(c, i18n.T("Timing"), func() {
		if ui.Select(c.Key("gen-timing"), &tcur, tnames).Label(i18n.T("Timing")).Disabled(locked).Changed() {
			tm := timings[slices.Index(tnames, tcur)]
			a.dubSettings(func(s *dub.Settings) { s.Timing = tm })
		}
	})
	ui.Text(c, dubTimingHelp(p.Settings.Timing)).FontSize(px(t, 11)).TextColor(t.TextMuted)
	keep := p.Settings.KeepBackground
	if dubSwitch(c, "gen-bg", i18n.T("Keep background (music and effects)"), &keep, locked) {
		a.dubSettings(func(s *dub.Settings) { s.KeepBackground = keep })
	}
	ui.Divider(c)
	ui.Text(c, i18n.T("Voice controls")).Bold()
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
	if ui.Button(c.Key("gen-reset"), i18n.T("Reset voice controls")).Disabled(locked).Clicked() {
		a.dubSettings(func(s *dub.Settings) { s.Steps, s.Guidance, s.Speed, s.Instruct = 0, 0, 0, "" })
	}
	ui.Divider(c)
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
			ui.Text(c, dubLangName(l)).Grow(1)
			ui.Text(c, state).FontSize(px(t, 11)).TextColor(t.TextMuted)
		})
	}
	ui.Row(c).Gap(px(t, 6)).Wrap().Children(func() {
		if ui.PrimaryButton(c.Key("gen-go"), i18n.N(len(p.Targets), "Generate dub", "Generate %d dubs", len(p.Targets))).Disabled(locked || len(p.Segments) == 0).Clicked() {
			a.dubGenerate()
		}
		if p.Track(a.dubLang).File != "" && ui.Button(c.Key("gen-verify"), i18n.T("Verify")).Disabled(locked).Clicked() {
			a.dubVerify()
		}
	})
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
	ui.Column(c.Key("dub-gloss")).Gap(px(t, 6)).Width(px(t, 620)).MaxHeight(px(t, 520)).Children(func() {
		ui.Row(c).Gap(px(t, 6)).AlignItems(ui.Center).Children(func() {
			ui.Text(c, i18n.N(len(p.Glossary), "Glossary · %d term", "Glossary · %d terms", len(p.Glossary))).Bold().Grow(1)
			if ui.Button(c.Key("g-auto"), i18n.T("Auto")).Tooltip(i18n.T("Ask the language model for names and recurring terms")).
				Disabled(g.extracting || len(p.Segments) == 0 || a.llmRouter == nil).Clicked() {
				a.dubExtractTerms()
			}
			if ui.Button(c.Key("g-close"), "×").Label(i18n.T("Close")).Clicked() {
				g.open = false
			}
		})
		if len(p.Glossary) == 0 {
			ui.Text(c, i18n.T("No terms yet. Add one below, or Auto.")).FontSize(px(t, 12)).TextColor(t.TextMuted)
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
					if ui.Button(c.Key("cancel"), i18n.T("Cancel")).Clicked() {
						g.editing = -1
					}
					return
				}
				line := term.Source + " → " + term.Target
				if term.Note != "" {
					line += " · " + term.Note
				}
				ui.Text(c, line).FontSize(px(t, 12)).Grow(1).MinWidth(0)
				if term.Auto {
					ui.Badge(c, i18n.T("auto"))
				}
				if ui.Button(c.Key("edit"), i18n.T("Edit")).Clicked() {
					g.editing, g.eSrc, g.eTgt, g.eNot = ii, term.Source, term.Target, term.Note
				}
				if ui.Button(c.Key("del"), "×").Label(i18n.T("Delete %s", term.Source)).Clicked() {
					a.dubDeleteTerm(ii)
				}
			})
		}
		ui.Row(c.Key("g-add")).Gap(px(t, 6)).AlignItems(ui.Center).Children(func() {
			ui.TextInput(c.Key("src"), &g.src).Placeholder(i18n.T("Source")).Label(i18n.T("Source term")).Grow(1)
			ui.TextInput(c.Key("tgt"), &g.tgt).Placeholder(i18n.T("Translation")).Label(i18n.T("Translation")).Grow(1)
			ui.TextInput(c.Key("note"), &g.note).Placeholder(i18n.T("Note (optional)")).Label(i18n.T("Note")).Grow(1)
			if ui.Button(c.Key("add"), i18n.T("Add term")).Disabled(strings.TrimSpace(g.src) == "" || strings.TrimSpace(g.tgt) == "").Clicked() {
				a.dubAddTerm()
			}
		})
		ui.Text(c, i18n.T("Terms go into every translation; yours win over auto ones.")).FontSize(px(t, 11)).TextColor(t.TextMuted)
	})
}

// dubPastePanel pastes a translation.
func (a *app) dubPastePanel(c *ui.Context) {
	t := c.Theme()
	ps := &a.dubPaste
	ui.Column(c.Key("dub-paste")).Gap(px(t, 6)).Width(px(t, 620)).Children(func() {
		ui.Row(c).Gap(px(t, 6)).AlignItems(ui.Center).Children(func() {
			ui.Text(c, i18n.T("Paste translation · %s", dubLangName(a.dubLang))).Bold().Grow(1)
			if ui.Button(c.Key("p-file"), i18n.T("Load .srt / .vtt…")).Clicked() {
				a.dubPasteFile()
			}
			if ui.Button(c.Key("p-close"), "×").Label(i18n.T("Close")).Clicked() {
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
		ui.Row(c).Gap(px(t, 6)).AlignItems(ui.Center).Children(func() {
			ui.Badge(c, mode)
			ui.Text(c, i18n.T("%d of %d segments matched", pv.Matched, len(pv.Rows))).FontSize(px(t, 12))
			if pv.Unused > 0 {
				ui.Text(c, i18n.N(pv.Unused, "%d pasted line went unused", "%d pasted lines went unused", pv.Unused)).FontSize(px(t, 12)).TextColor(t.Warning)
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
			ui.Text(c.Key("pv-"+r.ID), firstOf(r.Before, "—")+"  →  "+r.After).FontSize(px(t, 11)).TextColor(t.TextMuted).MaxLines(2)
		}
		if pv.Matched > shown {
			ui.Text(c, i18n.N(pv.Matched-shown, "+ %d more", "+ %d more", pv.Matched-shown)).FontSize(px(t, 11)).TextColor(t.TextMuted)
		}
		if ps.err != "" {
			ui.Text(c, ps.err).FontSize(px(t, 11)).TextColor(t.Danger)
		}
		if ui.PrimaryButton(c.Key("p-apply"), i18n.N(pv.Matched, "Apply to %d segment", "Apply to %d segments", pv.Matched)).Disabled(pv.Matched == 0 || a.dubLocked()).Clicked() {
			a.dubApplyPaste()
		}
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

// dubSwitch is a switch with its label beside it; it reports a change.
func dubSwitch(c *ui.Context, key, label string, on *bool, disabled bool) bool {
	t := c.Theme()
	changed := false
	ui.Row(c.Key(key + "-row")).Gap(px(t, 8)).AlignItems(ui.Center).Children(func() {
		ui.Text(c, label).Grow(1).MinWidth(0)
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
