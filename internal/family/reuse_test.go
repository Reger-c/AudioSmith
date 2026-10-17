package family

import (
	"context"
	"slices"
	"testing"

	"github.com/hehehai/AudioSmith/internal/engine/proto"
	"github.com/hehehai/actnowkit/conformance"
	"github.com/hehehai/actnowkit/models"
)

// fresh is AudioSmith started in the fixture with nothing migrated (a
// first run: the models on disk are other apps').
func fresh(t *testing.T, refs ...models.Ref) (*testFamily, *conformance.Fixture) {
	t.Helper()
	fx := conformance.New(t)
	install(fx, refs...)
	f := newFamily(t, fx, "")
	if err := f.Start(context.Background(), inputs(fx.Hub), wantedIn(nil)); err != nil {
		t.Fatal(err)
	}
	return f, fx
}

func surveyed(s Survey) []string {
	var out []string
	for _, m := range s.Models {
		out = append(out, m.Entry.ID)
	}
	return out
}

// The reuse card's survey (spec §7.10.1): every model of the pack on
// disk, some, none; installed by a named app, by nobody known, by a dev
// build only (reusable, not named); not what AudioSmith claims already.
func TestSurvey(t *testing.T) {
	t.Run("all", func(t *testing.T) {
		f, fx := fresh(t, refA, refG, refB)
		fx.Register(dictaID)
		f.Rediscover()
		claim(t, fx, dictaID, refA, refG, refB)
		s := f.Survey(fx.Hub, testCatalog)
		if got := surveyed(s); !slices.Equal(got, []string{"tts-a", "asr-b"}) || s.By() != "Dicta" {
			t.Fatalf("models %v by %q", got, s.By())
		}
		want := int64(0)
		for _, r := range []models.Ref{refA, refG, refB} {
			want += models.Check(fx.Hub, models.Claim{Repo: r.Repo, Revision: r.Revision, Aux: true}).Bytes
		}
		if s.Bytes != want || want == 0 {
			t.Errorf("bytes %d, want %d", s.Bytes, want)
		}
		if c := s.Models[0].Claims; len(c) != 2 || c[0].Origin != models.OriginReused {
			t.Errorf("claims %+v", c)
		}
	})
	t.Run("some", func(t *testing.T) {
		f, fx := fresh(t, refB, refA) // tts-a's auxiliary repo is missing
		fx.Register(dictaID)
		f.Rediscover()
		claim(t, fx, dictaID, refB, refA)
		if got := surveyed(f.Survey(fx.Hub, testCatalog)); !slices.Equal(got, []string{"asr-b"}) {
			t.Errorf("models %v", got)
		}
	})
	t.Run("none", func(t *testing.T) {
		f, fx := fresh(t)
		if s := f.Survey(fx.Hub, testCatalog); len(s.Models) != 0 || s.Bytes != 0 {
			t.Errorf("survey %+v", s)
		}
	})
	t.Run("unknown installer", func(t *testing.T) {
		f, fx := fresh(t, refB)
		s := f.Survey(fx.Hub, testCatalog)
		if got := surveyed(s); !slices.Equal(got, []string{"asr-b"}) || s.By() != "" {
			t.Errorf("models %v by %q", got, s.By())
		}
	})
	t.Run("dev build only", func(t *testing.T) {
		f, fx := fresh(t, refB)
		fx.Register(dictaDevID)
		f.Rediscover()
		claim(t, fx, dictaDevID, refB)
		s := f.Survey(fx.Hub, testCatalog)
		if got := surveyed(s); !slices.Equal(got, []string{"asr-b"}) || s.By() != "" {
			t.Errorf("models %v by %q", got, s.By())
		}
	})
	t.Run("uninstalled installer", func(t *testing.T) {
		f, fx := fresh(t, refB)
		fx.Register(dictaID)
		claim(t, fx, dictaID, refB)
		fx.Uninstall(dictaID)
		f.Rediscover()
		if s := f.Survey(fx.Hub, testCatalog); len(s.Models) != 1 || s.By() != "" {
			t.Errorf("survey %+v", s)
		}
	})
	t.Run("claimed already", func(t *testing.T) {
		f, fx := started(t) // migrated: AudioSmith claims everything
		fx.Register(dictaID)
		f.Rediscover()
		claim(t, fx, dictaID, refB)
		if s := f.Survey(fx.Hub, testCatalog); len(s.Models) != 0 {
			t.Errorf("survey %v", surveyed(s))
		}
	})
}

// "Use them" claims the models, origin reused; a second time writes
// nothing; Dicta's claims stay; the next survey finds nothing.
func TestReuse(t *testing.T) {
	f, fx := fresh(t, refA, refG, refB)
	fx.Register(dictaID)
	f.Rediscover()
	claim(t, fx, dictaID, refA, refG, refB)
	ctx := context.Background()
	s := f.Survey(fx.Hub, testCatalog)
	var all []models.Claim
	for _, m := range s.Models {
		all = append(all, m.Claims...)
	}
	n, err := f.Reuse(ctx, fx.Hub, all)
	if err != nil || n != 3 {
		t.Fatalf("reuse: %d %v", n, err)
	}
	for _, r := range []models.Ref{refA, refG, refB} {
		c, ok := fx.Claims(ReleaseBundleID).Find(r)
		if !ok || c.Origin != models.OriginReused || !fx.Claimed(dictaID, r) {
			t.Errorf("%s: %+v %v", r, c, ok)
		}
	}
	if got, _ := fx.Claims(ReleaseBundleID).Find(refA); got.Model != "tts-a" || got.Aux {
		t.Errorf("main claim %+v", got)
	}
	if got, _ := fx.Claims(ReleaseBundleID).Find(refG); !got.Aux {
		t.Errorf("aux claim %+v", got)
	}
	if n, err := f.Reuse(ctx, fx.Hub, all); n != 0 || err != nil {
		t.Errorf("again: %d %v", n, err)
	}
	if s := f.Survey(fx.Hub, testCatalog); len(s.Models) != 0 {
		t.Errorf("survey after: %v", surveyed(s))
	}
}

