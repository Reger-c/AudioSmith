package main

// TestBookDialogueE2E renders a short audiobook with dialogue passages on
// the real engine through the page's pipeline (thread #88, plan §4.2 P4):
// two chapters, one passage each, the narration and the passages' fallback
// on Qwen3-TTS 0.6B CustomVoice, the passages on SoulX-Podcast; an M4B
// with chapters. It measures each line (Qwen3-ASR's error rate), the
// loudness of the passages' lines against the narration's, the sidecar's
// places and the M4B's chapters (ffprobe, when AUDIOSMITH_FFPROBE or PATH
// has one), and renders chapter 1 again line by line (the dialogue model
// off) to compare. Everything goes to AUDIOSMITH_BOOKDLG_OUT:
//
//	AUDIOSMITH_BOOKDLG_OUT=/tmp/bd go test -run TestBookDialogueE2E -v -timeout 1h .

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hehehai/AudioSmith/internal/catalog"
	"github.com/hehehai/AudioSmith/internal/core"
	"github.com/hehehai/AudioSmith/internal/dsp"
	"github.com/hehehai/AudioSmith/internal/dsp/loudness"
	"github.com/hehehai/AudioSmith/internal/dsp/wav"
	"github.com/hehehai/AudioSmith/internal/dub"
	"github.com/hehehai/AudioSmith/internal/engine"
	"github.com/hehehai/AudioSmith/internal/engine/proto"
	"github.com/hehehai/AudioSmith/internal/services"
)

const e2eBook = `# 第一章 渡口

雾还没有散，渡口只有一盏灯。一个女人提着箱子，沿着石阶走到水边。

[对话]
伊内丝：请问，这是去哈罗角的船吗？
托马斯：是的，今晚只有这一班。
他把缆绳抛上岸，指了指灯下的长椅。
托马斯（低声）：随便坐吧，要两个钟头才到。
伊内丝：两个钟头？地图上看着挺近的。
（远处传来汽笛声。）
船长：地图总是骗人的。坐稳了，开船了！
[/对话]

她选了灯下的长椅，看着小镇的灯一盏一盏熄灭。

# 第二章 小岛

天亮的时候，雾终于散了。小岛比地图上画的还要小。

[对话]
伊内丝：我们已经到了吗？我好像睡了一路。
托马斯：大家都这样。下船的时候，小心脚下的台阶。
[/对话]

她提起箱子，慢慢走上了码头。`

// bdLine is a line of the manifest.
type bdLine struct {
	Chapter int     `json:"chapter"`
	N       int     `json:"n"`
	Block   int     `json:"block,omitempty"`
	Who     string  `json:"who,omitempty"`
	Text    string  `json:"text"`
	Start   float64 `json:"start"`
	End     float64 `json:"end"`
	LUFS    float64 `json:"lufs"`
	Heard   string  `json:"heard"`
	CER     float64 `json:"cer"`
}

