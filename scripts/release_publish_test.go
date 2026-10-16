package scripts

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// publishRoot is a checkout with a verified build of 0.4.0-beta.5 in
// build/darwin-arm64, configured for the website (release-config.sh), and
// fakes of gh and mygo that log their calls (and mygo the credentials it
// got) in log.
func publishRoot(t *testing.T, manifestURL string) (root, log string) {
	t.Helper()
	const base = "https://actnow.dev/updates/audiosmith/beta/"
	if manifestURL == "" {
		manifestURL = base + "audiosmith-0.4.0-beta.5-darwin-arm64.tar.gz"
	}
	manifest, _ := json.Marshal(map[string]any{
		"version": "0.4.0-beta.5", "notes": "- Five.", "url": manifestURL, "size": 7, "signature": "sig",
		"deltas":   []map[string]any{{"from": "0.4.0-beta.4", "url": base + "audiosmith-0.4.0-beta.4-to-0.4.0-beta.5-darwin-arm64.delta", "size": 5, "signature": "dsig"}},
		"previous": []map[string]any{{"version": "0.4.0-beta.4", "url": "https://github.com/hehehai/AudioSmith/releases/download/v0.4.0-beta.4/a.tar.gz"}},
	})
	config := `{"name": "AudioSmith", "version": "0.4.0-beta.5", "macos": {"minimumSystemVersion": "15.0"},
		"updates": {"publicKey": "k", "url": "https://actnow.dev/updates/audiosmith", "s3": {"bucket": "actnow-releases", "prefix": "audiosmith"}}}`
	root = fakeRoot(t, []string{"actnow.sh", "actnow-register.sh", "release-publish.sh"}, map[string]string{
		"mygo.json":    config,
		"CHANGELOG.md": "# Changelog\n\n## 0.4.0-beta.5\n\n- Five.\n\n## 0.4.0-beta.4\n\n- Four.\n",
		"build/darwin-arm64/AudioSmith 0.4.0-beta.5 arm64.dmg":                          "disk image",
		"build/darwin-arm64/audiosmith-0.4.0-beta.5-darwin-arm64.tar.gz":                "archive",
		"build/darwin-arm64/audiosmith-0.4.0-beta.4-to-0.4.0-beta.5-darwin-arm64.delta": "delta",
		"build/darwin-arm64/update-darwin-arm64.json":                                   string(manifest),
		"build/darwin-arm64/AudioSmith.app/Contents/Info.plist":                         "plist",
		"bin/gh": `#!/bin/bash
echo "gh $*" >> "$LOG"
# A release exists once created.
if [ "$1 $2" = "release view" ]; then [ -f "$LOG.created" ]; exit; fi
if [ "$1 $2" = "release create" ]; then touch "$LOG.created"; fi
exit 0
`,
		"bin/mygo": `#!/bin/bash
echo "mygo $* key=$AWS_ACCESS_KEY_ID secret=$AWS_SECRET_ACCESS_KEY" >> "$LOG"
`,
	})
	return root, filepath.Join(root, "log")
}

func readLog(path string) []string {
	b, _ := os.ReadFile(path)
	if len(b) == 0 {
		return nil
	}
	return strings.Split(strings.TrimSpace(string(b)), "\n")
}

