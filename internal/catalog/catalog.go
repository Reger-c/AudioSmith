// Package catalog describes the models AudioSmith offers: what each one
// does (tasks, languages, voices, capabilities), what it costs (files,
// sizes, memory) and under which licence. It is data, not code: the
// built-in catalogue is catalog.json, embedded, versioned, and validated
// by the tests; a newer one may later be fetched and cached (M7).
//
// The UI reads entries to decide which controls to show; the hub
// (internal/hub) downloads an entry's files; the licence gate reads its
// Licence; the engine confirms capabilities when it loads the model
// (proto.LoadResult).
//
// Imports: lang (to validate language codes).
package catalog

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"unicode"

	"github.com/hehehai/AudioSmith/internal/lang"
)

// Task is a kind of work a model does. One model may do several.
type Task string

const (
	TaskTTS       Task = "tts"       // text → speech
	TaskSTT       Task = "stt"       // speech → text
	TaskAlign     Task = "align"     // forced alignment, word timings
	TaskVAD       Task = "vad"       // voice activity
	TaskDiarize   Task = "diarize"   // who spoke when
	TaskEnhance   Task = "enhance"   // denoise / dereverb
	TaskSeparate  Task = "separate"  // stems (vocals / background)
	TaskLID       Task = "lid"       // language identification
	TaskLLM       Task = "llm"       // text generation (cleanup, direction)
	TaskTranslate Task = "translate" // machine translation
)

var knownTasks = []Task{TaskTTS, TaskSTT, TaskAlign, TaskVAD, TaskDiarize, TaskEnhance, TaskSeparate, TaskLID, TaskLLM, TaskTranslate}

// Capability is an optional feature of a model, which shows or hides a
// control and lets the API reject a request early.
type Capability string

const (
	CapSpeed      Capability = "speed"      // honours a speed factor
	CapSampling   Capability = "sampling"   // honours temperature / top-p / max tokens
	CapClone      Capability = "clone"      // clones from reference audio (+ transcript)
	CapVoiceStyle Capability = "voiceStyle" // the voice field takes a free-form style prompt
	CapLanguage   Capability = "language"   // takes a language parameter (else it follows the voice)
	CapInstruct   Capability = "instruct"   // takes a style/emotion instruction
	CapDesign     Capability = "design"     // makes a voice from a description
	CapSeed       Capability = "seed"       // deterministic with a seed
	CapDuration   Capability = "duration"   // can target a duration
	CapStream     Capability = "stream"     // emits audio chunks while generating
	CapWordTimes  Capability = "wordTimes"  // STT: word-level timestamps

	// M1.
	CapVoicePrompt  Capability = "voicePrompt"  // tts.encodeVoice: a clone reference can be encoded once and reused
	CapSteps        Capability = "steps"        // honours steps (diffusion / refinement steps)
	CapGuidance     Capability = "guidance"     // honours guidance (classifier-free guidance scale)
	CapExaggeration Capability = "exaggeration" // honours exaggeration (Chatterbox emotion intensity)
	CapNonVerbal    Capability = "nonVerbal"    // renders inline non-verbal tags (Entry.NonVerbal)
	CapTranslate    Capability = "translate"    // STT: can translate speech into English (task "translate")
)

// Catalog is a versioned list of entries.
type Catalog struct {
	// Version increases with every published change of the schema or data.
	Version int     `json:"version"`
	Entries []Entry `json:"entries"`
}

