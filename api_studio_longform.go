package main

// Stories and Audiobook projects for agents (thread #35): made as the
// pages make them (import, Auto-cast, Split into lines), saved in the
// project library, compiled and rendered by the longform service, and
// exported as Save does (longform_view.go).

import (
	"context"
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
	"github.com/hehehai/AudioSmith/internal/doc"
	"github.com/hehehai/AudioSmith/internal/ffmpeg"
	"github.com/hehehai/AudioSmith/internal/jobs"
	"github.com/hehehai/AudioSmith/internal/llm"
	"github.com/hehehai/AudioSmith/internal/services"
	"github.com/hehehai/AudioSmith/internal/translate"
)

// lfKind is a project kind from the API's.
func lfKind(kind string) core.ProjectKind {
	if kind == "story" {
		return core.ProjectStory
	}
	return core.ProjectAudiobook
}

// readImport opens a book or story file as the pages' import does.
func readImport(ctx context.Context, path string) (*doc.Document, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	d, err := doc.Open(ctx, path, doc.Limits{MaxFileBytes: maxBookBytes})
	switch {
	case err == nil:
		return d, nil
	case errors.Is(err, doc.ErrUnsupported):
		return nil, apiErr(http.StatusBadRequest, api.CodeUnsupportedFormat, "path", "%s: this kind of file cannot be imported (EPUB, TXT, Markdown, DOCX; stories also SRT and VTT).", filepath.Base(path))
	case errors.Is(err, doc.ErrTooLarge):
		return nil, apiErr(http.StatusBadRequest, api.CodeTooLarge, "path", "%s is too large to import (64 MB at most).", filepath.Base(path))
	case errors.Is(err, doc.ErrNoText):
		return nil, errBadRequest("path", "%s has no text to import.", filepath.Base(path))
	}
	return nil, errBadRequest("path", "Could not import %s: %v", filepath.Base(path), err)
}

// CreateLongform makes and saves a Stories or Audiobook project.
func (b *apiBackend) CreateLongform(ctx context.Context, req api.LongformCreate) (out api.LongformJSON, err error) {
	kind := lfKind(req.Kind)
	e, warn, err := ttsEntry(req.Model, b.defaultTTSRepo(), false)
	if err != nil {
		return api.LongformJSON{}, err
	}
	defer func() {
		if err == nil {
			out.Warning = warn
		}
	}()
	voices, err := b.voices(ctx)
	if err != nil {
		return api.LongformJSON{}, err
	}
	d := services.NewLongformDoc(kind, e.Repo)
	d.Language = strings.TrimSpace(req.Language)
	if req.Format == services.FormatM4B {
		if !b.CanEncode() {
			return api.LongformJSON{}, apiErr(http.StatusBadRequest, api.CodeUnsupportedFormat, "format", "M4B needs the engine's encoder, which is not available: use wav.")
		}
		d.Format = services.FormatM4B
	}
	if d.Voice, err = voiceRef(voices, e, req.Voice, "voice"); err != nil {
		return api.LongformJSON{}, err
	}
	title := strings.TrimSpace(req.Title)
	src := req.Text
	if req.Path != "" {
		bk, err := readImport(ctx, req.Path)
		if err != nil {
			return api.LongformJSON{}, err
		}
		if kind == core.ProjectAudiobook {
			src = applyBook(&d, bk)
		} else {
			src = bk.Script()
			if d.Title == "" {
				d.Title = bk.Title
			}
		}
		if d.Title == "" {
			d.Title = strings.TrimSuffix(filepath.Base(req.Path), filepath.Ext(req.Path))
		}
	}
	if title != "" {
		d.Title = title
	}
	lfv := lfVoicesOf(voices, e)
	if kind == core.ProjectAudiobook {
		d.Script = strings.TrimSpace(src) + "\n"
		if d.Voice == "" && len(lfv) > 0 {
			d.Voice = lfv[0].ref
		}
		names := services.VoiceNames(d.Script)
		for name, v := range req.Cast {
			i := slices.IndexFunc(names, func(n string) bool { return strings.EqualFold(n, name) })
			if i < 0 {
				return api.LongformJSON{}, errBadRequest("cast", "The manuscript has no [voice:%s]; its names are: %s.", name, strings.Join(names, ", "))
			}
			ref, err := voiceRef(voices, e, v, "cast")
			if err != nil {
				return api.LongformJSON{}, err
			}
			if d.VoiceCast == nil {
				d.VoiceCast = map[string]string{}
			}
			d.VoiceCast[names[i]] = ref
		}
	} else {
		if err := b.storyLines(&d, req, src, voices, e, lfv); err != nil {
			return api.LongformJSON{}, err
		}
	}
	if strings.TrimSpace(d.Title) == "" {
		d.Title = firstWords(strings.TrimLeft(services.StoryScript(d.Lines)+d.Script, "# "), 6)
	}
	if strings.TrimSpace(d.Title) == "" {
		d.Title = "Untitled"
	}
	saved, err := b.lib.svc.Library.SaveDoc(ctx, core.Project{Kind: kind, Title: d.Title}, d)
	if err != nil {
		return api.LongformJSON{}, err
	}
	return b.longformJSON(ctx, saved, d, voices)
}

