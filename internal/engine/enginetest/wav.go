package enginetest

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
)

// Audio is decoded PCM: interleaved samples in [-1, 1].
type Audio struct {
	Rate     int
	Channels int
	Samples  []float64 // len = frames × channels
	Bits     int       // of the file it was read from (ReadWAV); 0 otherwise
	Float    bool      // the file held IEEE floats
}

// Frames is the number of sample frames.
func (a Audio) Frames() int {
	if a.Channels == 0 {
		return 0
	}
	return len(a.Samples) / a.Channels
}

// Seconds is the duration.
func (a Audio) Seconds() float64 {
	if a.Rate == 0 {
		return 0
	}
	return float64(a.Frames()) / float64(a.Rate)
}

// Mono mixes down to one channel.
func (a Audio) Mono() []float64 {
	if a.Channels <= 1 {
		return a.Samples
	}
	out := make([]float64, a.Frames())
	for i := range out {
		var sum float64
		for c := range a.Channels {
			sum += a.Samples[i*a.Channels+c]
		}
		out[i] = sum / float64(a.Channels)
	}
	return out
}

// Tone makes seconds of a sine at hz, amplitude amp (0–1).
func Tone(rate, channels int, seconds, hz, amp float64) Audio {
	n := int(math.Round(seconds * float64(rate)))
	a := Audio{Rate: rate, Channels: channels, Samples: make([]float64, n*channels)}
	for i := range n {
		v := amp * math.Sin(2*math.Pi*hz*float64(i)/float64(rate))
		for c := range channels {
			a.Samples[i*channels+c] = v
		}
	}
	return a
}

// Convert resamples (linear) and remixes a to rate and channels (0 keeps).
func Convert(a Audio, rate, channels int) Audio {
	if rate <= 0 {
		rate = a.Rate
	}
	if channels <= 0 {
		channels = a.Channels
	}
	mono := a.Mono()
	n := a.Frames()
	if rate != a.Rate && a.Rate > 0 {
		n = int(math.Round(float64(a.Frames()) * float64(rate) / float64(a.Rate)))
	}
	out := Audio{Rate: rate, Channels: channels, Samples: make([]float64, n*channels)}
	for i := range n {
		for c := range channels {
			var v float64
			src := float64(i) * float64(a.Rate) / float64(rate)
			j := int(src)
			frac := src - float64(j)
			at := func(k int) float64 {
				if k >= a.Frames() {
					k = a.Frames() - 1
				}
				if k < 0 {
					return 0
				}
				if channels == a.Channels {
					return a.Samples[k*a.Channels+c]
				}
				return mono[k]
			}
			v = at(j)*(1-frac) + at(j+1)*frac
			out.Samples[i*channels+c] = v
		}
	}
	return out
}

// Cut returns the window [start, start+dur) in seconds; dur 0 = to the end.
func Cut(a Audio, start, dur float64) Audio {
	from := min(max(int(start*float64(a.Rate)), 0), a.Frames())
	to := a.Frames()
	if dur > 0 {
		to = min(from+int(math.Round(dur*float64(a.Rate))), a.Frames())
	}
	return Audio{Rate: a.Rate, Channels: a.Channels, Samples: a.Samples[from*a.Channels : to*a.Channels]}
}

// WriteWAV writes a as a WAV file of bits 16 (default), 24 or 32 (float),
// through a temporary file renamed into place, as the engine does.
func WriteWAV(path string, a Audio, bits int) error {
	if bits == 0 {
		bits = 16
	}
	if bits != 16 && bits != 24 && bits != 32 {
		return fmt.Errorf("bits %d", bits)
	}
	bps := bits / 8
	data := make([]byte, len(a.Samples)*bps)
	for i, s := range a.Samples {
		s = max(-1, min(1, s))
		switch bits {
		case 16:
			binary.LittleEndian.PutUint16(data[i*2:], uint16(int16(math.Round(s*32767))))
		case 24:
			v := int32(math.Round(s * 8388607))
			data[i*3], data[i*3+1], data[i*3+2] = byte(v), byte(v>>8), byte(v>>16)
		case 32:
			binary.LittleEndian.PutUint32(data[i*4:], math.Float32bits(float32(s)))
		}
	}
	format := uint16(1)
	if bits == 32 {
		format = 3
	}
	h := make([]byte, 44)
	copy(h[0:], "RIFF")
	binary.LittleEndian.PutUint32(h[4:], uint32(36+len(data)))
	copy(h[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(h[16:], 16)
	binary.LittleEndian.PutUint16(h[20:], format)
	binary.LittleEndian.PutUint16(h[22:], uint16(a.Channels))
	binary.LittleEndian.PutUint32(h[24:], uint32(a.Rate))
	binary.LittleEndian.PutUint32(h[28:], uint32(a.Rate*a.Channels*bps))
	binary.LittleEndian.PutUint16(h[32:], uint16(a.Channels*bps))
	binary.LittleEndian.PutUint16(h[34:], uint16(bits))
	copy(h[36:], "data")
	binary.LittleEndian.PutUint32(h[40:], uint32(len(data)))
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	_, err = tmp.Write(append(h, data...))
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp.Name(), path)
	}
	if err != nil {
		os.Remove(tmp.Name())
	}
	return err
}

