package family

import (
	"context"
	"errors"
	"os"
	"slices"
	"testing"

	"github.com/hehehai/actnowkit/conformance"
	"github.com/hehehai/actnowkit/credentials"
)

const auralinkID = "dev.hehehai.auralink"

// keysWith is AudioSmith's keys in a fixture over a fake Keychain, with
// Auralink's release build installed when auralink.
func keysWith(t *testing.T, auralink bool) (*Keys, *FakeKeychain, *conformance.Fixture) {
	t.Helper()
	fx := conformance.New(t)
	f := newFamily(t, fx, "")
	if auralink {
		fx.Register(auralinkID)
	}
	f.Rediscover()
	kc := NewFakeKeychain()
	return f.Keys(kc.Keychain()), kc, fx
}

// peer is another app's credentials store over the same Keychain.
func peer(fx *conformance.Fixture, kc *FakeKeychain, bundleID string) *credentials.Store {
	return &credentials.Store{Family: fx.Family, BundleID: bundleID, Keychain: kc.Keychain()}
}

var ctx = context.Background()

func deepseek(ref string) Slot {
	return Slot{Account: "llm.deepseek", Label: "AudioSmith deepseek API key", Ref: ref}
}

func TestSlotVendor(t *testing.T) {
	cases := []struct{ account, base, want string }{
		{"llm.deepseek", "https://api.deepseek.com/v1", "deepseek"},
		{"llm.openai", "https://api.openai.com/v1", "openai"},
		{"llm.openai", "http://127.0.0.1:8000/v1", ""},    // OpenAI-compatible elsewhere
		{"llm.openai-2", "https://api.openai.com/v1", ""}, // a second provider keeps its own
		{"llm.custom", "https://llm.example.com/v1", ""},
		{"llm.moonshot", "https://api.moonshot.cn/v1", "moonshot"},
		{"llm.moonshot", "https://api.moonshot.ai/v1", "moonshot-intl"},
		{"llm.siliconflow", "https://api.siliconflow.cn/v1", "siliconflow"},
		{"llm.siliconflow", "https://api.siliconflow.com/v1", "siliconflow-intl"},
		{"llm.dashscope-intl", "", "dashscope-intl"},
		{"mt.deepl", "", "deepl"},
		{"mt.google", "", "google-translate"},
		{"mt.microsoft", "", "microsoft-translator"},
		{"mt.google-v3", "", ""}, // an OAuth token
		{"api.key", "", ""},      // AudioSmith's own Local API key
		{"twilio.token", "", ""},
		{"huggingface-token", "", "huggingface"},
	}
	for _, c := range cases {
		if got := (Slot{Account: c.account, BaseURL: c.base}).Vendor(); got != c.want {
			t.Errorf("%s %s: %q, want %q", c.account, c.base, got, c.want)
		}
	}
	if VendorName("deepl", false) != "DeepL" || VendorName("siliconflow", true) != "硅基流动（中国站）" || VendorName("nope", false) != "nope" {
		t.Error("vendor names")
	}
}

// Auralink saved a DeepSeek key: AudioSmith offers it, naming Auralink,
// without reading it; requests do not read it either; "Use this key"
// does, and from then on requests use it.
func TestNoSharedReadBeforeConsent(t *testing.T) {
	k, kc, fx := keysWith(t, true)
	if err := peer(fx, kc, auralinkID).Save(ctx, "deepseek", "sk-auralink-0001", credentials.SaveOptions{}); err != nil {
		t.Fatal(err)
	}
	kc.Forget()
	info, err := k.Info(deepseek(""))
	if err != nil {
		t.Fatal(err)
	}
	if info.Offer == nil || info.From != "Auralink" || info.Own || info.Shared || info.Offer.Hint != "sk-…0001" {
		t.Fatalf("info %+v", info)
	}
	// A request: no ref, so the own item only; a ref without consent
	// (settings copied from elsewhere) reads nothing shared either.
	if v, err := k.Secret(ctx, deepseek("")); v != "" || err != nil {
		t.Errorf("secret %q %v", v, err)
	}
	if v, err := k.Secret(ctx, deepseek(credentials.Ref("deepseek"))); v != "" || err != nil {
		t.Errorf("secret without consent %q %v", v, err)
	}
	if _, err := k.SharedSecret(ctx, hf("")); !errors.Is(err, credentials.ErrNotConsented) {
		t.Errorf("shared HF token without a ref: %v", err)
	}
	if kc.Read(credentials.Service, "vendor.deepseek") {
		t.Fatal("the shared key was read before the user agreed")
	}
	ref, err := k.Use(ctx, deepseek(""))
	if err != nil || ref != "actnow:vendor.deepseek" {
		t.Fatalf("use: %q %v", ref, err)
	}
	if v, err := k.Secret(ctx, deepseek(ref)); v != "sk-auralink-0001" || err != nil {
		t.Errorf("after use: %q %v", v, err)
	}
	info, _ = k.Info(deepseek(ref))
	if !info.Shared || !slices.Equal(info.Others, []string{"Auralink"}) {
		t.Errorf("after use %+v", info)
	}
}