// storyLines fills a story's lines and cast from lines, or from text as
// Auto-cast (default) or Split into lines does.
func (b *apiBackend) storyLines(d *services.LongformDoc, req api.LongformCreate, src string, voices []core.Voice, e catalog.Entry, lfv []lfVoice) error {
	// The cast the agent gave, first: Auto-cast keeps known names.
	cast := map[string]string{} // lower-case name → voice ref
	for name, v := range req.Cast {
		ref, err := voiceRef(voices, e, v, "cast")
		if err != nil {
			return err
		}
		cast[strings.ToLower(strings.TrimSpace(name))] = ref
		d.Cast = append(d.Cast, services.CastMember{ID: core.NewID(time.Now()), Name: strings.TrimSpace(name), Voice: ref})
	}
	member := func(name string) string {
		k := strings.ToLower(strings.TrimSpace(name))
		for _, m := range d.Cast {
			if strings.ToLower(m.Name) == k {
				return m.ID
			}
		}
		id := core.NewID(time.Now())
		d.Cast = append(d.Cast, services.CastMember{ID: id, Name: strings.TrimSpace(name)})
		return id
	}
	switch {
	case len(req.Lines) > 0:
		for i, l := range req.Lines {
			if strings.TrimSpace(l.Text) == "" {
				return errBadRequest("lines", "Line %d has no text.", i+1)
			}
			line := services.StoryLine{ID: newLineID(), Text: strings.TrimSpace(l.Text), Speed: l.Speed, PauseAfter: max(l.PauseAfter, 0), Chapter: l.Chapter}
			if l.Speed != 0 && (l.Speed < 0.5 || l.Speed > 2) {
				return errBadRequest("lines", "Line %d: speed is 0.5–2.", i+1)
			}
			if !l.Chapter {
				if s := strings.TrimSpace(l.Speaker); s != "" {
					line.Character = member(s)
				}
				if l.Voice != "" {
					ref, err := voiceRef(voices, e, l.Voice, "lines")
					if err != nil {
						return err
					}
					line.Voice = ref
				}
			}
			d.Lines = append(d.Lines, line)
		}
		// Characters without a voice get one as Auto-cast gives them.
		pick := castPicker(d, lfv)
		for i := range d.Cast {
			if d.Cast[i].Voice == "" {
				d.Cast[i].Voice = pick()
			}
		}
	case req.Split == "" || req.Split == "speakers":
		if n, _ := autoCastText(d, strings.TrimSpace(src), lfv); n == 0 {
			return errBadRequest("text", "There is nothing to say in the text.")
		}
	default:
		d.SplitBy = req.Split
		if splitStoryText(d, strings.TrimSpace(src)) == 0 {
			return errBadRequest("text", "There is nothing to say in the text.")
		}
		if d.Voice == "" && len(lfv) > 0 {
			d.Voice = lfv[0].ref
		}
	}
	return nil
}

// Longform reads a project.
func (b *apiBackend) Longform(ctx context.Context, id string) (api.LongformJSON, error) {
	p, d, err := b.loadLongform(ctx, id)
	if err != nil {
		return api.LongformJSON{}, err
	}
	voices, err := b.voices(ctx)
	if err != nil {
		return api.LongformJSON{}, err
	}
	return b.longformJSON(ctx, p, d, voices)
}

