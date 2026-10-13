package wav

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"math"
	"os"
	"path/filepath"
	"testing"

	myaudio "github.com/egoist/mygo/audio"

	"github.com/hehehai/AudioSmith/internal/dsp"
	"github.com/hehehai/AudioSmith/internal/dsp/dsptest"
)

// seekBuf is an in-memory io.WriteSeeker.
type seekBuf struct {
	b   []byte
	pos int
}

func (s *seekBuf) Write(p []byte) (int, error) {
	if need := s.pos + len(p); need > len(s.b) {
		s.b = append(s.b, make([]byte, need-len(s.b))...)
	}
	copy(s.b[s.pos:], p)
	s.pos += len(p)
	return len(p), nil
}

func (s *seekBuf) Seek(off int64, whence int) (int64, error) {
	switch whence {
	case io.SeekStart:
		s.pos = int(off)
	case io.SeekCurrent:
		s.pos += int(off)
	case io.SeekEnd:
		s.pos = len(s.b) + int(off)
	}
	return int64(s.pos), nil
}

func maxErr(a, b []float32) float64 {
	var m float64
	for i := range a {
		m = max(m, math.Abs(float64(a[i]-b[i])))
	}
	return m
}

func TestRoundTrip(t *testing.T) {
	signals := map[string]dsp.Buffer{
		"sine-mono-24k":    dsptest.Sine(24000, 1, 440, -1, 0.3, 0),
		"sine-stereo-48k":  dsptest.Sine(48000, 2, 1000, -6, 0.25, 0.3),
		"noise-stereo-44k": dsptest.Noise(44100, 2, -20, 0.2, 1),
		"silence-mono":     dsptest.Silence(16000, 1, 0.1),
		"odd-frames-mono":  dsptest.Sine(22050, 1, 300, -3, 1001.0/22050, 0), // odd byte count at 24-bit
		"empty":            {Rate: 48000, Channels: 2},
	}
	tol := map[Encoding]float64{PCM16: 1.0 / 32768, PCM24: 1.0 / (1 << 23), Float32: 0}
	for name, b := range signals {
		for _, e := range []Encoding{PCM16, PCM24, Float32} {
			t.Run(name+"/"+e.String(), func(t *testing.T) {
				var buf bytes.Buffer
				if err := Encode(&buf, b, e); err != nil {
					t.Fatal(err)
				}
				want := 44 + len(b.Samples)*e.Bits()/8
				want += want & 1
				if buf.Len() != want {
					t.Errorf("size %d, want %d", buf.Len(), want)
				}
				got, f, err := Decode(bytes.NewReader(buf.Bytes()))
				if err != nil {
					t.Fatal(err)
				}
				if f != (Format{b.Rate, b.Channels, e}) || got.Rate != b.Rate || got.Channels != b.Channels {
					t.Fatalf("format %+v", f)
				}
				if len(got.Samples) != len(b.Samples) {
					t.Fatalf("samples %d, want %d", len(got.Samples), len(b.Samples))
				}
				if m := maxErr(got.Samples, b.Samples); m > tol[e]*0.5+1e-12 {
					t.Errorf("max error %g > half LSB %g", m, tol[e]/2)
				}
				// mygo/audio (the player) must decode it the same.
				d, _, err := myaudio.NewDecoder(bytes.NewReader(buf.Bytes()))
				if err != nil {
					t.Fatal(err)
				}
				my := make([]float32, len(b.Samples)+8)
				k := 0
				for k < len(b.Samples) {
					m, err := d.Read(my[k:])
					k += m
					if err != nil || m == 0 {
						break
					}
				}
				if k != len(b.Samples) || maxErr(my[:k], got.Samples) != 0 {
					t.Errorf("mygo decoded %d samples, differs from ours", k)
				}
			})
		}
	}
}

func TestClipAndNaN(t *testing.T) {
	b := dsp.Buffer{Rate: 8000, Channels: 1, Samples: []float32{2, -2, float32(math.NaN()), 1, -1}}
	var buf bytes.Buffer
	if err := Encode(&buf, b, PCM16); err != nil {
		t.Fatal(err)
	}
	got, _, _ := Decode(&buf)
	want := []float32{32767.0 / 32768, -1, 0, 32767.0 / 32768, -1}
	for i := range want {
		if got.Samples[i] != want[i] {
			t.Errorf("sample %d = %v, want %v", i, got.Samples[i], want[i])
		}
	}
}

