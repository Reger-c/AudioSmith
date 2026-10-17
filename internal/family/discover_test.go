package family

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/hehehai/actnowkit/conformance"
	actnow "github.com/hehehai/actnowkit/family"
	"github.com/hehehai/actnowkit/models"
	"github.com/hehehai/actnowkit/registry"
)

func TestOthersForUI(t *testing.T) {
	app := func(id string) actnow.App { a, _ := actnow.AppByID(id); return a }
	found := []registry.Found{
		{BundleID: "dev.hehehai.dicta", App: app("dicta"), Variant: actnow.Release, Name: "Dicta", Installed: true},
		{BundleID: "dev.hehehai.dicta.dev", App: app("dicta"), Variant: actnow.Dev, Name: "Dicta", Installed: true},
		{BundleID: "dev.hehehai.pik.m7", App: app("pik"), Variant: actnow.Test, Name: "Pik", Installed: true},
		{BundleID: "dev.hehehai.auralink", App: app("auralink"), Variant: actnow.Release, Name: "Auralink"}, // uninstalled
		{BundleID: ReleaseBundleID, App: app("audiosmith"), Variant: actnow.Release, Name: "AudioSmith", Installed: true},
		{BundleID: DevBundleID, App: app("audiosmith"), Variant: actnow.Dev, Name: "AudioSmith", Installed: true},
	}
	if got := othersForUI(found); !slices.Equal(got, []string{"Dicta"}) {
		t.Errorf("othersForUI = %v, want [Dicta]", got)
	}
	if got := othersForUI(found[1:]); len(got) != 0 {
		t.Errorf("only dev/test builds, uninstalled apps and AudioSmith itself: %v", got)
	}

	// Through discovery: manifests of a test variant and of the release.
	fx := conformance.New(t)
	f := newFamily(t, fx, DevBundleID)
	fx.Register("dev.hehehai.voxtnew.conformance")
	f.Rediscover()
	if got := f.OthersForUI(); len(got) != 0 {
		t.Errorf("a test variant counts for the UI: %v", got)
	}
	fx.Register("dev.hehehai.voxtnew")
	f.Rediscover()
	if got := f.OthersForUI(); !slices.Equal(got, []string{"VoxtNew"}) {
		t.Errorf("OthersForUI = %v", got)
	}
	// Found keeps every variant (claims of dev builds still protect).
	if n := len(f.Found()); n != 2 {
		t.Errorf("found %d apps", n)
	}
}

// The phase-0 fallback: Dicta installed without a manifest uses what the
// pins table gives it, until it registers.
func TestLegacyUsers(t *testing.T) {
	pins := models.PinsOfApp("dicta")
	if len(pins) == 0 {
		t.Skip("no pins for Dicta")
	}
	repo := pins[0].Repo
	fx := conformance.New(t)
	f := newFamily(t, fx, "")
	dicta, err := conformance.FakeApp(t.TempDir(), "Dicta", "dev.hehehai.dicta")
	if err != nil {
		t.Fatal(err)
	}
	f.Rediscover()
	if u := f.LegacyUsers(repo); len(u) != 0 {
		t.Errorf("Dicta not installed: %+v", u)
	}
	f.sys["dev.hehehai.dicta"] = dicta
	f.Rediscover()
	u := f.LegacyUsers(repo)
	if len(u) != 1 || u[0].Name != "Dicta" || u[0].BundleID != "dev.hehehai.dicta" {
		t.Errorf("legacy users %+v", u)
	}
	if u := f.LegacyUsers("acme/not-pinned"); len(u) != 0 {
		t.Errorf("a repo outside the pins: %+v", u)
	}
	// Dicta registers: its claims decide from now on.
	fx.Register("dev.hehehai.dicta")
	f.Rediscover()
	if u := f.LegacyUsers(repo); len(u) != 0 {
		t.Errorf("Dicta has a manifest: %+v", u)
	}
	// AudioSmith's own builds are never legacy users.
	f.sys[ReleaseBundleID] = dicta
	f.Rediscover()
	if u := legacyUsers(f.Found(), repo); slices.ContainsFunc(u, func(x models.User) bool { return x.AppID == AppID }) {
		t.Errorf("AudioSmith counted: %+v", u)
	}
}

