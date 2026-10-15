package main

import (
	"context"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/hehehai/AudioSmith/internal/api"
	"github.com/hehehai/AudioSmith/internal/core"
	"github.com/hehehai/AudioSmith/internal/engine/proto"
	"github.com/hehehai/AudioSmith/internal/hub"
	"github.com/hehehai/AudioSmith/internal/i18n"
	mcpsrv "github.com/hehehai/AudioSmith/internal/mcp"
	"github.com/hehehai/AudioSmith/internal/services"
	"github.com/hehehai/AudioSmith/internal/settings"
	"github.com/hehehai/AudioSmith/internal/telephony"
	"github.com/hehehai/AudioSmith/internal/translate"
	vd "github.com/hehehai/AudioSmith/internal/voicedesign"
	"github.com/hehehai/AudioSmith/internal/workflow"
)

// b2Config is one look of the 0.3 phase-B2 review set: a window size, a
// UI scale, light or dark, a language.
type b2Config struct {
	name  string
	w, h  int
	scale float64
	dark  bool
	lang  string
}

// b2Configs: the review pairs (1100×720 light, dark, Chinese; 820×520
// at 125 %) and the overflow checks (125 % in Chinese, the minimum in
// dark Chinese).
var b2Configs = []b2Config{
	{"1100-light", 1100, 720, 1, false, "en"},
	{"1100-dark", 1100, 720, 1, true, "en"},
	{"1100-light-zh", 1100, 720, 1, false, "zh-Hans"},
	{"min125-light", 820, 520, 1.25, false, "en"},
	{"min125-light-zh", 820, 520, 1.25, false, "zh-Hans"},
	{"min-dark-zh", 820, 520, 1, true, "zh-Hans"},
}

// TestSnapshotsB2 renders the pages of the 0.3 redesign's phase B2
// (Voices, the voice editor, a voice from a clip, Design, Gallery,
// History, Workflows, Calls and the Twilio setup, Tools, Integrations,
// the API reference, Models, the licence dialog, Engine, Logs, the
// first-run wizard, Settings → Permissions) in each b2Config:
//
//	AUDIOSMITH_SNAPSHOTS=/tmp/shots go test -run TestSnapshotsB2 .
//
// AUDIOSMITH_B2_CFG and AUDIOSMITH_B2_PAGES (comma-separated prefixes)
// narrow the set while iterating.
func TestSnapshotsB2(t *testing.T) {
	out := os.Getenv("AUDIOSMITH_SNAPSHOTS")
	if out == "" {
		t.Skip("set AUDIOSMITH_SNAPSHOTS to a directory")
	}
	os.MkdirAll(out, 0o755)
	t.Cleanup(func() { i18n.SetLocale("en") })
	useMemKeys(t)
	stubMachine(t, 16, true)
	pick := func(env, s string) bool {
		v := os.Getenv(env)
		if v == "" {
			return true
		}
		for _, p := range strings.Split(v, ",") {
			if strings.HasPrefix(s, p) {
				return true
			}
		}
		return false
	}
	for _, cfg := range b2Configs {
		if !pick("AUDIOSMITH_B2_CFG", cfg.name) {
			continue
		}
		setup := func(a *app, tt *ui.Tester) {
			i18n.SetLocale("en")
			a.updateSettings(func(s *settings.Settings) {
				s.General.Language = cfg.lang
				s.Appearance.Scale = cfg.scale
			})
			tt.SetSize(cfg.w, cfg.h)
			tt.SetDark(cfg.dark)
			tt.SetScale(2)
		}
		shoot := func(a *app, tt *ui.Tester, page string, fn func()) {
			if !pick("AUDIOSMITH_B2_PAGES", page) {
				return
			}
			fn()
			a.toasts = nil
			tt.Frame()
			tt.Frame()
			tt.Frame()
			f, err := os.Create(filepath.Join(out, page+"-"+cfg.name+".png"))
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			if err := png.Encode(f, tt.Image()); err != nil {
				t.Fatal(err)
			}
		}
		b2Voices(t, setup, shoot)
		b2Library(t, setup, shoot)
		b2Calls(t, setup, shoot)
		b2Workflows(t, setup, shoot)
		i18n.SetLocale("en")
	}
}

