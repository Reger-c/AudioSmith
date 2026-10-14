package qc

import (
	"path/filepath"
	"testing"

	"github.com/hehehai/AudioSmith/internal/dsp"
	"github.com/hehehai/AudioSmith/internal/dsp/wav"
)

// Real TTS takes (Kokoro, English with plosives and Chinese, 24 kHz) have
// no clicks: plosive onsets after silence are sharp, but they are not
// isolated (speech follows), so Check audio must not flag them. A click
// added to the same take still is.
func TestTTSHasNoClicks(t *testing.T) {
	files, _ := filepath.Glob("testdata/*.wav")
	if len(files) < 2 {
		t.Fatal("no testdata takes")
	}
	for _, path := range files {
		b, _, err := wav.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if n := len(codes(Check(b, Options{}))["click"]); n != 0 {
			t.Errorf("%s: %d clicks: %+v", filepath.Base(path), n, codes(Check(b, Options{}))["click"])
		}
		// One real click in a loud part.
		withClick := dsp.Buffer{Rate: b.Rate, Channels: b.Channels, Samples: append([]float32(nil), b.Samples...)}
		at := loudestFrame(withClick)
		withClick.Samples[at*b.Channels] += 0.5
		got := codes(Check(withClick, Options{}))["click"]
		if len(got) != 1 || got[0].Start > float64(at)/float64(b.Rate)+0.01 || got[0].Start < float64(at)/float64(b.Rate)-0.01 {
			t.Errorf("%s: the added click at %.3f s: %+v", filepath.Base(path), float64(at)/float64(b.Rate), got)
		}
	}
}

// loudestFrame is the frame of the largest |sample| of the first channel.
func loudestFrame(b dsp.Buffer) int {
	best, at := float32(0), 0
	for i := 0; i < len(b.Samples); i += b.Channels {
		v := b.Samples[i]
		if v < 0 {
			v = -v
		}
		if v > best {
			best, at = v, i/b.Channels
		}
	}
	return at
}
