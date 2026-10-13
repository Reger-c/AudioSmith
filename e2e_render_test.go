package main

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/hehehai/AudioSmith/internal/catalog"
	"github.com/hehehai/AudioSmith/internal/core"
	"github.com/hehehai/AudioSmith/internal/dsp/loudness"
	"github.com/hehehai/AudioSmith/internal/dsp/wav"
	"github.com/hehehai/AudioSmith/internal/engine"
	"github.com/hehehai/AudioSmith/internal/engine/proto"
	"github.com/hehehai/AudioSmith/internal/settings"
)

// TestLongRender renders long scripts through the Studio, the take.render
// job and the real engine, as a user would: 10,000 characters of English
// and of Chinese on Kokoro, a mixed Chinese/English script with [laughter]
// and a target duration on OmniVoice, and Kokoro with [slow]/[fast]. It
// checks that no chunk is refused (input_too_long), that the take is as
// long as its text, close to −18 LUFS ("Even out volume"), and reports
// chunks, speed and the worker's memory. The models must be downloaded
// (in the HF cache, or AUDIOSMITH_SMOKE_CACHE).
//
//	AUDIOSMITH_E2E=1 go test -run TestLongRender -v -timeout 2h .
func TestLongRender(t *testing.T) {
	if os.Getenv("AUDIOSMITH_E2E") == "" {
		t.Skip("set AUDIOSMITH_E2E=1 to run the real engine")
	}
	bin := engine.Find("resources")
	if bin == "" {
		t.Fatal("no engine: run scripts/build-engine.sh")
	}
	lib := openTestLibrary(t)
	if dir := os.Getenv("AUDIOSMITH_SMOKE_CACHE"); dir != "" {
		lib.settings.Update(t.Context(), func(s *settings.Settings) { s.Models.CacheDir = dir })
	}
	h := &engineHost{bin: bin, logDir: t.TempDir()}
	a := newApp(h, lib)
	a.autoplay = false
	events := make(chan func(), 1<<16)
	a.enginePost = func(fn func()) { events <- fn }
	a.postQueue = a.enginePost
	h.sup = engine.NewSupervisor(engine.Config{Bin: bin, Bus: a.bus, LogDir: h.logDir, Offline: true, CacheDir: modelCache(a.prefs),
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
			case <-deadline:
				t.Fatalf("timed out waiting for %s (job %+v, error %q)", what, a.job, a.jobError)
			}
		}
	}
	a.startEngine()
	waitFor("ready", time.Minute, func() bool { return a.engState == engineReady })

	render := func(name, modelID, voice, language, text string, duration float64) *core.Take {
		t.Helper()
		e, _ := catalog.Builtin().ByID(modelID)
		a.selectModel(e)
		a.text, a.voice, a.language, a.duration = text, voice, language, duration
		a.evenOut = true
		started := time.Now()
		a.generate()
		if a.job == nil {
			t.Fatalf("%s: did not start: %q", name, a.jobError)
		}
		waitFor(name, 90*time.Minute, func() bool { return a.job == nil && a.awaitTake == "" })
		if a.jobError != "" {
			t.Fatalf("%s: %s", name, a.jobError)
		}
		take := a.lastClip()
		if take == nil || take.Render == nil {
			t.Fatalf("%s: no take", name)
		}
		buf, _, err := wav.ReadFile(a.takeStore.AudioPath(*take))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		lufs := loudness.Integrated(buf)
		var peak float64
		for _, w := range a.workers {
			if w.Role == proto.RoleGPU && w.Stats != nil {
				peak = float64(w.Stats.Memory.Peak) / (1 << 30)
			}
		}
		var codes []string
		for _, n := range append(take.Render.Warnings, take.Render.Issues...) {
			codes = append(codes, fmt.Sprintf("%s@%d", n.Code, n.Chunk))
		}
		t.Logf("%s: %d chars → %.1f s of audio in %.1f s (%.1f× realtime, wall %.0f s), %d chunks, %.1f LUFS, MLX peak %.2f GB, notices %v",
			name, len([]rune(text)), take.AudioSeconds, take.GenSeconds, take.RTF(), time.Since(started).Seconds(),
			len(take.Render.Chunks), lufs, peak, codes)
		if lufs < -19.5 || lufs > -16.5 {
			t.Errorf("%s: %.1f LUFS, want about -18", name, lufs)
		}
		for _, n := range take.Render.Warnings {
			if strings.Contains(n.Code, "too_long") || strings.Contains(n.Code, "failed") {
				t.Errorf("%s: %+v", name, n)
			}
		}
		return take
	}

	models := os.Getenv("AUDIOSMITH_RENDER_MODELS")
	if models == "" {
		models = "kokoro,omnivoice"
	}
	if strings.Contains(models, "kokoro") {
		en := longText(enSentences, 10000)
		take := render("kokoro-en-10k", "kokoro-82m-bf16", "af_heart", "", en, 0)
		// About 14 characters a second of English speech.
		if want := float64(len(en)) / 14; take.AudioSeconds < want*0.6 || take.AudioSeconds > want*1.6 {
			t.Errorf("kokoro en: %.0f s of audio for %d characters", take.AudioSeconds, len(en))
		}
		zh := longText(zhSentences, 10000)
		take = render("kokoro-zh-10k", "kokoro-82m-bf16", "zf_xiaobei", "", zh, 0)
		if want := float64(len([]rune(zh))) / 4.5; take.AudioSeconds < want*0.6 || take.AudioSeconds > want*1.6 {
			t.Errorf("kokoro zh: %.0f s of audio for %d characters", take.AudioSeconds, len([]rune(zh)))
		}
		// Speed per chunk.
		take = render("kokoro-speed", "kokoro-82m-bf16", "af_heart", "",
			"This sentence is read at the normal pace. [slow] This one is read slowly, word by word. [fast] And this last one rushes past as fast as it can.", 0)
		for i, c := range take.Render.Chunks {
			t.Logf("  chunk %d: %.2f–%.2f s %q", i+1, c.Start, c.End, c.Text)
		}
	}
	if strings.Contains(models, "omnivoice") {
		mixed := longText(append(append([]string{}, zhSentences...), enSentences...), 3000)
		render("omnivoice-mixed-3k", "omnivoice-bf16", "", "", "[laughter] "+mixed, 0)
		take := render("omnivoice-duration", "omnivoice-bf16", "female, young adult", "",
			strings.Join(enSentences[:6], " "), 30)
		if take.AudioSeconds < 29 || take.AudioSeconds > 31.5 {
			t.Errorf("omnivoice duration 30: %.2f s", take.AudioSeconds)
		}
		for i, c := range take.Render.Chunks {
			t.Logf("  chunk %d: %.2f–%.2f s", i+1, c.Start, c.End)
		}
	}
}

