package main

// Dubbing and the batch queue for agents (thread #35): projects made as
// the Dub page makes them (Settings → Dubbing's choices), runs and edits
// through the dub service, exports as the export panel queues them.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/hehehai/AudioSmith/internal/api"
	"github.com/hehehai/AudioSmith/internal/catalog"
	"github.com/hehehai/AudioSmith/internal/core"
	"github.com/hehehai/AudioSmith/internal/dub"
	"github.com/hehehai/AudioSmith/internal/jobs"
	"github.com/hehehai/AudioSmith/internal/lang"
	"github.com/hehehai/AudioSmith/internal/services"
	"github.com/hehehai/AudioSmith/internal/translate"
)

// dubStage is the stage a run goes through.
func dubStage(through string) dub.Stage {
	switch through {
	case "transcript":
		return dub.StageReferences
	case "translation":
		return dub.StageTranslate
	case "verify":
		return dub.StageQC
	}
	return dub.StageMix
}

// dubVoiceRef resolves a dub's voice: a saved voice (id or name) → its
// id; "speaker:", "design:", "gallery:" and "preset:" references pass.
func dubVoiceRef(voices []core.Voice, name, param string) (string, error) {
	n := strings.TrimSpace(name)
	if n == "" || isDefault(n) {
		return "", nil
	}
	for _, p := range []string{dub.SpeakerPrefix, dub.DesignPrefix, dub.GalleryPrefix, dub.PresetPrefix} {
		if strings.HasPrefix(n, p) {
			return n, nil
		}
	}
	v, err := savedVoice(voices, n)
	if err != nil {
		return "", err
	}
	if v == nil {
		return "", apiErr(http.StatusBadRequest, api.CodeVoiceNotFound, param, "No saved voice is called %s: list_voices lists them.", n)
	}
	return v.ID, nil
}

// checkLangs checks BCP-47 codes.
func checkLangs(param string, codes []string) error {
	for _, c := range codes {
		if _, ok := lang.Lookup(c); !ok {
			return errBadRequest(param, "%q is not a language code (BCP-47: en, es, zh, ja…).", c)
		}
	}
	return nil
}

// dubReady checks what a run through stage needs: the speech model
// downloaded and its licence accepted, and a translator (Settings →
// Writing help) unless every line is translated.
func (b *apiBackend) dubReady(ctx context.Context, p *dub.Project, through dub.Stage) error {
	if through == dub.StageMix {
		e := entryFor(p.Settings.Model)
		if err := b.modelReady(ctx, e); err != nil {
			return err
		}
	}
	if through == dub.StageTranslate || through == dub.StageMix {
		missing := len(p.Segments) == 0
		for _, l := range p.Languages() {
			if len(p.Untranslated(l)) > 0 {
				missing = true
			}
		}
		if missing {
			if _, err := dubMakeTranslator(b.router, p.Settings); err != nil {
				var te *translate.Error
				if errors.As(err, &te) {
					return apiErr(http.StatusBadRequest, services.DubCodeNoTranslator, "",
						"Translating needs a translation engine: ask the user to set up a local model (Ollama, LM Studio) or a provider in AudioSmith → Settings → Writing help.")
				}
				return err
			}
		}
	}
	return nil
}

// CreateDub makes a dub of a file, as dropping it on the Dub page does,
// and starts it (through: transcript, translation, dub; none: not yet).
func (b *apiBackend) CreateDub(ctx context.Context, req api.DubCreate) (api.DubJSON, error) {
	if b.dubs == nil {
		return api.DubJSON{}, errNoModule("Dubbing")
	}
	if !dub.IsMedia(req.Path, dub.MediaExts) {
		return api.DubJSON{}, apiErr(http.StatusBadRequest, api.CodeUnsupportedFormat, "path", "%s is not a video or audio file AudioSmith dubs (%s).", filepath.Base(req.Path), strings.Join(dub.MediaExts, ", "))
	}
	src := dub.Source{Kind: dub.SourceFile, Path: req.Path, Name: filepath.Base(req.Path)}
	if fi, err := os.Stat(req.Path); err == nil {
		src.Size = fi.Size()
	}
	p := newDubProject(src, b.lib.settings.Get().Dubbing, dubDefaultModelBy(b.onDisk))
	if t := strings.TrimSpace(req.Title); t != "" {
		p.Title = t
	}
	if sl := strings.TrimSpace(req.SourceLanguage); sl != "" {
		if err := checkLangs("source_language", []string{sl}); err != nil {
			return api.DubJSON{}, err
		}
		p.SourceLang = sl
	}
	if len(req.Targets) > 0 {
		if err := checkLangs("target_languages", req.Targets); err != nil {
			return api.DubJSON{}, err
		}
		p.Targets = slices.Clone(req.Targets)
	}
	if req.Model != "" {
		e, err := ttsEntry(req.Model, "")
		if err != nil {
			return api.DubJSON{}, err
		}
		p.Settings.Model = e.Repo
	}
	if req.Voice != "" {
		voices, err := b.voices(ctx)
		if err != nil {
			return api.DubJSON{}, err
		}
		ref, err := dubVoiceRef(voices, req.Voice, "voice")
		if err != nil {
			return api.DubJSON{}, err
		}
		if ref != "" {
			p.Cast = map[string]string{dub.CastAll: ref}
		}
	}
	p.Speakers = req.Speakers
	if req.KeepBackground != nil {
		p.Settings.KeepBackground = *req.KeepBackground
	}
	through := req.Through
	if through != "none" {
		if err := b.dubReady(ctx, p, dubStage(through)); err != nil {
			return api.DubJSON{}, err
		}
	}
	created, err := b.dubs.Create(ctx, p)
	if err != nil {
		return api.DubJSON{}, err
	}
	if through != "none" {
		if _, err := b.dubs.Run(ctx, created.ID, services.RunRequest{Through: dubStage(through), Priority: jobs.PriorityNormal}); err != nil {
			return api.DubJSON{}, err
		}
	}
	return b.Dub(ctx, api.DubQuery{ID: created.ID})
}

