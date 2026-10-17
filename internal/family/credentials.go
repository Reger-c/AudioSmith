package family

// Shared credentials (spec §8; plan P4b): AudioSmith's API keys and its
// Hugging Face token are its own Keychain items ("AudioSmith" /
// "llm.deepseek", "mt.deepl", "huggingface-token"), and — when the user
// lets the other ActNow apps use one — the family's item "ActNow" /
// "vendor.<id>" with its metadata in $FAMILY/credentials.json. A key's
// credentialRef ("actnow:vendor.deepseek", kept in AudioSmith's
// settings) says AudioSmith uses the shared one.
//
// Rules this file keeps:
//   - a shared secret is read only once AudioSmith is among the vendor's
//     users, which only Save (the user typed the key), Use (the user
//     clicked "Use this key") and Share (the user shared the key AudioSmith
//     already had) make it; Info never reads one;
//   - AudioSmith's own item is written too, and kept when a key is shared
//     (MigrateLegacy), so an older AudioSmith still finds it; it goes when
//     the user removes the key;
//   - a shared key deleted elsewhere ("Delete for all apps" in another
//     app, or in Keychain Access) is a missing key (spec §8.3): Secret
//     says ErrSharedGone, and DropShared deletes AudioSmith's own copy
//     when it is the same key (by fingerprint), so that a key deleted for
//     all apps — a leaked one — is gone here too; an own key that differs
//     (typed for AudioSmith alone) stays;
//   - "Stop using it here" and Erase everything only take AudioSmith off
//     the users: another app's key is deleted only by "Delete for all
//     apps", or when AudioSmith is its only user;
//   - not shared: keys the vendor table does not know for AudioSmith
//     (mt.google-v3, api.key, twilio.token, custom providers, a second
//     provider of a vendor, OpenAI on another endpoint).

import (
	"context"
	"errors"
	"slices"
	"strings"

	"github.com/hehehai/actnowkit/credentials"
	actnow "github.com/hehehai/actnowkit/family"
)

// Slot is one key AudioSmith keeps.
type Slot struct {
	Account string // its own Keychain item: "llm.deepseek", "mt.deepl", "huggingface-token"
	Label   string // the own item's label in Keychain Access ("" = "AudioSmith <account>")
	// BaseURL is the provider's endpoint, which decides the site of
	// AudioSmith's llm.moonshot and llm.siliconflow (and OpenAI's
	// official endpoint); "" for the rest.
	BaseURL string
	Ref     string // its credentialRef in the settings; "" = the own item only
}

// Vendor is the slot's family vendor id; "" when the key is not shared.
func (s Slot) Vendor() string {
	v, ok := credentials.VendorForLegacy(AppID, s.Account, s.BaseURL)
	if !ok {
		return ""
	}
	return v.ID
}

// VendorName is the vendor's display name for the UI ("DeepSeek";
// zh: the Chinese one when the table has it).
func VendorName(vendor string, zh bool) string {
	v, ok := credentials.VendorByID(vendor)
	switch {
	case !ok:
		return vendor
	case zh && v.NameZH != "":
		return v.NameZH
	}
	return v.Name
}

// Keys are AudioSmith's keys, its own and the family's shared ones.
// Every method may block (the Keychain runs /usr/bin/security): call
// them off the UI thread.
type Keys struct {
	Shared *credentials.Store
	Own    credentials.Keychain // service "AudioSmith"
	fam    *Family
}

// Keys returns AudioSmith's keys over kc: the zero Keychain is the login
// keychain through /usr/bin/security; tests pass one whose Run is a fake
// (both services go through it).
func (f *Family) Keys(kc credentials.Keychain) *Keys {
	shared := kc
	shared.Service = ""
	own := kc
	own.Service = keychainService
	return &Keys{Shared: &credentials.Store{Family: f.Dir, BundleID: f.BundleID, Keychain: shared}, Own: own, fam: f}
}

// own reads AudioSmith's own item; "" when there is none (or no
// Keychain: Linux tests).
func (k *Keys) own(account string) (string, error) {
	v, err := k.Own.Get(account)
	if errors.Is(err, credentials.ErrNotFound) || errors.Is(err, credentials.ErrNoKeychain) {
		return "", nil
	}
	return strings.TrimSpace(v), err
}

// sharedVendor is the vendor of the shared key the slot uses: its ref's,
// when it names one the slot may share; "" otherwise.
func sharedVendor(s Slot) string {
	v, ok := credentials.ParseRef(s.Ref)
	if !ok || v != s.Vendor() {
		return ""
	}
	return v
}

// ErrSharedGone: the shared key the slot's ref names was deleted
// (another app's "Delete for all apps", or Keychain Access). The key is
// missing: the app drops the ref (DropShared) and asks for a key again.
var ErrSharedGone = errors.New("the shared key was deleted")

