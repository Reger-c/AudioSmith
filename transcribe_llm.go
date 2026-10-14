package main

// Writing help: the language models that clean up transcripts and
// dictations (internal/llm, internal/translate), and Settings → Writing
// help. Providers are settings.LLM (no keys: those live in the Keychain
// as "llm.<provider>"); HTTP goes through hub.NewHTTPClient with the
// download proxy setting and no response-header bound. Routing, as
// threads/13.md decided: unless the user chose, a local Ollama when it
// answers on 127.0.0.1:11434, else none; cloud providers only when chosen.
// "Keep text on this Mac" (settings.Privacy.LocalOnlyLLM) leaves only
// local backends.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/hehehai/AudioSmith/internal/core"
	"github.com/hehehai/AudioSmith/internal/hub"
	"github.com/hehehai/AudioSmith/internal/i18n"
	"github.com/hehehai/AudioSmith/internal/llm"
	"github.com/hehehai/AudioSmith/internal/settings"
	"github.com/hehehai/AudioSmith/internal/translate"
)

// ollamaURL is where a local Ollama answers.
var ollamaURL = "http://127.0.0.1:11434"

// llmSecrets reads API keys; tests replace it.
var llmSecrets llm.Secrets = llm.SecretsFunc(func(_ context.Context, name string) (string, error) {
	v, err := keychainItem(name).Get()
	if err == nil && v == "" {
		err = llm.ErrNoSecret
	}
	return v, err
})

// keychainItem is the Keychain item of an API key ("llm.openai").
func keychainItem(name string) hub.Keychain {
	return hub.Secret(name, "AudioSmith "+strings.TrimPrefix(name, llm.SecretPrefix)+" API key")
}

// llmState is the writing-help side: the registry and router the
// clean-ups use, what a local Ollama offers, and the settings form.
type llmState struct {
	llmReg    *llm.Registry
	llmRouter *llm.Router
	// llmOllama are the models a local Ollama offers; nil when it did not
	// answer (or was not asked yet).
	llmOllama  []string
	llmChecked bool
	llmProxy   string // the proxy setting the registry's client was made for
	llmForm    llmForm
}

// llmForm is Settings → Writing help. Controls bind to its fields, which
// syncLLMForm sets from the settings when they change.
type llmForm struct {
	add     string                   // the preset label chosen in "Add a provider"
	edits   map[string]*providerEdit // by provider id
	hasKey  map[string]bool          // a key is in the Keychain
	models  map[string][]string
	test    map[string]string // the last test's answer
	testErr map[string]bool
	testing map[string]bool
	route   string // "Use for writing help" label
	clean   string // "Dictation clean-up" label
	local   bool
}

// providerEdit is what a provider's card binds to.
type providerEdit struct {
	base, model string
	key         string // typed, not saved
}

// syncLLMForm sets the form from a.prefs.
func (a *app) syncLLMForm() {
	f := &a.llmForm
	if f.edits == nil {
		return
	}
	labels, ids := a.routeOptions()
	cur := "auto"
	if a.prefs.LLM.Manual {
		cur = ""
		if len(a.prefs.LLM.Default) > 0 {
			cur = a.prefs.LLM.Default[0].Provider
		}
	}
	f.route = labels[max(slices.Index(ids, cur), 0)]
	cleanLabels, cleanIDs := cleanOptions(labels, ids)
	curClean := ""
	if r := a.prefs.LLM.Skills[string(llm.SkillDictationCleanup)]; len(r) > 0 {
		curClean = r[0].Provider
	}
	f.clean = cleanLabels[max(slices.Index(cleanIDs, curClean), 0)]
	f.local = a.prefs.Privacy.LocalOnlyLLM
	for _, p := range a.prefs.LLM.Providers {
		e := f.edits[p.ID]
		if e == nil {
			e = &providerEdit{}
			f.edits[p.ID] = e
			if pr, ok := llm.LookupPreset(pid(llm.ProviderConfig{ID: p.ID, Preset: p.Preset})); ok {
				a.loadKeyInfo(p.ID, pr)
			}
		}
		e.base, e.model = p.BaseURL, p.Model
	}
}

