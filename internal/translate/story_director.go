package translate

// The AI director of stories (Stories → AI director, MCP direct_story):
// a language model reads a whole scene and proposes the scene summary, a
// performance baseline per character and a direction (with an intensity)
// per line. Stories send them to models that take an instruction, so each
// line is said knowing what happened before it: the model's context
// stays in the language model, and the voice model hears short
// instructions. It routes as the direction_parse skill ("Direct a line"
// in Tools), so it uses the writing-help model the user chose, by default
// the local Qwen3 4B.

import (
	"context"
	"strings"
	"time"
	"unicode"

	"github.com/hehehai/AudioSmith/internal/llm"
)

// StoryScript is what the director reads: the story with what is already
// set (kept unless the director has better).
type StoryScript struct {
	Title string
	Scene string
	Cast  []StoryRole
	Lines []StoryScriptLine
}

// StoryRole is a character and its persona; Voice describes the voice it
// is cast with ("bright young female voice"), so the persona fits it
// (read, not written, by the director).
type StoryRole struct {
	Name    string `json:"name"`
	Persona string `json:"persona"`
	Voice   string `json:"-"`
}

// StoryScriptLine is one spoken line: who ("" for the narrator), what,
// and its direction so far.
type StoryScriptLine struct {
	Speaker   string
	Text      string
	Direction string
}

// StoryDirection is the director's proposal.
type StoryDirection struct {
	Scene string      `json:"scene"`
	Cast  []StoryRole `json:"cast"`
	// Lines are by position (N, 1-based) in StoryScript.Lines; a line the
	// model skipped has none.
	Lines []StoryLineDirection `json:"lines"`
}

// StoryLineDirection is one line's direction: a few words of emotion and
// delivery, and its intensity (1 restrained, 2 clear, 3 strong).
type StoryLineDirection struct {
	N         int    `json:"n"`
	Direction string `json:"direction"`
	Intensity int    `json:"intensity"`
}

// StoryDirector directs stories through the direction_parse skill.
type StoryDirector struct {
	Router *llm.Router
	// Budget bounds one batch of lines (0: 3 minutes: a local model
	// loads first).
	Budget time.Duration
}

// storyBatch is how many lines one call directs; longer stories go in
// batches that carry the scene, the cast and the lines just before.
const storyBatch = 40

// Limits of what the director writes, per script: runes of Chinese (or
// Japanese) text, characters of Latin text. A word is about 1.5 Chinese
// characters but 6 Latin characters, so one limit for both cut English
// directions mid-word ("skeptical, testing the e", thread #44). The
// Latin limits stay within what StoryInstruct passes to the voice.
const (
	storySceneMax          = 60
	storyPersonaMax        = 40
	storyDirectionMax      = 24
	storySceneMaxLatin     = 140
	storyPersonaMaxLatin   = 90
	storyDirectionMaxLatin = 60
)

var storySchema = func() *llm.Schema {
	s := llm.Object(map[string]*llm.Schema{
		"scene": llm.String(),
		"cast": llm.Array(llm.Object(map[string]*llm.Schema{
			"name": llm.String(), "persona": llm.String(),
		}, "name", "persona")),
		"lines": llm.Array(llm.Object(map[string]*llm.Schema{
			"n":         llm.Integer(),
			"direction": llm.String(),
			"intensity": llm.Integer().Range(1, 3),
		}, "n", "direction", "intensity")),
	}, "scene", "cast", "lines")
	s.Name = "story_direction"
	return s
}()

// StorySchema is the JSON schema of the director's answer (tests, docs).
func StorySchema() *llm.Schema { return storySchema }

type storyData struct {
	Zh      bool
	Title   string
	Scene   string
	Cast    []StoryRole
	Before  []storyPromptLine // context: the lines just before the batch
	Lines   []storyPromptLine
	First   int
	Last    int
	Batched bool
}

type storyPromptLine struct {
	N         int
	Speaker   string
	Text      string
	Direction string
}

// Available reports whether a language model is set up for the director.
func (d *StoryDirector) Available() bool { return d.Router.Available(llm.SkillDirectionParse) }

