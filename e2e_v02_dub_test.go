package main

// TestV02's Dub and Batch steps; split from e2e_v02_test.go.

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hehehai/AudioSmith/internal/dub"
	"github.com/hehehai/AudioSmith/internal/engine/enginetest"
	"github.com/hehehai/AudioSmith/internal/jobs"
	"github.com/hehehai/AudioSmith/internal/services"
	"github.com/hehehai/AudioSmith/internal/settings"
)

// dubClip writes the 30 s two-speaker clip over music, and an MP4 of it.
func (v *v02) dubClip(t *testing.T, name string, lines []struct{ voice, text string }, video bool) string {
	t.Helper()
	const rate = 44100
	var speech []float64
	speech = append(speech, make([]float64, rate/2)...)
	for i, l := range lines {
		p := say(t, v.dir, fmt.Sprintf("%s-line%d", name, i), l.voice, l.text, rate)
		au, err := enginetest.ReadWAV(p)
		if err != nil {
			t.Fatal(err)
		}
		speech = append(speech, au.Samples...)
		speech = append(speech, make([]float64, rate*7/10)...)
	}
	music := synthMusic(len(speech), rate)
	mix := make([]float64, 2*len(speech))
	for i, s := range speech {
		mix[2*i], mix[2*i+1] = 0.9*s+music[2*i], 0.9*s+music[2*i+1]
	}
	wavPath := filepath.Join(v.dir, name+".wav")
	enginetest.WriteWAV(wavPath, enginetest.Audio{Rate: rate, Channels: 2, Samples: mix}, 16)
	if !video {
		return wavPath
	}
	ff := lookTool("ffmpeg")
	if ff == "" {
		t.Fatal("needs ffmpeg for the source video")
	}
	mp4 := filepath.Join(v.dir, name+".mp4")
	secs := float64(len(speech)) / rate
	if b, err := exec.Command(ff, "-loglevel", "error", "-y", "-f", "lavfi", "-i", fmt.Sprintf("testsrc=size=640x360:rate=25:duration=%.2f", secs), "-i", wavPath,
		"-c:v", "h264_videotoolbox", "-b:v", "1M", "-c:a", "aac", "-shortest", mp4).CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg: %v %s", err, b)
	}
	return mp4
}

var dubLines = []struct{ voice, text string }{
	{"Samantha", "Good morning, and welcome back to the show. Today we are talking about trains."},
	{"Daniel", "Thanks for having me. I have loved trains since I was a little boy."},
	{"Samantha", "What is the longest journey you have ever taken?"},
	{"Daniel", "Probably the night train from London to the far north of Scotland."},
	{"Samantha", "That sounds wonderful. Did you sleep well on the way, or did you stay up watching the hills?"},
	{"Daniel", "I stayed up most of the night. The hills in the moonlight were too beautiful to miss, and the tea was good."},
}