// cleanOptions are the choices of "Dictation clean-up": the same as
// writing help, or one of the providers.
func cleanOptions(labels, ids []string) ([]string, []string) {
	return append([]string{i18n.T("The same")}, labels[2:]...), append([]string{""}, ids[2:]...)
}

// openLLM builds the registry and router from the settings and looks for
// a local Ollama.
func (a *app) openLLM() {
	a.llmForm = llmForm{edits: map[string]*providerEdit{}, hasKey: map[string]bool{}, models: map[string][]string{},
		test: map[string]string{}, testErr: map[string]bool{}, testing: map[string]bool{}}
	a.llmProxy = a.prefs.Models.Proxy
	a.llmReg = llm.NewRegistry(hub.NewHTTPClient(a.llmProxy, 0), llmSecrets)
	a.llmRouter = &llm.Router{Registry: a.llmReg}
	a.applyLLM()
	a.syncLLMForm()
	a.checkOllama()
}

// applyLLM configures the registry and the router from a.prefs.
func (a *app) applyLLM() {
	if a.llmReg == nil {
		return
	}
	s := a.prefs
	if s.Models.Proxy != a.llmProxy {
		a.llmProxy = s.Models.Proxy
		a.llmReg = llm.NewRegistry(hub.NewHTTPClient(a.llmProxy, 0), llmSecrets)
		a.llmRouter.Registry = a.llmReg
	}
	var cfgs []llm.ProviderConfig
	hasOllama := false
	for _, p := range s.LLM.Providers {
		cfg := llm.ProviderConfig{ID: p.ID, Preset: p.Preset, Name: p.Name, BaseURL: p.BaseURL, Model: p.Model,
			Headers: p.Headers, JSONMode: llm.JSONMode(p.JSONMode)}
		if pid(cfg) == "ollama" {
			hasOllama = true
			if cfg.Model == "" {
				cfg.Model = pickOllamaModel(a.llmOllama)
			}
		}
		cfgs = append(cfgs, cfg)
	}
	auto := !s.LLM.Manual && a.llmOllama != nil
	if auto && !hasOllama {
		cfgs = append(cfgs, llm.ProviderConfig{ID: "ollama", Model: pickOllamaModel(a.llmOllama)})
	}
	a.llmReg.Configure(cfgs)
	rt := llm.Routing{Default: routes(s.LLM.Default)}
	if len(s.LLM.Skills) > 0 {
		rt.Skills = map[llm.Skill][]llm.Route{}
		for k, v := range s.LLM.Skills {
			rt.Skills[llm.Skill(k)] = routes(v)
		}
	}
	if !s.LLM.Manual {
		rt.Default = nil
		if auto {
			rt.Default = []llm.Route{{Provider: "ollama"}}
		}
	}
	a.llmRouter.SetRouting(rt)
	a.llmRouter.SetLocalOnly(s.Privacy.LocalOnlyLLM)
}

func pid(cfg llm.ProviderConfig) string {
	if cfg.Preset != "" {
		return cfg.Preset
	}
	return cfg.ID
}

func routes(rs []settings.LLMRoute) []llm.Route {
	var out []llm.Route
	for _, r := range rs {
		out = append(out, llm.Route{Provider: r.Provider, Model: r.Model, Temperature: r.Temperature})
	}
	return out
}

// pickOllamaModel chooses a chat model among Ollama's: a known family
// first, never an embedding model.
func pickOllamaModel(models []string) string {
	var chat []string
	for _, m := range models {
		if !strings.Contains(strings.ToLower(m), "embed") {
			chat = append(chat, m)
		}
	}
	for _, fam := range []string{"qwen3", "qwen2.5", "gemma3", "llama3", "mistral", "phi"} {
		for _, m := range chat {
			if strings.HasPrefix(strings.ToLower(m), fam) {
				return m
			}
		}
	}
	if len(chat) > 0 {
		return chat[0]
	}
	return ""
}

// checkOllama asks a local Ollama for its models, in the background.
func (a *app) checkOllama() {
	a.background(func() {
		models, err := ollamaModels(context.Background())
		a.post(func() {
			a.llmChecked = true
			if err != nil {
				models = nil
			} else if models == nil {
				models = []string{}
			}
			a.llmOllama = models
			a.applyLLM()
		})
	})
}