// Secret is the slot's key as requests use it: the shared one when the
// slot's ref names it (and AudioSmith agreed to use it), else AudioSmith's
// own item; "" when there is none. ErrSharedGone when the shared key the
// ref names was deleted.
func (k *Keys) Secret(ctx context.Context, s Slot) (string, error) {
	if v := sharedVendor(s); v != "" {
		secret, err := k.shared(ctx, v)
		switch {
		case err == nil && strings.TrimSpace(secret) != "":
			return strings.TrimSpace(secret), nil
		case errors.Is(err, ErrSharedGone):
			return "", err
		case err != nil && !errors.Is(err, credentials.ErrNotConsented):
			k.fam.logf("family: shared %s key: %v", v, err)
		}
	}
	return k.own(s.Account)
}

// shared reads the vendor's shared key: ErrSharedGone when it was
// deleted (its metadata, or its Keychain item, is gone).
func (k *Keys) shared(ctx context.Context, v string) (string, error) {
	secret, err := k.Shared.Secret(ctx, v)
	switch {
	case errors.Is(err, credentials.ErrMissing):
		return "", ErrSharedGone
	case errors.Is(err, credentials.ErrNotConsented):
		if _, ok := k.Shared.Offer(v); !ok {
			return "", ErrSharedGone
		}
	}
	return secret, err
}

// SharedSecret is only the shared key the slot's ref names, for the
// Hugging Face token's order (spec §8.4: $HF_TOKEN, this, the own item,
// the token file). ErrNotConsented when the ref names none, ErrSharedGone
// when it was deleted.
func (k *Keys) SharedSecret(ctx context.Context, s Slot) (string, error) {
	v := sharedVendor(s)
	if v == "" {
		return "", credentials.ErrNotConsented
	}
	return k.shared(ctx, v)
}

// Gone reports whether the shared key the slot's ref names was deleted
// for all apps (its metadata is gone); it reads no secret.
func (k *Keys) Gone(s Slot) bool {
	v := sharedVendor(s)
	if v == "" {
		return false
	}
	f, ro, err := credentials.Read(k.Shared.Family)
	if err != nil || ro {
		return false // unreadable: not known to be gone
	}
	_, ok := f.Vendors[v]
	return !ok
}

// Fingerprint is the fingerprint of the shared key the slot's ref names
// (credentials.json; no secret is read); "" when there is none. The app
// keeps it, to know its own copy of a key deleted later (DropShared).
func (k *Keys) Fingerprint(s Slot) string {
	v := sharedVendor(s)
	if v == "" {
		return ""
	}
	e, _ := k.Shared.Offer(v)
	return e.Fingerprint
}

// DropShared is a deleted shared key's end in AudioSmith (the app clears
// the slot's ref): AudioSmith's own item goes too when it is that key —
// its fingerprint is fingerprint, the shared key's as AudioSmith last saw
// it; one that differs, or a fingerprint not known, stays. It reports
// whether an own key is left.
func (k *Keys) DropShared(s Slot, fingerprint string) (ownLeft bool, err error) {
	own, err := k.own(s.Account)
	if err != nil || own == "" {
		return false, err
	}
	if fingerprint == "" || credentials.Fingerprint(own) != fingerprint {
		return true, nil
	}
	if err := k.Own.Delete(s.Account); err != nil && !errors.Is(err, credentials.ErrNoKeychain) {
		return true, err
	}
	k.fam.logf("family: the shared %s key was deleted; AudioSmith's copy of it too", s.Vendor())
	return false, nil
}

// KeyInfo is what Settings shows of a slot. Finding it reads
// AudioSmith's own item, never a shared secret.
type KeyInfo struct {
	Vendor string // "" = not shared
	Own    bool   // AudioSmith's own item exists
	// Shared: the slot's ref names the vendor's key and AudioSmith is
	// among its users.
	Shared bool
	// Offer is the vendor's shared key's metadata (hint, fingerprint,
	// users), when there is one; nil otherwise.
	Offer *credentials.Entry
	// From names the app whose key Offer is ("Use the DeepSeek key from
	// Auralink"): the app that saved it, else another of its users; only
	// installed release builds of other apps (spec §7.10); "" = none to
	// name, and the offer is not shown.
	From string
	// Others name the other users of the shared key the UI may name
	// (release builds); OtherUsers are all of them, by bundle id.
	Others     []string
	OtherUsers []string
	// Different: AudioSmith has its own key, does not use the shared one,
	// and they differ (TextDifferentKey).
	Different bool
	// ReadOnly: a newer app owns credentials.json's format: nothing is
	// shared.
	ReadOnly bool
}

// Info finds what Settings shows of the slot.
func (k *Keys) Info(s Slot) (KeyInfo, error) {
	info := KeyInfo{Vendor: s.Vendor()}
	own, err := k.own(s.Account)
	if err != nil {
		return info, err
	}
	info.Own = own != ""
	if info.Vendor == "" {
		return info, nil
	}
	f, ro, err := credentials.Read(k.Shared.Family)
	if err != nil {
		k.fam.logf("family: credentials: %v", err)
		return info, nil
	}
	info.ReadOnly = ro
	e, ok := f.Vendors[info.Vendor]
	if !ok {
		return info, nil
	}
	info.Offer = &e
	me := k.Shared.BundleID
	info.Shared = sharedVendor(s) != "" && slices.Contains(e.Users, me)
	names := k.fam.releaseNames()
	for _, u := range e.Users {
		if u == me {
			continue
		}
		info.OtherUsers = append(info.OtherUsers, u)
		if n := names[u]; n != "" && !slices.Contains(info.Others, n) {
			info.Others = append(info.Others, n)
		}
	}
	if n := names[e.AddedBy]; n != "" {
		info.From = n
	} else if len(info.Others) > 0 {
		info.From = info.Others[0]
	}
	if info.Own && !info.Shared {
		info.Different = e.Fingerprint != credentials.Fingerprint(own)
	}
	return info, nil
}

