package main

// Settings: the settings service seen from the UI (a snapshot in
// a.prefs, kept by settings.Changed), the language and the theme they
// drive, and the settings pages under /settings/{section}.

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"github.com/hehehai/AudioSmith/internal/catalog"
	"github.com/hehehai/AudioSmith/internal/core"
	"github.com/hehehai/AudioSmith/internal/hub"
	"github.com/hehehai/AudioSmith/internal/i18n"
	"github.com/hehehai/AudioSmith/internal/settings"
)

// settingsForm is the settings pages' view state: the controls bind to
// these, and their changes go to the service.
type settingsForm struct {
	systemLocale string // mygo.App.Locale(), read when the window opens

	language        string // label
	theme           int
	scale           int
	reduceMotion    bool
	source          string // label
	endpoint        string // custom endpoint
	proxyMode       string // label
	proxy           string // custom proxy
	cacheDir        string
	memoryGB        float64
	autoUnload      float64
	defaultModel    string // label
	autoplaySetting bool

	token     string // typed, not yet saved
	tokenInfo string // where the token in use comes from
	proxyInfo string // the proxy downloads use
}

// settingsSection is a page of the settings.
type settingsSection struct{ id, title, en string }

func settingsSections() []settingsSection {
	return []settingsSection{
		{"general", i18n.T("General"), "General"},
		{"appearance", i18n.T("Appearance"), "Appearance"},
		{"models", i18n.T("Models and downloads"), "Models and downloads"},
		{"studio", i18n.T("Studio"), "Studio"},
		{"engine", i18n.T("Engine"), "Engine"},
		{"pronunciation", i18n.T("Pronunciation"), "Pronunciation"}, // M1-G: pronunciation_settings.go
		// M2 (thread #17): dictation_settings.go, transcribe_llm.go, permissions.go.
		{"dictation", i18n.T("Dictation"), "Dictation"},
		{"writing", i18n.T("Writing help"), "Writing help"},
		{"permissions", i18n.T("Permissions"), "Permissions"},
		dubSettingsSection(),                                         // M4: dub_settings.go (thread #19)
		{"api", i18n.T("Local API"), "Local API"},                    // M5: api_settings.go (thread #20)
		{"audio-tools", i18n.T("Audio tools"), "Audio tools ffmpeg"}, // M7a: ffmpeg_settings.go
	}
}

// updateSettings changes the settings: at once in the snapshot, and in
// the service (saved in the background, then announced with Changed).
// fn must only set fields, as it runs on both.
func (a *app) updateSettings(fn func(*settings.Settings)) {
	fn(&a.prefs)
	st := a.settings
	a.async(func() {
		if err := st.Update(context.Background(), fn); err != nil {
			a.post(func() { a.toast(i18n.T("Could not save the settings: %v", err)) })
		}
	})
}

// settingsChanged applies saved settings to the app.
func (a *app) settingsChanged(old, new settings.Settings) {
	a.prefs = new
	if old.General.Language != new.General.Language {
		a.applyLocale()
	}
	if !slices.Equal(old.Models.Endpoints, new.Models.Endpoints) || old.Models.CacheDir != new.Models.CacheDir || old.Models.Proxy != new.Models.Proxy {
		a.resetHub() // the library resets it too; downloads running keep theirs
		if old.Models.Proxy != new.Models.Proxy {
			a.loadProxyInfo()
		}
		if old.Models.CacheDir != new.Models.CacheDir {
			a.refreshModels()
		}
	}
	a.applyEngineSettings(old, new)
	a.m2SettingsChanged(old, new)     // M2: dictation_settings.go
	a.ffmpegSettingsChanged(old, new) // M7a: ffmpeg_settings.go
	if old.Studio.Autoplay != new.Studio.Autoplay {
		a.autoplay = new.Studio.Autoplay
	}
	a.loadSettingsForm()
}

