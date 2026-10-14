package api

// AudioSmith's own API (/api): voices, generation, takes, transcripts,
// models, languages and tools, plus /health and the capabilities
// document.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/hehehai/AudioSmith/internal/catalog"
	"github.com/hehehai/AudioSmith/internal/core"
	"github.com/hehehai/AudioSmith/internal/engine/proto"
	"github.com/hehehai/AudioSmith/internal/jobs"
	"github.com/hehehai/AudioSmith/internal/lang"
	"github.com/hehehai/AudioSmith/internal/services"
	"github.com/hehehai/AudioSmith/internal/translate"
	vd "github.com/hehehai/AudioSmith/internal/voicedesign"
)

// --- health ----------------------------------------------------------------

// Health is GET /health's answer.
type Health struct {
	Status  string `json:"status"` // "ok", "starting", "unavailable"
	Version string `json:"version"`
	Engine  Status `json:"engine"`
}

// Health reports whether requests can run now.
func (s *Server) Health() Health {
	st := s.b.Status()
	h := Health{Status: "ok", Version: s.o.Version, Engine: st}
	switch st.State {
	case StateStarting, StateRestarting:
		h.Status = "starting"
	case StateFailed, StateStopped:
		h.Status = "unavailable"
	}
	return h
}

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	h := s.Health()
	status := http.StatusOK
	if h.Status != "ok" {
		status = http.StatusServiceUnavailable
		retry := h.Engine.RetryAfter
		if retry <= 0 {
			retry = 5
		}
		w.Header().Set("Retry-After", strconv.Itoa(retry))
	}
	writeJSON(w, status, h)
}

// capabilities is GET /.well-known/audiosmith-speech: what this server
// offers, for agents and scripts that discover it.
func (s *Server) capabilities(w http.ResponseWriter, r *http.Request) {
	st := s.b.Settings()
	speech := []string{FormatWAV, FormatPCM}
	if s.b.CanEncode() {
		speech = append(speech, FormatAAC)
	}
	for _, f := range []string{FormatMP3, FormatOpus, FormatFLAC} { // M7a: ffmpeg
		if s.encodesFormat(f) {
			speech = append(speech, f)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"schema":      "audiosmith.speech-capabilities",
		"version":     1,
		"app_version": s.o.Version,
		"endpoints": map[string]string{
			"openai_base_url": "/v1", "speech": "/v1/audio/speech", "transcriptions": "/v1/audio/transcriptions",
			"translations": "/v1/audio/translations", "models": "/v1/models", "voices": "/v1/audio/voices",
			"generate": "/api/generate", "tts_stream": "/ws/tts", "job_events": "/api/jobs/{id}/events",
			"mcp": "/mcp", "mcp_stdio": "AudioSmith.app/Contents/MacOS/AudioSmith --mcp-stdio",
			"openapi": "/openapi.json", "docs": "/docs", "health": "/health",
		},
		"formats": map[string]any{
			"speech":         speech,
			"speech_stream":  []string{"audio", "sse"},
			"pcm":            "audio/pcm;rate=24000;encoding=s16le;channels=1",
			"transcriptions": []string{"json", "text", "verbose_json", "srt", "vtt"},
		},
		"features": map[string]bool{
			"streaming_tts": true, "word_timestamps": true, "voice_cloning": true, "voice_design": true,
			"translations_via_llm": true, "mcp": s.mcp.Load() != nil,
		},
		"limits": map[string]int{"openai_input_chars": MaxOpenAIInput, "input_chars": MaxInput, "upload_bytes": MaxUpload},
		"auth": map[string]any{
			"loopback_requires_key": st.API.RequireKey, "scheme": "bearer",
			"csrf_header": HeaderCSRF, "pin_header": HeaderPIN, "client_header": HeaderClient,
		},
		"engine": s.b.Status(),
	})
}

// --- voices ----------------------------------------------------------------

func (s *Server) listVoices(w http.ResponseWriter, r *http.Request) {
	q := core.VoiceQuery{Search: r.URL.Query().Get("search"), Kind: core.VoiceKind(r.URL.Query().Get("kind"))}
	vs, err := s.b.Voices(r.Context(), q)
	if err != nil {
		writeError(w, err)
		return
	}
	out := make([]VoiceJSON, len(vs))
	for i, v := range vs {
		out[i] = voiceJSON(v)
	}
	writeJSON(w, http.StatusOK, map[string]any{"object": "list", "data": out})
}

