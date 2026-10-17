package family

// Model licences accepted in any family app (spec §7.9):
// $FAMILY/models/licences.json. AudioSmith records its acceptances there
// too; another app's acceptance changes the wording of AudioSmith's
// licence dialog, which still shows the licence and still asks.
// Revoking in AudioSmith revokes only its own record (internal/licence):
// the spec defines no family revocation.

import (
	"context"
	"strings"
	"time"

	"github.com/hehehai/actnowkit/models"
)

// LicenceID is a catalogue licence id ("cc-by-nc-4.0") as the family
// file names it, SPDX-like ("CC-BY-NC-4.0").
func LicenceID(id string) string {
	if strings.HasPrefix(strings.ToLower(id), "cc-") {
		return strings.ToUpper(id)
	}
	return id
}

// AcceptLicence records that the user accepted repo's licence in
// AudioSmith; nothing while the family directory is not usable (before
// Start, or read only).
func (f *Family) AcceptLicence(ctx context.Context, repo, licence string) error {
	f.mu.Lock()
	st := f.state
	f.mu.Unlock()
	if st != ready && st != failed {
		return nil
	}
	return models.AcceptLicence(ctx, f.Dir, repo, LicenceID(licence), f.BundleID)
}

// LicenceAcceptedIn names the other app the UI may name (an installed
// release build, spec §7.10) in which the user accepted repo's licence,
// and when; "" when none (not accepted, accepted in AudioSmith itself,
// or in an app that cannot be named).
func (f *Family) LicenceAcceptedIn(repo, licence string) (string, time.Time) {
	acc, ok := models.LicenceAccepted(f.Dir, repo, LicenceID(licence))
	if !ok || acc.By == f.BundleID {
		return "", time.Time{}
	}
	if name, ok := visibleApps(f.Found())[acc.By]; ok {
		return name, acc.At
	}
	return "", time.Time{}
}