// ollamaModels lists the models of the Ollama on this Mac (its own API,
// which answers even before a model is pulled).
func ollamaModels(ctx context.Context) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, 1500*time.Millisecond)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ollamaURL+"/api/tags", nil)
	if err != nil {
		return nil, err
	}
	resp, err := (&http.Client{Transport: &http.Transport{Proxy: nil}}).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, errors.New(resp.Status)
	}
	var body struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, err
	}
	out := []string{}
	for _, m := range body.Models {
		out = append(out, m.Name)
	}
	return out, nil
}

// llmAvailable reports whether clean-up has a model, and which ("Ollama
// · qwen3:4b").
func (a *app) llmAvailable() (bool, string) {
	if a.llmRouter == nil {
		return false, ""
	}
	rs := a.llmRouter.Routes(llm.SkillDictationCleanup)
	if len(rs) == 0 {
		return false, ""
	}
	return true, a.routeName(rs[0])
}

// routeName names a route: the provider's name and its model.
func (a *app) routeName(r llm.Route) string {
	name := providerName(r.Provider, a.prefs.LLM.Providers)
	model := r.Model
	if model == "" {
		model = a.providerModel(r.Provider)
	}
	if model != "" {
		return name + " · " + model
	}
	return name
}

// providerModel is the model a provider uses by default.
func (a *app) providerModel(id string) string {
	for _, p := range a.prefs.LLM.Providers {
		if p.ID == id {
			cfg := llm.ProviderConfig{ID: p.ID, Preset: p.Preset, BaseURL: p.BaseURL, Model: p.Model}
			if p.Model == "" && pid(cfg) == "ollama" {
				return pickOllamaModel(a.llmOllama)
			}
			if pr, err := cfg.Resolve(); err == nil {
				return pr.DefaultModel
			}
		}
	}
	if id == "ollama" {
		return pickOllamaModel(a.llmOllama)
	}
	return ""
}

// providerName is a provider's display name.
func providerName(id string, ps []settings.LLMProvider) string {
	for _, p := range ps {
		if p.ID == id && p.Name != "" {
			return p.Name
		}
		if p.ID == id && p.Preset != "" {
			id = p.Preset
		}
	}
	if pr, ok := llm.LookupPreset(id); ok {
		return pr.Name
	}
	return id
}

// vocabulary is the dictation vocabulary hint as a list of terms.
func vocabulary(s string) []string {
	var out []string
	for _, f := range strings.FieldsFunc(s, func(r rune) bool { return r == '\n' || r == ',' || r == ';' || r == '，' || r == '；' }) {
		if f = strings.TrimSpace(f); f != "" {
			out = append(out, f)
		}
	}
	return out
}

// cleanTranscript asks the language model to clean the transcript shown,
// a part (about 1500 characters of sentences) at a time; parts it cannot
// clean are polished. It runs in the background and can be stopped.
func (a *app) cleanTranscript() {
	tr := a.trDetail
	if tr == nil || a.trClean != nil {
		return
	}
	ok, who := a.llmAvailable()
	if !ok {
		return
	}
	var parts []string
	var cur []core.TranscriptSegment
	size := 0
	for _, s := range tr.Segments {
		cur = append(cur, s)
		size += len([]rune(s.Text))
		if size >= 1500 {
			parts = append(parts, core.Transcript{Segments: cur}.Text())
			cur, size = nil, 0
		}
	}
	if len(cur) > 0 {
		parts = append(parts, core.Transcript{Segments: cur}.Text())
	}
	ctx, cancel := context.WithCancel(context.Background())
	id := tr.ID
	a.trClean = &trCleaning{id: id, cancel: cancel}
	cleaner := &translate.Cleaner{Router: a.llmRouter, Budget: 45 * time.Second}
	language, vocab := tr.Language, vocabulary(a.prefs.Dictation.Vocabulary)
	a.async(func() {
		var out []core.TranscriptSegment
		fallbacks := 0
		for _, p := range parts {
			if ctx.Err() != nil {
				break
			}
			res := cleaner.Clean(ctx, translate.CleanupRequest{Text: p, Lang: language, Vocabulary: vocab})
			if !res.LLM && res.Fallback != translate.FallbackTooShort {
				fallbacks++
			}
			out = append(out, core.TranscriptSegment{Text: res.Text})
		}
		stopped := ctx.Err() != nil
		cancel()
		text := core.Transcript{Segments: out}.Text()
		a.post(func() {
			if a.trClean == nil || a.trClean.id != id {
				return
			}
			a.trClean = nil
			switch {
			case stopped:
				a.toast(i18n.T("Clean-up stopped"))
				return
			case fallbacks == len(parts):
				a.toast(i18n.T("The language model did not answer: nothing was cleaned up. Check it in Settings → Writing help."))
				return
			case fallbacks > 0:
				a.toast(i18n.T("Some parts could not be cleaned up: they are only polished."))
			}
			if a.trDetail != nil && a.trDetail.ID == id {
				a.updateTranscript(func(t *core.Transcript) { t.Clean, t.CleanBy = text, who })
				a.trOrig = false
			}
		})
	})
}

