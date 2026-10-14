package main

// Settings → Diagnostics and about (M7a): versions and the machine, a
// self-check (library, engine, folders, network, Keychain, ffmpeg,
// permissions), "Copy diagnostics", a diagnostic bundle (a ZIP of the
// logs, the settings without secrets and the diagnostics), and Report a
// bug, which opens a prefilled GitHub issue in the browser.

import (
	"archive/zip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strings"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"github.com/hehehai/AudioSmith/internal/engine/proto"
	"github.com/hehehai/AudioSmith/internal/hub"
	"github.com/hehehai/AudioSmith/internal/i18n"
	"github.com/hehehai/AudioSmith/internal/icons"
	"github.com/hehehai/AudioSmith/internal/settings"
	"github.com/hehehai/AudioSmith/internal/store/sqlitestore"
)

// issuesURL is where bugs are reported.
const issuesURL = "https://github.com/hehehai/AudioSmith/issues/new"

// aboutState is the Diagnostics page's state (and the machine, which the
// first-run wizard reads too).
type aboutState struct {
	sys        *sysInfo
	sysLoading bool

	checks       []selfCheck
	selfChecking bool

	usage        *sqlitestore.Usage
	usageLoading bool
	usageErr     string
}

// selfCheck is one check's outcome.
type selfCheck struct {
	id     string
	title  string
	status string // "pass", "warn", "fail"
	detail string
}

// diagnosticsText describes this install, without anything the user
// wrote or said: for bug reports.
func (a *app) diagnosticsText() string {
	var b strings.Builder
	line := func(k, v string) { fmt.Fprintf(&b, "%s: %s\n", k, orDash(v)) }
	ver := a.version
	if ver == "" {
		ver = "dev"
	}
	line("AudioSmith", ver)
	line("Engine", a.engVersion+" ("+engStateName(a.engState)+")")
	line("Protocol", fmt.Sprint(proto.Version))
	if s := a.sys; s != nil {
		line("System", s.OS)
		line("Chip", s.Chip+" · "+s.Arch+fmt.Sprintf(" · %d cores", s.Cores))
		line("Memory", fmt.Sprintf("%d GB", s.MemoryGB()))
	}
	line("Go", runtime.Version())
	line("MyGo", moduleVersion("github.com/egoist/mygo"))
	line("Data folder", fmt.Sprintf("%s (free %s)", a.dataDir(), humanBytes(diskFree(a.dataDir()))))
	line("Model folder", fmt.Sprintf("%s (free %s)", modelCache(a.prefs), humanBytes(diskFree(modelCache(a.prefs)))))
	if tool := a.mediaTool(); tool != nil {
		line("ffmpeg", tool.Version+" "+tool.Licence+" ("+a.prefs.Media.Origin+")")
	} else {
		line("ffmpeg", "not set up")
	}
	line("Language", i18n.Locale())
	line("Loaded models", strings.Join(a.engineSummary().Loaded, ", "))
	for _, c := range a.checks {
		line("Check "+c.id, c.status+" "+c.detail)
	}
	return b.String()
}

// engStateName names an engine state in English (diagnostics).
func engStateName(s engineState) string {
	switch s {
	case engineReady:
		return "ready"
	case engineFailed:
		return "failed"
	}
	return "starting"
}

// moduleVersion is a dependency's version from the build.
func moduleVersion(path string) string {
	if bi, ok := debug.ReadBuildInfo(); ok {
		for _, m := range bi.Deps {
			if m.Path == path {
				return m.Version
			}
		}
	}
	return ""
}