// loadLongform reads a Stories or Audiobook project.
func (b *apiBackend) loadLongform(ctx context.Context, id string) (core.Project, services.LongformDoc, error) {
	p, d, err := b.lib.svc.Library.LoadDoc(ctx, id)
	if err != nil {
		return p, d, notFound(err, "project", id)
	}
	if p.Kind != core.ProjectStory && p.Kind != core.ProjectAudiobook {
		return p, d, errBadRequest("project_id", "%s is a %s project, not a story or an audiobook.", id, p.Kind)
	}
	return p, d, nil
}

// compileFor compiles a document with the saved voices.
func (b *apiBackend) compileFor(d *services.LongformDoc, voices []core.Voice) (services.LongformSpec, []services.Problem) {
	return services.Compile(d, lfResolverOf(d, voices, b.fill, b.lib.svc.Voices.FilePath))
}

// longformJSON is a project as agents see it.
func (b *apiBackend) longformJSON(ctx context.Context, p core.Project, d services.LongformDoc, voices []core.Voice) (api.LongformJSON, error) {
	spec, probs := b.compileFor(&d, voices)
	e := entryFor(d.Model)
	out := api.LongformJSON{ID: p.ID, Kind: kindName(p.Kind), Title: firstOf(d.Title, p.Title), Model: e.ID, Voice: d.Voice,
		VoiceName: voiceLabel(voices, d.Voice), Language: d.Language, Format: firstOf(d.Format, services.FormatWAV), Speed: d.Speed, Stems: d.Stems,
		Chapters: []api.ChapterJSON{}, Updated: p.UpdatedAt,
		Book: api.BookJSON{Author: d.Book.Author, Narrator: d.Book.Narrator, Year: d.Book.Year, Genre: d.Book.Genre,
			Description: d.Book.Description, Cover: d.Book.Cover, Loudness: d.Book.Loudness}}
	for i, ch := range spec.Chapters {
		c := api.ChapterJSON{Index: i + 1, Title: ch.Title, Lines: len(ch.Lines)}
		for _, l := range ch.Lines {
			for _, sp := range l.Spans {
				c.Chars += len([]rune(sp.Text))
			}
		}
		out.Chapters = append(out.Chapters, c)
	}
	script := d.Script
	if p.Kind == core.ProjectStory {
		script = services.StoryScript(d.Lines)
		names := map[string]string{}
		count := map[string]int{}
		for _, l := range d.Lines {
			count[l.Character]++
		}
		for _, m := range d.Cast {
			names[m.ID] = m.Name
			out.Cast = append(out.Cast, api.CastJSON{Name: m.Name, Voice: m.Voice, VoiceName: voiceLabel(voices, m.Voice), Lines: count[m.ID],
				Persona: m.Persona})
		}
		for _, l := range d.Lines {
			out.Lines = append(out.Lines, api.StoryLineJSON{ID: l.ID, Text: l.Text, Speaker: names[l.Character], Voice: l.Voice,
				Speed: l.Speed, PauseAfter: l.PauseAfter, Chapter: l.Chapter, Direction: l.Direction, Intensity: l.Intensity})
		}
		out.Scene, out.Pacing, out.Continuity = d.Scene, apiPacing(d.Production.Pacing), storyContinuity(d.Model)
	} else {
		for _, n := range services.VoiceNames(d.Script) {
			ref := d.VoiceCast[n]
			out.Cast = append(out.Cast, api.CastJSON{Name: n, Voice: ref, VoiceName: voiceLabel(voices, ref)})
		}
	}
	st := services.StatsOf(script, d.Speed)
	out.Words, out.Estimate = st.Words, float64(int(st.Seconds))
	for _, pr := range probs {
		pj := api.ProblemJSON{Code: pr.Code, Message: problemMessage(pr), Blocking: pr.Blocking}
		if pr.Chapter >= 0 {
			pj.Chapter = pr.Chapter + 1
		}
		if p.Kind == core.ProjectStory && pr.Line >= 0 && pr.Line < len(d.Lines) {
			pj.Line = d.Lines[pr.Line].ID
		}
		out.Problems = append(out.Problems, pj)
	}
	if d.Render != "" {
		if r, body, err := b.lib.svc.Longform.Get(ctx, d.Render); err == nil {
			rj := renderJSON(r, &body, len(spec.Chapters))
			out.Render = &rj
		}
	}
	return out, nil
}

