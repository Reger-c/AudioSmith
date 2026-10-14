package telephony

import (
	"crypto/hmac"
	"crypto/sha1"
	"encoding/base64"
	"math"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/hehehai/AudioSmith/internal/dsp"
)

func TestMulawRoundTrip(t *testing.T) {
	// Every byte decodes and re-encodes to itself (0x7F and 0xFF are
	// both zero: +0 and −0).
	for i := range 256 {
		v := mulawDecode[i]
		got := mulawEncode16(int(v))
		if got != byte(i) && !(v == 0 && (got == 0xFF || got == 0x7F)) {
			t.Errorf("byte %#x → %d → %#x", i, v, got)
		}
	}
	if MulawSilence != mulawEncode16(0) {
		t.Errorf("silence %#x", mulawEncode16(0))
	}
	// A sine survives with a G.711-like signal-to-noise ratio.
	pcm := make([]float32, 8000)
	for i := range pcm {
		pcm[i] = float32(0.5 * math.Sin(2*math.Pi*440*float64(i)/8000))
	}
	back := MulawToPCM(nil, PCMToMulaw(nil, pcm))
	var sig, noise float64
	for i := range pcm {
		sig += float64(pcm[i] * pcm[i])
		d := float64(pcm[i] - back[i])
		noise += d * d
	}
	if snr := 10 * math.Log10(sig/noise); snr < 30 {
		t.Errorf("SNR %.1f dB", snr)
	}
	// Clipping and NaN.
	if b := PCMToMulaw(nil, []float32{2, -2, float32(math.NaN())}); len(b) != 3 || b[2] != MulawSilence && b[2] != 0x7F {
		t.Errorf("clip %x", b)
	}
}

func TestPhoneQualityAndLevel(t *testing.T) {
	b := dsp.New(24000, 1, 24000)
	for i := range b.Samples {
		b.Samples[i] = float32(0.05 * math.Sin(2*math.Pi*300*float64(i)/24000))
	}
	p := PhoneQuality(b, 0)
	if p.Rate != PhoneRate || p.Frames() < 7990 || p.Frames() > 8010 {
		t.Fatalf("phone %d Hz %d frames", p.Rate, p.Frames())
	}
	// Levelled up to about −18 dBFS RMS.
	if db := dsp.DB(dsp.RMS(p.Samples[800:7200])); db < -21 || db > -15 {
		t.Errorf("level %.1f dB", db)
	}
	if g := Level(make([]float32, 100)); g != 1 {
		t.Errorf("silence gain %v", g)
	}
	up := PhoneQuality(b, 24000)
	if up.Rate != 24000 || up.Frames() < 23900 {
		t.Errorf("back up %d %d", up.Rate, up.Frames())
	}
	// PCM16 decoding mixes channels.
	got := PCM16ToFloat(nil, []byte{0x00, 0x40, 0x00, 0xC0, 0x00, 0x40, 0x00, 0x40}, 2)
	if len(got) != 2 || got[0] != 0 || got[1] != 0.5 {
		t.Errorf("pcm16 %v", got)
	}
}

func TestSignature(t *testing.T) {
	params := url.Values{"CallSid": {"CA1234567890ABCDE"}, "Caller": {"+12349013030"}, "Digits": {"1234"},
		"From": {"+12349013030"}, "To": {"+18005551212"}}
	u := "https://mycompany.com/myapp.php?foo=1&bar=2"
	sig := Signature("12345", u, params)
	// The documented construction, by hand.
	data := u + "CallSid" + "CA1234567890ABCDE" + "Caller" + "+12349013030" + "Digits" + "1234" + "From" + "+12349013030" + "To" + "+18005551212"
	if want := hmacSHA1Base64("12345", data); sig != want {
		t.Fatalf("signature %s, want %s", sig, want)
	}
	if !ValidSignature("12345", u, params, sig) {
		t.Error("valid signature refused")
	}
	if !ValidSignature("12345", "https://mycompany.com:443/myapp.php?foo=1&bar=2", params, sig) {
		t.Error("port variant refused")
	}
	if ValidSignature("other", u, params, sig) || ValidSignature("12345", u+"&x=1", params, sig) || ValidSignature("12345", u, params, "") {
		t.Error("bad signature accepted")
	}
	tampered := url.Values{}
	for k, v := range params {
		tampered[k] = v
	}
	tampered.Set("To", "+18005550000")
	if ValidSignature("12345", u, tampered, sig) {
		t.Error("tampered params accepted")
	}
}

