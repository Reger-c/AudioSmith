package services

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hehehai/AudioSmith/internal/core"
	"github.com/hehehai/AudioSmith/internal/dsp/dsptest"
	"github.com/hehehai/AudioSmith/internal/dsp/wav"
	"github.com/hehehai/AudioSmith/internal/engine"
	"github.com/hehehai/AudioSmith/internal/engine/enginetest"
	"github.com/hehehai/AudioSmith/internal/engine/proto"
	"github.com/hehehai/AudioSmith/internal/jobs"
)

// The dialogue runner's tests run the fake engine (enginetest): this
// test binary, started again as the engine's gpu worker.
func TestMain(m *testing.M) {
	if enginetest.Enabled() {
		enginetest.Main()
	}
	os.Exit(m.Run())
}

// runnerEnv is a dialogue runner on the fake engine, recording its calls.
type runnerEnv struct {
	dr    dialogueRunner
	dir   string
	mu    sync.Mutex
	calls []any // the params of each engine request
	fail  func(method string) error
}

func newRunnerEnv(t *testing.T) *runnerEnv {
	t.Helper()
	bin, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	sup := engine.NewSupervisor(engine.Config{Bin: bin, AppVersion: "test", Bus: core.NewBus(), LogDir: t.TempDir(),
		Env: enginetest.Env(""), StatsEvery: -1, Logf: t.Logf})
	t.Cleanup(sup.Stop)
	if err := sup.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(10 * time.Second); ; {
		if st := sup.Status(); len(st) > 0 && st[0].State == engine.WorkerReady {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the fake engine is not ready")
		}
		time.Sleep(5 * time.Millisecond)
	}
	e := &runnerEnv{dir: t.TempDir()}
	r := &renderer{cacheDir: filepath.Join(e.dir, "cache"), logf: t.Logf, recognizer: "m/qwen3-asr", aligner: "m/aligner",
		engine: func(method string) jobs.Executor { return engine.Executor(sup, proto.RoleGPU, method) }}
	e.dr = r.dialogueRunner(jobs.Job{ID: "J1"})
	call := e.dr.call
	e.dr.call = func(ctx context.Context, method, model string, params, out any, report func(jobs.Progress)) error {
		e.mu.Lock()
		e.calls = append(e.calls, cloneParams(params))
		fail := e.fail
		e.mu.Unlock()
		if fail != nil {
			if err := fail(method); err != nil {
				return err
			}
		}
		return call(ctx, method, model, params, out, report)
	}
	return e
}

func cloneParams(p any) any {
	if d, ok := p.(*proto.DialogueParams); ok {
		c := *d
		c.Turns = slices.Clone(d.Turns)
		return c
	}
	return p
}

// dialogues are the tts.dialogue requests so far.
func (e *runnerEnv) dialogues() []proto.DialogueParams {
	e.mu.Lock()
	defer e.mu.Unlock()
	var out []proto.DialogueParams
	for _, c := range e.calls {
		if d, ok := c.(proto.DialogueParams); ok {
			out = append(out, d)
		}
	}
	return out
}

func (e *runnerEnv) reset() {
	e.mu.Lock()
	e.calls, e.fail = nil, nil
	e.mu.Unlock()
}

// job is a segment of texts by two speakers (A, B, A, B…), cloned from
// tidied clips, its files under name.
func (e *runnerEnv) job(t *testing.T, model, name string, texts ...string) *dialogueJob {
	t.Helper()
	j := &dialogueJob{model: model, oneTrack: strings.Contains(model, "moss"), lang: "zh", seed: 10, key: name}
	for k, txt := range []string{"今天晚上我们早点吃饭。", "今天放学的时候，我看见一只小鸟。"} {
		clip := filepath.Join(e.dir, fmt.Sprintf("ref%d.wav", k))
		wav.WriteFile(clip, dsptest.Sine(24000, 1, 220+80*float64(k), -14, 6, 0), wav.Float32, wav.Options{})
		j.refs = append(j.refs, dialogueRef{audio: clip, text: txt, key: clip, tidy: filepath.Join(e.dir, fmt.Sprintf("ref%d-tidy.wav", k))})
	}
	os.MkdirAll(filepath.Join(e.dir, "cache"), 0o755)
	for k, txt := range texts {
		j.speakers = append(j.speakers, k%2)
		j.texts = append(j.texts, txt)
		j.paths = append(j.paths, filepath.Join(e.dir, "cache", fmt.Sprintf("%s-%d.wav", name, k)))
	}
	return j
}

