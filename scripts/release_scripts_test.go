// Package scripts holds no code, only tests of the release scripts
// (thread #69): release-config.sh, actnow.sh, actnow-register.sh and
// release-publish.sh, run with bash against fakes (an ingest server, gh
// and mygo), so they never touch R2, the website or GitHub.
package scripts

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
)

// needTools skips the test without bash, jq or curl.
func needTools(t *testing.T) {
	t.Helper()
	for _, tool := range []string{"bash", "jq", "curl"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("needs %s", tool)
		}
	}
}

// fakeRoot makes a checkout of its own: the scripts named, copied from
// this directory, and files (path → contents).
func fakeRoot(t *testing.T, scripts []string, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	os.Mkdir(filepath.Join(root, "scripts"), 0o755)
	for _, s := range scripts {
		data, err := os.ReadFile(s)
		if err != nil {
			t.Fatal(err)
		}
		os.WriteFile(filepath.Join(root, "scripts", s), data, 0o755)
	}
	for path, content := range files {
		p := filepath.Join(root, path)
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(content), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// run runs a command with env added to a clean environment (PATH and HOME
// only, so that a secret of the caller never leaks in), and returns its
// stdout, stderr and error.
func run(t *testing.T, dir string, env []string, name string, args ...string) (string, string, error) {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	cmd.Env = append([]string{"PATH=" + os.Getenv("PATH"), "HOME=" + t.TempDir()}, env...)
	var stdout, stderr strings.Builder
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	return stdout.String(), stderr.String(), err
}

func publicKey() string {
	b := make([]byte, 32)
	rand.Read(b)
	return base64.StdEncoding.EncodeToString(b)
}

const testConfig = `{"name": "AudioSmith", "version": "0.4.0-beta.5", "macos": {"minimumSystemVersion": "15.0"}}`

func TestReleaseConfig(t *testing.T) {
	needTools(t)
	root := fakeRoot(t, []string{"release-config.sh", "actnow.sh"}, map[string]string{"mygo.json": testConfig})
	key := publicKey()
	config := func(env ...string) (map[string]any, error) {
		t.Helper()
		out, stderr, err := run(t, root, env, "bash", "scripts/release-config.sh", "--print")
		if err != nil {
			return nil, &exec.ExitError{Stderr: []byte(stderr)}
		}
		var c map[string]any
		if err := json.Unmarshal([]byte(out), &c); err != nil {
			t.Fatalf("%v: %s", err, out)
		}
		return c, nil
	}
	updates := func(c map[string]any) string {
		b, _ := json.Marshal(c["updates"])
		return string(b)
	}

	// The website: the feed and the bucket the site serves.
	website := `{"publicKey":"` + key + `","s3":{"bucket":"actnow-releases","endpoint":"https://8a8325619b834f43256fd77f030b11a2.r2.cloudflarestorage.com","prefix":"audiosmith","region":"auto"},"url":"https://actnow.dev/updates/audiosmith"}`
	c, err := config("MYGO_UPDATER_PUBLIC_KEY=" + key)
	if err != nil || updates(c) != website {
		t.Errorf("website: %s, %v\nwant %s", updates(c), err, website)
	}
	// The GitHub transition toggle leaves the app on the website.
	c, err = config("MYGO_UPDATER_PUBLIC_KEY="+key, "GITHUB_FEED=true")
	if err != nil || updates(c) != website {
		t.Errorf("website with GITHUB_FEED=true: %s, %v", updates(c), err)
	}
	// The old GitHub feed, for forks and tests.
	c, err = config("MYGO_UPDATER_PUBLIC_KEY="+key, "UPDATES_FEED=github")
	if want := `{"github":"hehehai/AudioSmith","publicKey":"` + key + `"}`; err != nil || updates(c) != want {
		t.Errorf("github: %s, %v", updates(c), err)
	}
	// Without a key, no updates; signing and notarization as before.
	c, err = config("MACOS_SIGNING_IDENTITY=Developer ID Application: X (T)", "NOTARY_PROFILE=notary", "NOTARY_KEYCHAIN=/tmp/k")
	macos, _ := json.Marshal(c["macos"])
	if err != nil || c["updates"] != nil || string(macos) != `{"minimumSystemVersion":"15.0","notarize":{"keychain":"/tmp/k","keychainProfile":"notary"},"signingIdentity":"Developer ID Application: X (T)"}` {
		t.Errorf("no key: %v, %s, %v", c["updates"], macos, err)
	}
	for _, env := range [][]string{{"MYGO_UPDATER_PUBLIC_KEY=bm90IGEga2V5"}, {"MYGO_UPDATER_PUBLIC_KEY=" + key, "UPDATES_FEED=s3"}} {
		if _, err := config(env...); err == nil {
			t.Errorf("%v: no error", env)
		}
	}
	// --print changes nothing.
	if b, _ := os.ReadFile(filepath.Join(root, "mygo.json")); string(b) != testConfig {
		t.Errorf("mygo.json changed: %s", b)
	}
}

const testChangelog = `# Changelog

## 0.4.0-beta.10

- Ten.

## 0.4.0-beta.1 — 2026-10-16


### Updates
- One, with **Markdown** and "quotes" and a \backslash.

- Two.

## [0.3.0](https://example.com) - 2026-10-14
- Three.
## v0.2.0
- Two point oh.
`

// shell runs bash lines with scripts/actnow.sh sourced.
func shell(t *testing.T, root, lines string) string {
	t.Helper()
	out, stderr, err := run(t, root, nil, "bash", "-c", "set -euo pipefail; source scripts/actnow.sh; "+lines)
	if err != nil {
		t.Fatalf("%s: %v\n%s", lines, err, stderr)
	}
	return out
}

func TestActnowHelpers(t *testing.T) {
	needTools(t)
	root := fakeRoot(t, []string{"actnow.sh"}, map[string]string{"CHANGELOG.md": testChangelog})
	for version, want := range map[string]string{
		"0.4.0-beta.1":  "### Updates\n- One, with **Markdown** and \"quotes\" and a \\backslash.\n\n- Two.\n",
		"0.4.0-beta.10": "- Ten.\n",
		"0.3.0":         "- Three.\n",
		"0.2.0":         "- Two point oh.\n",
		"0.4.0":         "",
		"0.4.0-beta":    "",
	} {
		if got := shell(t, root, "changelog_section CHANGELOG.md "+version); got != want {
			t.Errorf("changelog_section %s = %q, want %q", version, got, want)
		}
	}
	for version, want := range map[string]string{
		"0.4.0-beta.5":       "audiosmith/beta/AudioSmith-0.4.0-beta.5.dmg beta",
		"0.4.0":              "audiosmith/AudioSmith-0.4.0.dmg stable",
		"1.0.0+build.5":      "audiosmith/AudioSmith-1.0.0+build.5.dmg stable",
		"1.0.0-rc.1+build.5": "audiosmith/beta/AudioSmith-1.0.0-rc.1+build.5.dmg beta",
	} {
		if got := shell(t, root, `echo "$(actnow_dmg_key `+version+`) $(actnow_channel `+version+`)"`); got != want+"\n" {
			t.Errorf("%s: %q, want %q", version, got, want)
		}
	}
	os.WriteFile(filepath.Join(root, "file"), []byte("hello"), 0o644)
	if got := shell(t, root, `echo "$(file_size file) $(file_sha256 file)"`); got != "5 2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824\n" {
		t.Errorf("size and sha256: %q", got)
	}
}

// ingest is a fake of actnow.dev's POST /api/releases: it answers status
// and keeps what it got.
type ingest struct {
	srv    *httptest.Server
	mu     sync.Mutex
	status int
	body   string // answer
	got    []ingestRequest
}

type ingestRequest struct {
	auth, typ string
	payload   map[string]any
}

func newIngest(t *testing.T) *ingest {
	in := &ingest{status: http.StatusCreated, body: `{"ok":true}`}
	in.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, _ := io.ReadAll(r.Body)
		var p map[string]any
		if err := json.Unmarshal(data, &p); err != nil {
			t.Errorf("not JSON: %s", data)
		}
		in.mu.Lock()
		defer in.mu.Unlock()
		if r.Method != http.MethodPost || r.URL.Path != "/api/releases" {
			t.Errorf("%s %s", r.Method, r.URL)
		}
		in.got = append(in.got, ingestRequest{r.Header.Get("Authorization"), r.Header.Get("Content-Type"), p})
		w.WriteHeader(in.status)
		io.WriteString(w, in.body)
	}))
	t.Cleanup(in.srv.Close)
	return in
}