// kindName is the API's name of a project kind.
func kindName(k core.ProjectKind) string {
	if k == core.ProjectStory {
		return "story"
	}
	return "audiobook"
}

// problemMessage is a compiler problem in English (the API's language).
func problemMessage(p services.Problem) string {
	switch p.Code {
	case services.ProblemNoModel:
		return "Choose a model."
	case services.ProblemNothingToSay:
		return "There is no spoken line to render."
	case services.ProblemNoVoice:
		if l := p.Params["line"]; l != "" {
			return "Line " + l + " has no voice: set a narrator, or give it a voice."
		}
		return "Set a narrator (voice), or give every line a voice."
	case services.ProblemMissingVoice:
		return "The voice " + p.Params["voice"] + " is gone or does not fit this model: choose another."
	case services.ProblemUnknownVoice:
		return "Nobody is cast as " + p.Params["voice"] + ": the narrator reads it (set it in cast)."
	case services.ProblemEmptyChapter:
		return "Chapter " + p.Params["title"] + " has no spoken text."
	case services.ProblemUnknownTag:
		return "Unrecognised tag " + p.Params["tag"] + ": it will be read aloud."
	case services.ProblemDuplicateWord:
		return p.Params["word"] + " is listed twice in the pronunciations."
	}
	return p.Code
}

// renderJSON is a render as agents see it; chapters is how many the
// project has (fewer in the render: a partial one).
func renderJSON(r core.Render, body *services.RenderBody, chapters int) api.RenderJSON {
	j := api.RenderJSON{ID: r.ID, JobID: services.OwnerLongform + r.ID, State: string(r.State), Created: r.CreatedAt,
		Partial: chapters > 0 && len(body.Chapters) < chapters}
	for _, c := range body.Chapters {
		rc := api.RenderChapterOut{Title: c.Title, State: c.State, File: c.File, Seconds: round1(c.AudioSeconds)}
		if c.Error != nil {
			rc.Error = c.Error.Code + ": " + c.Error.Message
		}
		j.Chapter = append(j.Chapter, rc)
	}
	if body.Error != nil {
		j.Error = body.Error.Code + ": " + body.Error.Message
	}
	if o := body.Output; o != nil {
		j.Book, j.M4B, j.Cues, j.Seconds, j.Encode = o.Book, o.Encoded, o.Cues, round1(o.Seconds), o.EncodeError
		for i, c := range o.Chapters {
			if i < len(j.Chapter) {
				j.Chapter[i].File, j.Chapter[i].Seconds = c.File, round1(c.Seconds)
			}
		}
		for _, s := range o.Stems {
			if j.Stems == nil {
				j.Stems = map[string]string{}
			}
			j.Stems[s.Character] = s.File
		}
	}
	return j
}

func round1(v float64) float64 { return float64(int64(v*10+0.5)) / 10 }

// apiPacing is a pacing's name in the API ("fixed" for none).
func apiPacing(p string) string {
	if p == services.PacingFixed {
		return "fixed"
	}
	return p
}

