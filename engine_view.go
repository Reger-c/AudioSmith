package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"github.com/hehehai/AudioSmith/internal/catalog"
	"github.com/hehehai/AudioSmith/internal/engine"
	"github.com/hehehai/AudioSmith/internal/engine/proto"
	"github.com/hehehai/AudioSmith/internal/hub"
	"github.com/hehehai/AudioSmith/internal/i18n"
)

func (a *app) enginePage(c *ui.Context) {
	t := c.Theme()
	header(c, i18n.T("Engine"), func() {
		if ui.Button(c.Key("restart"), i18n.T("Restart engine")).Clicked() {
			a.restartEngine()
		}
	})
	ui.Scroll(c).Grow(1).Children(func() {
		ui.Column(c).Padding(20).Gap(16).MaxWidth(720).Children(func() {
			ui.Text(c, i18n.T("AudioSmith runs speech models in audiosmith-engine, a helper built on mlx-audio-swift that runs on the Mac's GPU with MLX. Models download from Hugging Face on first use, into the same cache as the Python tools.")).
				TextColor(t.TextMuted)

			workers := a.workers
			if len(workers) == 0 {
				workers = []engine.WorkerStatus{{Role: proto.RoleGPU, State: engine.WorkerStarting}}
			}
			for _, w := range workers {
				a.workerCard(c, w)
			}

			rows := [][2]string{
				{i18n.T("Engine"), orDash(a.eng.Path())},
				{i18n.T("Takes"), a.takeStore.AudioDir()},
				{i18n.T("Model cache"), hub.CacheDir()},
				{i18n.T("Log"), orDash(a.logPath)},
			}
			infoGrid(c, rows)
			ui.Row(c).Gap(8).Children(func() {
				if ui.Button(c.Key("open-log"), i18n.T("Open log")).Disabled(a.logPath == "").Clicked() {
					mygo.Shell.OpenPath(a.logPath)
				}
				if ui.Button(c.Key("open-models"), i18n.T("Open model cache")).Clicked() {
					os.MkdirAll(hub.CacheDir(), 0o755)
					mygo.Shell.OpenPath(hub.CacheDir())
				}
			})
		})
	})
}

// workerCard shows one engine process: its state, models, memory and jobs,
// and why it stopped.
func (a *app) workerCard(c *ui.Context, w engine.WorkerStatus) {
	t := c.Theme()
	ui.Column(c.Key("worker-"+string(w.Role))).Gap(10).Padding(14).Radius(10).Border(1, t.Border).Background(t.Surface).Children(func() {
		color, state := t.Warning, i18n.T("Starting")
		switch w.State {
		case engine.WorkerReady:
			color, state = t.Success, i18n.T("Ready")
		case engine.WorkerFailed, engine.WorkerStopped:
			color, state = t.Danger, i18n.T("Stopped")
		}
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Box(c).Size(8, 8).Radius(4).Background(color)
			ui.Text(c, roleTitle(w.Role)).Bold()
			ui.Text(c, state).TextColor(t.TextMuted)
		})
		var rows [][2]string
		if w.Hello != nil {
			rows = append(rows, [2]string{i18n.T("Version"), w.Hello.Version})
			if d := w.Hello.Device; d.Name != "" {
				rows = append(rows, [2]string{i18n.T("Device"), fmt.Sprintf("%s · %s · %s", d.Name, catalog.HumanSize(d.Memory), d.OS)})
			}
		}
		if w.PID != 0 {
			rows = append(rows, [2]string{i18n.T("Process"), fmt.Sprint(w.PID)})
		}
		var models []string
		if w.Stats != nil {
			for _, m := range w.Stats.Models {
				name := catalog.Builtin().DisplayName(m.Model)
				if m.Bytes > 0 {
					name += " · " + catalog.HumanSize(m.Bytes)
				}
				models = append(models, name)
			}
			rows = append(rows, [2]string{i18n.T("Memory"), i18n.T("%s in use · %s at most",
				catalog.HumanSize(w.Stats.Memory.Active), catalog.HumanSize(w.Stats.Memory.Peak))})
		}
		if w.Stats == nil && w.Role == proto.RoleGPU {
			for _, m := range a.engineSummary().Loaded {
				models = append(models, catalog.Builtin().DisplayName(m))
			}
		}
		if w.State == engine.WorkerReady {
			loaded := i18n.T("No model loaded")
			if len(models) > 0 {
				loaded = strings.Join(models, "\n")
			}
			rows = append(rows, [2]string{i18n.T("Loaded models"), loaded})
		}
		if len(w.Running) > 0 {
			rows = append(rows, [2]string{i18n.T("Jobs"), strings.Join(w.Running, "\n")})
		}
		if w.Restarts > 0 {
			rows = append(rows, [2]string{i18n.T("Restarts"), fmt.Sprint(w.Restarts)})
		}
		if len(rows) > 0 {
			infoGrid(c, rows)
		}
		if msg := failureText(w); msg != "" {
			ui.Text(c, msg).TextColor(t.Danger).Selectable()
		}
		if w.StderrTail != "" && w.State == engine.WorkerFailed {
			ui.Text(c, w.StderrTail).Font("monospace").FontSize(11).Padding(10).Radius(8).Background(t.Background).Selectable()
		}
	})
}

func infoGrid(c *ui.Context, rows [][2]string) {
	t := c.Theme()
	ui.Grid(c).ColumnTracks(ui.Fixed(120), ui.Fr(1)).GapX(12).GapY(8).Children(func() {
		for _, r := range rows {
			ui.Text(c, r[0]).TextColor(t.TextMuted)
			ui.Text(c, r[1]).Selectable()
		}
	})
}

// roleTitle names an engine process for people.
func roleTitle(r proto.Role) string {
	switch r {
	case proto.RoleListen:
		return i18n.T("Dictation engine")
	case proto.RoleMedia:
		return i18n.T("Media engine")
	}
	return i18n.T("Speech engine")
}
