// Package audio is AudioSmith's sound handling in Go: waveform peaks (with
// a disk cache) and the streaming player today; the timeline mixer, peaks
// pyramid, loudness and QC analysis later (M1/M4).
//
// Imports: nothing internal. It wraps github.com/egoist/mygo/audio.
package audio

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"strings"

	myaudio "github.com/egoist/mygo/audio"
)

// Peaks reads an audio file and returns n peak magnitudes (0–1), one per
// equal slice of the file, for drawing its waveform. It decodes the whole
// file, streaming when the decoder knows its length: call it off the UI
// thread, or use a PeakCache.
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
	buf := make([]float32, 8192*ch)
	if total := d.Len(); n > 0 && total >= int64(n) {
		// One pass into n buckets, without keeping the samples.
		out := make([]float32, n)
		var frame int64
		for {
			k, err := d.Read(buf)
			for i := 0; i+ch <= k; i += ch {
				var m float32
				for j := range ch {
					m = max(m, float32(math.Abs(float64(buf[i+j]))))
				}
				b := min(int(frame*int64(n)/total), n-1)
				out[b] = max(out[b], m)
				frame++
			}
			if err != nil || k == 0 {
				break
			}
		}
		if frame == 0 {
			return nil, nil
		}
		if frame < total {
			// Shorter than announced: spread what there is over n peaks.
			return PeaksOf(stretch(out, int(frame*int64(n)/total)), n), nil
		}
		return normalize(out), nil
	}
	var samples []float32
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

// stretch returns the first used buckets (at least one).
func stretch(p []float32, used int) []float32 { return p[:max(min(used, len(p)), 1)] }

// PeaksOf reduces per-frame magnitudes to n peaks normalized to the loudest.
func PeaksOf(samples []float32, n int) []float32 {
	if n <= 0 || len(samples) == 0 {
		return nil
	}
	out := make([]float32, n)
	for i := range n {
		lo := i * len(samples) / n
		hi := max((i+1)*len(samples)/n, lo+1)
		var m float32
		for _, s := range samples[lo:min(hi, len(samples))] {
			m = max(m, s)
		}
		out[i] = m
	}
	return normalize(out)
}

func normalize(p []float32) []float32 {
	var top float32
	for _, v := range p {
		top = max(top, v)
	}
	if top > 0 {
		for i := range p {
			p[i] /= top
		}
	}
	return p
}

// PeakCache keeps waveform peaks on disk, one file per audio id
// (<Dir>/<id>.f32: N little-endian float32), so a waveform is decoded once.
// Audio is assumed immutable under its id. Safe for concurrent use; its
// methods do I/O, so call them off the UI thread.
type PeakCache struct {
	Dir string
	N   int // peaks per waveform
}

func (c PeakCache) path(id string) (string, error) {
	if id == "" || id == "." || id == ".." || strings.ContainsAny(id, `/\`+"\x00") {
		return "", fmt.Errorf("peaks: bad id %q", id)
	}
	return filepath.Join(c.Dir, id+".f32"), nil
}

// Get returns id's peaks from the cache, or computes them from audioPath
// and stores them. Silent or empty audio gives an empty, non-nil slice.
func (c PeakCache) Get(id, audioPath string) ([]float32, error) {
	path, err := c.path(id)
	if err != nil {
		return nil, err
	}
	if data, err := os.ReadFile(path); err == nil && (len(data) == 4*c.N || len(data) == 0) {
		p := make([]float32, len(data)/4)
		for i := range p {
			p[i] = math.Float32frombits(binary.LittleEndian.Uint32(data[4*i:]))
		}
		return p, nil
	}
	p, err := Peaks(audioPath, c.N)
	if err != nil {
		return nil, err
	}
	if p == nil {
		p = []float32{}
	}
	data := make([]byte, 4*len(p))
	for i, v := range p {
		binary.LittleEndian.PutUint32(data[4*i:], math.Float32bits(v))
	}
	// A failed write only costs a decode next time.
	if err := os.MkdirAll(c.Dir, 0o755); err == nil {
		if tmp, err := os.CreateTemp(c.Dir, ".peaks-*"); err == nil {
			_, werr := tmp.Write(data)
			if cerr := tmp.Close(); werr == nil && cerr == nil {
				if os.Rename(tmp.Name(), path) != nil {
					os.Remove(tmp.Name())
				}
			} else {
				os.Remove(tmp.Name())
			}
		}
	}
	return p, nil
}

// Remove deletes id's cached peaks, if any.
func (c PeakCache) Remove(id string) error {
	path, err := c.path(id)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}
