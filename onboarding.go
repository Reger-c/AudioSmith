package main

// The first-run wizard (M7a; VoiceStudio's four steps, without a Python
// runtime to install): 1 this Mac (macOS, chip, memory, disk, network,
// engine, ffmpeg, permissions), 2 a model pack chosen by memory (Fast,
// Balanced, Quality, Max: catalogue ids, downloaded as jobs), 3 privacy,
// 4 dictation; then the first sound: the Studio says a welcome line with
// Kokoro as soon as it is downloaded. A fresh install opens it; Settings
// → General runs it again.

import (
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"github.com/hehehai/AudioSmith/internal/catalog"
	"github.com/hehehai/AudioSmith/internal/core"
	"github.com/hehehai/AudioSmith/internal/hub"
	"github.com/hehehai/AudioSmith/internal/i18n"
	"github.com/hehehai/AudioSmith/internal/settings"
)

// onboardingState is the wizard's state.
type onboardingState struct {
	obOpen    bool
	obStep    int    // 0 this Mac, 1 models, 2 privacy, 3 dictation
	obPack    string // the pack chosen; "" = none
	obFree    int64  // free space on the model folder's disk
	obChecked bool   // the system check ran
	obKeep    int    // history: 0 every take, 1 the last 1000, 2 the last 200

	firstSound   bool // the welcome line waits for Kokoro and the engine
	fsRefreshed  bool
	demoResource string // a bundled welcome clip, when the app ships one
}

// modelPack is a set of catalogue models for a kind of Mac.
type modelPack struct {
	id    string
	minGB int      // memory it is recommended from
	adds  []string // catalogue ids, on top of the smaller packs
}

// modelPacks go from small to large; each holds the ones before it.
var modelPacks = []modelPack{
	{"fast", 0, []string{"kokoro-82m-bf16", catalog.DefaultDictation, catalog.DefaultVAD, catalog.DefaultWatermark}},
	{"balanced", 16, []string{"qwen3-tts-0.6b-base-8bit", catalog.DefaultSTT, catalog.DefaultAlign, catalog.DefaultEnhance}},
	{"quality", 24, []string{"qwen3-tts-1.7b-base-8bit", "qwen3-tts-1.7b-voicedesign-4bit", catalog.DefaultSeparate, catalog.DefaultDiarize, catalog.DefaultLLM}},
	{"max", 48, []string{"omnivoice-bf16", "mel-roformer-kim-vocal-2"}},
}

// packTitle and packAbout describe a pack.
func packTitle(id string) string {
	switch id {
	case "fast":
		return i18n.T("Fast")
	case "balanced":
		return i18n.T("Balanced")
	case "quality":
		return i18n.T("Quality")
	case "max":
		return i18n.T("Max")
	}
	return i18n.T("None")
}

func packAbout(id string) string {
	switch id {
	case "fast":
		return i18n.T("Kokoro's 54 voices in 9 languages, and dictation. Light on memory.")
	case "balanced":
		return i18n.T("Adds voice cloning (Qwen3-TTS 0.6B), Whisper transcription with word times, and noise removal.")
	case "quality":
		return i18n.T("Adds the larger Qwen3-TTS (cloning and voice design), dubbing (speech separation, speakers) and a local writing model.")
	case "max":
		return i18n.T("Adds OmniVoice (600+ languages, non-commercial licence) and the finest speech separation.")
	}
	return ""
}

// recommendPack is the pack for a Mac with memGB of memory.
func recommendPack(memGB int) string {
	id := modelPacks[0].id
	for _, p := range modelPacks {
		if memGB >= p.minGB {
			id = p.id
		}
	}
	return id
}

// packEntries are the catalogue entries of a pack (with the smaller
// packs'), the ones the catalogue lists.
func packEntries(id string) []catalog.Entry {
	var out []catalog.Entry
	for _, p := range modelPacks {
		for _, eid := range p.adds {
			if e, ok := catalog.Builtin().ByID(eid); ok && !e.Hidden && !slices.ContainsFunc(out, func(x catalog.Entry) bool { return x.ID == e.ID }) {
				out = append(out, e)
			}
		}
		if p.id == id {
			break
		}
	}
	return out
}

// packSizes are a pack's size and what of it is still to download.
func (a *app) packSizes(id string) (total, missing int64, names []string) {
	for _, e := range packEntries(id) {
		total += e.Size
		if m := a.model(e); !m.known || m.local.State != hub.Downloaded {
			missing += e.Size
			names = append(names, e.Label)
		}
	}
	return total, missing, names
}

