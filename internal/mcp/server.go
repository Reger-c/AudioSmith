// Package mcp is AudioSmith's Model Context Protocol server (M5): agents
// (Claude Code, Cursor, Codex…) speak, clone and design voices, and
// transcribe through it. It runs inside the Local API (internal/api),
// mounted at /mcp over Streamable HTTP, and reaches stdio-only clients
// through the shim of stdio.go (AudioSmith --mcp-stdio).
//
// Tools: generate_speech, clone_voice, design_voice, describe_voice,
// transcribe, list_voices, list_models, list_languages, check_health;
// and over the app's modules (studio.go, thread #35) jobs, the project
// library and History, Stories and Audiobook, dubbing and batch, voice
// discovery, exports and workflows.
// Resources: voice://{id}, history://recent.
//
// Every tool goes through api.Server's operations, so the queue, the
// licence gate and the engine checks apply as for HTTP clients. Each
// agent may have its own voice (settings.API.Bindings), found by the
// X-AudioSmith-Client header or, failing that, the client's MCP name.
// Audio comes back inline (base64 audio content), as a file in the
// sandbox folder, or both (settings.API.MCPOutput, or the call's output
// argument). Files are read by path only for clients on this Mac (or with
// the API key); LAN clients with the PIN send base64.
//
// Imports: api, core, settings, voicedesign, and the official MCP Go SDK.
package mcp

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/hehehai/AudioSmith/internal/api"
	"github.com/hehehai/AudioSmith/internal/core"
	vd "github.com/hehehai/AudioSmith/internal/voicedesign"
)

// Options configure a Server.
type Options struct {
	API     *api.Server
	Backend api.Backend
	Version string
}

// Server is the MCP server.
type Server struct {
	o       Options
	srv     *sdk.Server
	handler http.Handler

	mu      sync.Mutex
	clients map[string]*Client
}

// Client is an agent that called the server, for Settings → Local API's
// voice bindings.
type Client struct {
	ID       string    // its client id (header) or MCP name
	Name     string    // its MCP name and version, as it introduced itself
	LastSeen time.Time // its last call
	Calls    int
}

// Instructions tell agents how to use the server: the initialize
// result carries them, and clients like Claude Code show them to the
// model in every new chat. They are a compact form of the agent skill
// the Integrations page makes (integrations_skill.go), so that an agent
// without the skill knows the basics; TestInstructions keeps the tools
// and arguments they name real.
const Instructions = "AudioSmith is a speech app on this Mac, all local (MLX models; nothing leaves the Mac): text to speech, " +
	"voices, transcription, audiobooks and stories, video dubbing, exports and workflows. Use it for speech, narration, " +
	"a voice-over, an audiobook, a dub, a new voice, or a transcript or subtitles.\n" +
	"- generate_speech: text (≤ 100,000 chars) to a WAV. voice = a saved voice (list_voices), a built-in speaker " +
	"(list_models → voices) or empty for your own. output \"file\" writes it to the agents' folder and returns its path " +
	"(name it with filename); \"inline\" puts base64 in your context. instructions sets the delivery.\n" +
	"- transcribe: path = absolute path of audio or video; timestamps: true returns segments (for SRT or VTT); export_transcript saves txt/srt/vtt.\n" +
	"- Voices: design_voice (a description), describe_voice, clone_voice (name + path; consent: true only after the user " +
	"confirms the speaker agreed), search_voices and use_gallery_voice (Discover voices).\n" +
	"- Books and stories: create_audiobook (path of an EPUB, TXT, MD or DOCX), create_story (text of 'Name: line' lines), " +
	"get_longform, update_longform (voice, cast, format m4b; stories: scene, directions, direct: AI director), render_longform, export_longform.\n" +
	"- Dubbing: create_dub (path, target_languages), get_dub, edit_dub, run_dub, export_dub (mp4, wav, srt…); batch_dub, list_batch.\n" +
	"- Library: list_projects, list_takes, export_audio (format, loudness…), list_workflows, run_workflow.\n" +
	"- Long work returns a job_id at once: call get_job with wait_seconds (≤ 60) until done, then report its files; " +
	"cancel_job stops it.\n" +
	"- delete_item needs confirm: true, only after the user said yes.\n" +
	"- list_models (downloaded, licence_to_accept, needs_reference: cloned voices only), list_languages, check_health. " +
	"Resources: voice://{id}, history://recent.\n" +
	"Errors start with a code: licence_required or model_not_downloaded → ask the user to fix it in AudioSmith → Models; " +
	"engine_unavailable, queue_timeout or busy → wait and retry; needs_reference → a cloned voice or another model; " +
	"confirm_required → ask the user. Never change AudioSmith's settings, download models or accept licences yourself. " +
	"If AudioSmith cannot be reached, ask the user to open it with its Local API on. Use only 127.0.0.1; files go to the " +
	"agents' folder unless you give an absolute directory; report the paths of the files you make."