type b2Setup func(*app, *ui.Tester)
type b2Shoot func(*app, *ui.Tester, string, func())

// b2Voices: Voices, the editor, a voice from a clip, Design, Gallery.
func b2Voices(t *testing.T, setup b2Setup, shoot b2Shoot) {
	a, eng, tt := newTestApp(t)
	acceptLicences(t, a)
	setup(a, tt)
	shoot(a, tt, "voices-empty", func() { a.navigate("/voices") })

	me := addVoice(t, a, core.Voice{Name: "Narrator (me)", Kind: core.VoiceCloned, RefText: "The house at the end of the lane had been empty for years.",
		Style: "calm and slow", Language: "en", Tags: []string{"narration"}}, 9)
	a.setVoicePortrait(me.ID, testPNG(t, 320, 240))
	rec := filepath.Join(t.TempDir(), "consent.wav")
	writeSpeech(rec, 3, []span{{0, 3}})
	a.lib.svc.Voices.SetConsent(context.Background(), me.ID, core.ConsentRecording{Audio: rec, Statement: consentStatement(), Locale: "en"})
	addVoice(t, a, core.Voice{Name: "老奶奶讲故事", Kind: core.VoiceDesigned, Language: "zh", Design: "慈祥的老奶奶，语速慢，四川话",
		DesignAttrs: designAttrs(vd.Attributes{Gender: "female", Age: "elderly", Pace: "slow", Dialect: "sichuan"}), Seed: 42}, 0)
	addVoice(t, a, core.Voice{Name: "Trailer Voice with a very long name for the movie announcements", Kind: core.VoiceDesigned, Language: "en", Locked: true,
		DesignAttrs: designAttrs(vd.Attributes{Gender: "male", Age: "middle_aged", Pitch: "very_low", Styles: []string{"dramatic"}}), Seed: 7}, 0)
	addVoice(t, a, core.Voice{Name: "Podcast Host", Kind: core.VoiceCloned, Language: "en"}, 12)
	tt.Frame()
	shoot(a, tt, "voices-list", func() { a.edit = nil; a.navigate("/voices") })
	shoot(a, tt, "voices-editor", func() { a.openVoiceEditor(me.ID) })
	a.edit = nil

	shoot(a, tt, "clone-empty", func() { a.startClone("") })
	shoot(a, tt, "clone-loaded", func() {
		clip := filepath.Join(t.TempDir(), "interview.wav")
		writeSpeech(clip, 32, []span{{0.4, 3}, {4, 11}, {11.6, 19}, {20, 27}, {28, 31}})
		a.cloneLoad(clip, "interview.wav", false)
		if r := eng.lastOf(t, proto.MethodVADDetect); r.ID != "" {
			eng.fail(r.ID, proto.CodeModelNotDownloaded, "no VAD")
		}
		answerTranscribe(t, eng, "So the first thing I tell every new narrator is: slow down, and let the sentence breathe.")
		a.clone.name = "Interview voice"
	})

	shoot(a, tt, "design", func() {
		a.navigate("/design")
		a.design.description = "a warm elderly British storyteller, slightly raspy, sparkly"
		a.design.seed = 1234567
	})
	shoot(a, tt, "gallery", func() { a.navigate("/gallery") })
	shoot(a, tt, "gallery-filtered", func() {
		a.gallery.text = "narrator"
		a.gallery.favOnly = false
	})
	a.gallery.text = ""
}

