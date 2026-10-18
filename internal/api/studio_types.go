package api

// The requests and answers of the Studio operations (studio.go). The
// same structs are REST bodies (ids from the path win) and MCP tool
// inputs: their jsonschema tags describe the tools' arguments.

import (
	"encoding/json"
	"time"
)

// --- the project library -------------------------------------------------------------

// ProjectQuery lists the project library.
type ProjectQuery struct {
	Category string `json:"category,omitempty" jsonschema:"story, audiobook, dub, workflow, voice, transcript, take, export or call; empty: all."`
	Search   string `json:"search,omitempty" jsonschema:"Words in titles, scripts, names or paths."`
	Limit    int    `json:"limit,omitempty" jsonschema:"At most this many (default 50, up to 500)."`
	Offset   int    `json:"offset,omitempty" jsonschema:"Skip this many (paging)."`
}

// ProjectJSON is an item of the project library.
type ProjectJSON struct {
	ID       string          `json:"id"`
	Source   string          `json:"source"`   // project, render, voice, take, export, dub, workflow, transcript, call
	Category string          `json:"category"` // the Projects page's filter
	Title    string          `json:"title"`
	Detail   string          `json:"detail,omitempty"`
	Summary  json.RawMessage `json:"summary,omitempty"` // a project's (model, chapters, lines, words, seconds…)
	Updated  time.Time       `json:"updated"`
	// Tool is the tool that opens it (get_longform, get_dub…).
	Tool string `json:"tool,omitempty"`
}

// --- voice discovery ---------------------------------------------------------------------

// GalleryQuery searches Discover voices with the page's filters.
type GalleryQuery struct {
	ID       string `json:"id,omitempty" jsonschema:"One voice by id (the other filters are ignored)."`
	Query    string `json:"query,omitempty" jsonschema:"Words that must all occur in a voice's name, description, tags or attributes (English or Chinese)."`
	Category string `json:"category,omitempty" jsonschema:"narration, conversational, characters, social, advertising, education, news, games or wellness."`
	Gender   string `json:"gender,omitempty" jsonschema:"female, male or neutral."`
	Age      string `json:"age,omitempty" jsonschema:"child, teen, young_adult, middle_aged or elderly (see facets)."`
	Pitch    string `json:"pitch,omitempty" jsonschema:"An pitch value (see facets): very_low, low, moderate, high, very_high."`
	Accent   string `json:"accent,omitempty" jsonschema:"An English accent or Chinese dialect (british, american, sichuan…; see facets)."`
	Language string `json:"language,omitempty" jsonschema:"A recommended language (BCP-47 primary code: en, zh, ja…)."`
	Style    string `json:"style,omitempty" jsonschema:"A style value (see facets)."`
	Limit    int    `json:"limit,omitempty" jsonschema:"At most this many (default 20, up to 100)."`
	Offset   int    `json:"offset,omitempty" jsonschema:"Skip this many (paging)."`
}

// GalleryVoiceJSON is a voice of Discover voices (an archetype).
type GalleryVoiceJSON struct {
	ID          string          `json:"id"`
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Category    string          `json:"category"`
	Languages   []string        `json:"languages"`
	Tags        []string        `json:"tags,omitempty"`
	Attributes  json.RawMessage `json:"attributes"`
	Sample      string          `json:"sample_text,omitempty"`
	Featured    bool            `json:"featured,omitempty"`
	Saved       string          `json:"saved_voice_id,omitempty"` // "Use voice" made it already
}

// FacetValueJSON is a filter value and how many voices it gives.
type FacetValueJSON struct {
	Value string `json:"value"`
	Label string `json:"label"`
	Count int    `json:"count"`
}

// GalleryPage is a page of results.
type GalleryPage struct {
	Total  int                         `json:"total"`
	Offset int                         `json:"offset"`
	Next   int                         `json:"next"` // -1: none
	Items  []GalleryVoiceJSON          `json:"voices"`
	Facets map[string][]FacetValueJSON `json:"facets,omitempty"`
}

