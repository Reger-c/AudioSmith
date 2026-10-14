package dub

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"os"
	"slices"
	"sort"

	"github.com/hehehai/AudioSmith/internal/dsp"
	"github.com/hehehai/AudioSmith/internal/dsp/mix"
	"github.com/hehehai/AudioSmith/internal/dsp/resample"
	"github.com/hehehai/AudioSmith/internal/dsp/wav"
)

// The dubbed track's mix, streamed: the background (the separated music
// and effects, or the original audio lowered under the dub when it could
// not be separated) plus every rendered line at its place, with its
// volume and short fades. TrackMixer renders any stretch on demand and
// seeks, so the same code writes the track and plays it in the A/B
// player while lines are edited (it is a mygo/audio Decoder). Lines are
// read when the playhead reaches them and dropped after, so memory stays
// small however long the source.

// Ducking of the original audio when there is no separated background.
const (
	DuckOriginalDB = -18 // the original's voices must not compete with the dub
	DuckStemDB     = -3  // a little room for the dub over the music
)

// LineFade is the fade at each end of a line, seconds.
const LineFade = 0.01

// MixLine is a rendered line on the track.
type MixLine struct {
	ID     string
	Start  float64 // seconds
	Length float64 // seconds (to place it before its audio is read)
	Volume float64 // 0–2
	// Load reads the line's audio (any rate and channels).
	Load func() (dsp.Buffer, error)
}

// Background is the track under the lines: a seekable stream.
type Background interface {
	Read(dst []float32) (int, error)
	SeekFrame(frame int64) error
	Format() (rate, channels int)
	Close() error
}

// TrackMixer mixes a track. Not goroutine-safe.
type TrackMixer struct {
	rate, ch int
	frames   int64
	pos      int64
	bg       Background
	bgGain   float32
	env      *mix.Envelope // nil: the background is not ducked
	lines    []MixLine     // by start
	loaded   map[int]dsp.Buffer
	bgBuf    []float32
	gains    []float32
	// Err is the first error reading a line (the line is then silent).
	Err error
}

// MixOptions configure a TrackMixer.
type MixOptions struct {
	Rate, Channels int
	Seconds        float64 // the track's length
	Background     Background
	BackgroundDB   float64 // the background's gain
	// DuckDB lowers the background under the lines (0 = not at all).
	DuckDB float64
}

// NewTrackMixer prepares a mix of lines over o.Background.
func NewTrackMixer(lines []MixLine, o MixOptions) *TrackMixer {
	m := &TrackMixer{rate: o.Rate, ch: max(o.Channels, 1), bg: o.Background, loaded: map[int]dsp.Buffer{}}
	m.frames = int64(dsp.FramesAt(o.Seconds, o.Rate))
	m.bgGain = float32(dsp.Amp(o.BackgroundDB))
	m.lines = slices.Clone(lines)
	sort.SliceStable(m.lines, func(i, j int) bool { return m.lines[i].Start < m.lines[j].Start })
	for _, l := range m.lines {
		m.frames = max(m.frames, int64(dsp.FramesAt(l.Start+l.Length, o.Rate)))
	}
	if o.DuckDB < 0 && o.Background != nil {
		var regions []mix.Span
		for _, l := range m.lines {
			if l.Volume > 0 {
				regions = append(regions, mix.Span{Start: dsp.FramesAt(l.Start, o.Rate), End: dsp.FramesAt(l.Start+l.Length, o.Rate)})
			}
		}
		m.env = mix.NewEnvelope(o.Rate, regions, mix.Duck{DepthDB: o.DuckDB})
	}
	return m
}

// SampleRate, Channels, Len and SetPosition make TrackMixer a mygo/audio
// Decoder.
func (m *TrackMixer) SampleRate() int { return m.rate }
func (m *TrackMixer) Channels() int   { return m.ch }
func (m *TrackMixer) Len() int64      { return m.frames }

// SetPosition moves the mix to frame.
func (m *TrackMixer) SetPosition(frame int64) error {
	m.pos = min(max(frame, 0), m.frames)
	if m.bg != nil {
		return m.bg.SeekFrame(m.pos)
	}
	return nil
}

