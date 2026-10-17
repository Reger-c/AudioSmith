package main

// The ActNow family, phase 1 P4b (family spec v1.0.4 §8): API keys and the
// Hugging Face token shared with the other ActNow apps, through
// internal/family Keys (the family's Keychain item "ActNow" /
// "vendor.<id>" and credentials.json). This file: which settings hold a
// key's credentialRef, the resolver requests read keys through, and the
// actions of Settings (save, use another app's key, share, remove). The
// views are in family_keys_view.go.
//
// Without another ActNow app's release build the UI is what it was, yet a
// key saved is shared all the same (the checkbox, on by default, is not
// shown: spec §7.10, appearance rule 2), so that an app installed later
// can offer it.

import (
	"context"
	"errors"
	"log"
	"strings"
	"time"

	"github.com/hehehai/AudioSmith/internal/family"
	"github.com/hehehai/AudioSmith/internal/hub"
	"github.com/hehehai/AudioSmith/internal/i18n"
	"github.com/hehehai/AudioSmith/internal/llm"
	"github.com/hehehai/AudioSmith/internal/settings"
	"github.com/hehehai/AudioSmith/internal/translate"
	"github.com/hehehai/actnowkit/credentials"
)

// keySlot is one key Settings shows, and where the settings keep its
// credentialRef.
type keySlot struct {
	id   string      // "llm:<provider>", "mt:<service>", "hf"
	slot family.Slot // its Keychain item and the ref the settings hold
	set  func(s *settings.Settings, ref string)
}

// llmKeySlot is a writing-help provider's key; ok false without one (a
// local provider, or no such provider).
func llmKeySlot(s settings.Settings, id string) (keySlot, bool) {
	for _, p := range s.LLM.Providers {
		if p.ID != id {
			continue
		}
		pr, err := llm.ProviderConfig{ID: p.ID, Preset: p.Preset, BaseURL: p.BaseURL}.Resolve()
		if err != nil || pr.Local || pr.Auth == llm.AuthNone {
			return keySlot{}, false
		}
		name := pr.SecretName()
		return keySlot{id: "llm:" + id,
			slot: family.Slot{Account: name, Label: keychainItem(name).Label, BaseURL: pr.BaseURL, Ref: p.CredentialRef},
			set: func(s *settings.Settings, ref string) {
				for i := range s.LLM.Providers {
					if s.LLM.Providers[i].ID == id {
						s.LLM.Providers[i].CredentialRef = ref
					}
				}
			}}, true
	}
	return keySlot{}, false
}

// mtKeySlot is an online translator's key ("deepl", "google",
// "microsoft").
func mtKeySlot(s settings.Settings, svc string) keySlot {
	name := translate.MTSecretPrefix + svc
	return keySlot{id: "mt:" + svc,
		slot: family.Slot{Account: name, Label: keychainItem(name).Label, Ref: s.Dubbing.CredentialRefs[svc]},
		set: func(s *settings.Settings, ref string) {
			m := map[string]string{}
			for k, v := range s.Dubbing.CredentialRefs {
				m[k] = v
			}
			delete(m, svc)
			if ref != "" {
				m[svc] = ref
			}
			if len(m) == 0 {
				m = nil
			}
			s.Dubbing.CredentialRefs = m
		}}
}

// hfKeySlot is the Hugging Face token.
func hfKeySlot(s settings.Settings) keySlot {
	return keySlot{id: "hf",
		slot: family.Slot{Account: hub.DefaultKeychain.Account, Label: hub.DefaultKeychain.Label, Ref: s.Models.HFCredentialRef},
		set:  func(s *settings.Settings, ref string) { s.Models.HFCredentialRef = ref }}
}

// secretSlot is the slot of a key requests ask for by name (provider: the
// writing-help provider asking; "" = any whose key it is).
func secretSlot(s settings.Settings, provider, name string) (keySlot, bool) {
	switch {
	case strings.HasPrefix(name, translate.MTSecretPrefix):
		return mtKeySlot(s, strings.TrimPrefix(name, translate.MTSecretPrefix)), true
	case provider != "":
		ks, ok := llmKeySlot(s, provider)
		return ks, ok && ks.slot.Account == name
	}
	for _, p := range s.LLM.Providers {
		if ks, ok := llmKeySlot(s, p.ID); ok && ks.slot.Account == name && ks.slot.Ref != "" {
			return ks, true
		}
	}
	return keySlot{}, false
}

// familySecrets are the keys requests read (llm.Secrets and
// llm.ProviderSecrets): a key with a credentialRef through the family's
// Keys (the shared key, else AudioSmith's own item), the others as
// before (legacy: AudioSmith's own items).
type familySecrets struct {
	keys   *family.Keys
	prefs  func() settings.Settings
	legacy llm.Secrets
	// gone ends a slot whose shared key was deleted (dropSharedKey).
	gone func(keySlot)
}

func (f familySecrets) Secret(ctx context.Context, name string) (string, error) {
	return f.ProviderSecret(ctx, "", name)
}

