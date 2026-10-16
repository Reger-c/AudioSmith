package audio

import (
	"bytes"
	"io"
	"os"
	"time"

	myaudio "github.com/egoist/mygo/audio"
)

// fadeOut is how long a preview cut short fades out.
const fadeOut = 150 * time.Millisecond

// PlayPreview plays a short preview as id's audio, in a format MyGo's
// audio decodes (the app registers Ogg Vorbis), from data (a bundled clip) or, when data is nil, from the file at path, at most limit of it
// (0: all), fading out where it cuts. It replaces what was loaded; Stop
// ends it.
func (pl *Player) PlayPreview(id string, data []byte, path string, limit time.Duration) error {
	pl.Stop()
	var r io.ReadSeeker
	if data != nil {
		r = bytes.NewReader(data)
	} else {
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		r = f
	}
	d, _, err := myaudio.NewDecoder(r)
	if err != nil {
		if c, ok := r.(io.Closer); ok {
			c.Close()
		}
		return err
	}
	var c io.Closer
	if cl, ok := r.(io.Closer); ok {
		c = cl
	}
	pl.id = id
	pl.p = myaudio.NewPlayer(newLimited(d, c, limit))
	pl.p.Play()
	return nil
}

// limited is a decoder that ends after max frames (0: never), fading
// out over its last ones when it cuts the audio short.
type limited struct {
	myaudio.Decoder
	c         io.Closer
	pos, max  int64
	fade      int64
	shortened bool
}

func newLimited(d myaudio.Decoder, c io.Closer, limit time.Duration) *limited {
	l := &limited{Decoder: d, c: c}
	if limit > 0 {
		l.max = int64(limit.Seconds() * float64(d.SampleRate()))
		n := d.Len()
		l.shortened = n < 0 || n > l.max
		l.fade = int64(fadeOut.Seconds() * float64(d.SampleRate()))
	}
	return l
}

func (l *limited) Read(buf []float32) (int, error) {
	ch := max(l.Decoder.Channels(), 1)
	if l.max > 0 {
		left := l.max - l.pos
		if left <= 0 {
			return 0, io.EOF
		}
		if int64(len(buf)/ch) > left {
			buf = buf[:left*int64(ch)]
		}
	}
	n, err := l.Decoder.Read(buf)
	frames := int64(n / ch)
	if l.max > 0 && l.shortened {
		start := l.max - l.fade
		for f := range frames {
			if at := l.pos + f; at >= start {
				g := float32(l.max-at) / float32(max(l.fade, 1))
				for j := range ch {
					buf[int(f)*ch+j] *= g
				}
			}
		}
	}
	l.pos += frames
	if l.max > 0 && l.pos >= l.max && err == nil {
		err = io.EOF
	}
	return n, err
}

func (l *limited) SetPosition(frame int64) error {
	if l.max > 0 {
		frame = min(frame, l.max)
	}
	l.pos = frame
	return l.Decoder.SetPosition(frame)
}

func (l *limited) Len() int64 {
	n := l.Decoder.Len()
	if l.max > 0 && (n < 0 || n > l.max) {
		return l.max
	}
	return n
}

// Close closes the file the audio comes from, if any.
func (l *limited) Close() error {
	if l.c != nil {
		return l.c.Close()
	}
	return nil
}
