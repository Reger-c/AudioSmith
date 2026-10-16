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