var scene = []string{"妈妈，我想领养一只小猫。", "养猫要花很多时间照顾它。", "我能！我每天喂它。", "那我们先去救助站看看吧。"}

// SoulX-Podcast (per-turn files): a line heard wrong is made again with
// the lines before it as context and another seed; one heard wrong twice
// is left to the caller (reason "heard"); the lines and codes are cached.
func TestDialogueRunPerTurn(t *testing.T) {
	e := newRunnerEnv(t)
	j := e.job(t, "m/soulx-podcast", "seg", scene...)
	// The fake garbles a marked turn on even seeds: line 2 (redo seed
	// 10+7919·3, odd) passes the second time; line 1 (10+7919·2, even)
	// never does.
	j.tagged = slices.Clone(j.texts)
	j.tagged[1] = enginetest.FakeGarbleMark + j.tagged[1]
	j.tagged[2] = enginetest.FakeGarbleMark + j.tagged[2]
	out, err := e.dr.Run(ctx, j)
	if err != nil {
		t.Fatal(err)
	}
	d := e.dialogues()
	if len(d) != 3 || out.redone != 2 || len(out.failed) != 1 || dialogueReason(out.failed[1]) != DialogueHeard || out.cached {
		t.Fatalf("%d calls, outcome %+v", len(d), out)
	}
	if len(d[0].Turns) != 4 || d[0].Speakers[0].RefAudio != j.refs[0].tidy || d[0].Turns[1].Text != j.tagged[1] {
		t.Errorf("first call %+v", d[0])
	}
	redo := d[2] // line 2 after lines 0 and 1
	if len(redo.Turns) != 3 || len(redo.Turns[0].Codes) == 0 || len(redo.Turns[1].Codes) == 0 || len(redo.Turns[2].Codes) != 0 ||
		*redo.Seed != dialogueRedoSeed(10, 2, 0) {
		t.Errorf("redo %+v", redo)
	}
	for k, p := range j.paths {
		_, statErr := os.Stat(p)
		_, failErr := os.Stat(p + ".fail")
		if (k == 1) != (statErr != nil) || (k == 1) != (failErr == nil) || (k != 1) != (len(dialogueCodes(p)) > 0) {
			t.Errorf("line %d: file %v, .fail %v, codes %v", k, statErr, failErr, dialogueCodes(p))
		}
	}
	// Cached: no call; the failed line is still failed, for the same reason.
	e.reset()
	out, err = e.dr.Run(ctx, j)
	if err != nil || !out.cached || len(e.calls) != 0 || dialogueReason(out.failed[1]) != DialogueHeard {
		t.Errorf("cached: %v %+v, %d calls", err, out, len(e.calls))
	}
}

// MOSS-TTSD (one track): the track is cut at the lines by the aligner,
// each line's frames kept; a line that fails its check is made again by
// continuation, the earlier lines' frames as context.
func TestDialogueRunOneTrack(t *testing.T) {
	e := newRunnerEnv(t)
	e.dr.recognizer = "" // the fake hears cut clips as its stock transcript
	j := e.job(t, "m/moss-ttsd", "moss", scene...)
	checks := map[int]int{}
	j.check = func(ctx context.Context, k int, path string) error {
		checks[k]++
		if k == 2 && checks[k] == 1 {
			return fmt.Errorf("heard nothing: %w", errCutHeard)
		}
		return e.dr.check(ctx, j, k, path)
	}
	out, err := e.dr.Run(ctx, j)
	if err != nil {
		t.Fatal(err)
	}
	d := e.dialogues()
	if len(d) != 2 || out.redone != 1 || len(out.failed) != 0 {
		t.Fatalf("%d calls, outcome %+v", len(d), out)
	}
	redo := d[1]
	if len(redo.Turns) != 3 || len(redo.Turns[0].Codes) == 0 || len(redo.Turns[0].Codes)%16 != 0 || len(redo.Turns[1].Codes)%16 != 0 ||
		len(redo.Turns[2].Codes) != 0 || *redo.Seed == *d[0].Seed {
		t.Errorf("redo %+v", redo)
	}
	for k, p := range j.paths {
		b, _, err := wav.ReadFile(p)
		if err != nil || b.Seconds() < 0.5 || len(dialogueCodes(p))%16 != 0 || len(dialogueCodes(p)) == 0 {
			t.Errorf("line %d: %v, codes %d", k, err, len(dialogueCodes(p)))
		}
	}
	// The single-line redo: line 3 again, after the cached lines' frames.
	e.reset()
	ctxCodes := [][]int{dialogueCodes(j.paths[0]), dialogueCodes(j.paths[1]), dialogueCodes(j.paths[2])}
	os.Remove(j.paths[3])
	if err := e.dr.Redo(ctx, j, 3, ctxCodes); err != nil {
		t.Fatal(err)
	}
	if d := e.dialogues(); len(d) != 1 || len(d[0].Turns) != 4 || len(d[0].Turns[2].Codes) == 0 || len(d[0].Turns[3].Codes) != 0 {
		t.Errorf("redo calls %+v", d)
	}
	if _, err := os.Stat(j.paths[3]); err != nil {
		t.Error(err)
	}
}