// b2Library: History, Models (+ the licence dialog), Engine, Logs, Tools,
// Integrations, the API reference, the wizard, Permissions.
func b2Library(t *testing.T, setup b2Setup, shoot b2Shoot) {
	a, _, tt := newTestApp(t)
	setup(a, tt)
	shoot(a, tt, "history-empty", func() { a.showPage("history") })
	texts := []string{
		"Hello from AudioSmith. This voice was generated on your Mac with MLX, and the history keeps every take you make so you can come back to it.",
		"你好，这是在 Mac 上用 MLX 生成的语音。",
		"The same voice, a little faster, for the trailer cut.",
		"Chapter one. The house at the end of the lane had been empty for years.",
	}
	var last string
	for i, text := range texts {
		at := time.Date(2026, 10, 13, 21, 50+i, 0, 0, time.Local)
		tk := core.Take{ID: core.NewID(at), CreatedAt: at, Text: text, Model: kokoro, Voice: []string{"af_heart", "zf_xiaobei", "am_adam", "bf_emma"}[i],
			Speed: 1, SampleRate: 24000, AudioSeconds: 5.1 - float64(i), GenSeconds: 1.2, Starred: i == 2, Seed: 81723,
			Render: &core.TakeRender{Bits: 24, Channels: 1, Bytes: 172844, Master: services.MasterDefault}}
		addTake(t, a, tk, 1.2)
		last = tk.ID
	}
	shoot(a, tt, "history-list", func() { a.showPage("history") })
	shoot(a, tt, "history-details", func() { a.history.selected = last })
	a.history.selected = ""

	useFakeHub(t, a)
	a.download(entry(t, "kokoro-82m-bf16"))
	dl := a.model(entry(t, "chatterbox-turbo-8bit"))
	shoot(a, tt, "models", func() {
		a.navigate("/models")
		dl.downloading, dl.progress = true, hub.Progress{Bytes: 480e6, TotalBytes: 1202987769, Mirror: "https://hf-mirror.com"}
		a.notices = nil
		a.toasts = nil
	})
	shoot(a, tt, "licence", func() { a.askLicence(entry(t, "omnivoice-bf16"), nil) })
	a.licenceOpen = false
	dl.downloading = false

	shoot(a, tt, "engine", func() { a.showPage("engine") })
	at := time.Date(2026, 10, 13, 21, 58, 3, 0, time.Local)
	for i, l := range []core.LogEntry{
		{Source: "app", Message: "AudioSmith 0.3.0 started"},
		{Source: "hub", Message: "downloading mlx-community/Kokoro-82M-bf16"},
		{Source: "engine:gpu", Message: "loaded Kokoro-82M-bf16 in 1.4 s"},
		{Source: "hub", Level: core.SeverityError, Message: "acme/gated: the repo is gated: accept its terms on huggingface.co and add a token"},
	} {
		l.Time = at.Add(time.Duration(i) * time.Second)
		a.logs.Append(l)
	}
	shoot(a, tt, "logs", func() { a.navigate("/logs") })

	shoot(a, tt, "tools-ab", func() {
		a.navigate("/tools")
		a.toolTab = 0
		a.ab.runs = [2]*toolRun{{model: "Kokoro 82M", output: "/tmp/a.wav", key: "tool:a"}, {model: "Kokoro 82M", busy: true}}
	})
	shoot(a, tt, "tools-convert", func() {
		a.toolTab = 1
		a.cv = toolConvert{file: "/tmp/memo.m4a", name: "memo.m4a", duration: 4.2, heard: "Meet me at the station at noon, and bring the tickets.", match: true,
			note: i18n.T("The take is %.1f s and the recording %.1f s: stretched as far as speech allows (%.1f s).", 9.1, 4.2, 4.6),
			take: &core.Take{ID: "t-conv", AudioSeconds: 3.9}}
	})
	shoot(a, tt, "tools-ratefit", func() {
		a.toolTab = 2
		a.rf.text = "Nous devons partir avant que la tempête n'arrive sur la côte."
		a.rf.lang = langName("fr")
		a.rf.slot = 2.5
		fit := translate.DefaultPredictor.Fit(a.rf.text, "fr", 2.5, 1)
		a.rf.res = &translate.FitResult{Fit: fit, Rewrite: "Partons avant la tempête.", RewriteFit: &translate.Fit{Predicted: 1.6, Slot: 2.5, Ratio: 0.64, Verdict: translate.FitOK}}
	})
	shoot(a, tt, "tools-direction", func() {
		a.toolTab = 3
		a.dir.note = "more urgent, a little scared"
		d := translate.ParseDirection(a.dir.note)
		a.dir.res = &d
	})
	shoot(a, tt, "tools-probe", func() {
		a.toolTab = 4
		a.prb = toolProbe{file: "/Users/me/Movies/interview.mov", out: "{\n  \"format\": \"mov\",\n  \"duration\": 754.32,\n  \"hasAudio\": true,\n  \"hasVideo\": true,\n  \"codec\": \"aac\",\n  \"sampleRate\": 48000,\n  \"channels\": 2\n}"}
	})

	a.voices = append(a.voices, core.Voice{ID: "v1", Name: "Narrator", Kind: core.VoiceCloned})
	a.updateSettings(func(s *settings.Settings) {
		s.API.Bindings = []settings.APIBinding{{Client: "claude-code", Voice: "v1"}}
	})
	a.syncAPIForm()
	a.apiPort, a.apiHasKey = 3901, true
	a.apiShare = &api.Share{PIN: "482913", Port: 3902, URLs: []string{"http://192.168.1.23:3902"}}
	a.apiClients = []mcpsrv.Client{{ID: "claude-code", Name: "claude-code 2.1", LastSeen: time.Date(2026, 10, 14, 9, 30, 0, 0, time.Local), Calls: 12}}
	shoot(a, tt, "integrations", func() { a.navigate("/integrations") })
	shoot(a, tt, "integration-claude-code", func() { a.navigate("/integrations/claude-code") })
	shoot(a, tt, "integration-openai-sdk", func() { a.navigate("/integrations/openai-sdk") })
	shoot(a, tt, "api-reference", func() { a.navigate("/api-reference") })

	shoot(a, tt, "permissions", func() { a.navigate("/settings/permissions") })
	a.navigate("/studio")
	for i, name := range []string{"system", "models", "privacy", "dictation"} {
		shoot(a, tt, "onboarding-"+name, func() {
			if !a.obOpen {
				a.openOnboarding()
			}
			a.obStep = i
		})
	}
	a.obOpen = false
}