func hf(ref string) Slot {
	return Slot{Account: "huggingface-token", Label: "AudioSmith Hugging Face token", Ref: ref}
}

// Save writes AudioSmith's own item always, the family's with share; a
// shared key saved again replaces it for every app (Also updates it in
// Auralink); saving unshared stops using the shared one.
func TestSaveShare(t *testing.T) {
	k, kc, fx := keysWith(t, true)
	ref, err := k.Save(ctx, deepseek(""), " sk-mine-00000001 ", false)
	if err != nil || ref != "" {
		t.Fatalf("unshared: %q %v", ref, err)
	}
	if v, _ := kc.Item("AudioSmith", "llm.deepseek"); v != "sk-mine-00000001" {
		t.Errorf("own item %q", v)
	}
	if _, ok := kc.Item(credentials.Service, "vendor.deepseek"); ok {
		t.Error("shared without the checkbox")
	}
	ref, err = k.Save(ctx, deepseek(""), "sk-mine-00000002", true)
	if err != nil || ref != "actnow:vendor.deepseek" {
		t.Fatalf("shared: %q %v", ref, err)
	}
	if v, _ := kc.Item(credentials.Service, "vendor.deepseek"); v != "sk-mine-00000002" {
		t.Errorf("shared item %q", v)
	}
	if v, _ := kc.Item("AudioSmith", "llm.deepseek"); v != "sk-mine-00000002" {
		t.Errorf("own item not kept current: %q", v)
	}
	au := peer(fx, kc, auralinkID)
	if _, err := au.Use(ctx, "deepseek"); err != nil {
		t.Fatal(err)
	}
	if _, err := k.Save(ctx, deepseek(ref), "sk-mine-00000003", true); err != nil {
		t.Fatal(err)
	}
	if v, _ := au.Secret(ctx, "deepseek"); v != "sk-mine-00000003" {
		t.Errorf("Auralink reads %q after the update", v)
	}
	ref, err = k.Save(ctx, deepseek(ref), "sk-mine-00000004", false)
	if err != nil || ref != "" || k.Shared.Using("deepseek") {
		t.Errorf("saved unshared: %q %v using %v", ref, err, k.Shared.Using("deepseek"))
	}
	if v, _ := au.Secret(ctx, "deepseek"); v != "sk-mine-00000003" {
		t.Errorf("Auralink's key changed by an unshared save: %q", v)
	}
	// Not a vendor of the table: never shared.
	if ref, _ := k.Save(ctx, Slot{Account: "mt.google-v3"}, "ya29.token-abcdef", true); ref != "" {
		t.Errorf("google-v3 shared: %q", ref)
	}
}

// Removing a key Auralink uses too: "Stop using it here" keeps it for
// Auralink, "Delete for all apps" deletes it; a key nobody else uses is
// deleted. AudioSmith's own item goes in every case.
func TestRemoveStopUsingOrDeleteForAll(t *testing.T) {
	k, kc, fx := keysWith(t, true)
	au := peer(fx, kc, auralinkID)
	ref, _ := k.Save(ctx, deepseek(""), "sk-shared-000001", true)
	au.Use(ctx, "deepseek")
	if err := k.Remove(ctx, deepseek(ref), false); err != nil {
		t.Fatal(err)
	}
	if _, ok := kc.Item(credentials.Service, "vendor.deepseek"); !ok {
		t.Fatal("stop using deleted Auralink's key")
	}
	if _, ok := kc.Item("AudioSmith", "llm.deepseek"); ok {
		t.Error("own item kept")
	}
	if e, _ := k.Shared.Offer("deepseek"); !slices.Equal(e.Users, []string{auralinkID}) {
		t.Errorf("users %v", e.Users)
	}
	ref, _ = k.Use(ctx, deepseek(""))
	if err := k.Remove(ctx, deepseek(ref), true); err != nil {
		t.Fatal(err)
	}
	if _, ok := kc.Item(credentials.Service, "vendor.deepseek"); ok {
		t.Error("delete for all kept the item")
	}
	if _, ok := k.Shared.Offer("deepseek"); ok {
		t.Error("delete for all kept the metadata")
	}
	// Alone: deleted.
	ref, _ = k.Save(ctx, Slot{Account: "mt.deepl"}, "deepl-key-000001", true)
	if err := k.Remove(ctx, Slot{Account: "mt.deepl", Ref: ref}, false); err != nil {
		t.Fatal(err)
	}
	if _, ok := kc.Item(credentials.Service, "vendor.deepl"); ok {
		t.Error("a key only AudioSmith used stayed")
	}
}

