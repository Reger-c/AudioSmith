package services

// The dub's mix and qc stages: the track mixer (TrackMixer, the A/B
// player's live mix), the dubbed track and its speech alone under a
// limiter, and qc ("Verify"), which flags lines that do not say their
// text or run into the next; split from dub_render.go.

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/hehehai/AudioSmith/internal/dsp"
	"github.com/hehehai/AudioSmith/internal/dsp/dynamics"
	"github.com/hehehai/AudioSmith/internal/dsp/wav"
	"github.com/hehehai/AudioSmith/internal/dub"
	"github.com/hehehai/AudioSmith/internal/engine/proto"
	"github.com/hehehai/AudioSmith/internal/jobs"
)

// trackMixer prepares lang's mix: the background (separated, or the
// original ducked) and the rendered lines. withBackground false mixes
// the speech alone. The caller closes it.
func (s *Dubs) trackMixer(p *dub.Project, l string, withBackground bool) (*dub.TrackMixer, string, error) {
	audio := s.Path(p.ID, p.Media.Audio)
	if p.Media.Audio == "" {
		return nil, "", dub.ErrNoAudio
	}
	probe, err := dub.OpenWAV(audio, 0, 0)
	if err != nil {
		return nil, "", err
	}
	rate, _ := probe.Format()
	length := float64(probe.Frames()) / float64(rate)
	probe.Close()
	o := dub.MixOptions{Rate: rate, Channels: 2, Seconds: length}
	kind := ""
	if withBackground && p.Settings.KeepBackground {
		src, duck := audio, float64(dub.DuckOriginalDB)
		kind = dub.BackgroundDucked
		if p.Media.Background != "" {
			src, duck, kind = s.Path(p.ID, p.Media.Background), dub.DuckStemDB, dub.BackgroundStem
		}
		bg, err := dub.OpenWAV(src, rate, 2)
		if err != nil {
			return nil, "", err
		}
		o.Background, o.DuckDB = bg, duck
	}
	t := p.Track(l)
	var lines []dub.MixLine
	for _, seg := range p.Segments {
		r, ok := t.Lines[seg.ID]
		if !ok || r.File == "" || r.Err != "" {
			continue
		}
		path := s.Path(p.ID, r.File)
		lines = append(lines, dub.MixLine{ID: seg.ID, Start: seg.Start, Length: r.Length, Volume: seg.Volume, Load: func() (dsp.Buffer, error) {
			b, _, err := wav.ReadFile(path)
			return b, err
		}})
	}
	return dub.NewTrackMixer(lines, o), kind, nil
}

// TrackMixer is a project's live mix of lang (the A/B player): what the
// track would be with the lines as rendered now.
func (s *Dubs) TrackMixer(p *dub.Project, l string) (*dub.TrackMixer, error) {
	m, _, err := s.trackMixer(p, l, true)
	return m, err
}

// mix writes lang's dubbed track and its speech alone.
func (s *Dubs) mix(ctx context.Context, sc *stageCtx) error {
	p := sc.p
	l := sc.params.Lang
	if len(p.Track(l).Lines) == 0 {
		return stageErr(DubCodeNoLines, "Nothing was rendered in this language.")
	}
	if err := os.MkdirAll(sc.path("tracks"), 0o755); err != nil {
		return err
	}
	name := filepath.Join("tracks", dub.SafeName(l)+".wav")
	speech := filepath.Join("tracks", dub.SafeName(l)+".speech.wav")
	m, kind, err := s.trackMixer(p, l, true)
	if err != nil {
		return err
	}
	if p.Settings.KeepBackground && kind == dub.BackgroundDucked {
		sc.warn(DubWarnNoSeparation)
	}
	sc.progress("mixing", 0, "")
	frames, err := renderMix(ctx, m, sc.path(name), true, func(f float64) { sc.progress("mixing", f*0.7, "") })
	m.Close()
	if err != nil {
		return err
	}
	sm, _, err := s.trackMixer(p, l, false)
	if err != nil {
		return err
	}
	_, err = renderMix(ctx, sm, sc.path(speech), true, func(f float64) { sc.progress("mixing", 0.7+f*0.3, "") })
	sm.Close()
	if err != nil {
		return err
	}
	if m.Err != nil {
		return &jobs.Error{Code: CodeRenderFailed, Message: m.Err.Error()}
	}
	// The invisible watermark goes on the final, fitted tracks (thread #14).
	s.watermarkTracks(ctx, sc, sc.path(name), sc.path(speech))
	return sc.save(ctx, func(q *dub.Project) {
		t := q.Track(l)
		t.Lang, t.File, t.Speech, t.Background = l, name, speech, kind
		t.Seconds = round3(float64(frames) / float64(m.SampleRate()))
		t.Mixed = s.cfg.Now()
		q.PutTrack(t)
	})
}

