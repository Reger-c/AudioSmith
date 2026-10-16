package main

// Editing and exporting dubs for agents (thread #35): lines and the cast
// changed as the editor changes them, and exports queued as the export
// panel queues them.

import (
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"slices"
	"strings"

	"github.com/hehehai/AudioSmith/internal/api"
	"github.com/hehehai/AudioSmith/internal/core"
	"github.com/hehehai/AudioSmith/internal/dub"
	"github.com/hehehai/AudioSmith/internal/jobs"
	"github.com/hehehai/AudioSmith/internal/services"
)

// EditDub changes lines and the cast, as the editor does.
func (b *apiBackend) EditDub(ctx context.Context, req api.DubEdit) (api.DubJSON, error) {
	if b.dubs == nil {
		return api.DubJSON{}, errNoModule("Dubbing")
	}
	if active, _ := b.dubs.Active(ctx, req.ID); len(active) > 0 {
		return api.DubJSON{}, errBusy("The dub is running: wait for get_job %s, or cancel it, before editing.", services.DubOwner+req.ID)
	}
	voices, err := b.voices(ctx)
	if err != nil {
		return api.DubJSON{}, err
	}
	_, err = b.dubs.Update(ctx, req.ID, func(p *dub.Project) error {
		for _, ed := range req.Segments {
			s, ok := p.Segment(ed.ID)
			if !ok {
				return errBadRequest("segments", "The dub has no line %q (get_dub lists them).", ed.ID)
			}
			if t := strings.TrimSpace(ed.Text); t != "" {
				p.SetText(ed.ID, "", t)
			}
			if t := strings.TrimSpace(ed.Trans); t != "" {
				l := ed.Language
				if l == "" && len(p.Targets) > 0 {
					l = p.Targets[0]
				}
				if !slices.Contains(p.Languages(), l) {
					return errBadRequest("segments", "The dub is not into %s.", l)
				}
				if s, ok = p.Segment(ed.ID); ok {
					l = dub.LangFor(*s, l)
				}
				if l == p.SourceLanguage() {
					l = ""
				}
				p.SetText(ed.ID, l, t)
			}
			if s, ok = p.Segment(ed.ID); ok {
				if sp := strings.TrimSpace(ed.Speaker); sp != "" {
					s.Speaker = sp
				}
				if ed.Voice != "" {
					ref, err := dubVoiceRef(voices, ed.Voice, "segments")
					if err != nil {
						return err
					}
					s.Voice = ref
				}
			}
		}
		for label, v := range req.Cast {
			ref, err := dubVoiceRef(voices, v, "cast")
			if err != nil {
				return err
			}
			if label != dub.CastAll && !slices.Contains(p.SpeakerLabels(), label) {
				return errBadRequest("cast", "The dub has no speaker %q; its speakers are %s (or * for everyone).", label, strings.Join(p.SpeakerLabels(), ", "))
			}
			if p.Cast == nil {
				p.Cast = map[string]string{}
			}
			if ref == "" {
				delete(p.Cast, label)
			} else {
				p.Cast[label] = ref
			}
		}
		for label, name := range req.Names {
			if !slices.Contains(p.SpeakerLabels(), label) {
				return errBadRequest("names", "The dub has no speaker %q.", label)
			}
			if p.Names == nil {
				p.Names = map[string]string{}
			}
			p.Names[label] = strings.TrimSpace(name)
		}
		return nil
	})
	if err != nil {
		return api.DubJSON{}, notFound(err, "dub", req.ID)
	}
	return b.Dub(ctx, api.DubQuery{ID: req.ID})
}

// ExportDub queues an export, as the export panel does.
func (b *apiBackend) ExportDub(ctx context.Context, req api.DubExport) (api.JobJSON, error) {
	if b.dubs == nil {
		return api.JobJSON{}, errNoModule("Dubbing")
	}
	p, err := b.dubs.Get(ctx, req.ID)
	if err != nil {
		return api.JobJSON{}, notFound(err, "dub", req.ID)
	}
	l := req.Language
	if l == "" && len(p.Targets) > 0 {
		l = p.Targets[0]
	}
	er := services.ExportRequest{Dub: p.ID, Format: req.Format, Lang: l, Default: req.Default, Dual: req.Dual, Subtitles: req.Subtitles, Bits: req.Bits}
	if er.Bits == 0 {
		er.Bits = 24
	}
	switch req.Format {
	case dub.ExportSRT, dub.ExportVTT, dub.ExportASS, dub.ExportBurn:
		if len(p.Segments) == 0 {
			return api.JobJSON{}, errNotReady("There is no transcript yet: run_dub through transcript first.")
		}
	case dub.ExportMP4:
		er.Tracks = req.Tracks
		if len(er.Tracks) == 0 {
			er.Tracks = []string{l, dub.OrigTrack}
		}
		for _, t := range er.Tracks {
			if t != dub.OrigTrack && p.Track(t).File == "" {
				return api.JobJSON{}, errNotReady("There is no %s track yet: run_dub first.", t)
			}
		}
		if !p.Source.HasVideo && p.Source.Video == nil && p.Media.Audio != "" {
			return api.JobJSON{}, errBadRequest("format", "The source has no video: export wav or m4a.")
		}
		er.Lang = ""
	case dub.ExportSegments:
		if len(p.Track(l).Lines) == 0 {
			return api.JobJSON{}, errNotReady("There is no %s track yet: run_dub first.", l)
		}
	}
	switch req.Format {
	case dub.ExportWAV, dub.ExportM4A, dub.ExportMP3, dub.ExportStems:
		if p.Track(l).File == "" {
			return api.JobJSON{}, errNotReady("There is no %s track yet: run_dub first.", l)
		}
	}
	if need := services.ExportNeeds(req.Format); need == "ffmpeg" && !b.tool().CanEncode("mp3") {
		return api.JobJSON{}, apiErr(http.StatusBadRequest, api.CodeUnsupportedFormat, "format",
			"%s needs ffmpeg: the user can set it up in AudioSmith → Settings → Audio tools.", req.Format)
	}
	name := l
	switch {
	case req.Format == dub.ExportMP4:
		name = ""
	case l == dub.OrigTrack:
		name = "orig"
	}
	ext := dub.ExportExt(req.Format)
	er.Output = filepath.Join(req.Dir, api.SafeName(req.Filename, dub.ExportName(p.Title, name, req.Format), ext))
	id, err := b.dubs.ExportJob(ctx, er)
	if err != nil {
		return api.JobJSON{}, err
	}
	lib, title := b.lib.svc.Library, p.Title
	b.whenDone(id, func(j jobs.Job) {
		if j.State != jobs.StateSucceeded {
			return
		}
		var res services.ExportResult
		if json.Unmarshal(j.Result, &res) == nil && res.Output != "" {
			lib.RecordExport(context.Background(), core.Export{Kind: "dub", SourceID: p.ID, Title: title, Path: res.Output, Format: res.Format, Bytes: res.Bytes})
		}
	})
	j, err := b.lib.sched.Get(ctx, id)
	if err != nil {
		return api.JobJSON{}, err
	}
	out := api.GroupJob(id, j.Kind, j.Title, j.State, j.Progress.Stage, j.Progress.Fraction, "", j.Err, nil, nil, nil)
	out.Owner, out.Lane = j.Owner, string(j.Lane)
	out.Files = []string{er.Output}
	return out, nil
}
