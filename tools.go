package main

// Tools (M5, /tools): small workbenches over the services —
//
//	A/B        the same phrase in two voices, side by side (previews,
//	           not kept in the History)
//	Convert    a recording re-spoken in a saved voice: transcribed, then
//	           generated (kept in the History); "Match the length" uses a
//	           model's target duration (OmniVoice)
//	Rate fit   will a line fit its slot when spoken? (translate.Fitter;
//	           a shorter version from the writing-help model)
//	Direction  a performance note → emotions, an instruction, rate, pitch
//	           and energy (translate.Director), with a spoken preview
//	Probe      what a media file holds (the engine's media.probe)
//
// Files: tools.go (state, jobs, voices), tools_convert.go (A/B,
// Convert), tools_direction.go (Rate fit, Direction, Probe), tools_view.go
// (the page).

import (
	"context"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/hehehai/AudioSmith/internal/api"
	"github.com/hehehai/AudioSmith/internal/catalog"
	"github.com/hehehai/AudioSmith/internal/core"
	"github.com/hehehai/AudioSmith/internal/i18n"
	"github.com/hehehai/AudioSmith/internal/jobs"
	"github.com/hehehai/AudioSmith/internal/services"
	"github.com/hehehai/AudioSmith/internal/translate"
)

// toolsState is the Tools page's state.
type toolsState struct {
	toolTab int // 0 A/B, 1 convert, 2 rate fit, 3 direction, 4 probe
	// toolWait are the jobs the page waits for, by id; toolEnded the
	// ones that ended before the page knew their id.
	toolWait  map[string]func(jobs.Job)
	toolEnded map[string]jobs.Job

	ab   toolAB
	cv   toolConvert
	rf   toolRateFit
	dir  toolDirection
	prb  toolProbe
	tSeq int // numbers previews for the player
}

// toolRun is a preview render.
type toolRun struct {
	key    string // the player's id
	jobID  string
	output string
	busy   bool
	err    string
	model  string // label of the model it used
}

type toolAB struct {
	text string
	a, b string // choice labels
	runs [2]*toolRun
}

type toolConvert struct {
	file, name string
	duration   float64
	voice      string // choice label
	match      bool
	stage      string // what runs now (translated); "" = idle
	heard      string
	take       *core.Take
	err, note  string
}

type toolRateFit struct {
	text    string
	lang    string // label
	slot    float64
	speed   float64
	rewrite bool
	busy    bool
	res     *translate.FitResult
	err     string
}

type toolDirection struct {
	note    string
	busy    bool
	warming bool // the language model was not loaded when the note was read
	waiting bool // the language model waits for the gpu lane (a render runs)
	run     int  // counts readings: a late report of an older one is dropped
	res     *translate.Direction
	preview *toolRun
}

type toolProbe struct {
	file string
	busy bool
	out  string
	err  string
}

// openTools follows the jobs the Tools page waits for (once, from newApp).
func (a *app) openTools(lib *library) {
	a.toolWait, a.toolEnded = map[string]func(jobs.Job){}, map[string]jobs.Job{}
	a.rf.slot, a.rf.speed = 3, 1
	a.ab.text = i18n.T("The quick brown fox jumps over the lazy dog.")
	core.On(lib.bus, func(u jobs.Updated) {
		if u.Chunk != nil || !(u.Job.State.Terminal() || u.Job.State == jobs.StateInterrupted) {
			return
		}
		if !strings.HasPrefix(u.Job.Owner, "preview:") && !strings.HasPrefix(u.Job.Owner, services.TranscriptOwner) && !strings.HasPrefix(u.Job.Owner, "take:") {
			return
		}
		j := u.Job
		a.post(func() { a.toolJobEnded(j) })
	})
}

// toolJobEnded hands an ended job to whoever waits for it.
func (a *app) toolJobEnded(j jobs.Job) {
	if fn, ok := a.toolWait[j.ID]; ok {
		delete(a.toolWait, j.ID)
		fn(j)
		return
	}
	if len(a.toolEnded) > 64 {
		clear(a.toolEnded)
	}
	a.toolEnded[j.ID] = j
}

// toolAwait calls fn when job id ends (at once if it already did).
func (a *app) toolAwait(id string, fn func(jobs.Job)) {
	if j, ok := a.toolEnded[id]; ok {
		delete(a.toolEnded, id)
		fn(j)
		return
	}
	a.toolWait[id] = fn
}