// New makes the MCP server.
func New(o Options) *Server {
	s := &Server{o: o, clients: map[string]*Client{}}
	s.srv = sdk.NewServer(&sdk.Implementation{Name: "audiosmith", Title: "AudioSmith", Version: o.Version},
		&sdk.ServerOptions{Instructions: Instructions})
	s.addTools()
	s.addResources()
	s.handler = sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return s.srv },
		&sdk.StreamableHTTPOptions{SessionTimeout: 30 * time.Minute, MaxRequestBodyBytes: 64 << 20})
	return s
}

// Handler serves MCP's Streamable HTTP (mounted at /mcp by the API).
func (s *Server) Handler() http.Handler { return s.handler }

// Clients lists the agents seen since the app started, most recent first.
func (s *Server) Clients() []Client {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Client, 0, len(s.clients))
	for _, c := range s.clients {
		out = append(out, *c)
	}
	slices.SortFunc(out, func(a, b Client) int { return b.LastSeen.Compare(a.LastSeen) })
	return out
}

// caller is who calls a tool.
type caller struct {
	id  string // binding key: the header, else the MCP client name
	via string // api.Via*
}

func (s *Server) caller(req *sdk.CallToolRequest) caller {
	var c caller
	var h http.Header
	if req.Extra != nil {
		h = req.Extra.Header
	}
	c.via = h.Get(api.HeaderVia)
	c.id = strings.TrimSpace(h.Get(api.HeaderClient))
	name := ""
	if req.Session != nil {
		if p := req.Session.InitializeParams(); p != nil && p.ClientInfo != nil {
			name = strings.TrimSpace(p.ClientInfo.Name)
			if p.ClientInfo.Version != "" {
				name += " " + p.ClientInfo.Version
			}
			if c.id == "" {
				c.id = strings.TrimSpace(p.ClientInfo.Name)
			}
		}
	}
	if c.id != "" {
		s.mu.Lock()
		cl := s.clients[strings.ToLower(c.id)]
		if cl == nil {
			cl = &Client{ID: c.id}
			s.clients[strings.ToLower(c.id)] = cl
		}
		cl.Name, cl.LastSeen = name, time.Now()
		cl.Calls++
		s.mu.Unlock()
	}
	return c
}

// trusted callers may name files anywhere on this Mac.
func (c caller) trusted() bool {
	return c.via == api.ViaLoopback || c.via == api.ViaLANKey || c.via == ""
}

// sandbox is the folder tools write audio to.
func (s *Server) sandbox() string {
	if s.o.API != nil {
		return s.o.API.OutputDir()
	}
	if d := strings.TrimSpace(s.o.Backend.Settings().API.MCPDir); d != "" {
		return d
	}
	return filepath.Join(s.o.Backend.DataDir(), "mcp")
}

// Sandbox is the folder MCP tools write to (for the settings page).
func (s *Server) Sandbox() string { return s.sandbox() }

