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

// --- Stories and Audiobook ---------------------------------------------------------------

// StoryLineIn is a line of a story given as data.
type StoryLineIn struct {
	Text       string  `json:"text" jsonschema:"What is said (script markup works: [pause 500ms], tone tags); for a chapter line, its title."`
	Speaker    string  `json:"speaker,omitempty" jsonschema:"The character who says it (a name; the cast gives it a voice); empty: the narrator."`
	Voice      string  `json:"voice,omitempty" jsonschema:"A voice for this line only (a saved voice's name or id, or a built-in speaker)."`
	Speed      float64 `json:"speed,omitempty" jsonschema:"0.5–2; 0: the story's."`
	PauseAfter int     `json:"pause_after_ms,omitempty" jsonschema:"Silence after the line, in milliseconds (0: the usual gap)."`
	Chapter    bool    `json:"chapter,omitempty" jsonschema:"true: the line starts a chapter titled text."`
}

// LongformCreate makes a Stories or Audiobook project (REST body of
// POST /api/audiobooks and /api/stories).
type LongformCreate struct {
	Kind     string            `json:"-"` // "story" or "audiobook"
	Title    string            `json:"title,omitempty"`
	Path     string            `json:"path,omitempty"`
	Text     string            `json:"text,omitempty"`
	Lines    []StoryLineIn     `json:"lines,omitempty"`
	Split    string            `json:"split,omitempty"`
	Model    string            `json:"model,omitempty"`
	Voice    string            `json:"voice,omitempty"`
	Cast     map[string]string `json:"cast,omitempty"`
	Language string            `json:"language,omitempty"`
	Format   string            `json:"format,omitempty"`
}

// AudiobookCreate is create_audiobook's input.
type AudiobookCreate struct {
	Title    string            `json:"title,omitempty" jsonschema:"The book's title; empty: the book's own or the file's."`
	Path     string            `json:"path,omitempty" jsonschema:"Absolute path of the book: EPUB, TXT, Markdown or DOCX (headings become chapters)."`
	Text     string            `json:"text,omitempty" jsonschema:"Or the manuscript itself: '# Title' lines start chapters, [voice:NAME] switches to a character's voice."`
	Model    string            `json:"model,omitempty" jsonschema:"A speech model id (list_models); empty: AudioSmith's default."`
	Voice    string            `json:"voice,omitempty" jsonschema:"The narrator: a saved voice's name or id (list_voices) or a built-in speaker of the model; empty: the first voice that fits."`
	Cast     map[string]string `json:"cast,omitempty" jsonschema:"[voice:NAME] name → voice (name or id, or a built-in speaker)."`
	Language string            `json:"language,omitempty" jsonschema:"BCP-47 code of the text; empty: the book's, else automatic."`
	Format   string            `json:"format,omitempty" jsonschema:"wav (default) or m4b: also make an M4B audiobook with chapters and cover."`
}

// StoryCreate is create_story's input.
type StoryCreate struct {
	Title    string            `json:"title,omitempty" jsonschema:"The story's title; empty: the file's."`
	Path     string            `json:"path,omitempty" jsonschema:"Absolute path of a file to import: TXT, Markdown, EPUB, DOCX, SRT or VTT."`
	Text     string            `json:"text,omitempty" jsonschema:"Or the script itself: lines like 'Name: what they say' go to that character; '# Title' lines start chapters."`
	Lines    []StoryLineIn     `json:"lines,omitempty" jsonschema:"Or the lines as data, in order."`
	Split    string            `json:"split,omitempty" jsonschema:"How text becomes lines: speakers (default: as Auto-cast does), paragraphs, sentences or chapters."`
	Model    string            `json:"model,omitempty" jsonschema:"A speech model id (list_models); empty: AudioSmith's default."`
	Voice    string            `json:"voice,omitempty" jsonschema:"The narrator (lines without a character): a saved voice's name or id, or a built-in speaker; empty: the first voice that fits."`
	Cast     map[string]string `json:"cast,omitempty" jsonschema:"Character name → voice (name or id, or a built-in speaker); characters left out get voices in turn."`
	Language string            `json:"language,omitempty" jsonschema:"BCP-47 code of the text; empty: automatic."`
	Format   string            `json:"format,omitempty" jsonschema:"wav (default) or m4b."`
}

// Longform is the request of an audiobook's input.
func (in AudiobookCreate) Longform() LongformCreate {
	return LongformCreate{Kind: "audiobook", Title: in.Title, Path: in.Path, Text: in.Text, Model: in.Model, Voice: in.Voice,
		Cast: in.Cast, Language: in.Language, Format: in.Format}
}

// Longform is the request of a story's input.
func (in StoryCreate) Longform() LongformCreate {
	return LongformCreate{Kind: "story", Title: in.Title, Path: in.Path, Text: in.Text, Lines: in.Lines, Split: in.Split,
		Model: in.Model, Voice: in.Voice, Cast: in.Cast, Language: in.Language, Format: in.Format}
}