var errNotWAV = errors.New("not a WAV file")

// ReadWAV reads PCM 8/16/24/32-bit integer or 32/64-bit float WAV files,
// including WAVE_FORMAT_EXTENSIBLE.
func ReadWAV(path string) (Audio, error) {
	f, err := os.Open(path)
	if err != nil {
		return Audio{}, err
	}
	defer f.Close()
	var hdr [12]byte
	if _, err := io.ReadFull(f, hdr[:]); err != nil || string(hdr[0:4]) != "RIFF" || string(hdr[8:12]) != "WAVE" {
		return Audio{}, errNotWAV
	}
	var format, channels, bits uint16
	var rate uint32
	for {
		var ch [8]byte
		if _, err := io.ReadFull(f, ch[:]); err != nil {
			return Audio{}, errNotWAV
		}
		size := binary.LittleEndian.Uint32(ch[4:])
		body := make([]byte, size+size%2)
		if _, err := io.ReadFull(f, body); err != nil && string(ch[0:4]) != "data" {
			return Audio{}, errNotWAV
		}
		body = body[:size]
		switch string(ch[0:4]) {
		case "fmt ":
			if size < 16 {
				return Audio{}, errNotWAV
			}
			format = binary.LittleEndian.Uint16(body[0:])
			channels = binary.LittleEndian.Uint16(body[2:])
			rate = binary.LittleEndian.Uint32(body[4:])
			bits = binary.LittleEndian.Uint16(body[14:])
			if format == 0xFFFE && size >= 26 {
				format = binary.LittleEndian.Uint16(body[24:])
			}
		case "data":
			if channels == 0 || bits == 0 {
				return Audio{}, errNotWAV
			}
			bps := int(bits) / 8
			n := len(body) / bps
			a := Audio{Rate: int(rate), Channels: int(channels), Samples: make([]float64, n-n%int(channels)), Bits: int(bits), Float: format == 3}
			for i := range a.Samples {
				b := body[i*bps:]
				switch {
				case format == 1 && bits == 8:
					a.Samples[i] = (float64(b[0]) - 128) / 128
				case format == 1 && bits == 16:
					a.Samples[i] = float64(int16(binary.LittleEndian.Uint16(b))) / 32768
				case format == 1 && bits == 24:
					v := int32(uint32(b[0])|uint32(b[1])<<8|uint32(b[2])<<16) << 8 >> 8
					a.Samples[i] = float64(v) / 8388608
				case format == 1 && bits == 32:
					a.Samples[i] = float64(int32(binary.LittleEndian.Uint32(b))) / 2147483648
				case format == 3 && bits == 32:
					a.Samples[i] = float64(math.Float32frombits(binary.LittleEndian.Uint32(b)))
				case format == 3 && bits == 64:
					a.Samples[i] = math.Float64frombits(binary.LittleEndian.Uint64(b))
				default:
					return Audio{}, fmt.Errorf("WAV format %d, %d bits: %w", format, bits, errNotWAV)
				}
			}
			return a, nil
		}
	}
}

// levels returns the RMS and peak of samples in dBFS, floored at -120.
func levels(s []float64) (rms, peak float64) {
	var sum, pk float64
	for _, v := range s {
		sum += v * v
		pk = max(pk, math.Abs(v))
	}
	db := func(x float64) float64 {
		if x <= 1e-6 {
			return -120
		}
		return max(-120, 20*math.Log10(x))
	}
	if len(s) == 0 {
		return -120, -120
	}
	return db(math.Sqrt(sum / float64(len(s)))), db(pk)
}
