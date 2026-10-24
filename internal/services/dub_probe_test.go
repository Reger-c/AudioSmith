package services

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/hehehai/AudioSmith/internal/dub"
	"github.com/hehehai/AudioSmith/internal/dub/dubtest"
	"github.com/hehehai/AudioSmith/internal/jobs"
)

// probeYtDlp is a yt-dlp stand-in: asked for the info it runs info (a
// shell snippet) and counts the calls in <dir>/infos; asked to download
// it writes a small file.
func probeYtDlp(t *testing.T, info string) (path, dir string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell script")
	}
	dir = t.TempDir()
	path = filepath.Join(dir, "yt-dlp")
	script := `#!/bin/sh
for a in "$@"; do
  if [ "$a" = "--dump-single-json" ]; then echo x >> "` + dir + `/infos"; ` + info + `; fi
done
out=""; prev=""
for a in "$@"; do
  if [ "$prev" = "-o" ]; then out="$a"; fi
  prev="$a"
done
d=$(dirname "$out"); mkdir -p "$d"; cp "` + dir + `/video.wav" "$d/source.wav"; echo "ASF $d/source.wav"
`
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := dubtest.Speech(filepath.Join(dir, "video.wav"), 2); err != nil {
		t.Fatal(err)
	}
	return path, dir
}

func infoCalls(dir string) int {
	b, _ := os.ReadFile(filepath.Join(dir, "infos"))
	return strings.Count(string(b), "x")
}

const probeJSON = `{"id":"abc","title":"A talk about whales","extractor_key":"Youtube","uploader":"Sea Life","upload_date":"20240102",` +
	`"duration":125.5,"view_count":1234,"description":"Whales sing.\nA lot.","thumbnail":"https://i.ytimg.com/vi/abc/hq.jpg",` +
	`"subtitles":{"en":[{"ext":"vtt"}],"live_chat":[{"ext":"json"}]},"language":"en","format_id":"137+140","ext":"mp4",` +
	`"requested_formats":[{"vcodec":"avc1","acodec":"none","width":1920,"height":1080,"filesize":100000000},{"vcodec":"none","acodec":"mp4a","filesize":3000000}]}`

// A link's probe saves what the site says (and names the dub), then its
// cover; the download after it does not ask yt-dlp again.
func TestDubProbeURL(t *testing.T) {
	e := newDubEnv(t, 2)
	bin, dir := probeYtDlp(t, `printf '%s\n' '`+probeJSON+`'; exit 0`)
	e.ytdlp = dub.YtDlp{Path: bin}
	var fetched []string
	saved := fetchThumbnail
	fetchThumbnail = func(_ context.Context, raw, dir string, _ dub.ThumbOptions) (string, error) {
		fetched = append(fetched, raw)
		return "thumb.jpg", os.WriteFile(filepath.Join(dir, "thumb.jpg"), []byte("jpeg"), 0o644)
	}
	t.Cleanup(func() { fetchThumbnail = saved })
	url := "https://203.0.113.9/watch?v=abc"
	p := e.create(t, func(p *dub.Project) {
		p.Source = dub.Source{Kind: dub.SourceURL, URL: url, Name: url}
		p.Title = url
	})
	if err := e.dubs.Probe(ctx, p.ID); err != nil {
		t.Fatal(err)
	}
	got, _ := e.dubs.Get(ctx, p.ID)
	m := got.Source.Meta
	if m == nil {
		t.Fatal("no metadata")
	}
	if m.Title != "A talk about whales" || m.SiteName != "YouTube" || m.Channel != "Sea Life" || m.Uploaded != "2024-01-02" ||
		m.Views != 1234 || m.Size != 103000000 || m.Height != 1080 || m.Thumbnail != "thumb.jpg" || len(m.Captions) != 1 || m.At.IsZero() {
		t.Errorf("meta %+v", m)
	}
	if got.Title != m.Title || got.Source.Name != m.Title || got.Source.Duration != 125.5 {
		t.Errorf("project %q %q %v", got.Title, got.Source.Name, got.Source.Duration)
	}
	if len(fetched) != 1 || fetched[0] != "https://i.ytimg.com/vi/abc/hq.jpg" {
		t.Errorf("covers fetched %q", fetched)
	}
	if e.dubs.Probing(p.ID) {
		t.Error("still probing")
	}
	e.run(t, p.ID, RunRequest{Through: dub.StageReferences})
	if n := infoCalls(dir); n != 1 {
		t.Errorf("yt-dlp was asked %d times for the info", n)
	}
	got, _ = e.dubs.Get(ctx, p.ID)
	if got.Source.Path == "" || got.Source.Meta == nil || got.Source.Meta.Thumbnail != "thumb.jpg" || got.Detected != "en" {
		t.Errorf("after the download: %+v %+v", got.Source, got.Source.Meta)
	}
}

