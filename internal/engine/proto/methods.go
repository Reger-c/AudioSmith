package proto

// Role is what an engine process is for; one Swift binary runs as any of
// them (audiosmith-engine --role gpu|listen|media).
type Role string

const (
	RoleGPU    Role = "gpu"    // MLX models: TTS, STT, LLM, VAD, diarization, separation
	RoleListen Role = "listen" // microphone capture + small streaming ASR (M2)
	RoleMedia  Role = "media"  // AVFoundation/PDFKit/Vision, no MLX (M4)
)

// Methods of protocol v2 implemented in M0. Requests marked "job" are
// long-running: the engine may send job.progress (and tts.chunk) for them,
// job.cancel stops them, and they count against hello's concurrency.
// Other requests are control requests: answered promptly, even while jobs
// run, and not cancellable.
const (
	MethodHello    = "engine.hello"    // request; must be first
	MethodStats    = "engine.stats"    // request
	MethodFlush    = "engine.flush"    // request: drop MLX caches
	MethodShutdown = "engine.shutdown" // request: cancel jobs, answer, exit 0

	MethodModelsLoad   = "models.load"   // request, job
	MethodModelsUnload = "models.unload" // request
	MethodModelsLoaded = "models.loaded" // request

	MethodTTSSynthesize = "tts.synthesize" // request, job

	MethodJobCancel = "job.cancel" // request

	// Notifications, engine → app.
	MethodJobProgress = "job.progress"
	MethodTTSChunk    = "tts.chunk"
	MethodLog         = "log"
)

// Reserved method names for later milestones (see docs/protocol-v2.md §8).
// Listed so names are not reused for something else; not implemented.
var Reserved = []string{
	"tts.encodeVoice", "tts.batch",
	"stt.transcribe", "stt.align", "stt.stream.open", "stt.stream.push", "stt.stream.close", "stt.partial", "stt.final",
	"vad.detect", "diarize.run", "lid.detect", "enhance.run", "separate.run",
	"llm.generate", "translate.run",
	"media.probe", "media.decode", "media.encode", "media.mux", "media.timePitch", "media.thumbnail", "media.ocr", "doc.extractText",
	"capture.devices", "capture.start", "capture.stop", "capture.level", "capture.pcm",
}

// HelloParams opens a session. The app sends it first, right after
// spawning the process; until it is answered every other request fails
// with not_initialized.
type HelloParams struct {
	Protocol int    `json:"protocol"`           // Version
	App      string `json:"app"`                // app version, for logs
	Role     Role   `json:"role"`               // must match the process's --role
	CacheDir string `json:"cacheDir,omitempty"` // Hugging Face hub cache the app manages; "" = engine default
	Offline  bool   `json:"offline,omitempty"`  // never download: missing files → model_not_downloaded
	LogLevel string `json:"logLevel,omitempty"` // "debug" | "info" (default) | "warning" | "error"
}

// HelloResult describes the engine. The app refuses (and reports) an
// engine whose Protocol differs from Version or whose Role differs.
type HelloResult struct {
	Protocol    int      `json:"protocol"`
	Version     string   `json:"version"` // engine build version
	Role        Role     `json:"role"`
	PID         int      `json:"pid"`
	Concurrency int      `json:"concurrency"` // jobs it runs at once; more → busy
	Methods     []string `json:"methods"`     // requests it implements
	Families    []string `json:"families"`    // model families it can load (catalog.Entry.Family)
	Device      Device   `json:"device"`
}

// Device is the machine the engine runs on.
type Device struct {
	Name   string `json:"name"`   // "Apple M2 Pro"
	Memory int64  `json:"memory"` // physical memory, bytes
	OS     string `json:"os"`     // "macOS 15.1"
	MLX    string `json:"mlx,omitempty"`
}

// StatsResult answers engine.stats: memory for the supervisor's budget and
// the ⌘E switcher.
type StatsResult struct {
	Memory Memory        `json:"memory"`
	Models []LoadedModel `json:"models"`
	Jobs   []string      `json:"jobs"` // ids of running jobs
}

