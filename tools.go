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
// Files: tools.go (state, actions), tools_view.go (the page).

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/egoist/mygo"
	"github.com/hehehai/AudioSmith/internal/api"
	"github.com/hehehai/AudioSmith/internal/catalog"
	"github.com/hehehai/AudioSmith/internal/core"
	"github.com/hehehai/AudioSmith/internal/dsp/stretch"
	"github.com/hehehai/AudioSmith/internal/dsp/wav"
	"github.com/hehehai/AudioSmith/internal/engine/proto"
	"github.com/hehehai/AudioSmith/internal/i18n"
	"github.com/hehehai/AudioSmith/internal/jobs"
	"github.com/hehehai/AudioSmith/internal/llm"
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
		a.toast(i18n.T("Could not play: %v", err))
	}
}

// --- A/B ---------------------------------------------------------------------

func (a *app) runAB() {
	text := strings.TrimSpace(a.ab.text)
	if text == "" {
		return
	}
	for i, label := range []string{a.ab.a, a.ab.b} {
		ch, ok := a.toolChoice(label)
		if !ok {
			continue
		}
		if a.ab.runs[i] == nil {
			a.ab.runs[i] = &toolRun{}
		}
		a.toolPreview(a.ab.runs[i], a.toolRecipe(ch, text))
	}
}

// --- convert -------------------------------------------------------------------

func (a *app) chooseConvertFile() {
	go func() {
		paths, err := mygo.Dialog.Open(mygo.OpenDialogOptions{Parent: a.win, Title: i18n.T("A recording to convert"),
			Filters: []mygo.FileFilter{{Name: i18n.T("Audio or video"), Extensions: transcribeExts}}})
		if err != nil || len(paths) == 0 {
			return
		}
		a.post(func() { a.setConvertFile(paths[0]) })
	}()
}

// setConvertFile takes a file and measures it.
func (a *app) setConvertFile(path string) {
	cv := &a.cv
	cv.file, cv.name, cv.duration, cv.heard, cv.take, cv.err, cv.note = path, filepath.Base(path), 0, "", nil, "", ""
	a.probeMedia(path, func(info proto.ProbeResult, err error) {
		if a.cv.file == path && err == nil {
			a.cv.duration = info.Duration
		}
	})
}

// runConvert transcribes the file, then speaks the text in the voice.
func (a *app) runConvert() {
	cv := &a.cv
	ch, ok := a.toolChoice(cv.voice)
	if cv.file == "" || !ok || a.trSvc == nil {
		return
	}
	stt := sttEntry(a.prefs.Transcribe.FastModel, "")
	req := services.TranscribeRequest{Input: cv.file, Source: core.SourceFile, Title: i18n.T("Convert: %s", cv.name), Mode: "fast", Model: stt.Repo}
	cv.stage, cv.err, cv.note, cv.heard, cv.take = i18n.T("Listening…"), "", "", "", nil
	svc, file := a.trSvc, cv.file
	a.async(func() {
		_, jobID, err := svc.Transcribe(context.Background(), req)
		a.post(func() {
			if a.cv.file != file {
				return
			}
			if err != nil {
				a.cv.stage, a.cv.err = "", a.toolRefusal(err)
				return
			}
			a.toolAwait(jobID, func(j jobs.Job) {
				if a.cv.file != file {
					return
				}
				var res services.TranscribeResult
				if j.State != jobs.StateSucceeded || json.Unmarshal(j.Result, &res) != nil {
					a.cv.stage, a.cv.err = "", toolJobErr(j)
					return
				}
				a.cv.heard = res.Transcript.Text()
				if a.cv.duration == 0 {
					a.cv.duration = res.Transcript.Duration
				}
				a.convertSpeak(ch)
			})
		})
	})
}

