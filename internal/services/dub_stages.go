package services

// The dub stages before the transcript: fetch, ingest, separate,
// transcribe, references.

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"github.com/hehehai/AudioSmith/internal/dsp"
	"github.com/hehehai/AudioSmith/internal/dsp/analysis"
	"github.com/hehehai/AudioSmith/internal/dsp/wav"
	"github.com/hehehai/AudioSmith/internal/dub"
	"github.com/hehehai/AudioSmith/internal/engine/proto"
	"github.com/hehehai/AudioSmith/internal/jobs"
)

// Files of a dub project, in its directory.
const (
	dubAudio      = "audio.wav"     // the source's audio, ≤ 48 kHz, ≤ 2 channels
	dubSpeech     = "speech.wav"    // 16 kHz mono
	dubVocals     = "vocals.wav"    // separated voices
	dubVocals16k  = "vocals16k.wav" // 16 kHz mono of them
	dubBackground = "background.wav"
	dubPeaks      = "audio.peaks"
	dubPoster     = "poster.jpg"
	dubProxy      = "preview.mp4"
)

// cookies are cookies.txt files for URL downloads, kept in memory only
// (never in the project or a job's params), by project id.
var cookies sync.Map

// SetCookies gives project id's next download a cookies.txt ("" = none).
func (s *Dubs) SetCookies(id, path string) {
	if path == "" {
		cookies.Delete(id)
		return
	}
	cookies.Store(id, path)
}

// fetch downloads a URL source with yt-dlp.
func (s *Dubs) fetch(ctx context.Context, sc *stageCtx) error {
	p := sc.p
	var y dub.YtDlp
	if s.cfg.YtDlp != nil {
		y = s.cfg.YtDlp()
	}
	if y.Path == "" {
		return stageErr(dub.CodeNoYtDlp, "URL import is off: turn it on in Settings → Dubbing.")
	}
	if _, err := dub.CheckURL(ctx, p.Source.URL, dub.LookupHost); err != nil {
		return urlErr(err)
	}
	sc.progress("downloading", -1, "")
	info, err := y.Info(ctx, p.Source.URL)
	if err != nil {
		return urlErr(err)
	}
	o := dub.DownloadOptions{}
	if p.Source.WantCaptions {
		o.Captions = p.SourceLang
		if o.Captions == "" {
			o.Captions = dubFirst(info.Language, "en")
		}
	}
	if c, ok := cookies.LoadAndDelete(p.ID); ok {
		o.Cookies = c.(string)
	}
	video, captions, err := y.Download(ctx, p.Source.URL, filepath.Join(sc.dir(), "download"), o, func(f float64) {
		sc.progress("downloading", f, "")
	})
	if err != nil {
		return urlErr(err)
	}
	fi, _ := os.Stat(video)
	return sc.save(ctx, func(p *dub.Project) {
		p.Source.Path = video
		p.Source.Captions = captions
		if fi != nil {
			p.Source.Size = fi.Size()
		}
		if info.Title != "" {
			p.Source.Name = info.Title
			if p.Title == "" || p.Title == p.Source.URL {
				p.Title = info.Title
			}
		}
		if p.SourceLang == "" && info.Language != "" {
			p.Detected = info.Language
		}
		p.AutoRetarget() // a defaulted target is not the spoken language
	})
}

func urlErr(err error) error {
	var ue *dub.URLError
	if errors.As(err, &ue) {
		data, _ := json.Marshal(map[string]string{"detail": ue.Detail})
		return &jobs.Error{Code: ue.Code, Message: ue.Error(), Data: data}
	}
	return err
}

// playable reports whether the web view plays a video as it is.
func playable(path string, codec string) bool {
	switch strings.ToLower(strings.TrimPrefix(filepath.Ext(path), ".")) {
	case "mp4", "mov", "m4v":
	default:
		return false
	}
	switch strings.ToLower(codec) {
	case "", "h264", "avc1", "hevc", "hvc1", "hev1", "h265", "mp4v", "mpeg4":
		return true
	}
	return false
}

