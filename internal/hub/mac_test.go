package hub

import (
	"os"
	"runtime"
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
	k := Keychain{Service: "AudioSmith (test)", Account: "huggingface-token"}
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
