package dub

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// fakeYtDlp writes a yt-dlp stand-in that records its arguments and
// writes the file -o names (as mp4).
func fakeYtDlp(t *testing.T) (path, argsFile string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell script")
	}
	dir := t.TempDir()
	argsFile = filepath.Join(dir, "args")
	path = filepath.Join(dir, "yt-dlp")
	script := `#!/bin/sh
printf '%s\n' "$@" > "` + argsFile + `"
out=""
prev=""
for a in "$@"; do
  if [ "$prev" = "-o" ]; then out="$a"; fi
  prev="$a"
done
f=$(echo "$out" | sed 's/%(ext)s/mp4/')
mkdir -p "$(dirname "$f")"
echo video > "$f"
echo "$f"
`
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path, argsFile
}

// A video offered only as separate streams needs ffmpeg to join them:
// the ffmpeg YtDlp names is passed on with the merging format, and
// without one only single-file formats are asked for.
func TestDownloadUsesFFmpegToMerge(t *testing.T) {
	bin, argsFile := fakeYtDlp(t)
	for _, tc := range []struct {
		ffmpeg string
		merge  bool
	}{{"/opt/homebrew/bin/ffmpeg", true}, {"", false}} {
		y := YtDlp{Path: bin, FFmpeg: tc.ffmpeg}
		video, _, err := y.Download(context.Background(), "https://example.com/v", filepath.Join(t.TempDir(), "dl"), DownloadOptions{}, nil)
		if err != nil {
			t.Fatal(err)
		}
		if filepath.Base(video) != "source.mp4" {
			t.Errorf("video %q", video)
		}
		b, _ := os.ReadFile(argsFile)
		args := string(b)
		hasLoc := strings.Contains(args, "--ffmpeg-location\n"+tc.ffmpeg+"\n")
		hasMerge := strings.Contains(args, "bv*+ba")
		if tc.merge != hasLoc || tc.merge != hasMerge {
			t.Errorf("ffmpeg %q: location %v, merge format %v\n%s", tc.ffmpeg, hasLoc, hasMerge, args)
		}
	}
}

// Info's JSON is one long line (YouTube's lists of formats and automatic
// captions make it a few MB): it is read whole.
func TestInfoReadsALongLine(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script")
	}
	dir := t.TempDir()
	pad := strings.Repeat("x", 3<<20)
	if err := os.WriteFile(filepath.Join(dir, "info.json"), []byte(`{"id":"a","title":"Long","pad":"`+pad+`"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(dir, "yt-dlp")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\ncat '"+filepath.Join(dir, "info.json")+"'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	info, err := YtDlp{Path: bin}.Info(context.Background(), "https://example.com/v")
	if err != nil || info.Title != "Long" {
		t.Fatalf("%+v %v", info, err)
	}
}