// Position is the next frame Read renders.
func (m *TrackMixer) Position() int64 { return m.pos }

// Close closes the background.
func (m *TrackMixer) Close() error {
	if m.bg != nil {
		return m.bg.Close()
	}
	return nil
}

// Read renders the next frames into dst.
func (m *TrackMixer) Read(dst []float32) (int, error) {
	n := len(dst) / m.ch
	n = int(min(int64(n), m.frames-m.pos))
	if n <= 0 {
		return 0, io.EOF
	}
	out := dst[:n*m.ch]
	clear(out)
	from := int(m.pos)
	// The background.
	if m.bg != nil {
		if cap(m.bgBuf) < len(out) {
			m.bgBuf = make([]float32, len(out))
		}
		b := m.bgBuf[:len(out)]
		got := 0
		for got < len(b) {
			k, err := m.bg.Read(b[got:])
			got += k
			if err != nil || k == 0 {
				break
			}
		}
		clear(b[got:])
		if m.env != nil {
			if cap(m.gains) < n {
				m.gains = make([]float32, n)
			}
			g := m.gains[:n]
			m.env.Gains(g, from)
			for i := range n {
				gi := g[i] * m.bgGain
				for c := range m.ch {
					out[i*m.ch+c] = b[i*m.ch+c] * gi
				}
			}
		} else {
			for i, v := range b {
				out[i] = v * m.bgGain
			}
		}
	}
	// The lines.
	to := from + n
	for i, l := range m.lines {
		start := dsp.FramesAt(l.Start, m.rate)
		if start >= to {
			break
		}
		end := dsp.FramesAt(l.Start+l.Length, m.rate)
		if end <= from {
			delete(m.loaded, i)
			continue
		}
		buf, ok := m.loaded[i]
		if !ok {
			buf = m.load(l)
			m.loaded[i] = buf
		}
		lineEnd := start + buf.Frames()
		s, e := max(start, from), min(lineEnd, to)
		if e <= s || l.Volume <= 0 {
			continue
		}
		g := float32(l.Volume)
		src := buf.Samples[(s-start)*m.ch : (e-start)*m.ch]
		o := out[(s-from)*m.ch : (e-from)*m.ch]
		for k, v := range src {
			o[k] += v * g
		}
	}
	m.pos += int64(n)
	return len(out), nil
}

// load reads a line in the track's format, with its fades.
func (m *TrackMixer) load(l MixLine) dsp.Buffer {
	if l.Load == nil {
		return dsp.Buffer{Rate: m.rate, Channels: m.ch}
	}
	b, err := l.Load()
	if err != nil {
		if m.Err == nil {
			m.Err = err
		}
		return dsp.Buffer{Rate: m.rate, Channels: m.ch}
	}
	b = resample.Convert(b, m.rate, m.ch, resample.Good)
	fade := dsp.FramesAt(LineFade, m.rate)
	dsp.FadeIn(b, fade, dsp.EqualPower)
	dsp.FadeOut(b, fade, dsp.EqualPower)
	return b
}

// --- seekable WAV ----------------------------------------------------------------

// WAVSource reads a WAV file from any frame, converted to a rate and a
// channel count on the fly: a Background.
type WAVSource struct {
	f       *os.File
	hdr     []byte // the file's bytes up to its data
	format  wav.Format
	frames  int64
	rd      *wav.Reader
	rate    int
	ch      int
	rs      *resample.Resampler // nil when the rates match
	in, buf []float32
	pend    []float32 // converted samples not yet read
	eof     bool
}

// OpenWAV opens path for reading at rate and channels (0 keeps the file's).
func OpenWAV(path string, rate, channels int) (*WAVSource, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	hdr, err := wavHeader(f)
	if err != nil {
		f.Close()
		return nil, err
	}
	rd, err := wav.NewReader(io.MultiReader(bytes.NewReader(hdr), f))
	if err != nil {
		f.Close()
		return nil, err
	}
	s := &WAVSource{f: f, hdr: hdr, format: rd.Format(), frames: rd.Frames(), rd: rd, rate: rate, ch: channels}
	if s.rate <= 0 {
		s.rate = s.format.Rate
	}
	if s.ch <= 0 {
		s.ch = s.format.Channels
	}
	if s.frames < 0 {
		if fi, err := f.Stat(); err == nil {
			s.frames = (fi.Size() - int64(len(hdr))) / int64(s.format.FrameBytes())
		}
	}
	if s.rate != s.format.Rate {
		s.rs = resample.New(s.format.Rate, s.rate, s.ch, resample.Good)
	}
	return s, nil
}

