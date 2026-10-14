package main

import (
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/hehehai/AudioSmith/internal/dub"
	"github.com/hehehai/AudioSmith/internal/i18n"
	"github.com/hehehai/AudioSmith/internal/jobs"
	"github.com/hehehai/AudioSmith/internal/services"
	"github.com/hehehai/AudioSmith/internal/store"
)

// batchPage is /batch.
func (a *app) batchPage(c *ui.Context) {
	t := c.Theme()
	b := &a.batch
	if len(b.jobs) > 0 {
		c.After(time.Second)
	}
	header(c, i18n.T("Batch dubbing"), func() {
		if ui.Button(c.Key("batch-dub"), i18n.T("Dub page")).Clicked() {
			a.navigate("/dub")
		}
	})
	ui.Row(c).Grow(1).MinHeight(0).AlignItems(ui.Stretch).Children(func() {
		ui.Scroll(c.Key("batch-side")).Width(px(t, inspectorWidth+40)).Shrink(0).Background(t.Surface).Children(func() {
			ui.Column(c).Padding(px(t, 16)).Gap(px(t, 10)).Children(func() {
				a.batchSetup(c)
				ui.Divider(c)
				a.batchWatchPanel(c)
			})
		})
		ui.Box(c).Width(1).Background(t.Border)
		ui.Column(c).Grow(1).MinWidth(0).Padding(px(t, 12), px(t, 16)).Gap(px(t, 8)).Children(func() {
			a.batchQueue(c)
		})
	})
	a.batchConfirm(c)
}