// readable resolves a path argument: clients on this Mac (or with the
// key) may name any file; LAN clients only files in the sandbox.
func (s *Server) readable(c caller, path string) (string, error) {
	path = strings.TrimSpace(path)
	if strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			path = filepath.Join(home, path[2:])
		}
	}
	if !filepath.IsAbs(path) {
		return "", fmt.Errorf("invalid_request: the path must be absolute (%s)", path)
	}
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", fmt.Errorf("invalid_request: cannot read %s: %v", path, err)
	}
	if c.trusted() {
		return real, nil
	}
	box, err := filepath.EvalSymlinks(s.sandbox())
	if err != nil {
		return "", fmt.Errorf("%s: LAN clients may only read files in AudioSmith's MCP folder; send the audio as base64", api.CodePathNotAllowed)
	}
	if rel, err := filepath.Rel(box, real); err != nil || rel == ".." || strings.HasPrefix(rel, "../") {
		return "", fmt.Errorf("%s: LAN clients may only read files in AudioSmith's MCP folder; send the audio as base64", api.CodePathNotAllowed)
	}
	return real, nil
}

// inputFile is a tool's audio input: a path, or base64 written to a
// temporary file (remove it with the returned func).
func (s *Server) inputFile(c caller, path, b64, name string) (string, func(), error) {
	switch {
	case path != "" && b64 != "":
		return "", nil, errors.New("invalid_request: give path or audio_base64, not both")
	case path != "":
		p, err := s.readable(c, path)
		return p, func() {}, err
	case b64 != "":
		if i := strings.Index(b64, ","); strings.HasPrefix(b64, "data:") && i > 0 {
			b64 = b64[i+1:]
		}
		data, err := base64.StdEncoding.DecodeString(strings.TrimSpace(b64))
		if err != nil {
			return "", nil, fmt.Errorf("invalid_request: audio_base64 is not base64: %v", err)
		}
		dir := filepath.Join(s.o.Backend.TempDir(), "mcp")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return "", nil, err
		}
		ext := strings.ToLower(filepath.Ext(name))
		if ext == "" || len(ext) > 6 {
			ext = ".wav"
		}
		f, err := os.CreateTemp(dir, "input-*"+ext)
		if err != nil {
			return "", nil, err
		}
		_, err = f.Write(data)
		f.Close()
		if err != nil {
			os.Remove(f.Name())
			return "", nil, err
		}
		return f.Name(), func() { os.Remove(f.Name()) }, nil
	}
	return "", nil, errors.New("invalid_request: give the audio as path or audio_base64")
}

// toolError turns an API error into a tool error message with its code.
func toolError(err error) error {
	var e *api.Error
	if errors.As(err, &e) {
		return fmt.Errorf("%s: %s", e.Code, e.Message)
	}
	var lic *core.LicenceRequiredError
	if errors.As(err, &lic) {
		return fmt.Errorf("licence_required: the %s licence of %s must be accepted in AudioSmith → Models first", lic.Name, lic.Model)
	}
	if errors.Is(err, core.ErrNotDownloaded) {
		return fmt.Errorf("model_not_downloaded: %v (download it in AudioSmith → Models)", err)
	}
	// The services' sentinels (not found, invalid, busy…) get their code.
	if e := api.ErrorOf(err); e.Code != "internal" {
		return fmt.Errorf("%s: %s", e.Code, e.Message)
	}
	return err
}

// result is a tool result: its JSON as text and as structured content,
// after any other content.
func result(v any, extra ...sdk.Content) (*sdk.CallToolResult, any, error) {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return nil, nil, err
	}
	content := append(extra, &sdk.TextContent{Text: string(data)})
	return &sdk.CallToolResult{Content: content, StructuredContent: json.RawMessage(data)}, nil, nil
}

// maxInline bounds audio returned inline; longer takes come as files.
const maxInline = 20 << 20

// --- tools ---------------------------------------------------------------------

