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

// --- dubbing ------------------------------------------------------------------------------

// DubCreate makes a dub of a file and, unless Through is "none", starts it.
type DubCreate struct {
	Path           string   `json:"path" jsonschema:"Absolute path of the video or audio to dub (mp4, mov, mkv, mp3, wav, m4a…)."`
	Title          string   `json:"title,omitempty" jsonschema:"The project's title; empty: the file's name."`
	SourceLanguage string   `json:"source_language,omitempty" jsonschema:"BCP-47 code spoken in the file; empty: detected."`
	Targets        []string `json:"target_languages,omitempty" jsonschema:"BCP-47 codes to dub into, the main one first; empty: AudioSmith's Dubbing settings (by default Chinese, or English for a video in Chinese)."`
	Model          string   `json:"model,omitempty" jsonschema:"The speech model (list_models); empty: AudioSmith's dubbing default (it clones each speaker)."`
	Voice          string   `json:"voice,omitempty" jsonschema:"One voice for every speaker (a saved voice's name or id); empty: each speaker's own voice, cloned."`
	Speakers       int      `json:"speakers,omitempty" jsonschema:"How many people speak (1–20); 0: detected."`
	KeepBackground *bool    `json:"keep_background,omitempty" jsonschema:"Keep the music and effects under the dub (default true)."`
	Through        string   `json:"through,omitempty" jsonschema:"How far to run now: transcript, translation, dub (default: the whole dub) or none."`
}

// DubQuery reads a dub.
type DubQuery struct {
	ID       string `json:"dub_id" jsonschema:"The dub (create_dub or list_projects with category dub)."`
	Language string `json:"language,omitempty" jsonschema:"The target language whose translations to show; empty: the main one."`
	Segments *bool  `json:"segments,omitempty" jsonschema:"Include the lines (default true)."`
}

// DubRun runs a dub's pipeline.
type DubRun struct {
	ID           string   `json:"dub_id" jsonschema:"The dub."`
	Through      string   `json:"through,omitempty" jsonschema:"transcript, translation, dub (default) or verify (a second listen that flags lines)."`
	Languages    []string `json:"languages,omitempty" jsonschema:"Only these target languages; empty: all."`
	Retranslate  bool     `json:"retranslate,omitempty" jsonschema:"Translate every line again (edited lines too)."`
	Retranscribe bool     `json:"retranscribe,omitempty" jsonschema:"Transcribe the source again (drops the lines and their edits)."`
	Segments     []string `json:"segments,omitempty" jsonschema:"Only re-render these line ids; empty: the lines that changed."`
}

// DubSegmentEdit changes one line of a dub.
type DubSegmentEdit struct {
	ID       string `json:"id" jsonschema:"The line's id (get_dub)."`
	Text     string `json:"text,omitempty" jsonschema:"New source text (drops its translations, which are made again)."`
	Language string `json:"language,omitempty" jsonschema:"With translation: its target language; empty: the main one."`
	Trans    string `json:"translation,omitempty" jsonschema:"New translated text in language."`
	Speaker  string `json:"speaker,omitempty" jsonschema:"Another speaker label (S1, S2…)."`
	Voice    string `json:"voice,omitempty" jsonschema:"A voice for this line only (a saved voice's name or id); default: the speaker's."`
}

// DubEdit edits lines and the cast of a dub.
type DubEdit struct {
	ID       string            `json:"dub_id" jsonschema:"The dub."`
	Segments []DubSegmentEdit  `json:"segments,omitempty" jsonschema:"Line edits."`
	Cast     map[string]string `json:"cast,omitempty" jsonschema:"Speaker label (S1…) or * (everyone) → voice (name or id); default: the speaker's cloned voice."`
	Names    map[string]string `json:"names,omitempty" jsonschema:"Speaker label → display name."`
}

// DubSegmentJSON is a line of a dub.
type DubSegmentJSON struct {
	ID          string  `json:"id"`
	Start       float64 `json:"start"`
	End         float64 `json:"end"`
	Speaker     string  `json:"speaker,omitempty"`
	Text        string  `json:"text"`
	Translation string  `json:"translation,omitempty"`
	Voice       string  `json:"voice,omitempty"`
	Rendered    bool    `json:"rendered,omitempty"`
	Stale       bool    `json:"stale,omitempty"` // changed since its audio was made
	Error       string  `json:"error,omitempty"`
}

// DubStageJSON is a stage's last run.
type DubStageJSON struct {
	Status   string   `json:"status"`
	Code     string   `json:"code,omitempty"`
	Message  string   `json:"message,omitempty"`
	Warnings []string `json:"warnings,omitempty"`
}

// DubTrackJSON is a dubbed track.
type DubTrackJSON struct {
	File    string  `json:"file,omitempty"`
	Speech  string  `json:"speech,omitempty"`
	Seconds float64 `json:"seconds,omitempty"`
	Flagged int     `json:"flagged,omitempty"` // lines a verify flagged
}

// DubSpeakerJSON is a speaker and their voice.
type DubSpeakerJSON struct {
	Label string `json:"label"`
	Name  string `json:"name,omitempty"`
	Voice string `json:"voice,omitempty"` // "" = cloned from the source
}

