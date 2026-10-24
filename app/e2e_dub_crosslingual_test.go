package main

// TestDubCrossLingual (thread #90) measures cloning a speaker into another
// language (a dub's per-segment lines): short two-speaker scripts in
// en/ja/ko/fr (→ zh) and zh (→ en), made with Qwen3-TTS CustomVoice over
// synthetic music, run through the dub's ingest, separate and references
// stages; then each "arm" makes every line, and the score step hears them
// again (Qwen3-ASR 1.7B: the text in the target language, and the language
// it detects). Speaker similarity (CAM++) is computed off the Mac from
// manifest.json.
//
//	AUDIOSMITH_XL=dir AUDIOSMITH_XL_STEP=source|prep|gen|dub|score go test -run TestDubCrossLingual -v -timeout 2h .
//
// gen makes the lines with tts.synthesize straight away
// (AUDIOSMITH_XL_ARM names the arm, AUDIOSMITH_XL_MODEL picks the catalogue
// model, AUDIOSMITH_XL_REFTEXT=0 sends no reference transcript);
// dub makes them through the product's synthesize stage (fit, mix).
// AUDIOSMITH_XL_ONLY=E1,E2 limits the materials.

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/hehehai/AudioSmith/internal/catalog"
	"github.com/hehehai/AudioSmith/internal/core"
	"github.com/hehehai/AudioSmith/internal/dsp"
	"github.com/hehehai/AudioSmith/internal/dsp/edit"
	"github.com/hehehai/AudioSmith/internal/dsp/wav"
	"github.com/hehehai/AudioSmith/internal/dub"
	"github.com/hehehai/AudioSmith/internal/engine"
	"github.com/hehehai/AudioSmith/internal/engine/proto"
	"github.com/hehehai/AudioSmith/internal/jobs"
	"github.com/hehehai/AudioSmith/internal/services"
)

// xlLine: who (0 = S1, 1 = S2) and the line in each language.
type xlLine struct {
	who                int
	en, ja, zh, ko, fr string
}

var xlScript = []xlLine{
	{0, "Hey, did you finish packing for the trip tomorrow?", "ねえ、明日の旅行の準備はもう終わった？", "嘿，明天出门的行李你收拾好了吗？",
		"야, 내일 여행 짐은 다 쌌어?", "Salut, tu as fini de faire tes bagages pour le voyage de demain ?"},
	{1, "Almost. I still can't find my rain jacket.", "だいたいね。でも、まだレインコートが見つからないんだ。", "差不多了。我还是找不到我的雨衣。",
		"거의. 근데 아직 우비를 못 찾겠어.", "Presque. Je ne trouve toujours pas mon imperméable."},
	{0, "Really?", "本当に？", "真的吗？", "정말?", "Vraiment ?"},
	{1, "I think I left it in your car last month.", "先月、君の車に置き忘れたと思う。", "我好像上个月把它落在你车里了。",
		"지난달에 네 차에 두고 온 것 같아.", "Je crois que je l'ai laissé dans ta voiture le mois dernier."},
	{0, "Oh, the blue one with the hood? Yes, it has been lying on the back seat for weeks, I keep forgetting to tell you.",
		"ああ、フードの付いた青いやつ？うん、もう何週間も後ろの席に置いたままで、ずっと言い忘れてたんだ。",
		"哦，带帽子的那件蓝色的？对，它在后座上放了好几个星期了，我一直忘了告诉你。",
		"아, 모자 달린 파란 거? 응, 몇 주째 뒷좌석에 있었는데, 계속 말하는 걸 잊어버렸어.",
		"Ah, le bleu avec la capuche ? Oui, il traîne sur la banquette arrière depuis des semaines, j'oublie toujours de te le dire."},
	{1, "Great. Can you bring it when you pick me up?", "よかった。迎えに来るとき、持ってきてくれる？", "太好了。你来接我的时候能顺便带上吗？",
		"잘됐다. 나 데리러 올 때 가져다줄 수 있어?", "Super. Tu peux l'apporter quand tu viens me chercher ?"},
	{0, "Sure.", "いいよ。", "没问题。", "그럼.", "Bien sûr."},
	{1, "And could you come around seven? I want to get to the lake before lunch, because the trail gets really crowded in the afternoon.",
		"それと、七時ごろに来られる？午後は山道がすごく混むから、お昼の前に湖に着きたいんだ。",
		"还有，你能七点左右来吗？我想在午饭前赶到湖边，因为下午步道上人特别多。",
		"그리고 일곱 시쯤 올 수 있어? 오후에는 산길이 엄청 붐비니까 점심 전에 호수에 도착하고 싶어.",
		"Et tu pourrais venir vers sept heures ? Je voudrais arriver au lac avant le déjeuner, parce que le sentier est vraiment bondé l'après-midi."},
	{0, "Seven is early, but fine. Should I bring some food?", "七時は早いけど、まあいいよ。何か食べ物を持っていこうか？", "七点有点早，不过行。要我带点吃的吗？", "", ""},
	{1, "Just snacks. My sister is making sandwiches for everyone.", "お菓子だけでいいよ。姉がみんなのためにサンドイッチを作ってるから。", "带点零食就行。我姐姐在给大家做三明治。", "", ""},
	{0, "Wait, your sister is coming too?", "待って、お姉さんも来るの？", "等等，你姐姐也去？", "", ""},
	{1, "Yes, and she's bringing her dog.", "うん、それに犬も連れてくるんだ。", "对，她还要带上她的狗。", "", ""},
	{0, "Then we definitely need a bigger tent.", "じゃあ、絶対にもっと大きいテントが必要だね。", "那我们肯定得换个大点的帐篷了。", "", ""},
	{1, "Ha, you're right. See you at seven.", "はは、そうだね。じゃあ七時に。", "哈，你说得对。七点见。", "", ""},
}