// applyEngineSettings hands changed engine settings to the supervisor:
// the memory budget and the idle limit apply at once; a new model folder
// with the next start of the engine, which happens now unless it works.
func (a *app) applyEngineSettings(old, new settings.Settings) {
	if a.eng == nil || a.eng.sup == nil {
		return
	}
	sup := a.eng.sup
	if old.Engine.MemoryBudgetGB != new.Engine.MemoryBudgetGB {
		sup.SetMemoryBudget(gb(new.Engine.MemoryBudgetGB))
	}
	if old.Models.AutoUnload != new.Models.AutoUnload {
		sup.SetIdleUnload(time.Duration(new.Models.AutoUnload) * time.Minute)
	}
	if dir := modelCache(new); dir != modelCache(old) {
		sup.SetCacheDir(dir)
		if a.job == nil && len(a.activeJobs()) == 0 && a.engState == engineReady {
			a.restartEngine()
		} else {
			a.toast(i18n.T("The engine uses the new model folder after its next restart."))
		}
	}
}

// applyLocale switches the UI to the settings' language, or the system's,
// and rebuilds the native menu in it.
func (a *app) applyLocale() {
	tag := a.prefs.General.Language
	if tag == "" {
		tag = a.systemLocale
	}
	i18n.SetLocale(tag)
	if i18n.Locale() != a.menuLocale {
		a.buildMenu()
	}
	a.loadSettingsForm() // its labels are translated
}

// applyTheme sets the frame's theme from the appearance settings: a
// forced light or dark theme, and the UI scale, which grows the text and
// the spacing (sizes of our own scale with px).
func (a *app) applyTheme(c *ui.Context) {
	ap := a.prefs.Appearance
	sys := c.Theme()
	if (ap.Theme == "system" || ap.Theme == "") && (ap.Scale == 1 || ap.Scale == 0) {
		return
	}
	th := *sys
	if (ap.Theme == "light" && sys.Dark) || (ap.Theme == "dark" && !sys.Dark) {
		if ap.Theme == "dark" {
			th = *ui.DarkTheme()
		} else {
			th = *ui.LightTheme()
		}
		if s := c.Preferences().TextScale; s > 0 {
			th.FontSize *= s
		}
	}
	if s := float32(ap.Scale); s > 0 && s != 1 {
		th.FontSize *= s
		th.Spacing = th.Space(1) * s
	}
	c.SetTheme(&th)
}

// The options of the selects, and what they mean.
func languageOptions() []string {
	opts := []string{i18n.T("Follow the system")}
	for _, tag := range i18n.Supported() {
		opts = append(opts, i18n.Name(tag))
	}
	return opts
}

func sourceOptions() []string {
	return []string{i18n.T("Automatic (fastest of huggingface.co and hf-mirror.com)"), "huggingface.co", "hf-mirror.com", i18n.T("Custom…")}
}

func proxyOptions() []string {
	return []string{i18n.T("Automatic (system proxy)"), i18n.T("None"), i18n.T("Custom…")}
}

var scaleLabels = []string{"90%", "100%", "110%", "125%"}

// loadSettingsForm sets the controls from a.prefs.
func (a *app) loadSettingsForm() {
	s := a.prefs
	f := &a.settingsForm
	f.language = languageOptions()[0]
	for i, tag := range i18n.Supported() {
		if tag == s.General.Language {
			f.language = languageOptions()[i+1]
		}
	}
	f.theme = max(0, slices.Index(settings.Themes, s.Appearance.Theme))
	f.scale = max(0, slices.Index(settings.Scales, s.Appearance.Scale))
	f.reduceMotion = s.Appearance.ReduceMotion
	src := sourceOptions()
	switch {
	case len(s.Models.Endpoints) == 0:
		f.source = src[0]
	case len(s.Models.Endpoints) == 1 && s.Models.Endpoints[0] == "https://huggingface.co":
		f.source = src[1]
	case len(s.Models.Endpoints) == 1 && s.Models.Endpoints[0] == "https://hf-mirror.com":
		f.source = src[2]
	default:
		f.source, f.endpoint = src[3], strings.Join(s.Models.Endpoints, " ")
	}
	px := proxyOptions()
	switch s.Models.Proxy {
	case "":
		f.proxyMode = px[0]
	case hub.ProxyDirect:
		f.proxyMode = px[1]
	default:
		f.proxyMode, f.proxy = px[2], s.Models.Proxy
	}
	f.cacheDir = s.Models.CacheDir
	f.defaultModel = catalog.Builtin().ForTask(catalog.TaskTTS)[0].Label
	if e, ok := catalog.Builtin().ByID(s.Studio.Model); ok {
		f.defaultModel = e.Label
	}
	f.autoplaySetting = s.Studio.Autoplay
	f.memoryGB = s.Engine.MemoryBudgetGB
	f.autoUnload = float64(s.Models.AutoUnload)
}

