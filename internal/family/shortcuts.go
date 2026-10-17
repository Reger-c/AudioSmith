package family

// Shortcut coordination (spec §10.4): the dictation shortcut against the
// other installed family apps' declared shortcuts — under the recorder
// (UsedBy), and at start and when an app changes its manifest
// (ShortcutConflicts: a warning on the settings row, and a prompt from
// the later of the two apps).

import (
	actnow "github.com/hehehai/actnowkit/family"
	"github.com/hehehai/actnowkit/registry"
	"github.com/hehehai/actnowkit/shortcuts"
)

// holders are the other apps' shortcuts: installed apps with a manifest,
// other than AudioSmith's builds; never dev builds (spec §6.3 step 5).
// forUI keeps release builds only (spec §7.10: the UI names only those).
func (f *Family) holders(forUI bool) []shortcuts.Holder {
	var keep []registry.Found
	for _, a := range f.Found() {
		switch {
		case a.BundleID == f.BundleID, a.App.ID == AppID:
		case a.Variant == actnow.Dev:
		case forUI && a.Variant != actnow.Release:
		default:
			keep = append(keep, a)
		}
	}
	return registry.Holders(keep)
}

// UsedBy lists the other apps' enabled shortcuts on keys (an accelerator
// being recorded), for "Also used by Auralink (Transcribe)" under the
// recorder: release builds only. Saving stays allowed. It reads the last
// discovery: no I/O.
func (f *Family) UsedBy(keys string) []shortcuts.Hit {
	if keys == "" {
		return nil
	}
	return shortcuts.UsedBy(keys, f.BundleID, f.holders(true))
}

// ShortcutConflicts lists the conflicts of AudioSmith's shortcut, as its
// manifest declares it, with the other installed apps' (shortcuts
// .FindConflicts; Prompt marks those AudioSmith, the later one, prompts
// for). forUI: release builds only. Nothing before the manifest is
// written. It reads AudioSmith's manifest (its updated time stands in
// for a missing enabledAt).
func (f *Family) ShortcutConflicts(forUI bool) []shortcuts.Conflict {
	f.mu.Lock()
	has := f.hasMan
	f.mu.Unlock()
	if !has {
		return nil
	}
	me, err := registry.Read(f.Dir, f.BundleID)
	if err != nil {
		f.logf("family: own manifest: %v", err)
		return nil
	}
	return shortcuts.FindConflicts(registry.HolderOf(me), f.holders(forUI))
}

// ShortcutLink is the other app's page for its shortcuts (its manifest's
// links.shortcuts), "" when it names none.
func ShortcutLink(h shortcuts.Holder) string { return h.Links[registry.LinkShortcuts] }
