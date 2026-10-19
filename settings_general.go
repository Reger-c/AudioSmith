package main

// The settings sections the shell itself owns (Settings → General,
// Appearance, Models and downloads, Engine, Studio). The other sections
// live with their areas (settings_*.go, dictation_settings.go, …); the
// dialog that shows them is settings_dialog.go.

import (
	"slices"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/hehehai/AudioSmith/internal/catalog"
	"github.com/hehehai/AudioSmith/internal/hub"
	"github.com/hehehai/AudioSmith/internal/i18n"
	"github.com/hehehai/AudioSmith/internal/settings"
	famset "github.com/hehehai/actnowkit/settings"
)

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
	a.sharedScope(c, famset.KeyLanguage) // family_shared_view.go: the ActNow family's
	a.m2GeneralSettings(c)               // M2: dictation_settings.go
	a.onboardingRow(c)                   // M7a: onboarding.go
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
	a.sidebarRow(c)
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
	settingRowNote(c, "cache-dir", i18n.T("Model folder"), shown, func() {
		if ui.Button(c.Key("cache-choose"), i18n.T("Choose…")).Clicked() {
			a.chooseCacheDir()
		}
		if ui.Button(c.Key("cache-default"), i18n.T("Use the default")).Disabled(dir == "").
			Tooltip(i18n.T("The ActNow model folder in Application Support")).Clicked() {
			a.updateSettings(func(s *settings.Settings) { s.Models.CacheDir = "" })
		}
	})
	a.sharedScope(c, famset.KeyModelsRoot)

	settingRowNote(c, "network-link", i18n.T("Download source and proxy"), a.proxyInfo, func() { // M7a: moved to Settings → Network
		if ui.Button(c.Key("network-open"), i18n.T("Network…")).Clicked() {
			a.navigate("/settings/network")
		}
	})

	sectionTitle(c, i18n.T("Hugging Face token"))
	hf := hfKeySlot(a.prefs)
	settingRowNote(c, "token", i18n.T("Access token"), orDash(a.tokenInfo), func() {
		if a.keyOffer(c, hf, true) { // family_keys_view.go: another ActNow app's token
			return
		}
		ui.TextInput(c.Key("token-input"), &a.token).Password().Placeholder("hf_…").Width(px(t, 200)).Label(i18n.T("Access token"))
		if ui.Button(c.Key("token-save"), i18n.T("Save")).Disabled(strings.TrimSpace(a.token) == "").Clicked() {
			a.saveToken(a.token)
		}
		if ui.Button(c.Key("token-remove"), i18n.T("Remove")).Tooltip(i18n.T("Remove the token saved in the Keychain")).Clicked() {
			a.saveToken("")
		}
	})
	a.keyNotes(c, hf, a.token)
	a.keySecurityHelp(c)

	sectionTitle(c, i18n.T("Licences"))
	if len(a.accepted) == 0 {
		ui.Text(c, i18n.T("You have not accepted any model licence.")).FontSize(px(t, 12)).TextColor(t.TextMuted)
	}
	for _, acc := range a.accepted {
		who := i18n.T("every model under it")
		if acc.Model != "" {
			who = catalog.Builtin().DisplayName(acc.Model)
		}
		settingRowNote(c, "acc-"+acc.Licence+"-"+acc.Model, licenceName(acc.Licence), i18n.T("Accepted for %s on %s", who, i18n.Date(acc.AcceptedAt)), func() {
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
		if numberInput(c.Key("memory-input"), &a.memoryGB, 0, 512, 1, numOpts{Label: i18n.T("Memory budget"), Width: px(t, 120)}) {
			gb := a.memoryGB
			a.updateSettings(func(s *settings.Settings) { s.Engine.MemoryBudgetGB = gb })
		}
	})
	settingRow(c, "unload", i18n.T("Unload idle models"), i18n.T("Minutes without use before a model leaves memory; 0 keeps it."), func() {
		if numberInput(c.Key("unload-input"), &a.autoUnload, 0, 240, 5, numOpts{Label: i18n.T("Unload idle models"), Width: px(t, 120)}) {
			m := int(a.autoUnload)
			a.updateSettings(func(s *settings.Settings) { s.Models.AutoUnload = m })
		}
	})
}

func (a *app) studioSettings(c *ui.Context) {
	t := c.Theme()
	settingRow(c, "default-model", i18n.T("Default model"), i18n.T("The model the Studio starts with. Choosing another one in the Studio changes it too."), func() {
		cur, _ := catalog.Builtin().ByLabel(a.defaultModel)
		if e, ok := modelSelect(c, "default-model-select", cur, catalog.Builtin().ForTask(catalog.TaskTTS), i18n.T("Default model"), px(t, 260)); ok {
			a.defaultModel = e.Label
			id := e.ID
			a.updateSettings(func(s *settings.Settings) { s.Studio.Model = id })
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
