package services

// Longform documents: the body of a Stories or Audiobook project (also the
// pages' drafts), and the compiler that turns one into a render manifest
// (LongformSpec) once the UI has resolved its voices for the model.
//
// A Stories document is a list of lines, each with a character (whose voice
// the cast sets), an optional voice of its own, a speed and a pause after
// it; "chapter" lines start chapters. An Audiobook document is one
// manuscript in the script markup: "# Title" lines start chapters and
// [voice:NAME] switches voice until the next switch or chapter; NAME is
// looked up in the book's cast, then among the voices.
//
// Voices are referred to by a voice reference (VoiceRef): a saved voice's
// id, "speaker:<id>" for one of the model's own speakers, or (in [voice:]
// tags) a name. The compiler asks a VoiceResolver for what a reference
// means for the model: the voice fields of a recipe.
//
// Files: longform_doc.go (the document), longform_spec.go (the
// manifest), longform_compile.go (the compiler).

import (
	"crypto/rand"
	"encoding/binary"
	"strings"

	"github.com/hehehai/AudioSmith/internal/core"
	"github.com/hehehai/AudioSmith/internal/text"
	"github.com/hehehai/AudioSmith/internal/text/markup"
)

// LongformVersion is the version of LongformDoc. Version 2 added the
// optional dialogue fields (Scene, CastMember.Persona, StoryLine.Direction
// and Intensity, Production.Pacing and NoMerge): a version 1 document
// reads as a version 2 one with them empty, which renders exactly as
// version 1 did, so no migration rewrites anything.
const LongformVersion = 2

// Loudness targets of a book (dsp/loudness presets).
const (
	LoudnessOff     = ""
	LoudnessACX     = "acx"     // −19 LUFS, −3 dBTP (Audible)
	LoudnessPodcast = "podcast" // −16 LUFS, −1.5 dBTP
)

// Output formats of a book.
const (
	FormatWAV = "wav"
	FormatM4B = "m4b" // needs the engine's media.encode (EncodeSupported)
)

// SpeakerRef prefixes a VoiceRef naming one of the model's own speakers.
const SpeakerRef = "speaker:"

// LongformDoc is a Stories or Audiobook project (core.Project.Body) and the
// page's draft.
type LongformDoc struct {
	Version  int              `json:"version"`
	Kind     core.ProjectKind `json:"kind"` // ProjectStory or ProjectAudiobook
	Title    string           `json:"title"`
	Model    string           `json:"model"`
	Voice    string           `json:"voice,omitempty"`    // the default (narrator) voice: a VoiceRef
	Language string           `json:"language,omitempty"` // BCP-47; "" = auto
	Speed    float64          `json:"speed,omitempty"`    // 0.5–2; 0 = 1
	Format   string           `json:"format,omitempty"`   // FormatWAV ("" too) or FormatM4B
	// Seed is the project's own seed: chapters are rendered with it unless
	// Production.Seed pins another, so a render can be made again and its
	// chunks come from the cache.
	Seed int64 `json:"seed,omitempty"`

	// Stories.
	// Scene is where and how the story's dialogue happens ("evening, the
	// living room; the child wants a kitten, the mother hesitates"): a
	// summary models that take an instruction hear with every line.
	// Never spoken.
	Scene      string       `json:"scene,omitempty"`
	Lines      []StoryLine  `json:"lines,omitempty"`
	Cast       []CastMember `json:"cast,omitempty"`
	ImportText string       `json:"importText,omitempty"` // the review buffer
	SplitBy    string       `json:"splitBy,omitempty"`    // "sentences", "paragraphs" (default), "chapters"
	MaxChars   int          `json:"maxChars,omitempty"`   // per line when splitting by sentences; 0 = 500

	// Audiobook.
	Script    string            `json:"script,omitempty"`
	VoiceCast map[string]string `json:"voiceCast,omitempty"` // [voice:NAME] → VoiceRef

	Lexicon    []LexiconEntry `json:"lexicon,omitempty"` // the book's pronunciations, over the dictionary
	Production Production     `json:"production"`
	Book       BookInfo       `json:"book"`
	Stems      bool           `json:"stems,omitempty"` // write a stem per character
	// Render is the id of the last full render.
	Render string `json:"render,omitempty"`
}