// toolJobErr is a failed job's reason, translated.
func toolJobErr(j jobs.Job) string {
	if j.Err != nil {
		return jobErrorText(j.Err)
	}
	if j.State == jobs.StateInterrupted {
		return i18n.T("The engine stopped; try again.")
	}
	return i18n.T("Cancelled")
}

// --- voices ---------------------------------------------------------------

// toolChoice is a voice a tool may speak with.
type toolChoice struct {
	label string
	model catalog.Entry
	voice *core.Voice // saved; nil = a built-in speaker
	spk   string
}

// toolChoices are the saved voices (each on a model that speaks it), then
// the built-in speakers of the Studio's model.
func (a *app) toolChoices() []toolChoice {
	var out []toolChoice
	for i := range a.voices {
		v := &a.voices[i]
		if e, ok := a.toolModelFor(v); ok {
			out = append(out, toolChoice{label: v.Name, model: e, voice: v})
		}
	}
	e := a.preset()
	for _, sp := range e.Voices {
		out = append(out, toolChoice{label: sp.ID + " · " + e.DisplayName(), model: e, spk: sp.ID})
	}
	if len(out) == 0 {
		out = append(out, toolChoice{label: e.DisplayName(), model: e})
	}
	return out
}

func (a *app) toolChoice(label string) (toolChoice, bool) {
	cs := a.toolChoices()
	i := slices.IndexFunc(cs, func(c toolChoice) bool { return c.label == label })
	if i < 0 {
		return toolChoice{}, false
	}
	return cs[i], true
}

func toolLabels(cs []toolChoice) []string {
	out := make([]string, len(cs))
	for i, c := range cs {
		out[i] = c.label
	}
	return out
}

// toolModelFor is a model that speaks v: the Studio's, the voice's own,
// else the first that can.
func (a *app) toolModelFor(v *core.Voice) (catalog.Entry, bool) {
	cands := []catalog.Entry{a.preset()}
	for _, repo := range []string{v.Model, v.CreatedWith} {
		if e, ok := catalog.Builtin().ByRepo(repo); ok {
			cands = append(cands, e)
		}
	}
	if v.Kind == core.VoiceDesigned {
		cands = append(cands, designModels()...)
	}
	cands = append(cands, cloneModels()...)
	for _, e := range cands {
		if ok, _ := voiceUsable(v, e); ok {
			return e, true
		}
	}
	return catalog.Entry{}, false
}

// toolRecipe is the recipe of text in a choice's voice.
func (a *app) toolRecipe(ch toolChoice, text string) core.Take {
	t := apiRecipe(api.SpeakRequest{ID: core.NewID(timeNow()), Text: text, Model: ch.model, Voice: ch.voice, Speaker: ch.spk, Normalize: true},
		a.fillVoice, a.lib.svc.Voices.FilePath)
	t.Origin = core.TakeConvert // the take Convert keeps (History, the Export dialog)
	return t
}

// preview renders a recipe without keeping it, into run.
func (a *app) toolPreview(run *toolRun, recipe core.Take) {
	run.busy, run.err, run.output, run.jobID = true, "", "", ""
	run.model = catalog.Builtin().DisplayName(recipe.Model)
	a.tSeq++
	run.key = "tool:" + itoa(a.tSeq)
	svc := a.lib.svc.Render
	a.async(func() {
		jobID, out, err := svc.Preview(context.Background(), recipe)
		a.post(func() {
			if err != nil {
				run.busy, run.err = false, a.toolRefusal(err)
				return
			}
			run.jobID, run.output = jobID, out
			a.toolAwait(jobID, func(j jobs.Job) {
				run.busy = false
				if j.State != jobs.StateSucceeded {
					run.err = toolJobErr(j)
					run.output = ""
				}
			})
		})
	})
}

// toolRefusal explains why a job was refused before it ran.
func (a *app) toolRefusal(err error) string {
	var lic *core.LicenceRequiredError
	switch {
	case errors.As(err, &lic):
		return i18n.T("Accept the licence of %s first (Models).", catalog.Builtin().DisplayName(lic.Model))
	case errors.Is(err, core.ErrNotDownloaded):
		return i18n.T("Download the model first (Models).")
	}
	return engineErrorText(err)
}

var timeNow = time.Now

// playRun plays or stops a preview.
func (a *app) playRun(run *toolRun) {
	if run == nil || run.output == "" {
		return
	}
	if err := a.pl.Toggle(run.key, run.output); err != nil {
		a.toastWarn(i18n.T("Could not play: %v", err))
	}
}