// Sharing the key AudioSmith already had copies it, checks it, and
// keeps AudioSmith's own item.
func TestShareMigratesLegacy(t *testing.T) {
	k, kc, _ := keysWith(t, true)
	kc.Put("AudioSmith", "huggingface-token", "hf_legacy_token_1234")
	ref, err := k.Share(ctx, hf(""))
	if err != nil || ref != "actnow:vendor.huggingface" {
		t.Fatalf("share: %q %v", ref, err)
	}
	if v, _ := kc.Item(credentials.Service, "vendor.huggingface"); v != "hf_legacy_token_1234" {
		t.Errorf("shared item %q", v)
	}
	if v, ok := kc.Item("AudioSmith", "huggingface-token"); !ok || v != "hf_legacy_token_1234" {
		t.Error("the legacy item went")
	}
	if v, err := k.SharedSecret(ctx, hf(ref)); v != "hf_legacy_token_1234" || err != nil {
		t.Errorf("shared token %q %v", v, err)
	}
	if _, err := k.Share(ctx, Slot{Account: "api.key"}); err == nil {
		t.Error("api.key shared")
	}
}

// AudioSmith's own key and Auralink's differ: TextDifferentKey; the same
// key: no prompt.
func TestDifferentKey(t *testing.T) {
	k, kc, fx := keysWith(t, true)
	peer(fx, kc, auralinkID).Save(ctx, "deepseek", "sk-auralink-0001", credentials.SaveOptions{})
	kc.Put("AudioSmith", "llm.deepseek", "sk-audiosmith-01")
	info, _ := k.Info(deepseek(""))
	if !info.Different || !info.Own || info.From != "Auralink" {
		t.Errorf("different: %+v", info)
	}
	kc.Put("AudioSmith", "llm.deepseek", "sk-auralink-0001")
	if info, _ := k.Info(deepseek("")); info.Different {
		t.Error("the same key counts as different")
	}
	if kc.Read(credentials.Service, "vendor.deepseek") {
		t.Error("Info read the shared key")
	}
}

// Only installed release builds are named: Auralink's dev build makes no
// offer; nor does a key AudioSmith alone saved.
func TestOfferNamesReleaseBuildsOnly(t *testing.T) {
	k, kc, fx := keysWith(t, false)
	fx.Register(auralinkID + ".dev")
	k.fam.Rediscover()
	peer(fx, kc, auralinkID+".dev").Save(ctx, "deepseek", "sk-dev-000000001", credentials.SaveOptions{})
	info, _ := k.Info(deepseek(""))
	if info.Offer == nil || info.From != "" || len(info.Others) != 0 || len(info.OtherUsers) != 1 {
		t.Errorf("dev build: %+v", info)
	}
	k2, _, _ := keysWith(t, false)
	ref, _ := k2.Save(ctx, deepseek(""), "sk-alone-0000001", true)
	if info, _ := k2.Info(deepseek(ref)); !info.Shared || info.From != "" || len(info.OtherUsers) != 0 {
		t.Errorf("alone: %+v", info)
	}
}

// Erase everything: AudioSmith leaves every shared key's users; none is
// deleted, not even one only AudioSmith used.
func TestStopUsingAll(t *testing.T) {
	k, kc, fx := keysWith(t, true)
	k.Save(ctx, deepseek(""), "sk-shared-000001", true)
	peer(fx, kc, auralinkID).Use(ctx, "deepseek")
	k.Save(ctx, Slot{Account: "mt.deepl"}, "deepl-key-000001", true)
	peer(fx, kc, auralinkID).Save(ctx, "openai", "sk-openai-000001", credentials.SaveOptions{})
	if err := k.StopUsingAll(ctx); err != nil {
		t.Fatal(err)
	}
	for _, v := range []string{"deepseek", "deepl", "openai"} {
		if _, ok := kc.Item(credentials.Service, "vendor."+v); !ok {
			t.Errorf("%s deleted", v)
		}
		if k.Shared.Using(v) {
			t.Errorf("still using %s", v)
		}
	}
	if !peer(fx, kc, auralinkID).Using("deepseek") {
		t.Error("Auralink was taken off too")
	}
}

// credentials.json of a newer spec: nothing is shared, the own item is
// still saved.
func TestReadOnlyCredentials(t *testing.T) {
	k, kc, fx := keysWith(t, true)
	if err := os.MkdirAll(fx.Family.Root, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fx.Family.CredentialsFile(), []byte(`{"schema":"actnow.credentials/2","rev":1,"vendors":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	ref, err := k.Save(ctx, deepseek(""), "sk-mine-00000001", true)
	if err != nil || ref != "" {
		t.Errorf("read only: %q %v", ref, err)
	}
	if _, ok := kc.Item(credentials.Service, "vendor.deepseek"); ok {
		t.Error("shared into a newer format")
	}
	if info, _ := k.Info(deepseek("")); !info.ReadOnly || !info.Own {
		t.Errorf("info %+v", info)
	}
}
