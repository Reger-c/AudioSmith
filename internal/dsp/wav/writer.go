package wav

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"

	"github.com/hehehai/AudioSmith/internal/dsp"
)

// Options tune a Writer.
type Options struct {
	// Dither adds TPDF dither of ±1 LSB before rounding to 16 or 24 bits
	// (from a fixed seed, so output is reproducible). Off by default.
	Dither bool
	// Large reserves a 36-byte JUNK chunk so the file can become RF64 on
	// Close if its data passes 4 GiB. Off by default: the header is then the
	// canonical 44 bytes and more than 4 GiB is ErrTooLarge.
	// (mygo/audio cannot play RF64; files under 4 GiB stay plain RIFF.)
	Large bool
}

const maxRIFF = 0xffffffff

// limit is the largest RIFF size before RF64 (a variable for tests).
var limit int64 = maxRIFF

// Writer streams samples into a WAV file. Close patches the header.
type Writer struct {
	ws     io.WriteSeeker
	bw     *bufio.Writer
	f      Format
	opt    Options
	start  int64 // offset of the RIFF header
	hdr    int64 // header bytes before the data
	data   int64 // data bytes written
	buf    []byte
	rng    uint64
	err    error
	closed bool
}

// NewWriter writes a header for f to ws and returns a Writer positioned at
// the data. ws must support seeking back on Close.
func NewWriter(ws io.WriteSeeker, f Format, opt Options) (*Writer, error) {
	if err := f.check(true); err != nil {
		return nil, err
	}
	start, err := ws.Seek(0, io.SeekCurrent)
	if err != nil {
		return nil, err
	}
	w := &Writer{ws: ws, bw: bufio.NewWriterSize(ws, 256<<10), f: f, opt: opt, start: start, rng: 0x853c49e6748fea9b}
	h := header(f, 0, opt.Large)
	w.hdr = int64(len(h))
	if _, err := w.bw.Write(h); err != nil {
		return nil, err
	}
	return w, nil
}

// header returns the RIFF header for dataBytes of data (sizes clamped for a
// streaming header). With large it includes a JUNK chunk sized for ds64.
func header(f Format, dataBytes int64, large bool) []byte {
	var h []byte
	le := binary.LittleEndian
	h = append(h, "RIFF\x00\x00\x00\x00WAVE"...)
	if large {
		h = append(h, "JUNK"...)
		h = le.AppendUint32(h, 28)
		h = append(h, make([]byte, 28)...)
	}
	tag := uint16(tagPCM)
	if f.Encoding == Float32 {
		tag = tagFloat
	}
	h = append(h, "fmt "...)
	h = le.AppendUint32(h, 16)
	h = le.AppendUint16(h, tag)
	h = le.AppendUint16(h, uint16(f.Channels))
	h = le.AppendUint32(h, uint32(f.Rate))
	h = le.AppendUint32(h, uint32(f.BytesPerSecond()))
	h = le.AppendUint16(h, uint16(f.FrameBytes()))
	h = le.AppendUint16(h, uint16(f.Encoding.Bits()))
	h = append(h, "data"...)
	h = le.AppendUint32(h, uint32(min(dataBytes, maxRIFF)))
	riff := int64(len(h)) - 8 + dataBytes + dataBytes&1
	le.PutUint32(h[4:], uint32(min(riff, maxRIFF)))
	return h
}

// Format returns the format being written.
func (w *Writer) Format() Format { return w.f }

// Frames returns the frames written so far.
func (w *Writer) Frames() int64 { return w.data / int64(w.f.FrameBytes()) }

// Write encodes interleaved samples; len(samples) must be a whole number of frames.
func (w *Writer) Write(samples []float32) error {
	if w.err != nil {
		return w.err
	}
	if w.closed {
		return errors.New("wav: write after close")
	}
	if len(samples)%w.f.Channels != 0 {
		return fmt.Errorf("wav: %d samples is not a whole number of %d-channel frames", len(samples), w.f.Channels)
	}
	bs := w.f.Encoding.Bits() / 8
	n := int64(len(samples) * bs)
	if !w.opt.Large && w.hdr-8+w.data+n+1 > limit {
		w.err = ErrTooLarge
		return w.err
	}
	const chunk = 8192
	for len(samples) > 0 {
		s := samples[:min(len(samples), chunk)]
		samples = samples[len(s):]
		w.buf = w.encode(w.buf[:0], s)
		if _, err := w.bw.Write(w.buf); err != nil {
			w.err = err
			return err
		}
		w.data += int64(len(w.buf))
	}
	return nil
}

func (w *Writer) noise() float64 {
	// xorshift64*: two uniforms make a triangular PDF in (−1, 1).
	next := func() float64 {
		w.rng ^= w.rng >> 12
		w.rng ^= w.rng << 25
		w.rng ^= w.rng >> 27
		return float64((w.rng*0x2545f4914f6cdd1d)>>11) / (1 << 53)
	}
	return next() - next()
}

func (w *Writer) encode(b []byte, s []float32) []byte {
	le := binary.LittleEndian
	switch w.f.Encoding {
	case Float32:
		for _, v := range s {
			b = le.AppendUint32(b, math.Float32bits(v))
		}
	case PCM16:
		for _, v := range s {
			x := float64(v) * (1 << 15)
			if w.opt.Dither {
				x += w.noise()
			}
			b = le.AppendUint16(b, uint16(int16(clampRound(x, 1<<15))))
		}
	case PCM24:
		for _, v := range s {
			x := float64(v) * (1 << 23)
			if w.opt.Dither {
				x += w.noise()
			}
			i := uint32(clampRound(x, 1<<23))
			b = append(b, byte(i), byte(i>>8), byte(i>>16))
		}
	}
	return b
}