// longText cycles through sentences, numbering paragraphs, to n characters.
func longText(sentences []string, n int) string {
	var b strings.Builder
	for i := 0; len([]rune(b.String())) < n; i++ {
		s := sentences[i%len(sentences)]
		b.WriteString(s)
		if i%5 == 4 {
			b.WriteString("\n\n")
		} else if strings.ContainsAny(s, "abcdefghijklmnopqrstuvwxyz") {
			b.WriteString(" ")
		}
	}
	return string([]rune(b.String())[:n])
}

var enSentences = []string{
	"The morning train left the station a few minutes late, as it often did in winter.",
	"Maria had packed a small bag, a notebook, and the letter she had never managed to send.",
	"Outside the window, the fields were white with frost, and the river moved slowly under a thin sheet of ice.",
	"She counted the stations: twelve stops, three hours and forty minutes, if nothing went wrong.",
	"A man across the aisle was reading a newspaper from the day before, folding each page with great care.",
	"When the conductor came by, he asked about the weather in the north, and nobody knew what to say.",
	"At noon the sun finally broke through, and the whole carriage seemed to wake up at once.",
	"Children pressed their faces to the glass, pointing at horses, barns, and a lonely red tractor.",
	"Maria opened the letter, read the first line again, and smiled for the first time that day.",
	"By the time they reached the coast, the sky was clear, the sea was calm, and the decision was made.",
}

var zhSentences = []string{
	"清晨的火车比平时晚了几分钟才出发，冬天常常是这样。",
	"小雨收拾了一个小包，带上笔记本，还有那封一直没有寄出的信。",
	"窗外的田野覆盖着白霜，河水在薄冰下缓缓流动。",
	"她数着车站：一共十二站，三个小时四十分钟，如果一切顺利的话。",
	"过道对面的男人在看前一天的报纸，每一页都折得整整齐齐。",
	"列车员走过来，问起北方的天气，大家都不知道该怎么回答。",
	"到了中午，太阳终于出来了，整节车厢好像一下子醒了过来。",
	"孩子们把脸贴在玻璃上，指着窗外的马、谷仓和一辆孤零零的红色拖拉机。",
	"小雨打开那封信，又读了一遍第一行，这是她今天第一次露出笑容。",
	"等到火车抵达海边，天空已经放晴，大海平静，她也终于做出了决定。",
}