// openOnboarding opens the wizard at its first step.
func (a *app) openOnboarding() {
	a.obOpen, a.obStep, a.obChecked = true, 0, false
	a.loadSysInfo()
	a.refreshModels()
	if !a.ffScanned {
		a.ffScanned = true
		a.scanFFmpeg()
	}
	if a.prefs.Setup.Pack != "" {
		a.obPack = a.prefs.Setup.Pack
	}
	switch n := a.prefs.Studio.KeepTakes; {
	case n == 0:
		a.obKeep = 0
	case n > 200:
		a.obKeep = 1
	default:
		a.obKeep = 2
	}
}

// maybeOnboard opens the wizard on a fresh install (openWindow).
func (a *app) maybeOnboard() {
	if !a.prefs.Setup.Done {
		a.openOnboarding()
	}
	if res, err := mygo.App.Path(mygo.PathResources); err == nil {
		// A bundled welcome clip plays while Kokoro downloads, when the
		// app ships one: resources/demo/welcome-<locale>.wav (none yet).
		for _, name := range []string{"welcome-" + i18n.Locale() + ".wav", "welcome-en.wav"} {
			if p := filepath.Join(res, "demo", name); fileExists(p) {
				a.demoResource = p
				break
			}
		}
	}
}

func fileExists(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && !fi.IsDir()
}

// runSystemCheck measures what the first step shows (free space, the
// network through the proxy) in the background.
func (a *app) runSystemCheck() {
	a.obChecked = true
	dir := modelCache(a.prefs)
	a.background(func() {
		free := diskFree(dir)
		a.post(func() { a.obFree = free })
	})
	a.testNetwork(nil)
	a.recheckPermissions()
}

// startPack downloads a pack's missing models (the licence dialog comes
// first for those that need it, so they go last).
func (a *app) startPack(id string) {
	entries := packEntries(id)
	slices.SortStableFunc(entries, func(x, y catalog.Entry) int {
		gx, gy := x.Licence.RequiresAcceptance, y.Licence.RequiresAcceptance
		switch {
		case gx == gy:
			return 0
		case gx:
			return 1
		}
		return -1
	})
	for _, e := range entries {
		if m := a.model(e); m.known && m.local.State == hub.Downloaded {
			continue
		}
		a.download(e)
	}
}

// finishOnboarding closes the wizard and asks for the first sound.
func (a *app) finishOnboarding() {
	pack := a.obPack
	keep := []int{0, 1000, 200}[a.obKeep]
	a.updateSettings(func(s *settings.Settings) {
		s.Setup.Done, s.Setup.Pack = true, pack
		s.Studio.KeepTakes = keep
	})
	a.obOpen = false
	if pack != "" {
		a.startPack(pack)
	}
	if !a.prefs.Setup.FirstSound {
		a.firstSound, a.fsRefreshed = true, false
		if a.demoResource != "" {
			a.pl.Toggle("demo:welcome", a.demoResource)
		}
	}
	a.navigate("/studio")
}

// skipOnboarding closes the wizard for good (Settings → General opens it).
func (a *app) skipOnboarding() {
	a.obOpen = false
	a.updateSettings(func(s *settings.Settings) { s.Setup.Done = true })
}

// welcomeTake is the first sound: a Kokoro line in the UI's language.
func welcomeTake() core.Take {
	t := core.Take{Text: i18n.T("Welcome to AudioSmith. This voice was made on your Mac, and nothing left it."), Voice: "af_heart", Speed: 1}
	if i18n.Locale() == "zh-Hans" {
		t.Voice = "zf_xiaobei"
	}
	if e, ok := catalog.Builtin().ByID("kokoro-82m-bf16"); ok {
		t.Model = e.Repo
	}
	return t
}

// maybeFirstSound says the welcome line once Kokoro is downloaded and
// the engine is free. Called each frame while it waits: cheap, and it
// acts once.
func (a *app) maybeFirstSound() {
	if !a.firstSound || a.obOpen || a.licenceOpen {
		return
	}
	e, ok := catalog.Builtin().ByID("kokoro-82m-bf16")
	if !ok {
		a.firstSound = false
		return
	}
	m := a.model(e)
	if !m.known {
		if !a.fsRefreshed {
			a.fsRefreshed = true
			a.refreshModel(e)
		}
		return
	}
	if m.local.State != hub.Downloaded || a.engState != engineReady || a.job != nil {
		return
	}
	a.firstSound = false
	tk := welcomeTake()
	a.reuse(&tk)
	a.navigate("/studio")
	a.generate()
	a.updateSettings(func(s *settings.Settings) { s.Setup.FirstSound = true })
}