// renderMix writes a mix to path (24-bit), under a −1 dBFS limiter.
func renderMix(ctx context.Context, m *dub.TrackMixer, path string, limit bool, progress func(float64)) (int64, error) {
	rate, ch := m.SampleRate(), m.Channels()
	out, err := wav.Create(path, wav.Format{Rate: rate, Channels: ch, Encoding: wav.PCM24}, wav.Options{Large: true})
	if err != nil {
		return 0, err
	}
	var lim *dynamics.Limiter
	if limit {
		lim = dynamics.NewLimiter(rate, ch, dynamics.LimiterParams{CeilingDB: -1})
	}
	buf := make([]float32, 8192*ch)
	var lb []float32
	total := max(m.Len(), 1)
	for {
		if err := ctx.Err(); err != nil {
			out.Abort()
			return 0, err
		}
		n, rerr := m.Read(buf)
		if n > 0 {
			block := buf[:n]
			if lim != nil {
				lb = lim.Process(lb[:0], block)
				block = lb
			}
			if err := out.Write(block); err != nil {
				out.Abort()
				return 0, err
			}
			if progress != nil {
				progress(float64(m.Position()) / float64(total))
			}
		}
		if rerr == io.EOF || n == 0 {
			break
		}
		if rerr != nil {
			out.Abort()
			return 0, rerr
		}
	}
	if lim != nil {
		if err := out.Write(lim.Flush(lb[:0])); err != nil {
			out.Abort()
			return 0, err
		}
	}
	frames := out.Frames()
	return frames, out.Close()
}

// --- the check ----------------------------------------------------------------------------

// qc listens to every rendered line of a track again (speech to text)
// and flags the lines that do not say their text, and lines that run
// into the next.
func (s *Dubs) qc(ctx context.Context, sc *stageCtx) error {
	p := sc.p
	l := sc.params.Lang
	m := s.cfg.Models()
	if m.STT == "" {
		return stageErr(proto.CodeModelNotDownloaded, "No speech-to-text model.")
	}
	t := p.Track(l)
	trackSTT, _ := m.STTFor(baseLang(l))
	rep := &dub.QCReport{At: s.cfg.Now(), Keys: dub.QCKeys{}, Model: trackSTT}
	type placed struct {
		id         string
		start, end float64
	}
	var spans []placed
	ids := make([]string, 0, len(t.Lines))
	for _, seg := range p.Segments {
		if r, ok := t.Lines[seg.ID]; ok && r.File != "" && r.Err == "" {
			ids = append(ids, seg.ID)
		}
	}
	for n, id := range ids {
		seg, _ := p.Segment(id)
		r := t.Lines[id]
		sc.progress("transcribing", float64(n)/float64(max(len(ids), 1)), fmt.Sprintf("%d/%d", n+1, len(ids)))
		lineLang := baseLang(dub.LangFor(*seg, l))
		stt, _ := m.STTFor(lineLang)
		tp := proto.TranscribeParams{Model: stt, Audio: sc.path(r.File), Language: lineLang}
		var tr proto.TranscribeResult
		if err := sc.call(ctx, proto.MethodSTTTranscribe, stt, &tp, &tp.ModelPath, &tr, nil); err != nil {
			return err
		}
		q := dub.CheckLine(id, r.Text, tr.Text, r.Length, seg.Duration())
		q.MeasuredAt = r.Start
		rep.Lines = append(rep.Lines, q)
		rep.Keys[id] = r.Key
		if q.Flagged {
			rep.Flagged++
		}
		spans = append(spans, placed{id, r.Start, r.Start + r.Length})
	}
	for k := 0; k+1 < len(spans); k++ {
		if spans[k].end > spans[k+1].start+0.05 {
			rep.Overlap = append(rep.Overlap, spans[k].id)
		}
	}
	return sc.save(ctx, func(q *dub.Project) {
		tr := q.Track(l)
		tr.QC = rep
		q.PutTrack(tr)
	})
}

// baseLang is a tag's language ("pt-BR" → "pt").
func baseLang(tag string) string {
	b, _, _ := strings.Cut(tag, "-")
	return b
}