// loadTokenInfo finds which HF token downloads use, in the background
// (the Keychain blocks).
func (a *app) loadTokenInfo() {
	a.async(func() {
		tok, src := hub.Token(hub.DefaultKeychain)
		info := i18n.T("No token: gated models cannot download.")
		switch src {
		case hub.TokenEnv:
			info = i18n.T("Using %s from $HF_TOKEN.", hub.MaskToken(tok))
		case hub.TokenKeychain:
			info = i18n.T("Using %s from the Keychain.", hub.MaskToken(tok))
		case hub.TokenCLI:
			info = i18n.T("Using %s from huggingface-cli login.", hub.MaskToken(tok))
		}
		a.post(func() { a.tokenInfo = info })
	})
}

// loadProxyInfo finds the proxy downloads use, in the background.
func (a *app) loadProxyInfo() {
	mode := a.prefs.Models.Proxy
	a.async(func() {
		p := hub.DescribeProxy(mode)
		info := i18n.T("Downloads connect directly.")
		if p != "" {
			info = i18n.T("Downloads go through %s.", p)
		}
		a.post(func() { a.proxyInfo = info })
	})
}

func (a *app) settingsPage(c *ui.Context, r *ui.Route) {
	t := c.Theme()
	header(c, i18n.T("Settings"), nil)
	ui.Row(c).Grow(1).MinHeight(0).AlignItems(ui.Stretch).Children(func() {
		ui.Column(c).Width(px(t, 190)).Shrink(0).Padding(px(t, 10)).Gap(px(t, 2)).Children(func() {
			for _, s := range settingsSections() {
				b := ui.ButtonBase(c.Key("settings-"+s.id)).Padding(px(t, 6), px(t, 10)).Radius(6).Label(s.title)
				switch {
				case strings.HasPrefix(a.router.Path(), "/settings/"+s.id):
					b.Background(t.Accent.Alpha(0.15))
				case b.Hovered():
					b.Background(t.SurfaceHover)
				}
				b.Children(func() { ui.Text(c, s.title).Grow(1) })
				if b.Clicked() {
					a.navigate("/settings/" + s.id)
				}
			}
		})
		ui.Box(c).Width(1).Background(t.Border)
		r.View(c, func(r *ui.Route) {
			ui.Scroll(c).Grow(1).Children(func() {
				ui.Column(c).Padding(px(t, 20)).Gap(px(t, 18)).MaxWidth(px(t, 680)).Children(func() {
					switch {
					case r.Match("/general"):
						r.Title(i18n.T("General"))
						a.lastSettings = "general"
						a.generalSettings(c)
					case r.Match("/appearance"):
						r.Title(i18n.T("Appearance"))
						a.lastSettings = "appearance"
						a.appearanceSettings(c)
					case r.Match("/models"):
						r.Title(i18n.T("Models and downloads"))
						a.lastSettings = "models"
						a.modelSettings(c)
					case r.Match("/studio"):
						r.Title(i18n.T("Studio"))
						a.lastSettings = "studio"
						a.studioSettings(c)
					case r.Match("/engine"):
						r.Title(i18n.T("Engine"))
						a.lastSettings = "engine"
						a.engineSettings(c)
					case r.Match("/pronunciation"): // M1-G: pronunciation_settings.go
						r.Title(i18n.T("Pronunciation"))
						a.lastSettings = "pronunciation"
						a.pronunciationSettings(c)
					// M2 (thread #17).
					case r.Match("/dictation"):
						r.Title(i18n.T("Dictation"))
						a.lastSettings = "dictation"
						a.dictationSettings(c)
					case r.Match("/writing"):
						r.Title(i18n.T("Writing help"))
						a.lastSettings = "writing"
						a.writingSettings(c)
					case r.Match("/permissions"):
						r.Title(i18n.T("Permissions"))
						a.lastSettings = "permissions"
						a.permissionsSettings(c)
					case a.dubSettingsRoute(c, r): // M4: Settings → Dubbing (dub_pages.go, thread #19)
					case a.apiSettingsRoute(c, r): // M5: api_settings.go (thread #20)
					case r.Match("/audio-tools"): // M7a: ffmpeg_settings.go
						r.Title(i18n.T("Audio tools"))
						a.lastSettings = "audio-tools"
						a.audioToolsSettings(c)
					default:
						ui.Text(c, i18n.T("There is no page at %s.", a.router.Path())).TextColor(t.TextMuted)
					}
				})
			})
		}).Grow(1).MinWidth(0)
	})
}

