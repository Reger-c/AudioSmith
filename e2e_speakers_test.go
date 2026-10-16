package main

import (
	"context"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode"

	"github.com/hehehai/AudioSmith/internal/catalog"
	"github.com/hehehai/AudioSmith/internal/core"
	"github.com/hehehai/AudioSmith/internal/dsp"
	"github.com/hehehai/AudioSmith/internal/dsp/resample"
	"github.com/hehehai/AudioSmith/internal/dsp/wav"
	"github.com/hehehai/AudioSmith/internal/dub"
	"github.com/hehehai/AudioSmith/internal/engine"
	"github.com/hehehai/AudioSmith/internal/engine/proto"
	"github.com/hehehai/AudioSmith/internal/jobs"
	"github.com/hehehai/AudioSmith/internal/services"
	"github.com/hehehai/AudioSmith/internal/settings"
)

// TestSpeakersE2E evaluates transcription with speakers (thread #48) on
// a Mac, through the app: dialogues of 2, 3 and 4 speakers made with
// Qwen3-TTS CustomVoice 0.6B (built-in voices, known lines and turns) and
// a 12-minute 5-speaker one (two windows), transcribed
//
//   - moss: Transcribe with Speakers on (MOSS-Transcribe-Diarize + the
//     forced aligner, services.TranscribeSpeakers);
//   - base: what the dub did before: Qwen3-ASR + the aligner, then
//     Sortformer diarize.run and dub.AssignSpeakers;
//   - dub:  a dub project through its transcript stage (the speakers
//     model, as dubbing now uses it);
//
// scoring each line's speaker (labels mapped one to one onto the truth),
// the speakers found, CER (Chinese) or WER (English), time and memory.
// AUDIOSMITH_SPK picks steps (make,moss,base,dub; default all),
// AUDIOSMITH_SPK_ONLY dialogues, AUDIOSMITH_SPK_DIR keeps the material
// and the report (report.md, rows.jsonl, transcripts/).
//
//	AUDIOSMITH_E2E=1 AUDIOSMITH_SPK_DIR=/tmp/as-moss/eval go test -run TestSpeakersE2E -v -timeout 3h .
func TestSpeakersE2E(t *testing.T) {
	if os.Getenv("AUDIOSMITH_E2E") == "" {
		t.Skip("set AUDIOSMITH_E2E=1 to run the real engine")
	}
	bin := engine.Find("resources")
	if bin == "" {
		t.Fatal("no engine: run scripts/build-engine.sh")
	}
	want := map[string]bool{}
	for _, s := range strings.Split(os.Getenv("AUDIOSMITH_SPK"), ",") {
		if s = strings.TrimSpace(s); s != "" {
			want[s] = true
		}
	}
	on := func(step string) bool { return len(want) == 0 || want[step] }
	var only []string
	if v := os.Getenv("AUDIOSMITH_SPK_ONLY"); v != "" {
		only = strings.Split(v, ",")
	}
	dir := os.Getenv("AUDIOSMITH_SPK_DIR")
	if dir == "" {
		dir = t.TempDir()
	}
	os.MkdirAll(filepath.Join(dir, "transcripts"), 0o755)
	data := filepath.Join(dir, "data")
	lib := openTestLibraryWith(t, data, libraryOptions{})
	h := &engineHost{bin: bin, logDir: filepath.Join(dir, "logs")}
	os.MkdirAll(h.logDir, 0o755)
	a := newApp(h, lib)
	a.autoplay = false
	v := &v02{a: a, h: h, events: make(chan func(), 1<<16), dir: dir, mem: newMemSampler()}
	defer v.mem.stop()
	a.enginePost = func(fn func()) { v.events <- fn }
	a.postQueue = a.enginePost
	h.sup = engine.NewSupervisor(engine.Config{Bin: bin, Bus: a.bus, LogDir: h.logDir, Offline: true, CacheDir: modelCache(a.prefs),
		OnDemand: []proto.Role{proto.RoleMedia}, StatsEvery: time.Second, Logf: t.Logf})
	a.connectEngine(h)
	defer h.Stop()
	a.startEngine()
	v.wait(t, "engine ready", time.Minute, func() bool { return a.engState == engineReady })
	a.refreshModels()
	v.drain()
	acceptLicences(t, a)
	ctx := context.Background()
	run := func(method string, params, out any) error {
		raw, _ := json.Marshal(params)
		res, err := engine.KindExecutor(h.sup, method).Run(ctx, jobs.Job{ID: core.NewID(time.Now()), Spec: jobs.Spec{Kind: method, Params: raw}}, func(jobs.Progress) {}, nil)
		if err == nil && out != nil {
			err = json.Unmarshal(res, out)
		}
		return err
	}
	snapshot := func(id string) catalog.Entry {
		e := v.model(t, id)
		return e
	}
	// fresh restarts the gpu worker: one model at a time, MLX's peak from 0.
	fresh := func() {
		h.sup.Restart(proto.RoleGPU)
		time.Sleep(time.Second)
		v.wait(t, "engine ready", time.Minute, func() bool { return a.engState == engineReady })
		v.mem.reset()
	}

	dialogues := spkDialogues()
	var rows []spkRow
	report := func() {
		var b strings.Builder
		fmt.Fprintf(&b, "# Transcription with speakers (thread #48), %s\n\nengine %s\n\n", time.Now().Format(time.RFC3339), a.engVersion)
		b.WriteString("| dialogue | system | audio | speakers (true / found) | lines right | CER/WER | time | ×RT | MLX peak | RSS peak |\n|---|---|---|---|---|---|---|---|---|---|\n")
		for _, r := range rows {
			fmt.Fprintf(&b, "| %s | %s | %.0f s | %d / %d | %.2f (%d/%d) | %.1f %% | %.0f s | %.1f | %.2f GB | %.2f GB |\n", r.Dialogue, r.System, r.Audio,
				r.True, r.Found, r.Attribution, r.Right, r.Lines, 100*r.ErrorRate, r.Seconds, r.Audio/max(r.Seconds, 0.01), r.MLXPeak, r.RSSPeak)
		}
		os.WriteFile(filepath.Join(dir, "report.md"), []byte(b.String()), 0o644)
		t.Logf("\n%s", b.String())
	}
	defer report()
	addRow := func(r spkRow) {
		rows = append(rows, r)
		f, _ := os.OpenFile(filepath.Join(dir, "rows.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
		json.NewEncoder(f).Encode(r)
		f.Close()
		report()
	}

	// make: synthesize the lines (cached per voice and text) and lay the
	// dialogues out with pauses.
	if on("make") {
		cv := snapshot("qwen3-tts-0.6b-customvoice-8bit")
		snap, err := lib.modelSnapshot(ctx, cv.Repo)
		if err != nil {
			t.Fatal(err)
		}
		clips := filepath.Join(dir, "clips")
		os.MkdirAll(clips, 0o755)
		for _, d := range dialogues {
			if len(only) > 0 && !slices.Contains(only, d.Name) {
				continue
			}
			if _, err := os.Stat(filepath.Join(dir, d.Name+".wav")); err == nil {
				continue
			}
			langValue := map[string]string{"zh": "Chinese", "en": "English"}[d.Lang]
			var mix []float32
			var truth []spkTurn
			at := 0.4
			for i, l := range d.Lines {
				voice := d.Voices[l.Who]
				hsh := fnv.New32a()
				hsh.Write([]byte(voice + "|" + l.Text))
				clip := filepath.Join(clips, fmt.Sprintf("%s-%08x.wav", voice, hsh.Sum32()))
				if _, err := os.Stat(clip); err != nil {
					seed := int64(7)
					temp := 0.7
					err := run(proto.MethodTTSSynthesize, proto.SynthesizeParams{Model: cv.Repo, ModelPath: snap, Text: l.Text, Voice: voice, Language: langValue,
						Seed: &seed, Temperature: &temp, Output: proto.Output{Path: clip, Format: "wav", Bits: 16}}, nil)
					if err != nil {
						t.Fatalf("%s line %d: %v", d.Name, i, err)
					}
				}
				b, _, err := wav.ReadFile(clip)
				if err != nil {
					t.Fatal(err)
				}
				b = resample.Convert(b, 16000, 1, resample.Good)
				s, e := trimSilence(b)
				b = b.Slice(s, e)
				gap := 0.35 + 0.5*math.Abs(math.Sin(float64(i)*1.7))
				if i == 0 {
					gap = 0.4
				}
				for len(mix) < int((at+gap)*16000) {
					mix = append(mix, 0)
				}
				start := float64(len(mix)) / 16000
				mix = append(mix, b.Samples...)
				end := float64(len(mix)) / 16000
				truth = append(truth, spkTurn{Start: start, End: end, Who: l.Who, Text: l.Text})
				at = end
			}
			mix = append(mix, make([]float32, 8000)...)
			// A little room noise, as a recording has.
			for i := range mix {
				mix[i] += float32(0.002 * math.Sin(float64(i)*0.37) * math.Sin(float64(i)*0.0011))
			}
			out := dsp.Buffer{Rate: 16000, Channels: 1, Samples: mix}
			if err := wav.WriteFile(filepath.Join(dir, d.Name+".wav"), out, wav.PCM16, wav.Options{}); err != nil {
				t.Fatal(err)
			}
			tj, _ := json.MarshalIndent(truth, "", " ")
			os.WriteFile(filepath.Join(dir, d.Name+".truth.json"), tj, 0o644)
			t.Logf("%s: %.0f s, %d lines", d.Name, out.Seconds(), len(truth))
		}
		h.sup.Restart(proto.RoleGPU) // unload the TTS model
	}

	load := func(d spkDialogue) (string, []spkTurn, bool) {
		path := filepath.Join(dir, d.Name+".wav")
		var truth []spkTurn
		b, err := os.ReadFile(filepath.Join(dir, d.Name+".truth.json"))
		if err != nil || json.Unmarshal(b, &truth) != nil {
			return "", nil, false
		}
		return path, truth, true
	}
	transcribe := func(path string, speakers bool) core.Transcript {
		t.Helper()
		a.updateSettings(func(s *settings.Settings) { s.Transcribe.Speakers = speakers })
		a.trMode = 1
		a.transcribeFile(path)
		if a.trJob == nil {
			t.Fatalf("transcription did not start: %q", a.trErr)
		}
		v.wait(t, "transcription", 60*time.Minute, func() bool { return a.trJob == nil })
		if a.trErr != "" {
			t.Fatalf("transcription: %s", a.trErr)
		}
		return *a.trDetail
	}
	for _, d := range dialogues {
		if len(only) > 0 && !slices.Contains(only, d.Name) {
			continue
		}
		path, truth, ok := load(d)
		if !ok {
			t.Logf("%s: no material (run step make)", d.Name)
			continue
		}
		audio := truth[len(truth)-1].End + 0.5
		people := map[string]bool{}
		for _, x := range truth {
			people[x.Who] = true
		}
		if on("moss") {
			fresh()
			started := time.Now()
			tr := transcribe(path, true)
			r := scoreSpeakers(d, truth, trSegs(tr))
			r.System, r.Seconds, r.Audio, r.True = "MOSS + aligner (Transcribe, Speakers)", time.Since(started).Seconds(), audio, len(people)
			v.drain()
			r.MLXPeak, r.RSSPeak = v.mlxPeak(), v.mem.peak()
			addRow(r)
			dumpTranscript(dir, d.Name+"-moss", tr)
		}
		if on("base") {
			fresh()
			started := time.Now()
			tr := transcribe(path, false)
			diar := v.model(t, catalog.DefaultDiarize)
			snap, _ := lib.modelSnapshot(ctx, diar.Repo)
			var dr dub.DiarizeResult
			if err := run(dub.MethodDiarize, dub.DiarizeParams{Model: diar.Repo, ModelPath: snap, Audio: lib.store.Transcripts().AudioPath(tr)}, &dr); err != nil {
				t.Fatalf("diarize: %v", err)
			}
			p := dub.New("", time.Now())
			for _, s := range tr.Segments {
				seg := p.NewSegment(s.Start, s.End, "", s.Text)
				for _, w := range s.Words {
					seg.Words = append(seg.Words, dub.Word{Text: w.Text, Start: w.Start, End: w.End})
				}
				p.Segments = append(p.Segments, seg)
			}
			p.AssignSpeakers(dr.Turns)
			var segs []spkSeg
			for _, s := range p.Segments {
				segs = append(segs, spkSeg{s.Start, s.End, s.Speaker, s.Text})
			}
			r := scoreSpeakers(d, truth, segs)
			r.System, r.Seconds, r.Audio, r.True = "Qwen3-ASR + aligner + Sortformer (dub before)", time.Since(started).Seconds(), audio, len(people)
			v.drain()
			r.MLXPeak, r.RSSPeak = v.mlxPeak(), v.mem.peak()
			addRow(r)
		}
		if on("dub") && !d.Long {
			fresh()
			n := len(a.dubList)
			a.updateSettings(func(s *settings.Settings) { s.Dubbing.Targets, s.Dubbing.NoReview = []string{"en"}, true })
			a.newDub(dub.Source{Kind: dub.SourceFile, Path: path, Name: filepath.Base(path)})
			v.wait(t, "dub created", time.Minute, func() bool { return a.dub != nil && len(a.dubList) > n })
			id := a.dub.ID
			started := time.Now()
			ids, err := a.dubSvc.Run(ctx, id, services.RunRequest{Through: dub.StageReferences})
			if err != nil {
				t.Fatal(err)
			}
			for st, jid := range ids {
				if j := v.job(t, jid, 60*time.Minute); j.State != jobs.StateSucceeded {
					t.Errorf("%s stage %s: %s %+v", d.Name, st, j.State, j.Err)
				}
			}
			p, _ := a.dubSvc.Get(ctx, id)
			var segs []spkSeg
			for _, s := range p.Segments {
				segs = append(segs, spkSeg{s.Start, s.End, s.Speaker, s.Text})
			}
			r := scoreSpeakers(d, truth, segs)
			r.System, r.Seconds, r.Audio, r.True = "dub source analysis (separate + MOSS + aligner)", time.Since(started).Seconds(), audio, len(people)
			r.Note = strings.Join(p.Stage(dub.StageTranscribe, "").Warnings, ",")
			v.drain()
			r.MLXPeak, r.RSSPeak = v.mlxPeak(), v.mem.peak()
			addRow(r)
		}
	}
	// The Infinite Zoom dub source (one speaker): speakers found and the
	// text against Qwen3-ASR's.
	if iz := os.Getenv("AUDIOSMITH_SPK_IZ"); iz != "" && on("iz") {
		fresh()
		started := time.Now()
		m := transcribe(iz, true)
		took := time.Since(started).Seconds()
		v.drain()
		mlx, rss := v.mlxPeak(), v.mem.peak()
		fresh()
		q := transcribe(iz, false)
		labels := map[string]bool{}
		for _, s := range m.Segments {
			labels[s.Speaker] = true
		}
		r := spkRow{Dialogue: "infinite-zoom", System: "MOSS (vs Qwen3-ASR text)", Audio: m.Duration, True: 1, Found: len(labels),
			ErrorRate: errorRate(q.Text(), m.Text(), "en"), Seconds: took, MLXPeak: mlx, RSSPeak: rss, Lines: len(m.Segments)}
		addRow(r)
		dumpTranscript(dir, "infinite-zoom-moss", m)
		dumpTranscript(dir, "infinite-zoom-qwen", q)
	}
}

// spkDialogue is a scripted conversation: who says each line, and the
// built-in voice of each speaker.
type spkDialogue struct {
	Name, Lang string
	Voices     map[string]string
	Lines      []spkLine
	Long       bool
}

type spkLine struct{ Who, Text string }

type spkTurn struct {
	Start, End float64
	Who, Text  string
}

type spkSeg struct {
	Start, End float64
	Who, Text  string
}

type spkRow struct {
	Dialogue, System string
	Audio            float64
	True, Found      int
	Lines, Right     int
	Attribution      float64
	ErrorRate        float64
	Seconds          float64
	MLXPeak, RSSPeak float64
	Note             string `json:",omitempty"`
}

func trSegs(t core.Transcript) []spkSeg {
	var out []spkSeg
	for _, s := range t.Segments {
		out = append(out, spkSeg{s.Start, s.End, s.Speaker, s.Text})
	}
	return out
}

func dumpTranscript(dir, name string, t core.Transcript) {
	os.WriteFile(filepath.Join(dir, "transcripts", name+".txt"), []byte(services.PlainText(t)+"\n"), 0o644)
	var b strings.Builder
	for _, s := range t.Segments {
		fmt.Fprintf(&b, "%7.2f %7.2f %-3s %s\n", s.Start, s.End, s.Speaker, s.Text)
	}
	os.WriteFile(filepath.Join(dir, "transcripts", name+".segments.txt"), []byte(b.String()), 0o644)
}

// scoreSpeakers gives each true line the found speaker that overlaps it
// most, maps found labels one to one onto people (the mapping with the
// most lines right), and counts the lines right; the text's error rate
// is over the whole transcript.
func scoreSpeakers(d spkDialogue, truth []spkTurn, segs []spkSeg) spkRow {
	r := spkRow{Dialogue: d.Name, Lines: len(truth)}
	var labels, people []string
	for _, s := range segs {
		if s.Who != "" && !slices.Contains(labels, s.Who) {
			labels = append(labels, s.Who)
		}
	}
	r.Found = len(labels)
	got := make([]string, len(truth))
	for i, x := range truth {
		ov := map[string]float64{}
		best, bo := "", 0.0
		for _, s := range segs {
			if o := min(x.End, s.End) - max(x.Start, s.Start); o > 0 && s.Who != "" {
				ov[s.Who] += o
				if ov[s.Who] > bo {
					best, bo = s.Who, ov[s.Who]
				}
			}
		}
		got[i] = best
		if !slices.Contains(people, x.Who) {
			people = append(people, x.Who)
		}
	}
	// The best one-to-one mapping (few labels: try them all).
	bestRight := 0
	var try func(i int, used map[string]bool, m map[string]string)
	try = func(i int, used map[string]bool, m map[string]string) {
		if i == len(labels) {
			n := 0
			for k, x := range truth {
				if got[k] != "" && m[got[k]] == x.Who {
					n++
				}
			}
			bestRight = max(bestRight, n)
			return
		}
		try(i+1, used, m) // this label maps to nobody
		for _, p := range people {
			if !used[p] {
				used[p], m[labels[i]] = true, p
				try(i+1, used, m)
				used[p] = false
				delete(m, labels[i])
			}
		}
	}
	if len(labels) <= 8 {
		try(0, map[string]bool{}, map[string]string{})
	}
	r.Right = bestRight
	r.Attribution = float64(bestRight) / float64(max(len(truth), 1))
	var want, have []string
	for _, x := range truth {
		want = append(want, x.Text)
	}
	for _, s := range segs {
		have = append(have, s.Text)
	}
	r.ErrorRate = errorRate(strings.Join(want, " "), strings.Join(have, " "), d.Lang)
	return r
}

// errorRate is the character (Chinese) or word (else) error rate of got
// against want, without punctuation, spaces (Chinese) or case.
func errorRate(want, got, lang string) float64 {
	norm := func(s string) []string {
		var out []string
		if lang == "zh" {
			for _, r := range strings.ToLower(s) {
				if unicode.IsLetter(r) || unicode.IsDigit(r) {
					out = append(out, string(r))
				}
			}
			return out
		}
		for _, w := range strings.Fields(strings.ToLower(s)) {
			w = strings.TrimFunc(w, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
			if w != "" {
				out = append(out, w)
			}
		}
		return out
	}
	a, b := norm(want), norm(got)
	if len(a) == 0 {
		return 0
	}
	prev := make([]int, len(b)+1)
	cur := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			c := 1
			if a[i-1] == b[j-1] {
				c = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+c)
		}
		prev, cur = cur, prev
	}
	return float64(prev[len(b)]) / float64(len(a))
}

// trimSilence is where a clip's sound starts and ends (frames).
func trimSilence(b dsp.Buffer) (int, int) {
	n := b.Frames()
	s, e := 0, n
	for s < n && math.Abs(float64(b.Samples[s])) < 0.01 {
		s++
	}
	for e > s && math.Abs(float64(b.Samples[e-1])) < 0.01 {
		e--
	}
	return max(s-800, 0), min(e+1600, n)
}

// spkDialogues are the scripted conversations.
func spkDialogues() []spkDialogue {
	zh2 := spkDialogue{Name: "zh2-adopt-cat", Lang: "zh", Voices: map[string]string{"A": "Vivian", "B": "Serena"}, Lines: []spkLine{
		{"A", "周末我去了一趟动物救助站，想领养一只小猫。"},
		{"B", "真的吗？你不是一直说家里地方太小吗？"},
		{"A", "是啊，可是看到那些小家伙，我实在忍不住。"},
		{"B", "那你挑中哪一只了？"},
		{"A", "一只三个月大的橘猫，特别黏人，一直蹭我的手。"},
		{"B", "橘猫很能吃的，你要做好准备。"},
		{"A", "救助站的人也这么说，还给了我一张喂食的表格。"},
		{"B", "疫苗和驱虫都做过了吗？"},
		{"A", "第一针疫苗打过了，下个月还要再打一针。"},
		{"B", "那你得先买好猫砂盆和猫抓板。"},
		{"A", "我昨天已经在网上下单了，明天就能到。"},
		{"B", "好吧，等它到家了，我一定要去看看。"},
	}}
	zh3 := spkDialogue{Name: "zh3-meeting", Lang: "zh", Voices: map[string]string{"A": "Vivian", "B": "Uncle_Fu", "C": "Dylan"}, Lines: []spkLine{
		{"A", "我们开始今天的周会吧，先说一下配音项目的进度。"},
		{"B", "上周我们把三十分钟的样片全部转写完了，错误率不到百分之二。"},
		{"C", "翻译这边也差不多了，还剩最后两集没有校对。"},
		{"A", "校对大概需要几天？"},
		{"C", "如果不加新的术语，三天之内可以完成。"},
		{"B", "我建议先把术语表定下来，不然后面还要返工。"},
		{"A", "同意。术语表谁来负责？"},
		{"C", "我来吧，今天下班前发给大家。"},
		{"B", "另外，配音的音色还没有最终确认，客户想再听几个版本。"},
		{"A", "那就周四之前准备三个版本给客户挑。"},
		{"C", "没问题，我会把每个版本的时长也标出来。"},
		{"B", "还有字幕的格式，客户要求每行不超过十六个字。"},
		{"A", "好，这个记下来。今天就先到这里，大家辛苦了。"},
	}}
	zh4 := spkDialogue{Name: "zh4-trip", Lang: "zh", Voices: map[string]string{"A": "Vivian", "B": "Serena", "C": "Uncle_Fu", "D": "Eric"}, Lines: []spkLine{
		{"A", "今年国庆我们去哪儿玩？我想听听大家的意见。"},
		{"B", "我想去云南，听说大理的天气特别好。"},
		{"C", "云南太远了，坐飞机要四个多小时，我年纪大了吃不消。"},
		{"D", "爷爷，我们可以坐高铁到昆明，中间还能休息。"},
		{"A", "高铁也要十个小时吧，时间是不是太长了？"},
		{"B", "那换个近一点的地方，比如杭州或者苏州。"},
		{"C", "苏州好，我一直想去看看那些园林。"},
		{"D", "可是国庆期间苏州人特别多，到处都要排队。"},
		{"A", "我们可以早上七点就出门，避开人流高峰。"},
		{"B", "住的地方要提前订，晚了就没有房间了。"},
		{"C", "酒店不用太贵，干净安静就行。"},
		{"D", "我在网上看到一家民宿，就在平江路旁边，评价很好。"},
		{"A", "价格怎么样？能住下我们四个人吗？"},
		{"D", "两间房，一晚上六百块，还包早餐。"},
		{"B", "挺划算的，那就订三个晚上吧。"},
		{"C", "我想再去一趟寒山寺，听听那里的钟声。"},
		{"A", "好，第二天上午去寒山寺，下午逛拙政园。"},
		{"D", "第三天我们去吃苏州的面条，听说特别有名。"},
		{"B", "别忘了给邻居带点特产回来。"},
		{"C", "那就这么定了，大家都早点准备。"},
	}}
	en4 := spkDialogue{Name: "en4-launch", Lang: "en", Voices: map[string]string{"A": "Ryan", "B": "Aiden", "C": "Serena", "D": "Eric"}, Lines: []spkLine{
		{"A", "Thanks for joining, everyone. Let's review the launch checklist."},
		{"B", "The installer is signed and the download page is ready."},
		{"C", "I still need final screenshots for the website."},
		{"D", "I can take them this afternoon on the new laptop."},
		{"A", "Great. What about the release notes?"},
		{"B", "They're drafted, but I'd like someone to proofread them."},
		{"C", "Send them to me, I'll go through them after lunch."},
		{"D", "Should we mention the new transcription feature in the headline?"},
		{"A", "Yes, it's the biggest change in this version."},
		{"C", "Then we need a short demo video as well."},
		{"D", "I'll record a thirty second clip with two people talking."},
		{"B", "Keep it simple, people only watch the first ten seconds."},
		{"A", "Agreed. Any blockers before Friday?"},
		{"C", "Only the translation of the help pages, which is almost done."},
		{"D", "And I'm waiting for the final logo files from the designer."},
		{"A", "Let's check in again on Thursday morning."},
	}}
	// About twelve minutes, five people: the three Chinese dialogues four
	// times over with their own voices (Vivian and Serena in two of them,
	// Uncle_Fu, Dylan, Eric), every other time in reverse order: two
	// windows of the speakers model, and more people than Sortformer's 4.
	long := spkDialogue{Name: "zh5-long", Lang: "zh", Long: true, Voices: map[string]string{}}
	for cycle := range 4 {
		for _, d := range []spkDialogue{zh4, zh3, zh2} {
			lines := slices.Clone(d.Lines)
			if cycle%2 == 1 {
				slices.Reverse(lines)
			}
			for _, l := range lines {
				who := d.Voices[l.Who] // a person is a voice
				long.Voices[who] = who
				long.Lines = append(long.Lines, spkLine{who, l.Text})
			}
		}
	}
	return []spkDialogue{zh2, zh3, zh4, en4, long}
}