func (s *Server) getVoice(w http.ResponseWriter, r *http.Request) {
	v, err := s.b.Voice(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, voiceJSON(v))
}

func (s *Server) voiceAudio(w http.ResponseWriter, r *http.Request) {
	v, err := s.b.Voice(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, err)
		return
	}
	name := v.Sample
	if name == "" {
		name = v.RefAudio
	}
	if name == "" {
		writeError(w, errNotFound(CodeNotFound, "The voice %s has no audio.", v.Name))
		return
	}
	serveFile(w, r, s.b.VoiceFile(v, name))
}

// CloneParams makes a cloned voice.
type CloneParams struct {
	Name     string
	File     string // the reference (any audio AVFoundation reads)
	RefText  string // what it says; "" lets the engine listen
	Language string
	Style    string
}

// CloneVoice saves a voice cloned from a file (copied into the library).
func (s *Server) CloneVoice(ctx context.Context, p CloneParams) (core.Voice, error) {
	name := strings.TrimSpace(p.Name)
	if name == "" {
		return core.Voice{}, errInvalid("name", "The voice needs a name.")
	}
	if p.File == "" {
		return core.Voice{}, errInvalid("file", "The voice needs a reference recording.")
	}
	if _, err := os.Stat(p.File); err != nil {
		return core.Voice{}, errInvalid("file", "The reference cannot be read: %v", err)
	}
	v := core.Voice{Name: name, Kind: core.VoiceCloned, RefText: strings.TrimSpace(p.RefText),
		Language: strings.TrimSpace(p.Language), Style: strings.TrimSpace(p.Style), Origin: "api"}
	return s.b.SaveVoice(ctx, v, core.VoiceFiles{Reference: p.File})
}

func (s *Server) createVoice(w http.ResponseWriter, r *http.Request) {
	path, _, err := s.formFile(r, "file")
	if err != nil {
		writeError(w, err)
		return
	}
	defer os.Remove(path)
	v, err := s.CloneVoice(r.Context(), CloneParams{Name: r.FormValue("name"), File: path, RefText: r.FormValue("ref_text"),
		Language: r.FormValue("language"), Style: r.FormValue("style")})
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, voiceJSON(v))
}

// DesignParams makes a designed voice.
type DesignParams struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Language    string `json:"language,omitempty"`
	Seed        int64  `json:"seed,omitempty"`  // 0 = a new one
	Model       string `json:"model,omitempty"` // a design model; "" = the first
}

// Designed is a designed voice and how its description was read.
type Designed struct {
	Voice     core.Voice
	Unmatched []string // words the parser could not map
	Model     catalog.Entry
}

// DesignVoice saves a voice made from a description. The voice renders
// with design models as described; others clone its sample once the app
// renders one.
func (s *Server) DesignVoice(ctx context.Context, p DesignParams) (Designed, error) {
	desc := strings.TrimSpace(p.Description)
	if desc == "" {
		return Designed{}, errInvalid("description", "Describe the voice.")
	}
	var e catalog.Entry
	if p.Model != "" {
		var ok bool
		if e, ok = findEntry(p.Model, catalog.TaskTTS); !ok || !e.Has(catalog.CapDesign) {
			return Designed{}, &Error{Status: http.StatusBadRequest, Code: CodeUnsupportedModel, Param: "model",
				Message: fmt.Sprintf("%q is not a model that designs voices.", p.Model)}
		}
	} else {
		for _, c := range catalog.Builtin().ForTask(catalog.TaskTTS) {
			if c.Has(catalog.CapDesign) {
				e = c
				break
			}
		}
	}
	res := vd.Parse(desc)
	seed := p.Seed
	if seed == 0 {
		seed = vd.NewSeed()
	}
	r := vd.Recipe{Description: desc, Attrs: res.Attrs.Canonical(), Seed: vd.FoldSeed(seed)}.Normalize()
	attrs, _ := json.Marshal(r.Attrs)
	name := strings.TrimSpace(p.Name)
	if name == "" {
		name = desc
		if rs := []rune(name); len(rs) > 40 {
			name = string(rs[:40]) + "…"
		}
	}
	language := strings.TrimSpace(p.Language)
	if language == "" {
		language = r.Attrs.Language()
	}
	v := core.Voice{Name: name, Kind: core.VoiceDesigned, Design: r.Description, DesignAttrs: attrs, Seed: r.Seed,
		Language: language, CreatedWith: e.Repo, Origin: "api"}
	saved, err := s.b.SaveVoice(ctx, v, core.VoiceFiles{})
	if err != nil {
		return Designed{}, err
	}
	var un []string
	for _, t := range res.Unmatched {
		un = append(un, t.Text)
	}
	return Designed{Voice: saved, Unmatched: un, Model: e}, nil
}

