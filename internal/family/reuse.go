package family

// Reusing the models another app put in the model folder (spec §7.10.1,
// §7.10.2, §7.3 "when to add"): the first-run reuse card's survey, the
// claims of "Use them" and of a model AudioSmith loads without claiming
// it, and what the Models page says of a model other apps claim
// ("Shared with Dicta", "Installed · from Dicta").

import (
	"context"
	"errors"
	"slices"
	"strings"

	"github.com/hehehai/AudioSmith/internal/catalog"
	actnow "github.com/hehehai/actnowkit/family"
	"github.com/hehehai/actnowkit/models"
	"github.com/hehehai/actnowkit/registry"
)

// EntryClaims are the claims of catalogue entry e in hub, origin reused:
// its repo and auxiliary repos at their pinned commits (a custom repo at
// the commit its ref names on disk). Empty when a revision is unknown.
func EntryClaims(hub string, e catalog.Entry) []models.Claim {
	var out []models.Claim
	if e.ID == "" || e.ID == "custom" {
		out = Wanted(hub, nil, []string{e.Repo})
	} else {
		out = Wanted(hub, []catalog.Entry{e}, nil)
	}
	for i := range out {
		out[i].Origin = models.OriginReused
	}
	return out
}

// Reusable is a model of the pack already in the model folder that
// AudioSmith does not claim yet.
type Reusable struct {
	Entry  catalog.Entry
	Claims []models.Claim // EntryClaims: what "Use them" claims
	// By names the other apps that claim it and the UI may name
	// (installed release builds, spec §7.10); empty when nobody named
	// claims it (a Python tool, an uninstalled app, a dev build).
	By []string
}

// Survey is the reuse card of the first run: the entries whose files are
// all installed in hub (their repo and auxiliary repos) and that
// AudioSmith does not claim, with who installed them, and their size
// (blobs counted once).
type Survey struct {
	Models []Reusable
	Bytes  int64
}

// By is the app the card names ("Installed by Dicta"): the first named
// installer; "" when none can be named ("Already on this Mac").
func (s Survey) By() string {
	for _, m := range s.Models {
		if len(m.By) > 0 {
			return m.By[0]
		}
	}
	return ""
}

// Survey finds which of entries are already in hub (models.Store.Survey).
func (f *Family) Survey(hub string, entries []catalog.Entry) Survey {
	store := f.Store(hub)
	mine, _ := store.Claims()
	if mine.Root != "" && !models.SameHub(mine.Root, hub) {
		mine = models.Claims{}
	}
	visible := visibleApps(f.Found())
	var out Survey
	seen := map[models.Ref]bool{}
	for _, e := range entries {
		claims := EntryClaims(hub, e)
		if len(claims) == 0 || claimsModel(mine, mainClaim(claims), ModelID(e)) {
			continue // not on disk, or AudioSmith uses it already
		}
		found := store.Survey(claims...)
		if len(found) != len(claims) {
			continue // a repo is missing: it downloads as usual
		}
		r := Reusable{Entry: e, Claims: claims}
		for _, x := range found {
			for _, u := range x.By {
				if name, ok := visible[u.BundleID]; ok && !slices.Contains(r.By, name) {
					r.By = append(r.By, name)
				}
			}
			if !seen[x.Want.Ref()] {
				seen[x.Want.Ref()] = true
				out.Bytes += x.Status.Bytes
			}
		}
		slices.Sort(r.By)
		out.Models = append(out.Models, r)
	}
	return out
}

// claimsModel reports whether mine claims c's ref for model (a claim
// naming no model counts for every model).
func claimsModel(mine models.Claims, c models.Claim, model string) bool {
	x, ok := mine.Find(c.Ref())
	users := x.Users()
	return ok && (len(users) == 0 || slices.Contains(users, model))
}

// mainClaim is the claim of the entry's own repo among EntryClaims.
func mainClaim(claims []models.Claim) models.Claim {
	for _, c := range claims {
		if !c.Aux {
			return c
		}
	}
	return claims[0]
}

// visibleApps maps the bundle ids of the other family apps the UI may
// name (installed release builds, OthersForUI) to their names.
func visibleApps(found []registry.Found) map[string]string {
	out := map[string]string{}
	for _, a := range found {
		if a.Installed && a.Variant == actnow.Release && a.App.ID != AppID && a.App.ID != "" {
			out[a.BundleID] = a.Name
		}
	}
	return out
}

// Reuse claims models already in hub for AudioSmith, origin reused: "Use
// them", [Use] on the Models page, or a model the engine loaded that
// AudioSmith did not claim (another app installed it, or AudioSmith
// removed it from itself and uses it again). Claims AudioSmith holds for
// the same model already are skipped; a model no longer installed is not
// claimed. Before the migration the claims wait for it; when the family
// directory is not usable it writes nothing. It returns how many claims
// it wrote.
func (f *Family) Reuse(ctx context.Context, hub string, claims []models.Claim) (int, error) {
	f.mu.Lock()
	if f.state == starting || f.switching {
		for _, c := range claims {
			if c.Origin == "" {
				c.Origin = models.OriginReused
			}
			f.pending = append(f.pending, pendingClaim{hub, c})
		}
		f.mu.Unlock()
		return 0, nil
	}
	f.mu.Unlock()
	store, ok := f.writable(hub)
	if !ok {
		return 0, nil
	}
	mine, err := store.Claims()
	if err != nil && !actnow.IsNotExist(err) {
		return 0, err
	}
	var todo []models.Claim
	for _, c := range claims {
		if c.Origin == "" {
			c.Origin = models.OriginReused
		}
		if !claimsModel(mine, c, c.Model) {
			todo = append(todo, c)
		}
	}
	n := 0
	var errs []error
	for _, c := range todo {
		switch err := store.Claim(ctx, c); {
		case err == nil:
			n++
			f.mu.Lock()
			delete(f.deleted, strings.ToLower(c.Repo))
			f.mu.Unlock()
		case errors.Is(err, models.ErrNotInstalled): // deleted meanwhile
		default:
			errs = append(errs, err)
		}
	}
	return n, errors.Join(errs...)
}

// Share is what the Models page says of a model and the other apps.
type Share struct {
	// Mine: AudioSmith claims the model's repo.
	Mine bool
	// With names the other apps the UI may name that claim it (valid
	// claims: same folder, installed release builds).
	With []string
}

// Shares reads, for each entry, whether AudioSmith claims its repo (at
// its pinned commit) and which named apps do, keyed by the repo in lower
// case. Entries outside the catalogue are left out.
func (f *Family) Shares(hub string, entries []catalog.Entry) map[string]Share {
	all, _ := models.ReadAllClaims(f.Dir)
	visible := visibleApps(f.Found())
	out := map[string]Share{}
	for _, e := range entries {
		if e.Revision == "" || !models.IsCommit(e.Revision) {
			continue
		}
		ref := models.Ref{Repo: e.Repo, Revision: e.Revision}
		var s Share
		for bid, cf := range all {
			if cf.Root != "" && !models.SameHub(cf.Root, hub) {
				continue
			}
			if _, ok := cf.Find(ref); !ok {
				continue
			}
			switch name, named := visible[bid]; {
			case bid == f.BundleID:
				s.Mine = claimsModel(cf, models.Claim{Repo: e.Repo, Revision: e.Revision}, ModelID(e))
			case named && !slices.Contains(s.With, name):
				s.With = append(s.With, name)
			}
		}
		slices.Sort(s.With)
		out[strings.ToLower(e.Repo)] = s
	}
	return out
}
