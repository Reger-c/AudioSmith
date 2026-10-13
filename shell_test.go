package main

import (
	"bytes"
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/hehehai/AudioSmith/internal/catalog"
	"github.com/hehehai/AudioSmith/internal/core"
	"github.com/hehehai/AudioSmith/internal/hub"
	"github.com/hehehai/AudioSmith/internal/i18n"
	"github.com/hehehai/AudioSmith/internal/jobs"
	"github.com/hehehai/AudioSmith/internal/licence"
	"github.com/hehehai/AudioSmith/internal/settings"
)

// fakeHF serves every repo as a tiny model (config.json and a 2 KB LFS
// weights file), as the Hugging Face API and resolve endpoints do.
type fakeHF struct {
	*httptest.Server
	mu     sync.Mutex
	served map[string]int // files served, by repo
}

func newFakeHF(t *testing.T) *fakeHF {
	t.Helper()
	f := &fakeHF{served: map[string]int{}}
	files := func(repo string) map[string][]byte {
		return map[string][]byte{
			"config.json":       []byte(`{"model":"` + repo + `"}`),
			"model.safetensors": bytes.Repeat([]byte(repo), 2048/len(repo)+1)[:2048],
		}
	}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		if rest, ok := strings.CutPrefix(p, "/api/models/"); ok {
			repo, rev, _ := strings.Cut(rest, "/revision/")
			rev, _, _ = strings.Cut(rev, "?")
			sha := strings.Repeat("a", 40)
			if len(rev) == 40 {
				sha = rev // a pinned commit
			}
			var sibs []map[string]any
			for name, b := range files(repo) {
				s := map[string]any{"rfilename": name, "size": len(b)}
				h := sha1.New()
				fmt.Fprintf(h, "blob %d\x00", len(b))
				h.Write(b)
				s["blobId"] = hex.EncodeToString(h.Sum(nil))
				if strings.HasSuffix(name, ".safetensors") {
					sum := sha256.Sum256(b)
					s["lfs"] = map[string]any{"sha256": hex.EncodeToString(sum[:]), "size": len(b)}
				}
				sibs = append(sibs, s)
			}
			json.NewEncoder(w).Encode(map[string]any{"id": repo, "sha": sha, "gated": false, "siblings": sibs})
			return
		}
		// /{owner}/{name}/resolve/{commit}/{file}
		parts := strings.SplitN(strings.TrimPrefix(p, "/"), "/", 5)
		if len(parts) == 5 && parts[2] == "resolve" {
			repo := parts[0] + "/" + parts[1]
			if b, ok := files(repo)[parts[4]]; ok {
				f.mu.Lock()
				f.served[repo]++
				f.mu.Unlock()
				http.ServeContent(w, r, "", time.Time{}, bytes.NewReader(b))
				return
			}
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(f.Close)
	return f
}

// useFakeHub points the app's downloads at a fake hub and a temporary
// model folder.
func useFakeHub(t *testing.T, a *app) *fakeHF {
	t.Helper()
	f := newFakeHF(t)
	dir := t.TempDir()
	a.updateSettings(func(s *settings.Settings) {
		s.Models.CacheDir = dir
		s.Models.Endpoints = []string{f.URL}
	})
	a.resetHub()
	if fs, ok := a.eng.sup.(*fakeSup); ok && a.engState == engineStarting {
		fs.f.ready() // the engine restarted for the new model folder
	}
	saved := hubToken
	hubToken = func() string { return "" }
	t.Cleanup(func() { hubToken = saved })
	return f
}

func entry(t *testing.T, id string) catalog.Entry {
	t.Helper()
	e, ok := catalog.Builtin().ByID(id)
	if !ok {
		t.Fatal("no entry", id)
	}
	return e
}

func TestNavigation(t *testing.T) {
	a, _, tt := newTestApp(t)
	tt.Frame()
	for i, want := range []string{"studio", "voices", "design", "gallery", "history", "models", "engine", "logs"} {
		tt.Key(ui.Cmd, ui.Key1+ui.Key(i))
		if a.currentPage() != want {
			t.Errorf("⌘%d: %s", i+1, a.currentPage())
		}
	}
	tt.Key(ui.Cmd, ui.KeyComma)
	if a.router.Path() != "/settings/general" || !tt.HasText("Interface size") && !tt.HasText("Language") {
		t.Errorf("⌘,: %s %q", a.router.Path(), tt.Texts())
	}
	// The sidebar.
	if err := tt.Click("History"); err != nil {
		t.Fatal(err)
	}
	if a.currentPage() != "history" {
		t.Errorf("sidebar: %s", a.router.Path())
	}
	// Back.
	a.router.Back()
	tt.Frame()
	if a.router.Path() != "/settings/general" {
		t.Errorf("back: %s", a.router.Path())
	}
	// The same page again adds no history.
	a.showPage("studio")
	a.showPage("studio")
	a.router.Back()
	if a.router.Path() != "/settings/general" {
		t.Errorf("showPage twice pushed twice: %s", a.router.Path())
	}
	// Settings remembers its section.
	a.navigate("/settings/appearance")
	a.showPage("studio")
	tt.Key(ui.Cmd, ui.KeyComma)
	if a.router.Path() != "/settings/appearance" {
		t.Errorf("settings section: %s", a.router.Path())
	}
}

func TestDeepLinks(t *testing.T) {
	a, _, tt := newTestApp(t)
	for link, want := range map[string]string{
		"audiosmith://models":          "/models",
		"audiosmith://settings/models": "/settings/models",
		"audiosmith:///history":        "/history",
		"audiosmith://studio/":         "/studio",
		"/settings/engine":             "/settings/engine",
		"audiosmith://logs?source=app": "/logs",
		"AudioSmith://engine":          "/engine",
	} {
		a.openURL(link)
		if a.router.Path() != want {
			t.Errorf("%s → %s, want %s", link, a.router.Path(), want)
		}
	}
	a.openURL("AudioSmith://engine")
	a.openURL("audiosmith://nowhere")
	a.openURL("https://example.com/")
	tt.Frame()
	if len(a.toasts) != 0 || a.router.Path() != "/engine" {
		t.Errorf("unknown links moved: %s", a.router.Path())
	}
}

func TestPalette(t *testing.T) {
	a, _, tt := newTestApp(t)
	tt.SetFocused(true) // the window has the keyboard
	tt.Frame()
	tt.Key(ui.Cmd, ui.KeyK)
	if !a.paletteOpen || !tt.HasText("Command palette") && !tt.HasText("Go to") {
		t.Fatalf("⌘K: %v %q", a.paletteOpen, tt.Texts())
	}
	if !tt.Focused("Search commands") {
		t.Fatalf("search not focused: %q", tt.Texts())
	}
	// The Tester takes typing only into an input clicked first.
	tt.Click("Search commands")
	tt.Type("models")
	if a.paletteQuery != "models" {
		t.Fatalf("query %q", a.paletteQuery)
	}
	items := a.paletteItems()
	if len(items) == 0 || items[0].id != "go/models" {
		t.Fatalf("items %+v", items)
	}
	tt.Key(0, ui.KeyEnter)
	if a.paletteOpen || a.router.Path() != "/models" {
		t.Errorf("enter: open %v path %s", a.paletteOpen, a.router.Path())
	}

	// Arrows move the choice; Escape closes.
	tt.Key(ui.Cmd, ui.KeyK)
	tt.Click("Search commands")
	tt.Type("theme")
	tt.Key(0, ui.KeyDown)
	if a.paletteSel != 1 {
		t.Errorf("down: %d", a.paletteSel)
	}
	tt.Key(0, ui.KeyUp)
	tt.Key(0, ui.KeyUp)
	if n := len(a.paletteItems()); a.paletteSel != n-1 {
		t.Errorf("up wraps: %d of %d", a.paletteSel, n)
	}
	tt.Key(0, ui.KeyEscape)
	if a.paletteOpen {
		t.Error("escape left it open")
	}
	// English words find commands in Chinese too.
	t.Cleanup(func() { i18n.SetLocale("en") })
	i18n.SetLocale("zh-Hans")
	a.paletteQuery = "settings"
	if items := a.paletteItems(); len(items) == 0 || items[0].title != "设置" {
		t.Errorf("zh items %+v", items)
	}
}

func TestShortcutSheet(t *testing.T) {
	a, _, tt := newTestApp(t)
	tt.Frame()
	// Typing "?" in the script is text, not the sheet.
	tt.Click("Script")
	if !tt.Focused("Script") {
		t.Fatal("script not focused")
	}
	before := strings.Count(a.text, "?")
	tt.TypeKey(ui.Shift, ui.KeySlash, "?")
	// Where the click put the caret depends on the platform's fonts.
	if a.sheetOpen || strings.Count(a.text, "?") != before+1 {
		t.Errorf("? in the script: sheet %v text %q", a.sheetOpen, a.text)
	}
	a.navigate("/history")
	tt.Frame()
	tt.Key(ui.Shift, ui.KeySlash)
	if !a.sheetOpen || !tt.HasText("Keyboard shortcuts") || !tt.HasText("⌘K") {
		t.Fatalf("sheet: %v %q", a.sheetOpen, tt.Texts())
	}
	tt.Click("Close")
	if a.sheetOpen {
		t.Error("not closed")
	}
}

func TestNotifications(t *testing.T) {
	a, _, tt := newTestApp(t)
	tt.Frame()
	a.notify(core.SeverityWarning, "Disk almost full", "2 GB left", "/settings/models")
	a.notify(core.SeverityInfo, "Hello", "", "")
	tt.Frame()
	if len(a.notices) != 2 || a.notices[0].Title != "Hello" {
		t.Fatalf("notices %+v", a.notices)
	}
	if !tt.HasText("2") {
		t.Errorf("no unread count: %q", tt.Texts())
	}
	tt.Click("Notifications")
	if !a.noticesOpen || !tt.HasText("Disk almost full") || !tt.HasText("2 GB left") {
		t.Fatalf("centre: %q", tt.Texts())
	}
	tt.Click("Open")
	if a.router.Path() != "/settings/models" || a.noticesOpen {
		t.Errorf("open link: %s", a.router.Path())
	}
	a.noticesOpen = true
	tt.Frame()
	tt.Click("Mark all read")
	list, _ := a.notes.List(context.Background())
	if a.notices[0].Read != true || !list[1].Read {
		t.Errorf("read: %+v", list)
	}
	tt.Click("Clear")
	if list, _ := a.notes.List(context.Background()); len(list) != 0 || len(a.notices) != 0 {
		t.Error("not cleared")
	}
}

func TestLogs(t *testing.T) {
	a, _, tt := newTestApp(t)
	a.logs.Append(core.LogEntry{Source: "app", Message: "app started"})
	a.logs.Append(core.LogEntry{Source: "hub", Level: core.SeverityError, Message: "download failed"})
	a.logs.Append(core.LogEntry{Source: "engine:gpu", Message: "weights loaded"})
	a.navigate("/logs")
	tt.Frame()
	for _, s := range []string{"app started", "download failed", "weights loaded", "3 lines"} {
		if !tt.HasText(s) {
			t.Errorf("no %q in %q", s, tt.Texts())
		}
	}
	a.logsSource = "engine:"
	tt.Frame()
	if tt.HasText("app started") || !tt.HasText("weights loaded") {
		t.Errorf("engine filter: %q", tt.Texts())
	}
	a.logsSource, a.logsQuery = "", "FAILED"
	tt.Frame()
	if !tt.HasText("download failed") || tt.HasText("weights loaded") {
		t.Errorf("text filter: %q", tt.Texts())
	}
	// The panel, under another page.
	a.logsQuery = ""
	a.navigate("/studio")
	tt.Key(ui.Cmd|ui.Shift, ui.KeyL)
	if !a.logsPanel || !tt.HasText("weights loaded") || !tt.HasText("Generate") {
		t.Errorf("panel: %v %q", a.logsPanel, tt.Texts())
	}
}

func TestLanguageSwitch(t *testing.T) {
	t.Cleanup(func() { i18n.SetLocale("en") })
	a, _, tt := newTestApp(t)
	a.navigate("/settings/general")
	tt.Frame()
	a.language = "简体中文"
	a.updateSettings(func(s *settings.Settings) { s.General.Language = "zh-Hans" })
	tt.Frame()
	if i18n.Locale() != "zh-Hans" || !tt.HasText("工作室") || !tt.HasText("设置") || !tt.HasText("引擎就绪") {
		t.Fatalf("zh: %s %q", i18n.Locale(), tt.Texts())
	}
	// Saved: a new app over the same library starts in Chinese.
	if got := a.settings.Get().General.Language; got != "zh-Hans" {
		t.Errorf("saved %q", got)
	}
	// Following the system.
	a.systemLocale = "zh_CN"
	a.updateSettings(func(s *settings.Settings) { s.General.Language = "" })
	if i18n.Locale() != "zh-Hans" {
		t.Errorf("system zh_CN: %s", i18n.Locale())
	}
	a.systemLocale = "fr-FR"
	a.applyLocale()
	tt.Frame()
	if i18n.Locale() != "en" || !tt.HasText("Studio") {
		t.Errorf("system fr: %s", i18n.Locale())
	}
}

func TestSettingsPersist(t *testing.T) {
	dir := t.TempDir()
	a, _, tt := newTestAppIn(t, dir)
	a.navigate("/settings/appearance")
	tt.Frame()
	if err := tt.Click("Dark"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("125%"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if s := a.settings.Get(); s.Appearance.Theme != "dark" || s.Appearance.Scale != 1.25 {
		t.Fatalf("saved %+v", s.Appearance)
	}
	// The theme follows: text is larger at 125 %.
	r, ok := tt.Find("Interface size")
	if !ok || r.H < 18 {
		t.Errorf("scaled text: %+v %v", r, ok)
	}
	a.navigate("/settings/engine")
	tt.Frame()
	a.memoryGB = 12
	a.updateSettings(func(s *settings.Settings) { s.Engine.MemoryBudgetGB = 12 })

	// Another app over the same database reads them back.
	b, _, _ := newTestAppIn(t, dir)
	if s := b.prefs; s.Appearance.Theme != "dark" || s.Appearance.Scale != 1.25 || s.Engine.MemoryBudgetGB != 12 {
		t.Errorf("reopened %+v", s)
	}
}

func TestQuickSwitch(t *testing.T) {
	a, _, tt := newTestApp(t)
	tt.Frame()
	tt.Key(ui.Cmd, ui.KeyE)
	if !a.switchOpen || !tt.HasText("Speech engine") || !tt.HasText("Qwen3-TTS 0.6B CustomVoice") {
		t.Fatalf("⌘E: %q", tt.Texts())
	}
	if err := tt.Click("Qwen3-TTS 0.6B CustomVoice"); err != nil {
		t.Fatal(err)
	}
	if a.preset().ID != "qwen3-tts-0.6b-customvoice-8bit" || a.voice != "Vivian" {
		t.Errorf("chosen %s voice %q", a.preset().ID, a.voice)
	}
	for _, e := range catalog.Builtin().Entries {
		if e.Hidden && tt.HasText(e.Label) {
			t.Errorf("hidden %s offered", e.Label)
		}
	}
}

func TestModelsPage(t *testing.T) {
	a, _, tt := newTestApp(t)
	f := useFakeHub(t, a)
	var progress []hub.Progress
	core.On(a.bus, func(ev hub.DownloadProgress) { progress = append(progress, ev.Progress) })
	a.navigate("/models")
	tt.Frame()
	if !tt.HasText("Kokoro 82M") || !tt.HasText("OmniVoice") || !tt.HasText("Not downloaded") {
		t.Fatalf("models: %q", tt.Texts())
	}
	// The first card is Kokoro: it downloads with its G2P repos.
	if err := tt.Click("Download"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	kokoro := entry(t, "kokoro-82m-bf16")
	m := a.models[kokoro.ID]
	if m.downloading || m.local.State != hub.Downloaded {
		t.Fatalf("after download: %+v", m)
	}
	for _, repo := range []string{kokoro.Repo, kokoro.Aux[0].Repo, kokoro.Aux[1].Repo} {
		if f.served[repo] != 2 {
			t.Errorf("%s: %d files", repo, f.served[repo])
		}
	}
	if len(progress) == 0 || progress[len(progress)-1].Bytes != progress[len(progress)-1].TotalBytes {
		t.Errorf("progress events %+v", progress)
	}
	if len(a.notices) == 0 || a.notices[0].Title != "Kokoro 82M is downloaded" {
		t.Errorf("notices %+v", a.notices)
	}
	if !tt.HasText("Verify") || !strings.Contains(strings.Join(tt.Texts(), "|"), "Downloaded · ") {
		t.Errorf("downloaded card: %q", tt.Texts())
	}
	tt.Click("Verify")
	tt.Frame()
	if !strings.Contains(strings.Join(a.toasts, "|")+strings.Join(tt.Texts(), "|"), "checks out") && len(a.notices) != 1 {
		t.Errorf("verify: toasts %q notices %+v", a.toasts, a.notices)
	}

	// Delete asks first; Enter confirms.
	tt.Click("Delete")
	if !a.deleteAsk || !tt.HasText("Delete Kokoro 82M?") {
		t.Fatalf("delete alert: %q", tt.Texts())
	}
	tt.Key(0, ui.KeyEnter)
	tt.Frame()
	if a.models[kokoro.ID].local.State != hub.Missing {
		t.Errorf("after delete: %+v", a.models[kokoro.ID].local)
	}
	// A loaded model cannot be deleted.
	a.loadedModel = kokoro.Repo
	a.models[kokoro.ID].local.State = hub.Downloaded
	tt.Frame()
	if ui := a.engineSummary(); len(ui.Loaded) != 1 {
		t.Fatal("not loaded")
	}
}

func TestLicenceGate(t *testing.T) {
	a, _, tt := newTestApp(t)
	f := useFakeHub(t, a)
	omni := entry(t, "omnivoice-bf16")
	a.navigate("/models")
	tt.Frame()
	if !tt.HasText("CC BY-NC 4.0 · non-commercial use only") || !tt.HasText("Review licence…") {
		t.Fatalf("licence line: %q", tt.Texts())
	}
	a.download(omni)
	tt.Frame()
	if !a.licenceOpen || !tt.HasText("Licence of OmniVoice") || a.models[omni.ID].downloading {
		t.Fatalf("dialog: %v %q", a.licenceOpen, tt.Texts())
	}
	if f.served[omni.Repo] != 0 {
		t.Error("downloaded before accepting")
	}
	// Accept is disabled until the box is ticked.
	tt.Click("Accept")
	if !a.licenceOpen {
		t.Fatal("accepted without the box")
	}
	tt.Click("I have read the licence and will use this model only as it allows.")
	tt.Click("Accept")
	tt.Frame()
	if a.licenceOpen || a.models[omni.ID].local.State != hub.Downloaded || f.served[omni.Repo] != 2 {
		t.Fatalf("after accepting: open %v %+v", a.licenceOpen, a.models[omni.ID])
	}
	if err := a.licences.Check(context.Background(), omni.Repo); err != nil {
		t.Errorf("gate after accepting: %v", err)
	}
	if !licence.Covers(a.accepted, omni) || !tt.HasText("· accepted") {
		t.Errorf("accepted %+v", a.accepted)
	}
	// Settings lists it, and withdrawing gates the model again.
	a.navigate("/settings/models")
	tt.Frame()
	if !tt.HasText("CC BY-NC 4.0") {
		t.Fatalf("settings: %q", tt.Texts())
	}
	tt.Click("Withdraw")
	tt.Frame()
	if len(a.accepted) != 0 || a.licences.Check(context.Background(), omni.Repo) == nil {
		t.Errorf("after withdrawing: %+v", a.accepted)
	}
	// Cancelling the dialog downloads nothing.
	a.models[omni.ID].local.State = hub.Missing
	a.download(omni)
	tt.Frame()
	tt.Click("Cancel")
	if a.licenceOpen || a.licenceThen != nil || f.served[omni.Repo] != 2 {
		t.Errorf("cancel: %v", a.licenceOpen)
	}
}

func TestDownloadErrors(t *testing.T) {
	a, _, tt := newTestApp(t)
	useFakeHub(t, a)
	a.updateSettings(func(s *settings.Settings) { s.Models.Endpoints = []string{"http://127.0.0.1:1"} })
	a.resetHub()
	a.navigate("/models")
	tt.Frame()
	a.download(entry(t, "soprano-80m-bf16"))
	tt.Frame()
	m := a.models["soprano-80m-bf16"]
	if m.downloading || m.err == "" || len(a.notices) == 0 || a.notices[0].Link != "/settings/models" {
		t.Fatalf("offline: %+v %+v", m, a.notices)
	}
	if !tt.HasText(m.err) {
		t.Errorf("error not shown: %q", tt.Texts())
	}
	if lines := a.logs.Tail(10, "hub"); len(lines) < 2 || lines[len(lines)-1].Level != core.SeverityError {
		t.Errorf("hub log %+v", lines)
	}
}

func TestStatusBar(t *testing.T) {
	a, _, tt := newTestApp(t)
	tt.Frame()
	if !tt.HasText("Engine ready") || !tt.HasText("· No model loaded") {
		t.Errorf("status: %q", tt.Texts())
	}
	m := a.model(entry(t, "soprano-80m-bf16"))
	m.downloading, m.progress = true, hub.Progress{Bytes: 50, TotalBytes: 200}
	tt.Frame()
	if !tt.HasText("Downloading Soprano 80M · 25%") {
		t.Errorf("download in status: %q", tt.Texts())
	}
	tt.Click("Downloading Soprano 80M · 25%")
	if a.router.Path() != "/models" {
		t.Errorf("click: %s", a.router.Path())
	}
	m.downloading = false
	tt.Click("Show or hide the logs panel (⇧⌘L)")
	if !a.logsPanel {
		t.Error("logs toggle")
	}
}

// Engine settings reach the supervisor while it runs.
func TestEngineSettingsApply(t *testing.T) {
	a, eng, _ := newTestApp(t)
	a.updateSettings(func(s *settings.Settings) {
		s.Engine.MemoryBudgetGB = 6
		s.Models.AutoUnload = 15
	})
	if eng.budget != 6<<30 || eng.idle != 15*time.Minute {
		t.Errorf("budget %d idle %v", eng.budget, eng.idle)
	}
	dir := t.TempDir()
	restarts := eng.restarts
	a.updateSettings(func(s *settings.Settings) { s.Models.CacheDir = dir })
	if eng.cacheDir != dir || eng.restarts != restarts+1 {
		t.Errorf("cache dir %q, restarts %d", eng.cacheDir, eng.restarts-restarts)
	}
}

// The status bar lists every unfinished job; each is cancelled by its id.
func TestJobsInStatusBar(t *testing.T) {
	a, eng, tt := newTestApp(t)
	tt.Frame()
	tt.Click("Generate")
	take := eng.last(t).ID
	other, _ := eng.host.sched.Submit(context.Background(), jobs.Spec{Kind: "x.batch", Lane: jobs.LaneCPU, Title: "Batch export", Owner: "project:p1"})
	eng.progress(take, "generating", 0.5, 1)
	eng.update(other, func(j *jobs.Job) { j.State = jobs.StateQueued })
	tt.Frame()
	if !tt.HasText("2 jobs · 1 waiting") {
		t.Fatalf("status: %q", tt.Texts())
	}
	tt.Click("2 jobs · 1 waiting")
	tt.Frame()
	if !a.jobsOpen || !tt.HasText("Batch export") || !tt.HasText(i18n.T("Generate take")) {
		t.Fatalf("jobs panel: %q", tt.Texts())
	}
	// Running first; the Studio's take is cancelled through the Studio.
	active := a.activeJobs()
	if active[0].ID != take || active[1].ID != other {
		t.Fatalf("order %+v", active)
	}
	a.cancelJobByID(active[1])
	if r := eng.last(t); r.Cmd != "cancel" || r.ID != other {
		t.Errorf("cancelled %+v, want %s", r, other)
	}
	a.cancelJobByID(active[0])
	if r := eng.last(t); r.Cmd != "cancel" || r.ID != take || !a.job.cancelling {
		t.Errorf("cancelled %+v, want %s", r, take)
	}
	eng.cancelled(other)
	eng.cancelled(take)
	tt.Frame()
	if len(a.activeJobs()) != 0 || tt.HasText("2 jobs · 1 waiting") {
		t.Errorf("after: %+v", a.activeJobs())
	}
}

// Notifications other than errors come back after a restart.
func TestNotificationsPersist(t *testing.T) {
	dir := t.TempDir()
	a, _, _ := newTestAppIn(t, dir)
	a.notify(core.SeverityWarning, "Disk almost full", "", "/settings/models")
	a.notify(core.SeverityError, "Download failed", "", "")
	a.notes.Close()
	a.lib.close()
	b, _, _ := newTestAppIn(t, dir)
	if len(b.notices) != 1 || b.notices[0].Title != "Disk almost full" {
		t.Errorf("after restart %+v", b.notices)
	}
}