func (s *Server) designVoice(w http.ResponseWriter, r *http.Request) {
	var p DesignParams
	if err := readJSON(w, r, 64<<10, &p); err != nil {
		writeError(w, err)
		return
	}
	d, err := s.DesignVoice(r.Context(), p)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"voice": voiceJSON(d.Voice), "unmatched": d.Unmatched, "model": d.Model.ID})
}

func (s *Server) deleteVoice(w http.ResponseWriter, r *http.Request) {
	if err := s.b.DeleteVoice(r.Context(), r.PathValue("id")); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": r.PathValue("id"), "deleted": true})
}

// --- generation and takes -------------------------------------------------

// generateBody is POST /api/generate: a take's whole recipe.
type generateBody struct {
	Text         string   `json:"text"`
	Model        string   `json:"model"`
	Voice        voiceRef `json:"voice"`
	Instructions string   `json:"instructions"`
	Language     string   `json:"language"`
	Speed        float64  `json:"speed"`
	Seed         int64    `json:"seed"`
	Duration     float64  `json:"duration"`
	Steps        int      `json:"steps"`
	Temperature  float64  `json:"temperature"`
	TopP         float64  `json:"top_p"`
	Guidance     float64  `json:"guidance"`
	Exaggeration float64  `json:"exaggeration"`
	// Normalize ("Even out volume") is on unless false, as in the Studio.
	Normalize *bool `json:"normalize"`
	Bits      int   `json:"bits"`
	// Wait for the take (default); false answers 202 with the job at once.
	Wait       *bool `json:"wait"`
	Background bool  `json:"background"`
}

func (s *Server) generate(w http.ResponseWriter, r *http.Request) {
	var b generateBody
	if err := readJSON(w, r, 2<<20, &b); err != nil {
		writeError(w, err)
		return
	}
	if b.Bits != 0 && b.Bits != 16 && b.Bits != 24 && b.Bits != 32 {
		writeError(w, errInvalid("bits", "bits is 16, 24 or 32."))
		return
	}
	p := SpeechParams{Text: b.Text, Model: b.Model, Voice: string(b.Voice), Instructions: b.Instructions, Language: b.Language,
		Speed: b.Speed, Seed: b.Seed, Duration: b.Duration, Steps: b.Steps, Temperature: b.Temperature, TopP: b.TopP,
		Guidance: b.Guidance, Exaggeration: b.Exaggeration, Normalize: b.Normalize == nil || *b.Normalize, Bits: b.Bits,
		Client: r.Header.Get(HeaderClient), Background: b.Background}
	sp, err := s.StartSpeech(r.Context(), p)
	if err != nil {
		writeError(w, err)
		return
	}
	if b.Wait != nil && !*b.Wait {
		sp.Detach()
		writeJSON(w, http.StatusAccepted, map[string]any{"take_id": sp.Recipe.ID, "job_id": sp.JobID, "model": sp.Model.ID,
			"ignored": sp.Ignored, "job_url": "/api/jobs/" + sp.JobID, "events_url": "/api/jobs/" + sp.JobID + "/events",
			"audio_url": "/api/takes/" + sp.Recipe.ID + "/audio"})
		return
	}
	res, err := sp.Wait(r.Context(), nil)
	if err != nil {
		writeError(w, err)
		return
	}
	speechHeaders(w.Header(), sp)
	writeJSON(w, http.StatusOK, map[string]any{"take": takeJSON(res.Take), "job_id": sp.JobID, "ignored": sp.Ignored})
}

// Detach stops following a speech request without cancelling it.
func (sp *Speech) Detach() { sp.x.stop() }

// queryInt reads an integer query parameter within [lo, hi].
func queryInt(r *http.Request, name string, def, lo, hi int) int {
	v := r.URL.Query().Get(name)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return min(max(n, lo), hi)
}

