package family

// Regression test of the family audit's C1 (released 0.4.0-beta.1–4,
// fixed in beta.5): AudioSmith's "Clean up" of removed models
// (hub.RemoveLeftovers, models_removed.go cleanRemoved) against Dicta's
// default LLM gemma-4-e4b, which AudioSmith's catalogue removed, in the
// states Dicta can be in: none of them loses it. Written in the audit (#18).

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/hehehai/AudioSmith/internal/hub"
	"github.com/hehehai/actnowkit/conformance"
	"github.com/hehehai/actnowkit/models"
	"github.com/hehehai/actnowkit/registry"
	famruntime "github.com/hehehai/actnowkit/runtime"
)

// lsSystem knows some apps through "LaunchServices" (no manifest needed).
type lsSystem struct{ paths map[string]string }

func (s lsSystem) AppPaths(bid string) []string {
	if p, ok := s.paths[bid]; ok {
		return []string{p}
	}
	return nil
}
func (lsSystem) Running(string) []registry.Process { return nil }
func (lsSystem) BundleID(p string) (string, error) { return registry.BundleIDAt(p) }

func TestCleanupKeepsDictaGemma(t *testing.T) {
	const dicta = "dev.hehehai.dicta"
	gemma := conformance.TestRepo("mlx-community/gemma-4-e4b-it-4bit", "475b9088d29754a3379866cf5aeb6b41acd313c2", 64<<10)
	other := "1111111111111111111111111111111111111111"
	cases := []struct {
		name     string
		setup    func(t *testing.T, fx *conformance.Fixture, sys *lsSystem)
		wantKept bool
	}{
		{"registered+claimed", func(t *testing.T, fx *conformance.Fixture, sys *lsSystem) {
			fx.Register(dicta)
			must(t, fx.Peer(dicta).Claim(context.Background(), models.Claim{Repo: gemma.Ref.Repo, Revision: gemma.Ref.Revision, Model: "gemma"}))
		}, true},
		{"legacy: installed, no manifest, no claims (pins rule)", func(t *testing.T, fx *conformance.Fixture, sys *lsSystem) {
			p, err := conformance.FakeApp(filepath.Join(filepath.Dir(fx.Family.Root), "Applications"), "Dicta", dicta)
			must(t, err)
			sys.paths[dicta] = p
		}, true},
		{"dev build: claims, no manifest, unknown to LaunchServices", func(t *testing.T, fx *conformance.Fixture, sys *lsSystem) {
			must(t, fx.Peer(dicta+".dev").Claim(context.Background(), models.Claim{Repo: gemma.Ref.Repo, Revision: gemma.Ref.Revision}))
		}, true},
		{"registered, loaded (runtime file) but claim missing", func(t *testing.T, fx *conformance.Fixture, sys *lsSystem) {
			fx.Register(dicta)
			must(t, famruntime.Write(fx.Family, famruntime.File{BundleID: dicta, Loaded: []famruntime.Loaded{{Repo: gemma.Ref.Repo, Revision: gemma.Ref.Revision, Bytes: 1}}}))
		}, true},
		{"registered, claims gemma at another revision only (pin moved)", func(t *testing.T, fx *conformance.Fixture, sys *lsSystem) {
			fx.Register(dicta)
			// a second snapshot on disk that Dicta claims; the old one is not Dicta's
			alt := conformance.TestRepo(gemma.Ref.Repo, other, 1<<10)
			fx.Install(alt)
			must(t, fx.Peer(dicta).Claim(context.Background(), models.Claim{Repo: alt.Ref.Repo, Revision: alt.Ref.Revision}))
		}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fx := conformance.New(t)
			fx.Install(gemma)
			sys := &lsSystem{paths: map[string]string{}}
			c.setup(t, fx, sys)
			app, err := conformance.FakeApp(filepath.Join(filepath.Dir(fx.Family.Root), "Applications", "as"), "AudioSmith", "dev.hehehai.audiosmith")
			must(t, err)
			team := ""
			f := New(Options{Dir: fx.Family, BundleID: "dev.hehehai.audiosmith", System: sys, AppPath: app, TeamID: &team, Logf: t.Logf})
			t.Cleanup(f.Close)
			must(t, f.Start(context.Background(), Inputs{Version: "0.4.0", Hub: fx.Hub}, func(string) []models.Claim { return nil }))
			f.Rediscover()
			fam := hub.Family{BundleID: f.BundleID, Decider: f.Decider(fx.Hub)}
			freed, kept, err := hub.RemoveLeftovers(context.Background(), fx.Hub, []string{gemma.Ref.Repo}, fam)
			snaps, _ := os.ReadDir(filepath.Join(models.RepoDir(fx.Hub, gemma.Ref.Repo), "snapshots"))
			var names []string
			for _, s := range snaps {
				names = append(names, s.Name()[:8])
			}
			t.Logf("freed %d, kept %d, err %v, snapshots left %v", freed, len(kept), err, names)
			still := fx.Installed(gemma.Ref)
			if c.name == "registered, claims gemma at another revision only (pin moved)" {
				still = fx.Installed(models.Ref{Repo: gemma.Ref.Repo, Revision: other})
			}
			if still != c.wantKept {
				t.Errorf("Dicta's gemma kept=%v, want %v", still, c.wantKept)
			}
		})
	}
}

func must(t testing.TB, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