// Memory is MLX's accounting, in bytes.
type Memory struct {
	Active int64 `json:"active"`
	Cache  int64 `json:"cache"`
	Peak   int64 `json:"peak"`
}

// LoadedModel is a resident model.
type LoadedModel struct {
	Model       string  `json:"model"` // repo id
	Task        string  `json:"task"`  // "tts", "stt"… (catalog task)
	SampleRate  int     `json:"sampleRate,omitempty"`
	Bytes       int64   `json:"bytes,omitempty"`       // estimated resident size
	IdleSeconds float64 `json:"idleSeconds,omitempty"` // since last use, for LRU eviction
}

// LoadParams loads a model ahead of use (jobs load what they need anyway).
type LoadParams struct {
	Model string `json:"model"`          // repo id
	Path  string `json:"path,omitempty"` // local snapshot directory; "" = resolve Model in the cache
	Task  string `json:"task,omitempty"` // "" = "tts"
}

// LoadResult reports what the model turned out to support; it can confirm
// or extend the catalogue entry.
type LoadResult struct {
	Model        string   `json:"model"`
	Task         string   `json:"task"`
	SampleRate   int      `json:"sampleRate"`
	Seconds      float64  `json:"seconds"` // load time
	Bytes        int64    `json:"bytes,omitempty"`
	Voices       []string `json:"voices,omitempty"`
	Languages    []string `json:"languages,omitempty"`
	Capabilities []string `json:"capabilities,omitempty"` // catalog capability names
}

// UnloadParams frees one model, or all with Model "".
type UnloadParams struct {
	Model string `json:"model,omitempty"`
}

// UnloadResult lists what was unloaded.
type UnloadResult struct {
	Unloaded []string `json:"unloaded"`
}

// LoadedResult answers models.loaded.
type LoadedResult struct {
	Models []LoadedModel `json:"models"`
}

// SynthesizeParams makes speech from text into a file. Unset optional
// parameters use the model's defaults. The model loads first if needed
// (stage "loading"); a model that cannot honour a set parameter ignores it
// unless that parameter is the point of the request (refAudio on a model
// that cannot clone → unsupported).
type SynthesizeParams struct {
	Model string `json:"model"`
	// ModelPath is the model's local snapshot directory (hub.Local.Snapshot),
	// as in LoadParams.Path; "" resolves Model in the cache. The app fills
	// it when the job runs, so engines started offline never download.
	ModelPath   string   `json:"modelPath,omitempty"`
	Text        string   `json:"text"`
	Voice       string   `json:"voice,omitempty"`    // speaker id or style prompt
	Language    string   `json:"language,omitempty"` // value the model takes ("en", "Chinese"…)
	Instruct    string   `json:"instruct,omitempty"` // style/emotion instruction (M1)
	Speed       *float64 `json:"speed,omitempty"`
	Temperature *float64 `json:"temperature,omitempty"`
	TopP        *float64 `json:"topP,omitempty"`
	MaxTokens   int      `json:"maxTokens,omitempty"`
	Seed        *int64   `json:"seed,omitempty"`
	Steps       int      `json:"steps,omitempty"`    // diffusion/refine steps (OmniVoice, M1)
	Duration    *float64 `json:"duration,omitempty"` // target seconds (OmniVoice, M1)
	RefAudio    string   `json:"refAudio,omitempty"` // absolute path of a reference clip (cloning)
	RefText     string   `json:"refText,omitempty"`
	Output      Output   `json:"output"`
	// Stream asks for tts.chunk notifications as audio is generated, in
	// addition to the file.
	Stream bool `json:"stream,omitempty"`
}

// Output says where and how to write audio. The engine writes to a
// temporary name beside Path and renames it, so Path never holds a partial file.
type Output struct {
	Path   string `json:"path"`             // absolute; the app owns the directory
	Format string `json:"format,omitempty"` // "wav" (default; the only one in M0)
	Bits   int    `json:"bits,omitempty"`   // 16 (default) | 24 | 32 (float)
}