func TestReleasePublish(t *testing.T) {
	needTools(t)
	in := newIngest(t)
	publish := func(root, log string, env []string, args ...string) (string, string, error) {
		env = append([]string{
			"PATH=" + filepath.Join(root, "bin") + ":" + os.Getenv("PATH"), "LOG=" + log,
			"MYGO=" + filepath.Join(root, "bin", "mygo"), "ACTNOW_API=" + in.srv.URL + "/api/releases",
		}, env...)
		return run(t, root, env, "bash", append([]string{"scripts/release-publish.sh"}, args...)...)
	}
	secrets := []string{"R2_ACCESS_KEY_ID=r2-id", "R2_SECRET_ACCESS_KEY=r2-secret", "ACTNOW_INGEST_TOKEN=" + testToken}
	ghManifest := func(root string) map[string]any {
		t.Helper()
		var m map[string]any
		b, err := os.ReadFile(filepath.Join(root, "build/github/update-darwin-arm64.json"))
		if err != nil || json.Unmarshal(b, &m) != nil {
			t.Fatalf("build/github/update-darwin-arm64.json: %v, %s", err, b)
		}
		return m
	}
	dmg := "build/darwin-arm64/AudioSmith-0.4.0-beta.5.dmg"

	// A dry run of the transition: the disk image renamed, the GitHub files
	// made, nothing uploaded or registered.
	root, log := publishRoot(t, "")
	out, stderr, err := publish(root, log, []string{"GITHUB_FEED=true"}, "--dry-run")
	if err != nil {
		t.Fatalf("dry run: %v\n%s%s", err, out, stderr)
	}
	if _, err := os.Stat(filepath.Join(root, dmg)); err != nil {
		t.Errorf("the disk image was not renamed: %v", err)
	}
	gh := "https://github.com/hehehai/AudioSmith/releases/download/v0.4.0-beta.5/"
	m := ghManifest(root)
	delta := m["deltas"].([]any)[0].(map[string]any)
	if m["url"] != gh+"audiosmith-0.4.0-beta.5-darwin-arm64.tar.gz" || delta["url"] != gh+"audiosmith-0.4.0-beta.4-to-0.4.0-beta.5-darwin-arm64.delta" ||
		m["signature"] != "sig" || delta["signature"] != "dsig" || m["version"] != "0.4.0-beta.5" {
		t.Errorf("the transition manifest: %v", m)
	}
	entries, _ := os.ReadDir(filepath.Join(root, "build/github"))
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	if want := []string{"audiosmith-0.4.0-beta.4-to-0.4.0-beta.5-darwin-arm64.delta", "audiosmith-0.4.0-beta.5-darwin-arm64.tar.gz", "update-darwin-arm64.json"}; !slices.Equal(names, want) {
		t.Errorf("build/github: %v", names)
	}
	notes, _ := os.ReadFile(filepath.Join(root, "build/github-notes.md"))
	if !strings.HasPrefix(string(notes), "- Five.\n\n---\n") || !strings.Contains(string(notes), "https://actnow.dev/download/audiosmith") || strings.Contains(string(notes), "Four") {
		t.Errorf("notes:\n%s", notes)
	}
	if !strings.Contains(out, `"dmgKey": "audiosmith/beta/AudioSmith-0.4.0-beta.5.dmg"`) || !strings.Contains(out, "s3://actnow-releases/audiosmith/beta/") {
		t.Errorf("dry run says:\n%s", out)
	}
	if calls := readLog(log); calls != nil || len(in.requests()) != 0 {
		t.Errorf("a dry run called %v and registered %d times", calls, len(in.requests()))
	}

	// The transition for real: the website (credentials mapped to AWS's
	// names), the registration, then the GitHub draft prerelease with the
	// archive and delta, then its manifest.
	out, stderr, err = publish(root, log, append([]string{"GITHUB_FEED=true"}, secrets...))
	if err != nil {
		t.Fatalf("publish: %v\n%s%s", err, out, stderr)
	}
	calls := readLog(log)
	want := []string{
		"mygo publish -platform darwin/arm64 key=r2-id secret=r2-secret",
		"gh release view v0.4.0-beta.5 --repo hehehai/AudioSmith",
		"gh release create v0.4.0-beta.5 --repo hehehai/AudioSmith --draft --title v0.4.0-beta.5 --notes-file build/github-notes.md --prerelease",
		"gh release edit v0.4.0-beta.5 --repo hehehai/AudioSmith --prerelease --latest=false",
		"gh release upload v0.4.0-beta.5 --repo hehehai/AudioSmith --clobber build/github/audiosmith-0.4.0-beta.5-darwin-arm64.tar.gz build/github/audiosmith-0.4.0-beta.4-to-0.4.0-beta.5-darwin-arm64.delta",
		"gh release upload v0.4.0-beta.5 --repo hehehai/AudioSmith --clobber build/github/update-darwin-arm64.json",
	}
	if !slices.Equal(calls, want) {
		t.Errorf("calls\n%s\nwant\n%s", strings.Join(calls, "\n"), strings.Join(want, "\n"))
	}
	if r := in.requests(); len(r) != 1 || r[0].payload["dmgKey"] != "audiosmith/beta/AudioSmith-0.4.0-beta.5.dmg" || r[0].payload["dmgSize"] != float64(10) || r[0].payload["notes"] != "- Five." {
		t.Errorf("registered %v", r)
	}
	if strings.Contains(out+stderr, testToken) || strings.Contains(out+stderr, "r2-secret") {
		t.Error("printed a secret")
	}

	// After the transition: the GitHub release (already there) gets the
	// website's manifest only, pointing at actnow.dev.
	os.Remove(log)
	in.answer(http.StatusOK, "")
	if out, stderr, err := publish(root, log, append([]string{"GITHUB_FEED=false"}, secrets...)); err != nil {
		t.Fatalf("publish: %v\n%s%s", err, out, stderr)
	}
	calls = readLog(log)
	want = []string{
		"mygo publish -platform darwin/arm64 key=r2-id secret=r2-secret",
		"gh release view v0.4.0-beta.5 --repo hehehai/AudioSmith",
		"gh release edit v0.4.0-beta.5 --repo hehehai/AudioSmith --prerelease --latest=false",
		"gh release upload v0.4.0-beta.5 --repo hehehai/AudioSmith --clobber build/github/update-darwin-arm64.json",
	}
	if !slices.Equal(calls, want) {
		t.Errorf("after the transition, calls\n%s\nwant\n%s", strings.Join(calls, "\n"), strings.Join(want, "\n"))
	}
	if m := ghManifest(root); m["url"] != "https://actnow.dev/updates/audiosmith/beta/audiosmith-0.4.0-beta.5-darwin-arm64.tar.gz" {
		t.Errorf("after the transition, the GitHub manifest: %v", m)
	}
	// --skip-github: the website only.
	os.Remove(log)
	if _, _, err := publish(root, log, secrets, "--skip-github"); err != nil || len(readLog(log)) != 1 {
		t.Errorf("--skip-github: %v, %v", err, readLog(log))
	}

	// Refused before any upload.
	for _, c := range []struct {
		name, url string
		env       []string
		says      string
	}{
		{"no R2 keys", "", []string{"ACTNOW_INGEST_TOKEN=x"}, "no R2 credentials"},
		{"no token", "", []string{"R2_ACCESS_KEY_ID=a", "R2_SECRET_ACCESS_KEY=b"}, "ACTNOW_INGEST_TOKEN is not set"},
		{"a GitHub build", "https://github.com/hehehai/AudioSmith/releases/download/v0.4.0-beta.5/a.tar.gz", secrets, "not at https://actnow.dev/updates/audiosmith"},
		{"bad toggle", "", append([]string{"GITHUB_FEED=yes"}, secrets...), "not true or false"},
	} {
		root, log := publishRoot(t, c.url)
		_, stderr, err := publish(root, log, c.env)
		if err == nil || !strings.Contains(stderr, c.says) || readLog(log) != nil {
			t.Errorf("%s: %v, %s, calls %v", c.name, err, stderr, readLog(log))
		}
	}
}
