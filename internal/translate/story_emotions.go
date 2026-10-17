package translate

// The emotion director (the AI director's second version,
// plan/story-emotion-context-plan.md §3 A): a language model reads a
// whole scene and decides its emotional curve: each character's baseline
// feeling, and per line an emotion from a small fixed vocabulary, its
// intensity 0–3, whether the line is a turn (转折) of the scene and a
// short continuity note. The caller smooths the curve with rules
// (services.SmoothEmotions) and makes each line's short instruction from
// it, so the voice model hears one consistent wording instead of guessing
// every line's feeling from its words.

import (
	"context"
	"strings"
	"time"

	"github.com/hehehai/AudioSmith/internal/llm"
)

// EmotionWord is one emotion of the vocabulary: its key (what the model
// answers) and its word in the script's language.
type EmotionWord struct {
	Key  string
	Word string
}

// StoryEmotions is the emotion director's proposal.
type StoryEmotions struct {
	Scene     string          `json:"scene"`
	Baselines []StoryBaseline `json:"cast"`
	// Lines are by position (N, 1-based) in StoryScript.Lines.
	Lines []StoryLineEmotion `json:"lines"`
}

// StoryBaseline is a character's usual feeling in the scene (a key).
type StoryBaseline struct {
	Name     string `json:"name"`
	Baseline string `json:"baseline"`
}

// StoryLineEmotion is one line's emotion (a key, as the model wrote it:
// the caller normalises it), intensity 0–3, turn and note.
type StoryLineEmotion struct {
	N         int    `json:"n"`
	Emotion   string `json:"emotion"`
	Intensity int    `json:"intensity"`
	Turn      bool   `json:"turn"`
	Note      string `json:"note"`
}

// Limits of a continuity note: runes of Chinese, characters of Latin text.
const (
	storyNoteMax      = 16
	storyNoteMaxLatin = 60
)

var emotionSchema = func() *llm.Schema {
	s := llm.Object(map[string]*llm.Schema{
		"scene": llm.String(),
		"cast": llm.Array(llm.Object(map[string]*llm.Schema{
			"name": llm.String(), "baseline": llm.String(),
		}, "name", "baseline")),
		"lines": llm.Array(llm.Object(map[string]*llm.Schema{
			"n":         llm.Integer(),
			"emotion":   llm.String(),
			"intensity": llm.Integer(), // 0–3, clamped: a 4 is no reason to ask again
			"turn":      llm.Boolean(),
			"note":      llm.String(),
		}, "n", "emotion", "intensity", "turn", "note")),
	}, "scene", "cast", "lines")
	s.Name = "story_emotions"
	return s
}()

// EmotionSchema is the JSON schema of the emotion director's answer.
func EmotionSchema() *llm.Schema { return emotionSchema }

type emotionData struct {
	storyData
	Vocab []EmotionWord
}

// DirectEmotions proposes the scene's emotional curve with the emotions
// of vocab. Lines with Fixed set are the user's: the model sees them and
// keeps them. Errors as Direct.
func (d *StoryDirector) DirectEmotions(ctx context.Context, s StoryScript, vocab []EmotionWord) (StoryEmotions, error) {
	if len(s.Lines) == 0 {
		return StoryEmotions{}, newErr(CodeNoSegments)
	}
	if !d.Available() {
		return StoryEmotions{}, newErr(CodeNoLLM, "skill", string(llm.SkillDirectionParse))
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
	out := StoryEmotions{Scene: strings.TrimSpace(s.Scene)}
	known := map[string]bool{}
	for _, n := range cast {
		known[strings.ToLower(n)] = true
	}
	for first := 0; first < len(s.Lines); first += storyBatch {
		last := min(first+storyBatch, len(s.Lines))
		data := emotionData{storyData: storyData{Zh: cjkShare(all.String()) > 0.3, Title: strings.TrimSpace(s.Title), Scene: out.Scene,
			Cast: mergeCast(s.Cast, nil, cast), First: first + 1, Last: last, Batched: len(s.Lines) > storyBatch}, Vocab: vocab}
		for i := max(first-4, 0); i < first; i++ {
			data.Before = append(data.Before, promptLine(i, s.Lines[i]))
		}
		for i := first; i < last; i++ {
			data.Lines = append(data.Lines, promptLine(i, s.Lines[i]))
		}
		msgs, err := messages("storyemotion", data)
		if err != nil {
			return StoryEmotions{}, newErr(CodeConfig, "service", "llm", "field", "template")
		}
		var got StoryEmotions
		req := llm.Request{Messages: msgs, Schema: emotionSchema, Timeout: budget, Temperature: llm.F64(0.3),
			MaxTokens: 300 + 56*(last-first)}
		bctx, cancel := llm.WithBudget(ctx, budget)
		_, err = d.Router.JSON(bctx, llm.SkillDirectionParse, req, &got, 1)
		cancel()
		if err != nil {
			return StoryEmotions{}, err
		}
		if first == 0 {
			if sc := clipScript(got.Scene, storySceneMax, storySceneMaxLatin); sc != "" {
				out.Scene = sc
			}
			seen := map[string]bool{}
			for _, b := range got.Baselines {
				k := strings.ToLower(strings.TrimSpace(b.Name))
				if known[k] && !seen[k] {
					seen[k] = true
					out.Baselines = append(out.Baselines, StoryBaseline{Name: strings.TrimSpace(b.Name), Baseline: cleanDirection(b.Baseline)})
				}
			}
		}
		seen := map[int]bool{}
		for _, l := range got.Lines {
			if l.N < first+1 || l.N > last || seen[l.N] {
				continue
			}
			seen[l.N] = true
			if f := s.Lines[l.N-1]; f.Fixed {
				l.Emotion, l.Intensity = f.Emotion, f.Intensity // the user's
			}
			l.Emotion = cleanDirection(l.Emotion)
			l.Intensity = min(max(l.Intensity, 0), 3)
			l.Note = clipScript(cleanDirection(l.Note), storyNoteMax, storyNoteMaxLatin)
			out.Lines = append(out.Lines, l)
		}
	}
	return out, nil
}