// onboardingRow is Settings → General's way back to the wizard.
func (a *app) onboardingRow(c *ui.Context) {
	settingRow(c, "setup-again", i18n.T("Setup"), i18n.T("Check this Mac, choose a model pack, privacy and dictation, as on the first day."), func() {
		if ui.Button(c.Key("setup-again"), i18n.T("Run the setup again")).Clicked() {
			a.openOnboarding()
		}
	})
}

// --- the wizard's view ----------------------------------------------------------

// onboarding is the wizard, over the window.
func (a *app) onboarding(c *ui.Context) {
	a.maybeFirstSound()
	if !a.obOpen {
		return
	}
	t := c.Theme()
	open := true
	ui.DialogBase(c.Key("onboarding"), &open, func(backdrop, panel ui.Element) {
		backdrop.Background(ui.RGBA(0, 0, 0, 0.35))
		panel.Width(px(t, 760)).Height(px(t, 600)).Radius(14).Background(t.Background).Border(1, t.Border).
			Shadow(0, 16, 40, 0, ui.RGBA(0, 0, 0, 0.3)).Label(i18n.T("Set up AudioSmith"))
		ui.Column(c).Fill().Children(func() {
			a.obHeader(c)
			ui.Divider(c)
			ui.Scroll(c).Grow(1).Children(func() {
				ui.Column(c).Padding(px(t, 20), px(t, 28)).Gap(px(t, 14)).Children(func() {
					switch a.obStep {
					case 0:
						a.obSystem(c)
					case 1:
						a.obModels(c)
					case 2:
						a.obPrivacy(c)
					default:
						a.obDictation(c)
					}
				})
			})
			ui.Divider(c)
			a.obFooter(c)
		})
	})
	if !open { // Esc: the setup can wait; Settings → General opens it
		a.skipOnboarding()
	}
}

// obSteps are the steps' titles.
func obSteps() []string {
	return []string{i18n.T("This Mac"), i18n.T("Models"), i18n.T("Privacy"), i18n.T("Dictation")}
}

func (a *app) obHeader(c *ui.Context) {
	t := c.Theme()
	ui.Row(c).Padding(px(t, 18), px(t, 24)).Gap(px(t, 16)).AlignItems(ui.Center).Children(func() {
		ui.Column(c).Grow(1).Gap(px(t, 2)).Children(func() {
			ui.Text(c, i18n.T("Welcome to AudioSmith")).FontSize(px(t, 18)).Bold()
			ui.Text(c, i18n.T("Speech, voices and dubbing, made on this Mac. Four short steps; everything can be changed later in Settings.")).
				FontSize(px(t, 12)).TextColor(t.TextMuted)
		})
		langs := languageOptions()
		if ui.Select(c.Key("ob-lang"), &a.settingsForm.language, langs).Label(i18n.T("Language")).Width(px(t, 160)).Changed() {
			tag := ""
			if i := slices.Index(langs, a.settingsForm.language); i > 0 {
				tag = i18n.Supported()[i-1]
			}
			a.updateSettings(func(s *settings.Settings) { s.General.Language = tag })
		}
	})
	ui.Row(c).Padding(0, px(t, 24), px(t, 12), px(t, 24)).Gap(px(t, 6)).Children(func() {
		for i, s := range obSteps() {
			col, bg := t.TextMuted, t.Surface
			if i == a.obStep {
				col, bg = t.AccentText, t.Accent
			} else if i < a.obStep {
				col = t.Text
			}
			ui.Row(c.Key("ob-step-"+itoa(i))).Padding(px(t, 4), px(t, 10)).Radius(12).Background(bg).Children(func() {
				ui.Text(c, itoa(i+1)+" · "+s).FontSize(px(t, 12)).TextColor(col)
			})
		}
	})
}

