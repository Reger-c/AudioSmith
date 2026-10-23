package dub

import (
	"bufio"
	"os"
	"path/filepath"
	"testing"
)

// replay feeds a fixture of yt-dlp's output (testdata/ytdlp/name) to a
// parser and returns the progress after each of its lines.
func replay(t *testing.T, name string) []DownloadProgress {
	t.Helper()
	f, err := os.Open(filepath.Join("testdata", "ytdlp", name))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var pp progressParser
	var out []DownloadProgress
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if p, ok := pp.line(sc.Text()); ok {
			out = append(out, p)
		}
	}
	if len(out) == 0 {
		t.Fatalf("%s: no progress", name)
	}
	return out
}

// checkSteady: the fraction never goes back while downloading, and stays
// within 0–1.
func checkSteady(t *testing.T, ps []DownloadProgress) {
	t.Helper()
	last := -1.0
	for i, p := range ps {
		if p.Phase != DownloadFetching || p.Fraction < 0 {
			continue
		}
		if p.Fraction > 1 || p.Fraction < last {
			t.Errorf("line %d: fraction %.3f after %.3f", i, p.Fraction, last)
		}
		last = p.Fraction
	}
}

// One file (a format with video and audio): the fraction and sizes are
// the file's, the ETA comes from the speed, and once it is fetched the
// last steps show without a fraction.
func TestProgressOneStream(t *testing.T) {
	ps := replay(t, "single.txt")
	checkSteady(t, ps)
	first := ps[0]
	if first.Title == "" || first.Streams != 1 || first.Fraction != -1 {
		t.Errorf("info line: %+v", first)
	}
	var mid *DownloadProgress
	for i := range ps {
		if ps[i].Fraction > 0.3 && ps[i].Fraction < 0.7 {
			mid = &ps[i]
			break
		}
	}
	if mid == nil || mid.Total <= 0 || mid.Done <= 0 || mid.ETA <= 0 || mid.Stream != 1 {
		t.Fatalf("mid-way: %+v", mid)
	}
	end := ps[len(ps)-1]
	if end.Phase != DownloadFinishing || end.Fraction != -1 {
		t.Errorf("end %+v", end)
	}
}

// Separate video and audio streams (and captions): one fraction for both
// (weighted by the size yt-dlp announced), which does not fall back to 0
// when the audio starts, then merging, without a fraction.
func TestProgressTwoStreams(t *testing.T) {
	ps := replay(t, "two-streams.txt")
	checkSteady(t, ps)
	// The captions come first: nothing to show for them.
	if ps[0].Fraction != -1 || ps[0].Stream != 0 {
		t.Errorf("captions: %+v", ps[0])
	}
	if info := ps[2]; info.Streams != 2 || info.Title != "Me at the zoo" {
		t.Fatalf("info line: %+v", info)
	}
	sawAudio := false
	var beforeAudio float64
	for _, p := range ps {
		if p.Stream == 1 && p.Fraction > 0 {
			beforeAudio = p.Fraction
		}
		if p.Stream == 2 && p.Phase == DownloadFetching {
			sawAudio = true
			if p.Fraction < beforeAudio || p.Fraction < 0.5 {
				t.Errorf("audio stream at %.3f after the video's %.3f", p.Fraction, beforeAudio)
			}
		}
	}
	if !sawAudio {
		t.Fatal("no audio stream")
	}
	if beforeAudio > 0.99 {
		t.Errorf("the video alone reached %.3f", beforeAudio)
	}
	end := ps[len(ps)-1]
	if end.Phase != DownloadMerging || end.Fraction != -1 {
		t.Errorf("end %+v", end)
	}
}

// Captions downloaded first do not count as the video.
func TestProgressSkipsCaptions(t *testing.T) {
	var pp progressParser
	pp.line(`ASI 18 1200000 "Clip"`)
	p, ok := pp.line("ASP finished 5000 5000 NA NA NA /d/source.en.vtt")
	if !ok || p.Stream != 0 || p.Fraction != -1 {
		t.Errorf("caption: %+v", p)
	}
	p, _ = pp.line("ASP downloading 600000 1200000 NA 300000 2 /d/source.mp4")
	if p.Stream != 1 || p.Fraction != 0.5 || p.ETA != 2 {
		t.Errorf("video: %+v", p)
	}
}

// Without a size (no total, no estimate, nothing announced) the bar stays
// indeterminate and the bytes still count.
func TestProgressUnknownSize(t *testing.T) {
	ps := replay(t, "unknown-size.txt")
	for _, p := range ps {
		if p.Phase == DownloadFetching && p.Fraction != -1 {
			t.Fatalf("a fraction without a size: %+v", p)
		}
	}
	var done int64
	for _, p := range ps {
		done = max(done, p.Done)
	}
	if done <= 0 {
		t.Error("no bytes")
	}
}

// Two streams with no size announced: each counts for half until the
// last one's size makes the whole known; the bar never goes back.
func TestProgressTwoStreamsNoAnnouncedSize(t *testing.T) {
	var pp progressParser
	pp.line(`ASI 137+140 NA "Clip"`)
	a, _ := pp.line("ASP downloading 500 1000 NA 100 5 /d/source.f137.mp4")
	b, _ := pp.line("ASP finished 1000 1000 NA NA NA /d/source.f137.mp4")
	c, _ := pp.line("ASP downloading 10 100 NA 100 1 /d/source.f140.m4a")
	d, _ := pp.line("ASP finished 100 100 NA NA NA /d/source.f140.m4a")
	if a.Fraction != 0.25 || b.Fraction != 0.5 || c.Fraction != 1010.0/1100 || c.Total != 1100 || d.Phase != DownloadMerging || d.Fraction != -1 {
		t.Errorf("%v %v %v %+v", a.Fraction, b.Fraction, c.Fraction, d)
	}
}

func TestProgressIgnoresOtherLines(t *testing.T) {
	var pp progressParser
	for _, l := range []string{"", "[youtube] abc: Downloading webpage", "ASF /d/source.mp4", "ASP short"} {
		if _, ok := pp.line(l); ok {
			t.Errorf("%q read as progress", l)
		}
	}
}
