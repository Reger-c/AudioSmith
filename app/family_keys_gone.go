package main

// Shared keys deleted elsewhere (family spec §8.3; main's decision on
// thread #74): another app's "Delete for all apps" — a leaked key, say —
// or the item deleted in Keychain Access. AudioSmith then treats the key
// as missing: the slot's credentialRef goes, and AudioSmith's own copy of
// the same key (same fingerprint as the shared key last seen) goes too;
// an own key that differs, typed for AudioSmith alone, stays and is used.
// It is found when a request reads the key (familySecrets, the HF token)
// and when credentials.json changes (checkSharedKeys).

import (
	"context"
	"log"
	"strings"
	"time"

	"github.com/hehehai/AudioSmith/internal/family"
	"github.com/hehehai/AudioSmith/internal/i18n"
	"github.com/hehehai/AudioSmith/internal/settings"
)

// keyRef is the fingerprint of the shared key ref names for ks
// (credentials.json; no secret is read); "" without a ref.
func keyRef(keys *family.Keys, ks keySlot, ref string) string {
	if ref == "" {
		return ""
	}
	s := ks.slot
	s.Ref = ref
	return keys.Fingerprint(s)
}

// keyFingerprint is the fingerprint the settings keep for ks's vendor.
func keyFingerprint(s settings.Settings, ks keySlot) string {
	return s.Family.KeyFingerprints[ks.slot.Vendor()]
}

// setKeyRef sets ks's ref and the fingerprint kept with it ("" clears).
func setKeyRef(s *settings.Settings, ks keySlot, ref, fp string) {
	ks.set(s, ref)
	v := ks.slot.Vendor()
	if v == "" {
		return
	}
	m := map[string]string{}
	for k, x := range s.Family.KeyFingerprints {
		m[k] = x
	}
	delete(m, v)
	if ref != "" && fp != "" {
		m[v] = fp
	}
	if len(m) == 0 {
		m = nil
	}
	s.Family.KeyFingerprints = m
}

// refSlots are the keys whose settings name a shared key.
func refSlots(s settings.Settings) []keySlot {
	var out []keySlot
	for _, p := range s.LLM.Providers {
		if ks, ok := llmKeySlot(s, p.ID); ok && ks.slot.Ref != "" {
			out = append(out, ks)
		}
	}
	for svc, ref := range s.Dubbing.CredentialRefs {
		if ref != "" {
			out = append(out, mtKeySlot(s, svc))
		}
	}
	if hf := hfKeySlot(s); hf.slot.Ref != "" {
		out = append(out, hf)
	}
	return out
}

// dropSharedKey ends ks's shared key, deleted elsewhere: AudioSmith's own
// copy of it goes, the ref goes, and Settings asks for a key again. Any
// goroutine; it blocks (the Keychain).
func (a *app) dropSharedKey(ks keySlot) {
	keys, st := a.lib.keys, a.lib.settings
	if keys == nil {
		return
	}
	cur := st.Get()
	now, ok := a.slotNow(cur, ks)
	if !ok || now.slot.Ref == "" || now.slot.Ref != ks.slot.Ref {
		return // already dropped (a request and a notification at once)
	}
	if _, err := keys.DropShared(now.slot, keyFingerprint(cur, now)); err != nil {
		log.Printf("keys: %s: %v", now.slot.Account, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := st.Update(ctx, func(s *settings.Settings) { setKeyRef(s, now, "", "") }); err != nil {
		log.Printf("keys: %s: %v", now.slot.Account, err)
	}
	log.Printf("keys: the shared %s key was deleted; %s asks for a key again", now.slot.Vendor(), now.slot.Account)
	a.post(func() {
		if len(a.familyUI()) > 0 {
			a.toast(i18n.T("The shared %s key was deleted. Enter a key again.", vendorName(now.slot.Vendor())))
		}
		if strings.HasPrefix(now.id, "llm:") {
			a.applyLLM()
		}
		if _, shown := a.keyViews[now.id]; shown {
			a.loadKey(now, a.keyLoaded(now))
		}
	})
}

// slotNow is ks with the settings s (its ref as it is now).
func (a *app) slotNow(s settings.Settings, ks keySlot) (keySlot, bool) {
	switch {
	case ks.id == "hf":
		return hfKeySlot(s), true
	case strings.HasPrefix(ks.id, "mt:"):
		return mtKeySlot(s, strings.TrimPrefix(ks.id, "mt:")), true
	}
	return llmKeySlot(s, strings.TrimPrefix(ks.id, "llm:"))
}

// checkSharedKeys looks at the shared keys the settings name after
// credentials.json changed (and at start): those deleted are dropped, and
// the fingerprints of those another app changed are kept up to date. It
// reads no secret unless one was deleted (then AudioSmith's own item, to
// compare). Any goroutine.
func (a *app) checkSharedKeys() {
	keys, st := a.lib.keys, a.lib.settings
	if keys == nil {
		return
	}
	cur := st.Get()
	type update struct {
		ks keySlot
		fp string
	}
	var updates []update
	for _, ks := range refSlots(cur) {
		if keys.Gone(ks.slot) {
			a.dropSharedKey(ks)
			continue
		}
		if fp := keys.Fingerprint(ks.slot); fp != "" && fp != keyFingerprint(cur, ks) {
			updates = append(updates, update{ks, fp})
		}
	}
	if len(updates) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := st.Update(ctx, func(s *settings.Settings) {
		for _, u := range updates {
			if now, ok := a.slotNow(*s, u.ks); ok && now.slot.Ref == u.ks.slot.Ref {
				setKeyRef(s, now, now.slot.Ref, u.fp)
			}
		}
	}); err != nil {
		log.Printf("keys: fingerprints: %v", err)
	}
}