// ingest probes and decodes the source, and makes what the workspace
// draws: the waveform pyramid, speech onsets, a poster and a preview.
func (s *Dubs) ingest(ctx context.Context, sc *stageCtx) error {
	p := sc.p
	src := p.Source.Path
	if src == "" {
		return stageErr(DubCodeNoSource, "The dub has no source file.")
	}
	fi, err := os.Stat(src)
	if err != nil {
		data, _ := json.Marshal(map[string]string{"path": src})
		return &jobs.Error{Code: proto.CodeFileNotFound, Message: err.Error(), Data: data}
	}
	if err := os.MkdirAll(sc.dir(), 0o755); err != nil {
		return err
	}
	sc.progress("decoding", -1, "")
	isWAV := strings.EqualFold(filepath.Ext(src), ".wav")
	var pr proto.ProbeResult
	if isWAV {
		w, err := dub.OpenWAV(src, 0, 0)
		if err != nil {
			data, _ := json.Marshal(map[string]string{"path": src})
			return &jobs.Error{Code: proto.CodeUnsupportedMedia, Message: err.Error(), Data: data}
		}
		rate, ch := w.Format()
		pr = proto.ProbeResult{Format: "wav", HasAudio: true, AudioTracks: 1, Codec: "lpcm", SampleRate: rate, Channels: ch,
			Duration: float64(w.Frames()) / float64(rate)}
		w.Close()
	} else if err := sc.call(ctx, proto.MethodMediaProbe, "", proto.ProbeParams{Input: src}, nil, &pr, nil); err != nil {
		return err
	}
	if !pr.HasAudio {
		return stageErr("dub.no_audio", "The file has no sound to dub.")
	}
	rate := pr.SampleRate
	if rate <= 0 || rate > 48000 {
		rate = 48000
	}
	ch := min(max(pr.Channels, 1), 2)
	audio := sc.path(dubAudio)
	if isWAV {
		if _, err := convertStream(ctx, src, audio, rate, ch, wav.PCM16, nil); err != nil {
			return err
		}
	} else {
		dp := proto.DecodeParams{Input: src, Output: proto.Output{Path: audio, Format: "wav", Bits: 16}, SampleRate: rate, Channels: ch}
		var dr proto.DecodeResult
		if err := sc.call(ctx, proto.MethodMediaDecode, "", dp, nil, &dr, func(pg jobs.Progress) {
			pg.Stage = "decoding"
			sc.report(pg)
		}); err != nil {
			return err
		}
	}
	// The speech copy, the waveform and the length.
	sc.progress("analysing", -1, "")
	pb := dub.NewPeakBuilder(rate, ch)
	frames, err := convertStream(ctx, audio, sc.path(dubSpeech), 16000, 1, wav.PCM16, nil)
	if err != nil {
		return err
	}
	if err := eachBlock(ctx, audio, func(b []float32) error { pb.Write(b); return nil }); err != nil {
		return err
	}
	if err := dub.WritePeaks(sc.path(dubPeaks), pb.Peaks()); err != nil {
		return err
	}
	onsets, err := speechOnsets(ctx, sc.path(dubSpeech))
	if err != nil {
		return err
	}
	duration := float64(frames) / 16000
	if pr.Duration > 0 && !isWAV {
		duration = pr.Duration
	}
	// Video: a poster and, for codecs the web view cannot play, a proxy.
	poster, proxy := "", ""
	if pr.HasVideo {
		tp := dub.ThumbnailParams{Input: src, Times: []float64{min(1, duration*0.1)}, Outputs: []string{sc.path(dubPoster)}, Width: 640}
		var tr dub.ThumbnailResult
		switch err := sc.call(ctx, dub.MethodThumbnail, "", tp, nil, &tr, nil); {
		case err == nil && len(tr.Frames) > 0:
			poster = dubPoster
		case err == nil || missing(err) || code(err) == proto.CodeUnsupportedMedia:
			sc.warn(DubWarnNoPoster)
		default:
			return err
		}
		codec := ""
		if pr.Video != nil {
			codec = pr.Video.Codec
		}
		if !playable(src, codec) {
			xp := dub.TranscodeParams{Input: src, Output: proto.Output{Path: sc.path(dubProxy), Format: "mp4"}, MaxHeight: 720}
			var xr dub.TranscodeResult
			switch err := sc.call(ctx, dub.MethodTranscode, "", xp, nil, &xr, func(pg jobs.Progress) {
				pg.Stage = "transcoding"
				sc.report(pg)
			}); {
			case err == nil:
				proxy = dubProxy
			case missing(err) || code(err) == proto.CodeUnsupportedMedia:
				sc.warn(DubWarnNoProxy)
			default:
				return err
			}
		}
	}
	return sc.save(ctx, func(p *dub.Project) {
		p.Source.Size, p.Source.Duration, p.Source.HasVideo, p.Source.Audio = fi.Size(), duration, pr.HasVideo, pr.Codec
		if pr.Video != nil {
			p.Source.Video = &dub.VideoInfo{Codec: pr.Video.Codec, Width: pr.Video.Width, Height: pr.Video.Height, FPS: pr.Video.FPS}
		}
		p.Media.Audio, p.Media.Speech, p.Media.Peaks, p.Media.Onsets = dubAudio, dubSpeech, dubPeaks, onsets
		p.Media.Poster, p.Media.Proxy = poster, proxy
	})
}