// addMember adds an unknown member to a JSON file.
func addMember(t *testing.T, path, key, value string) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	m[key] = json.RawMessage(value)
	b, _ = json.Marshal(m)
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
}

func member(t *testing.T, path, key string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]json.RawMessage
	json.Unmarshal(b, &m)
	var buf bytes.Buffer
	json.Compact(&buf, m[key])
	return buf.String()
}

// Spec §16.1 scenario 9: unknown members survive AudioSmith's
// read-modify-writes; files of a newer major are not written.
func TestForwardCompatibility(t *testing.T) {
	fx := conformance.New(t)
	install(fx, refA, refG, refB)
	ctx := context.Background()
	f := newFamily(t, fx, "")
	if err := f.Start(ctx, inputs(fx.Hub), wantedOf()); err != nil {
		t.Fatal(err)
	}
	app, claims := fx.Family.AppFile(ReleaseBundleID), fx.Family.ClaimsFile(ReleaseBundleID)
	addMember(t, app, "fromTheFuture", `{"x":1}`)
	addMember(t, claims, "fromTheFuture", `[1,2]`)
	in := inputs(fx.Hub)
	in.Version = "0.4.0-beta.7"
	if err := f.Update(ctx, in, wantedOf()); err != nil {
		t.Fatal(err)
	}
	if err := f.Release(fx.Hub, "asr-b", refB.Repo); err != nil {
		t.Fatal(err)
	}
	if m, _ := registry.Read(fx.Family, ReleaseBundleID); m.Version != "0.4.0-beta.7" {
		t.Errorf("manifest not rewritten: %+v", m)
	}
	if member(t, app, "fromTheFuture") != `{"x":1}` || member(t, claims, "fromTheFuture") != `[1,2]` {
		t.Errorf("unknown members lost: %q %q", member(t, app, "fromTheFuture"), member(t, claims, "fromTheFuture"))
	}

	// A manifest of schema /2: read only.
	addMember(t, app, "schema", `"actnow.app/2"`)
	before, _ := os.ReadFile(app)
	in.Version = "0.4.0-beta.8"
	if err := f.Update(ctx, in, wantedOf()); !errors.Is(err, actnow.ErrNewerSchema) {
		t.Errorf("Update over a newer manifest: %v", err)
	}
	if after, _ := os.ReadFile(app); string(after) != string(before) {
		t.Error("a newer manifest was rewritten")
	}

	// A claims file of schema /2: no claim, no migration, no manifest.
	fx2 := conformance.New(t)
	install(fx2, refA, refG, refB)
	f2 := newFamily(t, fx2, "")
	path := fx2.Family.ClaimsFile(ReleaseBundleID)
	os.MkdirAll(filepath.Dir(path), 0o700)
	os.WriteFile(path, []byte(`{"schema":"actnow.models.claims/2","app":"audiosmith","bundleId":"dev.hehehai.audiosmith","root":"/elsewhere","claims":[]}`), 0o600)
	before, _ = os.ReadFile(path)
	if err := f2.Start(ctx, inputs(fx2.Hub), wantedOf()); !errors.Is(err, ErrReadOnly) {
		t.Errorf("Start over newer claims: %v", err)
	}
	if after, _ := os.ReadFile(path); string(after) != string(before) {
		t.Error("newer claims were rewritten")
	}
	if _, err := os.Stat(fx2.Family.AppFile(ReleaseBundleID)); !os.IsNotExist(err) {
		t.Errorf("manifest written without claims: %v", err)
	}
}

// A corrupt claims file is set aside, not overwritten, and the
// migration runs.
func TestCorruptClaimsQuarantined(t *testing.T) {
	fx := conformance.New(t)
	install(fx, refB)
	path := fx.Family.ClaimsFile(ReleaseBundleID)
	os.MkdirAll(filepath.Dir(path), 0o700)
	os.WriteFile(path, []byte("{not json"), 0o600)
	f := newFamily(t, fx, "")
	if err := f.Start(context.Background(), inputs(fx.Hub), wantedOf()); err != nil {
		t.Fatal(err)
	}
	if !fx.Claimed(ReleaseBundleID, refB) {
		t.Error("not migrated")
	}
	matches, _ := filepath.Glob(path + ".corrupt-*")
	if len(matches) != 1 {
		t.Errorf("corrupt file not set aside: %v", matches)
	}
}
