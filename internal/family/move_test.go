package family

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/hehehai/actnowkit/conformance"
	actnow "github.com/hehehai/actnowkit/family"
	"github.com/hehehai/actnowkit/models"
)

// installIn puts a test repo in hub, as a download would.
func installIn(fx *conformance.Fixture, hub string, ref models.Ref, weights int) conformance.Repo {
	r := conformance.TestRepo(ref.Repo, ref.Revision, weights)
	x := *fx
	x.Hub = hub
	x.Install(r)
	return r
}

func installedIn(hub string, ref models.Ref) bool {
	return models.Check(hub, models.Claim{Repo: ref.Repo, Revision: ref.Revision}).State == models.Installed
}

func claimsOf(refs ...models.Ref) []models.Claim {
	var out []models.Claim
	for _, r := range refs {
		out = append(out, models.Claim{Repo: r.Repo, Revision: r.Revision})
	}
	return out
}

// acrossVolumes makes renames and links fail as between two volumes.
func acrossVolumes(t *testing.T) {
	r, l := renameDir, linkFile
	renameDir = func(a, b string) error { return &os.LinkError{Op: "rename", Old: a, New: b, Err: syscall.EXDEV} }
	linkFile = func(a, b string) error { return &os.LinkError{Op: "link", Old: a, New: b, Err: syscall.EXDEV} }
	t.Cleanup(func() { renameDir, linkFile = r, l })
}

func moveFixture(t *testing.T) (*testFamily, *conformance.Fixture, string) {
	t.Helper()
	f, fx := started(t)
	return f, fx, filepath.Join(t.TempDir(), "Shared", "hub")
}

// Same volume: a repo the target lacks is renamed (its engine copy
// goes); one the target has another commit of is linked file by file.
func TestMoveSameVolume(t *testing.T) {
	f, fx, to := moveFixture(t)
	other := models.Ref{Repo: refB.Repo, Revision: revA}
	installIn(fx, to, other, 32) // asr-b at another commit, in the target
	copyDir := models.EngineCopy(fx.Hub, refA.Repo)
	if err := os.MkdirAll(copyDir, 0o755); err != nil {
		t.Fatal(err)
	}
	var last MoveProgress
	res, err := f.MoveModels(context.Background(), fx.Hub, to, claimsOf(refA, refG, refB), func(p MoveProgress) { last = p })
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Moved) != 3 || len(res.Skipped) != 0 || len(res.Reused) != 0 || len(res.Kept) != 0 {
		t.Fatalf("result %+v", res)
	}
	for _, r := range []models.Ref{refA, refG, refB, other} {
		if !installedIn(to, r) {
			t.Errorf("%s not in the target", r)
		}
	}
	for _, r := range []models.Ref{refA, refG, refB} {
		if _, err := os.Stat(models.RepoDir(fx.Hub, r.Repo)); !os.IsNotExist(err) {
			t.Errorf("%s still in the source: %v", r, err)
		}
	}
	if _, err := os.Stat(copyDir); !os.IsNotExist(err) {
		t.Errorf("the source's engine copy stayed: %v", err)
	}
	if last.Done != 3 || last.Total != 3 || last.Bytes != last.TotalBytes || last.TotalBytes == 0 {
		t.Errorf("progress %+v", last)
	}
	if data, _ := os.ReadFile(filepath.Join(models.RepoDir(to, refB.Repo), "refs", "main")); string(data) != revA {
		t.Errorf("the target's refs/main became %q", data)
	}
}

// Across volumes: copied, checked, the source deleted; a stale engine
// copy in the target goes.
func TestMoveAcrossVolumes(t *testing.T) {
	acrossVolumes(t)
	f, fx, to := moveFixture(t)
	stale := models.EngineCopy(to, refA.Repo)
	if err := os.MkdirAll(stale, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/nowhere/model.safetensors", filepath.Join(stale, "model.safetensors")); err != nil {
		t.Fatal(err)
	}
	want, _ := os.ReadFile(filepath.Join(models.SnapshotDir(fx.Hub, refA), "model.safetensors"))
	res, err := f.MoveModels(context.Background(), fx.Hub, to, claimsOf(refA, refG), nil)
	if err != nil || len(res.Moved) != 2 {
		t.Fatalf("%+v %v", res, err)
	}
	got, err := os.ReadFile(filepath.Join(models.SnapshotDir(to, refA), "model.safetensors"))
	if err != nil || string(got) != string(want) {
		t.Errorf("weights differ after the copy (%v)", err)
	}
	if l, err := os.Readlink(filepath.Join(models.SnapshotDir(to, refA), "tokenizer", "vocab.json")); err != nil || filepath.IsAbs(l) {
		t.Errorf("link %q %v: want a relative link to the blob", l, err)
	}
	if installedIn(fx.Hub, refA) || !installedIn(to, refA) || !installedIn(fx.Hub, refB) {
		t.Error("source / target state")
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Errorf("stale engine copy kept: %v", err)
	}
	if left, _ := filepath.Glob(filepath.Join(models.RepoDir(to, refA.Repo), "*", "*.incomplete")); len(left) > 0 {
		t.Errorf("partial files left: %v", left)
	}
}