func (a *app) obFooter(c *ui.Context) {
	t := c.Theme()
	ui.Row(c).Padding(px(t, 12), px(t, 24)).Gap(px(t, 8)).AlignItems(ui.Center).Children(func() {
		if ui.Button(c.Key("ob-skip"), i18n.T("Skip setup")).Tooltip(i18n.T("Settings → General runs it again")).Clicked() {
			a.skipOnboarding()
		}
		ui.Spacer(c)
		if a.obStep > 0 && ui.Button(c.Key("ob-back"), i18n.T("Back")).Clicked() {
			a.obStep--
		}
		if a.obStep < 3 {
			if ui.PrimaryButton(c.Key("ob-next"), i18n.T("Continue")).Clicked() {
				a.obStep++
			}
			return
		}
		if ui.PrimaryButton(c.Key("ob-finish"), i18n.T("Start using AudioSmith")).Clicked() {
			a.finishOnboarding()
		}
	})
}

// obCheckRow is a line of the system check.
func obCheckRow(c *ui.Context, key, status, title, detail string, action func()) {
	t := c.Theme()
	ui.Row(c.Key(key)).Gap(px(t, 10)).AlignItems(ui.Center).Children(func() {
		mark, col := "✓", t.Success
		switch status {
		case "warn":
			mark, col = "!", t.Warning
		case "fail":
			mark, col = "✗", t.Danger
		case "wait":
			mark, col = "…", t.TextMuted
		}
		ui.Text(c, mark).Bold().TextColor(col).Width(px(t, 14))
		ui.Column(c).Grow(1).MinWidth(0).Gap(px(t, 1)).Children(func() {
			ui.Text(c, title)
			if detail != "" {
				ui.Text(c, detail).FontSize(px(t, 12)).TextColor(t.TextMuted)
			}
		})
		if action != nil {
			action()
		}
	})
}

// obSystem is step 1: this Mac.
func (a *app) obSystem(c *ui.Context) {
	t := c.Theme()
	if !a.obChecked {
		a.runSystemCheck()
	}
	ui.Text(c, i18n.T("AudioSmith needs no other software: the models run on the Mac's GPU with MLX.")).FontSize(px(t, 12)).TextColor(t.TextMuted)
	s := a.sys
	if s == nil {
		obCheckRow(c, "ob-sys", "wait", i18n.T("Looking at this Mac…"), "", nil)
	} else {
		st := "pass"
		if major := s.macOSMajor(); major > 0 && major < 15 {
			st = "fail"
		}
		obCheckRow(c, "ob-os", st, orDash(s.OS), i18n.T("AudioSmith needs macOS 15 or later."), nil)
		st = "pass"
		if !s.appleSilicon() {
			st = "fail"
		}
		obCheckRow(c, "ob-chip", st, orDash(s.Chip), i18n.T("Models run on Apple silicon (M1 or later)."), nil)
		st, detail := "pass", i18n.T("The %s pack suits this Mac.", packTitle(recommendPack(s.MemoryGB())))
		if s.MemoryGB() < 16 {
			st = "warn"
			detail = i18n.T("Small models suit this Mac best; large ones may be slow.")
		}
		obCheckRow(c, "ob-mem", st, i18n.T("%d GB of memory", s.MemoryGB()), detail, nil)
	}
	if a.obFree > 0 {
		st := "pass"
		if a.obFree < 10<<30 {
			st = "warn"
		}
		obCheckRow(c, "ob-disk", st, i18n.T("%s free for models", humanBytes(a.obFree)), shortPath(modelCache(a.prefs)), nil)
	}
	// The network.
	var hf, mirror *netCheck
	for i := range a.netChecks {
		switch a.netChecks[i].host {
		case "https://huggingface.co":
			hf = &a.netChecks[i]
		case "https://hf-mirror.com":
			mirror = &a.netChecks[i]
		}
	}
	switch {
	case a.netTesting || hf == nil:
		obCheckRow(c, "ob-net", "wait", i18n.T("Checking the network…"), "", nil)
	case hf.ok:
		obCheckRow(c, "ob-net", "pass", i18n.T("Hugging Face answers"), i18n.T("Models download from huggingface.co (or the faster mirror)."), nil)
	case mirror != nil && mirror.ok:
		usesMirror := slices.Equal(a.prefs.Models.Endpoints, []string{"https://hf-mirror.com"})
		obCheckRow(c, "ob-net", "warn", i18n.T("huggingface.co does not answer; hf-mirror.com does"), hf.err, func() {
			if ui.Button(c.Key("ob-mirror"), i18n.T("Use the mirror")).Disabled(usesMirror).Clicked() {
				a.updateSettings(func(s *settings.Settings) { s.Models.Endpoints = []string{"https://hf-mirror.com"} })
			}
		})
	default:
		obCheckRow(c, "ob-net", "warn", i18n.T("No download source answers"), i18n.T("You can download models later; a proxy goes in Settings → Network."), func() {
			if ui.Button(c.Key("ob-net-retry"), i18n.T("Try again")).Clicked() {
				a.testNetwork(nil)
			}
		})
	}
	// The engine.
	switch a.engState {
	case engineReady:
		obCheckRow(c, "ob-engine", "pass", i18n.T("The speech engine runs"), a.engVersion, nil)
	case engineFailed:
		obCheckRow(c, "ob-engine", "fail", i18n.T("The speech engine does not start"), a.engError, nil)
	default:
		obCheckRow(c, "ob-engine", "wait", i18n.T("The speech engine is starting…"), "", nil)
	}
	// ffmpeg (optional).
	switch {
	case a.mediaTool() != nil:
		obCheckRow(c, "ob-ffmpeg", "pass", i18n.T("ffmpeg is ready"), i18n.T("MP3, Opus and FLAC export, WebM and MKV videos."), nil)
	case len(a.ffSystem) > 0:
		p := a.ffSystem[0]
		obCheckRow(c, "ob-ffmpeg", "pass", i18n.T("ffmpeg found: %s", shortPath(p)), i18n.T("Optional: adds MP3, Opus and FLAC export, WebM and MKV videos."), func() {
			if ui.Button(c.Key("ob-ffmpeg-use"), i18n.T("Use it")).Clicked() {
				a.useFFmpeg(p, settings.MediaSystem)
			}
		})
	default:
		obCheckRow(c, "ob-ffmpeg", "pass", i18n.T("ffmpeg is not set up (optional)"), i18n.T("Add it later in Settings → Audio tools for MP3, Opus, FLAC and WebM."), nil)
	}
	// Permissions, asked for in the last step.
	if a.perms.checked {
		micText, micOK := permissionText(a.perms.mic)
		st := "pass"
		if !micOK {
			st = "warn"
		}
		obCheckRow(c, "ob-mic", st, i18n.T("Microphone: %s", micText), i18n.T("For recording voices and dictation; asked when first needed."), nil)
	}
}