// clampRound rounds x to an integer in [−full, full−1]; NaN becomes 0.
func clampRound(x float64, full float64) int32 {
	if x != x {
		return 0
	}
	x = math.Round(x)
	if x >= full {
		return int32(full - 1)
	}
	if x < -full {
		return int32(-full)
	}
	return int32(x)
}

// Close flushes, pads and patches the header (becoming RF64 if Large and
// over 4 GiB). It does not close the underlying writer.
func (w *Writer) Close() error {
	if w.closed {
		return w.err
	}
	w.closed = true
	if w.err != nil {
		return w.err
	}
	if w.data&1 == 1 {
		if err := w.bw.WriteByte(0); err != nil {
			w.err = err
			return err
		}
	}
	if err := w.bw.Flush(); err != nil {
		w.err = err
		return err
	}
	end, err := w.ws.Seek(0, io.SeekCurrent)
	if err != nil {
		w.err = err
		return err
	}
	h := header(w.f, w.data, w.opt.Large)
	if w.opt.Large && w.hdr-8+w.data+w.data&1 > limit {
		le := binary.LittleEndian
		copy(h, "RF64")
		le.PutUint32(h[4:], maxRIFF)
		copy(h[12:], "ds64")
		le.PutUint64(h[20:], uint64(w.hdr-8+w.data+w.data&1))
		le.PutUint64(h[28:], uint64(w.data))
		le.PutUint64(h[36:], uint64(w.Frames()))
		le.PutUint32(h[44:], 0)
		le.PutUint32(h[len(h)-4:], maxRIFF)
	}
	if _, err := w.ws.Seek(w.start, io.SeekStart); err != nil {
		w.err = err
		return err
	}
	if _, err := w.ws.Write(h); err != nil {
		w.err = err
		return err
	}
	if _, err := w.ws.Seek(end, io.SeekStart); err != nil {
		w.err = err
		return err
	}
	return nil
}

// Encode writes b as a complete WAV stream to w (no seeking needed).
func Encode(w io.Writer, b dsp.Buffer, e Encoding) error {
	if err := b.Check(); err != nil {
		return err
	}
	f := Format{Rate: b.Rate, Channels: b.Channels, Encoding: e}
	if err := f.check(true); err != nil {
		return err
	}
	data := int64(len(b.Samples) * e.Bits() / 8)
	if 36+data+1 > limit {
		return ErrTooLarge
	}
	bw := bufio.NewWriterSize(w, 256<<10)
	if _, err := bw.Write(header(f, data, false)); err != nil {
		return err
	}
	enc := &Writer{f: f}
	var buf []byte
	for s := b.Samples; len(s) > 0; {
		part := s[:min(len(s), 8192)]
		s = s[len(part):]
		buf = enc.encode(buf[:0], part)
		if _, err := bw.Write(buf); err != nil {
			return err
		}
	}
	if data&1 == 1 {
		bw.WriteByte(0)
	}
	return bw.Flush()
}

// FileWriter is a Writer onto a temporary file that Close renames to its
// final path. Abort (or a failed Close) removes the temporary file.
type FileWriter struct {
	*Writer
	path string
	tmp  *os.File
	done bool
}

// Create starts writing a WAV file at path, atomically: the file appears
// only when Close succeeds. The directory must exist.
func Create(path string, f Format, opt Options) (*FileWriter, error) {
	if err := f.check(true); err != nil {
		return nil, err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return nil, err
	}
	w, err := NewWriter(tmp, f, opt)
	if err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return nil, err
	}
	return &FileWriter{Writer: w, path: path, tmp: tmp}, nil
}

// Path returns the final path.
func (fw *FileWriter) Path() string { return fw.path }

// Close finishes the file, syncs it and renames it into place.
func (fw *FileWriter) Close() error {
	if fw.done {
		return nil
	}
	fw.done = true
	err := fw.Writer.Close()
	if err == nil {
		err = fw.tmp.Sync()
	}
	if cerr := fw.tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Chmod(fw.tmp.Name(), 0o644)
	}
	if err == nil {
		err = os.Rename(fw.tmp.Name(), fw.path)
	}
	if err != nil {
		os.Remove(fw.tmp.Name())
	}
	return err
}

// Abort discards the file. It is a no-op after Close.
func (fw *FileWriter) Abort() {
	if fw.done {
		return
	}
	fw.done = true
	fw.tmp.Close()
	os.Remove(fw.tmp.Name())
}

// WriteFile writes b to path atomically in encoding e.
func WriteFile(path string, b dsp.Buffer, e Encoding, opt Options) error {
	if err := b.Check(); err != nil {
		return err
	}
	fw, err := Create(path, Format{Rate: b.Rate, Channels: b.Channels, Encoding: e}, opt)
	if err != nil {
		return err
	}
	if err := fw.Write(b.Samples); err != nil {
		fw.Abort()
		return err
	}
	return fw.Close()
}

// WriteFileAtomic creates path by calling fn on a temporary file in the same
// directory, then syncing it and renaming it into place with mode perm. On
// any error the temporary file is removed and path is untouched.
func WriteFileAtomic(path string, perm os.FileMode, fn func(f *os.File) error) (err error) {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			tmp.Close()
			os.Remove(tmp.Name())
		}
	}()
	if err = fn(tmp); err != nil {
		return err
	}
	if err = tmp.Sync(); err != nil {
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	if err = os.Chmod(tmp.Name(), perm); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
