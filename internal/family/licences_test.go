package family

import (
	"context"
	"testing"

	"github.com/hehehai/actnowkit/conformance"
	"github.com/hehehai/actnowkit/models"
)

const omni = "mlx-community/OmniVoice-bf16"

// Licences (spec §7.9): AudioSmith's acceptance is recorded for the
// family; another app's is named when the UI may name it (an installed
// release build); AudioSmith's own is not "another app's".
func TestLicences(t *testing.T) {
	ctx := context.Background()
	f, fx := started(t)
	if name, _ := f.LicenceAcceptedIn(omni, "cc-by-nc-4.0"); name != "" {
		t.Fatalf("accepted in %q before anyone did", name)
	}
	if err := f.AcceptLicence(ctx, omni, "cc-by-nc-4.0"); err != nil {
		t.Fatal(err)
	}
	acc, ok := models.LicenceAccepted(fx.Family, omni, "CC-BY-NC-4.0")
	if !ok || acc.By != ReleaseBundleID || acc.Licence != "CC-BY-NC-4.0" {
		t.Fatalf("recorded %+v %v", acc, ok)
	}
	if name, _ := f.LicenceAcceptedIn(omni, "cc-by-nc-4.0"); name != "" {
		t.Errorf("AudioSmith's own acceptance named %q", name)
	}
	// Dicta accepts it (its record replaces AudioSmith's).
	fx.Register(dictaID)
	f.Rediscover()
	if err := models.AcceptLicence(ctx, fx.Family, omni, "CC-BY-NC-4.0", dictaID); err != nil {
		t.Fatal(err)
	}
	name, at := f.LicenceAcceptedIn(omni, "cc-by-nc-4.0")
	if name != "Dicta" || at.IsZero() {
		t.Errorf("accepted in %q at %v", name, at)
	}
	if name, _ := f.LicenceAcceptedIn(omni, "apache-2.0"); name != "" {
		t.Errorf("another licence: %q", name)
	}
	// A dev build's acceptance is not named.
	if err := models.AcceptLicence(ctx, fx.Family, omni, "CC-BY-NC-4.0", dictaDevID); err != nil {
		t.Fatal(err)
	}
	if name, _ := f.LicenceAcceptedIn(omni, "cc-by-nc-4.0"); name != "" {
		t.Errorf("a dev build named: %q", name)
	}
}

// A read-only (or not yet set up) directory gets no acceptance.
func TestAcceptLicenceBeforeStart(t *testing.T) {
	fx := conformance.New(t)
	f := newFamily(t, fx, "")
	if err := f.AcceptLicence(context.Background(), omni, "cc-by-nc-4.0"); err != nil {
		t.Fatal(err)
	}
	if _, ok := models.LicenceAccepted(fx.Family, omni, "CC-BY-NC-4.0"); ok {
		t.Error("written before Start")
	}
}
