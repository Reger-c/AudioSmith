package services

// Reading what a dub's source is before it is downloaded or decoded
// (thread #106), so the Dub page shows it at once: for a URL, what the
// site says of the video (yt-dlp's metadata: dub.SourceMeta) and its
// cover; for a file, its length, picture and a poster frame (the engine's
// media.probe and thumbnail, as ingest does). Probe is no job: it is
// quick, the run's stages do not wait for it, and the fetch stage reads
// the link itself when the probe has not. Cancel stops it as it stops a
// run, and so does the quit (CancelProbes).

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/hehehai/AudioSmith/internal/dub"
	"github.com/hehehai/AudioSmith/internal/engine/proto"
	"github.com/hehehai/AudioSmith/internal/jobs"
)

// probeTimeout bounds a probe (yt-dlp takes some seconds on most sites).
var probeTimeout = 60 * time.Second

// fetchThumbnail fetches a cover (tests replace it).
var fetchThumbnail = dub.FetchThumbnail

// probeRun is a probe going on.
type probeRun struct {
	cancel context.CancelFunc
	done   chan struct{}
}

// errStale: the project's source changed while it was probed.
var errStale = errors.New("dub: the source changed")

// Probe reads what project id's source is and saves it: a URL's metadata
// (Source.Meta, then its cover, Meta.Thumbnail), or a file's length,
// picture and poster. A link that cannot be read (a site yt-dlp does not
// know, a private video, one that needs a sign-in) saves why in
// Meta.Error and returns it. A source already downloaded or read is left
// alone. A new probe of the project replaces one going on.
func (s *Dubs) Probe(ctx context.Context, id string) error {
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	run := &probeRun{cancel: cancel, done: make(chan struct{})}
	s.probeMu.Lock()
	if s.probes == nil {
		s.probes = map[string]*probeRun{}
	}
	if old := s.probes[id]; old != nil {
		old.cancel()
	}
	s.probes[id] = run
	s.probeMu.Unlock()
	defer func() {
		cancel()
		s.probeMu.Lock()
		if s.probes[id] == run {
			delete(s.probes, id)
		}
		s.probeMu.Unlock()
		close(run.done)
	}()
	p, err := s.Get(ctx, id)
	if err != nil {
		return err
	}
	switch {
	case p.Source.Kind == dub.SourceURL && p.Source.Path == "" && p.Source.URL != "":
		return s.probeURL(ctx, p)
	case p.Source.Path != "" && p.Media.Audio == "":
		return s.probeFile(ctx, p)
	}
	return nil
}

// Probing reports whether project id's source is being probed.
func (s *Dubs) Probing(id string) bool {
	s.probeMu.Lock()
	defer s.probeMu.Unlock()
	return s.probes[id] != nil
}

// cancelProbe stops project id's probe and returns what closes when it
// ended (nil: none).
func (s *Dubs) cancelProbe(id string) <-chan struct{} {
	s.probeMu.Lock()
	defer s.probeMu.Unlock()
	if r := s.probes[id]; r != nil {
		r.cancel()
		return r.done
	}
	return nil
}

// CancelProbes stops every probe (the app quits). It does not wait.
func (s *Dubs) CancelProbes() {
	s.probeMu.Lock()
	defer s.probeMu.Unlock()
	for _, r := range s.probes {
		r.cancel()
	}
}

