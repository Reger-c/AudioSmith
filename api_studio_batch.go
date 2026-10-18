package main

// The batch queue for agents (thread #35): files added with the Batch
// page's settings, and the queue with its settings and watch folder.

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/hehehai/AudioSmith/internal/api"
	"github.com/hehehai/AudioSmith/internal/dub"
	"github.com/hehehai/AudioSmith/internal/services"
)

// BatchDub adds files to the batch queue with the Batch page's settings
// (the request's override them for these files only; nothing is saved).
func (b *apiBackend) BatchDub(ctx context.Context, req api.BatchRequest) (res api.BatchJSON, err error) {
	if b.dubs == nil {
		return api.BatchJSON{}, errNoModule("Dubbing")
	}
	bs := b.dubs.BatchSettings(ctx)
	if len(req.Targets) > 0 {
		if err := checkLangs("target_languages", req.Targets); err != nil {
			return api.BatchJSON{}, err
		}
		bs.Targets, bs.TargetsAuto = slices.Clone(req.Targets), false
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
		e, warn, err := ttsEntry(req.Model, "", true)
		if err != nil {
			return api.BatchJSON{}, err
		}
		bs.Model = e.Repo
		defer func() {
			if err == nil {
				res.Warning = warn
			}
		}()
	}
	if bs.Model == "" {
		bs.Model = dubDefaultModelBy(b.onDisk)
	}
	if req.KeepBackground != nil {
		bs.KeepBackground = *req.KeepBackground
	}
	switch m, err := dubDialogueMode(req.Dialogue); {
	case err != nil:
		return api.BatchJSON{}, err
	case m == dub.DialogueOff:
		bs.Dialogue = dub.DialogueOff
	case m == dub.DialogueAuto:
		bs.Dialogue = ""
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
		KeepBackground: bs.KeepBackground, Translator: bs.Translator, Dialogue: firstOf(bs.Dialogue, dub.DialogueAuto)}
	return out, nil
}