// Redo (a dub's edited line): one line made again by continuation, the
// lines before it from their cached codes; a second seed when the first
// fails; failing twice, the caller makes it (.fail cached).
func TestDialogueRedo(t *testing.T) {
	e := newRunnerEnv(t)
	j := e.job(t, "m/soulx-podcast", "redo", scene...)
	if _, err := e.dr.Run(ctx, j); err != nil {
		t.Fatal(err)
	}
	// Line 2 is edited: new text, new file.
	j.texts[2], j.paths[2] = "我能！我每天都喂它，还给它洗澡。", filepath.Join(e.dir, "cache", "redo-2b.wav")
	j.tagged = slices.Clone(j.texts)
	j.tagged[2] = enginetest.FakeGarbleMark + j.texts[2] // garbled on the first seed (even), heard on the second
	j.seed = 1                                           // 1+7919·3: even
	e.reset()
	ctxCodes := [][]int{dialogueCodes(j.paths[0]), dialogueCodes(j.paths[1])}
	if err := e.dr.Redo(ctx, j, 2, ctxCodes); err != nil {
		t.Fatal(err)
	}
	d := e.dialogues()
	if len(d) != 2 || len(d[0].Turns) != 3 || !slices.Equal(d[0].Turns[0].Codes, ctxCodes[0]) || *d[0].Seed == *d[1].Seed {
		t.Fatalf("redo calls %+v", d)
	}
	if _, err := os.Stat(j.paths[2]); err != nil || len(dialogueCodes(j.paths[2])) == 0 {
		t.Errorf("redone line: %v", err)
	}
	// Never heard: left to the caller.
	e.reset()
	j.paths[2] = filepath.Join(e.dir, "cache", "redo-2c.wav")
	j.check = func(context.Context, int, string) error { return fmt.Errorf("12 s: %w", errDialogueLength) }
	err := e.dr.Redo(ctx, j, 2, ctxCodes)
	if dialogueReason(err) != DialogueLength || len(e.dialogues()) != 2 {
		t.Errorf("failing redo: %v, %d calls", err, len(e.dialogues()))
	}
	if _, err := os.Stat(j.paths[2] + ".fail"); err != nil {
		t.Error("no .fail")
	}
	// Without the earlier lines' codes there is no context to continue.
	if err := e.dr.Redo(ctx, j, 2, ctxCodes[:1]); err == nil || !strings.Contains(err.Error(), "no codes") {
		t.Errorf("no context: %v", err)
	}
}

// A segment the model fails is cached as failed (.segfail); a missing
// model or engine is not (another try may work), and says so.
func TestDialogueRunFails(t *testing.T) {
	e := newRunnerEnv(t)
	j := e.job(t, "m/plain-tts", "fail", scene[:2]...) // no dialogue capability
	_, err := e.dr.Run(ctx, j)
	if dialogueReason(err) != DialogueSegmentFailed {
		t.Fatalf("unsupported model: %v", err)
	}
	e.reset()
	if _, err2 := e.dr.Run(ctx, j); err2 == nil || err2.Error() != err.Error() || len(e.calls) != 0 {
		t.Errorf("cached failure: %v, %d calls", err2, len(e.calls))
	}
	j = e.job(t, "m/soulx-podcast", "gone", scene[:2]...)
	e.fail = func(method string) error {
		return &jobs.Error{Code: proto.CodeModelNotDownloaded, Message: "not downloaded"}
	}
	if _, err := e.dr.Run(ctx, j); dialogueReason(err) != DialogueUnavailable {
		t.Errorf("not downloaded: %v", err)
	}
	if _, err := os.Stat(j.paths[0] + ".segfail"); err == nil {
		t.Error("a missing model is cached as a failure")
	}
	// A reference made first (a story's portable line) must be made.
	e.reset()
	j.refs[0].made = func(context.Context) (string, error) { return "", errors.New("no voice") }
	if _, err := e.dr.Run(ctx, j); err == nil || err.Error() != "no voice" {
		t.Errorf("made reference: %v", err)
	}
	// A clip that cannot be tidied is used as it is.
	j.refs[0].made = nil
	bad := filepath.Join(e.dir, "bad.wav")
	os.WriteFile(bad, []byte("not a wav"), 0o644)
	j.refs[0].audio, j.refs[0].tidy = bad, filepath.Join(e.dir, "bad-tidy.wav")
	sp, err := e.dr.references(ctx, j)
	if err != nil || sp[0].RefAudio != bad {
		t.Errorf("untidy clip: %v %+v", err, sp)
	}
}