// convertSpeak generates what was heard in the chosen voice.
func (a *app) convertSpeak(ch toolChoice) {
	cv := &a.cv
	if strings.TrimSpace(cv.heard) == "" {
		cv.stage, cv.err = "", i18n.T("No speech was heard in the recording.")
		return
	}
	t := a.toolRecipe(ch, cv.heard)
	stretchTo := 0.0
	if cv.match && cv.duration > 0 {
		if ch.model.Has(catalog.CapDuration) {
			t.Duration = cv.duration
		} else {
			stretchTo = cv.duration // after the take, by time-stretching it
		}
	}
	cv.stage = i18n.T("Speaking…")
	svc := a.lib.svc.Takes
	file := cv.file
	a.async(func() {
		jobID, err := svc.Generate(context.Background(), core.TakeRequest{Recipe: t})
		a.post(func() {
			if a.cv.file != file {
				return
			}
			if err != nil {
				a.cv.stage, a.cv.err = "", a.toolRefusal(err)
				return
			}
			a.toolAwait(jobID, func(j jobs.Job) {
				if a.cv.file != file {
					return
				}
				a.cv.stage = ""
				if j.State != jobs.StateSucceeded {
					a.cv.err = toolJobErr(j)
					return
				}
				var res services.RenderResult
				json.Unmarshal(j.Result, &res)
				done := t
				done.File = t.ID + ".wav"
				done.AudioSeconds, done.SampleRate, done.JobID, done.GenSeconds = res.AudioSeconds, res.SampleRate, j.ID, res.Seconds
				if res.Seed != 0 {
					done.Seed = res.Seed
				}
				a.cv.take = &done
				if stretchTo > 0 && res.Output != "" {
					a.convertStretch(done, res.Output, stretchTo)
				}
			})
		})
	})
}

// convertMaxRatio bounds how far Convert stretches a take to match a
// recording (beyond it speech smears; see dsp/stretch).
const convertMaxRatio = 2.0

// convertStretch makes a copy of take as long as the recording, by
// time-stretching it without changing its pitch, and adds it to the
// History (the natural take stays there too).
func (a *app) convertStretch(take core.Take, path string, target float64) {
	file := a.cv.file
	a.cv.stage = i18n.T("Matching the length…")
	svc := a.lib.svc.Takes
	a.async(func() {
		matched, note, err := stretchTake(context.Background(), svc, take, path, target)
		a.post(func() {
			if a.cv.file != file {
				return
			}
			a.cv.stage = ""
			if err != nil {
				a.cv.err = i18n.T("Could not match the length: %v", err)
				return
			}
			a.cv.note = note
			a.cv.take = &matched
		})
	})
}

// stretchTake writes take's audio stretched to target seconds as a new
// take. note says when the ratio had to be capped.
func stretchTake(ctx context.Context, svc core.TakeService, take core.Take, path string, target float64) (core.Take, string, error) {
	buf, _, err := wav.ReadFile(path)
	if err != nil {
		return core.Take{}, "", err
	}
	if buf.Frames() == 0 || buf.Rate <= 0 {
		return core.Take{}, "", errors.New("the take has no audio")
	}
	have := float64(buf.Frames()) / float64(buf.Rate)
	ratio, note := target/have, ""
	if r := min(max(ratio, 1/convertMaxRatio), convertMaxRatio); r != ratio {
		ratio = r
		note = i18n.T("The take is %.1f s and the recording %.1f s: stretched as far as speech allows (%.1f s).", have, target, have*ratio)
	}
	out := stretch.ToLength(buf, int(math.Round(have*ratio*float64(buf.Rate))), stretch.Options{})
	m := take
	m.ID = core.NewID(timeNow())
	m.File = m.ID + ".wav"
	m.CreatedAt, m.Starred, m.JobID = time.Time{}, false, ""
	m.Duration = out.Seconds()
	m.AudioSeconds, m.SampleRate = out.Seconds(), out.Rate
	if err := wav.WriteFile(filepath.Join(svc.AudioDir(), m.File), out, wav.PCM16, wav.Options{}); err != nil {
		return core.Take{}, "", err
	}
	saved, err := svc.Add(ctx, m)
	return saved, note, err
}

