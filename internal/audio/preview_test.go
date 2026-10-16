package audio

import (
	"io"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"

	myaudio "github.com/egoist/mygo/audio"
)

// readAll decodes everything d has.
func readAll(t *testing.T, d myaudio.Decoder) []float32 {
	t.Helper()
	var out []float32
	buf := make([]float32, 1000)
	for {
		n, err := d.Read(buf)
		out = append(out, buf[:n]...)
		if err == io.EOF {
			return out
		}
		if err != nil {
			t.Fatal(err)
		}
		if n == 0 {
			t.Fatal("no progress")
		}
	}
}

// TestLimitedDecoder: a preview stops at its limit, fading out where it
// cuts; audio shorter than the limit plays whole, untouched.
func TestLimitedDecoder(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a.wav")
	writeWAV(t, path, 8000, 8000*3)
	open := func(limit time.Duration) *limited {
		f, err := os.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		d, _, err := myaudio.NewDecoder(f)
		if err != nil {
			t.Fatal(err)
		}
		l := newLimited(d, f, limit)
		t.Cleanup(func() { l.Close() })
		return l
	}
	l := open(time.Second)
	if l.Len() != 8000 {
		t.Errorf("Len %d", l.Len())
	}
	s := readAll(t, l)
	if len(s) != 8000 {
		t.Fatalf("read %d frames, want 8000", len(s))
	}
	peak := func(s []float32) (m float64) {
		for _, v := range s {
			m = max(m, math.Abs(float64(v)))
		}
		return m
	}
	// The sine grows over the file: at 1 s it is ~0.2; the last 150 ms
	// fade to nothing.
	if before, end := peak(s[8000-1600:8000-1200]), peak(s[8000-40:]); before < 0.1 || end > before*0.05 {
		t.Errorf("no fade: %.3f before, %.3f at the end", before, end)
	}
	// Seeking past the limit ends it.
	if err := l.SetPosition(20000); err != nil {
		t.Fatal(err)
	}
	if n, err := l.Read(make([]float32, 100)); n != 0 || err != io.EOF {
		t.Errorf("after the limit: %d %v", n, err)
	}
	// Longer than the audio, or no limit: all of it, no fade.
	for _, lim := range []time.Duration{0, 10 * time.Second} {
		l := open(lim)
		s := readAll(t, l)
		if len(s) != 24000 || l.Len() != 24000 || peak(s[24000-40:]) < 0.3 {
			t.Errorf("limit %v: %d frames, Len %d, end %.3f", lim, len(s), l.Len(), peak(s[len(s)-40:]))
		}
	}
}

// TestPlayPreview plays bundled data and a file's start; bad data fails.
func TestPlayPreview(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a.wav")
	writeWAV(t, path, 8000, 8000*3)
	data, _ := os.ReadFile(path)
	var pl Player
	defer pl.Stop()
	if err := pl.PlayPreview("p1", data, "", 0); err != nil || !pl.Playing("p1") {
		t.Fatalf("data: %v, current %q", err, pl.Current())
	}
	if err := pl.PlayPreview("p2", nil, path, time.Second); err != nil || !pl.Playing("p2") || pl.Playing("p1") {
		t.Fatalf("file: %v, current %q", err, pl.Current())
	}
	if d := pl.p.Duration(); d != time.Second {
		t.Errorf("limited duration %v", d)
	}
	if err := pl.PlayPreview("p3", []byte("not audio"), "", 0); err == nil || pl.Current() != "" {
		t.Errorf("garbage: %v, current %q", err, pl.Current())
	}
	if err := pl.PlayPreview("p4", nil, "/does/not/exist.wav", 0); err == nil {
		t.Error("missing file played")
	}
}
