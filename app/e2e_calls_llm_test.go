package main

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/hehehai/AudioSmith/internal/catalog"
	"github.com/hehehai/AudioSmith/internal/core"
	"github.com/hehehai/AudioSmith/internal/engine"
	"github.com/hehehai/AudioSmith/internal/engine/proto"
	"github.com/hehehai/AudioSmith/internal/hub"
	"github.com/hehehai/AudioSmith/internal/telephony"
)

// TestCallsLocalLLM replays thread #44's two scripted calls (English
// restaurant booking, Chinese dentist) with the engine's language model
// (AUDIOSMITH_LLM, default catalog.DefaultLLM, from the HF cache: skips
// when not downloaded) through the calls prompt, the reply parser and the
// hangup guard, three times each, at the calls' temperature 0.4: the call
// must not end before the other person's goodbye, and HANGUP / OUTCOME
// must never be spoken (thread #47).
//
//	AUDIOSMITH_E2E=1 go test -run TestCallsLocalLLM -v -timeout 30m .
func TestCallsLocalLLM(t *testing.T) {
	if os.Getenv("AUDIOSMITH_E2E") == "" {
		t.Skip("set AUDIOSMITH_E2E=1 to run the real engine")
	}
	bin := engine.Find("../resources")
	if bin == "" {
		t.Fatal("no engine: run scripts/build-engine.sh")
	}
	ctx := context.Background()
	id := os.Getenv("AUDIOSMITH_LLM")
	if id == "" {
		id = catalog.DefaultLLM
	}
	e, _ := catalog.Builtin().ByID(id)
	l, err := hub.New(hub.Options{CacheDir: hub.CacheDir()}).Status(ctx, e)
	if err != nil || l.State != hub.Downloaded {
		t.Skipf("%s is not downloaded (%v)", e.Repo, err)
	}
	sup := engine.NewSupervisor(engine.Config{Bin: bin, Bus: core.NewBus(), Roles: []proto.Role{proto.RoleGPU}, LogDir: t.TempDir(), Offline: true,
		CacheDir: hub.CacheDir(), StatsEvery: -1, Logf: t.Logf})
	sup.Start(ctx)
	defer sup.Stop()
	deadline := time.Now().Add(2 * time.Minute)
	for sup.Status()[0].State != engine.WorkerReady {
		if time.Now().After(deadline) {
			t.Fatalf("gpu worker not ready: %+v", sup.Status())
		}
		time.Sleep(20 * time.Millisecond)
	}
	calls := []struct {
		name    string
		brief   telephony.Brief
		callers []string
	}{
		{"en", telephony.Brief{Task: "Book a table for two at 7 pm this Friday under the name Sam. If 7 is not available, 7:30 or 8 is fine.", User: "Sam",
			Disclosure: "Hi, this is an AI assistant calling on behalf of Sam.", MaxMinutes: 5, Language: "en"},
			[]string{"Good evening, Luigi's, how can I help you?", "Friday at seven... I'm sorry, we're fully booked at seven. I have seven forty-five.", "Great. Any dietary requirements?", "Perfect, see you Friday."}},
		{"zh", telephony.Brief{Task: "帮我预约明天上午的洗牙，最好十点左右，名字是李明，电话13800001111。", User: "李明",
			Disclosure: "您好，我是李明的 AI 助理。", MaxMinutes: 5, Language: "zh"},
			[]string{"您好，阳光口腔，请问有什么可以帮您？", "明天上午十点已经约满了，十点半可以吗？", "好的，请问贵姓，联系电话是多少？", "好的，已经给您登记了，明天见。"}},
	}
	temp := 0.4
	asked, early := 0, 0
	for _, c := range calls {
		for run := range 3 {
			msgs := []proto.LLMMessage{{Role: "system", Content: telephony.SystemPrompt(c.brief)}, {Role: "user", Content: telephony.OpeningNote(c.brief)}}
			outcome := ""
			for turn := 0; turn <= len(c.callers); turn++ {
				raw, err := sup.Call(ctx, proto.RoleGPU, core.NewID(time.Now()), proto.MethodLLMGenerate,
					proto.LLMGenerateParams{Model: e.Repo, ModelPath: l.Snapshot, Messages: msgs, MaxTokens: 400, Temperature: &temp}, nil)
				if err != nil {
					t.Fatal(err)
				}
				var r proto.LLMGenerateResult
				json.Unmarshal(raw, &r)
				var p telephony.ReplyParser
				said := p.Feed(r.Text)
				rest, reply := p.Finish()
				said = append(said, rest...)
				t.Logf("%s #%d turn %d: %q", c.name, run+1, turn, r.Text)
				for _, s := range said {
					if strings.Contains(s, "HANGUP") || strings.Contains(s, "OUTCOME") {
						t.Errorf("%s #%d turn %d: spoke %q", c.name, run+1, turn, s)
					}
				}
				if telephony.ValidOutcome(reply.Outcome) {
					outcome = reply.Outcome
				}
				last := turn == len(c.callers)
				if reply.Hangup {
					asked++
					hc := telephony.HangupCheck{CallerTurns: turn, Say: strings.Join(reply.Say, " "), Outcome: outcome}
					if turn > 0 {
						hc.LastCaller = c.callers[turn-1]
					}
					if ok, why := telephony.AllowHangup(hc); ok && !last {
						early++
						t.Errorf("%s #%d: the call would end at turn %d (%s)", c.name, run+1, turn, why)
					}
				}
				if last {
					break
				}
				msgs = append(msgs, proto.LLMMessage{Role: "assistant", Content: "SAY: " + strings.Join(reply.Say, " ")},
					proto.LLMMessage{Role: "user", Content: telephony.CallerLine(c.callers[turn])})
			}
		}
	}
	t.Logf("%d replies asked to hang up; %d would have ended the call early", asked, early)
}
