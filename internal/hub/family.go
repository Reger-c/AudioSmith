package hub

// The other ActNow apps that share the model folder (the family spec,
// github.com/hehehai/actnowkit): the cross-process locks of
// actnowkit/models (spec §7.4) around downloads and deletes, and the
// Decider that decides deletes from every app's claims (spec §7.6).
//
// The hub never writes claims and does not know internal/family: the app
// passes the Decider (internal/family Decider: its models.Store, with the
// apps that write no claims yet) and Options.Claimed.

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/hehehai/AudioSmith/internal/catalog"
	"github.com/hehehai/actnowkit/family"
	"github.com/hehehai/actnowkit/models"
)

// BundleID is AudioSmith's bundle id, named in the owner files of the
// locks it holds.
const BundleID = "dev.hehehai.audiosmith"

// Decider decides deletes from the ActNow family's claims (spec §7.6):
// who else uses a snapshot (Decide), and deleting it from AudioSmith or
// for all apps, keeping what other apps use (Delete; DeleteModel for one
// catalogue entry's repo, its own or an auxiliary one, which other
// entries of AudioSmith may share). The methods are models.Store's.
type Decider interface {
	Decide(ref models.Ref) (models.Decision, error)
	Delete(ctx context.Context, ref models.Ref, scope models.Scope) (models.Result, error)
	DeleteModel(ctx context.Context, ref models.Ref, e catalog.Entry, scope models.Scope) (models.Result, error)
}

// Family is how downloads and deletes deal with the other family apps.
// The zero value names AudioSmith in lock owners and deletes what it is
// asked to (no claims are read: tests, a read-only family directory).
type Family struct {
	// BundleID is this app's, for lock owners; "" = BundleID.
	BundleID string
	// Decider decides deletes from the claims; nil = no family.
	Decider Decider
	// Changed, when set, tells the family the models changed (posts
	// TopicModels): Verify calls it after removing bad blobs.
	Changed func()
}

func (f Family) bundleID() string {
	if f.BundleID != "" {
		return f.BundleID
	}
	return BundleID
}

// owner is this app doing op, for lock owner files.
func (f Family) owner(op string) models.Owner { return models.Owner{BundleID: f.bundleID(), Op: op} }

// AppName is the name of a family app's bundle id ("Dicta"); the bundle
// id itself when it is not a family app, "" for "".
func AppName(bundleID string) string {
	if bundleID == "" {
		return ""
	}
	if app, _, err := family.ParseBundleID(bundleID); err == nil {
		return app.Name
	}
	return bundleID
}

// Holder is who holds the lock err reports (a *models.BusyError): the
// app's name, "" when unknown; ok is false for other errors.
func Holder(err error) (name string, ok bool) {
	var be *models.BusyError
	if !errors.As(err, &be) {
		return "", false
	}
	if be.Owner != nil {
		return AppName(be.Owner.BundleID), true
	}
	return "", true
}

// noSnapshot names no snapshot: deleting it removes a repo's unused
// blobs (and the repo when it has no snapshot), nothing else.
const noSnapshot = "0000000000000000000000000000000000000000"

// snapshotsOf are the finished snapshots of repo in cacheDir (their
// commits).
func snapshotsOf(cacheDir, repo string) []string {
	var out []string
	entries, _ := os.ReadDir(filepath.Join(RepoDir(cacheDir, repo), "snapshots"))
	for _, de := range entries {
		if name := de.Name(); de.IsDir() && !strings.HasPrefix(name, ".") && !strings.HasSuffix(name, ".incomplete") {
			out = append(out, name)
		}
	}
	return out
}

// deleteRepo removes repo's snapshots and blobs from cacheDir (all of
// them, or the one of commit), holding its lock exclusively, without
// asking who uses them: for repos nobody claims (no Decider), the
// unfinished snapshots and blobs a Decider's deletes leave, and engine
// copies. The repo directory and its engine copy go when no snapshot is
// left (spec §7.8: models.DeleteSnapshot). A repo another process
// downloads or deletes is left alone: *models.BusyError.
func deleteRepo(cacheDir, repo, commit string, fam Family) (int64, error) {
	held, err := models.TryLockRepo(cacheDir, repo, family.Exclusive, fam.owner(models.OpDelete))
	if err != nil {
		return 0, err
	}
	defer held.Release()
	var commits []string
	if commit != "" {
		commits = []string{commit}
	} else {
		entries, _ := os.ReadDir(filepath.Join(RepoDir(cacheDir, repo), "snapshots"))
		for _, de := range entries {
			if name := strings.TrimSuffix(de.Name(), ".incomplete"); de.IsDir() && !slices.Contains(commits, name) {
				commits = append(commits, name)
			}
		}
	}
	if len(commits) == 0 {
		commits = []string{noSnapshot}
	}
	cp := EngineCopy(cacheDir, repo)
	cpBytes := dirBytes(cp)
	var freed int64
	for _, rev := range commits {
		n, err := models.DeleteSnapshot(cacheDir, models.Ref{Repo: repo, Revision: rev})
		freed += n
		if err != nil {
			return freed, err
		}
	}
	if err := dropOrphanCopy(cacheDir, repo); err != nil {
		return freed, err
	}
	if _, err := os.Lstat(cp); os.IsNotExist(err) {
		freed += cpBytes
	}
	return freed, nil
}

// dropOrphanCopy removes the engine copy of a repo that is no longer in
// the folder (it went before, or never was here): it belongs to nobody.
func dropOrphanCopy(cacheDir, repo string) error {
	if _, err := os.Stat(RepoDir(cacheDir, repo)); !os.IsNotExist(err) {
		return nil
	}
	return os.RemoveAll(EngineCopy(cacheDir, repo))
}
