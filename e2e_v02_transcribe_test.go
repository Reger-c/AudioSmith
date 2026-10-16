package main

// TestV02's Transcribe step; split from e2e_v02_test.go.

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hehehai/AudioSmith/internal/core"
	"github.com/hehehai/AudioSmith/internal/engine/enginetest"
	"github.com/hehehai/AudioSmith/internal/services"
	"github.com/hehehai/AudioSmith/internal/settings"
)

// transcribe: a minute of two voices, Accurate (Whisper turbo + the
// forced aligner), word timings, SRT and VTT exports.
func (v *v02) transcribe(t *testing.T) string {
	a := v.a
	lines := []string{
		"Good morning everyone, and welcome to the weekly product meeting.",
		"Today we will look at the release plan, the open bugs, and the feedback from our first users.",
		"The most common request is a faster way to export subtitles for long videos.",
		"We also heard that the first launch should explain which models to download.",
	}
	var parts []string
	for i, l := range lines {
		parts = append(parts, say(t, v.dir, fmt.Sprintf("tr%d", i), []string{"Daniel", "Samantha"}[i%2], l, 16000))
	}
	in := filepath.Join(v.dir, "meeting.wav")
	var all []float64
	for _, p := range parts {
		au, err := enginetest.ReadWAV(p)
		if err != nil {
			t.Fatal(err)
		}
		all = append(all, au.Samples...)
		all = append(all, make([]float64, 8000)...)
	}
	enginetest.WriteWAV(in, enginetest.Audio{Rate: 16000, Channels: 1, Samples: all}, 16)
	a.trMode = 1
	started := time.Now()
	a.transcribeFile(in)
	if a.trJob == nil {
		t.Fatalf("transcription did not start: %q", a.trErr)
	}
	v.wait(t, "transcription", 15*time.Minute, func() bool { return a.trJob == nil })
	if a.trErr != "" {
		t.Fatalf("transcription: %s", a.trErr)
	}
	ts, err := a.trSvc.List(context.Background(), core.TranscriptQuery{})
	if err != nil || len(ts) == 0 {
		t.Fatalf("transcripts %v %v", ts, err)
	}
	tr, _ := a.trSvc.Get(context.Background(), ts[0].ID)
	var text string
	nw := 0
	ordered := true
	last := -1.0
	for _, s := range tr.Segments {
		text += " " + s.Text
		for _, w := range s.Words {
			nw++
			if w.Start < last-0.05 {
				ordered = false
			}
			last = w.Start
		}
	}
	want := strings.Join(lines, " ")
	if overlap(want, text) < 0.9 || tr.Mode != settings.TranscribeAccurate || nw < len(words(want))*8/10 || !ordered {
		t.Errorf("transcript (%s, aligner %q): %d words timed, ordered %v: %q", tr.Mode, tr.Aligner, nw, ordered, text)
	}
	srt, err := services.Export(tr, services.ExportSRT)
	os.WriteFile(filepath.Join(v.dir, "meeting.srt"), srt, 0o644)
	if err != nil || !bytes.Contains(srt, []byte("-->")) || !bytes.Contains(srt, []byte("00:00:0")) {
		t.Errorf("srt %v %q", err, srt)
	}
	vtt, err := services.Export(tr, services.ExportVTT)
	if err != nil || !bytes.HasPrefix(vtt, []byte("WEBVTT")) {
		t.Errorf("vtt %v %q", err, vtt)
	}
	return fmt.Sprintf("%.0f s audio in %.0f s, %d segments, %d timed words, %.0f%% words right, aligner %s",
		tr.Duration, time.Since(started).Seconds(), len(tr.Segments), nw, 100*overlap(want, text), filepath.Base(tr.Aligner))
}
