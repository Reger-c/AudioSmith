package telephony

import (
	"math"

	"github.com/hehehai/AudioSmith/internal/dsp"
	"github.com/hehehai/AudioSmith/internal/dsp/resample"
)

// Phone audio rates.
const (
	PhoneRate  = 8000  // Twilio Media Streams: μ-law at 8 kHz
	ListenRate = 16000 // speech-to-text input
	// FrameMillis is the length of one Media Streams frame.
	FrameMillis = 20
	// FrameBytes is one 20 ms μ-law frame at 8 kHz.
	FrameBytes = PhoneRate * FrameMillis / 1000
)

// Upsampler turns the caller's 8 kHz μ-law into 16 kHz PCM for the
// endpointer and speech-to-text, as a stream. Not goroutine-safe.
type Upsampler struct {
	r   *resample.Resampler
	pcm []float32
}

// NewUpsampler returns an 8 → 16 kHz stream converter.
func NewUpsampler() *Upsampler {
	return &Upsampler{r: resample.New(PhoneRate, ListenRate, 1, resample.Fast)}
}

// Push decodes μ-law bytes and returns the 16 kHz samples they make
// available (valid until the next call).
func (u *Upsampler) Push(b []byte) []float32 {
	u.pcm = MulawToPCM(u.pcm[:0], b)
	return u.r.Process(nil, u.pcm)
}

// PhoneEncoder turns speech at any rate into 8 kHz μ-law, as a stream:
// band-limited for the phone, levelled (see Level). Not goroutine-safe.
type PhoneEncoder struct {
	rate int
	r    *resample.Resampler
	gain float32
}

// NewPhoneEncoder converts audio of rate (Hz) to phone audio. gain is
// applied before encoding (Level chooses one).
func NewPhoneEncoder(rate int, gain float32) *PhoneEncoder {
	if rate <= 0 {
		rate = PhoneRate
	}
	if gain <= 0 {
		gain = 1
	}
	return &PhoneEncoder{rate: rate, r: resample.New(rate, PhoneRate, 1, resample.Good), gain: gain}
}

// Push converts pcm (mono, the encoder's rate) and returns μ-law bytes.
func (e *PhoneEncoder) Push(pcm []float32) []byte {
	out := e.r.Process(nil, pcm)
	if e.gain != 1 {
		dsp.Gain(out, e.gain)
	}
	return PCMToMulaw(nil, out)
}

// Flush returns the rest of the stream.
func (e *PhoneEncoder) Flush() []byte {
	out := e.r.Flush(nil)
	if e.gain != 1 {
		dsp.Gain(out, e.gain)
	}
	return PCMToMulaw(nil, out)
}

// Level returns the gain that brings speech to about −18 dBFS RMS on the
// phone with peaks under −1 dBFS, from a sample of it (the first sentence
// of a reply is enough). Silence gets 1.
func Level(pcm []float32) float32 {
	rms := dsp.RMS(pcm)
	peak := float64(dsp.Peak(pcm))
	if rms < 1e-4 || peak < 1e-4 {
		return 1
	}
	g := math.Pow(10, -18.0/20) / rms
	if lim := 0.89 / peak; g > lim {
		g = lim
	}
	return float32(min(max(g, 0.25), 8))
}

// PhoneQuality renders speech as a phone would carry it: down to 8 kHz
// μ-law and back, at rate out (for the setup page's preview). The result
// is mono at PhoneRate when out is 0.
func PhoneQuality(b dsp.Buffer, out int) dsp.Buffer {
	m := dsp.Remix(b, 1)
	enc := NewPhoneEncoder(m.Rate, Level(m.Samples))
	mu := enc.Push(m.Samples)
	mu = append(mu, enc.Flush()...)
	phone := dsp.Buffer{Rate: PhoneRate, Channels: 1, Samples: MulawToPCM(nil, mu)}
	if out > 0 && out != PhoneRate {
		return resample.Buffer(phone, out, resample.Good)
	}
	return phone
}

// PCM16ToFloat decodes little-endian 16-bit PCM (the engine's streamed
// chunks), appending to dst; channels > 1 are mixed down.
func PCM16ToFloat(dst []float32, data []byte, channels int) []float32 {
	if channels <= 0 {
		channels = 1
	}
	step := 2 * channels
	for i := 0; i+step <= len(data); i += step {
		var sum float32
		for c := range channels {
			v := int16(uint16(data[i+2*c]) | uint16(data[i+2*c+1])<<8)
			sum += float32(v) / 32768
		}
		dst = append(dst, sum/float32(channels))
	}
	return dst
}