// --- Settings → Writing help --------------------------------------------

// presetLabels are the presets "Add a provider" offers.
func presetLabels() []string {
	var out []string
	for _, p := range llm.Presets() {
		out = append(out, p.Name)
	}
	return out
}

// routeOptions are the choices of "Use for writing help": automatic,
// none, then each provider set up.
func (a *app) routeOptions() (labels, ids []string) {
	labels = []string{i18n.T("Automatic: Ollama on this Mac when it runs"), i18n.T("None: the built-in polish only")}
	ids = []string{"auto", ""}
	for _, p := range a.prefs.LLM.Providers {
		labels, ids = append(labels, providerName(p.ID, a.prefs.LLM.Providers)), append(ids, p.ID)
	}
	return labels, ids
}

// chooseLLMRoute chooses writing help's model: "auto", "" (none) or a
// provider id.
func (a *app) chooseLLMRoute(id string) {
	a.updateSettings(func(s *settings.Settings) {
		s.LLM.Manual = id != "auto"
		s.LLM.Default = nil
		if id != "auto" && id != "" {
			s.LLM.Default = []settings.LLMRoute{{Provider: id}}
		}
	})
	a.applyLLM()
	a.syncLLMForm()
}

// addProvider adds a provider from a preset.
func (a *app) addProvider(label string) {
	for _, p := range llm.Presets() {
		if p.Name != label {
			continue
		}
		id := p.ID
		for n := 2; slices.ContainsFunc(a.prefs.LLM.Providers, func(x settings.LLMProvider) bool { return x.ID == id }); n++ {
			id = p.ID + "-" + itoa(n)
		}
		np := settings.LLMProvider{ID: id, Preset: p.ID}
		a.updateSettings(func(s *settings.Settings) { s.LLM.Providers = append(slices.Clone(s.LLM.Providers), np) })
		a.applyLLM()
		a.syncLLMForm()
		return
	}
}

// removeProvider removes a provider and its routes (its key stays in the
// Keychain until "Remove the key").
func (a *app) removeProvider(id string) {
	a.updateSettings(func(s *settings.Settings) {
		s.LLM.Providers = slices.DeleteFunc(slices.Clone(s.LLM.Providers), func(p settings.LLMProvider) bool { return p.ID == id })
		s.LLM.Default = slices.DeleteFunc(slices.Clone(s.LLM.Default), func(r settings.LLMRoute) bool { return r.Provider == id })
		if s.LLM.Skills != nil {
			m := map[string][]settings.LLMRoute{}
			for k, v := range s.LLM.Skills {
				if v = slices.DeleteFunc(slices.Clone(v), func(r settings.LLMRoute) bool { return r.Provider == id }); len(v) > 0 {
					m[k] = v
				}
			}
			s.LLM.Skills = m
		}
	})
	a.applyLLM()
}

// setProvider changes a provider's fields.
func (a *app) setProvider(id string, fn func(*settings.LLMProvider)) {
	a.updateSettings(func(s *settings.Settings) {
		ps := slices.Clone(s.LLM.Providers)
		for i := range ps {
			if ps[i].ID == id {
				fn(&ps[i])
			}
		}
		s.LLM.Providers = ps
	})
	a.applyLLM()
}

