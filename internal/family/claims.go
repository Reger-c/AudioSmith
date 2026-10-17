package family

// AudioSmith's claims after the migration (spec §7.3): a claim for every
// finished download, written under the repo's exclusive lock the
// downloader holds (internal/hub Options.Claimed), and the claims a
// delete leaves behind released.

import (
	"context"
	"slices"
	"strings"

	"github.com/hehehai/actnowkit/models"
)

// pendingClaim is a download that finished before the migration of its
// folder: it is claimed after (or dropped when the migration failed:
// the next launch migrates it).
type pendingClaim struct {
	hub   string
	claim models.Claim
}

// Claimed claims a model the downloader just finished in hub, origin
// downloaded unless set, holding its repo's lock (models.ClaimLocked).
// Before the migration it waits for it; when the family directory is not
// usable, or hub is not the folder AudioSmith uses now (a download that
// started before the model folder changed), it writes nothing.
func (f *Family) Claimed(ctx context.Context, hub string, c models.Claim) error {
	if c.Origin == "" {
		c.Origin = models.OriginDownloaded
	}
	f.mu.Lock()
	if f.state == starting || f.switching {
		f.pending = append(f.pending, pendingClaim{hub, c})
		f.mu.Unlock()
		return nil
	}
	if f.state != ready || !models.SameHub(hub, f.hub) {
		f.mu.Unlock()
		return nil
	}
	store := f.storeLocked(hub)
	f.mu.Unlock()
	return store.ClaimLocked(ctx, c)
}

// flushPending claims the downloads that finished during a migration.
func (f *Family) flushPending(ctx context.Context) {
	f.mu.Lock()
	pending, hub := f.pending, f.hub
	f.pending = nil
	f.mu.Unlock()
	for _, p := range pending {
		if !models.SameHub(p.hub, hub) {
			continue
		}
		if err := f.Store(hub).Claim(ctx, p.claim); err != nil {
			f.logf("family: claim %s: %v", p.claim.Ref(), err)
		}
	}
}

// writable is the store of hub when AudioSmith writes claims there now.
func (f *Family) writable(hub string) (*models.Store, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.state != ready || f.switching || !models.SameHub(hub, f.hub) {
		return nil, false
	}
	return f.storeLocked(hub), true
}

// Release takes model (ModelID) off AudioSmith's claims on repos in hub,
// whatever their revision: after Models → Delete removed it. A claim
// other models of AudioSmith still name stays for them (Store.Release).
func (f *Family) Release(hub, model string, repos ...string) error {
	store, ok := f.writable(hub)
	if !ok {
		return nil
	}
	mine, err := store.Claims()
	if err != nil {
		return err
	}
	for _, c := range mine.Claims {
		if hasRepo(repos, c.Repo) {
			if _, err := store.Release(c.Ref(), model); err != nil {
				return err
			}
		}
	}
	return nil
}

// Forget drops AudioSmith's claims on repos in hub whose snapshot is no
// longer installed: after Clean up or "Erase everything" deleted them
// (the repos kept for other apps keep their claims).
func (f *Family) Forget(hub string, repos ...string) error {
	store, ok := f.writable(hub)
	if !ok {
		return nil
	}
	mine, err := store.Claims()
	if err != nil {
		return err
	}
	var gone []models.Ref
	for _, c := range mine.Claims {
		if hasRepo(repos, c.Repo) && models.Check(hub, c).State != models.Installed {
			gone = append(gone, c.Ref())
		}
	}
	if len(gone) == 0 {
		return nil
	}
	return store.Unclaim(gone...)
}

func hasRepo(repos []string, repo string) bool {
	return slices.ContainsFunc(repos, func(r string) bool { return strings.EqualFold(r, repo) })
}