// DubJSON is a dub project.
type DubJSON struct {
	ID         string                  `json:"dub_id"`
	Title      string                  `json:"title"`
	Source     string                  `json:"source"`
	Duration   float64                 `json:"duration,omitempty"`
	SourceLang string                  `json:"source_language,omitempty"`
	Detected   string                  `json:"detected_language,omitempty"`
	Targets    []string                `json:"target_languages"`
	Phase      string                  `json:"phase"`
	Model      string                  `json:"model,omitempty"`
	Translator string                  `json:"translator,omitempty"`
	Batch      bool                    `json:"batch,omitempty"`
	Running    bool                    `json:"running"`
	JobID      string                  `json:"job_id"` // "dub:<dub_id>"
	Speakers   []DubSpeakerJSON        `json:"speakers,omitempty"`
	Stages     map[string]DubStageJSON `json:"stages,omitempty"`
	Tracks     map[string]DubTrackJSON `json:"tracks,omitempty"`
	Language   string                  `json:"language,omitempty"` // of the translations shown
	Segments   []DubSegmentJSON        `json:"segments,omitempty"`
	Updated    time.Time               `json:"updated_at"`
	// Warning: the request named a removed model; its replacement is used.
	Warning string `json:"warning,omitempty"`
}

// DubExport saves a dub.
type DubExport struct {
	ID        string   `json:"dub_id" jsonschema:"The dub."`
	Format    string   `json:"format" jsonschema:"mp4 (the video with dubbed audio tracks), wav, m4a or mp3 (a dubbed track; mp3 needs ffmpeg), srt, vtt or ass (subtitles), stems or segments (ZIP), burn (MP4 with burnt-in subtitles; needs ffmpeg)."`
	Language  string   `json:"language,omitempty" jsonschema:"The language of a track or subtitles (orig: the source's); empty: the main target."`
	Tracks    []string `json:"tracks,omitempty" jsonschema:"mp4: the audio tracks, languages and orig; empty: the main target and orig."`
	Default   string   `json:"default_track,omitempty" jsonschema:"mp4: the track players pick; empty: the first."`
	Subtitles bool     `json:"subtitles,omitempty" jsonschema:"mp4: add soft subtitle tracks."`
	Dual      bool     `json:"dual,omitempty" jsonschema:"Subtitles with the translation and the original."`
	Bits      int      `json:"bits,omitempty" jsonschema:"wav: 16, 24 (default) or 32."`
	Dir       string   `json:"directory,omitempty" jsonschema:"Absolute folder to write to; empty: AudioSmith's agents' folder."`
	Filename  string   `json:"filename,omitempty" jsonschema:"The file's name (no folders); empty: from the title and language."`
	Output    string   `json:"-"`
}

// BatchRequest dubs files with the batch queue.
type BatchRequest struct {
	Paths          []string `json:"paths,omitempty" jsonschema:"Absolute paths of videos or audio files."`
	Folder         string   `json:"folder,omitempty" jsonschema:"Or an absolute folder: every video and audio file in it (not its subfolders)."`
	Targets        []string `json:"target_languages,omitempty" jsonschema:"BCP-47 codes to dub into; empty: the Batch page's."`
	SourceLanguage string   `json:"source_language,omitempty" jsonschema:"BCP-47 code spoken; empty: the Batch page's (or detected)."`
	Voice          string   `json:"voice,omitempty" jsonschema:"One voice for every speaker (a saved voice's name or id); empty: the Batch page's (or each speaker cloned)."`
	Model          string   `json:"model,omitempty" jsonschema:"The speech model; empty: the Batch page's (or the dubbing default)."`
	KeepBackground *bool    `json:"keep_background,omitempty" jsonschema:"Keep music and effects; empty: the Batch page's."`
}

// BatchItemJSON is a video of the batch queue.
type BatchItemJSON struct {
	ID        string   `json:"dub_id"`
	JobID     string   `json:"job_id"`
	Title     string   `json:"title"`
	Source    string   `json:"source"`
	Languages []string `json:"target_languages"`
	Phase     string   `json:"phase"`
	Updated   string   `json:"updated_at,omitempty"`
}

// BatchJSON is the batch queue (and what a request added).
type BatchJSON struct {
	Added  []BatchItemJSON    `json:"added,omitempty"`
	Errors []BatchErrorJSON   `json:"errors,omitempty"`
	Queue  []BatchItemJSON    `json:"queue,omitempty"`
	Watch  *WatchFolderJSON   `json:"watch_folder,omitempty"`
	Prefs  *BatchSettingsJSON `json:"settings,omitempty"`
	// Warning: the request named a removed model; its replacement is used.
	Warning string `json:"warning,omitempty"`
}

// BatchErrorJSON is a file the batch could not take.
type BatchErrorJSON struct {
	Path    string `json:"path"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

// WatchFolderJSON is the Batch page's watch folder (read only).
type WatchFolderJSON struct {
	Folder string `json:"folder,omitempty"`
	Active bool   `json:"active"`
	Paused bool   `json:"paused,omitempty"`
	Added  int    `json:"added"`
	Error  string `json:"error,omitempty"`
}

// BatchSettingsJSON are the Batch page's settings.
type BatchSettingsJSON struct {
	Targets        []string `json:"target_languages"`
	SourceLanguage string   `json:"source_language,omitempty"`
	Voice          string   `json:"voice,omitempty"`
	Model          string   `json:"model,omitempty"`
	KeepBackground bool     `json:"keep_background"`
	Translator     string   `json:"translator,omitempty"`
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
