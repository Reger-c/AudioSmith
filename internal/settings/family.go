package settings

// The ActNow family's settings of AudioSmith's own (internal/family; the
// family spec v1.0.4): whether each shared setting follows the family's,
// what was already shown once, and when the dictation shortcut was set.

import (
	"maps"
	"time"
)

// Family: AudioSmith's own bookkeeping for the ActNow family. The shared
// settings themselves (General.Language, Models.Proxy, Models.Endpoints,
// Models.CacheDir) stay where they are: internal/family's Shared writes
// the family's values into them while they follow it (spec §9.3).
type Family struct {
	// Follow is false for a shared setting AudioSmith keeps for itself
	// ("Use a different setting for this app"), by the family's key
	// ("general.language", "network.proxy", "network.hfEndpoints",
	// "models.root"). A key that is missing follows: new installs follow
	// everything (spec §9.3), so only false is kept (normalize).
	Follow map[string]bool `json:"follow,omitempty"`
	// SharedMigrated: the first-run merge of AudioSmith's settings with
	// the family's (spec §9.4) is done.
	SharedMigrated bool `json:"sharedMigrated,omitempty"`
	// OtherValue are the shared settings whose migration found another
	// value in the family while AudioSmith kept its own: the row says so
	// once, with "Use theirs" (for models.root: the Models page's "Move
	// models…" banner, until moved or "Keep separate").
	OtherValue map[string]bool `json:"otherValue,omitempty"`
	// ConflictShown are the shortcut conflicts already prompted for, by
	// pair (shortcuts.Conflict.Key: "dictation|<bundle id>|<function>"):
	// each pair is prompted once (spec §10.4).
	ConflictShown map[string]bool `json:"conflictShown,omitempty"`
	// KeyFingerprints are the fingerprints of the shared keys AudioSmith
	// uses, by vendor, as it last saw them: when one is deleted for all
	// apps, AudioSmith's own copy of the same key goes too (family spec
	// §8.3).
	KeyFingerprints map[string]string `json:"keyFingerprints,omitempty"`
}

// Follows reports whether the shared setting key follows the family's.
func (f Family) Follows(key string) bool {
	v, ok := f.Follow[key]
	return !ok || v
}

// SetFollows sets key's follow flag (only false is kept).
func (f *Family) SetFollows(key string, follow bool) {
	if follow {
		delete(f.Follow, key)
		return
	}
	if f.Follow == nil {
		f.Follow = map[string]bool{}
	}
	f.Follow[key] = false
}

// normalizeFamily keeps the follow flags that are false and the prompts
// still to show.
func normalizeFamily(s *Settings) {
	f := &s.Family
	maps.DeleteFunc(f.Follow, func(_ string, v bool) bool { return v })
	maps.DeleteFunc(f.OtherValue, func(_ string, v bool) bool { return !v })
	if len(f.Follow) == 0 {
		f.Follow = nil
	}
	if len(f.OtherValue) == 0 {
		f.OtherValue = nil
	}
}

// now is the clock that stamps Dictation.EnabledAt (tests set it).
var now = time.Now

// stampDictation records when the user set the dictation shortcut or
// turned dictation on (the family manifest's enabledAt: of two apps with
// the same keys, the later one prompts).
func stampDictation(old Settings, next *Settings) {
	o, n := old.Dictation, &next.Dictation
	if n.Enabled && (o.Shortcut != n.Shortcut || !o.Enabled) {
		n.EnabledAt = now().UTC().Truncate(time.Second)
	}
}

func cloneFamily(s *Settings) {
	s.Family.ConflictShown = maps.Clone(s.Family.ConflictShown)
	s.Family.Follow = maps.Clone(s.Family.Follow)
	s.Family.OtherValue = maps.Clone(s.Family.OtherValue)
	s.Family.KeyFingerprints = maps.Clone(s.Family.KeyFingerprints)
	s.Dubbing.CredentialRefs = maps.Clone(s.Dubbing.CredentialRefs)
}
