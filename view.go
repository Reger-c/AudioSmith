package main

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

const (
	titleBarHeight = 52
	sidebarWidth   = 200
	inspectorWidth = 300
)

func icon(shapes string) *ui.SVG {
	return ui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.3" stroke-linecap="round" stroke-linejoin="round">` + shapes + `</svg>`))
}

var (
	studioIcon  = icon(`<path d="M2 8h1.5M5 5v6M8 3v10M11 5.5v5M14 8h-1"/>`)
	historyIcon = icon(`<circle cx="8" cy="8" r="6"/><path d="M8 4.5V8l2.5 1.5"/>`)
	engineIcon  = icon(`<rect x="3.5" y="3.5" width="9" height="9" rx="1.5"/><path d="M6 1.5v2M10 1.5v2M6 12.5v2M10 12.5v2M1.5 6h2M1.5 10h2M12.5 6h2M12.5 10h2"/>`)
)

func (a *app) view(c *ui.Context) {
	t := c.Theme()
	for _, m := range a.toasts {
		c.Toast(m)
	}
	a.toasts = nil

	if c.Shortcut(ui.Cmd, ui.KeyEnter) {
		a.generate()
	}
	if a.job != nil {
		// Keep the elapsed time current.
		c.After(500 * time.Millisecond)
	}
	if a.pl.p != nil && a.pl.p.Playing() {
		// Notice the end of playback, so Pause turns back into Play.
		c.After(200 * time.Millisecond)
	}

	ui.Row(c).Fill().AlignItems(ui.Stretch).Children(func() {
		ui.Column(c).Width(sidebarWidth).Shrink(0).Background(t.Surface).Children(func() {
			a.sidebar(c)
		})
		ui.Box(c).Width(1).Background(t.Border)
		ui.Column(c).Grow(1).MinWidth(0).Background(t.Background).Children(func() {
			switch a.page {
			case "history":
				a.historyPage(c)
			case "engine":
				a.enginePage(c)
			default:
				a.studioPage(c)
			}
		})
	})
}

func (a *app) sidebar(c *ui.Context) {
	t := c.Theme()
	ui.Box(c).Height(titleBarHeight).Shrink(0).DragWindow()
	ui.Sidebar(c, &a.page, func() {
		ui.SidebarSection(c, "AudioSmith", nil, func() {
			ui.SidebarItem(c, "studio", studioIcon, "Studio")
			ui.SidebarItem(c, "history", historyIcon, "History").Children(func() {
				if n := len(a.lib.clips); n > 0 {
					ui.Badge(c, fmt.Sprint(n))
				}
			})
			ui.SidebarItem(c, "engine", engineIcon, "Engine")
		})
	}).Grow(1).Background(ui.Transparent).Label("Sections")

	// The engine's status, at the bottom.
	ui.Column(c).Padding(12, 14).Gap(4).Children(func() {
		color, label := t.Warning, "Engine starting…"
		switch a.engState {
		case engineReady:
			color, label = t.Success, "Engine ready"
		case engineFailed:
			color, label = t.Danger, "Engine stopped"
		}
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Box(c).Size(8, 8).Radius(4).Background(color)
			ui.Text(c, label).FontSize(12)
		})
		model := "No model loaded"
		if a.loadedModel != "" {
			model = modelLabelOf(a.loadedModel)
		}
		ui.Text(c, model).FontSize(11).TextColor(t.TextMuted).SingleLine().Ellipsis("…")
	})
}

// header is a page's title bar, which drags the window.
func header(c *ui.Context, title string, trailing func()) {
	ui.Row(c).Height(titleBarHeight).Shrink(0).AlignItems(ui.Center).Padding(0, 16, 0, 20).Gap(8).DragWindow().Children(func() {
		ui.Text(c, title).FontSize(15).Bold().SingleLine().Grow(1)
		if trailing != nil {
			trailing()
		}
	})
	ui.Divider(c)
}

// MARK: Studio

func (a *app) studioPage(c *ui.Context) {
	t := c.Theme()
	header(c, "Studio", nil)
	ui.Row(c).Grow(1).MinHeight(0).AlignItems(ui.Stretch).Children(func() {
		ui.Column(c).Grow(1).MinWidth(0).Padding(16, 20).Gap(12).Children(func() {
			ui.TextArea(c.Key("script"), &a.text).
				Placeholder("Type or paste the text to speak…").
				Label("Script").
				Grow(1).MinHeight(120).
				FontSize(15)
			a.generateBar(c)
			if a.job != nil {
				a.jobStatus(c)
			} else if a.jobError != "" {
				ui.Text(c, a.jobError).TextColor(t.Danger).FontSize(12).Selectable()
			}
			if cl := a.lastClip(); cl != nil {
				a.clipCard(c, cl, true)
			}
		})
		ui.Box(c).Width(1).Background(t.Border)
		ui.Scroll(c).Width(inspectorWidth).Shrink(0).Background(t.Surface).Children(func() {
			ui.Column(c).Padding(16).Gap(14).Children(func() {
				a.inspector(c)
			})
		})
	})
}