func (f familySecrets) ProviderSecret(ctx context.Context, provider, name string) (string, error) {
	ks, ok := secretSlot(f.prefs(), provider, name)
	if !ok || ks.slot.Ref == "" {
		return f.legacy.Secret(ctx, name)
	}
	v, err := f.keys.Secret(ctx, ks.slot)
	if errors.Is(err, family.ErrSharedGone) {
		// Deleted for all apps: a missing key, unless AudioSmith has a
		// different one of its own (family_keys_gone.go).
		if f.gone != nil {
			f.gone(ks)
		}
		ks.slot.Ref = ""
		v, err = f.keys.Secret(ctx, ks.slot)
	}
	if err == nil && v == "" {
		err = llm.ErrNoSecret
	}
	return v, err
}

// connectKeys makes the family's keys (connectFamily): requests read
// keys through them, and downloads the shared HF token.
func (a *app) connectKeys() {
	if a.fam == nil {
		return
	}
	a.lib.keys = a.fam.Keys(credentials.Keychain{})
	a.lib.keyGone = a.dropSharedKey // family_keys_gone.go
	llmSecrets = familySecrets{keys: a.lib.keys, prefs: a.lib.settings.Get, legacy: llmSecrets, gone: a.dropSharedKey}
}

// sharedHFToken reads the family's shared HF token for downloads (spec
// §8.4: after $HF_TOKEN, before AudioSmith's own item); nil when the
// settings name none.
func (l *library) sharedHFToken() func() (string, error) {
	k := l.keys
	if k == nil {
		return nil
	}
	ks := hfKeySlot(l.settings.Get())
	if ks.slot.Ref == "" {
		return nil
	}
	gone := l.keyGone
	return func() (string, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		t, err := k.SharedSecret(ctx, ks.slot)
		if errors.Is(err, family.ErrSharedGone) && gone != nil {
			gone(ks) // then the own token, when one is left
		}
		return t, err
	}
}

// familyKeys is Settings' state of the keys (UI thread).
type familyKeys struct {
	keyViews map[string]*keyView // by keySlot.id
	// keyAsk is the alert "Auralink also uses this key." of removing
	// keyAskSlot's shared key.
	keyAsk      bool
	keyAskSlot  keySlot
	keyAskNames []string
	// mtKeys are the online translators' key inputs, by service.
	mtKeys map[string]*string
}

// keyView is one key's state in Settings.
type keyView struct {
	info  family.KeyInfo
	known bool // info was read
	// another: "Enter another key" instead of the offer of another app's.
	another bool
	// share is the checkbox "Let other ActNow apps on this Mac use this
	// key"; shareSet: it was set from info once.
	share, shareSet bool
	busy            bool
}

func (a *app) keyView(id string) *keyView {
	if a.keyViews == nil {
		a.keyViews = map[string]*keyView{}
	}
	v := a.keyViews[id]
	if v == nil {
		v = &keyView{}
		a.keyViews[id] = v
	}
	return v
}

// familyUI reports whether family texts may show: another ActNow app's
// release build is installed (spec §7.10).
func (a *app) familyUI() []string {
	if a.fam == nil {
		return nil
	}
	return a.fam.OthersForUI()
}

// loadKey reads what Settings shows of a key, in the background (the
// Keychain blocks); then runs after it, when not nil, on the UI thread.
func (a *app) loadKey(ks keySlot, then func(family.KeyInfo)) {
	keys := a.lib.keys
	if keys == nil {
		return
	}
	a.async(func() {
		info, err := keys.Info(ks.slot)
		a.post(func() {
			if err != nil {
				log.Printf("keys: %s: %v", ks.slot.Account, err)
			}
			v := a.keyView(ks.id)
			v.info, v.known = info, true
			if !v.shareSet {
				// On for a new key; an own key not shared yet shows off.
				v.share, v.shareSet = !info.Own || info.Shared, true
			}
			if then != nil {
				then(info)
			}
		})
	})
}

// reloadKeys reads the keys Settings shows again (another app changed
// credentials.json).
func (a *app) reloadKeys() {
	for id := range a.keyViews {
		if ks, ok := a.keySlotByID(id); ok {
			a.loadKey(ks, a.keyLoaded(ks))
		}
	}
}

// keySlotByID finds a slot from its id, with the settings as they are.
func (a *app) keySlotByID(id string) (keySlot, bool) {
	switch {
	case id == "hf":
		return hfKeySlot(a.prefs), true
	case strings.HasPrefix(id, "mt:"):
		return mtKeySlot(a.prefs, strings.TrimPrefix(id, "mt:")), true
	case strings.HasPrefix(id, "llm:"):
		return llmKeySlot(a.prefs, strings.TrimPrefix(id, "llm:"))
	}
	return keySlot{}, false
}