// --- rate fit ----------------------------------------------------------------

// rateFitLangs are the languages the predictor knows, by name.
func rateFitLangs() (labels, codes []string) {
	for code := range translate.DefaultRates {
		codes = append(codes, code)
	}
	slices.SortFunc(codes, func(x, y string) int { return strings.Compare(langName(x), langName(y)) })
	for _, c := range codes {
		labels = append(labels, langName(c))
	}
	return labels, codes
}

func (a *app) runRateFit() {
	rf := &a.rf
	labels, codes := rateFitLangs()
	code := "en"
	if i := slices.Index(labels, rf.lang); i >= 0 {
		code = codes[i]
	}
	req := translate.FitRequest{Text: rf.text, Lang: code, Slot: rf.slot, Speed: rf.speed, Rewrite: rf.rewrite}
	router := a.llmRouter
	rf.busy, rf.err = true, ""
	a.async(func() {
		res, err := (&translate.Fitter{Router: router}).Check(context.Background(), req)
		a.post(func() {
			a.rf.busy = false
			if err != nil {
				a.rf.err, a.rf.res = err.Error(), nil
				return
			}
			a.rf.res = &res
		})
	})
}

// llmCan reports whether a writing-help skill has a model.
func (a *app) llmCan(s llm.Skill) bool { return a.llmRouter != nil && a.llmRouter.Available(s) }

// --- direction -------------------------------------------------------------------

func (a *app) runDirection() {
	d := &a.dir
	note := strings.TrimSpace(d.note)
	if note == "" {
		return
	}
	router := a.llmRouter
	d.busy = true
	a.async(func() {
		res := (&translate.Director{Router: router}).Parse(context.Background(), note)
		a.post(func() {
			a.dir.busy = false
			a.dir.res = &res
		})
	})
}

// directionModel is the model the direction preview speaks with: one
// that takes instructions with a speaker (Qwen3-TTS CustomVoice), else the
// Studio's.
func directionModel(studio catalog.Entry) catalog.Entry {
	for _, e := range catalog.Builtin().ForTask(catalog.TaskTTS) {
		if e.Has(catalog.CapVoiceStyle) && len(e.Voices) > 0 {
			return e
		}
	}
	return studio
}

func (a *app) previewDirection() {
	d := &a.dir
	if d.res == nil {
		return
	}
	e := directionModel(a.preset())
	speed := 1 + d.res.Rate
	t := apiRecipe(api.SpeakRequest{ID: core.NewID(timeNow()), Text: i18n.T("I can't believe we actually made it this far."), Model: e,
		Instructions: d.res.Instruct, Speed: speed, Normalize: true}, a.fillVoice, a.lib.svc.Voices.FilePath)
	if d.preview == nil {
		d.preview = &toolRun{}
	}
	a.toolPreview(d.preview, t)
}

// --- probe -------------------------------------------------------------------------

func (a *app) chooseProbeFile() {
	go func() {
		paths, err := mygo.Dialog.Open(mygo.OpenDialogOptions{Parent: a.win, Title: i18n.T("A media file")})
		if err != nil || len(paths) == 0 {
			return
		}
		a.post(func() { a.runProbe(paths[0]) })
	}()
}

func (a *app) runProbe(path string) {
	p := &a.prb
	p.file, p.busy, p.out, p.err = path, true, "", ""
	a.probeMedia(path, func(info proto.ProbeResult, err error) {
		if a.prb.file != path {
			return
		}
		a.prb.busy = false
		if err != nil {
			a.prb.err = engineErrorText(err)
			return
		}
		b, _ := json.MarshalIndent(info, "", "  ")
		a.prb.out = string(b)
	})
}

// rateLangDefault is the rate-fit language for the UI's.
func rateLangDefault() string {
	code := "en"
	if strings.HasPrefix(i18n.Locale(), "zh") {
		code = "zh"
	}
	return langName(code)
}