type speechIn struct {
	Text         string  `json:"text" jsonschema:"What to say. Markup like [pause 500ms] works; [laughter] on models with non-verbal tags."`
	Voice        string  `json:"voice,omitempty" jsonschema:"A saved voice (id or name, see list_voices), a built-in speaker of the model, or an OpenAI voice name. Empty: this agent's voice from AudioSmith's settings, else the model's default."`
	Model        string  `json:"model,omitempty" jsonschema:"A model id from list_models. Empty: AudioSmith's default (or one that can speak the voice)."`
	Language     string  `json:"language,omitempty" jsonschema:"BCP-47 code (en, zh, fr…). Empty: the voice's or the model's."`
	Instructions string  `json:"instructions,omitempty" jsonschema:"How to say it (instruct models), or, without a voice on a design model, the voice to design."`
	Speed        float64 `json:"speed,omitempty" jsonschema:"Speaking rate, 0.5–2 (1 = normal)."`
	Seed         int64   `json:"seed,omitempty" jsonschema:"Seed, for a reproducible take."`
	Duration     float64 `json:"duration,omitempty" jsonschema:"Target length in seconds (OmniVoice)."`
	Steps        int     `json:"steps,omitempty" jsonschema:"Diffusion steps (OmniVoice): 8 draft, 16, 32 quality."`
	Normalize    bool    `json:"normalize,omitempty" jsonschema:"Even out volume (mastering)."`
	Output       string  `json:"output,omitempty" jsonschema:"inline (base64 audio in the result), file (a WAV in AudioSmith's MCP folder), or both. Empty: AudioSmith's setting."`
	Filename     string  `json:"filename,omitempty" jsonschema:"Name of the WAV written in file mode (no folders)."`
}

type speechOut struct {
	TakeID       string   `json:"take_id"`
	Model        string   `json:"model"`
	Voice        string   `json:"voice,omitempty"`
	AudioSeconds float64  `json:"audio_seconds"`
	GenSeconds   float64  `json:"generation_seconds"`
	SampleRate   int      `json:"sample_rate"`
	Seed         int64    `json:"seed,omitempty"`
	File         string   `json:"file,omitempty"`
	AudioURL     string   `json:"audio_url"`
	Ignored      []string `json:"ignored,omitempty"`
	Note         string   `json:"note,omitempty"`
}

func (s *Server) generateSpeech(ctx context.Context, req *sdk.CallToolRequest, in speechIn) (*sdk.CallToolResult, any, error) {
	c := s.caller(req)
	if in.Speed != 0 && (in.Speed < 0.25 || in.Speed > 4) {
		return nil, nil, errors.New("invalid_request: speed must be between 0.25 and 4")
	}
	mode := strings.ToLower(strings.TrimSpace(in.Output))
	if mode == "" {
		mode = s.o.Backend.Settings().API.Output()
	}
	if mode != "inline" && mode != "file" && mode != "both" {
		return nil, nil, errors.New("invalid_request: output is inline, file or both")
	}
	sp, err := s.o.API.StartSpeech(ctx, api.SpeechParams{Text: in.Text, Model: in.Model, Voice: in.Voice, Instructions: in.Instructions,
		Language: in.Language, Speed: in.Speed, Seed: in.Seed, Duration: in.Duration, Steps: in.Steps, Normalize: in.Normalize, Client: c.id})
	if err != nil {
		return nil, nil, toolError(err)
	}
	res, err := sp.Wait(ctx, nil)
	if err != nil {
		return nil, nil, toolError(err)
	}
	out := speechOut{TakeID: res.Take.ID, Model: sp.Model.ID, Voice: sp.VoiceName, AudioSeconds: round2(res.Take.AudioSeconds),
		GenSeconds: round2(res.Take.GenSeconds), SampleRate: res.Take.SampleRate, Seed: res.Take.Seed,
		AudioURL: "/api/takes/" + res.Take.ID + "/audio", Ignored: sp.Ignored}
	var extra []sdk.Content
	if mode == "inline" || mode == "both" {
		data, err := os.ReadFile(res.Output)
		if err != nil {
			return nil, nil, err
		}
		if len(data) > maxInline {
			out.Note = "The take is too long to send inline: it was written to a file."
			mode = "file"
		} else {
			extra = append(extra, &sdk.AudioContent{Data: data, MIMEType: "audio/wav"})
		}
	}
	if mode == "file" || mode == "both" {
		path, err := s.writeFile(res.Output, in.Filename, res.Take.ID)
		if err != nil {
			return nil, nil, err
		}
		out.File = path
	}
	return result(out, extra...)
}