func TestStreamingWriter(t *testing.T) {
	src := dsptest.Noise(48000, 2, -12, 1, 7)
	for _, e := range []Encoding{PCM16, PCM24, Float32} {
		var sb seekBuf
		sb.Write([]byte("prefix")) // the writer starts at the current offset
		w, err := NewWriter(&sb, Format{48000, 2, e}, Options{})
		if err != nil {
			t.Fatal(err)
		}
		for s := src.Samples; len(s) > 0; {
			n := min(len(s), 2*977)
			if err := w.Write(s[:n]); err != nil {
				t.Fatal(err)
			}
			s = s[n:]
		}
		if err := w.Write([]float32{1}); err == nil {
			t.Error("a partial frame must be refused")
		}
		// Before Close the header says "streamed": readers read to EOF.
		mid, _, err := Decode(bytes.NewReader(append([]byte(nil), sb.b[6:]...)))
		if err == nil && len(mid.Samples) != 0 {
			// bufio may not have flushed; whatever is there must be whole frames
			if len(mid.Samples)%2 != 0 {
				t.Error("partial frame read")
			}
		}
		if err := w.Close(); err != nil {
			t.Fatal(err)
		}
		if w.Frames() != int64(src.Frames()) {
			t.Errorf("frames %d", w.Frames())
		}
		r, err := NewReader(bytes.NewReader(sb.b[6:]))
		if err != nil {
			t.Fatal(err)
		}
		if r.Frames() != int64(src.Frames()) {
			t.Errorf("%v: header frames %d, want %d", e, r.Frames(), src.Frames())
		}
		got, _, _ := Decode(bytes.NewReader(sb.b[6:]))
		if len(got.Samples) != len(src.Samples) {
			t.Fatalf("%v: %d samples", e, len(got.Samples))
		}
	}
}

func TestDither(t *testing.T) {
	// A constant between two 16-bit steps: rounding gives a constant error,
	// dither gives the right mean.
	b := dsp.Buffer{Rate: 8000, Channels: 1, Samples: make([]float32, 80000)}
	for i := range b.Samples {
		b.Samples[i] = 0.3 / 32768
	}
	var sb seekBuf
	w, _ := NewWriter(&sb, Format{8000, 1, PCM16}, Options{Dither: true})
	w.Write(b.Samples)
	w.Close()
	got, _, _ := Decode(bytes.NewReader(sb.b))
	var mean float64
	for _, v := range got.Samples {
		mean += float64(v) * 32768
	}
	mean /= float64(len(got.Samples))
	if math.Abs(mean-0.3) > 0.02 {
		t.Errorf("dithered mean %.3f LSB, want 0.3", mean)
	}
}

func TestChunksAndExtensible(t *testing.T) {
	le := binary.LittleEndian
	var h []byte
	h = append(h, "RIFF\x00\x00\x00\x00WAVE"...)
	h = append(h, "LIST"...)
	h = le.AppendUint32(h, 5) // odd: padded
	h = append(h, "INFO\x01\x00"...)
	h = append(h, "fmt "...)
	h = le.AppendUint32(h, 40)
	h = le.AppendUint16(h, tagExtensible)
	h = le.AppendUint16(h, 2)
	h = le.AppendUint32(h, 44100)
	h = le.AppendUint32(h, 44100*6)
	h = le.AppendUint16(h, 6)
	h = le.AppendUint16(h, 24)
	h = le.AppendUint16(h, 22)
	h = le.AppendUint16(h, 24)
	h = le.AppendUint32(h, 3)
	h = le.AppendUint16(h, tagPCM)
	h = append(h, make([]byte, 14)...)
	h = append(h, "data"...)
	h = le.AppendUint32(h, 12)
	h = append(h, 0, 0, 0x40, 0, 0, 0xc0, 0xff, 0xff, 0xff, 0x01, 0, 0) // 0.5, -0.5, -1/2^23, 1/2^23
	r, err := NewReader(bytes.NewReader(h))
	if err != nil {
		t.Fatal(err)
	}
	if r.Format() != (Format{44100, 2, PCM24}) || r.Frames() != 2 {
		t.Fatalf("format %+v frames %d", r.Format(), r.Frames())
	}
	buf := make([]float32, 8)
	n, _ := r.Read(buf)
	want := []float32{0.5, -0.5, -1.0 / (1 << 23), 1.0 / (1 << 23)}
	if n != 4 || maxErr(buf[:4], want) != 0 {
		t.Errorf("read %v", buf[:n])
	}
	for _, bad := range [][]byte{nil, []byte("RIFX0000WAVE"), h[:30]} {
		if _, err := NewReader(bytes.NewReader(bad)); !errors.Is(err, ErrInvalid) {
			t.Errorf("bad header: %v", err)
		}
	}
}