// SynthesizeResult describes the written file.
type SynthesizeResult struct {
	Output       string  `json:"output"`
	Model        string  `json:"model"`
	SampleRate   int     `json:"sampleRate"`
	Channels     int     `json:"channels"`
	Frames       int64   `json:"frames"`
	AudioSeconds float64 `json:"audioSeconds"`
	Seconds      float64 `json:"seconds"` // generation time, excluding model load
	RTF          float64 `json:"rtf"`     // audioSeconds / seconds
	Seed         *int64  `json:"seed,omitempty"`
}

// CancelParams names the job (the id of its request) to cancel.
type CancelParams struct {
	Job string `json:"job"`
}

// CancelResult says whether a running or queued job was found. The job's
// own request then answers with a cancelled error.
type CancelResult struct {
	Cancelled bool `json:"cancelled"`
}

// Empty is the params/result of methods that take or return nothing ({}).
type Empty struct{}

// ProgressParams reports a job's progress. All of a job's notifications
// are written before its response.
type ProgressParams struct {
	Job          string   `json:"job"`
	Stage        string   `json:"stage"`              // "loading" | "generating" | "writing" (open set)
	Fraction     *float64 `json:"fraction,omitempty"` // 0–1; absent when unknown
	AudioSeconds float64  `json:"audioSeconds,omitempty"`
	Message      string   `json:"message,omitempty"` // English, for logs and the status line
}

// ChunkParams carries generated audio while a streaming job runs. Seq
// counts from 0 without gaps; Final marks the last chunk (which may be
// empty). Data is base64 in JSON (Go []byte, Swift Data).
type ChunkParams struct {
	Job        string `json:"job"`
	Seq        int    `json:"seq"`
	SampleRate int    `json:"sampleRate"`
	Channels   int    `json:"channels"`
	Encoding   string `json:"encoding"` // "pcm16le" (the only one in M0)
	Data       []byte `json:"data"`
	Final      bool   `json:"final,omitempty"`
}

// LogParams is a structured log line (stderr remains free-form text).
type LogParams struct {
	Level   string `json:"level"` // "debug" | "info" | "warning" | "error"
	Message string `json:"message"`
	Job     string `json:"job,omitempty"`
}

// ParamsFor returns a new value of the params type of method, or nil for
// an unknown method. ResultFor does the same for results. The supervisor
// and the fixture tests use them.
func ParamsFor(method string) any {
	switch method {
	case MethodHello:
		return new(HelloParams)
	case MethodStats, MethodFlush, MethodShutdown, MethodModelsLoaded:
		return new(Empty)
	case MethodModelsLoad:
		return new(LoadParams)
	case MethodModelsUnload:
		return new(UnloadParams)
	case MethodTTSSynthesize:
		return new(SynthesizeParams)
	case MethodJobCancel:
		return new(CancelParams)
	case MethodJobProgress:
		return new(ProgressParams)
	case MethodTTSChunk:
		return new(ChunkParams)
	case MethodLog:
		return new(LogParams)
	}
	return nil
}

// ResultFor: see ParamsFor.
func ResultFor(method string) any {
	switch method {
	case MethodHello:
		return new(HelloResult)
	case MethodStats:
		return new(StatsResult)
	case MethodFlush, MethodShutdown:
		return new(Empty)
	case MethodModelsLoad:
		return new(LoadResult)
	case MethodModelsUnload:
		return new(UnloadResult)
	case MethodModelsLoaded:
		return new(LoadedResult)
	case MethodTTSSynthesize:
		return new(SynthesizeResult)
	case MethodJobCancel:
		return new(CancelResult)
	}
	return nil
}

// IsJob reports whether method is a long-running, cancellable job.
func IsJob(method string) bool {
	return method == MethodModelsLoad || method == MethodTTSSynthesize
}
