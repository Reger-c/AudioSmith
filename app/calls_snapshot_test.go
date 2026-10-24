package main

import (
	"image/png"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/hehehai/AudioSmith/internal/i18n"
	"github.com/hehehai/AudioSmith/internal/services"
	"github.com/hehehai/AudioSmith/internal/settings"
	"github.com/hehehai/AudioSmith/internal/telephony"
)

// TestSnapshotsCalls renders the Calls page and the Twilio setup (M6b)
// light and dark, in English and Chinese, as TestSnapshots does the others:
//
//	AUDIOSMITH_SNAPSHOTS=/tmp/shots go test -run TestSnapshots .
func TestSnapshotsCalls(t *testing.T) { runSnapshots(t, snapshotsCalls) }

func snapshotsCalls(t *testing.T, out string) {
	t.Cleanup(func() { i18n.SetLocale("en") })
	for _, run := range []struct {
		lang string
		dark bool
	}{{"en", false}, {"en", true}, {"zh-Hans", false}, {"zh-Hans", true}} {
		i18n.SetLocale("en")
		// Not set up.
		d0 := newCallTest(t, false)
		d := newCallTest(t, true)
		suffix := "light"
		if run.dark {
			suffix = "dark"
		}
		if run.lang != "en" {
			suffix += "-" + run.lang
		}
		for _, x := range []*callTest{d0, d} {
			if run.lang != "en" {
				x.a.updateSettings(func(s *settings.Settings) { s.General.Language = run.lang })
			}
			x.tt.SetDark(run.dark)
			x.tt.SetScale(2)
			x.tt.SetSize(1500, 900)
		}
		save := func(x *callTest, name string) {
			x.tt.Frame()
			x.tt.Frame()
			f, err := os.Create(filepath.Join(out, name+"-"+suffix+".png"))
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			if err := png.Encode(f, x.tt.Image()); err != nil {
				t.Fatal(err)
			}
		}
		d0.a.navigate("/calls")
		save(d0, "calls-setup-needed")
		d0.a.navigate("/calls/setup")
		save(d0, "twilio-setup-empty")

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
		a.navigate("/calls")
		a.selectCall("01live")
		a.callForm.number = "+44 20 7946 0958"
		a.callForm.brief = "Ask whether they have a table for four on Saturday evening."
		save(d, "calls-live")
		a.selectCall("01done")
		save(d, "calls-ended")
		a.callAsk = true
		save(d, "calls-confirm")
		a.callAsk = false
		a.openInbound()
		save(d, "calls-inbound")
		a.callInOpen = false
		a.navigate("/calls/setup")
		a.tw.tunnel, a.tw.tunnelMsg = "ok", i18n.T("The tunnel reaches the gateway on %s.", a.callGWAddr)
		save(d, "twilio-setup")
		d.a.closeCalls()
		d0.a.closeCalls()
	}
}