// A link that cannot be read keeps why; the fetch stage asks again.
func TestDubProbeError(t *testing.T) {
	e := newDubEnv(t, 2)
	bin, dir := probeYtDlp(t, `echo "ERROR: [youtube] abc: Private video. Sign in if you've been granted access to this video" >&2; exit 1`)
	e.ytdlp = dub.YtDlp{Path: bin}
	url := "https://203.0.113.9/watch?v=abc"
	p := e.create(t, func(p *dub.Project) { p.Source = dub.Source{Kind: dub.SourceURL, URL: url, Name: url} })
	if err := e.dubs.Probe(ctx, p.ID); err == nil {
		t.Error("no error")
	}
	got, _ := e.dubs.Get(ctx, p.ID)
	m := got.Source.Meta
	if m == nil || m.Error != dub.CodeYtDlp || !strings.HasPrefix(m.Detail, "Private video.") || m.Title != "" {
		t.Fatalf("meta %+v", m)
	}
	ids, err := e.dubs.Run(ctx, p.ID, RunRequest{Through: dub.StageReferences})
	if err != nil {
		t.Fatal(err)
	}
	if j := e.wait(t, ids["fetch"]); j.State != jobs.StateFailed || j.Err == nil || j.Err.Code != dub.CodeYtDlp {
		t.Errorf("fetch %s %+v", j.State, j.Err)
	}
	if n := infoCalls(dir); n != 2 {
		t.Errorf("info asked %d times", n)
	}
}

// A probe stops at once when the dub is stopped (the page's Cancel) and
// saves nothing; a file's probe reads its length.
func TestDubProbeCancelAndFile(t *testing.T) {
	e := newDubEnv(t, 2)
	bin, _ := probeYtDlp(t, `sleep 30`)
	e.ytdlp = dub.YtDlp{Path: bin}
	url := "https://203.0.113.9/watch?v=abc"
	p := e.create(t, func(p *dub.Project) { p.Source = dub.Source{Kind: dub.SourceURL, URL: url, Name: url} })
	done := make(chan error, 1)
	go func() { done <- e.dubs.Probe(context.Background(), p.ID) }()
	for end := time.Now().Add(5 * time.Second); !e.dubs.Probing(p.ID); time.Sleep(5 * time.Millisecond) {
		if time.Now().After(end) {
			t.Fatal("not probing")
		}
	}
	time.Sleep(100 * time.Millisecond) // yt-dlp runs
	at := time.Now()
	if err := e.dubs.Cancel(ctx, p.ID); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err == nil {
			t.Error("a cancelled probe returned no error")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the probe did not stop")
	}
	if d := time.Since(at); d > 2500*time.Millisecond {
		t.Errorf("stopped %v after the cancel", d)
	}
	if got, _ := e.dubs.Get(ctx, p.ID); got.Source.Meta != nil {
		t.Errorf("saved %+v", got.Source.Meta)
	}

	f := e.create(t, nil) // newDubEnv's speech file
	if err := e.dubs.Probe(ctx, f.ID); err != nil {
		t.Fatal(err)
	}
	got, _ := e.dubs.Get(ctx, f.ID)
	if got.Source.Duration <= 0 || got.Media.Audio != "" {
		t.Errorf("file probe: %+v", got.Source)
	}
}