// b2Calls: Calls (not set up, live, ended) and the Twilio setup.
func b2Calls(t *testing.T, setup b2Setup, shoot b2Shoot) {
	d0 := newCallTest(t, false)
	setup(d0.a, d0.tt)
	shoot(d0.a, d0.tt, "calls-setup-needed", func() { d0.a.navigate("/calls") })
	shoot(d0.a, d0.tt, "twilio-empty", func() { d0.a.navigate("/calls/setup") })
	d0.a.closeCalls()

	d := newCallTest(t, true)
	setup(d.a, d.tt)
	a := d.a
	now := time.Date(2026, 10, 14, 15, 4, 0, 0, time.Local)
	live := &services.Call{ID: "01live", Direction: services.CallOutbound, Number: "+442079460958", Phase: services.CallLive, Created: now,
		Answered: time.Now().Add(-75 * time.Second), Mode: telephony.ModeAgent, VoiceName: "Nova", Model: callTestDesign, MaxMinutes: 5,
		Brief: "Book a table for two this Friday at 7 pm under the name Sam.", Disclosure: "Hi, this is an AI assistant calling on behalf of Sam.",
		State: telephony.StateSpeaking, Transcript: []telephony.Line{
			{Speaker: telephony.SpeakerAgent, Text: "Hi, this is an AI assistant calling on behalf of Sam."},
			{Speaker: telephony.SpeakerAgent, Text: "I'd like to book a table for two this Friday at 7 pm.", At: 4 * time.Second},
			{Speaker: telephony.SpeakerCaller, Text: "Let me check… we have 7:30, is that all right?", At: 9 * time.Second},
			{Speaker: telephony.SpeakerAgent, Text: "7:30 works. It's under the name", At: 14 * time.Second, Interrupted: true},
			{Speaker: telephony.SpeakerCaller, Text: "Sorry, what was the name?", At: 16 * time.Second},
		}}
	ended := &services.Call{ID: "01done", Direction: services.CallInbound, Number: "+15550100111", Phase: services.CallEnded,
		Created: now.Add(-2 * time.Hour), Answered: now.Add(-2 * time.Hour), Duration: 48, Mode: telephony.ModeAgent, VoiceName: "Nova",
		Brief: "Take a message for Sam.", Outcome: telephony.OutcomeDone, Summary: "The courier will deliver the parcel tomorrow between 9 and 11; nobody needs to sign.",
		Transcript: []telephony.Line{{Speaker: telephony.SpeakerAgent, Text: "Hi, you've reached the AI assistant of Sam. How can I help?"},
			{Speaker: telephony.SpeakerCaller, Text: "It's the courier, the parcel comes tomorrow morning.", At: 5 * time.Second}},
		Turns: []telephony.TurnLatency{{STT: 310 * time.Millisecond, LLMFirst: 620 * time.Millisecond, TTSFirst: 450 * time.Millisecond, Total: 1500 * time.Millisecond}}}
	failed := &services.Call{ID: "01busy", Direction: services.CallOutbound, Number: "+8613800138000", Phase: services.CallFailed,
		Created: now.Add(-26 * time.Hour), Brief: "Ask opening hours", Outcome: telephony.OutcomeNotDone, Summary: "The line was busy."}
	for _, c := range []*services.Call{failed, ended, live} {
		a.callChanged(services.CallChanged{ID: c.ID, Call: c})
	}
	shoot(d.a, d.tt, "calls-live", func() {
		a.navigate("/calls")
		a.selectCall("01live")
		a.callForm.number = "+44 20 7946 0958"
		a.callForm.brief = "Ask whether they have a table for four on Saturday evening."
	})
	shoot(d.a, d.tt, "calls-ended", func() { a.selectCall("01done") })
	shoot(d.a, d.tt, "twilio-ready", func() {
		a.navigate("/calls/setup")
		a.tw.tunnel, a.tw.tunnelMsg = "ok", i18n.T("The tunnel reaches the gateway on %s.", a.callGWAddr)
	})
	a.closeCalls()
}

// b2Workflows: Workflows (empty, a graph with a step selected, a run).
func b2Workflows(t *testing.T, setup b2Setup, shoot b2Shoot) {
	d := newWfTest(t)
	a, tt := d.a, d.tt
	setup(a, tt)
	shoot(a, tt, "workflows-empty", func() { a.navigate("/workflows") })
	shoot(a, tt, "workflows-graph", func() {
		a.wfNew("translate-narrate")
		for i := range a.wf.Nodes {
			if a.wf.Nodes[i].Kind == workflow.KindTranslate {
				a.wfSel = a.wf.Nodes[i].ID
			}
		}
	})
	shoot(a, tt, "workflows-run", func() {
		a.wfNew("narration-normalize")
		start := a.wf.InputNode()
		start.P.Scripts = []workflow.Script{{Text: "Welcome to the course.\n---\nLesson one: sound."}, {Name: "chapter-2.md", Text: "Lesson two: light."}}
		a.wfEdit(func(*workflow.Workflow) {})
		a.wfStart(false)
		if a.wfRun == nil {
			return
		}
		for k := range a.wfRun.Items {
			a.wfExpanded[a.wfRun.Items[k].Key] = true
		}
	})
}