// Dub reads a dub: its stages, speakers, tracks and lines in a language.
func (b *apiBackend) Dub(ctx context.Context, q api.DubQuery) (api.DubJSON, error) {
	if b.dubs == nil {
		return api.DubJSON{}, errNoModule("Dubbing")
	}
	p, err := b.dubs.Get(ctx, q.ID)
	if err != nil {
		return api.DubJSON{}, notFound(err, "dub", q.ID)
	}
	active, _ := b.dubs.Active(ctx, p.ID)
	out := api.DubJSON{ID: p.ID, Title: p.Title, Source: firstOf(p.Source.Path, p.Source.URL), Duration: round1(p.Source.Duration),
		SourceLang: p.SourceLang, Detected: p.Detected, Targets: p.Targets, Phase: string(p.Phase()), Translator: p.Settings.Translator,
		Batch: p.Batch != "", Running: len(active) > 0, JobID: services.DubOwner + p.ID, Updated: p.UpdatedAt}
	if e, ok := catalog.Builtin().ByRepo(p.Settings.Model); ok {
		out.Model = e.ID
	} else {
		out.Model = p.Settings.Model
	}
	voices, _ := b.voices(ctx)
	label := func(ref string) string {
		if n := voiceLabel(voices, ref); n != "" {
			return n
		}
		return ref
	}
	for _, sp := range p.SpeakerLabels() {
		ref := p.Cast[sp]
		if ref == "" {
			ref = p.Cast[dub.CastAll]
		}
		out.Speakers = append(out.Speakers, api.DubSpeakerJSON{Label: sp, Name: p.Names[sp], Voice: label(ref)})
	}
	for k, st := range p.Stages {
		if out.Stages == nil {
			out.Stages = map[string]api.DubStageJSON{}
		}
		out.Stages[k] = api.DubStageJSON{Status: string(st.Status), Code: st.Code, Message: st.Message, Warnings: st.Warnings}
	}
	for l, t := range p.Tracks {
		if out.Tracks == nil {
			out.Tracks = map[string]api.DubTrackJSON{}
		}
		tj := api.DubTrackJSON{File: b.dubs.Path(p.ID, t.File), Speech: b.dubs.Path(p.ID, t.Speech), Seconds: round1(t.Seconds)}
		if t.QC != nil {
			tj.Flagged = t.QC.Flagged
		}
		out.Tracks[l] = tj
	}
	if q.Segments != nil && !*q.Segments {
		return out, nil
	}
	l := q.Language
	if l == "" && len(p.Targets) > 0 {
		l = p.Targets[0]
	}
	out.Language = l
	t := p.Track(l)
	for _, s := range p.Segments {
		sj := api.DubSegmentJSON{ID: s.ID, Start: round2(s.Start), End: round2(s.End), Speaker: s.Speaker, Text: s.Text,
			Voice: label(s.Voice)}
		if l != "" {
			sj.Translation = p.TextFor(s, l)
			if ln := s.Lines[dub.LangFor(s, l)]; ln.Err != "" {
				sj.Error = ln.Err
			}
			if r, ok := t.Lines[s.ID]; ok {
				sj.Rendered = r.File != "" && r.Err == ""
				if r.Err != "" {
					sj.Error = r.Err
				}
			}
		}
		out.Segments = append(out.Segments, sj)
	}
	return out, nil
}

