// Package audio is AudioSmith's sound handling in Go: waveform peaks and
// playback today; the streaming player, timeline mixer, peaks pyramid
// cache, loudness and QC analysis later (thread A in M0, then M1/M4).
//
// Imports: nothing internal. It wraps github.com/egoist/mygo/audio.
package audio

import (
	"math"
	"os"

	myaudio "github.com/egoist/mygo/audio"
)

// Peaks reads an audio file and returns n peak magnitudes (0–1), one per
// equal slice of the file, for drawing its waveform. It decodes the whole
// file: call it off the UI thread (thread A moves it to a cached
// background job).
func Peaks(path string, n int) ([]float32, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	d, _, err := myaudio.NewDecoder(f)
	if err != nil {
		return nil, err
	}
	ch := max(d.Channels(), 1)
	var samples []float32
	buf := make([]float32, 8192*ch)
	for {
		k, err := d.Read(buf)
		for i := 0; i+ch <= k; i += ch {
			var m float32
			for j := range ch {
				m = max(m, float32(math.Abs(float64(buf[i+j]))))
			}
			samples = append(samples, m)
		}
		if err != nil || k == 0 {
			break
		}
	}
	return PeaksOf(samples, n), nil
}

// PeaksOf reduces per-frame magnitudes to n peaks normalized to the loudest.
func PeaksOf(samples []float32, n int) []float32 {
	if n <= 0 || len(samples) == 0 {
		return nil
	}
	out := make([]float32, n)
	var top float32
	for i := range n {
		lo := i * len(samples) / n
		hi := max((i+1)*len(samples)/n, lo+1)
		var m float32
		for _, s := range samples[lo:min(hi, len(samples))] {
			m = max(m, s)
		}
		out[i] = m
		top = max(top, m)
	}
	if top > 0 {
		for i := range out {
			out[i] /= top
		}
	}
	return out
}
