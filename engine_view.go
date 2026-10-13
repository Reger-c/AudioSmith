package main

import (
	"fmt"
	"os"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
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
			state := map[engineState]string{engineStarting: i18n.T("Starting"), engineReady: i18n.T("Ready"), engineFailed: i18n.T("Stopped")}[a.engState]
			pid := ""
			if a.engPID != 0 {
				pid = fmt.Sprint(a.engPID)
			}
			rows := [][2]string{
				{i18n.T("Status"), state},
				{i18n.T("Version"), orDash(a.engVersion)},
				{i18n.T("Process"), orDash(pid)},
				{i18n.T("Loaded model"), orDash(a.loadedModel)},
				{i18n.T("Engine"), orDash(a.eng.Path())},
				{i18n.T("Takes"), a.takeStore.AudioDir()},
				{i18n.T("Model cache"), hub.CacheDir()},
				{i18n.T("Log"), orDash(a.logPath)},
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
