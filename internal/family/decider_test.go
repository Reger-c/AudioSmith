package family

import (
	"context"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/hehehai/AudioSmith/internal/catalog"
	"github.com/hehehai/actnowkit/conformance"
	"github.com/hehehai/actnowkit/models"
)

const (
	dictaID    = "dev.hehehai.dicta"
	dictaDevID = "dev.hehehai.dicta.dev"
	pikID      = "dev.hehehai.pik"
)

// started is AudioSmith started in the fixture with the test catalogue
// installed and migrated (claimed).
func started(t *testing.T) (*testFamily, *conformance.Fixture) {
	t.Helper()
	fx := conformance.New(t)
	install(fx, refA, refG, refB)
	f := newFamily(t, fx, "")
	if err := f.Start(context.Background(), inputs(fx.Hub), wantedOf()); err != nil {
		t.Fatal(err)
	}
	return f, fx
}

func claim(t *testing.T, fx *conformance.Fixture, bid string, refs ...models.Ref) {
	t.Helper()
	for _, r := range refs {
		if err := fx.Peer(bid).Claim(context.Background(), models.Claim{Repo: r.Repo, Revision: r.Revision}); err != nil {
			t.Fatal(err)
		}
	}
}

// The branches of Decide (spec §7.6): nobody else, another app (named),
// only a dev build (protects, unnamed), an uninstalled app (an orphan,
// protects nothing), an unreadable claims file (unsure: keep).
func TestDecide(t *testing.T) {
	f, fx := started(t)
	d := f.Decider(fx.Hub)
	dec, err := d.Decide(refB)
	if err != nil || dec.InUse() || !dec.Mine || len(DecisionNames(dec)) != 0 {
		t.Errorf("nobody else: %+v %v", dec, err)
	}

	fx.Register(dictaID)
	f.Rediscover()
	claim(t, fx, dictaID, refB)
	dec, _ = d.Decide(refB)
	if !dec.InUse() || !slices.Equal(DecisionNames(dec), []string{"Dicta"}) {
		t.Errorf("Dicta: %+v", dec)
	}

	claim(t, fx, dictaDevID, refA)
	dec, _ = d.Decide(refA)
	if !dec.InUse() || len(DecisionNames(dec)) != 0 || dec.UserNames()[0] != "Dicta (dev)" {
		t.Errorf("only a dev build: %+v (names %v)", dec, DecisionNames(dec))
	}

	fx.Register(pikID)
	claim(t, fx, pikID, refG)
	fx.Uninstall(pikID)
	f.Rediscover()
	dec, _ = d.Decide(refG)
	if dec.InUse() || len(dec.Orphans) != 1 || dec.Orphans[0].BundleID != pikID {
		t.Errorf("uninstalled Pik: %+v", dec)
	}

	os.WriteFile(fx.Family.ClaimsFile("dev.hehehai.auralink"), []byte("{broken"), 0o600)
	dec, _ = d.Decide(refG)
	if !dec.InUse() || !dec.Unsure || len(DecisionNames(dec)) != 0 {
		t.Errorf("unreadable claims: %+v", dec)
	}
}

// Another app deleted a model AudioSmith uses for all apps: the claim
// goes, the Models page can say who, until AudioSmith downloads it again.
func TestTombstone(t *testing.T) {
	f, fx := started(t)
	fx.Register(dictaID)
	f.Rediscover()
	ctx := context.Background()
	dicta := fx.Peer(dictaID)
	claim(t, fx, dictaID, refB)
	if res, err := dicta.Delete(ctx, refB, models.AllApps); err != nil || !res.Tombstoned {
		t.Fatalf("Dicta deletes for all apps: %+v %v", res, err)
	}
	f.handle([]string{"models"}) // as the notification does
	if fx.Claimed(ReleaseBundleID, refB) {
		t.Error("the claim on a model deleted for all apps stayed")
	}
	if got := f.DeletedIn(refB.Repo); got != "Dicta" {
		t.Errorf("DeletedIn = %q", got)
	}
	install(fx, refB)
	time.Sleep(200 * time.Millisecond) // the watcher handles Dicta's notification too (on macOS)
	if err := f.Claimed(ctx, fx.Hub, ClaimOf(testCatalog[1], refB.Repo, revB, nil, false, "")); err != nil {
		t.Fatal(err)
	}
	if got := f.DeletedIn(refB.Repo); got != "" {
		t.Errorf("after downloading again: %q", got)
	}
}

