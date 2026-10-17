package hub

// The other ActNow apps that share the model folder (the family spec's
// phase 0, §15.1; github.com/hehehai/actnowkit): which repos they may use,
// so that deleting and cleaning up keep them, and the cross-process locks
// of actnowkit/models (spec §7.4) around downloads and deletes.
//
// Phase 0 has no claim files yet: a repo is another app's when the
// family pins table lists it for that app (models.PinsOfApp) and
// LaunchServices knows the app. Phase 1 replaces the table with the
// apps' claims (models.Store).

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/hehehai/actnowkit/family"
	"github.com/hehehai/actnowkit/models"
	"github.com/hehehai/actnowkit/registry"
)

// BundleID is AudioSmith's bundle id, named in the owner files of the
// locks it holds.
const BundleID = "dev.hehehai.audiosmith"

// sharingApps are the family apps whose catalogues share repos with the
// model folder (the pins table's apps other than AudioSmith).
var sharingApps = []string{"dicta", "voxtnew"}

// Family says which other family apps use the model folder. The zero
// value asks LaunchServices.
type Family struct {
	// BundleID is this app's, for lock owners; "" = BundleID.
	BundleID string
	// Installed reports whether an app (a release bundle id) is on this
	// Mac; nil = LaunchServices (registry.DefaultSystem), which also
	// counts its dev builds (<id>.dev).
	Installed func(bundleID string) bool
}

func (f Family) bundleID() string {
	if f.BundleID != "" {
		return f.BundleID
	}
	return BundleID
}

// owner is this app doing op, for lock owner files.
func (f Family) owner(op string) models.Owner { return models.Owner{BundleID: f.bundleID(), Op: op} }

func (f Family) installed(bundleID string) bool {
	if f.Installed != nil {
		return f.Installed(bundleID)
	}
	sys := registry.DefaultSystem
	return len(sys.AppPaths(bundleID)) > 0 || len(sys.AppPaths(bundleID+".dev")) > 0
}

// Users maps the repos that installed family apps other than AudioSmith
// may use (their pins, auxiliary repos included; lower case) to the
// apps' names. It asks LaunchServices once per app.
func (f Family) Users() map[string][]string {
	out := map[string][]string{}
	for _, id := range sharingApps {
		app, ok := family.AppByID(id)
		if !ok || !f.installed(app.BundleID) {
			continue
		}
		add := func(repo string) {
			k := strings.ToLower(repo)
			if !slices.Contains(out[k], app.Name) {
				out[k] = append(out[k], app.Name)
			}
		}
		for _, p := range models.PinsOfApp(id) {
			add(p.Repo)
			for _, a := range p.Aux {
				add(a.Repo)
			}
		}
	}
	return out
}

// UsersOf are the installed other apps that may use repo (names).
func (f Family) UsersOf(repo string) []string { return f.Users()[strings.ToLower(repo)] }

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

// deleteRepo removes repo's snapshots and blobs from cacheDir (all of
// them, or the one of commit), holding its lock exclusively; the repo
// directory and its engine copy go when no snapshot is left (spec §7.6,
// §7.8: models.DeleteSnapshot). A repo another process downloads or
// deletes is left alone: *models.BusyError.
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
	// An engine copy without its repo (the repo went before, or never
	// was in this folder) belongs to nobody.
	if _, err := os.Stat(RepoDir(cacheDir, repo)); os.IsNotExist(err) {
		if err := os.RemoveAll(cp); err != nil {
			return freed, err
		}
	}
	if _, err := os.Lstat(cp); os.IsNotExist(err) {
		freed += cpBytes
	}
	return freed, nil
}