func (in *ingest) requests() []ingestRequest {
	in.mu.Lock()
	defer in.mu.Unlock()
	return slices.Clone(in.got)
}

func (in *ingest) answer(status int, body string) {
	in.mu.Lock()
	in.status, in.body, in.got = status, body, nil
	in.mu.Unlock()
}

const testToken = "test-ingest-token-5f2a"

func TestActnowRegister(t *testing.T) {
	needTools(t)
	root := fakeRoot(t, []string{"actnow.sh", "actnow-register.sh"}, map[string]string{
		"mygo.json":    strings.Replace(testConfig, "0.4.0-beta.5", "0.4.0-beta.1", 1),
		"CHANGELOG.md": testChangelog,
		"build/a.dmg":  "a disk image",
	})
	in := newIngest(t)
	env := []string{"ACTNOW_API=" + in.srv.URL + "/api/releases", "ACTNOW_INGEST_TOKEN=" + testToken}
	register := func(env []string, args ...string) (string, string, error) {
		return run(t, root, env, "bash", append([]string{"scripts/actnow-register.sh", "--dmg", "build/a.dmg"}, args...)...)
	}
	sum := sha256.Sum256([]byte("a disk image"))
	want := map[string]any{
		"app": "audiosmith", "version": "0.4.0-beta.1", "channel": "beta", "date": "2026-10-16",
		"notes":  "### Updates\n- One, with **Markdown** and \"quotes\" and a \\backslash.\n\n- Two.",
		"dmgKey": "audiosmith/beta/AudioSmith-0.4.0-beta.1.dmg", "dmgSize": float64(12), "sha256": hex.EncodeToString(sum[:]),
		"minMacOS": "15.0", "arch": "arm64", "published": true,
	}
	check := func(name string, got []ingestRequest, want map[string]any) {
		t.Helper()
		if len(got) != 1 {
			t.Fatalf("%s: %d requests", name, len(got))
		}
		if got[0].auth != "Bearer "+testToken || got[0].typ != "application/json" {
			t.Errorf("%s: Authorization %q, Content-Type %q", name, got[0].auth, got[0].typ)
		}
		for k, v := range want {
			if got[0].payload[k] != v {
				t.Errorf("%s: %s = %#v, want %#v", name, k, got[0].payload[k], v)
			}
		}
		if len(got[0].payload) != len(want) {
			t.Errorf("%s: fields %v", name, got[0].payload)
		}
	}

	// 201 created, from mygo.json's version.
	out, stderr, err := register(env, "--date", "2026-10-16")
	if err != nil || !strings.Contains(out, "created") {
		t.Fatalf("201: %v\n%s%s", err, out, stderr)
	}
	check("201", in.requests(), want)
	// 200 updated, a release given by hand.
	in.answer(http.StatusOK, `{"ok":true}`)
	out, _, err = register(env, "--version", "0.3.0", "--date", "2026-10-14", "--unpublished")
	if err != nil || !strings.Contains(out, "updated") {
		t.Errorf("200: %v, %s", err, out)
	}
	stable := map[string]any{"version": "0.3.0", "channel": "stable", "dmgKey": "audiosmith/AudioSmith-0.3.0.dmg", "notes": "- Three.", "published": false, "date": "2026-10-14"}
	for k, v := range want {
		if _, ok := stable[k]; !ok {
			stable[k] = v
		}
	}
	check("200", in.requests(), stable)

	// Failures say what the site said, never the token, even when the site
	// repeats it.
	for code, hint := range map[int]string{
		400: "refused the body", 401: "ACTNOW_INGEST_TOKEN is wrong", 403: "not audiosmith's",
		422: "is not in R2: upload the disk image first", 404: "HTTP 404",
	} {
		in.answer(code, `{"error":"no: Bearer `+testToken+`"}`)
		out, stderr, err := register(env)
		if err == nil || !strings.Contains(stderr, "HTTP "+itoa(code)) || !strings.Contains(stderr, hint) || !strings.Contains(stderr, `{"error":"no: Bearer ***"}`) {
			t.Errorf("%d: %v\n%s%s", code, err, out, stderr)
		}
		if strings.Contains(out+stderr, testToken) {
			t.Errorf("%d: printed the token", code)
		}
	}

	// A dry run sends nothing and needs no token.
	in.answer(http.StatusCreated, "")
	out, _, err = register([]string{"ACTNOW_API=" + in.srv.URL + "/api/releases"}, "--dry-run", "--date", "2026-10-16")
	var p map[string]any
	if _, body, _ := strings.Cut(out, ":\n"); err != nil || json.Unmarshal([]byte(body), &p) != nil || p["dmgKey"] != want["dmgKey"] || len(in.requests()) != 0 {
		t.Errorf("dry run: %v, %s", err, out)
	}

	// Refused before sending anything.
	for _, c := range []struct {
		env  []string
		args []string
		says string
	}{
		{[]string{"ACTNOW_API=" + in.srv.URL}, nil, "ACTNOW_INGEST_TOKEN is not set"},
		{env, []string{"--version", "9.9.9"}, `no "## 9.9.9" section`},
		{env, []string{"--channel", "nightly"}, "not beta or stable"},
		{env, []string{"--date", "16/10/2026"}, "not YYYY-MM-DD"},
		{env, []string{"--date"}, "--date needs a value"},
		{env, []string{"--dmg", "build/none.dmg"}, "no disk image"},
	} {
		_, stderr, err := register(c.env, c.args...)
		if err == nil || !strings.Contains(stderr, c.says) {
			t.Errorf("%v: %v, %s", c.args, err, stderr)
		}
	}
	if n := len(in.requests()); n != 0 {
		t.Errorf("refused registrations sent %d requests", n)
	}
	// The site unreachable.
	_, stderr, err = register([]string{"ACTNOW_API=http://127.0.0.1:1/api/releases", "ACTNOW_INGEST_TOKEN=" + testToken})
	if err == nil || !strings.Contains(stderr, "could not reach") || strings.Contains(stderr, testToken) {
		t.Errorf("unreachable: %v, %s", err, stderr)
	}
}

func itoa(n int) string {
	b, _ := json.Marshal(n)
	return string(b)
}
