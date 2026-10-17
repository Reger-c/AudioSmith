package family

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/hehehai/AudioSmith/internal/catalog"
	"github.com/hehehai/actnowkit/conformance"
	actnow "github.com/hehehai/actnowkit/family"
	"github.com/hehehai/actnowkit/models"
	"github.com/hehehai/actnowkit/registry"
)

// TestMacFamily runs Start as the app does, with the real LaunchServices
// and code-signing lookups, against the family directory and model
// folder of the environment, which must be throwaway ones:
//
//	AUDIOSMITH_MAC=1 ACTNOW_FAMILY_DIR=/tmp/c72/ActNow HF_HUB_CACHE=/tmp/c72/hub \
//	  AUDIOSMITH_APP=…/AudioSmith.app go test -run TestMacFamily -v ./internal/family
//
// It puts stand-in snapshots of three catalogue models in the folder, and
// leaves what it wrote for `actnow-conformance check|discover|claims`.
// AUDIOSMITH_APP (optional) is an AudioSmith.app build: the manifest then
// names it, with its bundle id.
func TestMacFamily(t *testing.T) {
	if os.Getenv("AUDIOSMITH_MAC") == "" || runtime.GOOS != "darwin" {
		t.Skip("set AUDIOSMITH_MAC=1 on a Mac")
	}
	dir, hub := os.Getenv(actnow.EnvDir), os.Getenv("HF_HUB_CACHE")
	home, _ := os.UserHomeDir()
	userDirs := []string{filepath.Join(home, "Library", "Application Support", "ActNow"), filepath.Join(home, ".cache", "huggingface", "hub")}
	if dir == "" || hub == "" {
		t.Skip("set ACTNOW_FAMILY_DIR and HF_HUB_CACHE to throwaway directories")
	}
	if models.SameHub(dir, userDirs[0]) || models.SameHub(hub, userDirs[1]) {
		t.Fatalf("ACTNOW_FAMILY_DIR and HF_HUB_CACHE must be throwaway directories, not %v", userDirs)
	}
	fx := &conformance.Fixture{T: t, Family: actnow.At(dir), Hub: hub}
	var picked []catalog.Entry
	for _, e := range catalog.Builtin().Entries {
		if len(e.Files) > 0 || len(picked) == 3 {
			continue
		}
		picked = append(picked, e)
		for _, a := range e.Aux {
			if len(a.Files) == 0 {
				fx.Install(conformance.TestRepo(a.Repo, a.Revision, 1024))
			}
		}
		fx.Install(conformance.TestRepo(e.Repo, e.Revision, 1024))
	}
	o := Options{Dir: fx.Family, Logf: t.Logf}
	if app := os.Getenv("AUDIOSMITH_APP"); app != "" {
		id, err := registry.BundleIDAt(app)
		if err != nil {
			t.Fatal(err)
		}
		o.AppPath, o.BundleID = app, id
	}
	f := New(o)
	defer f.Close()
	in := Inputs{Version: "0.4.0-beta.5", Beta: true, Hub: hub, DataDir: filepath.Join(filepath.Dir(dir), "data"),
		Shortcut: "CmdOrCtrl+Shift+Space", ShortcutEnabled: true, APIEnabled: true, APIPort: 3901}
	if err := f.Start(context.Background(), in, func(h string) []models.Claim {
		return Wanted(h, catalog.Builtin().Entries, nil)
	}); err != nil {
		t.Fatal(err)
	}
	m, err := registry.Read(fx.Family, f.BundleID)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("manifest: %s %s variant %s path %s team %q", m.BundleID, m.Version, m.Variant, m.Path, m.TeamID)
	c := fx.Claims(f.BundleID)
	for _, cl := range c.Claims {
		t.Logf("claim: %s %s %s aux=%v", cl.Ref(), cl.Model, cl.Origin, cl.Aux)
	}
	for _, e := range picked {
		if !fx.Claimed(f.BundleID, models.Ref{Repo: e.Repo, Revision: e.Revision}) {
			t.Errorf("%s not claimed", e.ID)
		}
	}
	for _, a := range f.Found() {
		t.Logf("found: %s %s installed=%v running=%v manifest=%v path=%s", a.BundleID, a.Variant, a.Installed, a.Running, a.Manifest != nil, a.Path)
	}
	t.Logf("others for the UI: %v", f.OthersForUI())
}