func (l xlLine) in(lang string) string {
	switch lang {
	case "ja":
		return l.ja
	case "zh":
		return l.zh
	case "ko":
		return l.ko
	case "fr":
		return l.fr
	}
	return l.en
}

// xlGaps: the silence after each line (negative: overlap), as P0's.
var xlGaps = []float64{0.6, 0.3, 0.4, 0.8, 0.5, 0.2, 1.2, 0.7, 0.4, 0.9, -0.3, 0.5, 1.0}

type xlMaterial struct {
	name, src, dst string
	voices         [2]string
	lines          int
}

var xlMaterials = []xlMaterial{
	{"E1", "en", "zh", [2]string{"Ryan", "Aiden"}, 14},
	{"E2", "ja", "zh", [2]string{"Ono_Anna", "Ryan"}, 14},
	{"E3", "ko", "zh", [2]string{"Sohee", "Ryan"}, 8},
	{"E4", "fr", "zh", [2]string{"Serena", "Aiden"}, 8},
	{"E5", "zh", "en", [2]string{"Vivian", "Eric"}, 8},
	{"E6", "zh", "zh", [2]string{"Vivian", "Eric"}, 8}, // same language (re-voice)
}

// xlManifest is what the off-Mac scoring reads.
type xlManifest struct {
	Projects map[string]string                        `json:"projects"`
	Refs     map[string]map[string]string             `json:"refs"`    // material → speaker → file
	RefText  map[string]map[string]string             `json:"refText"` // material → speaker → transcript
	Lines    map[string][]xlManLine                   `json:"lines"`   // material → lines
	Arms     map[string]map[string]map[string]string  `json:"arms"`    // arm → material → line id → file
	Heard    map[string]map[string]map[string]xlHeard `json:"heard"`   // arm → material → line id
	Seconds  map[string]map[string]float64            `json:"seconds"` // arm → material → generation s
	Steps    map[string]map[string]float64            `json:"steps"`   // step → timing
}

type xlManLine struct {
	ID      string `json:"id"`
	Speaker string `json:"speaker"`
	Source  string `json:"source"`
	Target  string `json:"target"`
	Lang    string `json:"lang"` // target language
}

type xlHeard struct {
	Text     string  `json:"text"`     // heard in the target language
	AutoLang string  `json:"autoLang"` // detected language
	AutoText string  `json:"autoText"`
	Seconds  float64 `json:"seconds"`
}

