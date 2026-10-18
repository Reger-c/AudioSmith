package services

// A longform render's manifest (longform_doc.go): LongformSpec with its
// chapters, lines and spans as the compiler makes them, the VoiceResolver
// it asks, and the problems it reports; split from longform_doc.go.

import (
	"strconv"

	"github.com/hehehai/AudioSmith/internal/core"
	"github.com/hehehai/AudioSmith/internal/text"
)

// VoiceResolver says what a voice reference (or a name in a [voice:] tag)
// means for the document's model: a stable key for it and the voice
// fields of a recipe (Voice, RefAudio, RefText, VoicePrompt, Seed,
// Language, VoiceID…). ok is false for a reference it does not know.
type VoiceResolver func(ref string) (key string, voice core.Take, ok bool)

// LongformSpec is what a longform render renders: the voices resolved for
// the model, and the chapters as lines of voiced spans.
type LongformSpec struct {
	Project string           `json:"project,omitempty"` // "" for a draft
	Kind    core.ProjectKind `json:"kind"`
	Title   string           `json:"title"`
	Model   string           `json:"model"`
	Lang    string           `json:"lang,omitempty"` // BCP-47, for normalisation and the dictionary
	Seed    int64            `json:"seed,omitempty"`
	// Voices are the voice fields of the recipes by key (SpanSpec.Voice).
	Voices     map[string]core.Take `json:"voices"`
	Chapters   []ChapterSpec        `json:"chapters"`
	Production Production           `json:"production"`
	Book       BookInfo             `json:"book"`
	Format     string               `json:"format,omitempty"`
	Stems      bool                 `json:"stems,omitempty"`
	Lexicon    []LexiconEntry       `json:"lexicon,omitempty"`
	// Scene is the story's scene (LongformDoc.Scene), for instructions.
	Scene string `json:"scene,omitempty"`
	// Audition renders the chapters for listening only: no book, no
	// stems, no loudness target (a chapter or a line preview).
	Audition bool `json:"audition,omitempty"`
}

// ChapterSpec is one chapter: a title ("" = untitled) and its lines.
type ChapterSpec struct {
	Title string     `json:"title,omitempty"`
	Lines []LineSpec `json:"lines"`
}

// LineSpec is one line (a story line, a paragraph of a manuscript).
type LineSpec struct {
	ID         string     `json:"id,omitempty"`
	Character  string     `json:"character,omitempty"` // for stems; "" = the narrator
	Spans      []SpanSpec `json:"spans"`
	Speed      float64    `json:"speed,omitempty"` // 0 or 1 = normal
	PauseAfter int        `json:"pauseAfter,omitempty"`
	Paragraph  bool       `json:"paragraph,omitempty"` // the paragraph gap before it
	// Repeat counts the earlier lines with the same text and voice (for
	// VaryRepeats).
	Repeat int `json:"repeat,omitempty"`
	// The line's direction and its intensity, and its character's persona
	// (stories): the instruction of models that take one.
	Direction string `json:"direction,omitempty"`
	Intensity int    `json:"intensity,omitempty"`
	Persona   string `json:"persona,omitempty"`
	// Block is the dialogue block of a manuscript the line is in
	// (book_dialogue.go), from 1 in its chapter; 0: none.
	Block int `json:"block,omitempty"`
	// Emotion (story_emotion.go): the line's feeling, a steady line's
	// being its character's baseline; with it, the instruction is made
	// from Emotion and Intensity (0–3), Direction being the user's own
	// words. Turn: a turn of the scene.
	Emotion string `json:"emotion,omitempty"`
	Turn    bool   `json:"turn,omitempty"`
	// Baseline is the character's baseline emotion (tracks are said in it).
	Baseline string `json:"baseline,omitempty"`
}

// SpanSpec is a part of a line in one voice.
type SpanSpec struct {
	Voice     string `json:"voice"` // a key of LongformSpec.Voices
	Character string `json:"character,omitempty"`
	Text      string `json:"text"` // script markup without [voice:] tags
}

// Problem is something the compiler found: Blocking ones stop a render.
// The UI translates Code with Params.
type Problem struct {
	Code     string            `json:"code"`
	Blocking bool              `json:"blocking,omitempty"`
	Chapter  int               `json:"chapter"`       // 0-based; -1 = none
	Line     int               `json:"line"`          // the story line's index, -1 = none
	Span     text.Span         `json:"span,omitzero"` // in the manuscript (Audiobook)
	Params   map[string]string `json:"params,omitzero"`
}

// Problem codes (Params in brackets).
const (
	ProblemNoModel       = "longform.no_model"
	ProblemNothingToSay  = "longform.nothing_to_say"
	ProblemNoVoice       = "longform.no_voice"       // a line without a voice [line]
	ProblemMissingVoice  = "longform.missing_voice"  // a voice that is gone [voice]
	ProblemUnknownVoice  = "longform.unknown_voice"  // [voice:NAME] nobody is cast as: the line's voice reads it [voice]
	ProblemEmptyChapter  = "longform.empty_chapter"  // [title]
	ProblemUnknownTag    = "longform.unknown_tag"    // read aloud [tag]
	ProblemDuplicateWord = "longform.duplicate_word" // the lexicon lists a word twice [word]

	// A manuscript's dialogue blocks (book_dialogue.go); none blocks.
	ProblemDialogueUnclosed   = "longform.dialogue_unclosed"    // a block ends with its chapter [at: "heading" when a heading ended it]
	ProblemDialogueNested     = "longform.dialogue_nested"      // an opening marker inside a block, ignored
	ProblemDialogueStrayClose = "longform.dialogue_stray_close" // a closer outside a block, ignored
	ProblemDialogueEmpty      = "longform.dialogue_empty"       // a block with nothing to say, ignored
	ProblemDialogueUncast     = "longform.dialogue_uncast"      // names in a block without a voice [names]: voices are given at render
	ProblemDialogueOneSpeaker = "longform.dialogue_one_speaker" // one speaker: read line by line
	ProblemDialogueUnknown    = "longform.dialogue_unknown"     // "？" turns nobody was found for: the narrator reads them [count]
	ProblemDialogueLanguage   = "longform.dialogue_language"    // not Chinese or English: read line by line [lang]
	ProblemDialogueTags       = "longform.dialogue_tags"        // tags the dialogue model leaves out [tags]
)

// Untitled is the title of chapter n (1-based) without one, for cue sheets
// and metadata ("Chapter 3"); the UI translates its own.
func Untitled(n int) string { return "Chapter " + strconv.Itoa(n) }