// batchSetup: the videos to add and the batch settings.
func (a *app) batchSetup(c *ui.Context) {
	t := c.Theme()
	b := &a.batch
	s := &b.settings
	ui.Text(c, i18n.T("Videos")).FontSize(px(t, 14)).Bold()
	zone := ui.Column(c.Key("batch-drop")).Padding(px(t, 12)).Gap(px(t, 6)).Radius(8).Border(1, t.Border)
	if files := zone.DroppedFiles(); len(files) > 0 {
		a.batchDropped(files)
	}
	if zone.FileDragOver() {
		zone.Border(2, t.Accent)
	}
	zone.Children(func() {
		if len(b.files) == 0 {
			ui.Text(c, i18n.T("Drop videos here to dub them in a batch.")).FontSize(px(t, 12)).TextColor(t.TextMuted)
		}
		for _, f := range b.files {
			ff := f
			ui.Row(c.Key("file-" + f)).Gap(px(t, 6)).AlignItems(ui.Center).Children(func() {
				ui.Text(c, filepath.Base(ff)).FontSize(px(t, 12)).Grow(1).MinWidth(0).SingleLine()
				if ui.Button(c.Key("x"), "×").Label(i18n.T("Remove %s", filepath.Base(ff))).Clicked() {
					b.files = slices.DeleteFunc(b.files, func(x string) bool { return x == ff })
				}
			})
		}
		if ui.Button(c.Key("batch-add"), i18n.T("Add videos…")).Clicked() {
			a.batchAddFiles()
		}
	})
	// Target languages.
	ui.Text(c, i18n.T("Dub into")).FontSize(px(t, 12)).Bold()
	ui.Row(c.Key("batch-langs")).Gap(px(t, 4)).Wrap().AlignItems(ui.Center).Children(func() {
		for _, l := range s.Targets {
			ll := l
			ui.Row(c.Key("lang-"+l)).Gap(px(t, 2)).AlignItems(ui.Center).Padding(px(t, 2), px(t, 6)).Radius(10).Background(t.Background).Border(1, t.Border).Children(func() {
				ui.Text(c, dubLangName(ll)).FontSize(px(t, 12))
				if ui.Button(c.Key("x"), "×").Label(i18n.T("Remove %s", dubLangName(ll))).Clicked() {
					a.batchSetSettings(func(s *services.BatchSettings) {
						s.Targets = slices.DeleteFunc(s.Targets, func(x string) bool { return x == ll })
					})
				}
			})
		}
		ui.MenuButton(c.Key("batch-lang-add"), i18n.T("+ Add"), func(m *ui.Menu) {
			for _, l := range dubTargetLangs {
				if !slices.Contains(s.Targets, l) && m.Item(dubLangName(l)).Chosen() {
					ll := l
					a.batchSetSettings(func(s *services.BatchSettings) { s.Targets = append(s.Targets, ll) })
				}
			}
		})
	})
	// Voice.
	choices := []dubVoiceChoice{{"", i18n.T("Auto · each speaker's own voice"), ""}, {dub.VoiceDefault, i18n.T("Default voice"), ""}}
	model := firstOf(s.Model, a.dubDefaultModel())
	e := entryFor(model)
	for i := range a.voices {
		v := &a.voices[i]
		if ok, _ := voiceUsable(v, e); ok {
			choices = append(choices, dubVoiceChoice{v.ID, v.Name, i18n.T("Saved voices")})
		}
	}
	for _, pr := range dubPresets() {
		choices = append(choices, dubVoiceChoice{dub.PresetPrefix + pr.ID, pr.Name.In(i18n.Locale()), i18n.T("Design presets")})
	}
	labels := make([]string, len(choices))
	refs := make([]string, len(choices))
	for i, ch := range choices {
		labels[i], refs[i] = ch.label, ch.ref
		if ch.group != "" {
			labels[i] = ch.group + " · " + ch.label
		}
	}
	cur := labels[max(slices.Index(refs, s.Voice), 0)]
	ui.Field(c, i18n.T("Voice"), func() {
		if ui.Select(c.Key("batch-voice"), &cur, labels).Label(i18n.T("Voice")).Changed() {
			ref := refs[slices.Index(labels, cur)]
			a.batchSetSettings(func(s *services.BatchSettings) { s.Voice = ref })
		}
	})
	models := dubTTSModels()
	mnames := []string{i18n.T("Automatic")}
	mrepos := []string{""}
	for _, e := range models {
		mnames = append(mnames, e.Label)
		mrepos = append(mrepos, e.Repo)
	}
	mcur := mnames[max(slices.Index(mrepos, s.Model), 0)]
	ui.Field(c, i18n.T("Voice model"), func() {
		if ui.Select(c.Key("batch-model"), &mcur, mnames).Label(i18n.T("Voice model")).Changed() {
			repo := mrepos[slices.Index(mnames, mcur)]
			a.batchSetSettings(func(s *services.BatchSettings) { s.Model = repo })
		}
	})
	keep := s.KeepBackground
	if dubSwitch(c, "batch-bg", i18n.T("Keep background (music and effects)"), &keep, false) {
		a.batchSetSettings(func(s *services.BatchSettings) { s.KeepBackground = keep })
	}
	if ok, name := a.dubTranslatorReady(dub.Settings{Translator: s.Translator}); ok {
		ui.Text(c, i18n.T("Translator: %s", name)).FontSize(px(t, 11)).TextColor(t.TextMuted)
	} else {
		ui.Text(c, i18n.T("Translating needs a translation engine: a local model (Ollama, LM Studio) or a provider in Settings → Writing help.")).FontSize(px(t, 11)).TextColor(t.Warning)
		if ui.Button(c.Key("batch-llm"), i18n.T("Set up writing help")).Clicked() {
			a.navigate("/settings/writing")
		}
	}
	n, m := len(b.files), len(s.Targets)
	ui.Text(c, i18n.N(n, "%d video", "%d videos", n)+" × "+i18n.N(m, "%d language", "%d languages", m)+" = "+i18n.N(n*m, "%d dub", "%d dubs", n*m)).
		FontSize(px(t, 12)).TextColor(t.TextMuted)
	if primaryButton(c.Key("batch-queue"), i18n.T("Add to queue")).Disabled(n == 0 || m == 0).Clicked() {
		a.batchEnqueue()
	}
	for _, e := range b.errs {
		ui.Text(c, e).FontSize(px(t, 11)).TextColor(t.Danger)
	}
}