// loadKeyInfo finds whether a provider's key is saved (the Keychain blocks).
func (a *app) loadKeyInfo(id string, p llm.Preset) {
	if p.Local || p.Auth == llm.AuthNone {
		return
	}
	name := llm.ProviderConfig{ID: id, Preset: p.ID}
	pr, err := name.Resolve()
	if err != nil {
		pr = p
	}
	secret := pr.SecretName()
	a.async(func() {
		_, err := llmSecrets.Secret(context.Background(), secret)
		a.post(func() { a.llmForm.hasKey[id] = err == nil })
	})
}

// saveKey keeps a provider's API key in the Keychain ("" removes it).
func (a *app) saveKey(id, key string) {
	cfg := a.providerConfig(id)
	pr, err := cfg.Resolve()
	if err != nil {
		a.toast(i18n.T("Could not save the key: %v", err))
		return
	}
	item := keychainItem(pr.SecretName())
	key = strings.TrimSpace(key)
	if e := a.llmForm.edits[id]; e != nil {
		e.key = ""
	}
	a.async(func() {
		err := item.Set(key)
		a.post(func() {
			switch {
			case err != nil:
				a.toast(i18n.T("Could not save the key: %v", err))
			case key == "":
				a.llmForm.hasKey[id] = false
				a.toast(i18n.T("Key removed"))
			default:
				a.llmForm.hasKey[id] = true
				a.toast(i18n.T("Key saved in the Keychain"))
			}
		})
	})
}

func (a *app) providerConfig(id string) llm.ProviderConfig {
	for _, p := range a.prefs.LLM.Providers {
		if p.ID == id {
			return llm.ProviderConfig{ID: p.ID, Preset: p.Preset, Name: p.Name, BaseURL: p.BaseURL, Model: p.Model, Headers: p.Headers, JSONMode: llm.JSONMode(p.JSONMode)}
		}
	}
	return llm.ProviderConfig{ID: id}
}

// testProvider sends a provider a one-word question and shows the answer
// and how long it took; it lists the provider's models too.
func (a *app) testProvider(id string) {
	b, ok := a.llmReg.Backend(id)
	if !ok {
		a.llmForm.test[id], a.llmForm.testErr[id] = i18n.T("This provider is not set up: check its address."), true
		return
	}
	a.llmForm.testing[id] = true
	a.llmForm.test[id] = ""
	model := a.providerConfig(id).Model
	a.async(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		start := time.Now()
		client := &llm.Client{Backend: b, Timeout: 25 * time.Second, Retry: llm.RetryPolicy{MaxAttempts: 1}}
		resp, err := client.Chat(ctx, llm.Request{Model: model, MaxTokens: 16,
			Messages: []llm.Message{{Role: llm.User, Content: "Reply with the single word OK."}}})
		models, _ := b.Models(ctx)
		elapsed := time.Since(start)
		a.post(func() {
			a.llmForm.testing[id] = false
			if err != nil {
				a.llmForm.test[id], a.llmForm.testErr[id] = llmErrorText(err), true
				return
			}
			a.llmForm.test[id], a.llmForm.testErr[id] = i18n.T("It answered %q in %.1f s (%s).", strings.TrimSpace(resp.Text), elapsed.Seconds(), resp.Model), false
			if len(models) > 0 {
				var names []string
				for _, m := range models {
					names = append(names, m.ID)
				}
				a.llmForm.models[id] = names
			}
		})
	})
}

// llmErrorText explains a language model's error.
func llmErrorText(err error) string {
	var e *llm.Error
	if !errors.As(err, &e) {
		return err.Error()
	}
	switch e.Code {
	case llm.CodeNoKey:
		return i18n.T("No API key: paste one and save it.")
	case llm.CodeAuth:
		return i18n.T("The API key was refused.")
	case llm.CodeRateLimited:
		return i18n.T("Too many requests: try again in a moment.")
	case llm.CodeModelNotFound:
		return i18n.T("The provider does not have the model %s.", e.Params["model"])
	case llm.CodeTimeout:
		return i18n.T("No answer in time.")
	case llm.CodeNetwork:
		return i18n.T("Could not connect: is the server running, and the address right?")
	case llm.CodeConfig:
		return i18n.T("The provider's address is missing or wrong.")
	}
	return err.Error()
}