// runSelfCheck checks what AudioSmith needs, in the background.
func (a *app) runSelfCheck() {
	if a.selfChecking {
		return
	}
	a.selfChecking = true
	st, eng, engErr, engVer := a.lib.store, a.engState, a.engError, a.engVersion
	models, data := modelCache(a.prefs), a.dataDir()
	tool, mediaPath := a.mediaTool(), a.prefs.Media.FFmpeg
	client := hub.NewHTTPClient(a.prefs.Models.Proxy, 10*time.Second)
	perms := a.perms
	if a.dictSys != nil {
		a.recheckPermissions()
		perms = a.perms
	}
	a.background(func() {
		var out []selfCheck
		add := func(id, title, status, detail string) {
			out = append(out, selfCheck{id: id, title: title, status: status, detail: detail})
		}
		// The library.
		if u, err := st.Usage(context.Background()); err != nil {
			add("library", i18n.T("Library database"), "fail", err.Error())
		} else {
			add("library", i18n.T("Library database"), "pass", i18n.N(u.Takes, "%d take", "%d takes", u.Takes))
		}
		// The engine.
		switch eng {
		case engineReady:
			add("engine", i18n.T("Engine"), "pass", i18n.T("Ready (%s)", engVer))
		case engineFailed:
			add("engine", i18n.T("Engine"), "fail", orDash(engErr))
		default:
			add("engine", i18n.T("Engine"), "warn", i18n.T("Starting…"))
		}
		// The folders.
		for _, f := range []struct{ id, title, dir string }{{"models", i18n.T("Model folder"), models}, {"data", i18n.T("Data folder"), data}} {
			os.MkdirAll(f.dir, 0o755)
			tmp, err := os.CreateTemp(f.dir, ".audiosmith-check-*")
			if err != nil {
				add(f.id, f.title, "fail", i18n.T("Cannot write in %s: %v", f.dir, err))
				continue
			}
			tmp.Close()
			os.Remove(tmp.Name())
			free := diskFree(f.dir)
			switch {
			case free > 0 && free < 5<<30:
				add(f.id, f.title, "fail", i18n.T("Only %s free: models and renders need room.", humanBytes(free)))
			case free > 0 && free < 10<<30:
				add(f.id, f.title, "warn", i18n.T("%s free: keep at least 10 GB.", humanBytes(free)))
			default:
				add(f.id, f.title, "pass", i18n.T("Writable · %s free", humanBytes(free)))
			}
		}
		// The network, through the proxy.
		hf, mirror := reach(client, "https://huggingface.co"), reach(client, "https://hf-mirror.com")
		switch {
		case hf.ok:
			add("network", i18n.T("Network"), "pass", i18n.T("huggingface.co answers in %d ms", hf.latency.Milliseconds()))
		case mirror.ok:
			add("network", i18n.T("Network"), "warn", i18n.T("huggingface.co does not answer (%s); hf-mirror.com does: use it in Settings → Network.", hf.err))
		default:
			add("network", i18n.T("Network"), "warn", i18n.T("No download source answers (%s): check the proxy in Settings → Network.", hf.err))
		}
		// The Keychain.
		if runtime.GOOS != "darwin" {
			add("keychain", i18n.T("Keychain"), "warn", i18n.T("Only on macOS"))
		} else if _, err := hub.DefaultKeychain.Get(); err != nil {
			add("keychain", i18n.T("Keychain"), "fail", err.Error())
		} else {
			add("keychain", i18n.T("Keychain"), "pass", i18n.T("Readable"))
		}
		// ffmpeg (optional).
		switch {
		case mediaPath == "":
			add("ffmpeg", "ffmpeg", "pass", i18n.T("Not set up (optional)"))
		case tool == nil:
			add("ffmpeg", "ffmpeg", "warn", i18n.T("%s does not run", mediaPath))
		default:
			add("ffmpeg", "ffmpeg", "pass", tool.Version)
		}
		// Permissions (dictation, recording).
		if perms.checked {
			if t, ok := permissionText(perms.mic); ok {
				add("microphone", i18n.T("Microphone"), "pass", t)
			} else {
				add("microphone", i18n.T("Microphone"), "warn", t)
			}
			if perms.access {
				add("accessibility", i18n.T("Accessibility"), "pass", i18n.T("Allowed"))
			} else {
				add("accessibility", i18n.T("Accessibility"), "warn", i18n.T("Not allowed: dictation copies the text instead of typing it."))
			}
		}
		a.post(func() { a.checks, a.selfChecking = out, false })
	})
}

