package main

// The smoke suite's cases for the M4 analysis models (catalogue tasks
// separate and diarize), and its sandboxed engine.

import (
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/hehehai/AudioSmith/internal/catalog"
	"github.com/hehehai/AudioSmith/internal/engine/enginetest"
	"github.com/hehehai/AudioSmith/internal/engine/proto"
)

// offlineEngine wraps bin so that it runs under sandbox-exec with
// outbound network denied: every model must load from its local snapshot.
// Without sandbox-exec it returns bin.
func offlineEngine(t *testing.T, bin string) string {
	t.Helper()
	sb, err := exec.LookPath("sandbox-exec")
	if err != nil {
		t.Log("no sandbox-exec: the engine runs with the network")
		return bin
	}
	wrapper := filepath.Join(t.TempDir(), "engine-offline.sh")
	script := "#!/bin/sh\ncd '" + filepath.Dir(bin) + "' && exec " + sb +
		" -p '(version 1)(allow default)(deny network-outbound (remote ip))' '" + bin + "' \"$@\"\n"
	if err := os.WriteFile(wrapper, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Log("engine runs with outbound network denied (sandbox-exec)")
	return wrapper
}

// smokeClip is two Kokoro speakers taking turns at 44.1 kHz mono, and the
// same over synthetic music in stereo.
type smokeClip struct {
	speech, music []float64 // mono
	mix           string    // stereo WAV: 0.9 × speech + music
	clean         string    // mono WAV: the speech alone
	switchAt      float64   // the second speaker starts here
	end           float64   // and ends here
}

const smokeClipRate = 44100

func makeSmokeClip(outDir string, ref string, call func(string, any, any) error, kokoro catalog.Entry, kokoroPath string) (smokeClip, error) {
	second := filepath.Join(outDir, "speaker2.wav")
	var sr proto.SynthesizeResult
	if err := call(proto.MethodTTSSynthesize, proto.SynthesizeParams{Model: kokoro.Repo, ModelPath: kokoroPath, Text: smokeEN + " " + smokeRefText,
		Voice: "am_michael", Output: proto.Output{Path: second}}, &sr); err != nil {
		return smokeClip{}, err
	}
	var c smokeClip
	for i, p := range []string{ref, second} {
		a, err := enginetest.ReadWAV(p)
		if err != nil {
			return c, err
		}
		a = enginetest.Convert(a, smokeClipRate, 1)
		c.speech = append(c.speech, make([]float64, smokeClipRate*7/10)...)
		if i == 1 {
			c.switchAt = float64(len(c.speech)) / smokeClipRate
		}
		c.speech = append(c.speech, a.Samples...)
	}
	c.end = float64(len(c.speech)) / smokeClipRate
	c.speech = append(c.speech, make([]float64, smokeClipRate/2)...)
	stereoMusic := synthMusic(len(c.speech), smokeClipRate)
	mix := make([]float64, len(stereoMusic))
	c.music = make([]float64, len(c.speech))
	for i, v := range c.speech {
		c.music[i] = stereoMusic[2*i]
		mix[2*i], mix[2*i+1] = 0.9*v+stereoMusic[2*i], 0.9*v+stereoMusic[2*i+1]
	}
	c.mix = filepath.Join(outDir, "two-speakers-music.wav")
	c.clean = filepath.Join(outDir, "two-speakers.wav")
	if err := enginetest.WriteWAV(c.mix, enginetest.Audio{Rate: smokeClipRate, Channels: 2, Samples: mix}, 16); err != nil {
		return c, err
	}
	return c, enginetest.WriteWAV(c.clean, enginetest.Audio{Rate: smokeClipRate, Channels: 1, Samples: c.speech}, 16)
}

// smokeSeparate: the vocals stem must be much closer to the speech than
// the mix is, the background close to the music, and as long as the input.
func smokeSeparate(e catalog.Entry, snap, outDir string, clip func() (smokeClip, error), call func(string, any, any) error, add func(smokeRow)) {
	r := smokeRow{Case: "separate"}
	c, err := clip()
	if err != nil {
		r.Note = "clip: " + err.Error()
		add(r)
		return
	}
	v, b := filepath.Join(outDir, e.ID+"-vocals.wav"), filepath.Join(outDir, e.ID+"-background.wav")
	var sr proto.SeparateResult
	if err := call(proto.MethodSeparateRun, proto.SeparateParams{Model: e.Repo, ModelPath: snap, Audio: c.mix,
		Vocals: proto.Output{Path: v}, Background: proto.Output{Path: b}}, &sr); err != nil {
		r.Note = err.Error()
		add(r)
		return
	}
	r.Seconds, r.AudioSeconds, r.Output = sr.Seconds, sr.AudioSeconds, v
	r.RTF = sr.AudioSeconds / max(sr.Seconds, 1e-3)
	va, err1 := enginetest.ReadWAV(v)
	ba, err2 := enginetest.ReadWAV(b)
	if err1 != nil || err2 != nil {
		r.Note = fmt.Sprint("stems: ", err1, err2)
		add(r)
		return
	}
	mixMono := make([]float64, len(c.speech))
	for i := range mixMono {
		mixMono[i] = 0.9*c.speech[i] + c.music[i]
	}
	base := siSDR(c.speech, mixMono)
	vs, bs := siSDR(c.speech, va.Mono()), siSDR(c.music, ba.Mono())
	r.Note = fmt.Sprintf("SI-SDR vocals %.1f dB (mix %.1f), background %.1f dB, %d Hz × %d", vs, base, bs, sr.SampleRate, sr.Channels)
	r.Pass = math.Abs(float64(sr.Frames-int64(len(c.speech)))) <= 1 && vs >= 8 && vs >= base+6 && bs >= 3
	add(r)
}

// smokeDiarize: two speakers, and most of the speech attributed to the
// right one.
func smokeDiarize(e catalog.Entry, snap string, clip func() (smokeClip, error), call func(string, any, any) error, add func(smokeRow)) {
	r := smokeRow{Case: "diarize"}
	c, err := clip()
	if err != nil {
		r.Note = "clip: " + err.Error()
		add(r)
		return
	}
	var dr proto.DiarizeResult
	if err := call(proto.MethodDiarizeRun, proto.DiarizeParams{Model: e.Repo, ModelPath: snap, Audio: c.clean}, &dr); err != nil {
		r.Note = err.Error()
		add(r)
		return
	}
	r.Seconds, r.AudioSeconds = dr.Seconds, dr.AudioSeconds
	r.RTF = dr.AudioSeconds / max(dr.Seconds, 1e-3)
	// Who speaks most in each half, and how much of the time turns agree.
	who := func(from, to float64) (string, float64) {
		best, bestT, total := "", 0.0, 0.0
		by := map[string]float64{}
		for _, t := range dr.Turns {
			if o := min(t.End, to) - max(t.Start, from); o > 0 {
				by[t.Speaker] += o
				total += o
			}
		}
		for s, d := range by {
			if d > bestT {
				best, bestT = s, d
			}
		}
		return best, bestT / max(to-from, 1e-9)
	}
	a, fa := who(0.7, c.switchAt-0.7)
	b, fb := who(c.switchAt, c.end)
	r.Note = fmt.Sprintf("%d speakers, %d turns; first %q %.0f%%, second %q %.0f%% (switch at %.1f s)", dr.Speakers, len(dr.Turns), a, 100*fa, b, 100*fb, c.switchAt)
	r.Pass = dr.Speakers == 2 && a != "" && b != "" && a != b && fa > 0.6 && fb > 0.6
	add(r)
}