func TestLargeRF64(t *testing.T) {
	old := limit
	limit = 1000
	defer func() { limit = old }()
	src := dsptest.Noise(8000, 1, -10, 0.1, 3) // 800 frames f32 = 3200 bytes
	var sb seekBuf
	w, _ := NewWriter(&sb, Format{8000, 1, Float32}, Options{})
	if err := w.Write(src.Samples); !errors.Is(err, ErrTooLarge) {
		t.Errorf("without Large: %v", err)
	}
	sb = seekBuf{}
	w, _ = NewWriter(&sb, Format{8000, 1, Float32}, Options{Large: true})
	if err := w.Write(src.Samples); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if string(sb.b[:4]) != "RF64" || string(sb.b[12:16]) != "ds64" {
		t.Fatalf("header %q", sb.b[:16])
	}
	got, _, err := Decode(bytes.NewReader(sb.b))
	if err != nil || maxErr(got.Samples, src.Samples) != 0 || len(got.Samples) != len(src.Samples) {
		t.Errorf("RF64 round trip: %v, %d samples", err, len(got.Samples))
	}
	// Under the limit, Large stays RIFF with a JUNK chunk.
	limit = old
	sb = seekBuf{}
	w, _ = NewWriter(&sb, Format{8000, 1, Float32}, Options{Large: true})
	w.Write(src.Samples)
	w.Close()
	if string(sb.b[:4]) != "RIFF" || string(sb.b[12:16]) != "JUNK" {
		t.Errorf("small Large file header %q", sb.b[:16])
	}
	if _, _, err := Decode(bytes.NewReader(sb.b)); err != nil {
		t.Error(err)
	}
}

func TestAtomicFiles(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "take.wav")
	src := dsptest.Sine(24000, 1, 220, -3, 0.5, 0)
	if err := WriteFile(path, src, PCM24, Options{}); err != nil {
		t.Fatal(err)
	}
	got, f, err := ReadFile(path)
	if err != nil || f.Encoding != PCM24 || got.Frames() != src.Frames() {
		t.Fatalf("%v %+v %d", err, f, got.Frames())
	}
	// Abort leaves the old file and no temporary.
	fw, err := Create(path, Format{24000, 1, PCM16}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	fw.Write(src.Samples[:100])
	fw.Abort()
	got, f, _ = ReadFile(path)
	if f.Encoding != PCM24 || got.Frames() != src.Frames() {
		t.Error("abort replaced the file")
	}
	if ents, _ := os.ReadDir(dir); len(ents) != 1 {
		t.Errorf("leftover files: %v", ents)
	}
	// A failing fn leaves path untouched.
	err = WriteFileAtomic(path, 0o644, func(f *os.File) error {
		f.WriteString("junk")
		return errors.New("boom")
	})
	if err == nil {
		t.Error("want error")
	}
	if _, f, _ := ReadFile(path); f.Encoding != PCM24 {
		t.Error("failed atomic write changed the file")
	}
	if ents, _ := os.ReadDir(dir); len(ents) != 1 {
		t.Errorf("leftover files: %v", ents)
	}
}

func BenchmarkWritePCM24(b *testing.B) {
	src := dsptest.Noise(48000, 2, -12, 10, 1)
	b.SetBytes(int64(len(src.Samples) * 4))
	for b.Loop() {
		var sb seekBuf
		w, _ := NewWriter(&sb, Format{48000, 2, PCM24}, Options{})
		w.Write(src.Samples)
		w.Close()
	}
}

func BenchmarkReadPCM16(b *testing.B) {
	src := dsptest.Noise(48000, 2, -12, 10, 1)
	var buf bytes.Buffer
	Encode(&buf, src, PCM16)
	b.SetBytes(int64(len(src.Samples) * 4))
	for b.Loop() {
		Decode(bytes.NewReader(buf.Bytes()))
	}
}
