package enginetest

// The fake's M4 (dubbing) methods:
//
//   - separate.run: "vocals" is the input at 44.1 kHz stereo, "background"
//     the same at a tenth of the level; an empty path skips a stem.
//   - diarize.run: the speech regions of the input (Regions), alternating
//     speakers "0" and "1"; more than 4 speakers asked → unsupported
//     {"capability":"speakers","max":4}.
//   - media.mux: the video must be .mov/.mp4/.m4v (else unsupported_media);
//     every audio and subtitle path must exist (file_not_found); retime is
//     unsupported. The output holds "FAKE-MP4" and the params as JSON.
//   - media.thumbnail: real PNG/JPEG images (by extension), 1920×1080
//     scaled to width.
//   - media.transcode: .mov/.mp4/.m4v → "FAKE-MP4" at most maxHeight (720)
//     high, 16:9; other extensions (webm, mkv) → unsupported_media.

import (
	"encoding/json"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/hehehai/AudioSmith/internal/engine/proto"
)

// FakeVideoWidth and FakeVideoHeight are the size of the fake's videos.
const (
	FakeVideoWidth  = 1920
	FakeVideoHeight = 1080
)

func (f *fakeEngine) separate(m proto.Message) {
	var p proto.SeparateParams
	if !f.modelJob(m, &p, func() string { return p.Model }) {
		return
	}
	a, ok := f.readInput(m.ID, p.Audio)
	if !ok || !f.work(m.ID, p.Audio, "separating", nil) {
		return
	}
	stems := Convert(a, 44100, 2)
	res := proto.SeparateResult{SampleRate: 44100, Channels: 2, Frames: int64(stems.Frames()), AudioSeconds: stems.Seconds(), Model: p.Model, Seconds: 0.01}
	if p.Vocals.Path != "" {
		if !f.write(m.ID, p.Vocals, stems) {
			return
		}
		res.Vocals = p.Vocals.Path
	}
	if p.Background.Path != "" {
		bg := Audio{Rate: stems.Rate, Channels: 2, Samples: make([]float64, len(stems.Samples))}
		for i, v := range stems.Samples {
			bg.Samples[i] = v / 10
		}
		if !f.write(m.ID, p.Background, bg) {
			return
		}
		res.Background = p.Background.Path
	}
	f.touch(p.Model)
	f.result(m.ID, res)
}

func (f *fakeEngine) diarize(m proto.Message) {
	var p proto.DiarizeParams
	if !f.modelJob(m, &p, func() string { return p.Model }) {
		return
	}
	if p.Speakers > 4 || p.MinSpeakers > 4 {
		f.failData(m.ID, proto.CodeUnsupported, map[string]any{"capability": "speakers", "max": 4}, "The model tells at most 4 speakers apart")
		return
	}
	a, ok := f.readInput(m.ID, p.Audio)
	if !ok || !f.work(m.ID, p.Audio, "diarizing", nil) {
		return
	}
	n := 2
	if p.Speakers > 0 {
		n = p.Speakers
	} else if p.MaxSpeakers > 0 {
		n = min(n, p.MaxSpeakers)
	}
	res := proto.DiarizeResult{Turns: []proto.Turn{}, Model: p.Model, AudioSeconds: a.Seconds(), Seconds: 0.01}
	seen := map[string]bool{}
	for i, r := range Regions(a, 0.25, 0.1, 0.03) {
		sp := strconv.Itoa(i % n)
		seen[sp] = true
		res.Turns = append(res.Turns, proto.Turn{Start: r.Start, End: r.End, Speaker: sp})
	}
	res.Speakers = len(seen)
	f.touch(p.Model)
	f.result(m.ID, res)
}

// video checks a video input; false when it answered.
func (f *fakeEngine) video(id, path string) bool {
	if _, err := os.Stat(path); err != nil {
		f.failData(id, proto.CodeFileNotFound, map[string]string{"path": path}, "No such file: %s", path)
		return false
	}
	if !isVideoExt(strings.ToLower(filepath.Ext(path))) {
		f.failData(id, proto.CodeUnsupportedMedia, map[string]string{"path": path}, "%s has no video track", path)
		return false
	}
	return true
}