// probeURL asks yt-dlp about the link, saves what it says, then fetches
// the cover into the project's folder.
func (s *Dubs) probeURL(ctx context.Context, p *dub.Project) error {
	var y dub.YtDlp
	if s.cfg.YtDlp != nil {
		y = s.cfg.YtDlp()
	}
	if y.Path == "" {
		return nil // URL import is off: the page says so
	}
	u := p.Source.URL
	info, err := dub.URLInfo{}, error(nil)
	if _, err = dub.CheckURL(ctx, u, dub.LookupHost); err == nil {
		info, err = y.Info(ctx, u)
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	m := info.Meta
	if err != nil {
		m = dub.SourceMeta{Error: dub.CodeYtDlp, Detail: err.Error()}
		var ue *dub.URLError
		if errors.As(err, &ue) {
			m.Error, m.Detail = ue.Code, ue.Detail
		}
	}
	m.At = s.cfg.Now()
	if serr := s.saveMeta(ctx, p.ID, u, m); serr != nil || err != nil {
		return errors.Join(err, serr)
	}
	if m.Thumb == "" {
		return nil
	}
	name, err := fetchThumbnail(ctx, m.Thumb, s.Dir(p.ID), dub.ThumbOptions{Proxy: y.Proxy})
	if err != nil {
		if ctx.Err() == nil {
			s.cfg.Logf("dub %s: cover: %v", p.ID, err)
		}
		return nil // the page shows the rest
	}
	_, err = s.Update(ctx, p.ID, func(p *dub.Project) error {
		if p.Source.URL != u || p.Source.Meta == nil {
			return errStale
		}
		p.Source.Meta.Thumbnail = name
		return nil
	})
	if errors.Is(err, errStale) {
		return nil
	}
	return err
}

// saveMeta keeps a URL's metadata in its project (and names the dub
// after the video), unless the source changed meanwhile.
func (s *Dubs) saveMeta(ctx context.Context, id, u string, m dub.SourceMeta) error {
	_, err := s.Update(ctx, id, func(p *dub.Project) error {
		if p.Source.URL != u || p.Source.Kind != dub.SourceURL {
			return errStale
		}
		if old := p.Source.Meta; old != nil && old.Thumbnail != "" && m.Error == "" {
			m.Thumbnail = old.Thumbnail // until the new cover comes
		}
		p.Source.Meta = &m
		if m.Error != "" {
			return nil
		}
		if m.Title != "" {
			if p.Title == "" || p.Title == u {
				p.Title = m.Title
			}
			if p.Source.Name == "" || p.Source.Name == u {
				p.Source.Name = m.Title
			}
		}
		if p.Source.Duration == 0 && m.Duration > 0 {
			p.Source.Duration = m.Duration
		}
		return nil
	})
	if errors.Is(err, errStale) {
		return nil
	}
	return err
}

// probeFile reads a file's length and picture and makes its poster,
// with the engine's media methods (nothing when the engine is not there
// yet: ingest does it all).
func (s *Dubs) probeFile(ctx context.Context, p *dub.Project) error {
	src := p.Source.Path
	if strings.EqualFold(src[max(len(src)-4, 0):], ".wav") {
		w, err := dub.OpenWAV(src, 0, 0)
		if err != nil {
			return nil // ingest says why
		}
		rate, _ := w.Format()
		d := float64(w.Frames()) / float64(rate)
		w.Close()
		return s.saveFileProbe(ctx, p.ID, src, proto.ProbeResult{HasAudio: true, Duration: d}, "")
	}
	sc := &stageCtx{s: s, p: p, job: jobs.Job{ID: "probe-" + p.ID, Spec: jobs.Spec{Owner: DubOwner + p.ID}}}
	var pr proto.ProbeResult
	if err := sc.call(ctx, proto.MethodMediaProbe, "", proto.ProbeParams{Input: src}, nil, &pr, nil); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return nil // ingest says why
	}
	poster := ""
	if pr.HasVideo {
		tp := dub.ThumbnailParams{Input: src, Times: []float64{min(1, pr.Duration*0.1)}, Outputs: []string{sc.path(dubPoster)}, Width: 640}
		var tr dub.ThumbnailResult
		if err := sc.call(ctx, dub.MethodThumbnail, "", tp, nil, &tr, nil); err == nil && len(tr.Frames) > 0 {
			poster = dubPoster
		} else if ctx.Err() != nil {
			return ctx.Err()
		}
	}
	return s.saveFileProbe(ctx, p.ID, src, pr, poster)
}

func (s *Dubs) saveFileProbe(ctx context.Context, id, src string, pr proto.ProbeResult, poster string) error {
	_, err := s.Update(ctx, id, func(p *dub.Project) error {
		if p.Source.Path != src || p.Media.Audio != "" {
			return errStale // another source, or ingest read it already
		}
		p.Source.Duration, p.Source.HasVideo, p.Source.Audio = pr.Duration, pr.HasVideo, pr.Codec
		if pr.Video != nil {
			p.Source.Video = &dub.VideoInfo{Codec: pr.Video.Codec, Width: pr.Video.Width, Height: pr.Video.Height, FPS: pr.Video.FPS}
		}
		if poster != "" {
			p.Media.Poster = poster
		}
		return nil
	})
	if errors.Is(err, errStale) {
		return nil
	}
	return err
}

// metaOrInfo is what the fetch stage knows of a link: the probe's
// metadata when it read it, else yt-dlp's answer now (kept, with its
// cover, as the probe would).
func (s *Dubs) metaOrInfo(ctx context.Context, y dub.YtDlp, p *dub.Project) (dub.SourceMeta, error) {
	if m := p.Source.Meta; m != nil && m.Error == "" && !m.At.IsZero() {
		return *m, nil
	}
	info, err := y.Info(ctx, p.Source.URL)
	if err != nil {
		return dub.SourceMeta{}, err
	}
	m := info.Meta
	m.At = s.cfg.Now()
	if err := s.saveMeta(ctx, p.ID, p.Source.URL, m); err != nil {
		return m, err
	}
	if m.Thumb != "" {
		if name, err := fetchThumbnail(ctx, m.Thumb, s.Dir(p.ID), dub.ThumbOptions{Proxy: y.Proxy}); err == nil {
			s.Update(ctx, p.ID, func(p *dub.Project) error {
				if p.Source.Meta != nil {
					p.Source.Meta.Thumbnail = name
				}
				return nil
			})
			m.Thumbnail = name
		}
	}
	return m, nil
}
