package main

// TestLocalLLM checks llm.generate with real models on a Mac: for each
// model (AUDIOSMITH_LLM, default catalog.DefaultLLM; ids comma-separated)
// it downloads the snapshot, runs the engine offline under sandbox-exec,
// and measures load time, tokens per second, MLX peak and resident memory;
// translates single sentences en→zh, zh→en and en→es; checks streaming,
// stop strings, JSON constrained to a schema and context_too_long; then
// translates a 20-line subtitle batch with internal/translate (Fast and
// Cinematic) through internal/llm's EngineBackend. Last, with a memory
// budget only the LLM fits in, loading it must evict a resident TTS model.
//
//	AUDIOSMITH_E2E=1 go test -run TestLocalLLM -v -timeout 1h .
//
// Downloads are deleted afterwards unless AUDIOSMITH_SMOKE_KEEP=1 (or
// they were there before). The report goes to /tmp/audiosmith-llm.md.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode"

	"github.com/hehehai/AudioSmith/internal/catalog"
	"github.com/hehehai/AudioSmith/internal/core"
	"github.com/hehehai/AudioSmith/internal/engine"
	"github.com/hehehai/AudioSmith/internal/engine/proto"
	"github.com/hehehai/AudioSmith/internal/hub"
	"github.com/hehehai/AudioSmith/internal/llm"
	"github.com/hehehai/AudioSmith/internal/translate"
)

var llmSubtitles = []string{
	"Good morning, and welcome back to the show.",
	"Thanks for having me. It's great to be here.",
	"So, tell us how it all started.",
	"I was nineteen, broke, and living above a bakery.",
	"Every morning the smell of bread woke me up at four.",
	"That's when I started writing songs.",
	"Did you ever think it would turn into a career?",
	"Honestly? No. I thought I'd be a teacher.",
	"What changed your mind?",
	"A stranger heard me play at a train station.",
	"She gave me her card and said, \"Call me tomorrow.\"",
	"And you called her?",
	"Not for a week. I was terrified.",
	"Who was she?",
	"The head of a small record label in Lyon.",
	"Your first album sold almost nothing, right?",
	"Three hundred copies. My mother bought fifty.",
	"And yet here you are, twenty years later.",
	"Still writing songs above a bakery, believe it or not.",
	"Let's hear one. The stage is yours.",
}