// ChapterJSON is a chapter of a project.
type ChapterJSON struct {
	Index int    `json:"index"` // 1-based
	Title string `json:"title,omitempty"`
	Lines int    `json:"lines"`
	Chars int    `json:"chars"`
}

// CastJSON is a character and the voice that plays it.
type CastJSON struct {
	Name      string `json:"name"`
	Voice     string `json:"voice,omitempty"` // a reference: a voice id, "speaker:<id>"
	VoiceName string `json:"voice_name,omitempty"`
	Lines     int    `json:"lines,omitempty"`
	Persona   string `json:"persona,omitempty"` // stories: the character's performance baseline
}

// StoryLineJSON is a line of a story.
type StoryLineJSON struct {
	ID         string  `json:"id"`
	Text       string  `json:"text"`
	Speaker    string  `json:"speaker,omitempty"`
	Voice      string  `json:"voice,omitempty"`
	Speed      float64 `json:"speed,omitempty"`
	PauseAfter int     `json:"pause_after_ms,omitempty"`
	Chapter    bool    `json:"chapter,omitempty"`
	Direction  string  `json:"direction,omitempty"` // how the line is said (never spoken)
	Intensity  int     `json:"intensity,omitempty"` // of the direction: 1 restrained, 2 clear, 3 strong; with an emotion 0–3
	Emotion    string  `json:"emotion,omitempty"`   // the AI director's emotion of the line (steady, happy, hurt…)
	Turn       bool    `json:"turn,omitempty"`      // a turn of the scene: its feeling may change at once
}

// ProblemJSON is something that stops (Blocking) or affects a render.
type ProblemJSON struct {
	Code     string `json:"code"`
	Message  string `json:"message"`
	Blocking bool   `json:"blocking,omitempty"`
	Chapter  int    `json:"chapter,omitempty"` // 1-based; 0 = none
	Line     string `json:"line,omitempty"`    // a story line's id
}

// BookJSON are a book's details.
type BookJSON struct {
	Author      string `json:"author,omitempty"`
	Narrator    string `json:"narrator,omitempty"`
	Year        string `json:"year,omitempty"`
	Genre       string `json:"genre,omitempty"`
	Description string `json:"description,omitempty"`
	Cover       string `json:"cover,omitempty"`
	Loudness    string `json:"loudness,omitempty"` // acx, podcast or ""
}

// RenderJSON is a render of a project.
type RenderJSON struct {
	ID      string             `json:"render_id"`
	JobID   string             `json:"job_id"` // "longform:<render_id>"
	State   string             `json:"state"`  // running, done, failed, stopped
	Partial bool               `json:"partial,omitempty"`
	Seconds float64            `json:"seconds,omitempty"`
	Book    string             `json:"book,omitempty"` // the book as WAV
	M4B     string             `json:"m4b,omitempty"`
	Cues    string             `json:"cues,omitempty"`
	Error   string             `json:"error,omitempty"`
	Encode  string             `json:"encode_error,omitempty"`
	Chapter []RenderChapterOut `json:"chapters,omitempty"`
	Stems   map[string]string  `json:"stems,omitempty"` // character ("" = narrator) → WAV
	Created time.Time          `json:"created_at"`
}

// RenderChapterOut is a chapter of a render.
type RenderChapterOut struct {
	Title   string  `json:"title,omitempty"`
	State   string  `json:"state"`
	File    string  `json:"file,omitempty"`
	Seconds float64 `json:"seconds,omitempty"`
	Error   string  `json:"error,omitempty"`
}

// LongformJSON is a Stories or Audiobook project.
type LongformJSON struct {
	ID        string          `json:"project_id"`
	Kind      string          `json:"kind"` // story, audiobook
	Title     string          `json:"title"`
	Model     string          `json:"model"` // catalogue id
	Voice     string          `json:"voice,omitempty"`
	VoiceName string          `json:"voice_name,omitempty"`
	Language  string          `json:"language,omitempty"`
	Format    string          `json:"format"`
	Stems     bool            `json:"stems,omitempty"`
	Speed     float64         `json:"speed,omitempty"`
	Chapters  []ChapterJSON   `json:"chapters"`
	Cast      []CastJSON      `json:"cast,omitempty"`
	Lines     []StoryLineJSON `json:"lines,omitempty"`
	// Stories: the scene, the pacing (dialogue, reading, relaxed or
	// fixed) and what the model does with scene, personas and directions
	// (continuity: instruct, intensity or lines).
	Scene      string        `json:"scene,omitempty"`
	Pacing     string        `json:"pacing,omitempty"`
	Continuity string        `json:"continuity,omitempty"`
	Words      int           `json:"words"`
	Estimate   float64       `json:"estimated_seconds"`
	Problems   []ProblemJSON `json:"problems,omitempty"`
	Book       BookJSON      `json:"book"`
	Render     *RenderJSON   `json:"last_render,omitempty"`
	Updated    time.Time     `json:"updated_at"`
	// Warning: the request named a removed model; its replacement is used.
	Warning string `json:"warning,omitempty"`
}