// obModels is step 2: a model pack.
func (a *app) obModels(c *ui.Context) {
	t := c.Theme()
	rec := "balanced"
	if a.sys != nil {
		rec = recommendPack(a.sys.MemoryGB())
	}
	if a.obPack == "" && a.prefs.Setup.Pack == "" {
		a.obPack = rec
	}
	ui.Text(c, i18n.T("Choose what to download now. Each model downloads once, resumes if interrupted, and can be added or removed later in Models.")).
		FontSize(px(t, 12)).TextColor(t.TextMuted)
	for _, p := range modelPacks {
		total, missing, names := a.packSizes(p.id)
		chosen := a.obPack == p.id
		card := ui.ButtonBase(c.Key("ob-pack-"+p.id)).Padding(px(t, 12), px(t, 14)).Radius(10).Border(1, t.Border).Label(packTitle(p.id))
		if chosen {
			card.Border(2, t.Accent).Background(t.Accent.Alpha(0.08))
		} else if card.Hovered() {
			card.Background(t.SurfaceHover)
		}
		card.Children(func() {
			ui.Column(c).Grow(1).Gap(px(t, 4)).Children(func() {
				ui.Row(c).Gap(px(t, 8)).AlignItems(ui.Center).Children(func() {
					ui.Text(c, packTitle(p.id)).Bold()
					if p.id == rec {
						ui.Badge(c, i18n.T("Recommended for this Mac"))
					}
					ui.Spacer(c)
					size := humanBytes(total)
					if missing < total {
						size = i18n.T("%s to download of %s", humanBytes(missing), humanBytes(total))
					}
					ui.Text(c, size).FontSize(px(t, 12)).TextColor(t.TextMuted)
				})
				ui.Text(c, packAbout(p.id)).FontSize(px(t, 12)).TextColor(t.TextMuted)
				if chosen && len(names) > 0 {
					ui.Text(c, i18n.T("Still needed: %s", joinNames(names))).FontSize(px(t, 11)).TextColor(t.TextMuted)
				}
			})
		})
		if card.Clicked() {
			a.obPack = p.id
		}
	}
	none := ui.ButtonBase(c.Key("ob-pack-none")).Padding(px(t, 10), px(t, 14)).Radius(10).Label(i18n.T("Download models later"))
	if a.obPack == "" {
		none.Border(2, t.Accent)
	} else {
		none.Border(1, t.Border)
	}
	none.Children(func() { ui.Text(c, i18n.T("Download models later")).FontSize(px(t, 12)) })
	if none.Clicked() {
		a.obPack = ""
	}
	if a.obPack != "" {
		_, missing, _ := a.packSizes(a.obPack)
		if a.obFree > 0 && missing > a.obFree-(5<<30) {
			ui.Text(c, i18n.T("Not enough free space: %s needed, %s free.", humanBytes(missing), humanBytes(a.obFree))).FontSize(px(t, 12)).TextColor(t.Danger)
		}
		if a.obPack == "max" {
			ui.Text(c, i18n.T("OmniVoice's licence (CC BY-NC 4.0) allows non-commercial use only: you are asked to accept it before it downloads.")).
				FontSize(px(t, 12)).TextColor(t.Warning)
		}
		ui.Text(c, i18n.T("Downloads start when you finish, and go on in the background.")).FontSize(px(t, 12)).TextColor(t.TextMuted)
	}
}