// releaseNames are the names of the installed release builds of the
// other family apps, by bundle id (the apps the UI may name).
func (f *Family) releaseNames() map[string]string {
	out := map[string]string{}
	for _, a := range f.Found() {
		if a.Installed && a.Variant == actnow.Release && a.App.ID != AppID && a.App.ID != "" && a.BundleID != f.BundleID {
			out[a.BundleID] = a.Name
		}
	}
	return out
}

// writable reports whether credentials.json may be written (not a newer
// spec's).
func (k *Keys) writable() bool {
	_, ro, err := credentials.Read(k.Shared.Family)
	return err == nil && !ro
}

// Save keeps the key the user typed: AudioSmith's own item, and — when
// share (the checkbox; on when it is not shown) and the vendor is shared
// — the family's item, which another app's key for the vendor becomes
// (TextAlsoUpdates). It returns the slot's new ref: "" when the key is
// not shared. Saving unshared takes AudioSmith off the shared key it used.
func (k *Keys) Save(ctx context.Context, s Slot, secret string, share bool) (string, error) {
	secret = strings.TrimSpace(secret)
	if secret == "" {
		return s.Ref, errors.New("family: empty key")
	}
	label := s.Label
	if label == "" {
		label = "AudioSmith " + s.Account
	}
	if err := k.Own.Set(s.Account, label, secret); err != nil && !errors.Is(err, credentials.ErrNoKeychain) {
		return s.Ref, err
	}
	v := s.Vendor()
	if v == "" || !share {
		if old := sharedVendor(s); old != "" {
			if err := k.Shared.StopUsing(ctx, old); err != nil {
				k.fam.logf("family: stop using the shared %s key: %v", old, err)
			}
		}
		return "", nil
	}
	if !k.writable() {
		return "", nil // a newer app's format: the own item only
	}
	if err := k.Shared.Save(ctx, v, secret, credentials.SaveOptions{}); err != nil {
		k.fam.logf("family: share the %s key: %v", v, err)
		return "", nil // kept as AudioSmith's own
	}
	return credentials.Ref(v), nil
}

// Use is the user clicking "Use this key": AudioSmith joins the shared
// key's users (only now may it read it). It returns the slot's ref.
func (k *Keys) Use(ctx context.Context, s Slot) (string, error) {
	v := s.Vendor()
	if v == "" {
		return s.Ref, credentials.ErrUnknownVendor
	}
	if _, err := k.Shared.Use(ctx, v); err != nil {
		return s.Ref, err
	}
	return credentials.Ref(v), nil
}

// Share lets the other apps use the key AudioSmith already has (the
// checkbox turned on): its own item is copied to the family's, read back
// and checked; the own item stays (spec §15.2 step 6).
func (k *Keys) Share(ctx context.Context, s Slot) (string, error) {
	v := s.Vendor()
	if v == "" {
		return s.Ref, credentials.ErrUnknownVendor
	}
	if !k.writable() {
		return s.Ref, errors.New("family: another ActNow app updated the shared format")
	}
	old := credentials.Legacy{App: AppID, Service: k.Own.Service, Account: s.Account}
	if err := k.Shared.MigrateLegacy(ctx, v, old, credentials.SaveOptions{}); err != nil {
		return s.Ref, err
	}
	return credentials.Ref(v), nil
}

// Remove is "Remove the key": AudioSmith's own item goes, and the shared
// key it used: for all apps when forAll or nobody else uses it, else
// AudioSmith only stops using it ("Stop using it here"). The slot's ref
// is then "".
func (k *Keys) Remove(ctx context.Context, s Slot, forAll bool) error {
	if err := k.Own.Delete(s.Account); err != nil && !errors.Is(err, credentials.ErrNoKeychain) {
		return err
	}
	v := sharedVendor(s)
	if v == "" || !k.Shared.Using(v) {
		return nil
	}
	if forAll || len(k.Shared.OtherUsers(v)) == 0 {
		return k.Shared.DeleteForAll(ctx, v)
	}
	return k.Shared.StopUsing(ctx, v)
}

// StopUsingAll takes AudioSmith off every shared key's users, deleting
// none (Erase everything: the other apps keep their keys).
func (k *Keys) StopUsingAll(ctx context.Context) error {
	f, ro, err := credentials.Read(k.Shared.Family)
	if err != nil || ro {
		return err
	}
	var errs []error
	for v, e := range f.Vendors {
		if slices.Contains(e.Users, k.Shared.BundleID) {
			errs = append(errs, k.Shared.StopUsing(ctx, v))
		}
	}
	return errors.Join(errs...)
}