// StoryLine is one line of a story.
type StoryLine struct {
	ID        string  `json:"id"`
	Text      string  `json:"text"`                // script markup; a chapter line's title
	Character string  `json:"character,omitempty"` // a CastMember's ID; "" = the default voice
	Voice     string  `json:"voice,omitempty"`     // a VoiceRef that overrides the character's
	Speed     float64 `json:"speed,omitempty"`     // 0 = the story's
	// PauseAfter is milliseconds of silence after the line, in place of the
	// gap between lines; 0 = the gap.
	PauseAfter int  `json:"pauseAfter,omitempty"`
	Chapter    bool `json:"chapter,omitempty"` // a chapter heading: Text is its title
	// Direction is how the line is delivered ("pleading", "softly",
	// "无奈地笑"): an instruction for models that take one. Never spoken.
	Direction string `json:"direction,omitempty"`
	// Intensity of the direction: 0 = unset, IntensityLight, Medium or
	// Strong. With an Emotion, 0–3 (0: barely).
	Intensity int `json:"intensity,omitempty"`
	// Emotion is the line's feeling from the vocabulary (Emotions,
	// story_emotion.go), as the second AI director writes it: the line's
	// instruction is made from it and Intensity (Direction, when set, is
	// the user's own wording of it). "" = directed as before (Direction).
	Emotion string `json:"emotion,omitempty"`
	// Turn marks a turn of the scene (转折): the feeling may change at
	// once here; elsewhere it moves a step at a time.
	Turn bool `json:"turn,omitempty"`
	// Note is the director's continuity note ("接着上一句的委屈"): shown,
	// never sent.
	Note string `json:"note,omitempty"`
	// EmotionByUser: the user set the emotion, intensity, turn or
	// direction; the director keeps them when it directs again.
	EmotionByUser bool `json:"emotionByUser,omitempty"`
}

// Direction intensities (StoryLine.Intensity).
const (
	IntensityLight  = 1
	IntensityMedium = 2
	IntensityStrong = 3
)

// CastMember is a character of a story and its voice.
type CastMember struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Voice string `json:"voice,omitempty"` // a VoiceRef; "" = the default voice
	// Persona is the character's performance baseline ("a seven-year-old
	// girl, lively, coaxing"), for models that take an instruction.
	Persona string `json:"persona,omitempty"`
	// Baseline is the character's usual feeling in the story (an
	// Emotion): its steady lines are said in it, faintly.
	Baseline string `json:"baseline,omitempty"`
}

// LexiconEntry is one of a book's pronunciations: Word is said as Say.
type LexiconEntry struct {
	Word string `json:"word"`
	Say  string `json:"say"`
}

// Production are a render's synthesis and joining settings.
type Production struct {
	Steps    int     `json:"steps,omitempty"`    // diffusion steps; 0 = the model's
	Guidance float64 `json:"guidance,omitempty"` // CFG; 0 = the model's
	Seed     int64   `json:"seed,omitempty"`     // 0 = the project's seed
	// VaryRepeats gives identical lines takes of their own instead of
	// reusing one recording.
	VaryRepeats    bool `json:"varyRepeats,omitempty"`
	LineGapMs      int  `json:"lineGapMs"`      // silence between lines (0–2000)
	ParagraphGapMs int  `json:"paragraphGapMs"` // between paragraphs (0–3000)
	// KeepEdges keeps the silence engines pad onto each chunk; the default
	// trims it before joining.
	KeepEdges bool `json:"keepEdges,omitempty"`
	// Pacing picks turn-aware gaps between a story's lines (PacingDialogue,
	// PacingReading, PacingRelaxed: story_pacing.go) in place of LineGapMs,
	// and lets consecutive lines of one character be synthesized as one;
	// "" (PacingFixed) keeps one gap for every line, as stories made
	// before it was added do.
	Pacing string `json:"pacing,omitempty"`
	// NoMerge keeps every line its own synthesis under a pacing preset;
	// NoLevel keeps each line's loudness as the model made it (a pacing
	// preset otherwise brings a character's lines toward one level).
	NoMerge bool `json:"noMerge,omitempty"`
	NoLevel bool `json:"noLevel,omitempty"`
	// NoEmotionGaps keeps the pacing's gaps for lines with an emotion,
	// which otherwise follow it (story_emotion.go).
	NoEmotionGaps bool `json:"noEmotionGaps,omitempty"`
	// NoTracks makes a directed story line by line: by default, within a
	// chapter, the lines of a character that have emotions are said in
	// one synthesis per stretch of one feeling, cut apart with the forced
	// aligner and put back in the dialogue (story_track.go).
	NoTracks bool `json:"noTracks,omitempty"`
}