// settingRow is a setting: its name and description on the left, its
// control on the right.
func settingRow(c *ui.Context, key, title, desc string, control func()) {
	t := c.Theme()
	ui.Row(c.Key(key)).Gap(px(t, 16)).AlignItems(ui.Center).Children(func() {
		ui.Column(c).Grow(1).MinWidth(0).Gap(px(t, 2)).Children(func() {
			ui.Text(c, title)
			if desc != "" {
				ui.Text(c, desc).FontSize(px(t, 12)).TextColor(t.TextMuted)
			}
		})
		control()
	})
}

func sectionTitle(c *ui.Context, title string) {
	t := c.Theme()
	ui.Text(c, title).FontSize(px(t, 13)).Bold().TextColor(t.TextMuted)
}

func (a *app) generalSettings(c *ui.Context) {
	t := c.Theme()
	settingRow(c, "language", i18n.T("Language"), i18n.T("The language of the app. Following the system uses English unless the system's language is supported."), func() {
		opts := languageOptions()
		if ui.Select(c.Key("language-select"), &a.settingsForm.language, opts).Label(i18n.T("Language")).Width(px(t, 200)).Changed() {
			tag := ""
			if i := slices.Index(opts, a.settingsForm.language); i > 0 {
				tag = i18n.Supported()[i-1]
			}
			a.updateSettings(func(s *settings.Settings) { s.General.Language = tag })
		}
	})
	a.m2GeneralSettings(c) // M2: dictation_settings.go
}

func (a *app) appearanceSettings(c *ui.Context) {
	settingRow(c, "theme", i18n.T("Theme"), "", func() {
		if ui.Segmented(c.Key("theme-seg"), &a.theme, i18n.T("System"), i18n.T("Light"), i18n.T("Dark")).Label(i18n.T("Theme")).Changed() {
			theme := settings.Themes[a.theme]
			a.updateSettings(func(s *settings.Settings) { s.Appearance.Theme = theme })
		}
	})
	settingRow(c, "scale", i18n.T("Interface size"), i18n.T("Scales text and spacing."), func() {
		if ui.Segmented(c.Key("scale-seg"), &a.scale, scaleLabels...).Label(i18n.T("Interface size")).Changed() {
			scale := settings.Scales[a.scale]
			a.updateSettings(func(s *settings.Settings) { s.Appearance.Scale = scale })
		}
	})
	settingRow(c, "motion", i18n.T("Reduce motion"), i18n.T("Pages and panels change without animating. The system's setting is followed too."), func() {
		if ui.Switch(c.Key("motion-switch"), &a.reduceMotion).Label(i18n.T("Reduce motion")).Changed() {
			on := a.reduceMotion
			a.updateSettings(func(s *settings.Settings) { s.Appearance.ReduceMotion = on })
		}
	})
}