// updatePerformance applies a story's scene, personas, directions, pacing
// and the AI director of an update.
func (b *apiBackend) updatePerformance(ctx context.Context, p core.Project, d *services.LongformDoc, req api.LongformUpdate, voices []core.Voice) error {
	story := p.Kind == core.ProjectStory
	if !story && (req.Scene != nil || len(req.Personas) > 0 || len(req.Directions) > 0 || len(req.Intensities) > 0 || req.Pacing != "" ||
		req.Merge != nil || req.Level != nil || req.Direct != "") {
		return errBadRequest("scene", "scene, personas, directions, pacing and direct are for stories.")
	}
	if !story {
		return nil
	}
	if req.Scene != nil {
		d.Scene = strings.TrimSpace(*req.Scene)
	}
	for name, persona := range req.Personas {
		i := slices.IndexFunc(d.Cast, func(m services.CastMember) bool { return strings.EqualFold(m.Name, strings.TrimSpace(name)) })
		if i < 0 {
			var names []string
			for _, m := range d.Cast {
				names = append(names, m.Name)
			}
			return errBadRequest("personas", "The story has no character %q; its characters are: %s.", name, strings.Join(names, ", "))
		}
		d.Cast[i].Persona = strings.TrimSpace(persona)
	}
	line := func(id, param string) (*services.StoryLine, error) {
		i := slices.IndexFunc(d.Lines, func(l services.StoryLine) bool { return l.ID == id })
		if i < 0 {
			return nil, errBadRequest(param, "The story has no line %q (get_longform lists them).", id)
		}
		return &d.Lines[i], nil
	}
	for id, dir := range req.Directions {
		l, err := line(id, "directions")
		if err != nil {
			return err
		}
		l.Direction = strings.TrimSpace(dir)
		if l.Direction == "" {
			l.Intensity = 0
		}
	}
	for id, n := range req.Intensities {
		l, err := line(id, "intensities")
		if err != nil {
			return err
		}
		if n < 0 || n > services.IntensityStrong {
			return errBadRequest("intensities", "An intensity is 1 (restrained), 2 (clear) or 3 (strong); 0 clears it.")
		}
		l.Intensity = n
	}
	switch req.Pacing {
	case "":
	case "fixed":
		d.Production.Pacing = services.PacingFixed
	case services.PacingDialogue, services.PacingReading, services.PacingRelaxed:
		d.Production.Pacing = req.Pacing
	default:
		return errBadRequest("pacing", "pacing is dialogue, reading, relaxed or fixed.")
	}
	if req.Merge != nil {
		d.Production.NoMerge = !*req.Merge
	}
	if req.Level != nil {
		d.Production.NoLevel = !*req.Level
	}
	switch req.Direct {
	case "":
		return nil
	case "fill", "all":
	default:
		return errBadRequest("direct", "direct is fill (only what is empty) or all (replace).")
	}
	if len(storyScript(d, nil).Lines) == 0 {
		return errBadRequest("direct", "The story has no lines to direct.")
	}
	dir, err := (&translate.StoryDirector{Router: b.router}).Direct(ctx, storyScript(d, voices))
	if err != nil {
		if c := translate.Code(err); c == translate.CodeNoLLM || c == llm.CodeNoRoute {
			return apiErr(http.StatusBadRequest, api.CodeLLMUnavailable, "direct",
				"The AI director needs a language model: set one up in AudioSmith's Settings → Writing help (Qwen3 4B runs on this Mac).")
		}
		return apiErr(http.StatusBadGateway, api.CodeDirectorFailed, "direct", "The AI director failed: %v", err)
	}
	applyDirection(d, dir, req.Direct == "fill")
	return nil
}

