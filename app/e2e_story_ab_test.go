package main

// TestStoryAB renders a story's dialogue in A/B variants on the real
// engine, for listening (plan/story-continuity-plan.md): (a) as stories
// rendered before pacing (one 400 ms gap, a call per line), (b) with the
// dialogue pacing and merged turns, (c) with that and the AI director's
// scene, personas and directions sent as instructions, (d) as (c) with the
// other character's last direction in the instruction. Each on Qwen3-TTS
// 0.6B CustomVoice (speakers Vivian and Serena) and on 1.7B VoiceDesign
// (designed voices). It writes the chapters, their sidecars, the
// director's JSON and objective measures (gaps, per-character loudness
// spread, pitch and rate) to AUDIOSMITH_STORY_AB (a directory); the story
// is AUDIOSMITH_STORY_JSON (GET /api/longform/{id} of the app).
//
//	AUDIOSMITH_STORY_AB=/tmp/ab AUDIOSMITH_STORY_JSON=story.json go test -run TestStoryAB -v -timeout 1h .
//
// AUDIOSMITH_STORY_MODELS picks models (cv06,vd17), AUDIOSMITH_STORY_VARIANTS
// variants (a,b,c,d); AUDIOSMITH_STORY_DIRECTIONS reuses a director's JSON.

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"
	"unicode"

	"github.com/hehehai/AudioSmith/internal/catalog"
	"github.com/hehehai/AudioSmith/internal/core"
	"github.com/hehehai/AudioSmith/internal/dsp"
	"github.com/hehehai/AudioSmith/internal/dsp/loudness"
	"github.com/hehehai/AudioSmith/internal/dsp/wav"
	"github.com/hehehai/AudioSmith/internal/engine"
	"github.com/hehehai/AudioSmith/internal/engine/proto"
	"github.com/hehehai/AudioSmith/internal/llm"
	"github.com/hehehai/AudioSmith/internal/services"
	"github.com/hehehai/AudioSmith/internal/translate"
)

// abStory is the app's GET /api/longform/{id} as far as the test reads it.
type abStory struct {
	Title string `json:"title"`
	Cast  []struct {
		Name  string `json:"name"`
		Voice string `json:"voice"`
	} `json:"cast"`
	Lines []struct {
		ID      string `json:"id"`
		Text    string `json:"text"`
		Speaker string `json:"speaker"`
	} `json:"lines"`
}

// abDesigns are the 1.7B VoiceDesign voices of the CustomVoice speakers.
var abDesigns = map[string]string{
	"Vivian": "一个七岁左右的小女孩，声音清脆稚嫩，音调偏高，活泼可爱。",
	"Serena": "一位三十多岁的年轻妈妈，声音温柔柔和，语速平稳，略带疲惫。",
}

