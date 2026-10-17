package family

// Deleting by the claims (spec §7.6): Decider is internal/hub's Decider
// for one model folder — models.Store's decisions and deletes, with the
// transition's legacy users (apps installed without a manifest, by the
// pins table) counted as users, and without writing anything while the
// family directory is not AudioSmith's to write (read only, or before
// the migration).

import (
	"context"
	"slices"
	"strings"

	"github.com/hehehai/AudioSmith/internal/catalog"
	actnow "github.com/hehehai/actnowkit/family"
	"github.com/hehehai/actnowkit/models"
)

// Decider decides AudioSmith's deletes in one model folder.
type Decider struct {
	f   *Family
	hub string
}

// Decider is the Decider of the model folder hub.
func (f *Family) Decider(hub string) *Decider { return &Decider{f: f, hub: hub} }

// Decide is models.Store.Decide with the legacy users.
func (d *Decider) Decide(ref models.Ref) (models.Decision, error) {
	dec, err := d.f.Store(d.hub).Decide(ref)
	if err != nil {
		return dec, err
	}
	d.withLegacy(&dec)
	return dec, nil
}

func (d *Decider) withLegacy(dec *models.Decision) {
	for _, u := range d.f.LegacyUsers(dec.Ref.Repo) {
		if !slices.ContainsFunc(dec.Users, func(x models.User) bool { return x.BundleID == u.BundleID }) {
			dec.Users = append(dec.Users, u)
		}
	}
}

// Delete is models.Store.Delete (a repo no catalogue entry names: Clean
// up, Erase everything).
func (d *Decider) Delete(ctx context.Context, ref models.Ref, scope models.Scope) (models.Result, error) {
	return d.delete(ctx, ref, "", scope)
}

// DeleteModel is models.Store.DeleteModel for entry e's main repo or one
// of its auxiliary repos. Deleting e for all apps leaves an auxiliary
// repo another of AudioSmith's models uses (it would break that model).
func (d *Decider) DeleteModel(ctx context.Context, ref models.Ref, e catalog.Entry, scope models.Scope) (models.Result, error) {
	model := ModelID(e)
	if scope == models.AllApps && !strings.EqualFold(ref.Repo, e.Repo) && len(d.ownOthers(ref, model)) > 0 {
		scope = models.ThisApp
	}
	return d.delete(ctx, ref, model, scope)
}

// ownOthers are AudioSmith's models other than model whose claim names
// ref.
func (d *Decider) ownOthers(ref models.Ref, model string) []string {
	mine, err := d.f.Store(d.hub).Claims()
	if err != nil {
		return nil
	}
	c, ok := mine.Find(ref)
	if !ok {
		return nil
	}
	return slices.DeleteFunc(c.Users(), func(u string) bool { return u == model || model == "" })
}

func (d *Decider) delete(ctx context.Context, ref models.Ref, model string, scope models.Scope) (models.Result, error) {
	store, writable := d.f.writable(d.hub)
	if !writable {
		return d.deleteReadOnly(ref, model, scope)
	}
	if scope == models.ThisApp && len(d.f.LegacyUsers(ref.Repo)) > 0 {
		// An app without claims may use it: only AudioSmith's claim goes.
		var others []string
		var err error
		if model != "" {
			others, err = store.Release(ref, model)
		} else {
			err = store.Unclaim(ref)
		}
		if err != nil {
			return models.Result{}, err
		}
		dec, err := d.Decide(ref)
		return models.Result{Decision: dec, OwnModels: others}, err
	}
	var res models.Result
	var err error
	if model != "" {
		res, err = store.DeleteModel(ctx, ref, model, scope)
	} else {
		res, err = store.Delete(ctx, ref, scope)
	}
	d.withLegacy(&res.Decision)
	return res, err
}

// deleteReadOnly decides and deletes as Store.Delete does, writing no
// claim and no tombstone: the family directory is a newer spec's, or
// AudioSmith has not migrated (or failed to).
func (d *Decider) deleteReadOnly(ref models.Ref, model string, scope models.Scope) (models.Result, error) {
	lock, err := models.TryLockRepo(d.hub, ref.Repo, actnow.Exclusive, models.Owner{BundleID: d.f.BundleID, Op: models.OpDelete})
	if err != nil {
		return models.Result{}, err
	}
	defer lock.Release()
	dec, err := d.Decide(ref)
	if err != nil {
		return models.Result{}, err
	}
	res := models.Result{Decision: dec, OwnModels: d.ownOthers(ref, model)}
	if len(res.OwnModels) > 0 || scope == models.ThisApp && dec.InUse() {
		return res, nil
	}
	res.Freed, err = models.DeleteSnapshot(d.hub, ref)
	res.Deleted = err == nil
	return res, err
}

// VisibleNames are the names of users the UI may name (spec §7.10):
// release builds of family apps other than AudioSmith, each once.
func VisibleNames(users ...models.User) []string {
	var out []string
	for _, u := range users {
		a, v, err := actnow.ParseBundleID(u.BundleID)
		if err != nil || v != actnow.Release || a.ID == AppID || slices.Contains(out, a.Name) {
			continue
		}
		out = append(out, a.Name)
	}
	return out
}

// DecisionNames are VisibleNames of the apps that use or hold a decided
// model.
func DecisionNames(d models.Decision) []string {
	return VisibleNames(append(slices.Clone(d.Users), d.LoadedBy...)...)
}