// UpdateLongform changes a project's voices and settings.
func (b *apiBackend) UpdateLongform(ctx context.Context, req api.LongformUpdate) (out api.LongformJSON, err error) {
	p, d, err := b.loadLongform(ctx, req.ID)
	if err != nil {
		return api.LongformJSON{}, err
	}
	voices, err := b.voices(ctx)
	if err != nil {
		return api.LongformJSON{}, err
	}
	if req.Model != "" {
		e, warn, err := ttsEntry(req.Model, "", false)
		if err != nil {
			return api.LongformJSON{}, err
		}
		d.Model = e.Repo
		defer func() {
			if err == nil {
				out.Warning = warn
			}
		}()
	}
	e := entryFor(d.Model)
	ref := func(v, param string) (string, error) {
		if isDefault(v) {
			return "", nil
		}
		return voiceRef(voices, e, v, param)
	}
	if req.Voice != "" {
		if d.Voice, err = ref(req.Voice, "voice"); err != nil {
			return api.LongformJSON{}, err
		}
	}
	if p.Kind == core.ProjectAudiobook {
		if len(req.Lines) > 0 {
			return api.LongformJSON{}, errBadRequest("line_voices", "line_voices are for stories; an audiobook switches voices with [voice:NAME] in its text.")
		}
		names := services.VoiceNames(d.Script)
		for name, v := range req.Cast {
			i := slices.IndexFunc(names, func(n string) bool { return strings.EqualFold(n, name) })
			if i < 0 {
				return api.LongformJSON{}, errBadRequest("cast", "The manuscript has no [voice:%s]; its names are: %s.", name, strings.Join(names, ", "))
			}
			r, err := ref(v, "cast")
			if err != nil {
				return api.LongformJSON{}, err
			}
			if d.VoiceCast == nil {
				d.VoiceCast = map[string]string{}
			}
			if r == "" {
				delete(d.VoiceCast, names[i])
			} else {
				d.VoiceCast[names[i]] = r
			}
		}
	} else {
		for name, v := range req.Cast {
			i := slices.IndexFunc(d.Cast, func(m services.CastMember) bool { return strings.EqualFold(m.Name, strings.TrimSpace(name)) })
			if i < 0 {
				var names []string
				for _, m := range d.Cast {
					names = append(names, m.Name)
				}
				return api.LongformJSON{}, errBadRequest("cast", "The story has no character %q; its characters are: %s.", name, strings.Join(names, ", "))
			}
			if d.Cast[i].Voice, err = ref(v, "cast"); err != nil {
				return api.LongformJSON{}, err
			}
		}
		for id, v := range req.Lines {
			i := slices.IndexFunc(d.Lines, func(l services.StoryLine) bool { return l.ID == id })
			if i < 0 {
				return api.LongformJSON{}, errBadRequest("line_voices", "The story has no line %q (get_longform lists them).", id)
			}
			if d.Lines[i].Voice, err = ref(v, "line_voices"); err != nil {
				return api.LongformJSON{}, err
			}
		}
	}
	if req.Language != "" {
		d.Language = req.Language
		if strings.EqualFold(req.Language, "auto") {
			d.Language = ""
		}
	}
	if req.Speed != 0 {
		d.Speed = req.Speed
	}
	if req.Stems != nil {
		d.Stems = *req.Stems
	}
	if req.Format != "" {
		if req.Format == services.FormatM4B && !b.CanEncode() {
			return api.LongformJSON{}, apiErr(http.StatusBadRequest, api.CodeUnsupportedFormat, "format", "M4B needs the engine's encoder, which is not available: use wav.")
		}
		d.Format = strings.ToLower(req.Format)
	}
	switch req.Loudness {
	case "":
	case "off":
		d.Book.Loudness = services.LoudnessOff
	default:
		d.Book.Loudness = req.Loudness
	}
	for _, f := range []struct {
		v   string
		dst *string
	}{{req.Author, &d.Book.Author}, {req.Narrator, &d.Book.Narrator}, {req.Year, &d.Book.Year}, {req.Genre, &d.Book.Genre},
		{req.About, &d.Book.Description}, {req.Cover, &d.Book.Cover}} {
		if f.v != "" {
			*f.dst = strings.TrimSpace(f.v)
		}
	}
	if t := strings.TrimSpace(req.Title); t != "" {
		d.Title, p.Title = t, t
	}
	if err := b.updatePerformance(ctx, p, &d, req, voices); err != nil {
		return api.LongformJSON{}, err
	}
	saved, err := b.lib.svc.Library.SaveDoc(ctx, core.Project{ID: p.ID, Kind: p.Kind, Title: p.Title}, d)
	if err != nil {
		return api.LongformJSON{}, err
	}
	return b.longformJSON(ctx, saved, d, voices)
}

// RenderLongform compiles a project and starts its render (or of some
// chapters: a book of those only).
func (b *apiBackend) RenderLongform(ctx context.Context, req api.LongformRender) (api.JobJSON, error) {
	p, d, err := b.loadLongform(ctx, req.ID)
	if err != nil {
		return api.JobJSON{}, err
	}
	voices, err := b.voices(ctx)
	if err != nil {
		return api.JobJSON{}, err
	}
	spec, probs := b.compileFor(&d, voices)
	var blocking []api.ProblemJSON
	for _, pr := range probs {
		if pr.Blocking {
			blocking = append(blocking, api.ProblemJSON{Code: pr.Code, Message: problemMessage(pr), Blocking: true})
		}
	}
	if len(blocking) > 0 {
		return api.JobJSON{}, &api.Error{Status: http.StatusBadRequest, Code: "not_renderable", Param: "project_id",
			Message: "The project cannot render yet: " + blocking[0].Message, Data: map[string]any{"problems": blocking}}
	}
	if err := b.modelReady(ctx, entryFor(d.Model)); err != nil {
		return api.JobJSON{}, err
	}
	if len(req.Chapters) > 0 {
		var chs []services.ChapterSpec
		for _, n := range req.Chapters {
			if n < 1 || n > len(spec.Chapters) {
				return api.JobJSON{}, errBadRequest("chapters", "Chapter %d does not exist: the project has %d.", n, len(spec.Chapters))
			}
			chs = append(chs, spec.Chapters[n-1])
		}
		spec.Chapters = chs
	}
	spec.Project = p.ID
	if spec.Title == "" {
		spec.Title = p.Title
	}
	r, err := b.lib.svc.Longform.Start(ctx, spec)
	if err != nil {
		if errors.Is(err, core.ErrBusy) {
			return api.JobJSON{}, errBusy("Another book or story is rendering: wait for it (list it with get_job) or cancel it first.")
		}
		return api.JobJSON{}, err
	}
	// The project remembers its last render, as the page does.
	d.Render = r.ID
	if _, err := b.lib.svc.Library.SaveDoc(ctx, core.Project{ID: p.ID, Kind: p.Kind, Title: p.Title}, d); err != nil {
		return api.JobJSON{}, err
	}
	return b.renderTask(ctx, r.ID)
}