func TestStoryAB(t *testing.T) {
	out := os.Getenv("AUDIOSMITH_STORY_AB")
	if out == "" {
		t.Skip("set AUDIOSMITH_STORY_AB to a directory (and AUDIOSMITH_STORY_JSON to the story)")
	}
	raw, err := os.ReadFile(os.Getenv("AUDIOSMITH_STORY_JSON"))
	if err != nil {
		t.Fatal(err)
	}
	var story abStory
	if err := json.Unmarshal(raw, &story); err != nil {
		t.Fatal(err)
	}
	os.MkdirAll(out, 0o755)
	bin := engine.Find("resources")
	if bin == "" {
		t.Fatal("no engine: set AUDIOSMITH_ENGINE or run scripts/build-engine.sh")
	}
	lib := openTestLibrary(t)
	h := &engineHost{bin: bin, logDir: t.TempDir()}
	a := newApp(h, lib)
	a.autoplay = false
	events := make(chan func(), 1<<16)
	a.enginePost = func(fn func()) { events <- fn }
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
	waitFor := func(what string, timeout time.Duration, done func() bool) {
		t.Helper()
		deadline := time.After(timeout)
		for !done() {
			if a.engState == engineFailed {
				t.Fatalf("engine failed: %s\n%s", a.engError, a.engTail)
			}
			select {
			case fn := <-events:
				fn()
			case <-time.After(200 * time.Millisecond):
			case <-deadline:
				t.Fatalf("timed out waiting for %s", what)
			}
		}
	}
	a.startEngine()
	waitFor("ready", time.Minute, func() bool { return a.engState == engineReady })
	ctx := context.Background()

	// The document: the story's cast and lines.
	doc := services.NewLongformDoc(core.ProjectStory, "")
	doc.Title, doc.Language, doc.Seed = story.Title, "zh", 20261015
	ids := map[string]string{}
	for i, c := range story.Cast {
		id := fmt.Sprintf("c%d", i+1)
		ids[c.Name] = id
		doc.Cast = append(doc.Cast, services.CastMember{ID: id, Name: c.Name, Voice: c.Voice})
	}
	for _, l := range story.Lines {
		doc.Lines = append(doc.Lines, services.StoryLine{ID: l.ID, Text: l.Text, Character: ids[l.Speaker]})
	}

	// The director: the local language model (Qwen3 4B).
	var dir translate.StoryDirection
	dirPath := filepath.Join(out, "directions.json")
	if p := os.Getenv("AUDIOSMITH_STORY_DIRECTIONS"); p != "" {
		b, err := os.ReadFile(p)
		if err != nil || json.Unmarshal(b, &dir) != nil {
			t.Fatalf("directions %s: %v", p, err)
		}
	} else {
		e, _ := catalog.Builtin().ByID(catalog.DefaultLLM)
		snap, err := a.lib.modelSnapshot(ctx, e.Repo)
		if err != nil {
			t.Fatalf("%s: %v", e.Repo, err)
		}
		reg := llm.NewRegistry(nil, nil)
		reg.Register("local", &llm.EngineBackend{ID: "local", DefaultModel: e.Repo, Call: func(ctx context.Context, method string, params any, notify func(string, json.RawMessage)) (json.RawMessage, error) {
			p := params.(llm.GenerateParams)
			p.ModelPath = snap
			return h.sup.Call(ctx, proto.RoleGPU, core.NewID(time.Now()), method, p, func(m proto.Message) {
				if notify != nil {
					notify(m.Method, m.Params)
				}
			})
		}})
		router := &llm.Router{Registry: reg}
		router.SetRouting(llm.Routing{Default: []llm.Route{{Provider: "local"}}})
		started := time.Now()
		dir, err = (&translate.StoryDirector{Router: router}).Direct(ctx, storyScript(&doc, nil))
		if err != nil {
			t.Fatalf("director: %v", err)
		}
		t.Logf("director: %.1f s", time.Since(started).Seconds())
	}
	if b, err := json.MarshalIndent(dir, "", "  "); err == nil {
		os.WriteFile(dirPath, b, 0o644)
		t.Logf("directions:\n%s", b)
	}
	directed := doc
	directed.Lines = slices.Clone(doc.Lines)
	directed.Cast = slices.Clone(doc.Cast)
	applyDirection(&directed, dir, false)

	models := envList("AUDIOSMITH_STORY_MODELS", "cv06,vd17")
	variants := envList("AUDIOSMITH_STORY_VARIANTS", "a,b,c,d")
	var report []string
	report = append(report, "# Story A/B measures "+time.Now().Format("2006-01-02 15:04"), "")
	for _, m := range models {
		repo, resolve := "", services.VoiceResolver(nil)
		switch m {
		case "cv06":
			e, _ := catalog.Builtin().ByID("qwen3-tts-0.6b-customvoice-8bit")
			repo = e.Repo
			resolve = func(ref string) (string, core.Take, bool) {
				id, ok := strings.CutPrefix(ref, services.SpeakerRef)
				return ref, core.Take{Voice: id, Language: "Chinese"}, ok
			}
		case "vd17":
			e, _ := catalog.Builtin().ByID("qwen3-tts-1.7b-voicedesign-4bit")
			repo = e.Repo
			resolve = func(ref string) (string, core.Take, bool) {
				id, _ := strings.CutPrefix(ref, services.SpeakerRef)
				d, ok := abDesigns[id]
				return "design:" + id, core.Take{Voice: d, Language: "Chinese", Seed: int64(len(id)) * 7919}, ok
			}
		default:
			t.Fatalf("model %s", m)
		}
		for _, v := range variants {
			d := doc
			switch v {
			case "a":
				d.Production = services.DefaultProduction()
			case "b":
				d.Production = services.DefaultStoryProduction()
			case "c", "d":
				d = directed
				d.Production = services.DefaultStoryProduction()
			}
			d.Model = repo
			services.StoryPrevDirection = v == "d"
			spec, probs := services.Compile(&d, resolve)
			for _, p := range probs {
				if p.Blocking {
					t.Fatalf("%s-%s: %+v", m, v, p)
				}
			}
			spec.Audition = true
			started := time.Now()
			r, err := a.lib.svc.Longform.Start(ctx, spec)
			if err != nil {
				t.Fatal(err)
			}
			var body services.RenderBody
			waitFor(m+"-"+v, 30*time.Minute, func() bool {
				rr, b, err := a.lib.svc.Longform.Get(ctx, r.ID)
				body = b
				return err == nil && rr.State != core.RenderRunning
			})
			if len(body.Chapters) != 1 || body.Chapters[0].State != "done" {
				t.Fatalf("%s-%s: %+v %+v", m, v, body.Chapters, body.Error)
			}
			name := m + "-" + v
			wavPath := filepath.Join(out, name+".wav")
			if err := services.CopyFile(ctx, body.Chapters[0].File, wavPath); err != nil {
				t.Fatal(err)
			}
			services.CopyFile(ctx, services.Sidecar(body.Chapters[0].File), wavPath+".json")
			res, _ := services.ReadChapter(body.Chapters[0].File)
			lines := spec.Chapters[0].Lines
			md := abMeasure(t, wavPath, res, lines)
			md += abListen(t, ctx, h.sup, a.lib, wavPath, res, lines)
			t.Logf("%s: %.1f s audio in %.0f s, turns %d (unsplit %d)\n%s", name, res.AudioSeconds, time.Since(started).Seconds(), res.Turns, res.TurnsUnsplit, md)
			report = append(report, fmt.Sprintf("## %s\n\n%.1f s; merged turns %d (could not split %d); rendered in %.0f s\n\n%s", name, res.AudioSeconds,
				res.Turns, res.TurnsUnsplit, time.Since(started).Seconds(), md))
		}
	}
	services.StoryPrevDirection = false
	os.WriteFile(filepath.Join(out, "measures.md"), []byte(strings.Join(report, "\n")+"\n"), 0o644)
}