// redactedSettings is the settings as JSON without anything secret
// (LLM provider headers can carry keys).
func redactedSettings(s settings.Settings) []byte {
	for i := range s.LLM.Providers {
		for k := range s.LLM.Providers[i].Headers {
			s.LLM.Providers[i].Headers[k] = "•••"
		}
	}
	data, _ := json.MarshalIndent(s, "", "  ")
	return data
}

// writeBundle writes the diagnostic bundle (a ZIP) to path.
func writeBundle(path, diag string, prefs settings.Settings, logDir string) error {
	tmp := path + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	zw := zip.NewWriter(f)
	put := func(name string, data []byte) error {
		w, err := zw.Create(name)
		if err != nil {
			return err
		}
		_, err = w.Write(data)
		return err
	}
	err = put("diagnostics.txt", []byte(diag))
	if err == nil {
		err = put("settings.json", redactedSettings(prefs))
	}
	if err == nil && logDir != "" {
		ents, _ := os.ReadDir(logDir)
		for _, e := range ents {
			if e.IsDir() || !strings.Contains(e.Name(), ".log") {
				continue
			}
			src, rerr := os.Open(filepath.Join(logDir, e.Name()))
			if rerr != nil {
				continue
			}
			w, cerr := zw.Create("logs/" + e.Name())
			if cerr == nil {
				_, cerr = io.Copy(w, src)
			}
			src.Close()
			if cerr != nil {
				err = cerr
				break
			}
		}
	}
	if cerr := zw.Close(); err == nil {
		err = cerr
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, path)
}

// bundleDir is where tests save the bundle (no dialog).
var bundleDir string

// saveBundle asks where, then writes the diagnostic bundle.
func (a *app) saveBundle() {
	diag, prefs, logs := a.diagnosticsText(), a.prefs, a.logDir
	name := "AudioSmith diagnostics " + time.Now().Format("2006-01-02 1504") + ".zip"
	write := func(dst string) {
		a.background(func() {
			err := writeBundle(dst, diag, prefs, logs)
			a.post(func() {
				if err != nil {
					a.toast(i18n.T("Could not save the bundle: %v", err))
					return
				}
				a.toast(i18n.T("Saved %s", filepath.Base(dst)))
			})
		})
	}
	if a.win == nil {
		if bundleDir != "" {
			write(filepath.Join(bundleDir, name))
		}
		return
	}
	go func() {
		dst, err := mygo.Dialog.Save(mygo.SaveDialogOptions{Parent: a.win, Title: i18n.T("Save a diagnostic bundle"), DefaultPath: name,
			Filters: []mygo.FileFilter{{Name: "ZIP", Extensions: []string{"zip"}}}})
		if err != nil || dst == "" {
			return
		}
		a.post(func() { write(dst) })
	}()
}

// bugReportURL is a new GitHub issue with the diagnostics filled in.
func (a *app) bugReportURL() string {
	diag := a.diagnosticsText()
	if len(diag) > 5000 {
		diag = diag[:5000]
	}
	body := "### What happened\n\n\n### What you expected\n\n\n### Steps to reproduce\n\n1. \n\n### Diagnostics\n\n```\n" + diag + "```\n"
	q := url.Values{"body": {body}, "labels": {"bug"}}
	return issuesURL + "?" + q.Encode()
}