// LongformUpdate changes a project; empty fields stay as they are.
type LongformUpdate struct {
	ID       string            `json:"project_id" jsonschema:"The project (create_audiobook, create_story or list_projects)."`
	Title    string            `json:"title,omitempty" jsonschema:"A new title."`
	Model    string            `json:"model,omitempty" jsonschema:"Another speech model (voices that do not fit it are reported as problems)."`
	Voice    string            `json:"voice,omitempty" jsonschema:"The narrator (default voice): a saved voice's name or id, or a built-in speaker."`
	Cast     map[string]string `json:"cast,omitempty" jsonschema:"Character name → voice; the voice default gives the character the narrator's voice."`
	Lines    map[string]string `json:"line_voices,omitempty" jsonschema:"Stories: line id → voice for that line only (default: back to its character's)."`
	Language string            `json:"language,omitempty" jsonschema:"BCP-47 code; auto: automatic."`
	Speed    float64           `json:"speed,omitempty" jsonschema:"Speaking rate 0.5–2."`
	Format   string            `json:"format,omitempty" jsonschema:"wav or m4b (M4B needs the engine's encoder)."`
	Loudness string            `json:"loudness,omitempty" jsonschema:"The book's loudness target: acx (Audible, −19 LUFS), podcast (−16 LUFS) or off."`
	Author   string            `json:"author,omitempty" jsonschema:"Book details for the M4B's tags."`
	Narrator string            `json:"narrator,omitempty" jsonschema:"The narrator's name, for the M4B's tags."`
	Year     string            `json:"year,omitempty" jsonschema:"The book's year, for its tags."`
	Genre    string            `json:"genre,omitempty" jsonschema:"The book's genre, for its tags."`
	About    string            `json:"description,omitempty" jsonschema:"The book's description, for its tags."`
	Cover    string            `json:"cover,omitempty" jsonschema:"Absolute path of a PNG or JPEG cover (≤ 8 MB)."`
	Stems    *bool             `json:"stems,omitempty" jsonschema:"true: renders also write one WAV per character (stems)."`
	// Stories' performance.
	Scene       *string           `json:"scene,omitempty" jsonschema:"Stories: where and when, who the characters are to each other, the mood (\"\" clears). Models that take instructions hear it with every line; never spoken."`
	Personas    map[string]string `json:"personas,omitempty" jsonschema:"Stories: character name → persona, its performance baseline (\"seven, lively, coaxing\"; \"\" clears)."`
	Directions  map[string]string `json:"directions,omitempty" jsonschema:"Stories: line id → direction, how that line is said (\"pleading\", \"softly, holding back tears\"; \"\" clears). Never spoken."`
	Intensities map[string]int    `json:"intensities,omitempty" jsonschema:"Stories: line id → the direction's intensity: 1 restrained, 2 clear, 3 strong (0 clears)."`
	Pacing      string            `json:"pacing,omitempty" jsonschema:"Stories: gaps between lines: dialogue (tight turns), reading, relaxed, or fixed (one gap for every line)."`
	Merge       *bool             `json:"merge_turns,omitempty" jsonschema:"Stories, with a pacing: say consecutive lines of one character in one go (default true)."`
	Level       *bool             `json:"even_out_voices,omitempty" jsonschema:"Stories, with a pacing: bring each character's lines toward one loudness (default true)."`
	Direct      string            `json:"direct,omitempty" jsonschema:"Stories: run the AI director (the writing-help language model reads the story and writes the scene, personas and directions) after the other changes: fill (only what is empty) or all (replace). Takes up to a minute or two."`
}

// LongformRender starts a render.
type LongformRender struct {
	ID       string `json:"project_id" jsonschema:"The project to render."`
	Chapters []int  `json:"chapters,omitempty" jsonschema:"Chapter numbers (1-based) to render; empty: the whole book or story."`
}

// LongformExport saves a finished render.
type LongformExport struct {
	ID       string `json:"project_id" jsonschema:"The project; its last render is exported."`
	Render   string `json:"render_id,omitempty" jsonschema:"Or a render of it (render_longform's job, without longform:)."`
	Format   string `json:"format,omitempty" jsonschema:"m4b (rendered with format m4b), wav (default), mp3 or opus (need ffmpeg), chapters (one WAV per chapter), cues (chapter times, .txt) or stems (one WAV per character)."`
	Bits     int    `json:"bits,omitempty" jsonschema:"WAV bit depth: 16, 24 or 32 (float); 0: as rendered (32-bit float)."`
	Dir      string `json:"directory,omitempty" jsonschema:"Absolute folder to write to; empty: AudioSmith's agents' folder."`
	Filename string `json:"filename,omitempty" jsonschema:"The file's name (no folders; for chapters and stems, the name of a folder made inside directory); empty: from the title."`
}

// ExportJSON is what an export wrote.
type ExportJSON struct {
	Files    []string `json:"files"`
	Format   string   `json:"format"`
	Bytes    int64    `json:"bytes,omitempty"`
	Seconds  float64  `json:"seconds,omitempty"`
	Loudness float64  `json:"loudness_lufs,omitempty"`
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