func (s *Server) listTakes(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	tq := core.TakeQuery{Search: q.Get("search"), VoiceID: q.Get("voice_id"), Starred: q.Get("starred") == "true",
		Limit: queryInt(r, "limit", 50, 1, 500), Offset: queryInt(r, "offset", 0, 0, 1<<30)}
	if m := q.Get("model"); m != "" {
		if e, ok := findEntry(m, catalog.TaskTTS); ok {
			tq.Model = e.Repo
		} else {
			tq.Model = m
		}
	}
	ts, err := s.b.Takes(r.Context(), tq)
	if err != nil {
		writeError(w, err)
		return
	}
	out := make([]TakeJSON, len(ts))
	for i, t := range ts {
		out[i] = takeJSON(t)
	}
	writeJSON(w, http.StatusOK, map[string]any{"object": "list", "data": out})
}

func (s *Server) getTake(w http.ResponseWriter, r *http.Request) {
	t, err := s.b.Take(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, takeJSON(t))
}

func (s *Server) takeAudio(w http.ResponseWriter, r *http.Request) {
	t, err := s.b.Take(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, err)
		return
	}
	serveFile(w, r, s.b.TakeAudio(t))
}

func (s *Server) deleteTake(w http.ResponseWriter, r *http.Request) {
	if err := s.b.DeleteTake(r.Context(), r.PathValue("id")); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": r.PathValue("id"), "deleted": true})
}

// serveFile serves an audio file (ranges, If-Modified-Since).
func serveFile(w http.ResponseWriter, r *http.Request, path string) {
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			writeError(w, errNotFound(CodeNotFound, "The audio file is missing."))
			return
		}
		writeError(w, err)
		return
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		writeError(w, err)
		return
	}
	if strings.HasSuffix(strings.ToLower(path), ".wav") {
		w.Header().Set("Content-Type", "audio/wav")
	}
	http.ServeContent(w, r, fi.Name(), fi.ModTime(), f)
}

// --- transcripts -----------------------------------------------------------

func (s *Server) listTranscripts(w http.ResponseWriter, r *http.Request) {
	q := core.TranscriptQuery{Search: r.URL.Query().Get("search"), Limit: queryInt(r, "limit", 50, 1, 500), Offset: queryInt(r, "offset", 0, 0, 1<<30)}
	ts, err := s.b.Transcripts(r.Context(), q)
	if err != nil {
		writeError(w, err)
		return
	}
	out := make([]TranscriptJSON, len(ts))
	for i, t := range ts {
		out[i] = transcriptJSON(t, false)
	}
	writeJSON(w, http.StatusOK, map[string]any{"object": "list", "data": out})
}

func (s *Server) getTranscript(w http.ResponseWriter, r *http.Request) {
	t, err := s.b.Transcript(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, err)
		return
	}
	switch f := r.URL.Query().Get("format"); f {
	case "", "json":
		writeJSON(w, http.StatusOK, transcriptJSON(t, true))
	case "txt", "text":
		writeTranscript(w, t, "text", "transcribe", false, false)
	case "srt", "vtt":
		writeTranscript(w, t, f, "transcribe", false, false)
	default:
		writeError(w, &Error{Status: http.StatusBadRequest, Code: CodeUnsupportedFormat, Param: "format", Message: "format is json, txt, srt or vtt."})
	}
}

func (s *Server) createTranscript(w http.ResponseWriter, r *http.Request) {
	path, name, err := s.formFile(r, "file")
	if err != nil {
		writeError(w, err)
		return
	}
	defer os.Remove(path)
	words := r.FormValue("words") != "false" // word timings unless asked not to
	tr, err := s.Transcribe(r.Context(), TranscribeParams{File: path, Title: firstNonEmpty(r.FormValue("title"), name), Model: r.FormValue("model"),
		Language: r.FormValue("language"), Prompt: r.FormValue("prompt"), Words: words})
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"transcript": transcriptJSON(tr.Transcript, true), "warnings": tr.Warnings})
}

func (s *Server) deleteTranscript(w http.ResponseWriter, r *http.Request) {
	if err := s.b.DeleteTranscript(r.Context(), r.PathValue("id")); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": r.PathValue("id"), "deleted": true})
}

// --- jobs -------------------------------------------------------------------