func TestTwiML(t *testing.T) {
	x := StreamTwiML("wss://a.example/twilio/stream", map[string]string{"token": `a"<b`})
	for _, want := range []string{`<Connect><Stream url="wss://a.example/twilio/stream">`, `<Parameter name="token" value="a&#34;&lt;b"/>`, `<Hangup/>`} {
		if !strings.Contains(x, want) {
			t.Errorf("%s lacks %s", x, want)
		}
	}
	if !strings.Contains(RejectTwiML("busy"), `reason="busy"`) || !strings.Contains(RejectTwiML("x"), `reason="rejected"`) {
		t.Error("reject")
	}
	if StreamURL("https://x.trycloudflare.com/") != "wss://x.trycloudflare.com/twilio/stream" || VoiceURL("https://x.io") != "https://x.io/twilio/voice" {
		t.Error("urls")
	}
}

func TestPhoneNumbers(t *testing.T) {
	for in, want := range map[string]string{
		" +44 20 7946-0958 ":  "+442079460958",
		"0049 (30) 1234.5678": "+493012345678",
		"+1 555 010 0199":     "+15550100199",
	} {
		if got := NormalizeNumber(in); got != want {
			t.Errorf("%q → %q, want %q", in, got, want)
		}
	}
	for s, want := range map[string]NumberProblem{
		"": NumberEmpty, "4420794": NumberNoPlus, "+44": NumberBadShape, "+0442079460958": NumberBadShape,
		"+44207946095a": NumberBadShape, "+1234567890123456": NumberBadShape, "+442079460958": NumberOK, "+12345678": NumberOK,
	} {
		if got := CheckNumber(s); got != want {
			t.Errorf("%q: %v, want %v", s, got, want)
		}
	}
	if Region("+442079460958") != "GB" || Region("+8613800138000") != "CN" || Region("+15550100199") != "" || Region("+85261234567") != "HK" {
		t.Error("regions")
	}
	if got := MaskNumber("+15550100123"); got != "+1••••••0123" {
		t.Errorf("mask %q", got)
	}
	if got := MaskNumber("+442079460958"); got != "+44••••••0958" {
		t.Errorf("mask %q", got)
	}
	if !ValidAccountSID("AC" + "0123456789abcdef0123456789abcdef") || ValidAccountSID("AC123") || ValidAccountSID("SK" + "0123456789abcdef0123456789abcdef") {
		t.Error("account sid")
	}
}

func feed(p *ReplyParser, text string, step int) []string {
	var out []string
	for i := 0; i < len(text); i += step {
		out = append(out, p.Feed(text[i:min(i+step, len(text))])...)
	}
	return out
}

func TestReplyParser(t *testing.T) {
	for _, step := range []int{1, 3, 7, 1000} {
		var p ReplyParser
		got := feed(&p, "SAY: Hi, this is an assistant calling for Sam. I'd like to book a table for two! Is Friday at 7 free?\nOUTCOME: needs_you — they asked for a deposit\nHANGUP\n", step)
		rest, r := p.Finish()
		got = append(got, rest...)
		want := []string{"Hi, this is an assistant calling for Sam.", "I'd like to book a table for two!", "Is Friday at 7 free?"}
		if strings.Join(got, "|") != strings.Join(want, "|") {
			t.Errorf("step %d: sentences %q", step, got)
		}
		if r.Outcome != OutcomeNeedsYou || r.Note != "they asked for a deposit" || !r.Hangup || len(r.Say) != 3 {
			t.Errorf("step %d: reply %+v", step, r)
		}
	}
	// The first sentence comes out before the reply ends.
	var p ReplyParser
	if got := p.Feed("SAY: Hello there. And"); len(got) != 1 || got[0] != "Hello there." {
		t.Errorf("early %q", got)
	}
	// Unprefixed text is speech; Chinese punctuation splits; ACTION.
	var q ReplyParser
	got := feed(&q, "好的，我帮您预约。请问几位？\nACTION: end_call", 2)
	rest, r := q.Finish()
	got = append(got, rest...)
	if strings.Join(got, "|") != "好的，我帮您预约。|请问几位？" || !r.Hangup {
		t.Errorf("zh %q %+v", got, r)
	}
	var o ReplyParser
	feed(&o, "SAY: Done, see you then.\nOUTCOME: Booked - Friday 7pm, two people", 5)
	_, r = o.Finish()
	if r.Outcome != OutcomeBooked || r.Note != "Friday 7pm, two people" {
		t.Errorf("booked %+v", r)
	}
	// "Dr. Smith" does not split; markdown is dropped.
	var d ReplyParser
	got = feed(&d, "SAY: Dr. Smith is in on **Monday**. Thanks", 4)
	rest, _ = d.Finish()
	got = append(got, rest...)
	if strings.Join(got, "|") != "Dr. Smith is in on Monday.|Thanks" {
		t.Errorf("abbrev %q", got)
	}
}