func (a *app) generateBar(c *ui.Context) {
	t := c.Theme()
	ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
		n := utf8.RuneCountInString(strings.TrimSpace(a.text))
		ui.Textf(c, "%d characters", n).FontSize(12).TextColor(t.TextMuted).Grow(1)
		if a.job != nil {
			if ui.Button(c.Key("cancel"), "Cancel").Clicked() {
				a.cancel()
			}
		}
		label := "Generate"
		if a.job != nil && a.job.kind == "synthesize" {
			label = "Generating…"
		}
		if ui.PrimaryButton(c.Key("generate"), label).
			Disabled(!a.canGenerate()).
			Tooltip("Generate speech (⌘↩)").
			Clicked() {
			a.generate()
		}
	})
}

func (a *app) jobStatus(c *ui.Context) {
	t := c.Theme()
	j := a.job
	ui.Column(c).Gap(6).Children(func() {
		ui.Progress(c, j.fraction).Label(j.status)
		ui.Row(c).Gap(8).Children(func() {
			ui.Text(c, j.status).FontSize(12).Grow(1).MinWidth(0).SingleLine().Ellipsis("…")
			info := fmt.Sprintf("%.0f s", time.Since(j.started).Seconds())
			if j.audioSeconds > 0 {
				info = fmt.Sprintf("%.1f s of audio · ", j.audioSeconds) + info
			}
			ui.Text(c, info).FontSize(12).TextColor(t.TextMuted)
		})
	})
}

func (a *app) inspector(c *ui.Context) {
	t := c.Theme()
	p := a.preset()

	ui.Field(c, "Model", func() {
		if ui.Select(c.Key("model"), &a.modelLabel, presetLabels()).Label("Model").Changed() {
			a.modelChanged()
		}
	})
	if a.modelLabel == customModelLabel {
		ui.Field(c, "Repo", func() {
			ui.TextInput(c.Key("repo"), &a.customRepo).Placeholder("owner/model-name")
		})
	}
	about := p.About
	if p.Size != "" {
		about += " Download " + p.Size + "."
	}
	ui.Text(c, about).FontSize(12).TextColor(t.TextMuted)

	ui.Row(c).Gap(8).Children(func() {
		loaded := p.Repo != "" && p.Repo == a.loadedModel
		label := "Preload"
		if loaded {
			label = "Loaded"
		}
		if ui.Button(c.Key("preload"), label).
			Disabled(loaded || a.job != nil || a.engState != engineReady || p.Repo == "").
			Tooltip("Download and load the model now").
			Clicked() {
			a.preload()
		}
		if ui.Button(c.Key("unload"), "Unload").
			Disabled(a.loadedModel == "" || a.job != nil).
			Tooltip("Free the memory the loaded model takes").
			Clicked() {
			a.unload()
		}
	})

	ui.Divider(c)

	voiceLabel := "Voice"
	if p.VoiceIsStyle {
		voiceLabel = "Voice / style"
	}
	if len(p.Voices) > 0 || a.modelLabel == customModelLabel {
		ui.Field(c, voiceLabel, func() {
			ui.Autocomplete(c.Key("voice"), &a.voice, p.Voices).Placeholder("Model default").Label(voiceLabel)
		})
	}
	if len(p.Languages) > 0 || a.modelLabel == customModelLabel {
		ui.Field(c, "Language", func() {
			ui.Autocomplete(c.Key("language"), &a.language, p.Languages).Placeholder("Auto").Label("Language")
		})
	}
	if p.Speed {
		ui.Field(c, fmt.Sprintf("Speed  %.2f×", a.speed), func() {
			snapped(ui.Slider(c.Key("speed"), &a.speed, 0.5, 2).Label("Speed"), &a.speed, 0.05)
		})
	}
	if p.Sampling {
		ui.Checkbox(c.Key("defaults"), &a.modelDefaults, "Use the model's sampling defaults")
		if !a.modelDefaults {
			ui.Field(c, fmt.Sprintf("Temperature  %.2f", a.temperature), func() {
				snapped(ui.Slider(c.Key("temperature"), &a.temperature, 0, 1.5).Label("Temperature"), &a.temperature, 0.05)
			})
			ui.Field(c, fmt.Sprintf("Top P  %.2f", a.topP), func() {
				snapped(ui.Slider(c.Key("topp"), &a.topP, 0.05, 1).Label("Top P"), &a.topP, 0.05)
			})
			ui.Field(c, "Max tokens", func() {
				ui.NumberInput(c.Key("maxtokens"), &a.maxTokens, 50, 8000, 50).Label("Max tokens")
			})
		}
	}
	if p.RefAudio {
		ui.Divider(c)
		ui.Text(c, "Voice cloning").Bold()
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			name := "No reference clip"
			if a.refAudio != "" {
				name = filepath.Base(a.refAudio)
			}
			ui.Text(c, name).FontSize(12).Grow(1).MinWidth(0).SingleLine().Ellipsis("…")
			if ui.Button(c.Key("choose-ref"), "Choose…").Clicked() {
				a.chooseRefAudio()
			}
			if a.refAudio != "" && ui.Button(c.Key("clear-ref"), "Clear").Clicked() {
				a.refAudio, a.refText = "", ""
			}
		})
		ui.Field(c, "Reference transcript", func() {
			ui.TextArea(c.Key("ref-text"), &a.refText).Lines(2, 5).Placeholder("What the reference clip says").Label("Reference transcript")
		})
	}
	ui.Divider(c)
	ui.Checkbox(c.Key("autoplay"), &a.autoplay, "Play each take when it is ready")
}