// renderTask is a render's job: "longform:<render id>".
func (b *apiBackend) renderTask(ctx context.Context, id string) (api.JobJSON, error) {
	r, body, err := b.lib.svc.Longform.Get(ctx, id)
	if err != nil {
		return api.JobJSON{}, notFound(err, "render", id)
	}
	state := jobs.StateRunning
	var jerr *jobs.Error
	switch r.State {
	case core.RenderDone:
		state = jobs.StateSucceeded
	case core.RenderFailed:
		state, jerr = jobs.StateFailed, body.Error
		if jerr == nil {
			for i, c := range body.Chapters {
				if c.Error != nil {
					e := *c.Error
					e.Message = services.Untitled(i+1) + ": " + e.Message
					jerr = &e
					break
				}
			}
		}
		if jerr == nil {
			jerr = &jobs.Error{Code: services.CodeRenderFailed, Message: "Some chapters did not render."}
		}
	case core.RenderStopped:
		state, jerr = jobs.StateCancelled, &jobs.Error{Code: jobs.CodeCancelled, Message: "The render was stopped; the chapters made are kept."}
	}
	n := len(body.Chapters)
	done, frac, stage := 0.0, 0.0, ""
	for i, c := range body.Chapters {
		switch c.State {
		case services.ChapterDone, services.ChapterCached:
			done++
		case services.ChapterRendering:
			frac += max(c.Fraction, 0)
			stage = "chapter " + itoa(i+1) + "/" + itoa(n)
			if c.Title != "" {
				stage += ": " + c.Title
			}
		}
	}
	progress := -1.0
	if n > 0 {
		progress = (done + frac) / float64(n) * 0.95
	}
	if body.Assembling {
		stage, progress = "assembling the book", 0.95+0.05*max(body.Assembled, 0)
	}
	if state == jobs.StateSucceeded {
		progress, stage = 1, ""
	}
	msg := ""
	if eta, ok := services.ETA(&body, time.Now()); ok && state == jobs.StateRunning {
		msg = "about " + eta.Round(time.Second).String() + " left"
	}
	var parts []string
	for _, c := range body.Chapters {
		if c.Job != "" {
			parts = append(parts, c.Job)
		}
	}
	if body.AssembleJob != "" {
		parts = append(parts, body.AssembleJob)
	}
	rj := renderJSON(r, &body, 0)
	var files []string
	if rj.M4B != "" {
		files = append(files, rj.M4B)
	}
	if rj.Book != "" {
		files = append(files, rj.Book)
	}
	for _, c := range rj.Chapter {
		if c.File != "" && state == jobs.StateSucceeded {
			files = append(files, c.File)
		}
	}
	res := map[string]any{"render": rj, "project_id": r.ProjectID}
	return api.GroupJob(services.OwnerLongform+id, "longform.render", r.Title, state, stage, progress, msg, jerr, files, res, parts), nil
}