// convertStream writes src (a WAV) to dst at rate and channels, block by
// block (sources of hours), atomically; also sees each block written.
func convertStream(ctx context.Context, src, dst string, rate, ch int, enc wav.Encoding, also func([]float32)) (int64, error) {
	in, err := dub.OpenWAV(src, rate, ch)
	if err != nil {
		return 0, err
	}
	defer in.Close()
	out, err := wav.Create(dst, wav.Format{Rate: rate, Channels: ch, Encoding: enc}, wav.Options{Large: true, Dither: enc == wav.PCM16})
	if err != nil {
		return 0, err
	}
	buf := make([]float32, 16384*ch)
	for {
		if err := ctx.Err(); err != nil {
			out.Abort()
			return 0, err
		}
		n, rerr := in.Read(buf)
		if n > 0 {
			if also != nil {
				also(buf[:n])
			}
			if err := out.Write(buf[:n]); err != nil {
				out.Abort()
				return 0, err
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
	frames := out.Frames()
	return frames, out.Close()
}

// speechOnsets finds speech onsets in a 16 kHz mono WAV, a minute at a time.
func speechOnsets(ctx context.Context, path string) ([]float64, error) {
	in, err := dub.OpenWAV(path, 16000, 1)
	if err != nil {
		return nil, err
	}
	defer in.Close()
	const window = 60 * 16000
	buf := make([]float32, window)
	var out []float64
	offset := 0.0
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		n := 0
		for n < window {
			k, err := in.Read(buf[n:])
			n += k
			if err != nil || k == 0 {
				break
			}
		}
		if n == 0 {
			break
		}
		b := dsp.Buffer{Rate: 16000, Channels: 1, Samples: buf[:n]}
		for _, o := range analysis.Onsets(b, analysis.OnsetOptions{}) {
			if o.Kind == "speech" || o.Strength >= 0.3 {
				out = append(out, offset+o.Time)
			}
		}
		offset += float64(n) / 16000
		if n < window {
			break
		}
	}
	return out, nil
}

// separate splits the voices from the music and effects.
func (s *Dubs) separate(ctx context.Context, sc *stageCtx) error {
	model := s.cfg.Models().Separate
	if model == "" {
		sc.warn(DubWarnNoSeparation)
		sc.skipped = DubWarnNoSeparation
		return nil
	}
	sp := dub.SeparateParams{Model: model, Audio: sc.path(dubAudio),
		Vocals:     proto.Output{Path: sc.path(dubVocals), Format: "wav", Bits: 16},
		Background: proto.Output{Path: sc.path(dubBackground), Format: "wav", Bits: 16}}
	var res dub.SeparateResult
	err := sc.call(ctx, dub.MethodSeparate, model, &sp, &sp.ModelPath, &res, func(pg jobs.Progress) {
		if pg.Stage == "" || pg.Stage == "working" {
			pg.Stage = "separating"
		}
		sc.report(pg)
	})
	if missing(err) {
		sc.warn(DubWarnNoSeparation)
		sc.skipped = DubWarnNoSeparation
		return nil
	}
	if err != nil {
		return err
	}
	if _, err := convertStream(ctx, sc.path(dubVocals), sc.path(dubVocals16k), 16000, 1, wav.PCM16, nil); err != nil {
		return err
	}
	return sc.save(ctx, func(p *dub.Project) {
		p.Media.Vocals, p.Media.Background, p.Media.Separator = dubVocals, dubBackground, model
	})
}

// speechFile is the 16 kHz audio speech models listen to: the separated
// voices when there are.
func speechFile(p *dub.Project) string {
	if p.Media.Vocals != "" {
		return dubVocals16k
	}
	return dubSpeech
}

// transcribe makes the transcript: the downloaded captions, or speech to
// text with word timings; then diarization gives the lines speakers.
func (s *Dubs) transcribe(ctx context.Context, sc *stageCtx) error {
	p := sc.p
	m := s.cfg.Models()
	speech := sc.path(speechFile(p))
	q := p.Clone()
	q.Segments, q.Names, q.NextSeg = nil, nil, 1
	detected := ""
	if p.Source.Captions != "" && !sc.params.Force {
		data, err := os.ReadFile(p.Source.Captions)
		if err == nil {
			q.ImportSubtitles(string(data), p.Source.Duration)
			sc.warn(DubWarnCaptions)
		}
	}
	if len(q.Segments) == 0 {
		if m.STT == "" {
			return stageErr(proto.CodeModelNotDownloaded, "No speech-to-text model.")
		}
		stt, wordTimes := m.STTFor(p.SourceLang)
		tp := proto.TranscribeParams{Model: stt, Audio: speech, Language: p.SourceLang, Words: wordTimes}
		var tr proto.TranscribeResult
		if err := sc.call(ctx, proto.MethodSTTTranscribe, stt, &tp, &tp.ModelPath, &tr, func(pg jobs.Progress) {
			pg.Stage = "transcribing"
			sc.report(pg)
		}); err != nil {
			return err
		}
		if tr.AudioSeconds <= 0 {
			tr.AudioSeconds = p.Source.Duration
		}
		CleanTranscription(stt, &tr)
		detected = tr.Language
		words := segmentWords(tr.Segments)
		if len(words) == 0 && m.Align != "" && strings.TrimSpace(tr.Text) != "" {
			ap := proto.AlignParams{Model: m.Align, Audio: speech, Text: strings.TrimSpace(tr.Text), Language: dubFirst(tr.Language, p.SourceLang)}
			aw, err := alignWords(ctx, func(ap *proto.AlignParams, out *proto.AlignResult) error {
				return sc.call(ctx, proto.MethodSTTAlign, m.Align, ap, &ap.ModelPath, out, func(pg jobs.Progress) {
					pg.Stage = "aligning"
					sc.report(pg)
				})
			}, ap, tr.Segments, tr.AudioSeconds)
			switch {
			case ctx.Err() != nil:
				return ctx.Err()
			case err != nil:
				sc.warn(WarnNoWordTimes)
			default:
				words = aw
			}
		}
		for _, ts := range BuildSegments(tr.Segments, words, p.Source.Duration) {
			if strings.TrimSpace(ts.Text) == "" {
				continue
			}
			seg := q.NewSegment(ts.Start, ts.End, "", strings.TrimSpace(ts.Text))
			for _, w := range ts.Words {
				seg.Words = append(seg.Words, dub.Word{Text: w.Text, Start: w.Start, End: w.End})
			}
			q.Segments = append(q.Segments, seg)
		}
	}
	if len(q.Segments) == 0 {
		return stageErr(DubCodeNoSpeech, "No speech was found.")
	}
	// Who speaks.
	var turns []dub.Turn
	if p.Speakers != 1 && !slices.ContainsFunc(q.Segments, func(s dub.Segment) bool { return s.Speaker != "" && q.Names != nil }) {
		if m.Diarize == "" {
			sc.warn(DubWarnNoDiarization)
		} else {
			dp := dub.DiarizeParams{Model: m.Diarize, Audio: speech, Speakers: p.Speakers}
			var dr dub.DiarizeResult
			err := sc.call(ctx, dub.MethodDiarize, m.Diarize, &dp, &dp.ModelPath, &dr, func(pg jobs.Progress) {
				pg.Stage = "diarizing"
				sc.report(pg)
			})
			switch {
			case ctx.Err() != nil:
				return ctx.Err()
			case code(err) == proto.CodeUnsupported:
				sc.warn(DubWarnTooManySpk)
			case missing(err):
				sc.warn(DubWarnNoDiarization)
			case err != nil:
				return err
			default:
				turns = dr.Turns
			}
		}
	}
	if q.Names == nil {
		q.AssignSpeakers(turns)
	}
	return sc.save(ctx, func(p *dub.Project) {
		p.Segments, p.NextSeg, p.Names = q.Segments, q.NextSeg, q.Names
		if detected != "" {
			p.Detected = detected
		}
		p.AutoRetarget() // a defaulted target is not the spoken language
		// A new transcript: translations, renders and references go.
		p.Tracks, p.References, p.RefTried = nil, nil, nil
		for k := range p.Stages {
			if !strings.HasPrefix(k, string(dub.StageIngest)) && !strings.HasPrefix(k, string(dub.StageSeparate)) &&
				!strings.HasPrefix(k, string(dub.StageFetch)) && !strings.HasPrefix(k, string(dub.StageTranscribe)) {
				delete(p.Stages, k)
			}
		}
	})
}

// references picks each speaker's clone reference and cleans it.
func (s *Dubs) references(ctx context.Context, sc *stageCtx) error {
	p := sc.p
	m := s.cfg.Models()
	scoring, _, err := wav.ReadFile(sc.path(speechFile(p)))
	if err != nil {
		return err
	}
	src := dubAudio
	if p.Media.Vocals != "" {
		src = dubVocals
	}
	cands := dub.PickReferences(p.Segments, scoring)
	refs := map[string]dub.Reference{}
	labels := p.SpeakerLabels()
	if err := os.MkdirAll(sc.path("refs"), 0o755); err != nil {
		return err
	}
	for i, sp := range labels {
		c, ok := cands[sp]
		if !ok {
			sc.warn(DubWarnNoReference)
			continue
		}
		sc.progress("references", float64(i)/float64(len(labels)), sp)
		name := filepath.Join("refs", dub.SafeName(sp)+".wav")
		if err := cutClip(sc.path(src), sc.path(name), c.Start, c.End); err != nil {
			return err
		}
		ref := dub.Reference{File: name, Start: c.Start, End: c.End, Text: c.Text, Score: c.Score}
		if p.Media.Vocals == "" && m.Enhance != "" {
			clean := filepath.Join("refs", dub.SafeName(sp)+".clean.wav")
			ep := proto.EnhanceParams{Model: m.Enhance, Audio: sc.path(name), Output: proto.Output{Path: sc.path(clean), Format: "wav", Bits: 16}}
			var er proto.EnhanceResult
			err := sc.call(ctx, proto.MethodEnhanceRun, m.Enhance, &ep, &ep.ModelPath, &er, func(pg jobs.Progress) {
				pg.Stage = "enhancing"
				sc.report(pg)
			})
			switch {
			case ctx.Err() != nil:
				return ctx.Err()
			case err == nil:
				ref.File, ref.Enhanced = clean, true
			case missing(err):
				sc.warn(DubWarnNoEnhance)
			default:
				return err
			}
		}
		refs[sp] = ref
	}
	return sc.save(ctx, func(p *dub.Project) {
		p.References, p.RefTried = refs, labels
	})
}

// cutClip writes start–end of src (a WAV) to dst as mono.
func cutClip(src, dst string, start, end float64) error {
	in, err := dub.OpenWAV(src, 0, 1)
	if err != nil {
		return err
	}
	defer in.Close()
	rate, _ := in.Format()
	if err := in.SeekFrame(int64(start * float64(rate))); err != nil {
		return err
	}
	n := max(int((end-start)*float64(rate)), 0)
	buf := make([]float32, n)
	got := 0
	for got < n {
		k, err := in.Read(buf[got:])
		got += k
		if err != nil || k == 0 {
			break
		}
	}
	b := dsp.Buffer{Rate: rate, Channels: 1, Samples: buf[:got]}
	dsp.FadeIn(b, dsp.FramesAt(0.01, rate), dsp.EqualPower)
	dsp.FadeOut(b, dsp.FramesAt(0.01, rate), dsp.EqualPower)
	tmp := dst + ".tmp"
	if err := wav.WriteFile(tmp, b, wav.PCM24, wav.Options{}); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, dst)
}