// RunDub runs a dub's pipeline as the page's buttons do.
func (b *apiBackend) RunDub(ctx context.Context, req api.DubRun) (api.JobJSON, error) {
	if b.dubs == nil {
		return api.JobJSON{}, errNoModule("Dubbing")
	}
	p, err := b.dubs.Get(ctx, req.ID)
	if err != nil {
		return api.JobJSON{}, notFound(err, "dub", req.ID)
	}
	if len(req.Languages) > 0 {
		for _, l := range req.Languages {
			if !slices.Contains(p.Languages(), l) {
				return api.JobJSON{}, errBadRequest("languages", "The dub is not into %s: its languages are %s.", l, strings.Join(p.Languages(), ", "))
			}
		}
	}
	through := dubStage(req.Through)
	if req.Through == "verify" && !slices.ContainsFunc(p.Languages(), func(l string) bool { return p.Tracks[l].File != "" }) {
		return api.JobJSON{}, errNotReady("There is no dubbed track to verify yet: run the dub first.")
	}
	if err := b.dubReady(ctx, p, through); err != nil {
		return api.JobJSON{}, err
	}
	rr := services.RunRequest{Through: through, Langs: req.Languages, Only: req.Segments, Priority: jobs.PriorityNormal}
	if req.Retranslate {
		rr.Force = append(rr.Force, dub.StageTranslate)
	}
	if req.Retranscribe {
		rr.Force = append(rr.Force, dub.StageTranscribe)
	}
	if _, err := b.dubs.Run(ctx, p.ID, rr); err != nil {
		if errors.Is(err, core.ErrBusy) {
			return api.JobJSON{}, errBusy("The dub is running: follow get_job %s, or cancel it.", services.DubOwner+p.ID)
		}
		return api.JobJSON{}, err
	}
	return b.dubTask(ctx, p.ID)
}

// dubTask is a dub's job: "dub:<id>", running while it has jobs.
func (b *apiBackend) dubTask(ctx context.Context, id string) (api.JobJSON, error) {
	if b.dubs == nil {
		return api.JobJSON{}, errNoModule("Dubbing")
	}
	p, err := b.dubs.Get(ctx, id)
	if err != nil {
		return api.JobJSON{}, notFound(err, "dub", id)
	}
	parts, cur := b.activeJobs(ctx, services.DubOwner+id)
	state := jobs.StateSucceeded
	stage, msg, progress := "", "", 1.0
	var jerr *jobs.Error
	if cur != nil {
		state, stage, progress = jobs.StateQueued, cur.Title, -1
		if cur.State == jobs.StateRunning {
			state = jobs.StateRunning
			progress = cur.Progress.Fraction
			msg = cur.Progress.Message
		}
	} else {
		for _, k := range p.Failed() {
			st := p.Stages[k]
			code := firstOf(st.Code, services.DubCodeFailed)
			if code == jobs.CodeCancelled {
				state, jerr = jobs.StateCancelled, &jobs.Error{Code: code, Message: "The run was cancelled: run_dub goes on where it stopped."}
				continue
			}
			state, jerr = jobs.StateFailed, &jobs.Error{Code: code, Message: k + ": " + firstOf(st.Message, code)}
			break
		}
		for _, st := range p.Stages {
			if st.Code == jobs.CodeCancelled && jerr == nil {
				state, jerr = jobs.StateCancelled, &jobs.Error{Code: st.Code, Message: "The run was cancelled: run_dub goes on where it stopped."}
			}
		}
	}
	var files []string
	tracks := map[string]string{}
	for _, l := range p.Languages() {
		if f := p.Tracks[l].File; f != "" {
			files = append(files, b.dubs.Path(p.ID, f))
			tracks[l] = b.dubs.Path(p.ID, f)
		}
	}
	res := map[string]any{"dub_id": p.ID, "phase": p.Phase(), "tracks": tracks, "segments": len(p.Segments)}
	return api.GroupJob(services.DubOwner+id, "dub.run", p.Title, state, stage, progress, msg, jerr, files, res, parts), nil
}

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

// --- the batch queue -------------------------------------------------------------------