func TestDubCrossLingual(t *testing.T) {
	dir := os.Getenv("AUDIOSMITH_XL")
	if dir == "" {
		t.Skip("set AUDIOSMITH_XL to a directory")
	}
	step := os.Getenv("AUDIOSMITH_XL_STEP")
	arm := os.Getenv("AUDIOSMITH_XL_ARM")
	only := strings.Split(os.Getenv("AUDIOSMITH_XL_ONLY"), ",")
	use := func(m xlMaterial) bool { return only[0] == "" || slices.Contains(only, m.name) }
	os.MkdirAll(dir, 0o755)
	manPath := filepath.Join(dir, "manifest.json")
	man := &xlManifest{Projects: map[string]string{}, Refs: map[string]map[string]string{}, RefText: map[string]map[string]string{},
		Lines: map[string][]xlManLine{}, Arms: map[string]map[string]map[string]string{}, Heard: map[string]map[string]map[string]xlHeard{},
		Seconds: map[string]map[string]float64{}, Steps: map[string]map[string]float64{}}
	if b, err := os.ReadFile(manPath); err == nil {
		if err := json.Unmarshal(b, man); err != nil {
			t.Fatal(err)
		}
	}
	save := func() {
		b, _ := json.MarshalIndent(man, "", " ")
		os.WriteFile(manPath+".tmp", b, 0o644)
		os.Rename(manPath+".tmp", manPath)
	}
	defer save()

	bin := engine.Find("../resources")
	if bin == "" {
		t.Fatal("no engine: set AUDIOSMITH_ENGINE")
	}
	lib := openTestLibraryWith(t, filepath.Join(dir, "data"), libraryOptions{})
	h := &engineHost{bin: bin, logDir: filepath.Join(dir, "logs", step+arm)}
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
	timing := map[string]float64{}
	man.Steps[step+" "+arm] = timing
	started := time.Now()
	defer func() {
		time.Sleep(2 * time.Second)
		v.drain()
		timing["wall"] = time.Since(started).Seconds()
		timing["mlxPeakGB"] = v.mlxPeak()
		timing["rssPeakGB"] = v.mem.peak()
		t.Logf("step %s %s: %.0f s, MLX peak %.2f GB, RSS peak %.2f GB", step, arm, timing["wall"], timing["mlxPeakGB"], timing["rssPeakGB"])
	}()
	call := func(method string, params, out any) error {
		raw, _ := json.Marshal(params)
		ex := engine.Executor(h.sup, proto.RoleGPU, method, engine.ExecutorOptions{ModelPath: lib.modelSnapshot, AuxPaths: lib.auxSnapshots})
		return v.do(t, method, 30*time.Minute, func() error {
			res, err := ex.Run(ctx, jobs.Job{ID: core.NewID(time.Now()), Spec: jobs.Spec{Params: raw}}, func(jobs.Progress) {}, nil)
			if err == nil && out != nil {
				err = json.Unmarshal(res, out)
			}
			return err
		})
	}
	cv := v.model(t, "qwen3-tts-0.6b-customvoice-8bit")
	base := v.model(t, "qwen3-tts-0.6b-base-8bit")
	speakerIDs := []string{"S1", "S2"}
	runDub := func(id string, req services.RunRequest) {
		ids, err := a.dubSvc.Run(ctx, id, req)
		if err != nil {
			t.Fatalf("run %s: %v", id, err)
		}
		for stg, jid := range ids {
			j := v.job(t, jid, 60*time.Minute)
			if j.State != jobs.StateSucceeded {
				t.Fatalf("stage %s: %s %+v", stg, j.State, j.Err)
			}
			if !j.Started.IsZero() && !j.Finished.IsZero() {
				timing[stg] = j.Finished.Sub(j.Started).Seconds()
			}
		}
	}

	switch step {
	case "source":
		made := map[string]core.Take{}
		core.On(a.bus, func(ev core.TakeCreated) { tc := ev.Take; v.events <- func() { made[tc.ID] = tc } })
		for _, m := range xlMaterials {
			if !use(m) {
				continue
			}
			const rate = 24000
			var lines []dsp.Buffer
			for i := range m.lines {
				tk := core.Take{ID: core.NewID(time.Now()), Text: xlScript[i].in(m.src), Model: cv.Repo, Voice: m.voices[xlScript[i].who],
					Language: modelLanguage(cv, m.src), Seed: int64(100 + i), Render: &core.TakeRender{Bits: 24, SeedPinned: true}}
				if _, err := lib.svc.Takes.Generate(ctx, core.TakeRequest{Recipe: tk}); err != nil {
					t.Fatal(err)
				}
				v.wait(t, "line", 10*time.Minute, func() bool { _, ok := made[tk.ID]; return ok })
				b, _, err := wav.ReadFile(a.takeStore.AudioPath(made[tk.ID]))
				if err != nil {
					t.Fatal(err)
				}
				lines = append(lines, edit.Trim(dsp.Remix(b, 1), edit.TrimOptions{}))
			}
			at := 0.8
			var segs []dub.Segment
			for i, b := range lines {
				segs = append(segs, dub.Segment{ID: fmt.Sprintf("s%d", i+1), Start: math.Round(at*1000) / 1000, End: math.Round((at+b.Seconds())*1000) / 1000,
					Speaker: speakerIDs[xlScript[i].who], Text: xlScript[i].in(m.src), Volume: 1})
				if i < len(xlGaps) {
					at += b.Seconds() + xlGaps[i]
				}
			}
			n := dsp.FramesAt(segs[len(segs)-1].End+1.0, rate)
			speech := make([]float32, n)
			for i, b := range lines {
				o := dsp.FramesAt(segs[i].Start, rate)
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
			os.MkdirAll(filepath.Join(dir, m.name), 0o755)
			if err := wav.WriteFile(filepath.Join(dir, m.name, "source.wav"), mix, wav.PCM16, wav.Options{}); err != nil {
				t.Fatal(err)
			}
			b, _ := json.Marshal(segs)
			os.WriteFile(filepath.Join(dir, m.name, "segments.json"), b, 0o644)
		}

	case "prep":
		for _, m := range xlMaterials {
			if !use(m) {
				continue
			}
			var segs []dub.Segment
			b, err := os.ReadFile(filepath.Join(dir, m.name, "segments.json"))
			if err != nil {
				t.Fatal(err)
			}
			json.Unmarshal(b, &segs)
			p := dub.New("", time.Now())
			p.Source = dub.Source{Kind: dub.SourceFile, Path: filepath.Join(dir, m.name, "source.wav"), Name: m.name + ".wav"}
			p.Title = "XL " + m.name
			p.SourceLang, p.Speakers = m.src, 2
			p.ChooseTargets([]string{m.dst})
			p.Settings.Model, p.Settings.Review, p.Settings.Timing, p.Settings.KeepBackground = base.Repo, false, dub.TimingSmart, true
			p.Names = map[string]string{"S1": "A", "S2": "B"}
			var ml []xlManLine
			for i, s := range segs {
				if m.src != m.dst {
					s.Lines = map[string]dub.Line{m.dst: {Text: xlScript[i].in(m.dst), Edited: true}}
				}
				p.Segments = append(p.Segments, s)
				ml = append(ml, xlManLine{ID: s.ID, Speaker: s.Speaker, Source: s.Text, Target: xlScript[i].in(m.dst), Lang: m.dst})
			}
			p.NextSeg = len(p.Segments) + 1
			created, err := a.dubSvc.Create(ctx, p)
			if err != nil {
				t.Fatal(err)
			}
			man.Projects[m.name], man.Lines[m.name] = created.ID, ml
			runDub(created.ID, services.RunRequest{Through: dub.StageReferences})
			q, _ := a.dubSvc.Get(ctx, created.ID)
			man.Refs[m.name], man.RefText[m.name] = map[string]string{}, map[string]string{}
			for _, sp := range speakerIDs {
				r := q.References[sp]
				dst := filepath.Join(dir, m.name, "ref-"+sp+".wav")
				services.CopyFile(ctx, a.dubSvc.Path(q.ID, r.File), dst)
				man.Refs[m.name][sp], man.RefText[m.name][sp] = dst, r.Text
				t.Logf("%s %s reference %.1f s %q", m.name, sp, r.End-r.Start, r.Text)
			}
			save()
		}

	case "gen":
		if arm == "" {
			t.Fatal("set AUDIOSMITH_XL_ARM")
		}
		model := base
		if id := os.Getenv("AUDIOSMITH_XL_MODEL"); id != "" {
			model = v.model(t, id)
		}
		snap, err := lib.modelSnapshot(ctx, model.Repo)
		if err != nil {
			t.Fatal(err)
		}
		refText := os.Getenv("AUDIOSMITH_XL_REFTEXT") != "0"
		if man.Arms[arm] == nil {
			man.Arms[arm] = map[string]map[string]string{}
			man.Seconds[arm] = map[string]float64{}
		}
		for _, m := range xlMaterials {
			if !use(m) {
				continue
			}
			out := filepath.Join(dir, m.name, arm)
			os.MkdirAll(out, 0o755)
			files := map[string]string{}
			begin := time.Now()
			for i, l := range man.Lines[m.name] {
				seed := int64(1000 + i)
				p := proto.SynthesizeParams{Model: model.Repo, ModelPath: snap, Text: l.Target, Language: modelLanguage(model, l.Lang),
					RefAudio: man.Refs[m.name][l.Speaker], Seed: &seed, Output: proto.Output{Path: filepath.Join(out, l.ID+".wav"), Bits: 32}}
				if refText {
					p.RefText = man.RefText[m.name][l.Speaker]
				}
				if err := call(proto.MethodTTSSynthesize, p, nil); err != nil {
					t.Fatalf("%s %s: %v", m.name, l.ID, err)
				}
				files[l.ID] = p.Output.Path
			}
			man.Arms[arm][m.name] = files
			man.Seconds[arm][m.name] = time.Since(begin).Seconds()
			t.Logf("%s %s: %.0f s", arm, m.name, man.Seconds[arm][m.name])
			save()
		}

	case "dub":
		// The product's synthesize stage (and mix): the fitted lines.
		if arm == "" {
			t.Fatal("set AUDIOSMITH_XL_ARM")
		}
		if man.Arms[arm] == nil {
			man.Arms[arm] = map[string]map[string]string{}
			man.Seconds[arm] = map[string]float64{}
		}
		for _, m := range xlMaterials {
			if !use(m) {
				continue
			}
			id := man.Projects[m.name]
			if mid := os.Getenv("AUDIOSMITH_XL_MODEL"); mid != "" {
				repo := v.model(t, mid).Repo
				if _, err := a.dubSvc.Update(ctx, id, func(p *dub.Project) error { p.Settings.Model = repo; return nil }); err != nil {
					t.Fatal(err)
				}
			}
			begin := time.Now()
			runDub(id, services.RunRequest{Through: dub.StageMix, Force: []dub.Stage{dub.StageSynthesize}})
			man.Seconds[arm][m.name] = time.Since(begin).Seconds()
			p, _ := a.dubSvc.Get(ctx, id)
			tr := p.Track(m.dst)
			out := filepath.Join(dir, m.name, arm)
			os.MkdirAll(out, 0o755)
			files := map[string]string{}
			for _, seg := range p.Segments {
				r := tr.Lines[seg.ID]
				if r.File == "" {
					t.Errorf("%s %s: %s", m.name, seg.ID, r.Err)
					continue
				}
				f := filepath.Join(out, seg.ID+".wav")
				services.CopyFile(ctx, a.dubSvc.Path(p.ID, r.File), f)
				files[seg.ID] = f
			}
			if tr.File != "" {
				services.CopyFile(ctx, a.dubSvc.Path(p.ID, tr.File), filepath.Join(out, "mix.wav"))
			}
			man.Arms[arm][m.name] = files
			t.Logf("%s %s: %.0f s", arm, m.name, man.Seconds[arm][m.name])
			save()
		}

	case "score":
		scorer := "mlx-community/Qwen3-ASR-1.7B-8bit"
		g, _ := filepath.Glob(filepath.Join(modelCache(a.prefs), "models--mlx-community--Qwen3-ASR-1.7B-8bit", "snapshots", "*"))
		if len(g) == 0 {
			t.Fatal("no Qwen3-ASR 1.7B")
		}
		arms := strings.Split(arm, ",")
		if arm == "" {
			arms = nil
			for k := range man.Arms {
				arms = append(arms, k)
			}
		}
		for _, an := range arms {
			if man.Heard[an] == nil {
				man.Heard[an] = map[string]map[string]xlHeard{}
			}
			for mname, files := range man.Arms[an] {
				if !use(xlMaterial{name: mname}) {
					continue
				}
				hs := map[string]xlHeard{}
				lang := man.Lines[mname][0].Lang
				for id, f := range files {
					var tr, auto proto.TranscribeResult
					if err := call(proto.MethodSTTTranscribe, proto.TranscribeParams{Model: scorer, ModelPath: g[0], Audio: f, Language: lang}, &tr); err != nil {
						t.Fatal(err)
					}
					call(proto.MethodSTTTranscribe, proto.TranscribeParams{Model: scorer, ModelPath: g[0], Audio: f}, &auto)
					hs[id] = xlHeard{Text: tr.Text, AutoLang: auto.Language, AutoText: auto.Text, Seconds: tr.AudioSeconds}
				}
				man.Heard[an][mname] = hs
				save()
			}
		}
	default:
		t.Fatalf("unknown step %q", step)
	}
	_ = catalog.DefaultSTT
}