// Entry is one downloadable model.
//
// Revision pins the commit the hub downloads and the engine loads (by the
// snapshot's path, offline): an update of the repo never changes a model
// under the user. Moving a pin is a catalogue change, tested on the Mac.
type Entry struct {
	ID       string `json:"id"`                 // stable slug, used in settings and the DB: "kokoro-82m-bf16"
	Label    string `json:"label"`              // shown in pickers (not translated: product names)
	Family   string `json:"family"`             // engine model family (proto.HelloResult.Families): "kokoro", "qwen3_tts"…
	Repo     string `json:"repo"`               // Hugging Face repo id
	Revision string `json:"revision,omitempty"` // commit to download; "" = main
	Tasks    []Task `json:"tasks"`
	About    string `json:"about"` // one or two sentences, English (translated via i18n catalogs)

	Languages    []Language    `json:"languages,omitempty"`
	Voices       []VoiceOption `json:"voices,omitempty"` // built-in speakers; the first is the default
	Capabilities []Capability  `json:"capabilities,omitempty"`
	SampleRate   int           `json:"sampleRate,omitempty"` // Hz; 0 = learned at load

	// MaxChars is the longest text one tts.synthesize may carry, measured
	// with TextLength; the app splits longer scripts into chunks. Longer
	// requests fail with input_too_long. 0 = no known limit.
	MaxChars int `json:"maxChars,omitempty"`
	// NonVerbal lists the inline tags the model renders (CapNonVerbal),
	// as written in the text: "[laughter]", "[sigh]"…
	NonVerbal []string `json:"nonVerbal,omitempty"`
	// RTF is the model's typical real-time factor (seconds of audio per
	// second of generation) measured by the smoke suite on a MacBook Air
	// M4 with 24 GB (e2e_smoke_test.go); the Studio warns about slow
	// renders with it. 0 = not measured.
	RTF float64 `json:"rtf,omitempty"`
	// Deterministic models make the same audio for the same input without
	// a seed (no sampling): their chunk cache ignores the seed.
	Deterministic bool `json:"deterministic,omitempty"`

	// Size is the download size in bytes: the sum of Files when they are
	// listed, else an estimate until the hub lists the repo.
	Size  int64  `json:"size"`
	Files []File `json:"files,omitempty"`
	// MinMemoryGB is the unified memory the model needs to run comfortably.
	MinMemoryGB  float64 `json:"minMemoryGB,omitempty"`
	Quantization string  `json:"quantization,omitempty"` // "bf16", "8bit", "4bit"…

	// Aux are other repos the model loads at run time (Kokoro's G2P
	// models): downloaded with it, before it, and needed for it to count
	// as downloaded. Their sizes are in Size.
	Aux []Aux `json:"aux,omitempty"`

	// Hidden entries are listed on the Models page (download, licence)
	// but not offered by pickers (ForTask, Labels) until the app can run
	// them end to end (none at the end of M0).
	Hidden bool `json:"hidden,omitempty"`

	Licence Licence `json:"licence"`
}

// Aux is an auxiliary repo of an entry.
type Aux struct {
	Repo     string `json:"repo"`
	Revision string `json:"revision,omitempty"`
	Files    []File `json:"files,omitempty"`
	Licence  string `json:"licence,omitempty"` // SPDX-like, for the credits
}

// Language is one language a model speaks or understands.
type Language struct {
	Code  string `json:"code"`            // BCP-47: "en", "en-GB", "zh", "pt-BR"
	Value string `json:"value,omitempty"` // what the model's language parameter takes, when not Code ("Chinese")
}

// VoiceOption is a built-in speaker.
type VoiceOption struct {
	ID       string `json:"id"`                 // what the voice parameter takes
	Language string `json:"language,omitempty"` // BCP-47
	Gender   string `json:"gender,omitempty"`   // "female", "male"
}

// File is one file of the repo to download.
type File struct {
	Path   string `json:"path"` // inside the repo
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256,omitempty"` // LFS files: verified after download
}

// Licence is the model weights' licence (from the upstream model card;
// mlx-community's tags are not reliable).
type Licence struct {
	ID         string `json:"id"`   // SPDX-like: "apache-2.0", "cc-by-nc-4.0"
	Name       string `json:"name"` // "Apache 2.0"
	URL        string `json:"url,omitempty"`
	Commercial bool   `json:"commercial"` // allows commercial use
	// RequiresAcceptance gates first use behind a dialog (non-commercial
	// and other restrictive licences); see core.LicenceService.
	RequiresAcceptance bool `json:"requiresAcceptance,omitempty"`
	// Gated means Hugging Face requires accepting terms on the website
	// and a token to download.
	Gated bool `json:"gated,omitempty"`
	// Verified is false until someone checked the upstream model card.
	Verified bool `json:"verified"`
}