// writeFile copies a take into the sandbox under a safe name.
func (s *Server) writeFile(src, name, id string) (string, error) {
	dir := s.sandbox()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	name = filepath.Base(strings.TrimSpace(name))
	if name == "" || name == "." || name == "/" || strings.HasPrefix(name, ".") {
		name = id + ".wav"
	}
	if !strings.HasSuffix(strings.ToLower(name), ".wav") {
		name += ".wav"
	}
	dst := filepath.Join(dir, name)
	in, err := os.Open(src)
	if err != nil {
		return "", err
	}
	defer in.Close()
	tmp, err := os.CreateTemp(dir, ".audiosmith-*")
	if err != nil {
		return "", err
	}
	_, err = io.Copy(tmp, in)
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp.Name(), dst)
	}
	if err != nil {
		os.Remove(tmp.Name())
		return "", err
	}
	return dst, nil
}

type cloneIn struct {
	Name        string `json:"name" jsonschema:"Name of the new voice."`
	Path        string `json:"path,omitempty" jsonschema:"Absolute path of a recording on this Mac (5–15 s of clean speech is best)."`
	AudioBase64 string `json:"audio_base64,omitempty" jsonschema:"Or the recording itself, base64 (a data: URL works)."`
	Filename    string `json:"filename,omitempty" jsonschema:"The recording's file name, for its format (with audio_base64)."`
	RefText     string `json:"ref_text,omitempty" jsonschema:"What the recording says. Empty lets the engine listen."`
	Language    string `json:"language,omitempty" jsonschema:"BCP-47 code of the speaker's language."`
	Style       string `json:"style,omitempty" jsonschema:"A delivery hint kept with the voice (calm, whisper…)."`
	Consent     bool   `json:"consent" jsonschema:"Confirm that the speaker agreed to having their voice cloned. Required."`
}

func (s *Server) cloneVoice(ctx context.Context, req *sdk.CallToolRequest, in cloneIn) (*sdk.CallToolResult, any, error) {
	c := s.caller(req)
	if !in.Consent {
		return nil, nil, errors.New("consent_required: clone only voices whose owner agreed; confirm with consent: true")
	}
	path, done, err := s.inputFile(c, in.Path, in.AudioBase64, in.Filename)
	if err != nil {
		return nil, nil, err
	}
	defer done()
	v, err := s.o.API.CloneVoice(ctx, api.CloneParams{Name: in.Name, File: path, RefText: in.RefText, Language: in.Language, Style: in.Style})
	if err != nil {
		return nil, nil, toolError(err)
	}
	return result(map[string]any{"voice": voiceOut(v), "note": "Speak with it: generate_speech with voice " + v.ID + " (or its name)."})
}

type designIn struct {
	Description string `json:"description" jsonschema:"The voice in words: gender, age, pitch, accent or dialect, style (a warm older British man; 女，青年，四川话)."`
	Name        string `json:"name,omitempty" jsonschema:"Name of the new voice; empty: from the description."`
	Language    string `json:"language,omitempty" jsonschema:"BCP-47 code it will speak."`
	Seed        int64  `json:"seed,omitempty" jsonschema:"Seed of its identity; empty: a new one."`
	Model       string `json:"model,omitempty" jsonschema:"A model that designs voices (see list_models); empty: the first."`
}