// batchWatchPanel is the watch folder.
func (a *app) batchWatchPanel(c *ui.Context) {
	t := c.Theme()
	w := a.batch.watch
	ui.Text(c, i18n.T("Watch folder")).FontSize(px(t, 14)).Bold()
	ui.Text(c, i18n.T("New videos dropped into the folder join the queue with the settings above (checked every 5 s, once a file stops growing). Files already there are left alone.")).
		FontSize(px(t, 11)).TextColor(t.TextMuted)
	if !w.Active {
		if ui.Button(c.Key("watch-start"), i18n.T("Watch a folder…")).Disabled(len(a.batch.settings.Targets) == 0).Clicked() {
			a.batchWatchChoose()
		}
		return
	}
	ui.Column(c.Key("watch-on")).Gap(px(t, 4)).Padding(px(t, 10)).Radius(8).Border(1, t.Border).Children(func() {
		state := i18n.T("Watching")
		if w.Paused {
			state = i18n.T("Paused")
		}
		ui.Text(c, state+" · "+shortPath(w.Dir)).FontSize(px(t, 12)).MaxLines(2)
		ui.Text(c, i18n.N(w.Added, "%d video added", "%d videos added", w.Added)).FontSize(px(t, 11)).TextColor(t.TextMuted)
		if w.Err != "" {
			ui.Text(c, w.Err).FontSize(px(t, 11)).TextColor(t.Danger)
		}
		ui.Row(c).Gap(px(t, 6)).Children(func() {
			if w.Paused {
				if ui.Button(c.Key("watch-resume"), i18n.T("Resume watching")).Clicked() {
					a.batchWatchPause(false)
				}
			} else if ui.Button(c.Key("watch-pause"), i18n.T("Pause")).Clicked() {
				a.batchWatchPause(true)
			}
			if ui.Button(c.Key("watch-stop"), i18n.T("Stop")).Clicked() {
				a.batchWatchStop()
			}
		})
	})
}

// batchQueue is the tabs and the job cards.
func (a *app) batchQueue(c *ui.Context) {
	t := c.Theme()
	b := &a.batch
	var tabs [3][]store.DubRecord
	for _, r := range b.items {
		switch a.batchStatus(r.ID) {
		case "active":
			tabs[0] = append(tabs[0], r)
		case "done":
			tabs[1] = append(tabs[1], r)
		default:
			tabs[2] = append(tabs[2], r)
		}
	}
	ui.Tabs(c.Key("batch-tabs"), &b.tab, i18n.T("Active (%d)", len(tabs[0])), i18n.T("Completed (%d)", len(tabs[1])), i18n.T("Failed (%d)", len(tabs[2])))
	list := tabs[min(max(b.tab, 0), 2)]
	if len(list) == 0 {
		empty := []string{i18n.T("No active dubs. Add videos on the left."), i18n.T("Nothing has completed yet."), i18n.T("No failed dubs.")}[b.tab]
		ui.Column(c.Key("batch-empty")).Grow(1).Center().Children(func() {
			ui.Text(c, empty).TextColor(t.TextMuted)
		})
		return
	}
	ui.Scroll(c.Key("batch-list")).Grow(1).Children(func() {
		ui.Column(c).Gap(px(t, 6)).Children(func() {
			for _, r := range list {
				a.batchCard(c, r)
			}
		})
	})
}