// dubFlow: the whole pipeline into Chinese with the local LLM, then an
// MP4 with the original and the dubbed track and subtitles.
func (v *v02) dubFlow(t *testing.T) string {
	a := v.a
	if ok, _ := a.dubTranslatorReady(dub.Settings{Translator: "llm"}); !ok {
		t.Fatalf("no translator (local LLM ready %v)", a.localLLMReady())
	}
	src := v.dubClip(t, "dub", dubLines, true)
	n := len(a.dubList)
	a.updateSettings(func(s *settings.Settings) {
		s.Dubbing.Targets, s.Dubbing.NoReview, s.Dubbing.Translator = []string{"zh"}, true, "llm"
		s.Dubbing.Model = v.model(t, "qwen3-tts-0.6b-base-8bit").Repo
	})
	a.newDub(dub.Source{Kind: dub.SourceFile, Path: src, Name: filepath.Base(src)})
	v.wait(t, "dub created", time.Minute, func() bool { return a.dub != nil && len(a.dubList) > n })
	id := a.dub.ID
	started := time.Now()
	ids, err := a.dubSvc.Run(context.Background(), id, services.RunRequest{Through: dub.StageMix})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	stageTimes := map[string]float64{}
	for st, jid := range ids {
		j := v.job(t, jid, 60*time.Minute)
		if j.State != jobs.StateSucceeded {
			t.Errorf("stage %s: %s %+v", st, j.State, j.Err)
		}
		if !j.Started.IsZero() && !j.Finished.IsZero() {
			stageTimes[st] = j.Finished.Sub(j.Started).Seconds()
		}
	}
	p, err := a.dubSvc.Get(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	speakers := map[string]bool{}
	han := 0
	for _, s := range p.Segments {
		speakers[s.Speaker] = true
		if hanRE.MatchString(s.Lines["zh"].Text) {
			han++
		}
	}
	for k, st := range p.Stages {
		if st.Status != dub.StatusDone {
			t.Logf("stage %s: %s %s %s %v", k, st.Status, st.Code, st.Message, st.Warnings)
		}
	}
	if len(p.Segments) < 4 || len(speakers) != 2 || han < len(p.Segments)*8/10 || p.Track("zh").File == "" {
		t.Errorf("dub: %d segments, speakers %v, %d translated, track %+v", len(p.Segments), speakers, han, p.Track("zh"))
	}
	for i, s := range p.Segments {
		if i < 3 {
			t.Logf("  %s %.1f–%.1f %q → %q", s.Speaker, s.Start, s.End, s.Text, s.Lines["zh"].Text)
		}
	}
	// Export the MP4: original + Chinese, soft subtitles.
	out := filepath.Join(v.dir, "V02 Dub.mp4")
	jid, err := a.dubSvc.ExportJob(context.Background(), services.ExportRequest{Dub: id, Format: dub.ExportMP4, Tracks: []string{dub.OrigTrack, "zh"},
		Default: "zh", Subtitles: true, Output: out})
	if err != nil {
		t.Fatal(err)
	}
	if j := v.job(t, jid, 10*time.Minute); j.State != jobs.StateSucceeded {
		t.Fatalf("export: %+v", j.Err)
	}
	note := fmt.Sprintf("%.0f s clip → %d lines, %d speakers, %d/%d in Chinese, in %.0f s (stages %v)", p.Source.Duration, len(p.Segments), len(speakers), han, len(p.Segments),
		time.Since(started).Seconds(), roundMap(stageTimes))
	if m := ffprobeJSON(t, "-show_streams", out); m != nil {
		streams, _ := m["streams"].([]any)
		count := map[string]int{}
		var langs []string
		for _, s := range streams {
			s := s.(map[string]any)
			count[fmt.Sprint(s["codec_type"])]++
			if tags, ok := s["tags"].(map[string]any); ok {
				langs = append(langs, fmt.Sprint(s["codec_type"], ":", tags["language"]))
			}
		}
		note += fmt.Sprintf("; MP4 streams %v %v", count, langs)
		if count["video"] != 1 || count["audio"] != 2 || count["subtitle"] < 1 {
			t.Errorf("MP4: %v", count)
		}
	}
	av := avTracks(t, v.dir, out)
	t.Logf("AVFoundation: %s", av)
	if !strings.Contains(av, "soun zh") {
		t.Errorf("AVFoundation does not see the Chinese track: %s", av)
	}
	return note
}

func roundMap(m map[string]float64) map[string]int {
	out := map[string]int{}
	for k, v := range m {
		out[k] = int(v + 0.5)
	}
	return out
}

// batchFlow: one file through the batch queue into Chinese.
func (v *v02) batchFlow(t *testing.T) string {
	a := v.a
	src := v.dubClip(t, "batch", dubLines[:2], false)
	bs := services.DefaultBatchSettings()
	bs.Targets, bs.Model = []string{"zh"}, v.model(t, "qwen3-tts-0.6b-base-8bit").Repo
	started := time.Now()
	ids, errs := a.dubSvc.Enqueue(context.Background(), []string{src}, bs)
	if len(ids) != 1 || len(errs) > 0 {
		t.Fatalf("enqueue %v %v", ids, errs)
	}
	var p *dub.Project
	v.wait(t, "batch item", 40*time.Minute, func() bool {
		act, _ := a.dubSvc.Active(context.Background(), ids[0])
		if len(act) > 0 {
			return false
		}
		p, _ = a.dubSvc.Get(context.Background(), ids[0])
		return p != nil && (p.Track("zh").File != "" || p.Run == nil && len(p.Stages) > 0)
	})
	if p.Track("zh").File == "" {
		t.Errorf("batch item: %+v", p.Stages)
	}
	q, _ := a.dubSvc.Queue(context.Background())
	phase := ""
	for _, r := range q {
		if r.ID == ids[0] {
			phase = r.Phase
		}
	}
	return fmt.Sprintf("1 item (%.0f s) in %.0f s, phase %q, %d lines", p.Source.Duration, time.Since(started).Seconds(), phase, len(p.Segments))
}