func (f *fakeEngine) exists(id string, paths ...string) bool {
	for _, p := range paths {
		if _, err := os.Stat(p); err != nil {
			f.failData(id, proto.CodeFileNotFound, map[string]string{"path": p}, "No such file: %s", p)
			return false
		}
	}
	return true
}

func (f *fakeEngine) mux(m proto.Message) {
	var p proto.MuxParams
	if err := m.DecodeParams(&p); err != nil || p.Video == "" || p.Output.Path == "" {
		f.fail(m.ID, proto.CodeBadRequest, "Missing video or output")
		return
	}
	if len(p.Retime) > 0 {
		f.failData(m.ID, proto.CodeUnsupported, map[string]string{"capability": "retime"}, "Retiming the video is not supported yet")
		return
	}
	if !f.video(m.ID, p.Video) {
		return
	}
	for _, t := range p.Audio {
		if !f.exists(m.ID, t.Path) {
			return
		}
	}
	for _, s := range p.Subtitles {
		if !f.exists(m.ID, s.Path) {
			return
		}
	}
	if !f.work(m.ID, p.Video, "muxing", nil) {
		return
	}
	body, _ := json.Marshal(p)
	data := append([]byte("FAKE-MP4\n"), body...)
	if err := os.WriteFile(p.Output.Path, data, 0o644); err != nil {
		f.fail(m.ID, proto.CodeInternal, "Could not write %s: %v", p.Output.Path, err)
		return
	}
	tracks := len(p.Audio)
	if p.KeepAudio {
		tracks++
	}
	f.result(m.ID, proto.MuxResult{Output: p.Output.Path, Bytes: int64(len(data)), Duration: FakeMediaSeconds, Tracks: tracks})
}

func (f *fakeEngine) thumbnail(m proto.Message) {
	var p proto.ThumbnailParams
	if err := m.DecodeParams(&p); err != nil || len(p.Times) == 0 || len(p.Times) != len(p.Outputs) {
		f.fail(m.ID, proto.CodeBadRequest, "times and outputs must match")
		return
	}
	if !f.video(m.ID, p.Input) {
		return
	}
	w := p.Width
	if w <= 0 {
		w = FakeVideoWidth
	}
	h := w * FakeVideoHeight / FakeVideoWidth
	res := proto.ThumbnailResult{Frames: []proto.Frame{}}
	for i, t := range p.Times {
		img := image.NewRGBA(image.Rect(0, 0, w, h))
		for y := range h {
			for x := range w {
				img.Set(x, y, color.RGBA{uint8(x), uint8(y), uint8(t), 255})
			}
		}
		out, err := os.Create(p.Outputs[i])
		if err == nil {
			if strings.EqualFold(filepath.Ext(p.Outputs[i]), ".png") {
				err = png.Encode(out, img)
			} else {
				err = jpeg.Encode(out, img, &jpeg.Options{Quality: 80})
			}
			out.Close()
		}
		if err != nil {
			f.fail(m.ID, proto.CodeInternal, "Could not write %s: %v", p.Outputs[i], err)
			return
		}
		res.Frames = append(res.Frames, proto.Frame{Time: min(max(t, 0), FakeMediaSeconds), Path: p.Outputs[i], Width: w, Height: h})
	}
	f.result(m.ID, res)
}

func (f *fakeEngine) transcode(m proto.Message) {
	var p proto.TranscodeParams
	if err := m.DecodeParams(&p); err != nil || p.Output.Path == "" {
		f.fail(m.ID, proto.CodeBadRequest, "Missing input or output")
		return
	}
	if !f.video(m.ID, p.Input) || !f.work(m.ID, p.Input, "transcoding", nil) {
		return
	}
	hgt := p.MaxHeight
	if hgt <= 0 {
		hgt = 720
	}
	hgt = min(hgt, FakeVideoHeight)
	body, _ := json.Marshal(p)
	data := append([]byte("FAKE-MP4\n"), body...)
	if err := os.WriteFile(p.Output.Path, data, 0o644); err != nil {
		f.fail(m.ID, proto.CodeInternal, "Could not write %s: %v", p.Output.Path, err)
		return
	}
	f.result(m.ID, proto.TranscodeResult{Output: p.Output.Path, Bytes: int64(len(data)), Duration: FakeMediaSeconds,
		Width: hgt * FakeVideoWidth / FakeVideoHeight, Height: hgt})
}