// DefaultProduction are the production settings of a new project.
func DefaultProduction() Production { return Production{LineGapMs: 400, ParagraphGapMs: 800} }

// DefaultStoryProduction are the production settings of a new story: the
// dialogue pacing.
func DefaultStoryProduction() Production {
	p := DefaultProduction()
	p.Pacing = PacingDialogue
	return p
}

// BookInfo are a book's details: the metadata of encoded books, the cover
// and the loudness target.
type BookInfo struct {
	Author      string `json:"author,omitempty"`
	Narrator    string `json:"narrator,omitempty"`
	Year        string `json:"year,omitempty"`
	Genre       string `json:"genre,omitempty"`
	Description string `json:"description,omitempty"`
	// Cover is a PNG or JPEG of at most 8 MB (absolute path).
	Cover    string `json:"cover,omitempty"`
	Loudness string `json:"loudness,omitempty"` // LoudnessOff, LoudnessACX or LoudnessPodcast
}

// MaxCoverBytes caps a cover image.
const MaxCoverBytes = 8 << 20

// NewLongformDoc is a new, empty project of kind for model.
func NewLongformDoc(kind core.ProjectKind, model string) LongformDoc {
	var b [8]byte
	rand.Read(b[:])
	prod := DefaultProduction()
	if kind == core.ProjectStory {
		prod = DefaultStoryProduction()
	}
	return LongformDoc{Version: LongformVersion, Kind: kind, Model: model, Speed: 1, Format: FormatWAV,
		Seed: int64(binary.LittleEndian.Uint64(b[:])>>34) + 1, Production: prod}
}

// DuplicateWords lists the words a book's lexicon has more than once
// (case-insensitive), each once.
func DuplicateWords(l []LexiconEntry) []string {
	seen := map[string]int{}
	var out []string
	for _, e := range l {
		w := strings.ToLower(strings.TrimSpace(e.Word))
		if w == "" {
			continue
		}
		seen[w]++
		if seen[w] == 2 {
			out = append(out, strings.TrimSpace(e.Word))
		}
	}
	return out
}

// VoiceNames are the [voice:NAME] names of a manuscript, in order of first
// appearance ("default" left out).
func VoiceNames(script string) []string {
	var out []string
	for _, n := range markup.Parse(script, nil).VoiceNames() {
		if !strings.EqualFold(n, "default") {
			out = append(out, n)
		}
	}
	return out
}

// ScriptStats are the stats line of a script: chapters, words (and
// unspaced characters) and the estimated runtime at speed.
type ScriptStats struct {
	Chapters int
	Words    int
	Chars    int
	Seconds  float64
}

// StatsOf counts a manuscript ("# " chapters) or a story's lines.
func StatsOf(script string, speed float64) ScriptStats {
	st := markup.Parse(script, nil).Stats()
	if speed <= 0 {
		speed = 1
	}
	return ScriptStats{Chapters: st.Chapters, Words: st.Words, Chars: st.Chars,
		Seconds: text.Estimate(st.Words, st.Chars).Seconds()/speed + float64(st.PauseMillis)/1000}
}

// StoryScript is a story's lines as one manuscript (chapter lines as
// headings), for stats and search.
func StoryScript(lines []StoryLine) string {
	var b strings.Builder
	for _, l := range lines {
		if b.Len() > 0 {
			b.WriteString("\n\n")
		}
		if l.Chapter {
			b.WriteString("# ")
			b.WriteString(strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(l.Text), "#")))
			continue
		}
		b.WriteString(l.Text)
	}
	return b.String()
}