// After "Remove from AudioSmith" (another app keeps the files) the model
// is AudioSmith's again when it uses it: the engine loading it claims it
// (Runtime's Loaded hook → Reuse), origin reused.
func TestReclaimOnUse(t *testing.T) {
	f, fx := started(t)
	fx.Register(dictaID)
	f.Rediscover()
	claim(t, fx, dictaID, refB)
	if err := f.Release(fx.Hub, "asr-b", refB.Repo); err != nil {
		t.Fatal(err)
	}
	if fx.Claimed(ReleaseBundleID, refB) {
		t.Fatal("still claimed")
	}
	if sh := f.Shares(fx.Hub, testCatalog)["acme/asr-b"]; sh.Mine || !slices.Equal(sh.With, []string{"Dicta"}) {
		t.Errorf("share after the removal %+v", sh)
	}
	ctx := context.Background()
	var loaded [][]string
	rt := f.Runtime(RuntimeOptions{Loaded: func(repos []string) {
		loaded = append(loaded, repos)
		for _, repo := range repos {
			for _, e := range testCatalog {
				if e.Repo == repo {
					if _, err := f.Reuse(ctx, fx.Hub, EntryClaims(fx.Hub, e)); err != nil {
						t.Error(err)
					}
				}
			}
		}
	}})
	rt.Publish(loadedModels("acme/asr-b"), 1<<30)
	rt.Publish(loadedModels("acme/asr-b"), 1<<30) // the same: no second call
	if len(loaded) != 1 {
		t.Errorf("Loaded calls %v", loaded)
	}
	c, ok := fx.Claims(ReleaseBundleID).Find(refB)
	if !ok || c.Origin != models.OriginReused || c.Model != "asr-b" {
		t.Errorf("claim on use %+v %v", c, ok)
	}
	if sh := f.Shares(fx.Hub, testCatalog)["acme/asr-b"]; !sh.Mine || !slices.Equal(sh.With, []string{"Dicta"}) {
		t.Errorf("share after use %+v", sh)
	}
	rt.Close()
}

// Before the migration, Reuse waits for it; a read-only directory gets
// nothing.
func TestReuseBeforeStart(t *testing.T) {
	fx := conformance.New(t)
	install(fx, refB)
	f := newFamily(t, fx, "")
	ctx := context.Background()
	if n, err := f.Reuse(ctx, fx.Hub, EntryClaims(fx.Hub, testCatalog[1])); n != 0 || err != nil {
		t.Fatal(n, err)
	}
	if err := f.Start(ctx, inputs(fx.Hub), wantedIn(nil)); err != nil {
		t.Fatal(err)
	}
	if c, ok := fx.Claims(ReleaseBundleID).Find(refB); !ok || c.Origin != models.OriginReused {
		t.Errorf("claim %+v %v", c, ok)
	}
}

// Shares: AudioSmith's own claim, named apps' (release builds,
// installed), not a dev build's or an uninstalled app's.
func TestShares(t *testing.T) {
	f, fx := started(t)
	fx.Register(dictaID)
	fx.Register(dictaDevID)
	fx.Register(pikID)
	claim(t, fx, dictaID, refA)
	claim(t, fx, dictaDevID, refB)
	claim(t, fx, pikID, refA, refB)
	fx.Uninstall(pikID)
	f.Rediscover()
	sh := f.Shares(fx.Hub, testCatalog)
	if a := sh["acme/tts-a"]; !a.Mine || !slices.Equal(a.With, []string{"Dicta"}) {
		t.Errorf("tts-a %+v", a)
	}
	if b := sh["acme/asr-b"]; !b.Mine || len(b.With) != 0 {
		t.Errorf("asr-b %+v", b)
	}
}

// LoadedIn names release builds only.
func TestLoadedInVisibleOnly(t *testing.T) {
	f := startedFamily(t)
	f.fx.Register(dictaDevID)
	f.Rediscover()
	peerHolds(t, f, dictaDevID, 1<<20)
	if got := f.Runtime(RuntimeOptions{}).LoadedIn("mlx-community/Qwen3-ASR-0.6B-4bit", ""); len(got) != 0 {
		t.Errorf("a dev build named: %v", got)
	}
}

// loadedModels is the gpu worker holding repos.
func loadedModels(repos ...string) map[proto.Role][]proto.LoadedModel {
	var list []proto.LoadedModel
	for _, r := range repos {
		list = append(list, proto.LoadedModel{Model: r, Bytes: 1 << 20})
	}
	return map[proto.Role][]proto.LoadedModel{proto.RoleGPU: list}
}
