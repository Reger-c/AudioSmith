package family

// The transition (plan for phase 1, §1): family apps installed without a
// manifest run a version before the spec's phase 1 and write no claims,
// so their claims cannot protect their models. Until they register, a
// repo their catalogue may have (the family pins table, auxiliary repos
// included) counts as theirs, as phase 0 did (internal/hub family.go).
// Once such an app writes its manifest this stops by itself; the rule
// goes when Dicta and VoxtNew ship manifests in a release.

import (
	"strings"

	"github.com/hehehai/actnowkit/models"
	"github.com/hehehai/actnowkit/registry"
)

// LegacyUsers are the other family apps that are installed, have no
// manifest, and whose pins name repo: users of it for deleting.
func (f *Family) LegacyUsers(repo string) []models.User {
	return legacyUsers(f.Found(), repo)
}

func legacyUsers(found []registry.Found, repo string) []models.User {
	var out []models.User
	for _, a := range found {
		if !a.Installed || a.Manifest != nil || a.App.ID == "" || a.App.ID == AppID || !pinned(a.App.ID, repo) {
			continue
		}
		out = append(out, models.User{BundleID: a.BundleID, AppID: a.App.ID, Name: a.Name})
	}
	return out
}

// pinned reports whether the pins table gives repo (or one of a pin's
// auxiliary repos) to the app appID.
func pinned(appID, repo string) bool {
	for _, p := range models.PinsOfApp(appID) {
		if strings.EqualFold(p.Repo, repo) {
			return true
		}
		for _, a := range p.Aux {
			if strings.EqualFold(a.Repo, repo) {
				return true
			}
		}
	}
	return false
}
