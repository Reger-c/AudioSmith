package services

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/hehehai/AudioSmith/internal/core"
)

// legacyBook is a manuscript of before dialogue blocks: chapters (one
// untitled), a cast of [voice:] names (one unknown), pauses, styles across
// a switch, reactions, overrides, a tag read aloud, an empty chapter.
func legacyBook() LongformDoc {
	d := NewLongformDoc(core.ProjectAudiobook, "m/kokoro")
	d.Seed, d.Voice, d.Language = 42, "speaker:af_heart", "en"
	d.VoiceCast = map[string]string{"Ines": "speaker:af_bella", "Tomas": "speaker:am_adam"}
	d.Lexicon = []LexiconEntry{{Word: "Harrow", Say: "Harro"}}
	d.Script = "Before the first heading. [pause 300ms] It begins.\n\n" +
		"# The Night Ferry\n\nThe last ferry left at eleven. [pause 500ms] Fog had settled.\n\n" +
		"[voice:Ines]Is this the boat to Harrow Point?\n\n" +
		"[voice:Tomas][slow]It is the only boat,[/slow] the ferryman said. [laughter] Sit anywhere.\n" +
		"[voice:Nobody]A stranger speaks. [voice:default]She chose the bench.\n\n" +
		"[[Harrow|Harro]] lights went out [whisper] one by one.\n\n" +
		"# Empty\n\n# Harrow Point\n\n[voice:Ines][fast]We're here already?[/fast] [sigh] I slept.\n\n" +
		"[voice:Tomas]The crossing is [emphasis]longer[/emphasis] than it looks: [spell]NO[/spell] swimming."
	return d
}

// legacyBookDigest is legacyBook's spec, problems and chapter keys as the
// compiler and renderer before dialogue blocks (2cad9cc) made them: old
// books render byte for byte as before, their chunks and chapters reused.
const legacyBookDigest = "691b0eac19c0c6be576ac132b7abdd52d9dafe078feeb578cb746a5c9e67348d"

func bookDigest(t *testing.T, d LongformDoc) (string, string) {
	t.Helper()
	spec, probs := Compile(&d, testVoices())
	var b strings.Builder
	js, _ := json.Marshal(spec)
	b.Write(js)
	for _, p := range probs {
		fmt.Fprintf(&b, "\nproblem %s %v %d %d %v %v", p.Code, p.Blocking, p.Chapter, p.Line, p.Span, p.Params)
	}
	for i, ch := range spec.Chapters {
		p := ChapterParams{Render: "r", Index: i, Title: ch.Title, Model: spec.Model, Lang: spec.Lang, Seed: spec.Seed,
			Voices: map[string]core.Take{}, Lines: ch.Lines, Production: spec.Production, Lexicon: spec.Lexicon,
			Loudness: spec.Book.Loudness, Output: "/tmp/ch.wav", Workflow: string(spec.Kind)}
		for _, l := range ch.Lines {
			for _, sp := range l.Spans {
				p.Voices[sp.Voice] = spec.Voices[sp.Voice]
			}
		}
		key, _ := chapterKeyOf(t, p, kokoroInfo)
		fmt.Fprintf(&b, "\nchapter %d %s", i, key)
	}
	h := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(h[:]), b.String()
}

func TestOldBooksRenderAsBefore(t *testing.T) {
	got, text := bookDigest(t, legacyBook())
	if got != legacyBookDigest {
		t.Errorf("an old book's digest is %s, want %s (as before dialogue blocks):\n%s", got, legacyBookDigest, text)
	}
}