//go:embed catalog.json
var builtinJSON []byte

var builtin = func() *Catalog {
	c, err := Parse(builtinJSON)
	if err != nil {
		panic("catalog.json: " + err.Error()) // caught by TestBuiltin
	}
	return c
}()

// Builtin returns the embedded catalogue. Do not modify it.
func Builtin() *Catalog { return builtin }

// Parse decodes and validates a catalogue.
func Parse(data []byte) (*Catalog, error) {
	var c Catalog
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, err
	}
	return &c, c.Validate()
}

// Validate checks ids, repos and labels are unique and set, tasks,
// capabilities and language codes (internal/lang) are known, and every
// entry has a licence.
func (c *Catalog) Validate() error {
	if c.Version < 1 {
		return fmt.Errorf("version %d", c.Version)
	}
	seen := map[string]bool{}
	for _, e := range c.Entries {
		if e.ID == "" || e.Repo == "" || e.Label == "" || e.Family == "" {
			return fmt.Errorf("entry %q: id, label, family and repo are required", e.ID)
		}
		for _, k := range []string{"id:" + e.ID, "repo:" + e.Repo, "label:" + e.Label} {
			if seen[k] {
				return fmt.Errorf("duplicate %s", k)
			}
			seen[k] = true
		}
		if len(e.Tasks) == 0 {
			return fmt.Errorf("%s: no tasks", e.ID)
		}
		for _, t := range e.Tasks {
			if !slices.Contains(knownTasks, t) {
				return fmt.Errorf("%s: unknown task %q", e.ID, t)
			}
		}
		for _, cp := range e.Capabilities {
			if !slices.Contains(knownCaps, cp) {
				return fmt.Errorf("%s: unknown capability %q", e.ID, cp)
			}
		}
		if e.Licence.ID == "" || e.Licence.Name == "" {
			return fmt.Errorf("%s: no licence", e.ID)
		}
		if !e.Licence.Commercial && !e.Licence.RequiresAcceptance {
			return fmt.Errorf("%s: a non-commercial licence must require acceptance", e.ID)
		}
		if e.Label == CustomLabel {
			return fmt.Errorf("%s: label %q is reserved", e.ID, CustomLabel)
		}
		for _, a := range e.Aux {
			if a.Repo == "" || a.Repo == e.Repo {
				return fmt.Errorf("%s: bad aux repo %q", e.ID, a.Repo)
			}
		}
		for _, l := range e.Languages {
			if lang.Canonical(l.Code) == "" {
				return fmt.Errorf("%s: unknown language code %q", e.ID, l.Code)
			}
		}
	}
	return nil
}

var knownCaps = []Capability{CapSpeed, CapSampling, CapClone, CapVoiceStyle, CapLanguage, CapInstruct, CapDesign, CapSeed, CapDuration, CapStream, CapWordTimes,
	CapVoicePrompt, CapSteps, CapGuidance, CapExaggeration, CapNonVerbal, CapTranslate}

// Default models of the M1 analysis tasks: what the app runs to clean,
// cut and transcribe a clone reference. Their entries may be hidden from
// pickers; find them with ByID.
const (
	DefaultSTT     = "whisper-large-v3-turbo-4bit"
	DefaultAlign   = "qwen3-forcedaligner-0.6b-4bit"
	DefaultVAD     = "silero-vad"
	DefaultEnhance = "deepfilternet-v3"
	// DefaultDictation is the speech-to-text model dictation starts with:
	// on a MacBook Air M4 it transcribes 5 s of speech in 0.2–0.33 s warm
	// (Whisper turbo: 1.2–1.3 s) and Chinese exactly (TestDictationLatency).
	DefaultDictation = "qwen3-asr-0.6b-4bit"
)

// TextLength measures text against Entry.MaxChars: one per character,
// three per CJK character (which a model's phonemiser turns into several
// phonemes).
func TextLength(text string) int {
	n := 0
	for _, r := range text {
		if unicode.In(r, unicode.Han, unicode.Hiragana, unicode.Katakana, unicode.Hangul) {
			n += 3
		} else {
			n++
		}
	}
	return n
}

