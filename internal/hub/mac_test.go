package hub

import (
	"bufio"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hehehai/AudioSmith/internal/catalog"
	"github.com/hehehai/actnowkit/conformance"
	"github.com/hehehai/actnowkit/models"
)

// TestMacKeychainAndProxy checks the parts of the hub only a Mac has: the
// token in the login Keychain (saved, read, removed through
// /usr/bin/security) and the system proxy read from scutil. It runs with
// AUDIOSMITH_MAC=1 on macOS:
//
//	AUDIOSMITH_MAC=1 go test -run TestMacKeychainAndProxy -v ./internal/hub
func TestMacKeychainAndProxy(t *testing.T) {
	if os.Getenv("AUDIOSMITH_MAC") == "" || runtime.GOOS != "darwin" {
		t.Skip("set AUDIOSMITH_MAC=1 on a Mac")
	}
	// The login keychain is locked in an SSH session ("User interaction is
	// not allowed"), so the commands run against a keychain of their own;
	// the app uses the login keychain, unlocked in the user's session.
	k := Keychain{Service: "AudioSmith (test)", Account: "huggingface-token"}
	if _, err := security("", "show-keychain-info"); err != nil && strings.Contains(err.Error(), "User interaction is not allowed") {
		file := filepath.Join(t.TempDir(), "test.keychain-db")
		if out, err := exec.Command("/usr/bin/security", "create-keychain", "-p", "test", file).CombinedOutput(); err != nil {
			t.Fatalf("create-keychain: %v %s", err, out)
		}
		defer exec.Command("/usr/bin/security", "delete-keychain", file).Run()
		if out, err := exec.Command("/usr/bin/security", "unlock-keychain", "-p", "test", file).CombinedOutput(); err != nil {
			t.Fatalf("unlock-keychain: %v %s", err, out)
		}
		k.File = file
		t.Log("login keychain locked (SSH session): testing against a temporary keychain")
	}
	k.Delete()
	const tok = "hf_probe_0123456789abcdef"
	if err := k.Set(tok); err != nil {
		t.Fatalf("save: %v", err)
	}
	got, err := k.Get()
	if err != nil || got != tok {
		t.Fatalf("read back %q, %v", MaskToken(got), err)
	}
	if err := k.Set(tok + "x"); err != nil { // replacing an item
		t.Fatalf("replace: %v", err)
	}
	if got, _ := k.Get(); got != tok+"x" {
		t.Errorf("after replacing: %q", MaskToken(got))
	}
	if err := k.Set(""); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if got, err := k.Get(); got != "" {
		t.Errorf("after removing: %q %v", MaskToken(got), err)
	}
	t.Logf("Keychain: saved, read, replaced and removed %s", MaskToken(tok))

	for _, v := range []string{"HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy", "ALL_PROXY", "all_proxy"} {
		t.Setenv(v, "")
		os.Unsetenv(v)
	}
	sp := currentSystemProxy()
	auto := DescribeProxy("")
	t.Logf("system proxy (scutil): https %v, http %v, exceptions %v; downloads use %q", sp.https, sp.http, sp.exceptions, auto)
	if want := os.Getenv("AUDIOSMITH_MAC_PROXY"); want != "" && auto != want {
		t.Errorf("automatic proxy %q, want %q", auto, want)
	}
	if DescribeProxy(ProxyDirect) != "" {
		t.Error("direct mode uses a proxy")
	}
}