func (s *Server) designVoice(ctx context.Context, req *sdk.CallToolRequest, in designIn) (*sdk.CallToolResult, any, error) {
	s.caller(req)
	d, err := s.o.API.DesignVoice(ctx, api.DesignParams{Name: in.Name, Description: in.Description, Language: in.Language, Seed: in.Seed, Model: in.Model})
	if err != nil {
		return nil, nil, toolError(err)
	}
	return result(map[string]any{"voice": voiceOut(d.Voice), "model": d.Model.ID, "unmatched": d.Unmatched})
}

type describeIn struct {
	Description string `json:"description" jsonschema:"A voice description to read, without saving anything."`
}

func (s *Server) describeVoice(ctx context.Context, req *sdk.CallToolRequest, in describeIn) (*sdk.CallToolResult, any, error) {
	s.caller(req)
	if strings.TrimSpace(in.Description) == "" {
		return nil, nil, errors.New("invalid_request: describe the voice")
	}
	r := vd.Parse(in.Description)
	matched := []map[string]any{}
	for _, m := range r.Matched {
		if m.Applied {
			matched = append(matched, map[string]any{"category": m.Category, "value": m.Value, "phrase": m.Phrase})
		}
	}
	var un []string
	for _, t := range r.Unmatched {
		un = append(un, t.Text)
	}
	return result(map[string]any{"attributes": r.Attrs.Canonical(), "matched": matched, "unmatched": un,
		"sentence": vd.Sentence(r.Attrs, vd.TextLang(in.Description))})
}

type transcribeIn struct {
	Path        string `json:"path,omitempty" jsonschema:"Absolute path of an audio or video file on this Mac."`
	AudioBase64 string `json:"audio_base64,omitempty" jsonschema:"Or the audio itself, base64."`
	Filename    string `json:"filename,omitempty" jsonschema:"Its file name, for its format (with audio_base64)."`
	Model       string `json:"model,omitempty" jsonschema:"A speech-to-text model from list_models; empty: AudioSmith's default."`
	Language    string `json:"language,omitempty" jsonschema:"BCP-47 code; empty detects it."`
	Prompt      string `json:"prompt,omitempty" jsonschema:"Names and terms to expect."`
	Timestamps  bool   `json:"timestamps,omitempty" jsonschema:"Return sentences with their times (and words)."`
}

func (s *Server) transcribe(ctx context.Context, req *sdk.CallToolRequest, in transcribeIn) (*sdk.CallToolResult, any, error) {
	c := s.caller(req)
	path, done, err := s.inputFile(c, in.Path, in.AudioBase64, in.Filename)
	if err != nil {
		return nil, nil, err
	}
	defer done()
	title := filepath.Base(firstNonEmpty(in.Filename, in.Path, "audio")) + " (MCP)"
	tr, err := s.o.API.Transcribe(ctx, api.TranscribeParams{File: path, Title: title, Model: in.Model, Language: in.Language,
		Prompt: in.Prompt, Words: in.Timestamps})
	if err != nil {
		return nil, nil, toolError(err)
	}
	t := tr.Transcript
	out := map[string]any{"text": t.Text(), "language": t.Language, "duration": t.Duration, "transcript_id": t.ID}
	if in.Timestamps {
		var segs []map[string]any
		for _, sg := range t.Segments {
			seg := map[string]any{"start": sg.Start, "end": sg.End, "text": sg.Text}
			if len(sg.Words) > 0 {
				var ws []map[string]any
				for _, w := range sg.Words {
					ws = append(ws, map[string]any{"word": w.Text, "start": w.Start, "end": w.End})
				}
				seg["words"] = ws
			}
			segs = append(segs, seg)
		}
		out["segments"] = segs
	}
	return result(out)
}

type listVoicesIn struct {
	Search string `json:"search,omitempty" jsonschema:"Filter by name, style, description or tags."`
}

