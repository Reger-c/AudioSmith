package main

import (
	"fmt"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"testing"

	"github.com/hehehai/AudioSmith/internal/dub"
	"github.com/hehehai/AudioSmith/internal/i18n"
	"github.com/hehehai/AudioSmith/internal/settings"
)

// TestSnapshotsTimeline renders the Dub editor's timeline for a long
// source (thread #107): 65 lines in 6.5 minutes at fit, their numbers
// collapsed, and zoomed in with the overview strip.
func TestSnapshotsTimeline(t *testing.T) { runSnapshots(t, snapshotsTimeline) }

func snapshotsTimeline(t *testing.T, out string) {
	t.Cleanup(func() { i18n.SetLocale("en") })
	for _, run := range []struct {
		lang, suffix string
		dark         bool
	}{{"en", "light", false}, {"zh-Hans", "dark-zh-Hans", true}} {
		i18n.SetLocale("en")
		d := newDubTest(t, 6)
		a, tt := d.a, d.tt
		if run.lang != "en" {
			a.updateSettings(func(s *settings.Settings) { s.General.Language = run.lang })
		}
		tt.SetDark(run.dark)
		tt.SetScale(2)
		save := func(name string) {
			tt.Frame()
			tt.Frame()
			f, err := os.Create(filepath.Join(out, name+"-"+run.suffix+".png"))
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			if err := png.Encode(f, tt.Image()); err != nil {
				t.Fatal(err)
			}
		}
		a.dubSourceFile(d.src)
		a.dubStart()
		tt.Frame()
		const dur = 390.0
		var spans [][2]float64
		a.dubSet(func(p *dub.Project) {
			base := p.Segments
			var segs []dub.Segment
			at := 1.0
			for i := range 65 {
				s := base[i%len(base)]
				s.ID = fmt.Sprintf("s%03d", i)
				s.Start, s.End = at, at+3.5+float64(i%4)*1.2
				at = s.End + 0.3 + float64(i%3)*0.2
				spans = append(spans, [2]float64{s.Start, s.End})
				segs = append(segs, s)
			}
			p.Segments = segs
			p.Source.Duration = dur
			p.Media.Onsets = nil
		})
		a.dubPeaks = tlTestPeaks(dur, spans)
		a.dubStep = dubStepTranscribe
		a.dubTL = timelineState{zoom: 1}
		tt.SetSize(1800, 760) // a wide window, as the user's: some numbers fit, side by side
		save("dub-timeline-dense")
		tt.SetSize(1100, 720)

		a.dubCur = "s030"
		a.dubTL.zoom, a.dubTL.offset = 8, 170
		save("dub-timeline-zoomed")
	}
}

// tlTestPeaks is a waveform of dur seconds: speech in spans, hum between.
func tlTestPeaks(dur float64, spans [][2]float64) *dub.Peaks {
	const rate = 2000
	b := dub.NewPeakBuilder(rate, 1)
	buf := make([]float32, int(dur*rate))
	k := 0
	for i := range buf {
		t := float64(i) / rate
		v := 0.004 * math.Sin(2*math.Pi*50*t)
		for k < len(spans) && spans[k][1] < t {
			k++
		}
		if k < len(spans) && t >= spans[k][0] {
			v += 0.35 * math.Sin(2*math.Pi*170*t) * (0.55 + 0.45*math.Sin(2*math.Pi*3.1*t+float64(k)))
		}
		buf[i] = float32(v)
	}
	b.Write(buf)
	return b.Peaks()
}