// wavHeader returns the bytes of a WAV file up to the start of its data,
// leaving f there.
func wavHeader(f *os.File) ([]byte, error) {
	var h [12]byte
	if _, err := io.ReadFull(f, h[:]); err != nil {
		return nil, wav.ErrInvalid
	}
	if (string(h[:4]) != "RIFF" && string(h[:4]) != "RF64") || string(h[8:]) != "WAVE" {
		return nil, wav.ErrInvalid
	}
	out := slices.Clone(h[:])
	for range 64 {
		var ch [8]byte
		if _, err := io.ReadFull(f, ch[:]); err != nil {
			return nil, wav.ErrInvalid
		}
		out = append(out, ch[:]...)
		if string(ch[:4]) == "data" {
			return out, nil
		}
		size := int64(binary.LittleEndian.Uint32(ch[4:]))
		size += size & 1
		if size > 1<<20 {
			return nil, wav.ErrInvalid
		}
		body := make([]byte, size)
		if _, err := io.ReadFull(f, body); err != nil {
			return nil, wav.ErrInvalid
		}
		out = append(out, body...)
	}
	return nil, wav.ErrInvalid
}

// Format is the rate and channels Read returns.
func (s *WAVSource) Format() (int, int) { return s.rate, s.ch }

// Frames is the file's length in the output rate's frames.
func (s *WAVSource) Frames() int64 {
	return s.frames * int64(s.rate) / int64(max(s.format.Rate, 1))
}

// SeekFrame moves to frame (at the output rate).
func (s *WAVSource) SeekFrame(frame int64) error {
	src := frame * int64(s.format.Rate) / int64(max(s.rate, 1))
	src = min(max(src, 0), max(s.frames, 0))
	off := int64(len(s.hdr)) + src*int64(s.format.FrameBytes())
	if _, err := s.f.Seek(off, io.SeekStart); err != nil {
		return err
	}
	rd, err := wav.NewReader(io.MultiReader(bytes.NewReader(s.hdr), s.f))
	if err != nil {
		return err
	}
	s.rd, s.pend, s.eof = rd, s.pend[:0], false
	if s.rs != nil {
		s.rs.Reset()
	}
	return nil
}

// Read reads converted samples.
func (s *WAVSource) Read(dst []float32) (int, error) {
	dst = dst[:len(dst)/s.ch*s.ch]
	for len(s.pend) < len(dst) && !s.eof {
		need := 8192 * s.format.Channels
		if cap(s.in) < need {
			s.in = make([]float32, need)
		}
		k, err := s.rd.Read(s.in[:need])
		chunk := s.in[:k]
		if s.format.Channels != s.ch {
			chunk = dsp.Remix(dsp.Buffer{Rate: s.format.Rate, Channels: s.format.Channels, Samples: chunk}, s.ch).Samples
		}
		if s.rs != nil {
			s.buf = s.rs.Process(s.buf[:0], chunk)
			chunk = s.buf
		}
		s.pend = append(s.pend, chunk...)
		if err != nil || k == 0 {
			s.eof = true
			if s.rs != nil {
				s.buf = s.rs.Flush(s.buf[:0])
				s.pend = append(s.pend, s.buf...)
			}
		}
	}
	n := copy(dst, s.pend)
	s.pend = s.pend[:copy(s.pend, s.pend[n:])]
	if n == 0 {
		return 0, io.EOF
	}
	return n, nil
}

// Close closes the file.
func (s *WAVSource) Close() error { return s.f.Close() }

// ErrNoAudio is returned for a project without decoded audio.
var ErrNoAudio = errors.New("dub: the source has no decoded audio")