func (s *Server) listJobs(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := jobs.Filter{Owner: q.Get("owner"), Kind: q.Get("kind"), Lane: jobs.Lane(q.Get("lane")), Limit: queryInt(r, "limit", 100, 1, 1000)}
	if st := q.Get("state"); st != "" {
		if st == "all" {
			f.States = []jobs.State{jobs.StateWaiting, jobs.StateQueued, jobs.StateRunning, jobs.StateSucceeded,
				jobs.StateFailed, jobs.StateCancelled, jobs.StateInterrupted}
		} else {
			for _, x := range strings.Split(st, ",") {
				f.States = append(f.States, jobs.State(strings.TrimSpace(x)))
			}
		}
	}
	js, err := s.sched.List(r.Context(), f)
	if err != nil {
		writeError(w, err)
		return
	}
	out := make([]JobJSON, len(js))
	for i, j := range js {
		out[i] = jobJSON(j, false)
	}
	writeJSON(w, http.StatusOK, map[string]any{"object": "list", "data": out})
}

func (s *Server) job(ctx context.Context, id string) (jobs.Job, error) {
	j, err := s.sched.Get(ctx, id)
	if errors.Is(err, jobs.ErrNotFound) {
		return j, errNotFound(CodeNotFound, "There is no job %s.", id)
	}
	return j, err
}

// getJob is a queue job or a group job ("longform:<id>"…, thread #35);
// ?wait=N waits up to N seconds (≤ 60) for it to end.
func (s *Server) getJob(w http.ResponseWriter, r *http.Request) {
	j, err := s.Job(r.Context(), r.PathValue("id"), waitParam(r))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, j)
}

func (s *Server) cancelJob(w http.ResponseWriter, r *http.Request) {
	j, err := s.CancelJob(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, err)
		return
	}
	j.Result = nil
	writeJSON(w, http.StatusOK, j)
}

// runEngineJob runs an engine method as a job of the queue (interactive)
// and waits for its result.
func (s *Server) runEngineJob(ctx context.Context, kind string, params any, title string, out any) error {
	data, err := json.Marshal(params)
	if err != nil {
		return err
	}
	owner := "api:" + core.NewID(time.Now())
	x := s.w.owner(owner)
	defer x.stop()
	id, err := s.sched.Submit(ctx, jobs.Spec{Kind: kind, Lane: jobs.Lane(proto.RoleOf(kind)), Priority: jobs.PriorityInteractive,
		Params: data, Title: title, Owner: owner})
	if err != nil {
		return err
	}
	j, err := waitJob(ctx, s.sched, x, id, s.queueTimeout(), nil)
	if err != nil {
		return err
	}
	return jobOutcome(j, out)
}

// --- models and languages --------------------------------------------------

func (s *Server) modelList(ctx context.Context) ([]ModelJSON, error) {
	models, err := s.b.Models(ctx)
	if err != nil {
		return nil, err
	}
	st := s.b.Settings()
	defaults := map[string]bool{defaultTTS(st).ID: true, defaultSTT(st, false).ID: true}
	out := make([]ModelJSON, len(models))
	for i, m := range models {
		out[i] = modelJSON(m, defaults[m.Entry.ID])
	}
	return out, nil
}

func (s *Server) listModels(w http.ResponseWriter, r *http.Request) {
	list, err := s.modelList(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	if task := r.URL.Query().Get("task"); task != "" {
		list = slices.DeleteFunc(list, func(m ModelJSON) bool { return !slices.Contains(m.Tasks, task) })
	}
	writeJSON(w, http.StatusOK, map[string]any{"object": "list", "data": list})
}

func (s *Server) getModel(w http.ResponseWriter, r *http.Request) {
	list, err := s.modelList(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	id := r.PathValue("id")
	for _, m := range list {
		if strings.EqualFold(m.ID, id) || strings.EqualFold(m.Repo, id) {
			writeJSON(w, http.StatusOK, m)
			return
		}
	}
	writeError(w, errNotFound(CodeModelNotFound, "There is no model %q.", id))
}

func (s *Server) downloadModel(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, ok := catalog.Builtin().ByID(id); !ok {
		writeError(w, errNotFound(CodeModelNotFound, "There is no model %q.", id))
		return
	}
	jobID, err := s.b.DownloadModel(r.Context(), id)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"model": id, "job_id": jobID, "events_url": "/api/jobs/" + jobID + "/events"})
}

