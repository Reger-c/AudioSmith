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
	Scene      string `json:"scene,omitempty"`
	Pacing     string `json:"pacing,omitempty"`
	Continuity string `json:"continuity,omitempty"`
	// Dialogue: the characters' lines are spoken by the dialogue model,
	// a scene at a time (whole dialogue).
	Dialogue      bool          `json:"dialogue,omitempty"`
	DialogueModel string        `json:"dialogue_model,omitempty"`
	Words         int           `json:"words"`
	Estimate      float64       `json:"estimated_seconds"`
	Problems      []ProblemJSON `json:"problems,omitempty"`
	Book          BookJSON      `json:"book"`
	Render        *RenderJSON   `json:"last_render,omitempty"`
	Updated       time.Time     `json:"updated_at"`
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
	Scene         *string           `json:"scene,omitempty" jsonschema:"Stories: where and when, who the characters are to each other, the mood (\"\" clears). Models that take instructions hear it with every line; never spoken."`
	Personas      map[string]string `json:"personas,omitempty" jsonschema:"Stories: character name → persona, its performance baseline (\"seven, lively, coaxing\"; \"\" clears)."`
	Directions    map[string]string `json:"directions,omitempty" jsonschema:"Stories: line id → direction, how that line is said (\"pleading\", \"softly, holding back tears\"; \"\" clears). Never spoken."`
	Emotions      map[string]string `json:"emotions,omitempty" jsonschema:"Stories: line id → its emotion, as the AI director writes it: steady, happy, excited, tender, pleading, hesitant, worried, hurt, sad, angry, scared, surprised or apologetic (\"\" clears). Models that take instructions hear a short instruction made from it and its intensity. Never spoken."`
	Intensities   map[string]int    `json:"intensities,omitempty" jsonschema:"Stories: line id → intensity: of an emotion 0 barely, 1 restrained, 2 clear, 3 strong; of a direction 1–3 (0 clears it)."`
	Pacing        string            `json:"pacing,omitempty" jsonschema:"Stories: gaps between lines: dialogue (tight turns), reading, relaxed, or fixed (one gap for every line)."`
	Merge         *bool             `json:"merge_turns,omitempty" jsonschema:"Stories, with a pacing: say consecutive lines of one character in one go (default true)."`
	Level         *bool             `json:"even_out_voices,omitempty" jsonschema:"Stories, with a pacing: bring each character's lines toward one loudness (default true)."`
	Dialogue      *bool             `json:"dialogue,omitempty" jsonschema:"Stories: true speaks the characters' lines with the dialogue model (dialogue_model; Chinese and English) a scene at a time, each line hearing the lines before it; narration stays on the story's model."`
	DialogueModel string            `json:"dialogue_model,omitempty" jsonschema:"Stories: the dialogue model: soulx (SoulX-Podcast, the default: a 2.4 GB download, 16 GB of memory, up to four voices) or moss (MOSS-TTSD: an 11 GB download, 24 GB of memory, up to five voices, a scene as one recording)."`
	Direct        string            `json:"direct,omitempty" jsonschema:"Stories: run the AI director (the writing-help language model reads the story and plans its emotions: a baseline per character, an emotion, intensity and turn per line) after the other changes: fill (only what is empty) or all (replace what you did not set). Takes up to a minute or two."`
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