// abListen transcribes each line's place in the chapter with Qwen3-ASR
// and compares it with the line's text (character error rate over Han
// characters): a split turn's lines must hold their own words, and no
// instruction may be spoken.
func abListen(t *testing.T, ctx context.Context, sup engine.Supervisor, lib *library, path string, res services.ChapterResult, lines []services.LineSpec) string {
	e, ok := catalog.Builtin().ByID("qwen3-asr-0.6b-4bit")
	if !ok {
		return ""
	}
	snap, err := lib.modelSnapshot(ctx, e.Repo)
	if err != nil {
		t.Logf("ASR: %v", err)
		return ""
	}
	buf, _, err := wav.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	b.WriteString("\n| # | said (ASR) | CER |\n|---|---|---|\n")
	var errs, total int
	for i, lp := range res.Lines {
		seg := buf.Slice(dsp.FramesAt(lp.Start, buf.Rate), min(dsp.FramesAt(lp.End, buf.Rate), buf.Frames()))
		f := filepath.Join(t.TempDir(), fmt.Sprintf("line%d.wav", i))
		if err := wav.WriteFile(f, seg, wav.Float32, wav.Options{}); err != nil {
			t.Fatal(err)
		}
		raw, err := sup.Call(ctx, proto.RoleGPU, core.NewID(time.Now()), proto.MethodSTTTranscribe,
			proto.TranscribeParams{Model: e.Repo, ModelPath: snap, Audio: f, Language: "zh"}, nil)
		if err != nil {
			t.Logf("ASR line %d: %v", i+1, err)
			continue
		}
		var tr proto.TranscribeResult
		json.Unmarshal(raw, &tr)
		want := hanOnly(lines[i].Spans[0].Text)
		got := hanOnly(tr.Text)
		d := editDistance([]rune(want), []rune(got))
		errs, total = errs+d, total+len([]rune(want))
		fmt.Fprintf(&b, "| %d | %s | %.0f%% |\n", i+1, tr.Text, 100*float64(d)/math.Max(float64(len([]rune(want))), 1))
	}
	fmt.Fprintf(&b, "\nCER over all lines: %.1f%%\n", 100*float64(errs)/math.Max(float64(total), 1))
	return b.String()
}