// Direct proposes a scene, personas and directions for s. Errors are
// *Error (CodeNoLLM when no model is set up) or the llm layer's.
func (d *StoryDirector) Direct(ctx context.Context, s StoryScript) (StoryDirection, error) {
	if len(s.Lines) == 0 {
		return StoryDirection{}, newErr(CodeNoSegments)
	}
	if !d.Available() {
		return StoryDirection{}, newErr(CodeNoLLM, "skill", string(llm.SkillDirectionParse))
	}
	budget := d.Budget
	if budget <= 0 {
		budget = 3 * time.Minute
	}
	var all strings.Builder
	for _, l := range s.Lines {
		all.WriteString(l.Text)
	}
	cast := castNames(s)
	out := StoryDirection{Scene: strings.TrimSpace(s.Scene), Cast: mergeCast(s.Cast, nil, cast)}
	for first := 0; first < len(s.Lines); first += storyBatch {
		last := min(first+storyBatch, len(s.Lines))
		data := storyData{Zh: cjkShare(all.String()) > 0.3, Title: strings.TrimSpace(s.Title), Scene: out.Scene, Cast: out.Cast,
			First: first + 1, Last: last, Batched: len(s.Lines) > storyBatch}
		for i := max(first-4, 0); i < first; i++ {
			data.Before = append(data.Before, promptLine(i, s.Lines[i]))
		}
		for i := first; i < last; i++ {
			data.Lines = append(data.Lines, promptLine(i, s.Lines[i]))
		}
		msgs, err := messages("storydirect", data)
		if err != nil {
			return StoryDirection{}, newErr(CodeConfig, "service", "llm", "field", "template")
		}
		var got StoryDirection
		req := llm.Request{Messages: msgs, Schema: storySchema, Timeout: budget, Temperature: llm.F64(0.4),
			MaxTokens: 400 + 48*(last-first)}
		bctx, cancel := llm.WithBudget(ctx, budget) // from when the model's turn comes
		_, err = d.Router.JSON(bctx, llm.SkillDirectionParse, req, &got, 1)
		cancel()
		if err != nil {
			return StoryDirection{}, err
		}
		if first == 0 {
			if sc := clipScript(got.Scene, storySceneMax, storySceneMaxLatin); sc != "" {
				out.Scene = sc
			}
			out.Cast = mergeCast(s.Cast, got.Cast, cast)
		}
		seen := map[int]bool{}
		for _, l := range got.Lines {
			if l.N < first+1 || l.N > last || seen[l.N] {
				continue
			}
			seen[l.N] = true
			l.Direction = clipScript(cleanDirection(l.Direction), storyDirectionMax, storyDirectionMaxLatin)
			l.Intensity = min(max(l.Intensity, 1), 3)
			if l.Direction == "" {
				l.Intensity = 0
			}
			out.Lines = append(out.Lines, l)
		}
	}
	return out, nil
}

func promptLine(i int, l StoryScriptLine) storyPromptLine {
	who := strings.TrimSpace(l.Speaker)
	return storyPromptLine{N: i + 1, Speaker: who, Text: clipRunes(strings.Join(strings.Fields(l.Text), " "), 600),
		Direction: strings.TrimSpace(l.Direction)}
}

// castNames are the characters who speak, in order.
func castNames(s StoryScript) []string {
	var out []string
	seen := map[string]bool{}
	add := func(n string) {
		n = strings.TrimSpace(n)
		if n != "" && !seen[strings.ToLower(n)] {
			seen[strings.ToLower(n)] = true
			out = append(out, n)
		}
	}
	for _, c := range s.Cast {
		add(c.Name)
	}
	for _, l := range s.Lines {
		add(l.Speaker)
	}
	return out
}

// mergeCast keeps a persona per known character: the model's, else what
// was set; characters the model made up are dropped.
func mergeCast(had, got []StoryRole, names []string) []StoryRole {
	byName := map[string]string{}
	voice := map[string]string{}
	for _, r := range had {
		byName[strings.ToLower(strings.TrimSpace(r.Name))] = strings.TrimSpace(r.Persona)
		voice[strings.ToLower(strings.TrimSpace(r.Name))] = strings.TrimSpace(r.Voice)
	}
	for _, r := range got {
		if p := clipScript(r.Persona, storyPersonaMax, storyPersonaMaxLatin); p != "" {
			byName[strings.ToLower(strings.TrimSpace(r.Name))] = p
		}
	}
	var out []StoryRole
	for _, n := range names {
		out = append(out, StoryRole{Name: n, Persona: byName[strings.ToLower(n)], Voice: voice[strings.ToLower(n)]})
	}
	return out
}

// cleanDirection drops brackets and quotes a model may wrap a direction in.
func cleanDirection(s string) string {
	s = strings.TrimSpace(s)
	s = strings.Trim(s, "()（）[]【】\"'“”‘’")
	switch strings.ToLower(s) {
	case "none", "无", "-", "—", "n/a":
		return ""
	}
	return strings.TrimSpace(s)
}

// clipScript shortens s to cjk runes when it is mostly Chinese or
// Japanese, else to latin characters, keeping whole phrases: it cuts at
// the last clause break (comma, semicolon…) that fits, else (Latin) at
// the last space, and never leaves trailing punctuation.
func clipScript(s string, cjk, latin int) string {
	s = strings.TrimSpace(strings.Join(strings.Fields(s), " "))
	n := latin
	if cjkShare(s) >= 0.3 {
		n = cjk
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	cut := -1
	for i := n; i > n/3; i-- { // a clause break inside the limit
		if strings.ContainsRune(",;:，；：、。.!?！？", r[i]) {
			cut = i
			break
		}
	}
	if cut < 0 && n == latin {
		for i := n; i > n/3; i-- { // a word break
			if r[i] == ' ' {
				cut = i
				break
			}
		}
	}
	if cut < 0 {
		cut = n
	}
	return strings.TrimRightFunc(string(r[:cut]), func(c rune) bool {
		return unicode.IsSpace(c) || strings.ContainsRune(",;:，；：、-—–", c)
	})
}