// batchCard is one video of the queue.
func (a *app) batchCard(c *ui.Context, r store.DubRecord) {
	t := c.Theme()
	b := &a.batch
	p := b.docs[r.ID]
	id := r.ID
	status := a.batchStatus(id)
	ui.Column(c.Key("card-"+id)).Gap(px(t, 4)).Padding(px(t, 10)).Radius(8).Border(1, t.Border).Children(func() {
		ui.Row(c).Gap(px(t, 6)).AlignItems(ui.Center).Children(func() {
			ui.Text(c, firstOf(r.Title, filepath.Base(r.Source))).Bold().Grow(1).MinWidth(0).SingleLine()
			for _, l := range r.Languages {
				ui.Badge(c, l)
			}
		})
		meta := []string{i18n.DateTime(r.CreatedAt)}
		if r.Duration > 0 {
			meta = append(meta, tlClock(r.Duration))
		}
		ui.Text(c, strings.Join(meta, " · ")).FontSize(px(t, 11)).TextColor(t.TextMuted)
		switch status {
		case "active":
			j := batchRunning(b.jobs[id])
			stage := i18n.T("Queued")
			if j.ID != "" && j.State == jobs.StateRunning {
				stage = dubStageName(dubStageOfKind(j.Kind))
				var pp dubJobParams
				dubJSON(j.Params, &pp)
				if pp.Lang != "" {
					stage = dubLangName(pp.Lang) + " · " + stage
				}
				if j.Progress.Message != "" && strings.Contains(j.Progress.Message, "/") {
					stage += " " + j.Progress.Message
				}
			}
			ui.Row(c).Gap(px(t, 6)).AlignItems(ui.Center).Children(func() {
				ui.Text(c, stage).FontSize(px(t, 12)).Grow(1)
				if j.Progress.Fraction > 0 {
					ui.Progress(c, j.Progress.Fraction).Width(px(t, 120))
				}
				if ui.Button(c.Key("cancel"), i18n.T("Cancel")).Clicked() {
					a.batchCancel(id)
				}
			})
		case "done":
			ui.Row(c).Gap(px(t, 6)).AlignItems(ui.Center).Wrap().Children(func() {
				if p != nil {
					for _, l := range p.Targets {
						ll := l
						if ui.Button(c.Key("export-"+l), i18n.T("Export %s", dubLangName(ll))).Disabled(p.Track(ll).File == "").Clicked() {
							a.batchExport(id, ll)
						}
					}
				}
				ui.Spacer(c)
				if ui.Button(c.Key("open"), i18n.T("Open in the Dub page")).Clicked() {
					a.openDubAny(id)
				}
				if ui.Button(c.Key("delete"), i18n.T("Delete")).Clicked() {
					b.confirm = id
				}
			})
		default:
			why := i18n.T("Stopped before it finished.")
			if p != nil {
				for _, k := range p.Failed() {
					st := p.Stages[k]
					why = fmt.Sprintf("%s: %s", dubStageName(dubStageOfKind("dub."+strings.Split(k, ":")[0])), dubErrorText(&jobs.Error{Code: st.Code, Message: st.Message}))
					break
				}
			}
			ui.Text(c, why).FontSize(px(t, 12)).TextColor(t.Danger).MaxLines(3)
			ui.Row(c).Gap(px(t, 6)).Children(func() {
				if ui.Button(c.Key("retry"), i18n.T("Retry")).Clicked() {
					a.batchRetry(id)
				}
				if ui.Button(c.Key("open"), i18n.T("Open in the Dub page")).Clicked() {
					a.openDubAny(id)
				}
				if ui.Button(c.Key("delete"), i18n.T("Delete")).Clicked() {
					b.confirm = id
				}
			})
		}
	})
}

// batchRunning is the running (else the oldest) job of a project.
func batchRunning(m map[string]jobs.Job) jobs.Job {
	var best jobs.Job
	for _, j := range m {
		if j.State == jobs.StateRunning {
			return j
		}
		if best.ID == "" || j.Created.Before(best.Created) {
			best = j
		}
	}
	return best
}

// openDubAny opens a project (the batch's too) on the Dub page.
func (a *app) openDubAny(id string) { a.openDub(id) }

// batchConfirm asks before deleting a queued video's dub.
func (a *app) batchConfirm(c *ui.Context) {
	b := &a.batch
	if b.confirm == "" {
		return
	}
	open := true
	switch ui.AlertDialog(c.Key("batch-confirm"), &open, i18n.T("Delete this dub?"), i18n.T("The project and every file made for it are deleted. The source file is kept."), i18n.T("Delete"), i18n.T("Cancel")) {
	case 0:
		id := b.confirm
		b.confirm = ""
		a.deleteDub(id)
	case 1:
		b.confirm = ""
	}
	if !open {
		b.confirm = ""
	}
}
