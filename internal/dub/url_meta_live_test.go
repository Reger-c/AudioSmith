package dub

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

// TestProbeLive reads real links' metadata with the real yt-dlp (no
// download) and fetches their covers through the proxy, as Dubs.Probe
// does. Run on a Mac by hand:
//
//	AUDIOSMITH_YTDLP=/path/to/yt-dlp AUDIOSMITH_YTDLP_PROXY=http://127.0.0.1:7897 \
//	AUDIOSMITH_PROBE_URLS=https://…,https://… go test -run TestProbeLive -v ./internal/dub
func TestProbeLive(t *testing.T) {
	bin, urls := os.Getenv("AUDIOSMITH_YTDLP"), os.Getenv("AUDIOSMITH_PROBE_URLS")
	if bin == "" || urls == "" {
		t.Skip("set AUDIOSMITH_YTDLP and AUDIOSMITH_PROBE_URLS")
	}
	y := YtDlp{Path: bin, Proxy: os.Getenv("AUDIOSMITH_YTDLP_PROXY"), FFmpeg: os.Getenv("AUDIOSMITH_FFMPEG")}
	for _, u := range strings.Split(urls, ",") {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		start := time.Now()
		info, err := y.Info(ctx, u)
		took := time.Since(start)
		if err != nil {
			t.Logf("%s: %.1fs error %v", u, took.Seconds(), err)
			cancel()
			continue
		}
		m := info.Meta
		t.Logf("%s: %.1fs %s | %q by %q | %s | %.0fs | %dx%d %s/%s | %d bytes approx=%v | views %d | captions %v | desc %d runes",
			u, took.Seconds(), m.SiteName, m.Title, m.Channel, m.Uploaded, m.Duration, m.Width, m.Height, m.VCodec, m.ACodec, m.Size, m.Approx,
			m.Views, m.Captions, len([]rune(m.Description)))
		if m.Thumb == "" {
			t.Logf("  no cover")
			cancel()
			continue
		}
		dir := t.TempDir()
		start = time.Now()
		name, err := FetchThumbnail(ctx, m.Thumb, dir, ThumbOptions{Proxy: y.Proxy})
		if err != nil {
			t.Errorf("  cover %s: %v", m.Thumb, err)
		} else {
			fi, _ := os.Stat(dir + "/" + name)
			t.Logf("  cover %s: %s, %d bytes, %.1fs", m.Thumb[:min(len(m.Thumb), 80)], name, fi.Size(), time.Since(start).Seconds())
		}
		cancel()
	}
}