// MARK: Clips

// clipCard shows a take: its waveform, what made it, and what to do with it.
func (a *app) clipCard(c *ui.Context, cl *clip, latest bool) {
	t := c.Theme()
	ui.Column(c.Key("card-"+cl.ID)).Gap(8).Padding(12).Radius(10).Border(1, t.Border).Background(t.Surface).Children(func() {
		ui.Row(c).Gap(10).AlignItems(ui.Center).Children(func() {
			label := "Play"
			if a.pl.playing(cl.ID) {
				label = "Pause"
			}
			if ui.PrimaryButton(c.Key("play"), label).Width(72).Clicked() {
				a.play(cl)
			}
			a.waveform(c, cl)
		})
		ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
			title := cl.Text
			if latest {
				title = "Latest take"
			}
			ui.Column(c).Grow(1).MinWidth(0).Gap(2).Children(func() {
				if latest {
					ui.Text(c, title).FontSize(13).SingleLine().Ellipsis("…")
				} else {
					ui.Text(c, title).FontSize(13).MaxLines(3).Ellipsis("…")
				}
				ui.Text(c, clipMeta(cl)).FontSize(11).TextColor(t.TextMuted).SingleLine().Ellipsis("…")
			})
			if !latest && ui.Button(c.Key("reuse"), "Reuse").Tooltip("Load this take's text and settings into the studio").Clicked() {
				a.reuse(cl)
			}
			if ui.Button(c.Key("reveal"), "Reveal").Tooltip("Show in Finder").Clicked() {
				mygo.Shell.ShowItemInFolder(a.lib.path(cl))
			}
			if ui.Button(c.Key("export"), "Export…").Clicked() {
				a.export(cl)
			}
			if !latest {
				ui.Button(c.Key("delete"), "Delete").OnClick(func() { a.deleteClip(cl) })
			}
		})
	})
}

func (a *app) waveform(c *ui.Context, cl *clip) {
	t := c.Theme()
	if cl.peaks == nil {
		peaks, err := wavePeaks(a.lib.path(cl), 160)
		if err != nil || peaks == nil {
			peaks = []float32{}
		}
		cl.peaks = peaks
	}
	w := ui.ButtonBase(c.Key("wave")).Grow(1).MinWidth(0).Height(44).Label("Waveform").Tooltip("Click to seek")
	if w.Clicked() {
		if x, _, ok := w.PointerPosition(); ok {
			if a.pl.clipID != cl.ID {
				a.play(cl)
			}
			a.pl.seek(cl.ID, x/max(w.Bounds().W, 1))
		}
	}
	played, muted := t.Accent, t.TextMuted.Alpha(0.45)
	pl := &a.pl
	w.Draw(func(p *ui.Painter, r ui.Rect) {
		n := len(cl.peaks)
		if n == 0 {
			p.Line(r.X, r.Y+r.H/2, r.X+r.W, r.Y+r.H/2, 1, muted)
			return
		}
		progress := pl.progress(cl.ID)
		step := r.W / float32(n)
		bar := max(step*0.6, 1)
		for i, v := range cl.peaks {
			h := max(v*r.H, 2)
			x := r.X + float32(i)*step
			color := muted
			if float32(i)/float32(n) < progress {
				color = played
			}
			p.Fill(ui.Rect{X: x, Y: r.Y + (r.H-h)/2, W: bar, H: h}, color, bar/2)
		}
		if pl.playing(cl.ID) {
			p.AnimationFrame()
		}
	})
}