func TestLocalLLM(t *testing.T) {
	if os.Getenv("AUDIOSMITH_E2E") == "" {
		t.Skip("set AUDIOSMITH_E2E=1 to download and run real language models")
	}
	bin := engine.Find("resources")
	if bin == "" {
		t.Fatal("no engine: run scripts/build-engine.sh")
	}
	ids := os.Getenv("AUDIOSMITH_LLM")
	if ids == "" {
		ids = catalog.DefaultLLM
	}
	ctx := context.Background()
	keep := os.Getenv("AUDIOSMITH_SMOKE_KEEP") != ""
	cacheDir := hub.CacheDir()
	client := hub.New(hub.Options{CacheDir: cacheDir})
	ensure := func(t *testing.T, e catalog.Entry) string {
		t.Helper()
		l, err := client.Status(ctx, e)
		if err == nil && l.State != hub.Downloaded {
			start := time.Now()
			if err = client.Download(ctx, e, nil); err == nil {
				t.Logf("%s: downloaded %d MB in %.0f s", e.ID, e.Size>>20, time.Since(start).Seconds())
				if !keep {
					t.Cleanup(func() { client.Delete(context.Background(), e) })
				}
			}
			l, _ = client.Status(ctx, e)
		}
		if err != nil || l.State != hub.Downloaded {
			t.Fatalf("%s: %v %+v", e.ID, err, l)
		}
		return l.Snapshot
	}
	start := func(t *testing.T, env ...string) engine.Supervisor {
		sup := engine.NewSupervisor(engine.Config{Bin: offlineEngine(t, bin), Bus: core.NewBus(), LogDir: t.TempDir(), Offline: true,
			CacheDir: cacheDir, StatsEvery: -1, Env: env, OnDemand: []proto.Role{proto.RoleMedia, proto.RoleListen}, Logf: t.Logf,
			Log: func(e core.LogEntry) {
				if e.Level != core.SeverityDebug {
					t.Logf("engine %s: %s", e.Level, e.Message)
				}
			}})
		sup.Start(ctx)
		t.Cleanup(sup.Stop)
		deadline := time.Now().Add(2 * time.Minute)
		for time.Now().Before(deadline) {
			for _, st := range sup.Status() {
				if st.Role == proto.RoleGPU && st.State == engine.WorkerReady {
					return sup
				}
			}
			time.Sleep(50 * time.Millisecond)
		}
		t.Fatalf("engine not ready: %+v", sup.Status())
		return nil
	}
	var report []string
	defer func() {
		os.WriteFile("/tmp/audiosmith-llm.md", []byte("# Local LLM "+time.Now().Format("2006-01-02 15:04")+"\n\n"+strings.Join(report, "\n")+"\n"), 0o644)
	}()

	for _, id := range strings.Split(ids, ",") {
		e, ok := catalog.Builtin().ByID(strings.TrimSpace(id))
		if !ok {
			t.Fatalf("no entry %s", id)
		}
		t.Run(e.ID, func(t *testing.T) {
			snap := ensure(t, e)
			sup := start(t)
			var pid int
			for _, st := range sup.Status() {
				if st.Role == proto.RoleGPU {
					pid = st.PID
				}
			}
			stopRSS := watchRSS(pid)
			note := func(format string, args ...any) {
				line := fmt.Sprintf("- %s: ", e.ID) + fmt.Sprintf(format, args...)
				report = append(report, line)
				t.Log(line)
			}
			// Load, timed apart.
			began := time.Now()
			var lr proto.LoadResult
			raw, err := sup.Call(ctx, proto.RoleGPU, core.NewID(time.Now()), proto.MethodModelsLoad, proto.LoadParams{Model: e.Repo, Path: snap, Task: "llm"}, nil)
			if err != nil {
				t.Fatalf("load: %v", err)
			}
			json.Unmarshal(raw, &lr)
			note("load %.1f s, %d MB resident", time.Since(began).Seconds(), lr.Bytes>>20)

			gen := func(p proto.LLMGenerateParams) (proto.LLMGenerateResult, string, error) {
				p.Model, p.ModelPath = e.Repo, snap
				var streamed strings.Builder
				raw, err := sup.Call(ctx, proto.RoleGPU, core.NewID(time.Now()), proto.MethodLLMGenerate, p, func(m proto.Message) {
					if m.Method == proto.MethodLLMToken {
						var tk proto.LLMTokenParams
						json.Unmarshal(m.Params, &tk)
						streamed.WriteString(tk.Text)
					}
				})
				var r proto.LLMGenerateResult
				if err == nil {
					err = json.Unmarshal(raw, &r)
				}
				return r, streamed.String(), err
			}
			zero := 0.0
			sys := func(s string) proto.LLMMessage { return proto.LLMMessage{Role: "system", Content: s} }
			user := func(s string) proto.LLMMessage { return proto.LLMMessage{Role: "user", Content: s} }
			translateOne := func(from, to, text string, check func(string) bool) {
				t.Helper()
				r, streamed, err := gen(proto.LLMGenerateParams{Temperature: &zero, Stream: true, MaxTokens: 200, Messages: []proto.LLMMessage{
					sys(fmt.Sprintf("You translate from %s to %s. Reply with the translation only.", from, to)), user(text)}})
				if err != nil {
					t.Errorf("%s→%s: %v", from, to, err)
					return
				}
				ok := check(r.Text) && streamed == r.Text && r.FinishReason == "stop"
				note("%s→%s %q → %q (%d+%d tokens, %.0f tok/s, %.2f s) %v", from, to, text, r.Text, r.PromptTokens, r.CompletionTokens, r.TokensPerSecond, r.Seconds, ok)
				if !ok {
					t.Errorf("%s→%s: %q (streamed %q, %s)", from, to, r.Text, streamed, r.FinishReason)
				}
			}
			han := func(s string) bool {
				return strings.IndexFunc(s, func(r rune) bool { return unicode.Is(unicode.Han, r) }) >= 0
			}
			translateOne("English", "Chinese (Simplified)", "The weather is lovely today, so let's walk to the park.", han)
			translateOne("Chinese", "English", "今天天气很好，我们一起去公园散步吧。", func(s string) bool {
				l := strings.ToLower(s)
				return !han(s) && strings.Contains(l, "park") && strings.Contains(l, "weather")
			})
			translateOne("English", "Spanish", "Good morning, and welcome back to the show.", func(s string) bool {
				l := strings.ToLower(s)
				return strings.Contains(l, "buenos días") || strings.Contains(l, "bienvenid")
			})

			// A long reply, for the decode speed.
			r, _, err := gen(proto.LLMGenerateParams{Temperature: &zero, MaxTokens: 300, Messages: []proto.LLMMessage{user("Write a 200-word story about a lighthouse keeper.")}})
			if err != nil {
				t.Error(err)
			} else {
				note("long reply: %d tokens in %.1f s, %.1f tok/s (%s)", r.CompletionTokens, r.Seconds, r.TokensPerSecond, r.FinishReason)
			}
			// Stop strings end the reply and are not in it.
			r, _, err = gen(proto.LLMGenerateParams{Temperature: &zero, Stop: []string{"4"}, Messages: []proto.LLMMessage{user("Count from 1 to 10, separated by commas, nothing else.")}})
			if err != nil || strings.Contains(r.Text, "4") || !strings.Contains(r.Text, "3") || r.FinishReason != "stop" {
				t.Errorf("stop: %+v %v", r, err)
			}
			// JSON constrained to a schema.
			schema := json.RawMessage(`{"type":"object","properties":{"lines":{"type":"array","items":{"type":"object","properties":{"id":{"type":"string"},"text":{"type":"string"}},"required":["id","text"],"additionalProperties":false}}},"required":["lines"],"additionalProperties":false}`)
			r, _, err = gen(proto.LLMGenerateParams{JSON: true, Schema: schema, MaxTokens: 400, Messages: []proto.LLMMessage{
				sys("Translate each line into Chinese (Simplified). Answer with JSON: {\"lines\":[{\"id\",\"text\"}]}."),
				user(`{"lines":[{"id":"1","text":"Hello there."},{"id":"2","text":"See you tomorrow."}]}`)}})
			var parsed struct {
				Lines []struct{ ID, Text string } `json:"lines"`
			}
			if err != nil || json.Unmarshal([]byte(r.Text), &parsed) != nil || len(parsed.Lines) != 2 || !han(parsed.Lines[1].Text) {
				t.Errorf("json: %+v %v", r, err)
			} else {
				note("JSON (constrained %v): %s in %.2f s", r.Constrained, r.Text, r.Seconds)
			}
			// Too long.
			_, _, err = gen(proto.LLMGenerateParams{MaxTokens: 1 << 20, Messages: []proto.LLMMessage{user("Hi")}})
			var perr *proto.Error
			if !errors.As(err, &perr) || perr.Code != proto.CodeContextTooLong {
				t.Errorf("context: %v", err)
			}

			// internal/translate through internal/llm.
			reg := llm.NewRegistry(nil, nil)
			reg.Register("local", &llm.EngineBackend{DefaultModel: e.Repo, Call: func(ctx context.Context, method string, params any, notify func(string, json.RawMessage)) (json.RawMessage, error) {
				p := params.(llm.GenerateParams)
				p.ModelPath = snap
				return sup.Call(ctx, proto.RoleGPU, core.NewID(time.Now()), method, p, func(m proto.Message) {
					if notify != nil {
						notify(m.Method, m.Params)
					}
				})
			}})
			router := &llm.Router{Registry: reg}
			router.SetRouting(llm.Routing{Default: []llm.Route{{Provider: "local"}}})
			var segs []translate.Segment
			for i, s := range llmSubtitles {
				segs = append(segs, translate.Segment{ID: fmt.Sprint(i + 1), Text: s})
			}
			for _, tc := range []struct {
				target  string
				quality translate.Quality
				check   func(string) bool
			}{
				{"zh-Hans", translate.Fast, han},
				{"es", translate.Fast, func(s string) bool { return !han(s) }},
				{"zh-Hans", translate.Cinematic, han},
			} {
				began := time.Now()
				res, err := (&translate.LLM{Router: router}).Translate(ctx, translate.Request{Source: "en", Target: tc.target, Segments: segs, Quality: tc.quality})
				if err != nil {
					t.Errorf("%s %s: %v", tc.target, tc.quality, err)
					continue
				}
				bad := 0
				for i, s := range res.Segments {
					if s.Err != nil || !tc.check(s.Text) || s.Text == llmSubtitles[i] {
						bad++
						t.Logf("  %s: %q → %q %v", s.ID, llmSubtitles[i], s.Text, s.Err)
					}
				}
				note("subtitles en→%s %s: 20 lines in %.1f s, %d failed, %d bad, %d+%d tokens; 1: %q, 11: %q, 20: %q", tc.target, tc.quality, time.Since(began).Seconds(),
					res.Failed, bad, res.Usage.PromptTokens, res.Usage.CompletionTokens, res.Segments[0].Text, res.Segments[10].Text, res.Segments[19].Text)
				if bad > 1 {
					t.Errorf("%s %s: %d bad lines", tc.target, tc.quality, bad)
				}
			}
			var st proto.StatsResult
			if raw, err := sup.Call(ctx, proto.RoleGPU, "", proto.MethodStats, nil, nil); err == nil {
				json.Unmarshal(raw, &st)
			}
			note("MLX peak %.2f GB, process RSS peak %.2f GB", float64(st.Memory.Peak)/(1<<30), float64(stopRSS())/(1<<30))
		})
	}

	// A language model evicts an idle TTS model when memory is tight.
	t.Run("evict", func(t *testing.T) {
		e, _ := catalog.Builtin().ByID(catalog.DefaultLLM)
		snap := ensure(t, e)
		kokoro, _ := catalog.Builtin().ByID("kokoro-82m-bf16")
		ksnap := ensure(t, kokoro)
		budget := weightBytesOf(snap) + 100<<20
		sup := start(t, fmt.Sprintf("AUDIOSMITH_MEMORY_BUDGET=%d", budget))
		call := func(method string, params any) json.RawMessage {
			raw, err := sup.Call(ctx, proto.RoleGPU, core.NewID(time.Now()), method, params, nil)
			if err != nil {
				t.Fatalf("%s: %v", method, err)
			}
			return raw
		}
		call(proto.MethodModelsLoad, proto.LoadParams{Model: kokoro.Repo, Path: ksnap})
		call(proto.MethodModelsLoad, proto.LoadParams{Model: e.Repo, Path: snap, Task: "llm"})
		var loaded proto.LoadedResult
		json.Unmarshal(call(proto.MethodModelsLoaded, nil), &loaded)
		var models []string
		for _, m := range loaded.Models {
			models = append(models, m.Model)
		}
		report = append(report, fmt.Sprintf("- evict: budget %d MB, resident after loading the LLM: %v", budget>>20, models))
		if len(models) != 1 || models[0] != e.Repo {
			t.Errorf("resident %v", models)
		}
	})
}

// weightBytesOf is the size of a snapshot's safetensors (the engine's
// estimate of what loading it adds).
func weightBytesOf(dir string) int64 {
	var n int64
	paths, _ := filepath.Glob(filepath.Join(dir, "*.safetensors"))
	for _, p := range paths {
		if fi, err := os.Stat(p); err == nil {
			n += fi.Size()
		}
	}
	return n
}