// writingSettings is Settings → Writing help.
func (a *app) writingSettings(c *ui.Context) {
	t := c.Theme()
	f := &a.llmForm
	if f.edits == nil {
		return
	}
	ui.Text(c, i18n.T("A language model can clean up transcripts and dictations: remove fillers, apply your self-corrections, fix punctuation. Without one, AudioSmith's own polish still runs.")).
		FontSize(px(t, 12)).TextColor(t.TextMuted)
	ok, who := a.llmAvailable()
	status := i18n.T("No language model: clean-up uses the built-in polish only.")
	if ok {
		status = i18n.T("Clean-up uses %s.", who)
	}
	ui.Text(c.Key("llm-status"), status).Bold()

	sectionTitle(c, i18n.T("Which model"))
	labels, ids := a.routeOptions()
	ollama := i18n.T("Ollama is not running on this Mac.")
	if a.llmOllama != nil {
		ollama = i18n.T("Ollama runs on this Mac with %d models.", len(a.llmOllama))
		if len(a.llmOllama) == 0 {
			ollama = i18n.T("Ollama runs on this Mac, but has no model: pull one, e.g. ollama pull qwen3:4b.")
		}
	}
	settingRow(c, "llm-route", i18n.T("Use for writing help"), ollama, func() {
		if ui.Select(c.Key("llm-route-select"), &f.route, labels).Label(i18n.T("Use for writing help")).Width(px(t, 260)).Changed() {
			a.chooseLLMRoute(ids[max(slices.Index(labels, f.route), 0)])
		}
		if ui.Button(c.Key("llm-recheck"), i18n.T("Look for Ollama")).Clicked() {
			a.checkOllama()
		}
	})
	cleanLabels, cleanIDs := cleanOptions(labels, ids)
	settingRow(c, "llm-clean-route", i18n.T("Dictation clean-up"), i18n.T("Dictation waits at most 4 seconds for it: a fast, local model works best."), func() {
		if ui.Select(c.Key("llm-clean-select"), &f.clean, cleanLabels).Label(i18n.T("Dictation clean-up")).Width(px(t, 260)).Changed() {
			id := cleanIDs[max(slices.Index(cleanLabels, f.clean), 0)]
			a.updateSettings(func(s *settings.Settings) {
				m := map[string][]settings.LLMRoute{}
				for k, v := range s.LLM.Skills {
					m[k] = slices.Clone(v)
				}
				delete(m, string(llm.SkillDictationCleanup))
				if id != "" {
					m[string(llm.SkillDictationCleanup)] = []settings.LLMRoute{{Provider: id}}
				}
				s.LLM.Skills = m
			})
			a.applyLLM()
		}
	})
	settingRow(c, "llm-local", i18n.T("Keep text on this Mac"), i18n.T("Only local models (Ollama, LM Studio) are used; cloud providers are skipped."), func() {
		if ui.Switch(c.Key("llm-local-switch"), &f.local).Label(i18n.T("Keep text on this Mac")).Changed() {
			on := f.local
			a.updateSettings(func(s *settings.Settings) { s.Privacy.LocalOnlyLLM = on })
			a.applyLLM()
		}
	})

	sectionTitle(c, i18n.T("Providers"))
	if len(a.prefs.LLM.Providers) == 0 {
		ui.Text(c, i18n.T("No provider set up. Ollama on this Mac is used without one.")).FontSize(px(t, 12)).TextColor(t.TextMuted)
	}
	for _, p := range a.prefs.LLM.Providers {
		a.providerCard(c, p)
	}
	settingRow(c, "llm-add", i18n.T("Add a provider"), i18n.T("OpenAI-compatible servers, Anthropic, and local Ollama or LM Studio."), func() {
		opts := presetLabels()
		if f.add == "" {
			f.add = opts[0]
		}
		ui.Select(c.Key("llm-add-select"), &f.add, opts).Label(i18n.T("Provider")).Width(px(t, 200))
		if ui.Button(c.Key("llm-add-button"), i18n.T("Add")).Clicked() {
			a.addProvider(f.add)
		}
	})
}