// ByID, ByRepo and ByLabel find an entry.
func (c *Catalog) ByID(id string) (Entry, bool) {
	return c.find(func(e *Entry) bool { return e.ID == id })
}

func (c *Catalog) ByRepo(repo string) (Entry, bool) {
	return c.find(func(e *Entry) bool { return e.Repo == repo })
}

func (c *Catalog) ByLabel(label string) (Entry, bool) {
	return c.find(func(e *Entry) bool { return e.Label == label })
}

func (c *Catalog) find(match func(*Entry) bool) (Entry, bool) {
	for i := range c.Entries {
		if match(&c.Entries[i]) {
			return c.Entries[i], true
		}
	}
	return Entry{}, false
}

// ForTask returns the entries that do task, in catalogue order, without
// hidden ones.
func (c *Catalog) ForTask(task Task) []Entry {
	var out []Entry
	for _, e := range c.Entries {
		if !e.Hidden && slices.Contains(e.Tasks, task) {
			out = append(out, e)
		}
	}
	return out
}

// Labels returns the labels of the entries that do task, then CustomLabel:
// the options of a model picker.
func (c *Catalog) Labels(task Task) []string {
	var labels []string
	for _, e := range c.ForTask(task) {
		labels = append(labels, e.Label)
	}
	return append(labels, CustomLabel)
}

// CustomLabel is the picker option for a repo that is not in the catalogue.
const CustomLabel = "Custom Hugging Face repo…"

// Custom describes an unknown TTS repo: every control is offered, since
// we cannot tell which ones the model reads. Its licence is unknown, so it
// is treated as requiring no acceptance (the user brought it).
func Custom(repo string) Entry {
	return Entry{
		ID:           "custom",
		Label:        CustomLabel,
		Family:       "",
		Repo:         repo,
		Tasks:        []Task{TaskTTS},
		About:        "Any TTS repo mlx-audio-swift supports (it reads model_type from config.json).",
		Capabilities: []Capability{CapSampling, CapClone, CapSpeed, CapLanguage},
		Licence:      Licence{ID: "unknown", Name: "Unknown", Commercial: true},
	}
}

// Has reports whether the entry has capability c.
func (e Entry) Has(c Capability) bool { return slices.Contains(e.Capabilities, c) }

// VoiceIDs lists the built-in speakers, default first.
func (e Entry) VoiceIDs() []string {
	ids := make([]string, len(e.Voices))
	for i, v := range e.Voices {
		ids[i] = v.ID
	}
	return ids
}

// LanguageOptions lists the values the language parameter takes, default
// first, or nil when the model takes none (CapLanguage unset: Kokoro's
// language follows the voice).
func (e Entry) LanguageOptions() []string {
	if !e.Has(CapLanguage) {
		return nil
	}
	var out []string
	for _, l := range e.Languages {
		if l.Value != "" {
			out = append(out, l.Value)
		} else {
			out = append(out, l.Code)
		}
	}
	return out
}

// HumanSize formats a download size as the picker shows it: "≈ 390 MB",
// "≈ 2 GB", "≈ 1.5 GB" (decimal units); "" for 0.
func HumanSize(bytes int64) string {
	switch {
	case bytes <= 0:
		return ""
	case bytes >= 1e9:
		return "≈ " + strings.TrimSuffix(strconv.FormatFloat(float64(bytes)/1e9, 'f', 1, 64), ".0") + " GB"
	default:
		return "≈ " + strconv.FormatInt((bytes+5e5)/1e6, 10) + " MB"
	}
}

// ShortRepo is a repo id without its owner: "Kokoro-82M-bf16".
func ShortRepo(repo string) string {
	if i := strings.LastIndex(repo, "/"); i >= 0 {
		return repo[i+1:]
	}
	return repo
}

// DisplayName is how to name a model in the UI: its label when the
// catalogue has it, else the repo without its owner.
func (c *Catalog) DisplayName(repo string) string {
	if e, ok := c.ByRepo(repo); ok {
		return e.Label
	}
	return ShortRepo(repo)
}
