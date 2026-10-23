package dub

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestYtDlpLive downloads a real video with the real yt-dlp, reporting
// its progress, then starts again and cancels half-way: nothing of
// yt-dlp may be left running. Run on a Mac by hand:
//
//	AUDIOSMITH_YTDLP=/path/to/yt-dlp AUDIOSMITH_YTDLP_URL=https://… \
//	AUDIOSMITH_YTDLP_PROXY=http://127.0.0.1:7897 AUDIOSMITH_FFMPEG=/opt/homebrew/bin/ffmpeg \
//	go test -run TestYtDlpLive -v ./internal/dub
func TestYtDlpLive(t *testing.T) {
	bin, u := os.Getenv("AUDIOSMITH_YTDLP"), os.Getenv("AUDIOSMITH_YTDLP_URL")
	if bin == "" || u == "" {
		t.Skip("set AUDIOSMITH_YTDLP and AUDIOSMITH_YTDLP_URL")
	}
	y := YtDlp{Path: bin, Proxy: os.Getenv("AUDIOSMITH_YTDLP_PROXY"), FFmpeg: os.Getenv("AUDIOSMITH_FFMPEG")}
	dir := t.TempDir()
	var got []DownloadProgress
	start := time.Now()
	video, _, err := y.Download(context.Background(), u, filepath.Join(dir, "full"), DownloadOptions{}, func(p DownloadProgress) {
		got = append(got, p)
		t.Logf("%5.1fs %-8s %6.1f%% %d/%d eta %.0fs stream %d/%d %q", time.Since(start).Seconds(), p.Phase, 100*p.Fraction, p.Done, p.Total, p.ETA, p.Stream, p.Streams, p.Title)
	})
	if err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(video)
	if err != nil || len(got) == 0 {
		t.Fatalf("video %q %v, %d reports", video, err, len(got))
	}
	t.Logf("%s: %d bytes", filepath.Base(video), fi.Size())
	last := -1.0
	for _, p := range got {
		if p.Phase == DownloadFetching && p.Fraction >= 0 {
			if p.Fraction < last {
				t.Errorf("went back: %.3f after %.3f", p.Fraction, last)
			}
			last = p.Fraction
		}
	}

	// Cancel once it is under way.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	slow := y
	done := make(chan error, 1)
	part := filepath.Join(dir, "cancel")
	go func() {
		_, _, err := slow.Download(ctx, u, part, DownloadOptions{}, func(p DownloadProgress) {
			if p.Fraction > 0.2 || time.Since(start) > time.Minute {
				cancel()
			}
		})
		done <- err
	}()
	go func() { time.Sleep(3 * time.Second); cancel() }() // a small video: it may not report much
	select {
	case err := <-done:
		t.Logf("after cancel: %v", err)
	case <-time.After(30 * time.Second):
		t.Fatal("Download did not return after the cancel")
	}
	time.Sleep(500 * time.Millisecond)
	out, _ := exec.Command("pgrep", "-fl", part).CombinedOutput()
	if s := strings.TrimSpace(string(out)); s != "" {
		t.Errorf("still running:\n%s", s)
	}
}