// keyLoaded keeps what the older views keep of a key (the writing-help
// card's "A key is saved", the HF token's source).
func (a *app) keyLoaded(ks keySlot) func(family.KeyInfo) {
	return func(info family.KeyInfo) {
		switch {
		case strings.HasPrefix(ks.id, "llm:"):
			if a.llmForm.hasKey != nil {
				a.llmForm.hasKey[strings.TrimPrefix(ks.id, "llm:")] = info.Own || info.Shared
			}
		case ks.id == "hf":
			a.loadTokenInfo()
		}
	}
}

// keyDone sets the slot's new ref, reads the key again and says how it
// went: msg (already translated; "" = nothing to say).
// fp is the shared key's fingerprint (keyRef), kept with the ref.
func (a *app) keyDone(ks keySlot, ref, fp, msg string) {
	v := a.keyView(ks.id)
	v.busy = false
	if ref != ks.slot.Ref || keyFingerprint(a.prefs, ks) != fp {
		a.updateSettings(func(s *settings.Settings) { setKeyRef(s, ks, ref, fp) })
		ks.slot.Ref = ref
		if strings.HasPrefix(ks.id, "llm:") {
			a.applyLLM()
		}
		if ks.id == "hf" {
			a.lib.resetHub()
		}
	}
	if msg != "" {
		a.toast(msg)
	}
	a.loadKey(ks, a.keyLoaded(ks))
}

// saveFamilyKey keeps a key the user typed (Save): AudioSmith's own item
// and, with the checkbox (or without one shown), the shared one.
func (a *app) saveFamilyKey(ks keySlot, secret string) {
	keys, v := a.lib.keys, a.keyView(ks.id)
	// The checkbox is on by default, and when it is not shown.
	share := v.share || !v.shareSet || len(a.familyUI()) == 0 || v.info.Shared
	v.busy, v.another = true, false
	a.async(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		ref, err := keys.Save(ctx, ks.slot, secret, share)
		fp := keyRef(keys, ks, ref)
		msg := savedText(ks)
		if err != nil {
			msg = i18n.T("Could not save the key: %v", err)
		}
		a.post(func() { a.keyDone(ks, ref, fp, msg) })
	})
}

func savedText(ks keySlot) string {
	if ks.id == "hf" {
		return i18n.T("Token saved in the Keychain")
	}
	return i18n.T("Key saved in the Keychain")
}

// useFamilyKey is "Use this key": only now is the shared key read.
func (a *app) useFamilyKey(ks keySlot) {
	keys, v := a.lib.keys, a.keyView(ks.id)
	v.busy = true
	a.async(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		ref, err := keys.Use(ctx, ks.slot)
		fp := keyRef(keys, ks, ref)
		msg := ""
		if err != nil {
			msg = i18n.T("Could not use the shared key: %v", err)
		}
		a.post(func() { a.keyDone(ks, ref, fp, msg) })
	})
}

// shareFamilyKey turns the checkbox on for a key AudioSmith had: it is
// copied to the family's item (AudioSmith's own stays).
func (a *app) shareFamilyKey(ks keySlot) {
	keys, v := a.lib.keys, a.keyView(ks.id)
	v.busy = true
	a.async(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		ref, err := keys.Share(ctx, ks.slot)
		fp := keyRef(keys, ks, ref)
		a.post(func() {
			msg := ""
			if err != nil {
				v.share, msg = false, i18n.T("Could not share the key: %v", err)
			}
			a.keyDone(ks, ref, fp, msg)
		})
	})
}

// removeFamilyKey is "Remove the key": when other apps use the shared
// key, the alert asks first (Stop using it here / Delete for all apps).
func (a *app) removeFamilyKey(ks keySlot) {
	info := a.keyView(ks.id).info
	if info.Shared && len(info.Others) > 0 {
		a.keyAsk, a.keyAskSlot, a.keyAskNames = true, ks, info.Others
		return
	}
	a.removeFamilyKeyNow(ks, false)
}

func (a *app) removeFamilyKeyNow(ks keySlot, forAll bool) {
	keys, v := a.lib.keys, a.keyView(ks.id)
	v.busy = true
	a.async(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		err := keys.Remove(ctx, ks.slot, forAll)
		ref, fp := ks.slot.Ref, keyRef(keys, ks, ks.slot.Ref)
		if err == nil {
			ref, fp = "", ""
		}
		a.post(func() {
			v.share, v.shareSet = true, true // a new key is shared by default again
			msg := i18n.T("Key removed")
			switch {
			case err != nil:
				msg = i18n.T("Could not remove the key: %v", err)
			case ks.id == "hf":
				msg = i18n.T("Token removed")
			}
			a.keyDone(ks, ref, fp, msg)
		})
	})
}

// forgetFamilyKeys takes AudioSmith off every shared key's users, deleting
// none (Erase everything; it blocks).
func (a *app) forgetFamilyKeys() {
	if a.lib.keys == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := a.lib.keys.StopUsingAll(ctx); err != nil && !errors.Is(err, context.Canceled) {
		log.Printf("erase: shared keys: %v", err)
	}
}