// Removing one of AudioSmith's models keeps an auxiliary repo another of
// its models uses, also when deleting for all apps.
func TestDeleteModelSharedAux(t *testing.T) {
	f, fx := started(t)
	c := catalog.Entry{ID: "tts-c", Repo: "acme/tts-c", Revision: revA, Tasks: []catalog.Task{catalog.TaskTTS},
		Aux: []catalog.Aux{{Repo: refG.Repo, Revision: revG}}}
	refC := models.Ref{Repo: c.Repo, Revision: revA}
	install(fx, refC)
	ctx := context.Background()
	f.Claimed(ctx, fx.Hub, ClaimOf(c, refG.Repo, revG, nil, true, ""))
	f.Claimed(ctx, fx.Hub, ClaimOf(c, refC.Repo, revA, nil, false, ""))
	d := f.Decider(fx.Hub)
	for _, scope := range []models.Scope{models.ThisApp, models.AllApps} {
		e := testCatalog[0]
		if scope == models.AllApps {
			e = c
			f.Claimed(ctx, fx.Hub, ClaimOf(testCatalog[0], refG.Repo, revG, nil, true, ""))
		}
		res, err := d.DeleteModel(ctx, refG, e, scope)
		if err != nil || res.Deleted || len(res.OwnModels) != 1 || !fx.Installed(refG) {
			t.Errorf("scope %d: the shared aux repo: %+v %v", scope, res, err)
		}
	}
	g, _ := fx.Claims(ReleaseBundleID).Find(refG)
	if !slices.Equal(g.Users(), []string{"tts-a"}) {
		t.Errorf("aux claim names %v", g.Users())
	}
	// Its last model: it goes.
	if res, err := d.DeleteModel(ctx, refG, testCatalog[0], models.ThisApp); err != nil || !res.Deleted || fx.Installed(refG) {
		t.Errorf("last model: %+v %v", res, err)
	}
}

// The transition: Dicta installed without a manifest uses what the pins
// give it, so removing such a model from AudioSmith keeps the files
// (AudioSmith's claim goes); once Dicta registers, its claims decide.
func TestLegacyFallback(t *testing.T) {
	pins := models.PinsOfApp("dicta")
	if len(pins) == 0 {
		t.Skip("no pins for Dicta")
	}
	ref := pins[0].Ref()
	fx := conformance.New(t)
	install(fx, ref)
	f := newFamily(t, fx, "")
	e := catalog.Entry{ID: "pinned", Repo: ref.Repo, Revision: ref.Revision}
	ctx := context.Background()
	if err := f.Start(ctx, inputs(fx.Hub), wantedIn([]catalog.Entry{e})); err != nil {
		t.Fatal(err)
	}
	dicta, _ := conformance.FakeApp(t.TempDir(), "Dicta", dictaID)
	f.sys[dictaID] = dicta
	f.Rediscover()
	d := f.Decider(fx.Hub)
	dec, _ := d.Decide(ref)
	if !dec.InUse() || !slices.Equal(DecisionNames(dec), []string{"Dicta"}) {
		t.Fatalf("Dicta without a manifest: %+v", dec)
	}
	res, err := d.DeleteModel(ctx, ref, e, models.ThisApp)
	if err != nil || res.Deleted || !fx.Installed(ref) || fx.Claimed(ReleaseBundleID, ref) {
		t.Fatalf("remove from AudioSmith: %+v %v (claimed %v)", res, err, fx.Claimed(ReleaseBundleID, ref))
	}
	// Dicta registers and claims nothing: the files may go.
	fx.Register(dictaID)
	f.Rediscover()
	if res, err := d.Delete(ctx, ref, models.ThisApp); err != nil || !res.Deleted || fx.Installed(ref) {
		t.Errorf("Dicta registered without claiming it: %+v %v", res, err)
	}
}

