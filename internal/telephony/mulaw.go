// Package telephony is AudioSmith's AI phone agent over Twilio (M6b):
//
//   - the Twilio REST client (credentials check, phone numbers, placing and
//     ending calls; twilio.go),
//   - webhook signatures (X-Twilio-Signature, signature.go) and TwiML
//     (twiml.go),
//   - the gateway: a separate HTTP listener that serves only Twilio's
//     webhooks and the Media Streams WebSocket, with single-use stream
//     tokens (gateway.go, ws.go, media.go),
//   - phone audio: G.711 μ-law at 8 kHz, resampling to and from the
//     engine's rates (mulaw.go, audio.go), an energy endpointer (vad.go),
//   - the agent loop: listen → transcribe → think → speak, with barge-in,
//     take-over, the AI disclosure and guardrails (agent.go, prompt.go),
//   - phone numbers: E.164 normalisation and country hints (phone.go).
//
// Speech-to-text, the language model and speech come from the caller as
// functions (Agent's STT, LLM and TTS): the package knows nothing of the
// engine, the job queue or the store. Everything is goroutine-safe where
// it says so; no UI strings live here (errors are English, events carry
// codes).
//
// Imports: dsp, dsp/resample, dsp/wav.
package telephony

// G.711 μ-law (ITU-T G.711), the encoding of Twilio Media Streams:
// 8 kHz, 8-bit, mono. Samples here are float32 in [-1, 1].

const (
	mulawBias = 0x84
	mulawClip = 32635
)

// mulawDecode maps each μ-law byte to its linear 16-bit value.
var mulawDecode = func() (t [256]int16) {
	for i := range 256 {
		u := ^byte(i)
		sign := u & 0x80
		exp := (u >> 4) & 0x07
		mant := int(u & 0x0F)
		v := ((mant << 3) + mulawBias) << exp
		v -= mulawBias
		if sign != 0 {
			v = -v
		}
		t[i] = int16(v)
	}
	return t
}()

// mulawEncode16 encodes a linear 16-bit sample.
func mulawEncode16(s int) byte {
	var sign byte
	if s < 0 {
		sign = 0x80
		s = -s
	}
	if s > mulawClip {
		s = mulawClip
	}
	s += mulawBias
	exp := byte(7)
	for mask := 0x4000; s&mask == 0 && exp > 0; mask >>= 1 {
		exp--
	}
	mant := byte(s>>(exp+3)) & 0x0F
	return ^(sign | exp<<4 | mant)
}

// MulawToPCM decodes μ-law bytes, appending float samples to dst.
func MulawToPCM(dst []float32, b []byte) []float32 {
	for _, u := range b {
		dst = append(dst, float32(mulawDecode[u])/32768)
	}
	return dst
}

// PCMToMulaw encodes float samples (clipped to [-1, 1]), appending the
// bytes to dst.
func PCMToMulaw(dst []byte, pcm []float32) []byte {
	for _, x := range pcm {
		switch {
		case x != x: // NaN
			x = 0
		case x > 1:
			x = 1
		case x < -1:
			x = -1
		}
		dst = append(dst, mulawEncode16(int(x*32767)))
	}
	return dst
}

// MulawSilence is the μ-law byte of silence.
const MulawSilence byte = 0xFF