// aboutSettings is Settings → Diagnostics and about.
func (a *app) aboutSettings(c *ui.Context) {
	t := c.Theme()
	a.loadSysInfo()
	sectionTitle(c, i18n.T("About"))
	ver := a.version
	if ver == "" {
		ver = i18n.T("development build")
	}
	rows := [][2]string{
		{i18n.T("AudioSmith"), ver},
		{i18n.T("Engine"), orDash(a.engVersion)},
		{i18n.T("Engine protocol"), fmt.Sprint(proto.Version)},
	}
	if s := a.sys; s != nil {
		rows = append(rows,
			[2]string{i18n.T("System"), orDash(s.OS)},
			[2]string{i18n.T("Chip"), orDash(s.Chip)},
			[2]string{i18n.T("Memory"), i18n.T("%d GB", s.MemoryGB())},
		)
	}
	if tool := a.mediaTool(); tool != nil {
		rows = append(rows, [2]string{"ffmpeg", tool.Version})
	}
	ui.Grid(c.Key("about-grid")).ColumnTracks(ui.Fixed(px(t, 150)), ui.Fr(1)).GapX(px(t, 12)).GapY(px(t, 6)).Children(func() {
		for _, r := range rows {
			ui.Text(c, r[0]).TextColor(t.TextMuted)
			ui.Text(c, r[1]).Selectable()
		}
	})
	ui.Text(c, i18n.T("AudioSmith runs every model on this Mac. Each model keeps its own licence: see Settings → Models and downloads.")).
		FontSize(px(t, 12)).TextColor(t.TextMuted)
	// 0.3: the icons' attribution (CC BY 4.0; NOTICE has it too).
	ui.Row(c.Key("about-icons")).Gap(px(t, 6)).AlignItems(ui.Center).Wrap().Children(func() {
		caption(c, i18n.T("Icons: Solar by 480 Design, under CC BY 4.0 (@iconify-json/solar %s).", icons.SolarVersion))
		ui.Link(c, i18n.T("Licence"), "https://creativecommons.org/licenses/by/4.0/").FontSize(px(t, fsCaption))
	})

	sectionTitle(c, i18n.T("Self-check"))
	settingRow(c, "about-check", i18n.T("Check AudioSmith"), i18n.T("The library, the engine, the folders, the network, the Keychain, ffmpeg and the permissions."), func() {
		if a.selfChecking {
			ui.Spinner(c)
		}
		if ui.Button(c.Key("about-check"), i18n.T("Run the check")).Disabled(a.selfChecking).Clicked() {
			a.runSelfCheck()
		}
	})
	for _, ch := range a.checks {
		ui.Row(c.Key("check-" + ch.id)).Gap(px(t, 8)).AlignItems(ui.Start).Children(func() {
			mark, col := icons.CheckCircle, t.Success
			switch ch.status {
			case "warn":
				mark, col = icons.DangerTriangle, t.Warning
			case "fail":
				mark, col = icons.CloseCircle, t.Danger
			}
			ui.Icon(c, mark.Linear).Size(px(t, 14), px(t, 14)).TextColor(col).Shrink(0).Margin(px(t, 2), 0, 0)
			ui.Text(c, ch.title).Width(px(t, 150))
			ui.Text(c, ch.detail).FontSize(px(t, 12)).TextColor(t.TextMuted).Grow(1).MinWidth(0)
		})
	}

	sectionTitle(c, i18n.T("Report a problem"))
	settingRow(c, "about-copy", i18n.T("Copy diagnostics"), i18n.T("Versions, the machine, folders and the self-check: no text, audio or keys."), func() {
		if ui.Button(c.Key("about-copy"), i18n.T("Copy")).Clicked() {
			c.WriteClipboard(a.diagnosticsText())
			a.toast(i18n.T("Diagnostics copied"))
		}
	})
	settingRow(c, "about-bundle", i18n.T("Save a diagnostic bundle"), i18n.T("A ZIP of the diagnostics, the logs and the settings (without keys), to attach to a report."), func() {
		if ui.Button(c.Key("about-bundle"), i18n.T("Save…")).Clicked() {
			a.saveBundle()
		}
	})
	settingRow(c, "about-bug", i18n.T("Report a bug"), i18n.T("Opens a new issue on GitHub in your browser, with the diagnostics filled in. Nothing is sent until you submit it."), func() {
		if ui.Button(c.Key("about-bug"), i18n.T("Report…")).Clicked() {
			u := a.bugReportURL()
			if a.win == nil {
				c.Services().OpenURL(u)
			} else {
				go mygo.Shell.OpenExternal(u)
			}
		}
	})
}