func TestGuard(t *testing.T) {
	brief := "Book a table. My callback number is +1 555 010 0199 and booking ref 123456789."
	if s, changed := Guard("My card is 4111 1111 1111 1111.", brief); !changed || strings.Contains(s, "4111") {
		t.Errorf("card %q", s)
	}
	if s, changed := Guard("You can reach Sam at 555-010-0199.", brief); changed {
		t.Errorf("brief number withheld: %q", s)
	}
	if s, changed := Guard("The reference is 123 456 789.", brief); changed {
		t.Errorf("brief ref withheld: %q", s)
	}
	if _, changed := Guard("We are two people at 7:30 on the 14th.", brief); changed {
		t.Error("short numbers withheld")
	}
}

func TestSummaryParse(t *testing.T) {
	o, s := ParseSummary("OUTCOME: booked\nSUMMARY: Table for two on Friday at 7 pm.\nUnder the name Sam.")
	if o != OutcomeBooked || s != "Table for two on Friday at 7 pm. Under the name Sam." {
		t.Errorf("%q %q", o, s)
	}
	o, s = ParseSummary("They were closed.")
	if o != "" || s != "They were closed." {
		t.Errorf("%q %q", o, s)
	}
	msgs := SummaryPrompt(Brief{Task: "Ask opening hours", User: "Sam"}, "Agent: Hi\nThem: We open at 9.")
	if len(msgs) != 2 || !strings.Contains(msgs[1].Content, "We open at 9.") || !strings.Contains(msgs[0].Content, "Sam") {
		t.Errorf("%+v", msgs)
	}
	sp := SystemPrompt(Brief{Task: "Book a table", User: "Sam", Disclosure: "Hi, I'm an AI.", MaxMinutes: 5})
	for _, want := range []string{"Book a table", "Sam", "AI assistant", "5 minutes", "Hi, I'm an AI.", "SAY:", "HANGUP", "payment"} {
		if !strings.Contains(sp, want) {
			t.Errorf("system prompt lacks %q", want)
		}
	}
}

func TestEndpointer(t *testing.T) {
	e := NewEndpointer(EndpointOptions{})
	tone := func(sec float64, amp float64) []float32 {
		n := int(sec * ListenRate)
		out := make([]float32, n)
		for i := range out {
			out[i] = float32(amp * math.Sin(2*math.Pi*200*float64(i)/ListenRate))
		}
		return out
	}
	var got []Endpoint
	got = append(got, e.Push(tone(1, 0.0003))...) // quiet: the floor
	if len(got) != 0 {
		t.Fatalf("noise found speech: %v", got)
	}
	got = append(got, e.Push(tone(0.15, 0.3))...) // too short to start
	if len(got) != 0 {
		t.Fatalf("150 ms started: %v", got)
	}
	got = append(got, e.Push(tone(0.2, 0.0003))...)
	got = append(got, e.Push(tone(1.0, 0.3))...)
	if len(got) != 1 || got[0].Kind != SpeechStarted || !e.Speaking() {
		t.Fatalf("start: %v", got)
	}
	got = append(got, e.Push(tone(0.4, 0.0003))...) // a pause inside the utterance
	got = append(got, e.Push(tone(0.5, 0.3))...)
	if len(got) != 1 {
		t.Fatalf("pause ended it: %v", len(got))
	}
	got = append(got, e.Push(tone(0.8, 0.0003))...)
	if len(got) != 2 || got[1].Kind != SpeechEnded {
		t.Fatalf("end: %d", len(got))
	}
	sec := float64(len(got[1].Audio)) / ListenRate
	// 0.3 s pre-roll + 1.0 + 0.4 + 0.5 + 0.2 s of trailing silence.
	if sec < 2.2 || sec > 2.6 {
		t.Errorf("utterance %.2f s", sec)
	}
	// A long one is cut at Max.
	e2 := NewEndpointer(EndpointOptions{Max: 2 * time.Second})
	out := e2.Push(tone(3, 0.3))
	if len(out) < 2 || out[1].Kind != SpeechEnded || len(out[1].Audio) > 2*ListenRate+100 {
		t.Errorf("max: %v", len(out))
	}
}

func hmacSHA1Base64(key, data string) string {
	m := hmac.New(sha1.New, []byte(key))
	m.Write([]byte(data))
	return base64.StdEncoding.EncodeToString(m.Sum(nil))
}