func (a *app) modelSettings(c *ui.Context) {
	t := c.Theme()
	sectionTitle(c, i18n.T("Storage"))
	dir := a.prefs.Models.CacheDir
	shown := dir
	if shown == "" {
		shown = hub.CacheDir()
	}
	settingRow(c, "cache-dir", i18n.T("Model folder"), shown, func() {
		if ui.Button(c.Key("cache-choose"), i18n.T("Choose…")).Clicked() {
			a.chooseCacheDir()
		}
		if ui.Button(c.Key("cache-default"), i18n.T("Use the default")).Disabled(dir == "").
			Tooltip(i18n.T("The Hugging Face cache the Python tools share")).Clicked() {
			a.updateSettings(func(s *settings.Settings) { s.Models.CacheDir = "" })
		}
	})

	sectionTitle(c, i18n.T("Network"))
	settingRow(c, "source", i18n.T("Download from"), i18n.T("Mirrors never receive your token; gated models always come from huggingface.co."), func() {
		opts := sourceOptions()
		if ui.Select(c.Key("source-select"), &a.source, opts).Label(i18n.T("Download from")).Width(px(t, 240)).Changed() {
			var eps []string
			switch slices.Index(opts, a.source) {
			case 1:
				eps = []string{"https://huggingface.co"}
			case 2:
				eps = []string{"https://hf-mirror.com"}
			case 3:
				eps = strings.Fields(a.endpoint)
				if len(eps) == 0 {
					return // until a URL is typed
				}
			}
			a.updateSettings(func(s *settings.Settings) { s.Models.Endpoints = eps })
		}
	})
	if slices.Index(sourceOptions(), a.source) == 3 {
		settingRow(c, "endpoint", i18n.T("Endpoint"), i18n.T("One or more URLs, separated by spaces; the fastest answers."), func() {
			in := ui.TextInput(c.Key("endpoint-input"), &a.endpoint).Placeholder("https://hf-mirror.com").Width(px(t, 280)).Label(i18n.T("Endpoint"))
			if in.Submitted() {
				eps := strings.Fields(a.endpoint)
				a.updateSettings(func(s *settings.Settings) { s.Models.Endpoints = eps })
			}
		})
	}
	settingRow(c, "proxy", i18n.T("Proxy"), a.proxyInfo, func() {
		opts := proxyOptions()
		if ui.Select(c.Key("proxy-select"), &a.proxyMode, opts).Label(i18n.T("Proxy")).Width(px(t, 240)).Changed() {
			mode := ""
			switch slices.Index(opts, a.proxyMode) {
			case 1:
				mode = hub.ProxyDirect
			case 2:
				mode = strings.TrimSpace(a.proxy)
				if mode == "" {
					return
				}
			}
			a.updateSettings(func(s *settings.Settings) { s.Models.Proxy = mode })
		}
	})
	if slices.Index(proxyOptions(), a.proxyMode) == 2 {
		settingRow(c, "proxy-url", i18n.T("Proxy URL"), i18n.T("http://, https:// or socks5://host:port"), func() {
			in := ui.TextInput(c.Key("proxy-input"), &a.proxy).Placeholder("http://127.0.0.1:7890").Width(px(t, 280)).Label(i18n.T("Proxy URL"))
			if in.Submitted() {
				mode := strings.TrimSpace(a.proxy)
				a.updateSettings(func(s *settings.Settings) { s.Models.Proxy = mode })
			}
		})
	}

	sectionTitle(c, i18n.T("Hugging Face token"))
	settingRow(c, "token", i18n.T("Access token"), orDash(a.tokenInfo), func() {
		ui.TextInput(c.Key("token-input"), &a.token).Password().Placeholder("hf_…").Width(px(t, 200)).Label(i18n.T("Access token"))
		if ui.Button(c.Key("token-save"), i18n.T("Save")).Disabled(strings.TrimSpace(a.token) == "").Clicked() {
			a.saveToken(a.token)
		}
		if ui.Button(c.Key("token-remove"), i18n.T("Remove")).Tooltip(i18n.T("Remove the token saved in the Keychain")).Clicked() {
			a.saveToken("")
		}
	})

	sectionTitle(c, i18n.T("Licences"))
	if len(a.accepted) == 0 {
		ui.Text(c, i18n.T("You have not accepted any model licence.")).FontSize(px(t, 12)).TextColor(t.TextMuted)
	}
	for _, acc := range a.accepted {
		who := i18n.T("every model under it")
		if acc.Model != "" {
			who = catalog.Builtin().DisplayName(acc.Model)
		}
		settingRow(c, "acc-"+acc.Licence+"-"+acc.Model, licenceName(acc.Licence), i18n.T("Accepted for %s on %s", who, i18n.Date(acc.AcceptedAt)), func() {
			if ui.Button(c.Key("revoke"), i18n.T("Withdraw")).Clicked() {
				a.revokeLicence(acc)
			}
		})
	}
}

// licenceName names a licence id from the catalogue.
func licenceName(id string) string {
	for _, e := range catalog.Builtin().Entries {
		if e.Licence.ID == id {
			return e.Licence.Name
		}
	}
	return id
}