func (s *Server) listVoices(ctx context.Context, req *sdk.CallToolRequest, in listVoicesIn) (*sdk.CallToolResult, any, error) {
	c := s.caller(req)
	vs, err := s.o.Backend.Voices(ctx, core.VoiceQuery{Search: in.Search})
	if err != nil {
		return nil, nil, toolError(err)
	}
	saved := []map[string]any{}
	for _, v := range vs {
		saved = append(saved, voiceOut(v))
	}
	out := map[string]any{"saved": saved,
		"openai_names": api.OpenAIVoiceNames(),
		"note":         "Built-in speakers of each model are in list_models (voices)."}
	if b, ok := s.o.Backend.Settings().API.Binding(c.id); ok && b.Voice != "" {
		out["your_voice"] = b.Voice
	}
	return result(out)
}

func voiceOut(v core.Voice) map[string]any {
	m := map[string]any{"id": v.ID, "name": v.Name, "kind": string(v.Kind)}
	if v.Language != "" {
		m["language"] = v.Language
	}
	if v.Style != "" {
		m["style"] = v.Style
	}
	if v.Design != "" {
		m["design"] = v.Design
	}
	if v.Model != "" {
		m["model"] = v.Model
	}
	if v.Verified() {
		m["verified"] = true
	}
	return m
}

type listModelsIn struct {
	Task string `json:"task,omitempty" jsonschema:"tts (speech) or stt (speech to text); empty: both."`
}

func (s *Server) listModels(ctx context.Context, req *sdk.CallToolRequest, in listModelsIn) (*sdk.CallToolResult, any, error) {
	s.caller(req)
	models, err := s.o.Backend.Models(ctx)
	if err != nil {
		return nil, nil, toolError(err)
	}
	var out []map[string]any
	for _, m := range models {
		e := m.Entry
		if e.Hidden {
			continue
		}
		task := ""
		for _, t := range e.Tasks {
			if t == "tts" || t == "stt" {
				task = string(t)
			}
		}
		if task == "" || (in.Task != "" && in.Task != task) {
			continue
		}
		caps := []string{}
		for _, c := range e.Capabilities {
			caps = append(caps, string(c))
		}
		item := map[string]any{"id": e.ID, "label": e.Label, "task": task, "capabilities": caps, "downloaded": m.Downloaded,
			"licence": e.Licence.Name, "commercial_use": e.Licence.Commercial}
		if m.LicenceNeeded {
			item["licence_to_accept"] = true
		}
		if e.NeedsReference { // speaks cloned voices only (MOSS-TTS-Nano)
			item["needs_reference"] = true
		}
		var speakers []string
		for _, v := range e.Voices {
			speakers = append(speakers, v.ID)
		}
		if len(speakers) > 0 {
			item["voices"] = speakers
		}
		out = append(out, item)
	}
	return result(map[string]any{"models": out})
}

type listLanguagesIn struct {
	Model string `json:"model,omitempty" jsonschema:"A model id; empty: every speech model."`
}

func (s *Server) listLanguages(ctx context.Context, req *sdk.CallToolRequest, in listLanguagesIn) (*sdk.CallToolResult, any, error) {
	s.caller(req)
	langs, err := s.o.API.Languages(in.Model)
	if err != nil {
		return nil, nil, toolError(err)
	}
	return result(map[string]any{"count": len(langs), "languages": langs})
}

func (s *Server) checkHealth(ctx context.Context, req *sdk.CallToolRequest, _ struct{}) (*sdk.CallToolResult, any, error) {
	s.caller(req)
	return result(s.o.API.Health())
}