func clipMeta(cl *clip) string {
	parts := []string{modelLabelOf(cl.Model)}
	if cl.Voice != "" {
		parts = append(parts, cl.Voice)
	}
	if cl.Language != "" {
		parts = append(parts, cl.Language)
	}
	parts = append(parts, fmt.Sprintf("%.1f s", cl.AudioSeconds))
	if rtf := cl.RTF(); rtf > 0 {
		parts = append(parts, fmt.Sprintf("%.1f× realtime", rtf))
	}
	parts = append(parts, cl.CreatedAt.Format("Jan 2 15:04"))
	return strings.Join(parts, " · ")
}

// MARK: History

func (a *app) historyPage(c *ui.Context) {
	t := c.Theme()
	header(c, "History", func() {
		ui.Textf(c, "%d takes", len(a.lib.clips)).FontSize(12).TextColor(t.TextMuted)
		if ui.Button(c.Key("open-folder"), "Open folder").Clicked() {
			mygo.Shell.OpenPath(a.lib.clipsDir())
		}
	})
	clips := a.lib.clips
	ui.List(c, &a.historyList, len(clips)).Grow(1).Padding(10, 0).ItemKey(func(i int) any { return clips[i].ID }).Rows(func(r ui.ListRow) {
		cl := clips[r.Index]
		ui.Column(c).Padding(6, 20).Children(func() {
			a.clipCard(c, cl, false)
		})
	})
	if len(clips) == 0 {
		ui.Column(c).Absolute().Left(0).Top(titleBarHeight).Right(0).Bottom(0).Center().Gap(6).PassThrough().Children(func() {
			ui.Text(c, "No takes yet").FontSize(15).Bold()
			ui.Text(c, "What you generate in the studio shows up here.").TextColor(t.TextMuted)
		})
	}
}

// MARK: Engine

func (a *app) enginePage(c *ui.Context) {
	t := c.Theme()
	header(c, "Engine", func() {
		if ui.Button(c.Key("restart"), "Restart engine").Clicked() {
			a.restartEngine()
		}
	})
	ui.Scroll(c).Grow(1).Children(func() {
		ui.Column(c).Padding(20).Gap(16).MaxWidth(720).Children(func() {
			ui.Text(c, "AudioSmith runs speech models in audiosmith-engine, a helper built on mlx-audio-swift that runs on the Mac's GPU with MLX. Models download from Hugging Face on first use, into the same cache as the Python tools.").
				TextColor(t.TextMuted)
			state := map[engineState]string{engineStarting: "Starting", engineReady: "Ready", engineFailed: "Stopped"}[a.engState]
			pid := ""
			if a.engPID != 0 {
				pid = fmt.Sprint(a.engPID)
			}
			rows := [][2]string{
				{"Status", state},
				{"Version", orDash(a.engVersion)},
				{"Process", orDash(pid)},
				{"Loaded model", orDash(a.loadedModel)},
				{"Engine", orDash(a.eng.Path())},
				{"Takes", a.lib.clipsDir()},
				{"Model cache", hfCacheDir()},
				{"Log", orDash(a.logPath)},
			}
			ui.Grid(c).ColumnTracks(ui.Fixed(120), ui.Fr(1)).GapX(12).GapY(8).Children(func() {
				for _, r := range rows {
					ui.Text(c, r[0]).TextColor(t.TextMuted)
					ui.Text(c, r[1]).Selectable()
				}
			})
			if a.engError != "" {
				ui.Text(c, a.engError).TextColor(t.Danger).Selectable()
			}
			if a.engTail != "" {
				ui.Text(c, a.engTail).Font("monospace").FontSize(11).Padding(10).Radius(8).Background(t.Surface).Selectable()
			}
			ui.Row(c).Gap(8).Children(func() {
				if ui.Button(c.Key("open-log"), "Open log").Disabled(a.logPath == "").Clicked() {
					mygo.Shell.OpenPath(a.logPath)
				}
				if ui.Button(c.Key("open-models"), "Open model cache").Clicked() {
					os.MkdirAll(hfCacheDir(), 0o755)
					mygo.Shell.OpenPath(hfCacheDir())
				}
			})
		})
	})
}

// hfCacheDir is where swift-huggingface keeps downloaded models: the same
// rules as huggingface_hub, so models are shared with the Python tools.
func hfCacheDir() string {
	if d := os.Getenv("HF_HUB_CACHE"); d != "" {
		return d
	}
	if d := os.Getenv("HF_HOME"); d != "" {
		return filepath.Join(d, "hub")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".cache", "huggingface", "hub")
}

// snapped rounds a slider's value to a step, so labels and requests show
// 1.05 rather than 1.0473; the slider moves smoothly in between.
func snapped(e ui.Element, v *float64, step float64) ui.Element {
	if e.Changed() {
		*v = math.Round(*v/step) * step
	}
	return e
}

func orDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}