func TestDialogueReason(t *testing.T) {
	for _, c := range []struct {
		err  error
		want string
	}{
		{nil, ""},
		{fmt.Errorf("x: %w", errCutHeard), DialogueHeard},
		{errors.New("heard \"嗯\" (error rate 0.80): " + errCutHeard.Error()), DialogueHeard}, // from a .fail file
		{errors.New("9.0 s for 1.0 s of text: " + errDialogueLength.Error()), DialogueLength},
		{&jobs.Error{Code: CodeEngineUnavailable}, DialogueUnavailable},
		{&jobs.Error{Code: proto.CodeLicenceRequired}, DialogueUnavailable},
		{errors.New("Metal GPU timeout"), DialogueSegmentFailed},
	} {
		if got := dialogueReason(c.err); got != c.want {
			t.Errorf("%v: %q, want %q", c.err, got, c.want)
		}
	}
}

// Two passes: every segment first, then the regular model's lines in
// order (those in no segment and those a segment gave back); the progress
// counts lines.
func TestDialogueTwoPass(t *testing.T) {
	var order []string
	var progress []string
	segs := [][]int{{1, 2}, {4, 5}}
	err := dialogueTwoPass(ctx, 7, segs,
		func(_ context.Context, i int) ([]int, error) {
			order = append(order, fmt.Sprint("seg", i))
			if i == 0 {
				return []int{2}, nil
			}
			return nil, nil
		},
		func(_ context.Context, li int) error { order = append(order, fmt.Sprint("line", li)); return nil },
		func(done, total int) { progress = append(progress, fmt.Sprintf("%d/%d", done, total)) })
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(order, " "); got != "seg0 seg1 line0 line2 line3 line6" {
		t.Errorf("order %s", got)
	}
	if got := strings.Join(progress, " "); got != "1/7 3/7 4/7 5/7 6/7 7/7" {
		t.Errorf("progress %s", got)
	}
	stop := errors.New("stop")
	order = nil
	err = dialogueTwoPass(ctx, 3, [][]int{{0, 1}}, func(context.Context, int) ([]int, error) { return nil, stop },
		func(_ context.Context, li int) error { order = append(order, "line"); return nil }, nil)
	if err != stop || len(order) != 0 {
		t.Errorf("a segment's error: %v %v", err, order)
	}
	err = dialogueTwoPass(ctx, 3, nil, nil, func(context.Context, int) error { return stop }, nil)
	if err != stop {
		t.Errorf("a line's error: %v", err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if err := dialogueTwoPass(cancelled, 1, [][]int{{0}}, nil, nil, nil); err != context.Canceled {
		t.Errorf("cancelled: %v", err)
	}
}

// The longform cache drops a dialogue line's sidecars with age, and its
// codes with its file.
func TestPruneDialogueSidecars(t *testing.T) {
	r := &renderer{cacheDir: t.TempDir()}
	dir := r.longformChunkDir()
	os.MkdirAll(dir, 0o755)
	old := time.Now().Add(-longformKeep - time.Hour)
	for _, n := range []string{"a-dlg2-0.wav.codes", "a-dlg2-1.wav.fail", "a-dlg2-0.wav.segfail", "notes.txt", "b-dlg2-0.wav.codes"} {
		p := filepath.Join(dir, n)
		os.WriteFile(p, []byte("x"), 0o644)
		if n != "b-dlg2-0.wav.codes" {
			os.Chtimes(p, old, old)
		}
	}
	r.pruneLongformChunks()
	var left []string
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		left = append(left, e.Name())
	}
	if got := strings.Join(left, " "); got != "b-dlg2-0.wav.codes notes.txt" {
		t.Errorf("left %s", got)
	}
}