func (s *Server) addTools() {
	sdk.AddTool(s.srv, &sdk.Tool{Name: "generate_speech", Title: "Generate speech",
		Description: "Speak text with a voice on this Mac. Returns the audio (WAV) inline and/or as a file, and the take's details."}, s.generateSpeech)
	sdk.AddTool(s.srv, &sdk.Tool{Name: "clone_voice", Title: "Clone a voice",
		Description: "Save a voice cloned from a recording (a path on this Mac, or base64). Only with the speaker's consent."}, s.cloneVoice)
	sdk.AddTool(s.srv, &sdk.Tool{Name: "design_voice", Title: "Design a voice",
		Description: "Save a new voice made from a description (gender, age, pitch, accent or dialect, style)."}, s.designVoice)
	sdk.AddTool(s.srv, &sdk.Tool{Name: "describe_voice", Title: "Read a voice description",
		Description: "Show how AudioSmith reads a voice description (attributes and words it cannot map), without saving anything."}, s.describeVoice)
	sdk.AddTool(s.srv, &sdk.Tool{Name: "transcribe", Title: "Transcribe",
		Description: "Turn speech in an audio or video file into text, optionally with times."}, s.transcribe)
	sdk.AddTool(s.srv, &sdk.Tool{Name: "list_voices", Title: "List voices",
		Description: "The saved voices, and OpenAI voice names that map to built-in speakers."}, s.listVoices)
	sdk.AddTool(s.srv, &sdk.Tool{Name: "list_models", Title: "List models",
		Description: "Speech and speech-to-text models: what they can do, their built-in voices, whether they are downloaded, their licence, and whether they speak cloned voices only (needs_reference)."}, s.listModels)
	sdk.AddTool(s.srv, &sdk.Tool{Name: "list_languages", Title: "List languages",
		Description: "Languages a model speaks (or every speech model)."}, s.listLanguages)
	sdk.AddTool(s.srv, &sdk.Tool{Name: "check_health", Title: "Check health",
		Description: "Whether AudioSmith's engine is ready, starting or down, and its device."}, s.checkHealth)
	s.addStudioTools() // thread #35: studio.go
}

// --- resources -------------------------------------------------------------------

func (s *Server) addResources() {
	s.srv.AddResourceTemplate(&sdk.ResourceTemplate{Name: "voice", Title: "A saved voice", URITemplate: "voice://{id}",
		Description: "A saved voice's profile (JSON).", MIMEType: "application/json"},
		func(ctx context.Context, req *sdk.ReadResourceRequest) (*sdk.ReadResourceResult, error) {
			uri := req.Params.URI
			id := strings.TrimPrefix(uri, "voice://")
			v, err := s.o.Backend.Voice(ctx, id)
			if err != nil {
				return nil, sdk.ResourceNotFoundError(uri)
			}
			data, _ := json.MarshalIndent(voiceOut(v), "", "  ")
			return &sdk.ReadResourceResult{Contents: []*sdk.ResourceContents{{URI: uri, MIMEType: "application/json", Text: string(data)}}}, nil
		})
	s.srv.AddResource(&sdk.Resource{Name: "history", Title: "Recent takes", URI: "history://recent",
		Description: "The 20 most recent takes of the History (JSON).", MIMEType: "application/json"},
		func(ctx context.Context, req *sdk.ReadResourceRequest) (*sdk.ReadResourceResult, error) {
			ts, err := s.o.Backend.Takes(ctx, core.TakeQuery{Limit: 20})
			if err != nil {
				return nil, err
			}
			var out []map[string]any
			for _, t := range ts {
				out = append(out, map[string]any{"id": t.ID, "created_at": t.CreatedAt, "text": t.Text, "model": t.Model,
					"voice_id": t.VoiceID, "audio_seconds": round2(t.AudioSeconds), "audio_url": "/api/takes/" + t.ID + "/audio"})
			}
			data, _ := json.MarshalIndent(map[string]any{"takes": out}, "", "  ")
			return &sdk.ReadResourceResult{Contents: []*sdk.ResourceContents{{URI: req.Params.URI, MIMEType: "application/json", Text: string(data)}}}, nil
		})
}

func round2(f float64) float64 { return float64(int64(f*100+0.5)) / 100 }

func firstNonEmpty(xs ...string) string {
	for _, x := range xs {
		if strings.TrimSpace(x) != "" {
			return x
		}
	}
	return ""
}
