package family

import (
	"bufio"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/hehehai/AudioSmith/internal/catalog"
	"github.com/hehehai/AudioSmith/internal/hub"
	"github.com/hehehai/actnowkit/conformance"
	actnow "github.com/hehehai/actnowkit/family"
	"github.com/hehehai/actnowkit/models"
	"github.com/hehehai/actnowkit/registry"
)

// TestMacScenarios plays the deletion and download scenarios of the
// family spec §16.1 on a Mac, with the real LaunchServices, in the family
// directory and model folder of the environment (throwaway ones, as
// TestMacFamily), with small stand-in repos:
//
//   - 2, deletion protection: Dicta (installed, from LaunchServices) uses
//     Qwen3-ASR; removing it from AudioSmith keeps the files, first by the
//     pins table (Dicta has no manifest there), then by Dicta's claim; once
//     Dicta deletes it too, the files go.
//
//   - 3, one download: a file Dicta is downloading (actnow-conformance
//     holds its blob lock as Dicta, AUDIOSMITH_ACTNOW_CLI) is waited for.
//
//   - 4, a crash: the holder is killed (kill -9) mid-way; AudioSmith takes
//     the blob within seconds and resumes its partial file.
//
//   - 8, an uninstalled app: Pik's claims become orphans once its .app is
//     gone; cleaning them up frees the files and removes its manifest.
//
//     AUDIOSMITH_MAC=1 ACTNOW_FAMILY_DIR=/tmp/c72/ActNow HF_HUB_CACHE=/tmp/c72/hub \
//     AUDIOSMITH_ACTNOW_CLI=/tmp/c72/actnow-conformance go test -run TestMacScenarios -v ./internal/family
func TestMacScenarios(t *testing.T) {
	if os.Getenv("AUDIOSMITH_MAC") == "" || runtime.GOOS != "darwin" {
		t.Skip("set AUDIOSMITH_MAC=1 on a Mac")
	}
	dir, hubDir := os.Getenv(actnow.EnvDir), os.Getenv("HF_HUB_CACHE")
	home, _ := os.UserHomeDir()
	if dir == "" || hubDir == "" {
		t.Skip("set ACTNOW_FAMILY_DIR and HF_HUB_CACHE to throwaway directories")
	}
	if models.SameHub(dir, filepath.Join(home, "Library", "Application Support", "ActNow")) || models.SameHub(hubDir, filepath.Join(home, ".cache", "huggingface", "hub")) {
		t.Fatal("ACTNOW_FAMILY_DIR and HF_HUB_CACHE must be throwaway directories")
	}
	fx := &conformance.Fixture{T: t, Family: actnow.At(dir), Hub: hubDir}
	ctx := context.Background()
	asr, _ := catalog.Builtin().ByID("qwen3-asr-0.6b-4bit")
	asrRef := models.Ref{Repo: asr.Repo, Revision: asr.Revision}
	fx.Install(conformance.TestRepo(asr.Repo, asr.Revision, 4096))
	f := New(Options{Dir: fx.Family, Logf: t.Logf})
	defer f.Close()
	in := Inputs{Version: "0.4.0-beta.6", Hub: hubDir}
	if err := f.Start(ctx, in, func(h string) []models.Claim { return Wanted(h, []catalog.Entry{asr}, nil) }); err != nil {
		t.Fatal(err)
	}
	fam := hub.Family{BundleID: f.BundleID, Decider: f.Decider(hubDir)}

	// Scenario 2.
	var dictaPath string
	for _, a := range f.Found() {
		if a.BundleID == dictaID && a.Installed {
			dictaPath = a.Path
		}
	}
	if dictaPath == "" {
		t.Log("scenario 2: Dicta is not installed on this Mac: skipped")
	} else {
		r, err := hub.Remove(ctx, hubDir, asr, fam, models.ThisApp)
		if err != nil || r.Deleted || !fx.Installed(asrRef) || fx.Claimed(f.BundleID, asrRef) {
			t.Fatalf("scenario 2, Dicta without a manifest: %+v %v", r, err)
		}
		t.Logf("scenario 2: Dicta at %s, no manifest: kept for %v (pins table)", dictaPath, r.Kept)
		m := registry.Manifest{App: "dicta", BundleID: dictaID, Name: "Dicta", Version: "test", Path: dictaPath,
			Capabilities: []string{registry.CapModelsHF}, Models: &registry.ModelsInfo{Root: hubDir}}
		if err := registry.Write(fx.Family, m); err != nil {
			t.Fatal(err)
		}
		dicta := &models.Store{Family: fx.Family, Hub: hubDir, BundleID: dictaID, AppID: "dicta"}
		if err := dicta.Claim(ctx, models.Claim{Repo: asr.Repo, Revision: asr.Revision}); err != nil {
			t.Fatal(err)
		}
		f.Claimed(ctx, hubDir, ClaimOf(asr, asr.Repo, asr.Revision, nil, false, models.OriginReused))
		f.Rediscover()
		r, err = hub.Remove(ctx, hubDir, asr, fam, models.ThisApp)
		if err != nil || r.Deleted || !fx.Installed(asrRef) || len(r.Decision.Users) != 1 || r.Decision.Users[0].Claim.Repo == "" {
			t.Fatalf("scenario 2, Dicta's claim: %+v %v", r, err)
		}
		res, err := dicta.Delete(ctx, asrRef, models.ThisApp)
		if err != nil || !res.Deleted || fx.Installed(asrRef) {
			t.Fatalf("scenario 2, Dicta deletes it too: %+v %v", res, err)
		}
		t.Log("scenario 2: kept by Dicta's claim; gone once Dicta deleted it too")
	}

	// Scenarios 3 and 4.
	cli := os.Getenv("AUDIOSMITH_ACTNOW_CLI")
	if cli == "" {
		t.Log("scenarios 3, 4: AUDIOSMITH_ACTNOW_CLI unset: skipped")
	} else {
		repo := conformance.TestRepo("acme/scenario-model", strings.Repeat("c", 40), 8<<20)
		srv := conformance.NewFakeHub(repo)
		defer srv.Close()
		weights := repo.Files[2]
		blob := weights.BlobID()
		hold := func(d time.Duration) *exec.Cmd {
			cmd := exec.Command(cli, "lock", "-repo", repo.Ref.Repo, "-blob", blob, "-as", dictaID, "-for", d.String(), "-hub", hubDir, "-family", dir)
			out, _ := cmd.StdoutPipe()
			cmd.Stderr = os.Stderr
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			if line, _ := bufio.NewReader(out).ReadString('\n'); !strings.HasPrefix(line, "holding") {
				t.Fatalf("actnow-conformance lock: %q", line)
			}
			return cmd
		}
		e := catalog.Entry{ID: "scenario", Repo: repo.Ref.Repo, Revision: repo.Ref.Revision}
		client := hub.New(hub.Options{CacheDir: hubDir, Endpoints: []string{srv.URL}, Official: srv.URL, ReserveBytes: -1, Family: fam,
			Claimed: func(ctx context.Context, d string, x hub.Finished) error {
				return f.Claimed(ctx, d, ClaimOf(x.Entry, x.Repo, x.Commit, x.Files, x.Aux, ""))
			}})
		// 4: "Dicta" wrote half the file, then dies holding its lock.
		blobs := filepath.Join(models.RepoDir(hubDir, repo.Ref.Repo), "blobs")
		os.MkdirAll(blobs, 0o755)
		half := int64(len(weights.Content) / 2)
		os.WriteFile(filepath.Join(blobs, blob+".incomplete"), weights.Content[:half], 0o644)
		cmd := hold(5 * time.Minute)
		var waited string
		done := make(chan error, 1)
		start := time.Now()
		go func() {
			done <- client.Download(ctx, e, func(p hub.Progress) {
				if p.Waiting && waited == "" {
					waited = p.WaitingFor
				}
			})
		}()
		time.Sleep(2 * time.Second)
		if err := cmd.Process.Signal(syscall.SIGKILL); err != nil {
			t.Fatal(err)
		}
		killed := time.Now()
		cmd.Wait()
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		took := time.Since(killed)
		if !fx.Installed(repo.Ref) || !fx.Claimed(f.BundleID, repo.Ref) {
			t.Fatal("scenario 4: not installed and claimed")
		}
		served := srv.ServedOf(repo.Ref.Repo, "model.safetensors")
		if waited != "Dicta" || took > 30*time.Second || served > int64(len(weights.Content))-half+64<<10 {
			t.Errorf("scenario 4: waited for %q, done %s after the kill, fetched %d of %d bytes", waited, took, served, len(weights.Content))
		}
		t.Logf("scenario 3/4: waited for %s %s, then done %s after kill -9, fetching %d of %d bytes (the rest resumed)",
			waited, killed.Sub(start).Round(time.Millisecond), took.Round(time.Millisecond), served, len(weights.Content))
	}

	// Scenario 8.
	pikApp, err := conformance.FakeApp(t.TempDir(), "Pik", "dev.hehehai.pik")
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.Write(fx.Family, registry.Manifest{App: "pik", BundleID: "dev.hehehai.pik", Name: "Pik", Version: "test", Path: pikApp,
		Capabilities: []string{registry.CapModelsHF}, Models: &registry.ModelsInfo{Root: hubDir}}); err != nil {
		t.Fatal(err)
	}
	only := conformance.TestRepo("acme/only-pik", strings.Repeat("d", 40), 1<<20)
	fx.Install(only)
	pik := &models.Store{Family: fx.Family, Hub: hubDir, BundleID: "dev.hehehai.pik", AppID: "pik"}
	if err := pik.Claim(ctx, models.Claim{Repo: only.Ref.Repo, Revision: only.Ref.Revision}); err != nil {
		t.Fatal(err)
	}
	os.RemoveAll(pikApp) // dragged to the Trash
	f.Rediscover()
	orphans, err := f.Orphans(hubDir)
	if err != nil || len(orphans) != 1 || orphans[0].Bytes == 0 {
		t.Fatalf("scenario 8: orphans %+v %v", orphans, err)
	}
	freed, err := f.CleanOrphans(ctx, hubDir)
	if err != nil || fx.Installed(only.Ref) {
		t.Fatalf("scenario 8: freed %d, %v", freed, err)
	}
	if _, err := os.Stat(fx.Family.AppFile("dev.hehehai.pik")); !os.IsNotExist(err) {
		t.Errorf("scenario 8: Pik's manifest stayed: %v", err)
	}
	t.Logf("scenario 8: %d model(s) only Pik used, %d bytes, cleaned up", len(orphans[0].Claims), freed)
}