func TestBookDialogueE2E(t *testing.T) {
	out := os.Getenv("AUDIOSMITH_BOOKDLG_OUT")
	if out == "" {
		t.Skip("set AUDIOSMITH_BOOKDLG_OUT")
	}
	os.MkdirAll(out, 0o755)
	bin := engine.Find("resources")
	if bin == "" {
		t.Fatal("no engine: set AUDIOSMITH_ENGINE")
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
	s := readSysInfo()
	a.sys = &s
	ctx := context.Background()

	cv, _ := catalog.Builtin().ByID("qwen3-tts-0.6b-customvoice-8bit")
	soulx, _ := dialogueByName("soulx")
	p := a.lfBook
	d := services.NewLongformDoc(core.ProjectAudiobook, cv.Repo)
	d.Title, d.Language, d.Seed, d.Format = "渡口", "zh", 20261018, services.FormatM4B
	d.Voice = services.SpeakerRef + "Eric"
	d.VoiceCast = map[string]string{"伊内丝": services.SpeakerRef + "Serena", "托马斯": services.SpeakerRef + "Uncle_Fu"} // 船长: given at render
	d.Book.Loudness, d.Book.Author = services.LoudnessACX, "AudioSmith"
	d.Production.Dialogue, d.Production.DialogueModel = true, soulx.Repo
	d.Script = e2eBook
	a.setLfDoc(p, d, "", "")
	a.compileLf(p)
	if len(p.blocks) != 2 {
		t.Fatalf("blocks %+v", p.blocks)
	}

	render := func(name string, spec func()) (*lfRender, time.Duration) {
		t.Helper()
		spec()
		started := time.Now()
		p.render = ""
		a.startLf(p, "")
		waitFor(name+" start", time.Minute, func() bool { return p.render != "" })
		id := p.render
		waitFor(name, time.Hour, func() bool {
			r := a.lfRenders[id]
			return r != nil && r.rec.State != core.RenderRunning && !r.body.Assembling
		})
		rec, body, err := a.lib.svc.Longform.Get(ctx, id)
		if err != nil || rec.State != core.RenderDone || body.Output == nil {
			t.Fatalf("%s: %s %+v %v", name, rec.State, body.Error, err)
		}
		return &lfRender{rec: rec, body: body}, time.Since(started)
	}
	r, took := render("book", func() {})
	if c := p.doc.VoiceCast["船长"]; c == "" {
		t.Errorf("船长 has no voice: %+v", p.doc.VoiceCast)
	}
	spec := p.spec
	o := r.body.Output
	if o.Encoded == "" {
		t.Errorf("no M4B: %s", o.EncodeError)
	}
	note := []string{fmt.Sprintf("book: %d chapters, %.1f s audio in %.0f s; voices %v", len(o.Chapters), o.Seconds, took.Seconds(), p.doc.VoiceCast)}

	asr, _ := catalog.Builtin().ByID(catalog.DefaultSTT)
	asrSnap, err := a.lib.modelSnapshot(ctx, asr.Repo)
	if err != nil {
		t.Fatalf("ASR: %v", err)
	}
	fold := strings.NewReplacer("她", "他", "它", "他")
	var lines []bdLine
	measure := func(ci int, file string, res services.ChapterResult, chLines []services.LineSpec, dir string) []bdLine {
		buf, _, err := wav.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		var outL []bdLine
		prevEnd := 0.0
		for i, lp := range res.Lines {
			l := chLines[i]
			if lp.Start < prevEnd || lp.End <= lp.Start || lp.End > buf.Seconds() {
				t.Errorf("chapter %d line %d placed %.2f–%.2f after %.2f (%.2f s)", ci+1, i+1, lp.Start, lp.End, prevEnd, buf.Seconds())
			}
			prevEnd = lp.End
			seg := buf.Slice(dsp.FramesAt(lp.Start, buf.Rate), min(dsp.FramesAt(lp.End, buf.Rate), buf.Frames()))
			f := filepath.Join(dir, fmt.Sprintf("ch%d-line-%02d.wav", ci+1, i+1))
			wav.WriteFile(f, seg, wav.PCM16, wav.Options{})
			raw, err := h.sup.Call(ctx, proto.RoleGPU, core.NewID(time.Now()), proto.MethodSTTTranscribe,
				proto.TranscribeParams{Model: asr.Repo, ModelPath: asrSnap, Audio: f, Language: "zh"}, nil)
			var tr proto.TranscribeResult
			if err == nil {
				json.Unmarshal(raw, &tr)
			}
			text := ""
			for _, sp := range l.Spans {
				text += stripTagsE2E(sp.Text)
			}
			outL = append(outL, bdLine{Chapter: ci + 1, N: i + 1, Block: l.Block, Who: l.Character, Text: text, Start: round2(lp.Start), End: round2(lp.End),
				LUFS: round2(loudness.Integrated(seg)), Heard: tr.Text, CER: round3(dub.Drift(fold.Replace(text), fold.Replace(tr.Text)))})
			os.Remove(f)
		}
		return outL
	}
	tmp := t.TempDir()
	for ci, ch := range o.Chapters {
		res, err := services.ReadChapter(r.body.Chapters[ci].File)
		if err != nil {
			t.Fatal(err)
		}
		note = append(note, fmt.Sprintf("chapter %d: %d dialogue segments, %d lines from SoulX (redone %d, failed %d), %.1f LUFS, levelled %d",
			ci+1, res.Dialogues, res.DialogueLines, res.DialogueRedone, res.DialogueFailed, ch.Report.Integrated, res.Levelled))
		if res.Dialogues != 1 {
			t.Errorf("chapter %d: %d dialogue segments", ci+1, res.Dialogues)
		}
		lines = append(lines, measure(ci, r.body.Chapters[ci].File, res, spec.Chapters[ci].Lines, tmp)...)
		bdCopy(t, r.body.Chapters[ci].File, filepath.Join(out, fmt.Sprintf("chapter-%d.wav", ci+1)))
	}
	if o.Encoded != "" {
		bdCopy(t, o.Encoded, filepath.Join(out, "book.m4b"))
	}
	// Error rates and loudness: the passages' turns against the rest.
	var cer, dl, nl []float64
	for _, l := range lines {
		cer = append(cer, l.CER)
		if l.Block > 0 && l.Who != "" {
			dl = append(dl, l.LUFS)
		} else {
			nl = append(nl, l.LUFS)
		}
	}
	gap := math.Abs(mean(dl) - mean(nl))
	note = append(note, fmt.Sprintf("mean CER %.3f (max %.3f); turns %.1f LUFS vs the rest %.1f: %.2f LU apart", mean(cer), maxOf(cer), mean(dl), mean(nl), gap))
	if gap > 1.5 {
		t.Errorf("turns %.2f LU from the narration", gap)
	}
	if mean(cer) > 0.05 {
		t.Errorf("mean CER %.3f", mean(cer))
	}
	// The M4B's chapters.
	fp := os.Getenv("AUDIOSMITH_FFPROBE")
	if fp == "" {
		fp = lookTool("ffprobe")
	}
	if fp != "" && o.Encoded != "" {
		raw, err := exec.Command(fp, "-v", "error", "-of", "json", "-show_chapters", "-show_format", o.Encoded).Output()
		var m struct {
			Chapters []struct {
				Start string            `json:"start_time"`
				End   string            `json:"end_time"`
				Tags  map[string]string `json:"tags"`
			} `json:"chapters"`
			Format struct {
				Duration string `json:"duration"`
			} `json:"format"`
		}
		json.Unmarshal(raw, &m)
		var chs []string
		for _, c := range m.Chapters {
			chs = append(chs, fmt.Sprintf("%s %s–%s", c.Tags["title"], c.Start, c.End))
		}
		note = append(note, fmt.Sprintf("M4B (ffprobe): %d chapters %v, %s s (%v)", len(m.Chapters), chs, m.Format.Duration, err))
		if len(m.Chapters) != 2 || m.Chapters[0].Tags["title"] != "第一章 渡口" {
			t.Errorf("M4B chapters %+v", m.Chapters)
		}
	}

	// Chapter 1 again, line by line (the dialogue model off), to compare.
	if os.Getenv("AUDIOSMITH_BOOKDLG_PERLINE") != "0" {
		p.doc.Production.Dialogue = false
		p.doc.Format = services.FormatWAV
		a.compileLf(p)
		saved := p.spec
		a.startLf(p, "chapter:0")
		var aid string
		waitFor("per-line start", time.Minute, func() bool { aid = p.auditions["chapter:0"]; return aid != "" })
		waitFor("per-line", time.Hour, func() bool { r := a.lfRenders[aid]; return r != nil && r.rec.State != core.RenderRunning })
		_, body, err := a.lib.svc.Longform.Get(ctx, aid)
		if err != nil || len(body.Chapters) == 0 || body.Chapters[0].File == "" {
			t.Fatalf("per line: %+v %v", body.Chapters, err)
		}
		res, _ := services.ReadChapter(body.Chapters[0].File)
		pl := measure(0, body.Chapters[0].File, res, saved.Chapters[0].Lines, tmp)
		var pc []float64
		for _, l := range pl {
			pc = append(pc, l.CER)
		}
		note = append(note, fmt.Sprintf("chapter 1 line by line: %.1f s, mean CER %.3f, %d dialogue segments", res.AudioSeconds, mean(pc), res.Dialogues))
		bdCopy(t, body.Chapters[0].File, filepath.Join(out, "chapter-1-per-line.wav"))
	}
	b, _ := json.MarshalIndent(map[string]any{"notes": note, "lines": lines}, "", " ")
	os.WriteFile(filepath.Join(out, "manifest.json"), b, 0o644)
	for _, n := range note {
		t.Log(n)
	}
}

func stripTagsE2E(s string) string {
	var b strings.Builder
	depth := 0
	for _, r := range s {
		switch {
		case r == '[':
			depth++
		case r == ']' && depth > 0:
			depth--
		case depth == 0:
			b.WriteRune(r)
		}
	}
	return b.String()
}

func round3(v float64) float64 { return math.Round(v*1000) / 1000 }

func maxOf(v []float64) float64 {
	m := 0.0
	for _, x := range v {
		m = max(m, x)
	}
	return m
}

func bdCopy(t *testing.T, from, to string) {
	t.Helper()
	data, err := os.ReadFile(from)
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(to, data, 0o644)
}
