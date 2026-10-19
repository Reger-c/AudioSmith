package family

// The model library moved to ~/Library/Application Support/ActNow/models/hub
// (family spec v1.0.9, actnowkit v0.8.0): the catalogue's models are
// brought over from the Hugging Face caches once (hard links: nothing
// downloaded, the caches untouched); Time Machine leaves the library out
// unless the family's models.backup is on; a claims file lost with the
// family folder is written again when AudioSmith comes to the front.

import (
	"context"
	"os"

	"github.com/hehehai/AudioSmith/internal/catalog"
	"github.com/hehehai/actnowkit/models"
	famset "github.com/hehehai/actnowkit/settings"
)

// importModels brings the catalogue's models (main and auxiliary repos at
// their pinned commits) and wanted that are in a Hugging Face cache into
// store's folder, before Migrate claims what AudioSmith uses: importing
// claims nothing (on a first run the reuse card offers what was found).
// AudioSmith's previous claims come over too (actnowkit Store.Import).
func (f *Family) importModels(ctx context.Context, store *models.Store, wanted []models.Claim) {
	var want []models.Claim
	for _, e := range catalog.Builtin().Entries {
		if models.IsCommit(e.Revision) {
			want = append(want, ClaimOf(e, e.Repo, e.Revision, paths(e.Files), false, models.OriginImported))
		}
		for _, a := range e.Aux {
			if models.IsCommit(a.Revision) {
				want = append(want, ClaimOf(e, a.Repo, a.Revision, paths(a.Files), true, models.OriginImported))
			}
		}
	}
	want = append(want, wanted...)
	res, err := store.Import(ctx, models.LegacyHubs(store.Hub), f.readBackup(), want...)
	n, bytes := 0, int64(0)
	for _, r := range res {
		switch r.Action {
		case models.ImportImported:
			n++
			bytes += r.Linked + r.Copied
		case models.ImportBusy, models.ImportNoSpace:
			f.logf("family: import %s from %s: %s (tried again at the next start)", r.Ref, r.From, r.Action)
		}
	}
	if err != nil {
		f.logf("family: importing models from the Hugging Face cache: %v", err)
	}
	if n > 0 {
		f.logf("family: brought %d models (%d bytes) from the Hugging Face cache into %s", n, bytes, store.Hub)
	}
}

// Backup is the family's models.backup as last read: whether Time
// Machine backs up the model folder (off by default). Cheap: views call it.
func (f *Family) Backup() bool { return f.backup.Load() }

// readBackup reads models.backup from settings.json.
func (f *Family) readBackup() bool {
	s, _, err := famset.Read(f.Dir)
	on := err == nil && famset.ModelsBackup(s)
	f.backup.Store(on)
	return on
}

// SetBackup changes models.backup for every family app and applies it to
// the model folder.
func (f *Family) SetBackup(ctx context.Context, on bool) error {
	f.mu.Lock()
	st, hub := f.state, f.hub
	f.mu.Unlock()
	if st == off {
		return ErrReadOnly
	}
	v := famset.BackupOff
	if on {
		v = famset.BackupOn
	}
	f.backup.Store(on) // the switch shows it at once
	if _, err := famset.Set(ctx, f.Dir, f.BundleID, famset.KeyModelsBackup, v); err != nil {
		f.readBackup()
		return err
	}
	if hub == "" {
		return nil
	}
	return models.ApplyBackup(f.Dir, hub, on)
}

// applyBackup marks the model folder for Time Machine as models.backup
// says (at start, after a new folder, when the family's settings change).
func (f *Family) applyBackup() {
	f.mu.Lock()
	st, hub := f.state, f.hub
	f.mu.Unlock()
	if st != ready || hub == "" {
		return
	}
	if err := models.ApplyBackup(f.Dir, hub, f.readBackup()); err != nil {
		f.logf("family: Time Machine: %v", err)
	}
}

// healClaims writes AudioSmith's claims again when its claims file is
// gone (a cleaner deleted the family folder or models/claims): until
// then other apps' deletes are unsure about AudioSmith's models (spec
// §7.6, v1.0.9). Migrate does it, as at the first start.
func (f *Family) healClaims(ctx context.Context) {
	f.mu.Lock()
	st, hub, wanted := f.state, f.hub, f.wanted
	f.mu.Unlock()
	if st != ready || hub == "" || wanted == nil {
		return
	}
	if _, err := os.Stat(f.Dir.ClaimsFile(f.BundleID)); !os.IsNotExist(err) {
		return
	}
	n, err := Migrate(ctx, f.Store(hub), wanted(hub))
	if err != nil {
		f.logf("family: claiming the models again: %v", err)
		return
	}
	f.logf("family: the claims file was gone; claimed %d models again", n)
}
