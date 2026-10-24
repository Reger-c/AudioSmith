package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hehehai/AudioSmith/internal/catalog"
	"github.com/hehehai/AudioSmith/internal/core"
	"github.com/hehehai/AudioSmith/internal/engine"
	"github.com/hehehai/AudioSmith/internal/engine/proto"
	"github.com/hehehai/AudioSmith/internal/hub"
	"github.com/hehehai/AudioSmith/internal/jobs"
)

// TestDictationLatency measures the speech-to-text candidates for
// dictation on the listen worker, as dictation runs them: a fresh worker
// (cold: load + first transcription) and the model resident (warm), on
// about five seconds of English and of Chinese (macOS `say`, 16 kHz WAV).
// Models are downloaded into the HF cache and deleted afterwards unless
// they were there (AUDIOSMITH_SMOKE_KEEP=1 keeps them).
//
//	AUDIOSMITH_E2E=1 go test -run TestDictationLatency -v -timeout 1h .
func TestDictationLatency(t *testing.T) {
	if os.Getenv("AUDIOSMITH_E2E") == "" {
		t.Skip("set AUDIOSMITH_E2E=1 to run the real engine")
	}
	bin := engine.Find("../resources")
	if bin == "" {
		t.Fatal("no engine: run scripts/build-engine.sh")
	}
	ids := os.Getenv("AUDIOSMITH_DICT_MODELS")
	if ids == "" {
		ids = "qwen3-asr-0.6b-4bit,whisper-large-v3-turbo-4bit"
	}
	ctx := context.Background()
	dir := t.TempDir()
	clip := func(name, voice, text string) string {
		aiff := filepath.Join(dir, name+".aiff")
		wav := filepath.Join(dir, name+".wav")
		args := []string{"-o", aiff, text}
		if voice != "" {
			args = append([]string{"-v", voice}, args...)
		}
		if out, err := exec.Command("say", args...).CombinedOutput(); err != nil {
			t.Fatalf("say: %v %s", err, out)
		}
		if out, err := exec.Command("afconvert", "-f", "WAVE", "-d", "LEI16@16000", "-c", "1", aiff, wav).CombinedOutput(); err != nil {
			t.Fatalf("afconvert: %v %s", err, out)
		}
		return wav
	}
	type utterance struct{ name, lang, text, path string }
	us := []utterance{
		{name: "en1", lang: "en", text: "Please send the meeting notes to Anna before lunch tomorrow, and copy the whole team."},
		{name: "en2", lang: "en", text: "Remind me to call the dentist on Friday afternoon about moving my appointment."},
		{name: "zh1", lang: "zh", text: "请在明天中午之前把会议记录发给安娜，并抄送整个团队。"},
		{name: "zh2", lang: "zh", text: "提醒我星期五下午给牙医打电话，改一下预约时间。"},
	}
	for i, u := range us {
		voice := "Samantha" // not the system voice: it may be a Chinese one
		if u.lang == "zh" {
			voice = "Tingting"
		}
		us[i].path = clip(u.name, voice, u.text)
	}

	client := hub.New(hub.Options{CacheDir: hub.CacheDir()})
	keep := os.Getenv("AUDIOSMITH_SMOKE_KEEP") != ""
	bus := core.NewBus()
	sup := engine.NewSupervisor(engine.Config{Bin: bin, Bus: bus, Roles: []proto.Role{proto.RoleListen}, LogDir: dir, Offline: true,
		CacheDir: hub.CacheDir(), StatsEvery: -1, Logf: t.Logf})
	sup.Start(ctx)
	defer sup.Stop()
	waitReady := func() time.Duration {
		start := time.Now()
		for time.Since(start) < 2*time.Minute {
			for _, st := range sup.Status() {
				if st.State == engine.WorkerReady {
					return time.Since(start)
				}
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatalf("listen worker not ready: %+v", sup.Status())
		return 0
	}
	run := func(kind string, params, out any) (time.Duration, error) {
		raw, _ := json.Marshal(params)
		start := time.Now()
		res, err := engine.KindExecutor(sup, kind).Run(ctx, jobs.Job{ID: core.NewID(time.Now()), Spec: jobs.Spec{Params: raw}}, func(jobs.Progress) {}, nil)
		d := time.Since(start)
		if err == nil && out != nil {
			err = json.Unmarshal(res, out)
		}
		return d, err
	}
	var report strings.Builder
	report.WriteString("| Model | Worker start | Load | 1st en (cold) | warm en | warm zh (1st) | warm zh | en score | zh score | MLX peak |\n|---|---|---|---|---|---|---|---|---|---|\n")
	for _, id := range strings.Split(ids, ",") {
		e, ok := catalog.Builtin().ByID(id)
		if !ok {
			t.Errorf("no catalogue entry %s", id)
			continue
		}
		l, err := client.Status(ctx, e)
		if err != nil {
			t.Fatal(err)
		}
		if l.State != hub.Downloaded {
			if err := client.Download(ctx, e, nil); err != nil {
				t.Errorf("%s: download: %v", id, err)
				continue
			}
			if !keep {
				defer client.Delete(ctx, e)
			}
			l, _ = client.Status(ctx, e)
		}
		sup.Restart(proto.RoleListen)
		started := waitReady()
		load, err := run(proto.KindFor(proto.RoleListen, proto.MethodModelsLoad), proto.LoadParams{Model: e.Repo, Path: l.Snapshot, Task: "stt"}, nil)
		if err != nil {
			t.Errorf("%s: load: %v", id, err)
			continue
		}
		times := map[string]time.Duration{}
		scores := map[string]float64{}
		for _, u := range us {
			if u.lang == "zh" && !slicesContainsLang(e, "zh") {
				continue
			}
			var tr proto.TranscribeResult
			d, err := run(proto.KindFor(proto.RoleListen, proto.MethodSTTTranscribe),
				proto.TranscribeParams{Model: e.Repo, ModelPath: l.Snapshot, Audio: u.path, Language: u.lang}, &tr)
			if err != nil {
				t.Errorf("%s %s: %v", id, u.name, err)
				continue
			}
			times[u.name] = d
			scores[u.name] = similarity(u.text, tr.Text, u.lang == "zh")
			t.Logf("%s %s: %.0f ms (%.1f s of audio) %q (%.2f)", id, u.name, d.Seconds()*1000, tr.AudioSeconds, tr.Text, scores[u.name])
		}
		var st proto.StatsResult
		if raw, err := sup.Call(ctx, proto.RoleListen, "", proto.MethodStats, nil, nil); err == nil {
			json.Unmarshal(raw, &st)
		}
		ms := func(name string) string {
			d, ok := times[name]
			if !ok {
				return "—"
			}
			return fmt.Sprintf("%.0f ms", d.Seconds()*1000)
		}
		fmt.Fprintf(&report, "| %s | %.0f ms | %.0f ms | %s | %s | %s | %s | %.2f | %.2f | %.2f GB |\n", id, started.Seconds()*1000, load.Seconds()*1000,
			ms("en1"), ms("en2"), ms("zh1"), ms("zh2"), (scores["en1"]+scores["en2"])/2, (scores["zh1"]+scores["zh2"])/2, float64(st.Memory.Peak)/(1<<30))
	}
	t.Log("\n" + report.String())
	os.WriteFile("/tmp/audiosmith-dictation.md", []byte(report.String()), 0o644)
}

func slicesContainsLang(e catalog.Entry, code string) bool {
	if len(e.Languages) == 0 {
		return true // unknown: try it
	}
	for _, l := range e.Languages {
		if l.Code == code || strings.HasPrefix(l.Code, code+"-") {
			return true
		}
	}
	return false
}
