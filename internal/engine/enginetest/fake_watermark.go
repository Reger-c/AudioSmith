package enginetest

// The fake's watermark methods (media role):
//
//   - watermark.embed: the input, unchanged, written to the output, and
//     the message in "<output>.wm" beside it; a message outside 0–65535 is
//     bad_request.
//   - watermark.detect: probability 1 and the message when "<input>.wm"
//     exists, else 0 and message 0.

import (
	"os"
	"strconv"
	"strings"

	"github.com/hehehai/AudioSmith/internal/engine/proto"
)

func (f *fakeEngine) watermarkEmbed(m proto.Message) {
	var p proto.WatermarkEmbedParams
	if !f.modelJob(m, &p, func() string { return p.Model }) {
		return
	}
	if p.Message < 0 || p.Message > 0xFFFF {
		f.fail(m.ID, proto.CodeBadRequest, "message must be 0–65535 (16 bits), not %d", p.Message)
		return
	}
	a, ok := f.readInput(m.ID, p.Input)
	if !ok || !f.work(m.ID, p.Input, "watermarking", nil) || !f.write(m.ID, p.Output, a) {
		return
	}
	os.WriteFile(p.Output.Path+".wm", []byte(strconv.Itoa(p.Message)), 0o644)
	f.result(m.ID, proto.WatermarkEmbedResult{Output: p.Output.Path, SampleRate: a.Rate, Channels: a.Channels, Frames: int64(a.Frames()),
		AudioSeconds: a.Seconds(), Model: p.Model, Message: p.Message, SNR: 30, Seconds: 0.01})
}

func (f *fakeEngine) watermarkDetect(m proto.Message) {
	var p proto.WatermarkDetectParams
	if !f.modelJob(m, &p, func() string { return p.Model }) {
		return
	}
	a, ok := f.readInput(m.ID, p.Input)
	if !ok || !f.work(m.ID, p.Input, "detecting", nil) {
		return
	}
	res := proto.WatermarkDetectResult{Bits: make([]float64, proto.WatermarkBits), AudioSeconds: a.Seconds(), Model: p.Model, Seconds: 0.01}
	if b, err := os.ReadFile(p.Input + ".wm"); err == nil {
		res.Message, _ = strconv.Atoi(strings.TrimSpace(string(b)))
		res.Probability, res.Detected, res.Confidence = 1, true, 1
		for i := range res.Bits {
			res.Bits[i] = float64(res.Message >> (proto.WatermarkBits - 1 - i) & 1)
		}
	} else {
		for i := range res.Bits {
			res.Bits[i] = 0.5
		}
	}
	for s := 0.0; s < a.Seconds(); s++ {
		res.Frames = append(res.Frames, proto.WatermarkFrame{Start: s, End: min(s+1, a.Seconds()), Probability: res.Probability})
	}
	f.result(m.ID, res)
}
