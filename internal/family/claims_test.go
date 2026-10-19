package family

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/hehehai/AudioSmith/internal/catalog"
	"github.com/hehehai/actnowkit/conformance"
	actnow "github.com/hehehai/actnowkit/family"
	"github.com/hehehai/actnowkit/models"
	"github.com/hehehai/actnowkit/registry"
)

// A download that finishes before the migration is claimed after it,
// origin downloaded; later ones at once.
func TestClaimedWaitsForTheMigration(t *testing.T) {
	fx := conformance.New(t)
	custom := models.Ref{Repo: "someone/custom-tts", Revision: "dddddddddddddddddddddddddddddddddddddddd"}
	install(fx, refB, custom)
	ctx := context.Background()
	f := newFamily(t, fx, "")
	c := ClaimOf(catalog.Custom(custom.Repo), custom.Repo, custom.Revision, nil, false, "")
	if err := f.Claimed(ctx, fx.Hub, c); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(fx.Family.ClaimsFile(ReleaseBundleID)); !os.IsNotExist(err) {
		t.Fatal("claimed before the migration")
	}
	if err := f.Start(ctx, inputs(fx.Hub), wantedOf()); err != nil {
		t.Fatal(err)
	}
	got, ok := fx.Claims(ReleaseBundleID).Find(custom)
	if !ok || got.Origin != models.OriginDownloaded || got.Model != "custom:someone/custom-tts" || got.Role != "tts" {
		t.Errorf("custom claim %+v", got)
	}
	if got, _ := fx.Claims(ReleaseBundleID).Find(refB); got.Origin != models.OriginMigrated {
		t.Errorf("migrated claim %+v", got)
	}

	// After the migration: at once (ClaimLocked, the downloader holds the lock).
	install(fx, refA)
	held, err := models.LockRepo(ctx, fx.Hub, refA.Repo, actnow.Exclusive, models.Owner{BundleID: ReleaseBundleID, Op: models.OpFinish})
	if err != nil {
		t.Fatal(err)
	}
	err = f.Claimed(ctx, fx.Hub, ClaimOf(testCatalog[0], refA.Repo, revA, nil, false, ""))
	held.Release()
	if err != nil || !fx.Claimed(ReleaseBundleID, refA) {
		t.Errorf("claim after the migration: %v", err)
	}
	// Another folder (a download started before the folder changed): nothing.
	other := filepath.Join(t.TempDir(), "hub")
	if err := f.Claimed(ctx, other, ClaimOf(testCatalog[0], refA.Repo, revA, nil, false, "")); err != nil {
		t.Fatal(err)
	}
	if c := fx.Claims(ReleaseBundleID); !models.SameHub(c.Root, fx.Hub) || len(c.Claims) != 3 {
		t.Errorf("claims after a download in another folder: %+v", c)
	}
}