// providerCard is one provider: address, model, key, test, remove.
func (a *app) providerCard(c *ui.Context, p settings.LLMProvider) {
	t := c.Theme()
	f := &a.llmForm
	e := f.edits[p.ID]
	if e == nil {
		return
	}
	cfg := a.providerConfig(p.ID)
	pr, resolveErr := cfg.Resolve()
	preset, _ := llm.LookupPreset(pid(cfg))
	ui.Column(c.Key("llm-p-"+p.ID)).Gap(px(t, 8)).Padding(px(t, 12)).Radius(10).Border(1, t.Border).Children(func() {
		ui.Row(c).Gap(px(t, 8)).AlignItems(ui.Center).Children(func() {
			ui.Text(c, providerName(p.ID, a.prefs.LLM.Providers)).Bold().Grow(1)
			where := i18n.T("Cloud: text is sent to this provider")
			if pr.Local || preset.Local {
				where = i18n.T("On this Mac")
			}
			ui.Text(c, where).FontSize(px(t, 11)).TextColor(t.TextMuted)
			if ui.Button(c.Key("test"), i18n.T("Test")).Disabled(f.testing[p.ID]).Clicked() {
				a.testProvider(p.ID)
			}
			if ui.Button(c.Key("remove"), i18n.T("Remove")).Clicked() {
				a.removeProvider(p.ID)
			}
		})
		ui.Field(c, i18n.T("Address (Return saves it)"), func() {
			if ui.TextInput(c.Key("base"), &e.base).Placeholder(preset.BaseURL).Label(i18n.T("Address")).Submitted() {
				v := strings.TrimSpace(e.base)
				a.setProvider(p.ID, func(x *settings.LLMProvider) { x.BaseURL = v })
			}
		})
		if resolveErr != nil {
			ui.Text(c, i18n.T("The provider's address is missing or wrong.")).FontSize(px(t, 12)).TextColor(t.Danger)
		}
		ui.Field(c, i18n.T("Model"), func() {
			if ms := f.models[p.ID]; len(ms) > 0 {
				if e.model == "" {
					e.model = pr.DefaultModel
				}
				if !slices.Contains(ms, e.model) {
					ms = append([]string{e.model}, ms...)
				}
				if ui.Select(c.Key("model-select"), &e.model, ms).Label(i18n.T("Model")).Width(px(t, 260)).Changed() {
					v := e.model
					a.setProvider(p.ID, func(x *settings.LLMProvider) { x.Model = v })
				}
			} else if ui.TextInput(c.Key("model"), &e.model).Placeholder(pr.DefaultModel).Label(i18n.T("Model")).Width(px(t, 260)).Submitted() {
				v := strings.TrimSpace(e.model)
				a.setProvider(p.ID, func(x *settings.LLMProvider) { x.Model = v })
			}
		})
		if !pr.Local && !preset.Local && preset.Auth != llm.AuthNone {
			ui.Text(c, i18n.T("API key")).FontSize(px(t, 12)).TextColor(t.TextMuted)
			ui.Row(c).Gap(px(t, 8)).AlignItems(ui.Center).Children(func() {
				ui.TextInput(c.Key("key"), &e.key).Password().Placeholder(i18n.T("API key")).Label(i18n.T("API key")).Width(px(t, 220))
				if ui.Button(c.Key("key-save"), i18n.T("Save")).Disabled(strings.TrimSpace(e.key) == "").Clicked() {
					a.saveKey(p.ID, e.key)
				}
				if f.hasKey[p.ID] {
					ui.Text(c, i18n.T("A key is saved in the Keychain.")).FontSize(px(t, 12)).TextColor(t.Success)
					if ui.Button(c.Key("key-remove"), i18n.T("Remove the key")).Clicked() {
						a.saveKey(p.ID, "")
					}
				}
			})
		}
		if msg := f.test[p.ID]; msg != "" {
			color := t.Success
			if f.testErr[p.ID] {
				color = t.Danger
			}
			ui.Text(c, msg).FontSize(px(t, 12)).TextColor(color).Selectable()
		} else if f.testing[p.ID] {
			ui.Text(c, i18n.T("Testing…")).FontSize(px(t, 12)).TextColor(t.TextMuted)
		}
	})
}
