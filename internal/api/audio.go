package api

import (
	"encoding/binary"
	"fmt"

	"github.com/hehehai/AudioSmith/internal/dsp"
	"github.com/hehehai/AudioSmith/internal/dsp/resample"
	"github.com/hehehai/AudioSmith/internal/dsp/wav"
)

// PCMRate is the rate of OpenAI's "pcm" format: 24 kHz, 16-bit signed
// little-endian, mono.
const PCMRate = 24000

// Response formats.
const (
	FormatWAV  = "wav"
	FormatPCM  = "pcm"
	FormatAAC  = "aac" // AAC-LC in an MP4 (M4A) container: media.encode
	FormatM4A  = "m4a"
	FormatMP3  = "mp3"
	FormatOpus = "opus"
	FormatFLAC = "flac"
)

// contentType is a format's MIME type.
func contentType(format string) string {
	switch format {
	case FormatWAV:
		return "audio/wav"
	case FormatPCM:
		return "audio/pcm"
	case FormatAAC, FormatM4A:
		return "audio/mp4"
	}
	return "application/octet-stream"
}

// wavStreamHeader is a WAV header for 16-bit PCM of unknown length (the
// sizes say "to the end of the stream", as streaming players expect).
func wavStreamHeader(rate, channels int) []byte {
	b := make([]byte, 0, 44)
	b = append(b, "RIFF"...)
	b = binary.LittleEndian.AppendUint32(b, 0xFFFFFFFF)
	b = append(b, "WAVEfmt "...)
	b = binary.LittleEndian.AppendUint32(b, 16)
	b = binary.LittleEndian.AppendUint16(b, 1) // PCM
	b = binary.LittleEndian.AppendUint16(b, uint16(channels))
	b = binary.LittleEndian.AppendUint32(b, uint32(rate))
	b = binary.LittleEndian.AppendUint32(b, uint32(rate*channels*2))
	b = binary.LittleEndian.AppendUint16(b, uint16(channels*2))
	b = binary.LittleEndian.AppendUint16(b, 16)
	b = append(b, "data"...)
	b = binary.LittleEndian.AppendUint32(b, 0xFFFFFFFF)
	return b
}

// pcmConverter turns streamed pcm16le of any rate and channel count into
// OpenAI's pcm (24 kHz mono).
type pcmConverter struct {
	rate, channels int
	rs             *resample.Resampler
	tmp            []float32
	out            []float32
}

func newPCMConverter(rate, channels int) *pcmConverter {
	c := &pcmConverter{rate: rate, channels: max(channels, 1)}
	if rate != PCMRate {
		c.rs = resample.New(rate, PCMRate, 1, resample.Good)
	}
	return c
}

// convert takes pcm16le frames and returns 24 kHz mono pcm16le.
func (c *pcmConverter) convert(in []byte) []byte {
	if c.rs == nil && c.channels == 1 {
		return in
	}
	frames := len(in) / 2 / c.channels
	c.tmp = c.tmp[:0]
	for f := 0; f < frames; f++ {
		var sum float32
		for ch := 0; ch < c.channels; ch++ {
			i := (f*c.channels + ch) * 2
			sum += float32(int16(binary.LittleEndian.Uint16(in[i:]))) / 32768
		}
		c.tmp = append(c.tmp, sum/float32(c.channels))
	}
	mono := c.tmp
	if c.rs != nil {
		c.out = c.rs.Process(c.out[:0], mono)
		mono = c.out
	}
	return floatsToPCM16(mono)
}

// flush returns what the resampler still holds.
func (c *pcmConverter) flush() []byte {
	if c.rs == nil {
		return nil
	}
	c.out = c.rs.Flush(c.out[:0])
	return floatsToPCM16(c.out)
}

func floatsToPCM16(s []float32) []byte {
	b := make([]byte, 0, len(s)*2)
	for _, v := range s {
		v = min(max(v, -1), 1)
		b = binary.LittleEndian.AppendUint16(b, uint16(int16(v*32767)))
	}
	return b
}

// filePCM reads a WAV file as mono pcm16le at rate (OpenAI's pcm: PCMRate;
// 0 keeps the file's).
func filePCM(path string, rate int) ([]byte, error) {
	buf, _, err := wav.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if buf.Rate <= 0 || buf.Channels <= 0 {
		return nil, fmt.Errorf("%s: no audio", path)
	}
	mono := buf
	if buf.Channels > 1 {
		mono = dsp.Buffer{Rate: buf.Rate, Channels: 1, Samples: make([]float32, buf.Frames())}
		for f := range mono.Samples {
			var sum float32
			for ch := 0; ch < buf.Channels; ch++ {
				sum += buf.Samples[f*buf.Channels+ch]
			}
			mono.Samples[f] = sum / float32(buf.Channels)
		}
	}
	if rate > 0 && mono.Rate != rate {
		mono = resample.Buffer(mono, rate, resample.Good)
	}
	return floatsToPCM16(mono.Samples), nil
}
