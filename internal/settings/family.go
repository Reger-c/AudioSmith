package settings

// The ActNow family's settings of AudioSmith's own (internal/family; the
// family spec v1.0.4): what was already shown once, and when the
// dictation shortcut was set.

import (
	"maps"
	"time"
)

// Family: AudioSmith's own bookkeeping for the ActNow family (the shared
// settings themselves arrive with phase 1's P4a).
type Family struct {
	// ConflictShown are the shortcut conflicts already prompted for, by
	// pair (shortcuts.Conflict.Key: "dictation|<bundle id>|<function>"):
	// each pair is prompted once (spec §10.4).
	ConflictShown map[string]bool `json:"conflictShown,omitempty"`
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
}
