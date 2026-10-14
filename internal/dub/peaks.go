package dub

import (
	"encoding/binary"
	"errors"
	"io"
	"math"
	"os"
)

// Peaks is a waveform pyramid for the timeline: the loudest sample of
// every 1/PerSecond s (level 0, as 0–255 on a square-root scale, so quiet
// speech still shows), and coarser levels that halve it, for zooming
// out without reading every bucket.
type Peaks struct {
	PerSecond int
	Levels    [][]uint8
}

// PeaksPerSecond is the finest resolution kept (10 ms).
const PeaksPerSecond = 100

// PeakBuilder accumulates samples into level 0 of a pyramid.
type PeakBuilder struct {
	rate, ch int
	per      int // frames per bucket
	n        int // frames in the current bucket
	cur      float32
	out      []uint8
}

// NewPeakBuilder makes a builder for audio at rate with channels.
func NewPeakBuilder(rate, channels int) *PeakBuilder {
	return &PeakBuilder{rate: rate, ch: max(channels, 1), per: max(rate/PeaksPerSecond, 1)}
}

// Write adds interleaved samples.
func (b *PeakBuilder) Write(samples []float32) {
	for i := 0; i+b.ch <= len(samples); i += b.ch {
		for c := range b.ch {
			v := samples[i+c]
			if v < 0 {
				v = -v
			}
			if v > b.cur {
				b.cur = v
			}
		}
		b.n++
		if b.n == b.per {
			b.out = append(b.out, peakByte(b.cur))
			b.n, b.cur = 0, 0
		}
	}
}

func peakByte(v float32) uint8 {
	return uint8(math.Round(math.Sqrt(float64(min(v, 1))) * 255))
}

// Peaks finishes the pyramid.
func (b *PeakBuilder) Peaks() *Peaks {
	base := b.out
	if b.n > 0 {
		base = append(base, peakByte(b.cur))
	}
	return pyramid(PeaksPerSecond, base)
}

func pyramid(perSecond int, base []uint8) *Peaks {
	p := &Peaks{PerSecond: perSecond, Levels: [][]uint8{base}}
	for cur := base; len(cur) > 1; {
		next := make([]uint8, (len(cur)+1)/2)
		for i := range next {
			v := cur[2*i]
			if 2*i+1 < len(cur) {
				v = max(v, cur[2*i+1])
			}
			next[i] = v
		}
		p.Levels = append(p.Levels, next)
		cur = next
	}
	return p
}

// Seconds is the length the pyramid covers.
func (p *Peaks) Seconds() float64 {
	if p == nil || len(p.Levels) == 0 {
		return 0
	}
	return float64(len(p.Levels[0])) / float64(p.PerSecond)
}

// Bars returns n values (0–1) for from–to seconds: each the loudest
// peak in its share, from the coarsest level that still has a bucket per
// bar.
func (p *Peaks) Bars(from, to float64, n int) []float32 {
	out := make([]float32, max(n, 0))
	if p == nil || len(p.Levels) == 0 || n <= 0 || to <= from {
		return out
	}
	span := (to - from) / float64(n) // seconds per bar
	lvl := 0
	for lvl+1 < len(p.Levels) && span >= float64(int(1)<<(lvl+1))/float64(p.PerSecond) {
		lvl++
	}
	level := p.Levels[lvl]
	per := float64(p.PerSecond) / float64(int(1)<<lvl) // buckets per second at lvl
	for i := range out {
		a := int((from + span*float64(i)) * per)
		b := max(int((from+span*float64(i+1))*per), a+1)
		var v uint8
		for k := max(a, 0); k < min(b, len(level)); k++ {
			v = max(v, level[k])
		}
		out[i] = float32(v) / 255
	}
	return out
}

// WritePeaks saves level 0 (the others are rebuilt when read).
func WritePeaks(path string, p *Peaks) error {
	tmp := path + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	var hdr [8]byte
	copy(hdr[:4], "ASPK")
	binary.LittleEndian.PutUint32(hdr[4:], uint32(p.PerSecond))
	_, err = f.Write(hdr[:])
	if err == nil && len(p.Levels) > 0 {
		_, err = f.Write(p.Levels[0])
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, path)
}

// ReadPeaks reads a pyramid WritePeaks saved.
func ReadPeaks(path string) (*Peaks, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var hdr [8]byte
	if _, err := io.ReadFull(f, hdr[:]); err != nil || string(hdr[:4]) != "ASPK" {
		return nil, errors.New("dub: not a peaks file")
	}
	base, err := io.ReadAll(f)
	if err != nil {
		return nil, err
	}
	per := int(binary.LittleEndian.Uint32(hdr[4:]))
	if per <= 0 {
		per = PeaksPerSecond
	}
	return pyramid(per, base), nil
}
