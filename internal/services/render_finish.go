package services

// The end of a render: the dropped-chunk check, the final WAV in the
// take's bit depth (renderState.finish, "Even out volume"), Check audio
// (CheckAudio, Issues) and copying engine files the pipeline cannot read;
// split from render.go.

import (
	"context"
	"io"
	"math"
	"os"
	"path/filepath"
	"strconv"

	"github.com/hehehai/AudioSmith/internal/core"
	"github.com/hehehai/AudioSmith/internal/dsp/loudness"
	"github.com/hehehai/AudioSmith/internal/dsp/master"
	"github.com/hehehai/AudioSmith/internal/dsp/qc"
	"github.com/hehehai/AudioSmith/internal/dsp/wav"
	"github.com/hehehai/AudioSmith/internal/text"
)

// dropCheck compares a chunk's length with what its text should take.
func dropCheck(i int, c renderChunk, speed, start, end, voiced float64) []core.TakeNotice {
	if speed <= 0 {
		speed = 1
	}
	if c.seg.Speed > 0 {
		speed *= c.seg.Speed
	}
	w, n := text.Count(c.seg.Text)
	expected := text.Estimate(w, n).Seconds() / speed
	if c.params.Duration != nil {
		expected = *c.params.Duration
	}
	notice := func(code string) core.TakeNotice {
		return core.TakeNotice{Code: code, Severity: "warning", Chunk: i, Start: start, End: end, Value: math.Round(voiced*10) / 10,
			Params: map[string]string{"expected": strconv.FormatFloat(math.Round(expected*10)/10, 'f', -1, 64)}}
	}
	switch {
	case voiced < 0.05:
		return []core.TakeNotice{notice(NoticeChunkSilent)}
	case expected >= 2 && voiced < 0.4*expected:
		return []core.TakeNotice{notice(NoticeChunkShort)}
	case expected >= 2 && voiced > 3*expected && voiced > 8:
		return []core.TakeNotice{notice(NoticeChunkLong)}
	}
	return nil
}

// finalAudio describes the take's file.
type finalAudio struct {
	rate, channels int
	frames         int64
	bytes          int64
	issues         []core.TakeNotice
	qcDone         bool
}

func (f finalAudio) seconds() float64 {
	if f.rate == 0 {
		return 0
	}
	return float64(f.frames) / float64(f.rate)
}

// encodingOf maps a take's bits to a WAV encoding.
func encodingOf(bits int) wav.Encoding {
	switch bits {
	case 24:
		return wav.PCM24
	case 32:
		return wav.Float32
	}
	return wav.PCM16
}

// finish closes the joined audio, masters it if asked, and writes the
// take's file with Check audio's findings.
func (st *renderState) finish(ctx context.Context, opts core.TakeRender) (finalAudio, error) {
	if err := st.joiner.Close(); err != nil {
		return finalAudio{}, err
	}
	st.flushStream(true)
	integrated := st.meter.Integrated()
	if err := st.tmp.Close(); err != nil {
		return finalAudio{}, err
	}
	st.tmp = nil
	st.files = append(st.files, st.tmpPath)
	src := st.tmpPath

	var gainStage *loudness.Stream
	if opts.Master != "" && opts.Master != master.Raw.ID {
		preset, err := master.Lookup(opts.Master)
		if err != nil {
			return finalAudio{}, err
		}
		pre := 0.0
		if preset.Compressor != nil && !math.IsInf(integrated, 0) {
			pre = master.Reference - integrated
		}
		pr, err := master.NewProcessor(preset, st.rate, st.channels, pre)
		if err != nil {
			return finalAudio{}, err
		}
		// Pass 2: filters and compressor, metered with true peak.
		mid := filepath.Join(st.r.tmpDir(), st.job.ID+"-mastered.wav")
		st.files = append(st.files, mid)
		w, err := wav.Create(mid, wav.Format{Rate: st.rate, Channels: st.channels, Encoding: wav.Float32}, wav.Options{Large: true})
		if err != nil {
			return finalAudio{}, err
		}
		m := loudness.NewMeter(st.rate, st.channels, loudness.Options{})
		err = eachBlock(ctx, src, func(s []float32) error {
			pr.Process(s)
			m.Write(s)
			return w.Write(s)
		})
		if err != nil {
			w.Abort()
			return finalAudio{}, err
		}
		if err := w.Close(); err != nil {
			return finalAudio{}, err
		}
		src = mid
		if preset.Loudness != 0 {
			target := loudness.Target{ID: preset.ID, Integrated: preset.Loudness, MinIntegrated: preset.Loudness - 1,
				MaxIntegrated: preset.Loudness + 1, TruePeak: preset.TruePeak}
			gain, limit := loudness.Plan(m.Stats(), target)
			gainStage = loudness.NewStream(st.rate, st.channels, gain, preset.TruePeak, limit)
		}
	}

	// Last pass: gain and ceiling, the take's bit depth, Check audio.
	enc := encodingOf(opts.Bits)
	out, err := wav.Create(st.p.Output, wav.Format{Rate: st.rate, Channels: st.channels, Encoding: enc}, wav.Options{Dither: enc == wav.PCM16})
	if err != nil {
		return finalAudio{}, err
	}
	check := qc.NewChecker(st.rate, st.channels, qc.Options{MaxIssues: 20})
	write := func(s []float32) error {
		check.Write(s)
		return out.Write(s)
	}
	var buf []float32
	err = eachBlock(ctx, src, func(s []float32) error {
		if gainStage == nil {
			return write(s)
		}
		buf = gainStage.Process(buf[:0], s)
		return write(buf)
	})
	if err == nil && gainStage != nil {
		err = write(gainStage.Flush(buf[:0]))
	}
	if err != nil {
		out.Abort()
		return finalAudio{}, err
	}
	frames := out.Frames()
	if err := out.Close(); err != nil {
		return finalAudio{}, err
	}
	fa := finalAudio{rate: st.rate, channels: st.channels, frames: frames, qcDone: true}
	if fi, err := os.Stat(st.p.Output); err == nil {
		fa.bytes = fi.Size()
	}
	fa.issues = Issues(check.Report())
	return fa, nil
}

// Issues converts Check audio's report to take notices.
func Issues(rep qc.Report) []core.TakeNotice {
	var out []core.TakeNotice
	for _, is := range rep.Issues {
		out = append(out, core.TakeNotice{Code: "qc." + is.Code, Severity: string(is.Severity),
			Start: math.Round(is.Start*1000) / 1000, End: math.Round(is.End*1000) / 1000, Value: math.Round(is.Value*100) / 100})
	}
	return out
}

// CheckAudio runs Check audio over a WAV file.
func CheckAudio(ctx context.Context, path string) ([]core.TakeNotice, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	rd, err := wav.NewReader(f)
	if err != nil {
		return nil, err
	}
	format := rd.Format()
	check := qc.NewChecker(format.Rate, format.Channels, qc.Options{MaxIssues: 20})
	buf := make([]float32, 8192*format.Channels)
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		n, err := rd.Read(buf)
		check.Write(buf[:n])
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
	}
	return Issues(check.Report()), nil
}

// copyRaw copies an engine file the pipeline could not read to the take.
func copyRaw(ctx context.Context, src, dst string) (finalAudio, error) {
	if err := copyFile(ctx, src, dst); err != nil {
		return finalAudio{}, err
	}
	fa := finalAudio{}
	if fi, err := os.Stat(dst); err == nil {
		fa.bytes = fi.Size()
	}
	return fa, nil
}