// TestMacFamily checks the family phase 0 on a Mac with Dicta installed,
// in a temporary model folder and family directory (conformance.New):
// LaunchServices finds Dicta, Clean up and Delete keep its models; with
// AUDIOSMITH_ACTNOW_CLI (a built actnow-conformance, actnowkit's own
// implementation playing Dicta) a delete is refused while it holds the
// repo, and a real download (Silero VAD, 2 MB) waits for it.
//
//	AUDIOSMITH_MAC=1 AUDIOSMITH_ACTNOW_CLI=/path/actnow-conformance go test -run TestMacFamily -v ./internal/hub
func TestMacFamily(t *testing.T) {
	if os.Getenv("AUDIOSMITH_MAC") == "" || runtime.GOOS != "darwin" {
		t.Skip("set AUDIOSMITH_MAC=1 on a Mac")
	}
	fx := conformance.New(t)
	var fam Family // LaunchServices
	if users := fam.UsersOf(gemma); !slices.Contains(users, "Dicta") {
		t.Fatalf("LaunchServices: Dicta not found (users of %s: %v)", gemma, users)
	}
	fakeRepo(t, fx.Hub, gemma, 500)
	fakeRepo(t, fx.Hub, parakeet, 300)
	freed, kept, err := RemoveLeftovers(fx.Hub, []string{gemma, parakeet}, fam)
	if err != nil || freed != 300 || len(kept) != 1 || kept[0].Repo != gemma {
		t.Fatalf("clean up: freed %d, kept %+v, %v", freed, kept, err)
	}
	asr := catalog.Entry{Repo: "mlx-community/Qwen3-ASR-0.6B-4bit", Revision: "0123456789abcdef0123456789abcdef01234567"}
	fakeRepo(t, fx.Hub, asr.Repo, 100)
	if r, err := Remove(fx.Hub, asr, fam); err != nil || !slices.Contains(r.Kept, "Dicta") {
		t.Fatalf("delete of a model Dicta uses: %+v %v", r, err)
	}
	t.Logf("Dicta found; Clean up kept %s, Delete kept %s", gemma, asr.Repo)

	cli := os.Getenv("AUDIOSMITH_ACTNOW_CLI")
	if cli == "" {
		t.Log("AUDIOSMITH_ACTNOW_CLI unset: no lock checks against actnowkit's CLI")
		return
	}
	// hold has the CLI hold repo's lock in mode for d, as Dicta.
	hold := func(repo, mode string, d time.Duration) *exec.Cmd {
		cmd := exec.Command(cli, "lock", "-repo", repo, "-mode", mode, "-as", "dev.hehehai.dicta", "-for", d.String(), "-hub", fx.Hub)
		out, _ := cmd.StdoutPipe()
		cmd.Stderr = os.Stderr
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		line, _ := bufio.NewReader(out).ReadString('\n')
		if !strings.HasPrefix(line, "holding") {
			t.Fatalf("actnow-conformance lock: %q", line)
		}
		return cmd
	}
	fakeRepo(t, fx.Hub, parakeet, 300)
	e := catalog.Entry{Repo: parakeet, Revision: "0123456789abcdef0123456789abcdef01234567"}
	cmd := hold(parakeet, "shared", 3*time.Second)
	if _, err := Remove(fx.Hub, e, noFamily); err == nil {
		t.Fatal("deleted while Dicta held the repo")
	} else if name, busy := Holder(err); !busy || name != "Dicta" {
		t.Fatalf("delete while held: %v", err)
	}
	cmd.Wait()
	if r, err := Remove(fx.Hub, e, noFamily); err != nil || r.Freed != 300 {
		t.Fatalf("delete after: %+v %v", r, err)
	}

	vad, ok := catalog.Builtin().ByRepo("mlx-community/silero-vad")
	if !ok {
		t.Fatal("no silero-vad in the catalogue")
	}
	c := New(Options{CacheDir: fx.Hub, Family: noFamily})
	cmd = hold(vad.Repo, "exclusive", 3*time.Second)
	start := time.Now()
	var once sync.Once
	var first time.Duration // the first progress: after the repo lock
	err = c.Download(context.Background(), vad, func(p Progress) {
		once.Do(func() { first = time.Since(start) })
	})
	cmd.Wait()
	if err != nil {
		t.Fatal(err)
	}
	if first < 2500*time.Millisecond {
		t.Errorf("the download went on while Dicta held the repo exclusively (first progress after %s)", first)
	}
	if l, _ := c.Status(context.Background(), vad); l.State != Downloaded {
		t.Errorf("silero-vad: %+v", l)
	}
	if _, err := os.Stat(models.RepoLockFile(fx.Hub, vad.Repo) + ".owner"); !os.IsNotExist(err) {
		t.Errorf("owner file left: %v", err)
	}
	t.Logf("download waited %s for the CLI's exclusive lock, done in %s", first.Round(time.Millisecond), time.Since(start).Round(time.Millisecond))
}