// Update rewrites the manifest when its inputs change, and migrates a
// new model folder before naming it.
func TestUpdate(t *testing.T) {
	fx := conformance.New(t)
	install(fx, refB)
	ctx := context.Background()
	f := newFamily(t, fx, "")
	in := inputs(fx.Hub)
	if err := f.Update(ctx, in, wantedOf()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(fx.Family.AppFile(ReleaseBundleID)); !os.IsNotExist(err) {
		t.Fatal("Update before Start wrote the manifest")
	}
	if err := f.Start(ctx, in, wantedOf()); err != nil {
		t.Fatal(err)
	}
	app := fx.Family.AppFile(ReleaseBundleID)
	st, _ := os.Stat(app)
	// Unchanged inputs: no write.
	os.Chtimes(app, time.Unix(1, 0), time.Unix(1, 0))
	f.Update(ctx, in, wantedOf())
	if st2, _ := os.Stat(app); st2.ModTime().Unix() != 1 {
		t.Errorf("rewritten without a change (%v → %v)", st.ModTime(), st2.ModTime())
	}
	in.Shortcut = "Ctrl+Alt+D"
	if err := f.Update(ctx, in, wantedOf()); err != nil {
		t.Fatal(err)
	}
	if m, _ := registry.Read(fx.Family, ReleaseBundleID); m.Shortcuts[0].Keys != "Control+Option+D" {
		t.Errorf("shortcut %+v", m.Shortcuts)
	}

	// A new model folder holding asr-b at the same commit.
	hub2 := filepath.Join(t.TempDir(), "hub2")
	fx2 := *fx
	fx2.Hub = hub2
	(&fx2).Install(conformance.TestRepo(refB.Repo, refB.Revision, 64))
	in.Hub = hub2
	if err := f.Update(ctx, in, wantedOf()); err != nil {
		t.Fatal(err)
	}
	m, _ := registry.Read(fx.Family, ReleaseBundleID)
	c := fx.Claims(ReleaseBundleID)
	if m.Models.Root != hub2 || !models.SameHub(c.Root, hub2) {
		t.Errorf("root: manifest %s, claims %s", m.Models.Root, c.Root)
	}
	if got, ok := c.Find(refB); !ok || got.Origin != models.OriginMigrated {
		t.Errorf("claims in the new folder %+v", c.Claims)
	}
}

// Release takes one model off a claim other models share.
func TestRelease(t *testing.T) {
	fx := conformance.New(t)
	install(fx, refA, refG, refB)
	shares := append(slices.Clone(testCatalog), catalog.Entry{ID: "tts-c", Repo: "acme/tts-c", Revision: revA,
		Tasks: []catalog.Task{catalog.TaskTTS}, Aux: []catalog.Aux{{Repo: "acme/g2p", Revision: revG}}})
	ctx := context.Background()
	f := newFamily(t, fx, "")
	if err := f.Start(ctx, inputs(fx.Hub), wantedIn(shares)); err != nil {
		t.Fatal(err)
	}
	g, _ := fx.Claims(ReleaseBundleID).Find(refG)
	if !slices.Equal(g.Users(), []string{"tts-a"}) {
		// tts-c is not installed: only tts-a claimed the tokenizer.
		t.Errorf("aux claim users %v", g.Users())
	}
	// tts-c downloaded later, sharing the aux repo.
	install(fx, models.Ref{Repo: "acme/tts-c", Revision: revA})
	f.Claimed(ctx, fx.Hub, ClaimOf(shares[2], "acme/g2p", revG, nil, true, ""))
	f.Claimed(ctx, fx.Hub, ClaimOf(shares[2], "acme/tts-c", revA, nil, false, ""))
	if err := f.Release(fx.Hub, "tts-a", refA.Repo, refG.Repo); err != nil {
		t.Fatal(err)
	}
	c := fx.Claims(ReleaseBundleID)
	if _, ok := c.Find(refA); ok {
		t.Error("tts-a's claim stayed")
	}
	if g, ok := c.Find(refG); !ok || !slices.Equal(g.Users(), []string{"tts-c"}) {
		t.Errorf("the shared aux claim %+v", g)
	}
}

func TestWanted(t *testing.T) {
	fx := conformance.New(t)
	custom := models.Ref{Repo: "someone/custom-tts", Revision: "dddddddddddddddddddddddddddddddddddddddd"}
	install(fx, custom)
	es := append(slices.Clone(testCatalog), catalog.Entry{ID: "files", Repo: "acme/partial", Revision: revA,
		Files: []catalog.File{{Path: "v3/a.bin"}}, Tasks: []catalog.Task{catalog.TaskEnhance}})
	w := Wanted(fx.Hub, es, []string{custom.Repo, "acme/tts-a", "bad repo", "someone/not-here"})
	var got []string
	for _, c := range w {
		got = append(got, c.Model+" "+c.Ref().String())
	}
	want := []string{"tts-a acme/g2p@" + revG, "tts-a acme/tts-a@" + revA, "asr-b acme/asr-b@" + revB,
		"files acme/partial@" + revA, "custom:someone/custom-tts " + custom.String()}
	if !slices.Equal(got, want) {
		t.Errorf("wanted\n%v\nwant\n%v", got, want)
	}
	if p := w[3]; !slices.Equal(p.Files, []string{"v3/a.bin"}) || p.Role != "enhance" {
		t.Errorf("partial %+v", p)
	}
	if w[0].Role != "other" || !w[0].Aux || w[1].Aux {
		t.Errorf("aux %+v / %+v", w[0], w[1])
	}
}

// Notifications are gathered and handled once; OnChange hears them.
func TestWatch(t *testing.T) {
	fx := conformance.New(t)
	f := newFamily(t, fx, "")
	var mu sync.Mutex
	var heard [][]string
	f.OnChange(func(ts []string) {
		mu.Lock()
		heard = append(heard, ts)
		mu.Unlock()
	})
	if err := f.Start(context.Background(), inputs(fx.Hub), wantedOf()); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond) // Start's own posts
	mu.Lock()
	heard = nil
	mu.Unlock()
	fx.Register("dev.hehehai.dicta")
	fx.Family.Post(actnow.TopicModels)
	// Both topics arrive (a peer that registers also writes its claims
	// file, spec v1.0.9: in one batch or two).
	all := func() []string {
		mu.Lock()
		defer mu.Unlock()
		var out []string
		for _, ts := range heard {
			out = append(out, ts...)
		}
		return out
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if got := all(); slices.Contains(got, actnow.TopicApps) && slices.Contains(got, actnow.TopicModels) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got := all(); !slices.Contains(got, actnow.TopicApps) || !slices.Contains(got, actnow.TopicModels) {
		mu.Lock()
		t.Errorf("heard %v", heard)
		mu.Unlock()
	}
	if got := f.OthersForUI(); !slices.Equal(got, []string{"Dicta"}) {
		t.Errorf("not rediscovered: %v", got)
	}
}
