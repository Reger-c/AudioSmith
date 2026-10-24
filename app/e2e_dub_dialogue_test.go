package main

// TestDubDialogueE2E (thread #89) runs P0's E1 (#86: a two-speaker
// English conversation over music, dubbed into Chinese) through the
// product's path with multi-speaker dialogue on (a new project's
// default): the dub's pipeline records the conversation with
// SoulX-Podcast (references, checks, continuation, fallback, fit, mix),
// then one line is edited and made again by continuation. The script's
// transcript and the pasted translation stand in for transcription and
// translation, as in P0, so the numbers compare with P0's arm B. Each
// step is its own run (one model family loaded at a time):
//
//	AUDIOSMITH_DLG=dir AUDIOSMITH_DLG_STEP=source go test -run TestDubDialogueE2E -v -timeout 2h .
//
// Steps: source (the material on Qwen3-TTS CustomVoice), dub (the whole
// dub), edit (one line edited, made again), score (Qwen3-ASR 1.7B hears
// every fitted line), report (dir/summary.md).

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/hehehai/AudioSmith/internal/core"
	"github.com/hehehai/AudioSmith/internal/dsp"
	"github.com/hehehai/AudioSmith/internal/dsp/edit"
	"github.com/hehehai/AudioSmith/internal/dsp/wav"
	"github.com/hehehai/AudioSmith/internal/dub"
	"github.com/hehehai/AudioSmith/internal/engine"
	"github.com/hehehai/AudioSmith/internal/engine/proto"
	"github.com/hehehai/AudioSmith/internal/jobs"
	"github.com/hehehai/AudioSmith/internal/services"
	"github.com/hehehai/AudioSmith/internal/translate"
)

// dlgScript is P0's E1: who (0 = S1 Ryan, 1 = S2 Aiden), English, Chinese.
var dlgScript = []struct {
	who    int
	en, zh string
}{
	{0, "Hey, did you finish packing for the trip tomorrow?", "嘿，明天出门的行李你收拾好了吗？"},
	{1, "Almost. I still can't find my rain jacket.", "差不多了。我还是找不到我的雨衣。"},
	{0, "Really?", "真的吗？"},
	{1, "I think I left it in your car last month.", "我好像上个月把它落在你车里了。"},
	{0, "Oh, the blue one with the hood? Yes, it has been lying on the back seat for weeks, I keep forgetting to tell you.", "哦，带帽子的那件蓝色的？对，它在后座上放了好几个星期了，我一直忘了告诉你。"},
	{1, "Great. Can you bring it when you pick me up?", "太好了。你来接我的时候能顺便带上吗？"},
	{0, "Sure.", "没问题。"},
	{1, "And could you come around seven? I want to get to the lake before lunch, because the trail gets really crowded in the afternoon.", "还有，你能七点左右来吗？我想在午饭前赶到湖边，因为下午步道上人特别多。"},
	{0, "Seven is early, but fine. Should I bring some food?", "七点有点早，不过行。要我带点吃的吗？"},
	{1, "Just snacks. My sister is making sandwiches for everyone.", "带点零食就行。我姐姐在给大家做三明治。"},
	{0, "Wait, your sister is coming too?", "等等，你姐姐也去？"},
	{1, "Yes, and she's bringing her dog.", "对，她还要带上她的狗。"},
	{0, "Then we definitely need a bigger tent.", "那我们肯定得换个大点的帐篷了。"},
	{1, "Ha, you're right. See you at seven.", "哈，你说得对。七点见。"},
}

// dlgGaps: the silence after each line (negative: an overlap), as P0's.
var dlgGaps = []float64{0.6, 0.3, 0.4, 0.8, 0.5, 0.2, 1.2, 0.7, 0.4, 0.9, -0.3, 0.5, 1.0}

// dlgEdit is the line edited (index) and its new text.
const dlgEditLine = 5

var dlgEditText = "太好了。来接我的时候带上它吧？"

// dlgState is the run's state, kept between steps.
type dlgState struct {
	Project string                        `json:"project"`
	Truth   []dub.Segment                 `json:"truth"`
	Steps   map[string]map[string]float64 `json:"steps"`
	// Lines are the track's lines after dub and after edit, by step.
	Lines map[string]map[string]dub.Render `json:"lines"`
	// Requests are the tts.dialogue requests of a step: turns spoken, context turns.
	Requests map[string][][2]int `json:"requests"`
	Heard    map[string]dlgHeard `json:"heard"` // by segment id
	Warnings map[string][]string `json:"warnings"`
}

