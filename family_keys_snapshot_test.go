package main

import (
	"context"
	"testing"

	"github.com/hehehai/AudioSmith/internal/dub"
	"github.com/hehehai/AudioSmith/internal/family"
	"github.com/hehehai/AudioSmith/internal/i18n"
	"github.com/hehehai/actnowkit/credentials"
)

// TestSnapshotsFamilyKeys renders the shared keys with Auralink's release
// build installed (thread #74, family spec §8.3): Settings → Writing help
// with Auralink's DeepSeek key offered and the share checkbox on another
// provider; AudioSmith's own, different DeepSeek key; a shared key being
// changed ("Also updates it in Auralink."); the alert of removing it; and
// Auralink's Hugging Face token offered on Settings → Models. The window
// is tall, so the providers show without scrolling. Without another app
// nothing of it shows (the other snapshot sets stay as they were):
//
//	AUDIOSMITH_SNAPSHOTS=/tmp/fam go test -run TestSnapshotsFamilyKeys .
func TestSnapshotsFamilyKeys(t *testing.T) { runSnapshots(t, snapshotsFamilyKeys) }

func snapshotsFamilyKeys(t *testing.T, out string) {
	t.Cleanup(func() { i18n.SetLocale("en") })
	ctx := context.Background()
	for _, run := range b1Runs {
		if run.size != "1100" && !(run.size == "min" && run.lang == "en") {
			continue
		}
		s := &b1Shooter{t: t, out: out, run: run}
		i18n.SetLocale("en")
		a, _, tt := newTestApp(t)
		s.setup(a, tt)
		f, fx := testFamily(t, a, true)
		fx.Register(auralinkBundle)
		f.Rediscover()
		kc := family.NewFakeKeychain()
		a.lib.keys = f.Keys(kc.Keychain())
		tt.SetSize(run.w, 1400)
		au := auralinkKeys(fx, kc)
		au.Save(ctx, "deepseek", "sk-auralink-a1b2", credentials.SaveOptions{})
		au.Save(ctx, "huggingface", "hf_auralink_wxyz", credentials.SaveOptions{})

		a.addProvider("DeepSeek")
		a.addProvider("OpenAI")
		a.navigate("/settings/writing")
		s.save(tt, "family-keys-offer")

		kc.Put("AudioSmith", "llm.deepseek", "sk-audiosmith-01")
		a.reloadKeys()
		s.save(tt, "family-keys-different")

		ks, _ := llmKeySlot(a.prefs, "deepseek")
		a.useFamilyKey(ks)
		a.llmForm.edits["deepseek"].key = "sk-new-key-0001"
		s.save(tt, "family-keys-updates")
		a.llmForm.edits["deepseek"].key = ""

		a.removeFamilyKey(ks)
		s.save(tt, "family-keys-remove")
		a.keyAsk = false

		a.navigate("/settings/models")
		s.save(tt, "family-keys-hf-offer")
	}
}

// TestSnapshotsFamilyKeysDub renders the online translator's key on the
// Dub page's Translate step (Advanced, DeepL): alone — the key row this
// thread added (before, only a caption named the Keychain item) — and
// with Auralink's DeepL key offered.
//
//	AUDIOSMITH_SNAPSHOTS=/tmp/fam go test -run TestSnapshotsFamilyKeysDub .
func TestSnapshotsFamilyKeysDub(t *testing.T) { runSnapshots(t, snapshotsFamilyKeysDub) }

func snapshotsFamilyKeysDub(t *testing.T, out string) {
	t.Cleanup(func() { i18n.SetLocale("en") })
	for _, run := range b1Runs {
		if run.size != "1100" || run.dark {
			continue
		}
		for _, withAuralink := range []bool{false, true} {
			s := &b1Shooter{t: t, out: out, run: run}
			i18n.SetLocale("en")
			d := newDubTest(t, 6)
			a, tt := d.a, d.tt
			s.setup(a, tt)
			f, fx := testFamily(t, a, true)
			kc := family.NewFakeKeychain()
			if withAuralink {
				fx.Register(auralinkBundle)
				auralinkKeys(fx, kc).Save(context.Background(), "deepl", "deepl-auralink-9f0e", credentials.SaveOptions{})
			}
			f.Rediscover()
			a.lib.keys = f.Keys(kc.Keychain())
			tt.SetSize(run.w, 1400)
			a.navigate("/dub")
			a.dubSourceFile(d.src)
			a.dubStart()
			a.dubTranslate(false)
			a.dubStep = dubStepTranslate
			a.setAdvancedOpen("dub", true)
			a.dubSettings(func(s *dub.Settings) { s.Translator = "deepl" })
			name := "family-keys-mt"
			if withAuralink {
				name += "-offer"
			}
			s.save(tt, name)
		}
	}
}