// entryLanguages are the BCP-47 codes a model speaks or understands
// (OmniVoice's 646 and Whisper's 100 come from the language table).
func entryLanguages(e catalog.Entry) []string {
	switch {
	case len(e.Languages) == 0 && e.Family == "omnivoice":
		return lang.OmniVoiceCodes()
	case e.Family == "whisper":
		return lang.WhisperCodes()
	}
	out := make([]string, 0, len(e.Languages))
	for _, l := range e.Languages {
		out = append(out, l.Code)
	}
	return out
}

// LanguageJSON is a language a model speaks.
type LanguageJSON struct {
	Code   string   `json:"code"`
	Name   string   `json:"name"`
	Native string   `json:"native,omitempty"`
	Models []string `json:"models,omitempty"`
}

// Languages lists the languages of a model (catalogue id, repo or
// label), or of every speech model ("").
func (s *Server) Languages(model string) ([]LanguageJSON, error) {
	var entries []catalog.Entry
	if model != "" {
		e, ok := findEntry(model, catalog.TaskTTS)
		if !ok {
			if e, ok = findEntry(model, catalog.TaskSTT); !ok {
				return nil, errNotFound(CodeModelNotFound, "There is no model %q.", model)
			}
		}
		entries = []catalog.Entry{e}
	} else {
		entries = catalog.Builtin().ForTask(catalog.TaskTTS)
	}
	by := map[string]*LanguageJSON{}
	var order []string
	for _, e := range entries {
		for _, code := range entryLanguages(e) {
			j := by[code]
			if j == nil {
				j = &LanguageJSON{Code: code, Name: code}
				if info, ok := lang.Lookup(code); ok {
					j.Name, j.Native = info.Name, info.Native
				}
				by[code] = j
				order = append(order, code)
			}
			if !slices.Contains(j.Models, e.ID) {
				j.Models = append(j.Models, e.ID)
			}
		}
	}
	out := make([]LanguageJSON, 0, len(order))
	for _, c := range order {
		out = append(out, *by[c])
	}
	slices.SortFunc(out, func(a, b LanguageJSON) int { return strings.Compare(a.Name, b.Name) })
	return out, nil
}

func (s *Server) languages(w http.ResponseWriter, r *http.Request) {
	list, err := s.Languages(r.URL.Query().Get("model"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"object": "list", "data": list})
}

// --- tools -------------------------------------------------------------------

func (s *Server) toolProbe(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Path string `json:"path"`
	}
	if err := readJSON(w, r, 64<<10, &b); err != nil {
		writeError(w, err)
		return
	}
	if !strings.HasPrefix(b.Path, "/") {
		writeError(w, errInvalid("path", "path must be absolute."))
		return
	}
	info, err := s.b.Probe(r.Context(), b.Path)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, info)
}

func (s *Server) toolDirection(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Note string `json:"note"`
	}
	if err := readJSON(w, r, 64<<10, &b); err != nil {
		writeError(w, err)
		return
	}
	if strings.TrimSpace(b.Note) == "" {
		writeError(w, errInvalid("note", "note is empty."))
		return
	}
	writeJSON(w, http.StatusOK, s.b.Direct(r.Context(), b.Note))
}

func (s *Server) toolRateFit(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Text       string  `json:"text"`
		Language   string  `json:"language"`
		Slot       float64 `json:"slot"`
		Speed      float64 `json:"speed"`
		Source     string  `json:"source"`
		SourceLang string  `json:"source_language"`
		Rewrite    bool    `json:"rewrite"`
	}
	if err := readJSON(w, r, 256<<10, &b); err != nil {
		writeError(w, err)
		return
	}
	if strings.TrimSpace(b.Text) == "" || b.Slot <= 0 {
		writeError(w, errInvalid("slot", "text and a slot in seconds (> 0) are needed."))
		return
	}
	res, err := s.b.Fit(r.Context(), translate.FitRequest{Text: b.Text, Lang: b.Language, Slot: b.Slot, Speed: b.Speed,
		Source: b.Source, SourceLang: b.SourceLang, Rewrite: b.Rewrite})
	if err != nil {
		var te *translate.Error
		if errors.As(err, &te) {
			writeError(w, errInvalid("", "%v", err))
			return
		}
		writeError(w, err)
		return
	}
	out := map[string]any{"fit": res.Fit}
	if res.Rewrite != "" {
		out["rewrite"] = res.Rewrite
		out["rewrite_fit"] = res.RewriteFit
	}
	writeJSON(w, http.StatusOK, out)
}

// sanity: services is used for the transcript owner and results.
var _ = services.TranscriptOwner