type dlgHeard struct {
	Text, Lang string
	CER        float64
}

func TestDubDialogueE2E(t *testing.T) {
	dir := os.Getenv("AUDIOSMITH_DLG")
	if dir == "" {
		t.Skip("set AUDIOSMITH_DLG to a directory")
	}
	step := os.Getenv("AUDIOSMITH_DLG_STEP")
	os.MkdirAll(dir, 0o755)
	statePath := filepath.Join(dir, "state.json")
	st := &dlgState{Steps: map[string]map[string]float64{}, Lines: map[string]map[string]dub.Render{}, Requests: map[string][][2]int{},
		Heard: map[string]dlgHeard{}, Warnings: map[string][]string{}}
	if b, err := os.ReadFile(statePath); err == nil {
		if err := json.Unmarshal(b, st); err != nil {
			t.Fatal(err)
		}
	}
	save := func() {
		b, _ := json.MarshalIndent(st, "", " ")
		os.WriteFile(statePath+".tmp", b, 0o644)
		os.Rename(statePath+".tmp", statePath)
	}
	defer save()
	if step == "report" {
		dlgReport(t, dir, st)
		return
	}
	bin := engine.Find("../resources")
	if bin == "" {
		t.Fatal("no engine: set AUDIOSMITH_ENGINE")
	}
	lib := openTestLibraryWith(t, filepath.Join(dir, "data"), libraryOptions{})
	h := &engineHost{bin: bin, logDir: filepath.Join(dir, "logs", step)}
	os.MkdirAll(h.logDir, 0o755)
	a := newApp(h, lib)
	a.autoplay = false
	v := &v02{a: a, h: h, events: make(chan func(), 1<<16), dir: dir, mem: newMemSampler()}
	defer v.mem.stop()
	a.enginePost = func(fn func()) { v.events <- fn }
	a.postQueue = a.enginePost
	h.sup = engine.NewSupervisor(engine.Config{Bin: bin, Bus: a.bus, LogDir: h.logDir, Offline: true, CacheDir: modelCache(a.prefs),
		OnDemand: []proto.Role{proto.RoleListen, proto.RoleMedia},
		Log: func(e core.LogEntry) {
			if e.Level != core.SeverityInfo && e.Level != core.SeverityDebug {
				t.Logf("%s %s: %s", e.Level, e.Source, e.Message)
			}
		}, Logf: t.Logf})
	a.connectEngine(h)
	defer h.Stop()
	a.startEngine()
	v.wait(t, "engine ready", time.Minute, func() bool { return a.engState == engineReady })
	a.refreshModels()
	v.drain()
	acceptLicences(t, a)
	ctx := context.Background()
	started := time.Now()
	timing := map[string]float64{}
	st.Steps[step] = timing
	defer func() {
		time.Sleep(2 * time.Second)
		v.drain()
		timing["wall"] = time.Since(started).Seconds()
		timing["mlxPeakGB"] = v.mlxPeak()
		timing["rssPeakGB"] = v.mem.peak()
		t.Logf("step %s: %.0f s, MLX peak %.2f GB, RSS peak %.2f GB", step, timing["wall"], timing["mlxPeakGB"], timing["rssPeakGB"])
	}()
	// The dialogue requests the dub makes (the dub's engine calls, counted).
	var requests [][2]int
	a.dubEngine = func(method string) jobs.Executor {
		ex := engine.Executor(h.sup, dub.RoleOf(method), method, engine.ExecutorOptions{AuxPaths: lib.auxSnapshots})
		if method != proto.MethodTTSDialogue {
			return ex
		}
		return jobs.ExecutorFunc(func(ctx context.Context, j jobs.Job, report func(jobs.Progress), chunk func(json.RawMessage)) (json.RawMessage, error) {
			var p proto.DialogueParams
			if json.Unmarshal(j.Spec.Params, &p) == nil {
				spoken, heard := 0, 0
				for _, tu := range p.Turns {
					if len(tu.Codes) > 0 {
						heard++
					} else {
						spoken++
					}
				}
				v.events <- func() { requests = append(requests, [2]int{spoken, heard}) }
			}
			return ex.Run(ctx, j, report, chunk)
		})
	}
	runDub := func(id string, req services.RunRequest) {
		ids, err := a.dubSvc.Run(ctx, id, req)
		if err != nil {
			t.Fatalf("run: %v", err)
		}
		for stg, jid := range ids {
			j := v.job(t, jid, 90*time.Minute)
			if j.State != jobs.StateSucceeded {
				t.Fatalf("stage %s: %s %+v", stg, j.State, j.Err)
			}
			if !j.Started.IsZero() && !j.Finished.IsZero() {
				timing[stg] = j.Finished.Sub(j.Started).Seconds()
			}
		}
	}
	project := func() *dub.Project {
		p, err := a.dubSvc.Get(ctx, st.Project)
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	collect := func(name string) {
		v.drain()
		p := project()
		tr := p.Track("zh")
		st.Lines[name] = tr.Lines
		st.Requests[name] = requests
		st.Warnings[name] = p.Stage(dub.StageSynthesize, "zh").Warnings
		out := filepath.Join(dir, name)
		os.MkdirAll(out, 0o755)
		for _, s := range p.Segments {
			if r := tr.Lines[s.ID]; r.File != "" {
				services.CopyFile(ctx, a.dubSvc.Path(p.ID, r.File), filepath.Join(out, s.ID+".wav"))
			}
		}
		if tr.File != "" {
			services.CopyFile(ctx, a.dubSvc.Path(p.ID, tr.File), filepath.Join(out, "mix.wav"))
		}
		for _, sp := range []string{"S1", "S2"} {
			g, _ := filepath.Glob(a.dubSvc.Path(p.ID, filepath.Join("refs", "dlg-"+sp+"-*.ref.wav")))
			for _, f := range g {
				services.CopyFile(ctx, f, filepath.Join(out, "ref-"+sp+".wav"))
			}
		}
	}

	switch step {
	case "source":
		cv := v.model(t, "qwen3-tts-0.6b-customvoice-8bit")
		made := map[string]core.Take{}
		core.On(a.bus, func(ev core.TakeCreated) { tc := ev.Take; v.events <- func() { made[tc.ID] = tc } })
		const rate = 24000
		var lines []dsp.Buffer
		for i, l := range dlgScript {
			tk := core.Take{ID: core.NewID(time.Now()), Text: l.en, Model: cv.Repo, Voice: []string{"Ryan", "Aiden"}[l.who],
				Language: modelLanguage(cv, "en"), Seed: int64(100 + i), Render: &core.TakeRender{Bits: 24, SeedPinned: true}}
			if _, err := lib.svc.Takes.Generate(ctx, core.TakeRequest{Recipe: tk}); err != nil {
				t.Fatal(err)
			}
			v.wait(t, fmt.Sprintf("line %d", i+1), 10*time.Minute, func() bool { _, ok := made[tk.ID]; return ok })
			b, _, err := wav.ReadFile(a.takeStore.AudioPath(made[tk.ID]))
			if err != nil {
				t.Fatal(err)
			}
			lines = append(lines, edit.Trim(dsp.Remix(b, 1), edit.TrimOptions{}))
		}
		at := 0.8
		st.Truth = nil
		for i, b := range lines {
			st.Truth = append(st.Truth, dub.Segment{ID: fmt.Sprintf("s%d", i+1), Start: dlgRound(at), End: dlgRound(at + b.Seconds()),
				Speaker: []string{"S1", "S2"}[dlgScript[i].who], Text: dlgScript[i].en, Volume: 1})
			if i < len(dlgGaps) {
				at += b.Seconds() + dlgGaps[i]
			}
		}
		n := dsp.FramesAt(st.Truth[len(st.Truth)-1].End+1, rate)
		speech := make([]float32, n)
		for i, b := range lines {
			o := dsp.FramesAt(st.Truth[i].Start, rate)
			for k, x := range b.Samples {
				if o+k < n {
					speech[o+k] += x * 0.8
				}
			}
		}
		music := synthMusic(n, rate)
		mix := dsp.Buffer{Rate: rate, Channels: 2, Samples: make([]float32, 2*n)}
		for k := range n {
			mix.Samples[2*k] = speech[k] + float32(0.45*music[2*k])
			mix.Samples[2*k+1] = speech[k] + float32(0.45*music[2*k+1])
		}
		if err := wav.WriteFile(filepath.Join(dir, "source.wav"), mix, wav.PCM16, wav.Options{}); err != nil {
			t.Fatal(err)
		}

	case "dub":
		// A new project as the Dub page makes it (dialogue auto), the
		// script's transcript and the pasted Chinese, then the whole dub.
		base := v.model(t, "qwen3-tts-0.6b-base-8bit")
		p := dub.New("", time.Now())
		p.Source = dub.Source{Kind: dub.SourceFile, Path: filepath.Join(dir, "source.wav"), Name: "E1.wav"}
		p.Title, p.SourceLang, p.Speakers = "P2 E1", "en", 2
		p.ChooseTargets([]string{"zh"})
		p.Settings.Model, p.Settings.Review = base.Repo, false
		if !p.Settings.DialogueOn() || p.Settings.Timing != dub.TimingSmart {
			t.Fatalf("a new dub: %+v", p.Settings)
		}
		for i, s := range st.Truth {
			s.Lines = map[string]dub.Line{"zh": {Text: dlgScript[i].zh, Edited: true}}
			p.Segments = append(p.Segments, s)
		}
		p.NextSeg = len(p.Segments) + 1
		created, err := a.dubSvc.Create(ctx, p)
		if err != nil {
			t.Fatal(err)
		}
		st.Project = created.ID
		save()
		translate.DefaultPredictor.ResetCalibration()
		runDub(created.ID, services.RunRequest{Through: dub.StageMix})
		collect("dub")

	case "edit":
		// One line edited: made again alone, after the lines before it.
		p := project()
		id := st.Truth[dlgEditLine].ID
		if _, err := a.dubSvc.Update(ctx, p.ID, func(p *dub.Project) error { p.SetText(id, "zh", dlgEditText); return nil }); err != nil {
			t.Fatal(err)
		}
		runDub(p.ID, services.RunRequest{Through: dub.StageMix})
		collect("edit")

	case "score":
		// Qwen3-ASR 1.7B (#80's scorer, by path) hears every fitted line.
		scorer := "mlx-community/Qwen3-ASR-1.7B-8bit"
		g, _ := filepath.Glob(filepath.Join(modelCache(a.prefs), "models--mlx-community--Qwen3-ASR-1.7B-8bit", "snapshots", "*"))
		if len(g) == 0 {
			t.Fatal("no Qwen3-ASR 1.7B")
		}
		ex := engine.Executor(h.sup, proto.RoleGPU, proto.MethodSTTTranscribe, engine.ExecutorOptions{ModelPath: lib.modelSnapshot})
		hear := func(path, lang string) proto.TranscribeResult {
			raw, _ := json.Marshal(proto.TranscribeParams{Model: scorer, ModelPath: g[0], Audio: path, Language: lang})
			var tr proto.TranscribeResult
			v.do(t, "hear", 10*time.Minute, func() error {
				res, err := ex.Run(ctx, jobs.Job{ID: core.NewID(time.Now()), Spec: jobs.Spec{Params: raw}}, func(jobs.Progress) {}, nil)
				if err == nil {
					err = json.Unmarshal(res, &tr)
				}
				return err
			})
			return tr
		}
		for _, name := range []string{"dub", "edit"} {
			for i, s := range st.Truth {
				f := filepath.Join(dir, name, s.ID+".wav")
				if _, err := os.Stat(f); err != nil {
					continue
				}
				want := dlgScript[i].zh
				if name == "edit" && i == dlgEditLine {
					want = dlgEditText
				}
				tr, auto := hear(f, "zh"), hear(f, "")
				st.Heard[name+"/"+s.ID] = dlgHeard{Text: tr.Text, Lang: auto.Language,
					CER: float64(dlgDist(dlgNorm(want), dlgNorm(tr.Text))) / float64(max(len(dlgNorm(want)), 1))}
			}
		}
	default:
		t.Fatalf("unknown step %q", step)
	}
	raw, _ := json.Marshal(proto.UnloadParams{})
	engine.Executor(h.sup, proto.RoleGPU, proto.MethodModelsUnload).Run(ctx, jobs.Job{ID: "unload", Spec: jobs.Spec{Params: raw}}, func(jobs.Progress) {}, nil)
}

func dlgRound(x float64) float64 { return math.Round(x*1000) / 1000 }

var dlgKeep = regexp.MustCompile(`[^\p{Han}0-9a-zA-Z]`)

func dlgNorm(s string) []rune { return []rune(strings.ToLower(dlgKeep.ReplaceAllString(s, ""))) }

func dlgDist(a, b []rune) int {
	d := make([]int, len(b)+1)
	for j := range d {
		d[j] = j
	}
	for i := 1; i <= len(a); i++ {
		prev := d[0]
		d[0] = i
		for j := 1; j <= len(b); j++ {
			cur := d[j]
			c := 1
			if a[i-1] == b[j-1] {
				c = 0
			}
			d[j] = min(d[j]+1, d[j-1]+1, prev+c)
			prev = cur
		}
	}
	return d[len(b)]
}

// dlgReport writes dir/summary.md: per line and in all, against P0's B.
func dlgReport(t *testing.T, dir string, st *dlgState) {
	var b strings.Builder
	fmt.Fprintf(&b, "# P2 E1 through the product (thread #89)\n\n")
	for _, name := range []string{"dub", "edit"} {
		lines := st.Lines[name]
		if lines == nil {
			continue
		}
		okTight, n := 0, 0
		var dev, over, maxOver, rawEst, cer float64
		var heard, notZh, viaDlg int
		fmt.Fprintf(&b, "## %s\n\n| line | via | verdict | ratio | raw s | raw ÷ est | over s | CER | lang | heard |\n|---|---|---|---|---|---|---|---|---|---|\n", name)
		for i, s := range st.Truth {
			r := lines[s.ID]
			n++
			if r.Verdict == dub.VerdictOK || r.Verdict == dub.VerdictTight {
				okTight++
			}
			if r.Via == dub.ViaDialogue {
				viaDlg++
			}
			dev += math.Abs(firstNonZero(r.Ratio, 1) - 1)
			over += r.Over
			maxOver = max(maxOver, r.Over)
			want := dlgScript[i].zh
			if name == "edit" && i == dlgEditLine {
				want = dlgEditText
			}
			est := translate.DefaultPredictor.Duration(want, "zh", 1)
			re := r.Raw / est
			rawEst += re
			h := st.Heard[name+"/"+s.ID]
			if h.Text != "" {
				heard++
				cer += h.CER
				if !strings.HasPrefix(h.Lang, "zh") && !strings.EqualFold(h.Lang, "chinese") {
					notZh++
				}
			}
			fmt.Fprintf(&b, "| %s | %s %s | %s | %.3f | %.2f | %.2f | %.2f | %.1f %% | %s | %s |\n", s.ID, firstOf(r.Via, "single"), r.FallbackReason, r.Verdict,
				firstNonZero(r.Ratio, 1), r.Raw, re, r.Over, 100*h.CER, h.Lang, h.Text)
		}
		fmt.Fprintf(&b, "\n- by the dialogue model: %d / %d; requests (spoken, context): %v; warnings %v\n", viaDlg, n, st.Requests[name], st.Warnings[name])
		fmt.Fprintf(&b, "- fit ok/tight: %d/%d (%.0f %%), mean |ratio−1| %.3f, over mean/max %.2f / %.2f s, raw ÷ estimate %.2f\n",
			okTight, n, 100*float64(okTight)/float64(n), dev/float64(n), over/float64(n), maxOver, rawEst/float64(n))
		if heard > 0 {
			fmt.Fprintf(&b, "- CER %.1f %% (heard %d), lines not Chinese %d\n", 100*cer/float64(heard), heard, notZh)
		}
		if tm := st.Steps[name]; tm != nil {
			fmt.Fprintf(&b, "- synthesize %.0f s, wall %.0f s, MLX peak %.2f GB, RSS peak %.2f GB\n", tm[dub.StageKey(dub.StageSynthesize, "zh")], tm["wall"], tm["mlxPeakGB"], tm["rssPeakGB"])
		}
		b.WriteString("\n")
	}
	b.WriteString("P0 arm B (E1, SoulX a1, hook): fit ok/tight 13/14 (93 %), mean |ratio−1| 0.023, over 0.07 / 0.95 s, raw ÷ estimate 1.12, CER 2.0 %, 0 lines not Chinese, model 79 s.\n")
	os.WriteFile(filepath.Join(dir, "summary.md"), []byte(b.String()), 0o644)
	t.Log(b.String())
}

func firstNonZero(x, def float64) float64 {
	if x == 0 {
		return def
	}
	return x
}
