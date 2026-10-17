package family

// The first run with the spec (§15.2 step 3): claim what of the
// catalogue is already in the model folder, origin "migrated". Disk only:
// no network, nothing moved.

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/hehehai/AudioSmith/internal/catalog"
	actnow "github.com/hehehai/actnowkit/family"
	"github.com/hehehai/actnowkit/models"
)

// Migrate claims, for AudioSmith's first run with the model folder
// store.Hub, every claim of wanted that is installed there (origin
// migrated). AudioSmith's claims file naming that folder marks it done,
// even an empty one: a model the user removes from AudioSmith later is
// not claimed again at the next start. It returns how many repos it
// claimed.
//
// When a claim fails (other than a model deleted meanwhile), what it
// claimed goes again and the error is returned: no marker, so that the
// next launch migrates again (and Start writes no manifest meanwhile). A
// claims file of a newer AudioSmith is left alone: ErrReadOnly.
func Migrate(ctx context.Context, store *models.Store, wanted []models.Claim) (int, error) {
	path := store.Family.ClaimsFile(store.BundleID)
	var mine models.Claims
	err := actnow.Load(path, models.KindClaims, 1, &mine)
	var corrupt *actnow.CorruptError
	switch {
	case err == nil && mine.Root != "" && models.SameHub(mine.Root, store.Hub):
		return 0, nil // done before, for this folder
	case errors.Is(err, actnow.ErrNewerSchema):
		return 0, ErrReadOnly
	case errors.As(err, &corrupt):
		// Set aside, never overwritten in place (spec §5.1).
		if err := actnow.Quarantine(path); err != nil {
			return 0, err
		}
		mine = models.Claims{}
	case err != nil && !actnow.IsNotExist(err):
		return 0, err
	}
	// An auxiliary repo is claimed for the models that are installed
	// (another model of AudioSmith, or another app, may have fetched it).
	var have []string
	for _, c := range wanted {
		if !c.Aux && models.Check(store.Hub, c).State == models.Installed {
			have = append(have, c.Model)
		}
	}
	var claimed []models.Ref
	var errs []error
	for _, c := range wanted {
		if err := ctx.Err(); err != nil {
			errs = append(errs, err)
			break
		}
		if !slices.Contains(have, c.Model) || models.Check(store.Hub, c).State != models.Installed {
			continue
		}
		c.Origin = models.OriginMigrated
		switch err := store.Claim(ctx, c); {
		case err == nil:
			if !slices.Contains(claimed, c.Ref()) {
				claimed = append(claimed, c.Ref())
			}
		case errors.Is(err, models.ErrNotInstalled): // deleted meanwhile
		default:
			errs = append(errs, err)
		}
	}
	if len(errs) > 0 {
		if len(claimed) > 0 {
			if err := store.Remove(); err != nil {
				errs = append(errs, err)
			}
		}
		return 0, errors.Join(errs...)
	}
	if len(claimed) == 0 {
		// Nothing installed: an empty claims file of this folder is the
		// marker (unknown members of an older file kept).
		marker := models.Claims{Schema: actnow.SchemaOf(models.KindClaims, 1), App: AppID, BundleID: store.BundleID,
			Updated: actnow.Now(), Root: store.Hub, Claims: []models.Claim{}, Extra: mine.Extra}
		if err := actnow.WriteJSON(path, marker); err != nil {
			return 0, err
		}
		_ = store.Family.Post(actnow.TopicModels)
	}
	return len(claimed), nil
}

// ModelID is the model a claim names for a catalogue entry: its id, or
// "custom:<repo>" for a repo outside the catalogue (the Studio's custom
// repo).
func ModelID(e catalog.Entry) string {
	if e.ID == "" || e.ID == "custom" {
		return "custom:" + e.Repo
	}
	return e.ID
}

// roles maps a catalogue task to a claim's role.
var roles = map[catalog.Task]string{
	catalog.TaskTTS: models.RoleTTS, catalog.TaskSTT: models.RoleASR, catalog.TaskAlign: models.RoleAlign,
	catalog.TaskVAD: models.RoleVAD, catalog.TaskDiarize: models.RoleDiarize, catalog.TaskEnhance: models.RoleEnhance,
	catalog.TaskLLM: models.RoleLLM,
}

// ClaimOf is the claim of e on repo at revision: e's own repo, or one of
// its auxiliary repos (aux). files are the files used when not the whole
// snapshot.
func ClaimOf(e catalog.Entry, repo, revision string, files []string, aux bool, origin string) models.Claim {
	c := models.Claim{Repo: repo, Revision: revision, Model: ModelID(e), Files: files, Aux: aux, Origin: origin,
		Role: models.RoleOther}
	if len(e.Tasks) > 0 {
		if r, ok := roles[e.Tasks[0]]; ok {
			c.Role = r
		}
	}
	if aux {
		c.Role = models.RoleOther
	}
	if p, ok := models.PinOf(repo); ok {
		c.Family = p.Family
	}
	return c
}

// Wanted are the claims AudioSmith makes at migration in hub: every
// catalogue entry and its auxiliary repos at their pinned commits, and
// the custom repos (the Studio's, the downloads remembered) at the commit
// their ref names on disk.
func Wanted(hub string, entries []catalog.Entry, custom []string) []models.Claim {
	var out []models.Claim
	add := func(e catalog.Entry, repo, rev string, files []catalog.File, aux bool) {
		if rev = revisionOn(hub, repo, rev); rev != "" {
			out = append(out, ClaimOf(e, repo, rev, paths(files), aux, models.OriginMigrated))
		}
	}
	known := map[string]bool{}
	for _, e := range entries {
		known[strings.ToLower(e.Repo)] = true
		for _, a := range e.Aux {
			add(e, a.Repo, a.Revision, a.Files, true)
		}
		add(e, e.Repo, e.Revision, e.Files, false)
	}
	for _, repo := range custom {
		repo = strings.TrimSpace(repo)
		if !models.ValidRepo(repo) || known[strings.ToLower(repo)] {
			continue
		}
		known[strings.ToLower(repo)] = true
		add(catalog.Custom(repo), repo, "", nil, false)
	}
	return out
}

// revisionOn is the commit of repo at rev in hub: rev itself when it is a
// commit, else what refs/<rev> (refs/main for "") names; "" when unknown.
func revisionOn(hub, repo, rev string) string {
	if models.IsCommit(rev) {
		return rev
	}
	if rev == "" {
		rev = "main"
	}
	if !models.ValidRepo(repo) || strings.Contains(rev, "..") {
		return ""
	}
	data, err := os.ReadFile(filepath.Join(models.RepoDir(hub, repo), "refs", filepath.FromSlash(rev)))
	if err != nil {
		return ""
	}
	if c := strings.TrimSpace(string(data)); models.IsCommit(c) {
		return c
	}
	return ""
}

func paths(files []catalog.File) []string {
	if len(files) == 0 {
		return nil
	}
	out := make([]string, len(files))
	for i, f := range files {
		out[i] = f.Path
	}
	return out
}