func (a *app) engineSettings(c *ui.Context) {
	t := c.Theme()
	settingRow(c, "memory", i18n.T("Memory budget"), i18n.T("How much memory models may take together, in GB; 0 lets AudioSmith decide. Models over it leave memory at once."), func() {
		if ui.NumberInput(c.Key("memory-input"), &a.memoryGB, 0, 512, 1).Width(px(t, 120)).Label(i18n.T("Memory budget")).Changed() {
			gb := a.memoryGB
			a.updateSettings(func(s *settings.Settings) { s.Engine.MemoryBudgetGB = gb })
		}
	})
	settingRow(c, "unload", i18n.T("Unload idle models"), i18n.T("Minutes without use before a model leaves memory; 0 keeps it."), func() {
		if ui.NumberInput(c.Key("unload-input"), &a.autoUnload, 0, 240, 5).Width(px(t, 120)).Label(i18n.T("Unload idle models")).Changed() {
			m := int(a.autoUnload)
			a.updateSettings(func(s *settings.Settings) { s.Models.AutoUnload = m })
		}
	})
}

func (a *app) studioSettings(c *ui.Context) {
	t := c.Theme()
	settingRow(c, "default-model", i18n.T("Default model"), i18n.T("The model the Studio starts with. Choosing another one in the Studio changes it too."), func() {
		labels := catalog.Builtin().Labels(catalog.TaskTTS)
		labels = slices.DeleteFunc(slices.Clone(labels), func(l string) bool { return l == catalog.CustomLabel })
		if ui.Select(c.Key("default-model-select"), &a.defaultModel, labels).Label(i18n.T("Default model")).Width(px(t, 260)).Changed() {
			if e, ok := catalog.Builtin().ByLabel(a.defaultModel); ok {
				id := e.ID
				a.updateSettings(func(s *settings.Settings) { s.Studio.Model = id })
			}
		}
	})
	settingRow(c, "autoplay", i18n.T("Play each take when it is ready"), "", func() {
		if ui.Switch(c.Key("autoplay-switch"), &a.autoplaySetting).Label(i18n.T("Play each take when it is ready")).Changed() {
			on := a.autoplaySetting
			a.updateSettings(func(s *settings.Settings) { s.Studio.Autoplay = on })
		}
	})
	a.historySettings(c) // M1-G: history cap (takes_view.go)
}

// chooseCacheDir asks for the model folder; the dialog blocks, so it runs
// on a goroutine of its own.
func (a *app) chooseCacheDir() {
	go func() {
		paths, err := mygo.Dialog.Open(mygo.OpenDialogOptions{
			Parent:            a.win,
			Title:             i18n.T("Model folder"),
			Directory:         true,
			CreateDirectories: true,
		})
		if err != nil || len(paths) == 0 {
			return
		}
		a.post(func() {
			dir := paths[0]
			a.updateSettings(func(s *settings.Settings) { s.Models.CacheDir = dir })
		})
	}()
}

// saveToken saves the HF token in the Keychain ("" removes it).
func (a *app) saveToken(tok string) {
	tok = strings.TrimSpace(tok)
	a.token = ""
	a.async(func() {
		err := hub.DefaultKeychain.Set(tok)
		a.post(func() {
			switch {
			case err != nil:
				a.toast(i18n.T("Could not save the token: %v", err))
			case tok == "":
				a.toast(i18n.T("Token removed"))
			default:
				a.toast(i18n.T("Token saved in the Keychain"))
			}
		})
		if err == nil {
			a.loadTokenInfo()
		}
	})
}

// loadAcceptances refreshes the snapshot of accepted licences.
func (a *app) loadAcceptances() {
	gate := a.licences
	a.async(func() {
		list, err := gate.List(context.Background())
		a.post(func() {
			if err != nil {
				a.toast(i18n.T("Could not read the licences: %v", err))
				return
			}
			a.accepted = list
		})
	})
}

func (a *app) revokeLicence(acc core.LicenceAcceptance) {
	gate := a.licences
	a.async(func() {
		if err := gate.Revoke(context.Background(), acc.Licence, acc.Model); err != nil {
			a.post(func() { a.toast(fmt.Sprint(err)) })
		}
	})
}
