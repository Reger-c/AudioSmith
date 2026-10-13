package main

// Logs: the newest lines of the app, the downloads and (once the
// supervisor feeds them) the engine workers, as a page (/logs) and as a
// panel under any page, filtered by source and text.

import (
	"strings"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"github.com/hehehai/AudioSmith/internal/core"
	"github.com/hehehai/AudioSmith/internal/i18n"
	"github.com/hehehai/AudioSmith/internal/logs"
)

// logLines is how many lines the logs view shows at most.
const logLines = 500

func (a *app) logsPage(c *ui.Context) {
	header(c, i18n.T("Logs"), nil)
	a.logsView(c, false)
}

// logSources are the filters of the logs view: "" every source.
var logSources = []string{"", "app", "engine:", "hub"}

func logSourceLabel(s string) string {
	switch s {
	case "":
		return i18n.T("All sources")
	case "app":
		return i18n.T("App")
	case "engine:":
		return i18n.T("Engine")
	case "hub":
		return i18n.T("Downloads")
	}
	return s
}

// visibleLogs are the lines the filters select, oldest first.
func (a *app) visibleLogs() []core.LogEntry {
	lines := a.logs.Tail(logLines*4, a.logsSource)
	q := strings.ToLower(strings.TrimSpace(a.logsQuery))
	var out []core.LogEntry
	for _, e := range lines {
		if q == "" || strings.Contains(strings.ToLower(e.Message), q) || strings.Contains(strings.ToLower(e.Source), q) {
			out = append(out, e)
		}
	}
	if len(out) > logLines {
		out = out[len(out)-logLines:]
	}
	return out
}

// logsView shows the log; panel is the compact form under a page.
func (a *app) logsView(c *ui.Context, panel bool) {
	t := c.Theme()
	lines := a.visibleLogs()
	ui.Row(c).Padding(px(t, 6), px(t, 12)).Gap(px(t, 6)).AlignItems(ui.Center).Shrink(0).Children(func() {
		if panel {
			ui.Text(c, i18n.T("Logs")).Bold().FontSize(px(t, 12))
		}
		var labels []string
		for _, s := range logSources {
			labels = append(labels, logSourceLabel(s))
		}
		chosen := logSourceLabel(a.logsSource)
		sel := ui.Select(c.Key("logs-source"), &chosen, labels).Label(i18n.T("Source"))
		if sel.Changed() {
			for _, s := range logSources {
				if logSourceLabel(s) == chosen {
					a.logsSource = s
				}
			}
		}
		ui.SearchField(c.Key("logs-search"), &a.logsQuery).Placeholder(i18n.T("Filter")).Width(px(t, 200)).Label(i18n.T("Filter the logs"))
		ui.Spacer(c)
		ui.Text(c, i18n.N(len(lines), "%d line", "%d lines", len(lines))).FontSize(px(t, 11)).TextColor(t.TextMuted)
		if ui.Button(c.Key("logs-copy"), i18n.T("Copy")).Disabled(len(lines) == 0).Tooltip(i18n.T("Copy the lines shown")).Clicked() {
			var b strings.Builder
			for _, e := range lines {
				b.WriteString(logs.Format(e) + "\n")
			}
			mygoClipboard(c, b.String())
			a.toast(i18n.T("Copied %d lines", len(lines)))
		}
		if path := a.logs.Path(); path != "" {
			if ui.Button(c.Key("logs-open"), i18n.T("Open log file")).Clicked() {
				mygo.Shell.OpenPath(path)
			}
		}
		if panel {
			if ui.Button(c.Key("logs-close"), i18n.T("Close")).Clicked() {
				a.logsPanel = false
			}
		}
	})
	ui.Divider(c)
	if len(lines) == 0 {
		ui.Column(c).Grow(1).Center().Children(func() {
			ui.Text(c, i18n.T("No log lines yet.")).TextColor(t.TextMuted)
		})
		return
	}
	ui.Scroll(c.Key("logs-scroll")).Grow(1).MinHeight(0).Children(func() {
		ui.Column(c).Padding(px(t, 6), px(t, 12)).Gap(px(t, 1)).Children(func() {
			for i, e := range lines {
				color := t.Text
				switch e.Level {
				case core.SeverityError:
					color = t.Danger
				case core.SeverityWarning:
					color = t.Warning
				case core.SeverityDebug:
					color = t.TextMuted
				}
				ui.Row(c.Key(i)).Gap(px(t, 8)).Children(func() {
					ui.Text(c, e.Time.Format("15:04:05")).Font("monospace").FontSize(px(t, 11)).TextColor(t.TextMuted).Shrink(0)
					ui.Text(c, e.Source).Font("monospace").FontSize(px(t, 11)).TextColor(t.TextMuted).Width(px(t, 80)).Shrink(0).SingleLine().Ellipsis("…")
					ui.Text(c, e.Message).Font("monospace").FontSize(px(t, 11)).TextColor(color).Grow(1).MinWidth(0).Selectable()
				})
			}
		})
	})
}

// mygoClipboard copies text through the window's services.
func mygoClipboard(c *ui.Context, text string) {
	c.Services().WriteClipboard(text)
}