// The target has the snapshot (another app downloaded it): nothing is
// copied, AudioSmith's copy goes. Another app's claim on the source keeps
// it there.
func TestMoveTargetHasItAndKept(t *testing.T) {
	acrossVolumes(t)
	f, fx, to := moveFixture(t)
	installIn(fx, to, refA, 64)
	blob := filepath.Join(models.SnapshotDir(to, refA), "model.safetensors")
	before, _ := os.Stat(blob)
	claim(t, fx, "dev.hehehai.dicta", refB) // Dicta uses asr-b in the source folder
	res, err := f.MoveModels(context.Background(), fx.Hub, to, claimsOf(refA, refB), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Reused) != 1 || res.Reused[0] != refA || len(res.Kept) != 1 || res.Kept[0] != refB {
		t.Errorf("result %+v", res)
	}
	if after, _ := os.Stat(blob); !after.ModTime().Equal(before.ModTime()) {
		t.Error("the target's copy was rewritten")
	}
	if installedIn(fx.Hub, refA) {
		t.Error("AudioSmith's copy of tts-a stayed")
	}
	if !installedIn(fx.Hub, refB) || !installedIn(to, refB) {
		t.Error("asr-b: kept in the source for Dicta, and in the target")
	}
}

// Cancelled midway: what moved stays in the target, the rest in the
// source, nothing half-made.
func TestMoveCancel(t *testing.T) {
	acrossVolumes(t)
	f, fx, to := moveFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	res, err := f.MoveModels(ctx, fx.Hub, to, claimsOf(refA, refB), func(p MoveProgress) {
		if p.Done == 1 {
			cancel()
		}
	})
	if !errors.Is(err, context.Canceled) || len(res.Moved) != 1 {
		t.Fatalf("%+v %v", res, err)
	}
	if !installedIn(to, refA) || installedIn(fx.Hub, refA) || !installedIn(fx.Hub, refB) {
		t.Error("state after cancel")
	}
	if _, err := os.Stat(models.RepoDir(to, refB.Repo)); err == nil {
		if left, _ := filepath.Glob(filepath.Join(models.RepoDir(to, refB.Repo), "snapshots", "*")); len(left) > 0 {
			t.Errorf("half a snapshot in the target: %v", left)
		}
	}
	// Cancelled within a file: the partial copy goes.
	big := models.Ref{Repo: "acme/big", Revision: revG}
	installIn(fx, fx.Hub, big, 24<<20)
	ctx2, cancel2 := context.WithCancel(context.Background())
	defer cancel2()
	_, err = f.MoveModels(ctx2, fx.Hub, to, claimsOf(big), func(p MoveProgress) {
		if p.Bytes > 0 {
			cancel2()
		}
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel within a file: %v", err)
	}
	if !installedIn(fx.Hub, big) || installedIn(to, big) {
		t.Error("big: state after cancel")
	}
	if left, _ := filepath.Glob(filepath.Join(models.RepoDir(to, big.Repo), "blobs", "*")); len(left) > 0 {
		t.Errorf("partial blobs left: %v", left)
	}
}

// A repo whose lock another app holds: the move waits, and skips it
// (naming the app) when it is held longer than MoveLockWait.
func TestMoveLockHeld(t *testing.T) {
	f, fx, to := moveFixture(t)
	fx.Register("dev.hehehai.dicta")
	f.Rediscover()
	wait := MoveLockWait
	t.Cleanup(func() { MoveLockWait = wait })
	MoveLockWait = 100 * time.Millisecond
	held, err := models.TryLockRepo(fx.Hub, refB.Repo, actnow.Shared, models.Owner{BundleID: "dev.hehehai.dicta", Op: models.OpDownload})
	if err != nil {
		t.Fatal(err)
	}
	var waited string
	res, err := f.MoveModels(context.Background(), fx.Hub, to, claimsOf(refA, refB), func(p MoveProgress) {
		if p.Waiting {
			waited = p.WaitingFor
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Skipped) != 1 || res.Skipped[0].Ref != refB || res.Skipped[0].By != "Dicta" || !errors.Is(res.Skipped[0].Err, ErrRepoBusy) || waited != "Dicta" {
		t.Errorf("result %+v, waited for %q", res, waited)
	}
	if !installedIn(fx.Hub, refB) || !installedIn(to, refA) {
		t.Error("state")
	}
	// Held for a moment only: it waits, then moves.
	MoveLockWait = 10 * time.Second
	go func() {
		time.Sleep(200 * time.Millisecond)
		held.Release()
	}()
	res, err = f.MoveModels(context.Background(), fx.Hub, to, claimsOf(refB), nil)
	if err != nil || len(res.Moved) != 1 {
		t.Fatalf("%+v %v", res, err)
	}
	if !installedIn(to, refB) {
		t.Error("asr-b not moved after the lock was released")
	}
}

func TestMoveRefusesSameOrInside(t *testing.T) {
	f, fx, _ := moveFixture(t)
	for _, to := range []string{fx.Hub, fx.Hub + "/", filepath.Join(fx.Hub, "inner")} {
		if _, err := f.MoveModels(context.Background(), fx.Hub, to, claimsOf(refA), nil); err == nil {
			t.Errorf("%s: moved", to)
		}
	}
}
