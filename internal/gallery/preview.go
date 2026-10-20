package gallery

import (
	"encoding/json"
	"fmt"
	"slices"

	"github.com/hehehai/AudioSmith/internal/engine/proto"
	"github.com/hehehai/AudioSmith/internal/jobs"
	"github.com/hehehai/AudioSmith/internal/text"
	vd "github.com/hehehai/AudioSmith/internal/voicedesign"
)

// Model is the design-capable model previews are rendered with.
type Model struct {
	ID        string   // catalogue id, part of the cache key
	Family    string   // engine family ("omnivoice", "qwen3_tts")
	Repo      string   // Hugging Face repo (picks the lowering)
	Languages []string // BCP-47 primary subtags it speaks; empty = any
	Speakers  []string // CustomVoice: speakers the build has; empty = all
	// Sampling is the model's recommended sampling (the catalogue's
	// presets), as the Studio's takes get it (thread #93: previews had
	// the engine's defaults, temperature 0.9 and top-p 1.0 for Qwen3-TTS).
	Sampling Sampling
}

// Sampling are generation parameters; 0 = the model's own.
type Sampling struct {
	Temperature, TopP, Guidance float64
	Steps                       int
}

func (s Sampling) key() string {
	return fmt.Sprintf("t%g p%g g%g s%d", s.Temperature, s.TopP, s.Guidance, s.Steps)
}

// PreviewKey is the cache key of a rendered preview: the recipe, the
// model, its sampling and the line spoken. Equal keys mean
// byte-identical requests. v2 (#93): the sampling joined the key, and
// design prompts lost their slow pace.
func PreviewKey(r vd.Recipe, modelID string, s Sampling, line, lang string) string {
	return vd.Key("gallery.preview.v2", r.Key(), modelID, s.key(), lang, text.NormalizeForKey(line))
}

// PreviewJob is one preview to render: everything a tts.synthesize call
// needs except where to write it.
type PreviewJob struct {
	ArchetypeID string       `json:"archetypeId"`
	Key         string       `json:"key"`
	Model       string       `json:"model"`
	Target      vd.Target    `json:"target"`
	Voice       string       `json:"voice"` // lowered recipe
	Text        string       `json:"text"`
	Lang        string       `json:"lang"`
	Seed        int64        `json:"seed"`
	Sampling    Sampling     `json:"sampling"`
	Dropped     []vd.Dropped `json:"dropped,omitempty"` // attributes this model can't express
}

// Params is the engine request. modelRef is what the engine loads (repo
// or snapshot path), language the model's language value, out the
// absolute output path in the preview cache.
func (p PreviewJob) Params(modelRef, language, out string) proto.SynthesizeParams {
	seed := p.Seed
	sp := proto.SynthesizeParams{
		Model: modelRef, Text: p.Text, Voice: p.Voice, Language: language,
		Seed: &seed, Output: proto.Output{Path: out, Format: "wav"}, Steps: p.Sampling.Steps,
	}
	ptr := func(v float64) *float64 {
		if v == 0 {
			return nil
		}
		return &v
	}
	sp.Temperature, sp.TopP, sp.Guidance = ptr(p.Sampling.Temperature), ptr(p.Sampling.TopP), ptr(p.Sampling.Guidance)
	return sp
}

// Spec wraps the request as a background job on the GPU lane: previews
// never delay what the user is waiting for, and re-run safely after an
// interruption (their output is keyed by input).
func (p PreviewJob) Spec(params proto.SynthesizeParams) (jobs.Spec, error) {
	raw, err := json.Marshal(params)
	if err != nil {
		return jobs.Spec{}, err
	}
	return jobs.Spec{
		Kind: proto.MethodTTSSynthesize, Lane: jobs.LaneGPU, Priority: jobs.PriorityBackground,
		Params: raw, Title: "Gallery preview", Owner: "archetype:" + p.ArchetypeID,
		MaxAttempts: 2, Idempotent: true,
	}, nil
}

// PreviewPlan lists the previews to render.
type PreviewPlan struct {
	Jobs        []PreviewJob
	Cached      int      // already in the cache
	Unsupported []string // archetype ids whose preview language the model doesn't speak
}

// PlanPreviews decides which previews to render for items (pass the
// visible page first: order is kept) with model m. has reports whether a
// key is already cached (nil = nothing cached). At most limit jobs are
// planned (≤ 0 = no limit). Items sharing a key are planned once.
func PlanPreviews(items []Archetype, m Model, has func(key string) bool, limit int) (PreviewPlan, error) {
	eng, ok := vd.EngineFor(m.Family, m.Repo)
	if !ok {
		return PreviewPlan{}, fmt.Errorf("gallery: %s can't design voices", m.ID)
	}
	var plan PreviewPlan
	seen := map[string]bool{}
	for _, a := range items {
		line, lang := a.PreviewLine()
		if len(m.Languages) > 0 && !slices.Contains(m.Languages, lang) {
			plan.Unsupported = append(plan.Unsupported, a.ID)
			continue
		}
		r := a.Recipe()
		key := PreviewKey(r, m.ID, m.Sampling, line, lang)
		if seen[key] {
			continue
		}
		seen[key] = true
		if has != nil && has(key) {
			plan.Cached++
			continue
		}
		if limit > 0 && len(plan.Jobs) >= limit {
			continue
		}
		low, err := vd.Lower(eng.Target, r, vd.Options{Lang: textLang(lang), Speakers: m.Speakers})
		if err != nil {
			return plan, err
		}
		plan.Jobs = append(plan.Jobs, PreviewJob{
			ArchetypeID: a.ID, Key: key, Model: m.ID, Target: eng.Target,
			Voice: low.Voice, Text: line, Lang: lang, Seed: low.Seed, Sampling: m.Sampling, Dropped: low.Dropped,
		})
	}
	return plan, nil
}

// textLang is the language to write the voice description in: Chinese for
// Chinese speech, English otherwise.
func textLang(speech string) string {
	if speech == "zh" {
		return "zh"
	}
	return "en"
}