// GalleryUse is what "Use voice" made.
type GalleryUse struct {
	Voice   VoiceJSON `json:"voice"`
	Created bool      `json:"created"` // false: it was saved before
	// Model is the model it was designed for; Models those that can speak
	// it (design models render it as designed; others need its sample).
	Model      string   `json:"model,omitempty"`
	Models     []string `json:"models,omitempty"`
	Downloaded bool     `json:"model_downloaded"`
	Note       string   `json:"note,omitempty"`
}

// GalleryUseIn is use_gallery_voice's input.
type GalleryUseIn struct {
	ID string `json:"id" jsonschema:"The voice's id from search_voices."`
}

// --- exporting takes ---------------------------------------------------------------------

// TakeExport writes a take with the Export audio dialog's options.
type TakeExport struct {
	ID         string `json:"take_id" jsonschema:"The take (generate_speech's take_id, or list_takes)."`
	Format     string `json:"format,omitempty" jsonschema:"wav (default), m4a, mp3, flac or opus (mp3, flac and opus need ffmpeg; m4a the engine)."`
	Bits       int    `json:"bits,omitempty" jsonschema:"wav: 16, 24 or 32 (float); flac: 16 or 24. Default 24 (wav), 16 (flac)."`
	Bitrate    int    `json:"bitrate_kbps,omitempty" jsonschema:"m4a 128/192/256, mp3 128/192/320, opus 64/96/128; 0: the format's default."`
	SampleRate int    `json:"sample_rate,omitempty" jsonschema:"44100 or 48000; 0: the take's (opus is always 48000)."`
	Channels   int    `json:"channels,omitempty" jsonschema:"1 (mono) or 2 (stereo); 0: the take's."`
	Loudness   string `json:"loudness,omitempty" jsonschema:"podcast (−16 LUFS), speech (−18 LUFS), broadcast (−23 LUFS) or off (default)."`
	Trim       bool   `json:"trim,omitempty" jsonschema:"Trim leading and trailing silence."`
	Title      string `json:"title,omitempty" jsonschema:"Title tag (not wav)."`
	Artist     string `json:"artist,omitempty" jsonschema:"Artist tag (not wav)."`
	Script     bool   `json:"script,omitempty" jsonschema:"Also save the text beside it (.txt)."`
	Subtitles  bool   `json:"subtitles,omitempty" jsonschema:"Also save subtitles beside it (.srt), timed to the audio."`
	Dir        string `json:"directory,omitempty" jsonschema:"Absolute folder to write to; empty: AudioSmith's agents' folder."`
	Filename   string `json:"filename,omitempty" jsonschema:"The file's name (no folders); empty: from the text."`
	Output     string `json:"-"`
}

// --- workflows -------------------------------------------------------------------------

// WorkflowJSON is a saved workflow and its inputs.
type WorkflowJSON struct {
	ID      string          `json:"workflow_id"`
	Name    string          `json:"name"`
	Steps   string          `json:"steps"` // "start → speak → export"
	Input   string          `json:"input"` // "scripts" or "files"
	Scripts []string        `json:"scripts,omitempty"`
	Files   []string        `json:"files,omitempty"`
	Exports []string        `json:"export_folders,omitempty"`
	LastRun *WorkflowRunOut `json:"last_run,omitempty"`
	JobID   string          `json:"job_id"` // "workflow:<id>"
	Problem []string        `json:"problems,omitempty"`
	Updated time.Time       `json:"updated_at"`
}

// WorkflowRunOut sums up a run.
type WorkflowRunOut struct {
	State string `json:"state"`
	Items int    `json:"items"`
	Done  int    `json:"done"`
}

// WorkflowRun runs a saved workflow.
type WorkflowRun struct {
	ID      string   `json:"workflow_id" jsonschema:"The workflow (list_workflows)."`
	Scripts []string `json:"scripts,omitempty" jsonschema:"Workflows that start with scripts: the scripts to run this time (each one an item); the saved ones stay."`
	Files   []string `json:"files,omitempty" jsonschema:"Workflows that start with audio: absolute paths of the files to run this time; the saved ones stay."`
	Again   bool     `json:"again,omitempty" jsonschema:"Make every result anew (default: keep results whose inputs did not change)."`
}