func joinNames(names []string) string { return strings.Join(names, " · ") }

// obPrivacy is step 3.
func (a *app) obPrivacy(c *ui.Context) {
	t := c.Theme()
	ui.Text(c, i18n.T("Everything you write, record and generate stays on this Mac. AudioSmith has no analytics and no account.")).Bold()
	local := a.prefs.Privacy.LocalOnlyLLM
	settingRow(c, "ob-local", i18n.T("Local language models only"), i18n.T("Writing help and translation never use a cloud provider, even one you add later."), func() {
		if ui.Switch(c.Key("ob-local"), &local).Label(i18n.T("Keep text on this Mac")).Changed() {
			on := local
			a.updateSettings(func(s *settings.Settings) { s.Privacy.LocalOnlyLLM = on })
		}
	})
	wm := !a.prefs.Privacy.NoWatermark
	settingRow(c, "ob-watermark", i18n.T("Invisible watermark"), i18n.T("Marks generated audio so it can be recognised as made by AudioSmith. Needs the small AudioSeal model."), func() {
		if ui.Switch(c.Key("ob-watermark"), &wm).Label(i18n.T("Add an invisible watermark to generated audio")).Changed() {
			on := wm
			a.updateSettings(func(s *settings.Settings) { s.Privacy.NoWatermark = !on })
		}
	})
	settingRow(c, "ob-keep", i18n.T("History"), i18n.T("Older takes that are not starred are deleted beyond the limit."), func() {
		ui.Segmented(c.Key("ob-keep"), &a.obKeep, i18n.T("Keep every take"), i18n.T("Last 1000"), i18n.T("Last 200")).Label(i18n.T("History"))
	})
	ui.Text(c, i18n.T("Settings → Privacy says exactly what may use the network, and when.")).FontSize(px(t, 12)).TextColor(t.TextMuted)
}

// obDictation is step 4.
func (a *app) obDictation(c *ui.Context) {
	t := c.Theme()
	ui.Text(c, i18n.T("Dictate into any app: press the shortcut, speak, and the text is typed where your cursor is. Speech stays on this Mac.")).
		FontSize(px(t, 12)).TextColor(t.TextMuted)
	f := &a.dictSet
	f.enabled = a.prefs.Dictation.Enabled
	settingRow(c, "ob-dict", i18n.T("Dictation shortcut"), i18n.T("Works in every app while AudioSmith runs."), func() {
		if ui.Switch(c.Key("ob-dict-switch"), &f.enabled).Label(i18n.T("Dictation shortcut")).Changed() {
			on := f.enabled
			a.updateSettings(func(s *settings.Settings) { s.Dictation.Enabled = on })
			a.applyDictShortcut()
		}
	})
	if a.prefs.Dictation.Enabled {
		a.shortcutRow(c) // dictation_settings.go
		if a.dictSys != nil {
			a.permissionsSettings(c) // permissions.go: microphone, accessibility
		}
	}
	ui.Text(c, i18n.T("When you finish, the Studio says hello as soon as the Kokoro voice is downloaded.")).FontSize(px(t, 12)).TextColor(t.TextMuted)
}

// obPackNames lists a pack's models (tests, the palette).
func obPackNames(id string) []string {
	var out []string
	for _, e := range packEntries(id) {
		out = append(out, e.Label)
	}
	return out
}