// ExportLongform saves a finished render's files (Save in the page).
func (b *apiBackend) ExportLongform(ctx context.Context, req api.LongformExport) (api.ExportJSON, error) {
	rid := strings.TrimPrefix(strings.TrimSpace(req.Render), services.OwnerLongform)
	if rid == "" {
		_, d, err := b.loadLongform(ctx, req.ID)
		if err != nil {
			return api.ExportJSON{}, err
		}
		if d.Render == "" {
			return api.ExportJSON{}, errNotReady("The project has not been rendered: render_longform first.")
		}
		rid = d.Render
	}
	rec, body, err := b.lib.svc.Longform.Get(ctx, rid)
	if err != nil {
		return api.ExportJSON{}, notFound(err, "render", rid)
	}
	if req.ID != "" && rec.ProjectID != req.ID {
		return api.ExportJSON{}, errBadRequest("render_id", "The render %s is not of the project %s.", rid, req.ID)
	}
	out := body.Output
	if rec.State != core.RenderDone || out == nil {
		return api.ExportJSON{}, errNotReady("The render is %s: wait for its job (get_job longform:%s) to succeed, then export.", rec.State, rid)
	}
	lib := b.lib.svc.Library
	title := services.SafeFileName(rec.Title, kindName(rec.Kind))
	format := firstOf(strings.ToLower(req.Format), services.FormatWAV)
	res := api.ExportJSON{Format: format}
	file := func(ext string) string { return filepath.Join(req.Dir, api.SafeName(req.Filename, title, ext)) }
	folder := func(suffix string) (string, error) {
		dir := filepath.Join(req.Dir, api.SafeName(req.Filename, title+suffix, ""))
		return dir, os.MkdirAll(dir, 0o755)
	}
	switch format {
	case services.FormatM4B:
		if out.Encoded == "" {
			why := ""
			if out.EncodeError != "" {
				why = " (" + out.EncodeError + ")"
			}
			return api.ExportJSON{}, apiErr(http.StatusBadRequest, api.CodeUnsupportedFormat, "format",
				"This render has no M4B%s: set format m4b with update_longform and render again, or export wav.", why)
		}
		dst := file("m4b")
		if err := writeRenderFile(ctx, lib, rec, "book", out.Encoded, dst, "m4b", 0); err != nil {
			return api.ExportJSON{}, err
		}
		res.Files = []string{dst}
	case services.FormatWAV:
		dst := file("wav")
		if err := writeRenderFile(ctx, lib, rec, "book", out.Book, dst, "wav", req.Bits); err != nil {
			return api.ExportJSON{}, err
		}
		res.Files = []string{dst}
	case ffmpeg.MP3, ffmpeg.Opus:
		tool := b.tool()
		if !tool.CanEncode(format) {
			return api.ExportJSON{}, apiErr(http.StatusBadRequest, api.CodeUnsupportedFormat, "format",
				"%s needs ffmpeg: the user can set it up in AudioSmith → Settings → Audio tools.", strings.ToUpper(format))
		}
		o := bookEncodeOptions(rec, &body.Spec, out, format)
		o.Output = file(formatExt(format))
		if err := tool.EncodeAudio(ctx, o, nil); err != nil {
			return api.ExportJSON{}, err
		}
		lib.RecordExport(ctx, core.Export{Kind: "book", SourceID: rec.ID, ProjectID: rec.ProjectID, Title: rec.Title, Path: o.Output, Format: formatExt(format)})
		res.Files = []string{o.Output}
	case "chapters":
		dir, err := folder(" chapters")
		if err != nil {
			return api.ExportJSON{}, err
		}
		if res.Files, err = writeRenderChapters(ctx, lib, rec, slices.Clone(out.Chapters), dir); err != nil {
			return api.ExportJSON{}, err
		}
	case "cues":
		dst := file("txt")
		if err := writeRenderFile(ctx, lib, rec, "cues", out.Cues, dst, "txt", 0); err != nil {
			return api.ExportJSON{}, err
		}
		res.Files = []string{dst}
	case "stems":
		if len(out.Stems) == 0 {
			return api.ExportJSON{}, errNotReady("This render has no stems: set stems: true with update_longform and render again.")
		}
		dir, err := folder(" stems")
		if err != nil {
			return api.ExportJSON{}, err
		}
		for _, s := range out.Stems {
			name := firstOf(s.Character, "Narrator")
			dst := filepath.Join(dir, api.SafeName(name, "stem", "wav"))
			if err := writeRenderFile(ctx, lib, rec, "stem", s.File, dst, "wav", req.Bits); err != nil {
				return api.ExportJSON{}, err
			}
			res.Files = append(res.Files, dst)
		}
	}
	for _, f := range res.Files {
		if fi, err := os.Stat(f); err == nil {
			res.Bytes += fi.Size()
		}
	}
	res.Seconds = round1(out.Seconds)
	return res, nil
}
