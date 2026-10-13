package hub

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
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
