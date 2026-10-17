package family

// Settings → Storage (spec §7.6 steps 3, 4, 6): the space of AudioSmith's
// models only it uses and shared with other apps, the models only
// uninstalled apps claimed (orphans) and their clean-up, and forgetting
// AudioSmith's claims when it erases everything.

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"

	actnow "github.com/hehehai/actnowkit/family"
	"github.com/hehehai/actnowkit/models"
)

// Usage splits the space of AudioSmith's claimed models in a folder
// (blobs counted once).
type Usage struct {
	Mine   int64 // only AudioSmith uses them
	Shared int64 // other apps (any build) use them too
	// SharedWith names the apps the UI may name among those (release
	// builds of other family apps).
	SharedWith []string
}

// Usage computes it for hub.
func (f *Family) Usage(hub string) (Usage, error) {
	mine, err := f.Store(hub).Claims()
	if err != nil {
		return Usage{}, err
	}
	var u Usage
	if mine.Root != "" && !models.SameHub(mine.Root, hub) {
		return u, nil
	}
	d := f.Decider(hub)
	seen := map[string]bool{}
	for _, c := range mine.Claims {
		dec, err := d.Decide(c.Ref())
		if err != nil {
			continue
		}
		for _, n := range DecisionNames(dec) {
			if dec.InUse() && !slices.Contains(u.SharedWith, n) {
				u.SharedWith = append(u.SharedWith, n)
			}
		}
		for p, n := range snapshotBlobs(models.SnapshotDir(hub, c.Ref())) {
			if seen[p] {
				continue
			}
			seen[p] = true
			if dec.InUse() {
				u.Shared += n
			} else {
				u.Mine += n
			}
		}
	}
	slices.Sort(u.SharedWith)
	return u, nil
}

// snapshotBlobs maps the blobs a snapshot links to their sizes.
func snapshotBlobs(snap string) map[string]int64 {
	out := map[string]int64{}
	filepath.WalkDir(snap, func(p string, de fs.DirEntry, err error) error {
		if err != nil || de.IsDir() {
			return nil
		}
		if target, err := filepath.EvalSymlinks(p); err == nil {
			if fi, err := os.Stat(target); err == nil {
				out[target] = fi.Size()
			}
		}
		return nil
	})
	return out
}

// Orphans are the claims in hub of uninstalled release builds of family
// apps (dev and test builds are developers' business, spec §6.3), for
// "N models (X GB) are only used by apps that are no longer installed".
func (f *Family) Orphans(hub string) ([]models.Orphan, error) {
	list, err := f.Store(hub).Orphans()
	return slices.DeleteFunc(list, func(o models.Orphan) bool {
		_, v, err := actnow.ParseBundleID(o.BundleID)
		return err != nil || v != actnow.Release || len(o.Claims) == 0
	}), err
}

// CleanOrphans deletes what only the uninstalled apps bundleIDs (none:
// those of Orphans) used (models.Store.CleanOrphans: models another app
// uses stay), and their files in $FAMILY. It returns the bytes freed.
func (f *Family) CleanOrphans(ctx context.Context, hub string, bundleIDs ...string) (int64, error) {
	store, ok := f.writable(hub)
	if !ok {
		return 0, ErrReadOnly
	}
	if len(bundleIDs) == 0 {
		orphans, err := f.Orphans(hub)
		if err != nil {
			return 0, err
		}
		for _, o := range orphans {
			bundleIDs = append(bundleIDs, o.BundleID)
		}
	}
	var freed int64
	var errs []error
	for _, bid := range bundleIDs {
		res, err := store.CleanOrphans(ctx, bid)
		for _, r := range res {
			freed += r.Freed
		}
		if err != nil {
			errs = append(errs, err)
		}
	}
	f.Rediscover()
	return freed, errors.Join(errs...)
}

// ForgetAll removes AudioSmith's claims file of hub ("Erase everything"
// with the models): the next start migrates what is still there.
func (f *Family) ForgetAll(hub string) error {
	store, ok := f.writable(hub)
	if !ok {
		return nil
	}
	return store.Remove()
}
