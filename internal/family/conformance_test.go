package family

// actnowkit's conformance scenarios (spec §16.1) run against AudioSmith's
// own code: its downloader (internal/hub, with the claims of Claimed) and
// its deletes (hub.Remove deciding with Decider), in the fixture's
// directories.

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/hehehai/AudioSmith/internal/catalog"
	"github.com/hehehai/AudioSmith/internal/hub"
	"github.com/hehehai/actnowkit/conformance"
	"github.com/hehehai/actnowkit/models"
)

type adapter struct {
	f   *Family
	hub string
}

func newAdapter(t testing.TB, fx *conformance.Fixture, bundleID string) any {
	app, err := conformance.FakeApp(filepath.Join(filepath.Dir(fx.Family.Root), "Applications", bundleID), "AudioSmith", bundleID)
	if err != nil {
		t.Fatal(err)
	}
	team := ""
	f := New(Options{Dir: fx.Family, BundleID: bundleID, System: conformance.FixtureSystem{}, AppPath: app, TeamID: &team, Logf: t.Logf})
	t.Cleanup(f.Close)
	if err := f.Start(context.Background(), Inputs{Version: "0.4.0", Hub: fx.Hub}, func(string) []models.Claim { return nil }); err != nil {
		t.Fatal(err)
	}
	return &adapter{f: f, hub: fx.Hub}
}

func (a *adapter) BundleID() string { return a.f.BundleID }

func (a *adapter) family() hub.Family {
	a.f.Rediscover() // as on activation
	return hub.Family{BundleID: a.f.BundleID, Decider: a.f.Decider(a.hub)}
}

// claims registers refs for model (the first its own repo, the others
// auxiliary) as AudioSmith reuses a model: Family.Reuse.
func (a *adapter) claims(ctx context.Context, model, origin string, refs ...models.Ref) error {
	var cs []models.Claim
	for i, r := range refs {
		cs = append(cs, models.Claim{Repo: r.Repo, Revision: r.Revision, Model: model, Aux: i > 0, Origin: origin})
	}
	_, err := a.f.Reuse(ctx, a.hub, cs)
	return err
}

func (a *adapter) Reuse(ctx context.Context, refs ...models.Ref) error {
	for _, r := range refs {
		if err := a.claims(ctx, "", models.OriginReused, r); err != nil {
			return err
		}
	}
	return nil
}

func (a *adapter) Remove(ctx context.Context, ref models.Ref, allApps bool) error {
	scope := models.ThisApp
	if allApps {
		scope = models.AllApps
	}
	_, err := hub.Remove(ctx, a.hub, catalog.Entry{ID: "model", Repo: ref.Repo, Revision: ref.Revision}, a.family(), scope)
	return err
}

func (a *adapter) UseModel(ctx context.Context, model string, refs ...models.Ref) error {
	return a.claims(ctx, model, models.OriginReused, refs...)
}

func (a *adapter) RemoveModel(ctx context.Context, model string, refs ...models.Ref) error {
	e := catalog.Entry{ID: model, Repo: refs[0].Repo, Revision: refs[0].Revision}
	for _, r := range refs[1:] {
		e.Aux = append(e.Aux, catalog.Aux{Repo: r.Repo, Revision: r.Revision})
	}
	_, err := hub.Remove(ctx, a.hub, e, a.family(), models.ThisApp)
	return err
}

func (a *adapter) Download(ctx context.Context, endpoint string, ref models.Ref) error {
	c := hub.New(hub.Options{CacheDir: a.hub, Endpoints: []string{endpoint}, Official: endpoint, ReserveBytes: -1, Family: a.family(),
		Claimed: func(ctx context.Context, dir string, d hub.Finished) error {
			return a.f.Claimed(ctx, dir, ClaimOf(d.Entry, d.Repo, d.Commit, d.Files, d.Aux, models.OriginDownloaded))
		}})
	return c.Download(ctx, catalog.Entry{ID: "whisper", Repo: ref.Repo, Revision: ref.Revision}, nil)
}

func (a *adapter) CleanOrphans(ctx context.Context, bundleID string) error {
	a.f.Rediscover()
	_, err := a.f.CleanOrphans(ctx, a.hub, bundleID)
	return err
}

func TestFamilyConformance(t *testing.T) {
	conformance.Run(t, func(t testing.TB, fx *conformance.Fixture, bundleID string) any {
		return newAdapter(t, fx, bundleID)
	})
}