// BatchDub adds files to the batch queue with the Batch page's settings
// (the request's override them for these files only; nothing is saved).
func (b *apiBackend) BatchDub(ctx context.Context, req api.BatchRequest) (api.BatchJSON, error) {
	if b.dubs == nil {
		return api.BatchJSON{}, errNoModule("Dubbing")
	}
	bs := b.dubs.BatchSettings(ctx)
	if len(req.Targets) > 0 {
		if err := checkLangs("target_languages", req.Targets); err != nil {
			return api.BatchJSON{}, err
		}
		bs.Targets = slices.Clone(req.Targets)
	}
	if req.SourceLanguage != "" {
		if err := checkLangs("source_language", []string{req.SourceLanguage}); err != nil {
			return api.BatchJSON{}, err
		}
		bs.SourceLang = req.SourceLanguage
	}
	if req.Voice != "" {
		voices, err := b.voices(ctx)
		if err != nil {
			return api.BatchJSON{}, err
		}
		if bs.Voice, err = dubVoiceRef(voices, req.Voice, "voice"); err != nil {
			return api.BatchJSON{}, err
		}
	}
	if req.Model != "" {
		e, err := ttsEntry(req.Model, "")
		if err != nil {
			return api.BatchJSON{}, err
		}
		bs.Model = e.Repo
	}
	if bs.Model == "" {
		bs.Model = dubDefaultModelBy(b.onDisk)
	}
	if req.KeepBackground != nil {
		bs.KeepBackground = *req.KeepBackground
	}
	if len(bs.Targets) == 0 {
		return api.BatchJSON{}, errBadRequest("target_languages", "Give target_languages: the Batch page has none.")
	}
	probe := dub.New("", time.Now())
	probe.Settings.Model, probe.Settings.Translator = bs.Model, firstOf(bs.Translator, probe.Settings.Translator)
	if err := b.dubReady(ctx, probe, dub.StageMix); err != nil {
		return api.BatchJSON{}, err
	}
	var out api.BatchJSON
	var paths []string
	for _, p := range req.Paths {
		real, err := api.InputFile("paths", p)
		if err != nil {
			e := api.ErrorOf(err)
			out.Errors = append(out.Errors, api.BatchErrorJSON{Path: p, Code: e.Code, Message: e.Message})
			continue
		}
		paths = append(paths, real)
	}
	if req.Folder != "" {
		entries, err := os.ReadDir(req.Folder)
		if err != nil {
			return api.BatchJSON{}, errBadRequest("folder", "Cannot read %s: %v", req.Folder, err)
		}
		for _, e := range entries {
			if p := filepath.Join(req.Folder, e.Name()); e.Type().IsRegular() && dub.IsMedia(p, dub.MediaExts) && !strings.HasPrefix(e.Name(), ".") {
				paths = append(paths, p)
			}
		}
	}
	if len(paths) == 0 && len(out.Errors) == 0 {
		return api.BatchJSON{}, errBadRequest("folder", "There is no video or audio file to dub (%s).", strings.Join(dub.MediaExts, ", "))
	}
	ids, errs := b.dubs.Enqueue(ctx, paths, bs)
	for _, p := range paths {
		if err, ok := errs[p]; ok {
			e := api.ErrorOf(err)
			out.Errors = append(out.Errors, api.BatchErrorJSON{Path: p, Code: e.Code, Message: e.Message})
		}
	}
	for _, id := range ids {
		if p, err := b.dubs.Get(ctx, id); err == nil {
			out.Added = append(out.Added, batchItem(p.ID, p.Title, p.Source.Path, p.Targets, string(p.Phase()), since(p.UpdatedAt)))
		}
	}
	return out, nil
}

func batchItem(id, title, src string, langs []string, phase, updated string) api.BatchItemJSON {
	return api.BatchItemJSON{ID: id, JobID: services.DubOwner + id, Title: title, Source: src, Languages: langs, Phase: phase, Updated: updated}
}

// Batch is the batch queue, its settings and its watch folder (read only).
func (b *apiBackend) Batch(ctx context.Context) (api.BatchJSON, error) {
	if b.dubs == nil {
		return api.BatchJSON{}, errNoModule("Dubbing")
	}
	q, err := b.dubs.Queue(ctx)
	if err != nil {
		return api.BatchJSON{}, err
	}
	out := api.BatchJSON{Queue: []api.BatchItemJSON{}}
	for _, r := range q {
		out.Queue = append(out.Queue, batchItem(r.ID, r.Title, r.Source, r.Languages, r.Phase, since(r.UpdatedAt)))
	}
	w := b.dubs.Watch()
	out.Watch = &api.WatchFolderJSON{Folder: w.Dir, Active: w.Active, Paused: w.Paused, Added: w.Added, Error: w.Err}
	bs := b.dubs.BatchSettings(ctx)
	out.Prefs = &api.BatchSettingsJSON{Targets: bs.Targets, SourceLanguage: bs.SourceLang, Voice: bs.Voice, Model: bs.Model,
		KeepBackground: bs.KeepBackground, Translator: bs.Translator}
	return out, nil
}