// Before the migration (or in a read-only directory) deletes are decided
// the same, and nothing is written in the family directory.
func TestDeleteReadOnly(t *testing.T) {
	fx := conformance.New(t)
	install(fx, refA, refB)
	fx.Register(dictaID)
	claim(t, fx, dictaID, refB)
	f := newFamily(t, fx, "")
	f.Rediscover()
	d := f.Decider(fx.Hub)
	ctx := context.Background()
	before := tree(t, fx.Family.Root)
	res, err := d.DeleteModel(ctx, refB, testCatalog[1], models.ThisApp)
	if err != nil || res.Deleted || !fx.Installed(refB) {
		t.Errorf("Dicta's model: %+v %v", res, err)
	}
	res, err = d.Delete(ctx, refA, models.AllApps)
	if err != nil || !res.Deleted || fx.Installed(refA) {
		t.Errorf("nobody's model: %+v %v", res, err)
	}
	if after := tree(t, fx.Family.Root); !mapsEqual(before, after) {
		t.Errorf("wrote in the family directory: %v → %v", keys(before), keys(after))
	}
}

// Storage: the space only AudioSmith uses and shared (named), orphans of
// uninstalled release builds and their clean-up, ForgetAll.
func TestUsageAndOrphans(t *testing.T) {
	f, fx := started(t)
	fx.Register(dictaID)
	claim(t, fx, dictaID, refB)
	claim(t, fx, dictaDevID, refA)
	f.Rediscover()
	u, err := f.Usage(fx.Hub)
	sizeB := models.Check(fx.Hub, models.Claim{Repo: refB.Repo, Revision: revB}).Bytes
	sizeA := models.Check(fx.Hub, models.Claim{Repo: refA.Repo, Revision: revA}).Bytes
	if err != nil || u.Shared != sizeA+sizeB || u.Mine == 0 || !slices.Equal(u.SharedWith, []string{"Dicta"}) {
		t.Errorf("usage %+v %v (A %d, B %d)", u, err, sizeA, sizeB)
	}

	other := models.Ref{Repo: "acme/only-pik", Revision: revA}
	install(fx, other)
	fx.Register(pikID)
	claim(t, fx, pikID, other, refB)
	fx.Register("dev.hehehai.auralink.dev")
	claim(t, fx, "dev.hehehai.auralink.dev", other)
	fx.Uninstall(pikID)
	fx.Uninstall("dev.hehehai.auralink.dev")
	f.Rediscover()
	orphans, err := f.Orphans(fx.Hub)
	if err != nil || len(orphans) != 1 || orphans[0].BundleID != pikID || orphans[0].Bytes == 0 {
		t.Fatalf("orphans %+v %v", orphans, err)
	}
	freed, err := f.CleanOrphans(context.Background(), fx.Hub)
	if err != nil || freed == 0 || fx.Installed(other) || !fx.Installed(refB) {
		t.Errorf("clean up: freed %d, %v (only Pik's %v, Dicta's %v)", freed, err, fx.Installed(other), fx.Installed(refB))
	}
	if _, err := os.Stat(fx.Family.AppFile(pikID)); !os.IsNotExist(err) {
		t.Errorf("Pik's manifest stayed: %v", err)
	}
	if err := f.ForgetAll(fx.Hub); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(fx.Family.ClaimsFile(ReleaseBundleID)); !os.IsNotExist(err) {
		t.Errorf("claims stayed after ForgetAll: %v", err)
	}
}

func TestVisibleNames(t *testing.T) {
	got := VisibleNames(models.User{BundleID: dictaID}, models.User{BundleID: dictaDevID}, models.User{BundleID: "dev.hehehai.pik.m7"},
		models.User{BundleID: ReleaseBundleID}, models.User{BundleID: "com.example.x"}, models.User{BundleID: dictaID}, models.User{BundleID: "dev.hehehai.voxtnew"})
	if !slices.Equal(got, []string{"Dicta", "VoxtNew"}) {
		t.Errorf("VisibleNames = %v", got)
	}
}