func hanOnly(s string) string {
	var b strings.Builder
	for _, r := range s {
		if unicode.Is(unicode.Han, r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func editDistance(a, b []rune) int {
	prev := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur := make([]int, len(b)+1)
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			c := 1
			if a[i-1] == b[j-1] {
				c = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+c)
		}
		prev = cur
	}
	return prev[len(b)]
}

func envList(k, def string) []string {
	v := os.Getenv(k)
	if v == "" {
		v = def
	}
	return strings.Split(v, ",")
}

// abMeasure measures a chapter: per line its start, the gap before it,
// loudness, median pitch and pitch range, and speaking rate; per
// character the spread of loudness, pitch and rate.
func abMeasure(t *testing.T, path string, res services.ChapterResult, lines []services.LineSpec) string {
	buf, _, err := wav.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	mono := dsp.Remix(buf, 1)
	type stat struct{ lufs, f0, rate []float64 }
	per := map[string]*stat{}
	var b strings.Builder
	b.WriteString("| # | who | start | gap before | turn | LUFS | F0 median | F0 range (st) | chars/s |\n|---|---|---|---|---|---|---|---|---|\n")
	var gaps []float64
	for i, lp := range res.Lines {
		seg := mono.Slice(dsp.FramesAt(lp.Start, mono.Rate), min(dsp.FramesAt(lp.End, mono.Rate), mono.Frames()))
		lufs := loudness.Integrated(seg)
		f0, rng := abPitch(seg)
		chars := 0
		for _, sp := range lp.Spans {
			for _, r := range sp.Text {
				if unicode.Is(unicode.Han, r) {
					chars++
				}
			}
		}
		rate := float64(chars) / math.Max(lp.End-lp.Start, 0.1)
		who := lines[i].Character
		if who == "" {
			who = "Narrator"
		}
		gap, turn := "", ""
		if i > 0 {
			g := lp.Start - res.Lines[i-1].End
			gaps = append(gaps, g)
			gap = fmt.Sprintf("%.2f", g)
			turn = services.TurnOf(lines[i-1], lines[i])
		}
		s := per[who]
		if s == nil {
			s = &stat{}
			per[who] = s
		}
		s.lufs, s.f0, s.rate = append(s.lufs, lufs), append(s.f0, f0), append(s.rate, rate)
		fmt.Fprintf(&b, "| %d | %s | %.2f | %s | %s | %.1f | %.0f Hz | %.1f | %.1f |\n", i+1, who, lp.Start, gap, turn, lufs, f0, rng, rate)
	}
	sorted := slices.Clone(gaps)
	sort.Float64s(sorted)
	if len(sorted) > 0 {
		fmt.Fprintf(&b, "\nGaps: min %.2f, median %.2f, max %.2f, total %.2f s\n\n", sorted[0], sorted[len(sorted)/2], sorted[len(sorted)-1], sum(gaps))
	}
	b.WriteString("| who | lines | loudness spread (LU) | pitch spread (st, across lines) | rate mean ± sd (chars/s) |\n|---|---|---|---|---|\n")
	var whos []string
	for w := range per {
		whos = append(whos, w)
	}
	sort.Strings(whos)
	for _, w := range whos {
		s := per[w]
		var st []float64
		for _, f := range s.f0 {
			if f > 0 {
				st = append(st, 12*math.Log2(f/100))
			}
		}
		fmt.Fprintf(&b, "| %s | %d | %.1f | %.1f | %.1f ± %.1f |\n", w, len(s.lufs), spread(s.lufs), spread(st), mean(s.rate), sd(s.rate))
	}
	return b.String()
}

// abPitch is the median F0 of a mono buffer and its range (10th to 90th
// percentile, semitones), by normalised autocorrelation on 40 ms frames.
func abPitch(b dsp.Buffer) (float64, float64) {
	x := b.Samples
	rate := b.Rate
	// Down to 12 kHz by averaging pairs.
	if rate >= 24000 {
		y := make([]float32, len(x)/2)
		for i := range y {
			y[i] = (x[2*i] + x[2*i+1]) / 2
		}
		x, rate = y, rate/2
	}
	win, hop := rate*40/1000, rate*10/1000
	lo, hi := rate/500, rate/70
	var f0s []float64
	for s := 0; s+win+hi < len(x); s += hop {
		var e float64
		for _, v := range x[s : s+win] {
			e += float64(v) * float64(v)
		}
		if e/float64(win) < 1e-4 {
			continue
		}
		best, bestLag := 0.0, 0
		for lag := lo; lag <= hi; lag++ {
			var c, e2 float64
			for i := 0; i < win; i++ {
				c += float64(x[s+i]) * float64(x[s+i+lag])
				e2 += float64(x[s+i+lag]) * float64(x[s+i+lag])
			}
			if n := c / math.Sqrt(e*e2+1e-12); n > best {
				best, bestLag = n, lag
			}
		}
		if best > 0.6 && bestLag > 0 {
			f0s = append(f0s, float64(rate)/float64(bestLag))
		}
	}
	if len(f0s) < 3 {
		return 0, 0
	}
	sort.Float64s(f0s)
	p := func(q float64) float64 { return f0s[int(q*float64(len(f0s)-1))] }
	return p(0.5), 12 * math.Log2(p(0.9)/p(0.1))
}

func sum(v []float64) float64 {
	s := 0.0
	for _, x := range v {
		s += x
	}
	return s
}

func mean(v []float64) float64 {
	if len(v) == 0 {
		return 0
	}
	return sum(v) / float64(len(v))
}

func sd(v []float64) float64 {
	if len(v) < 2 {
		return 0
	}
	m, s := mean(v), 0.0
	for _, x := range v {
		s += (x - m) * (x - m)
	}
	return math.Sqrt(s / float64(len(v)-1))
}

func spread(v []float64) float64 {
	if len(v) == 0 {
		return 0
	}
	return slices.Max(v) - slices.Min(v)
}
