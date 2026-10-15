package telephony

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// e44Calls are local models' replies recorded by thread #44: Qwen3 4B
// wrote HANGUP after every English reply, the opening one included.
type e44Calls struct {
	Callers map[string][]string `json:"callers"`
	Runs    []struct {
		Model   string   `json:"model"`
		Call    string   `json:"call"`
		Replies []string `json:"replies"`
	} `json:"runs"`
}

// TestHangupRecordedReplies replays the recorded calls through the parser
// and the hangup guard: no call ends before its last reply, every last
// reply with HANGUP ends it, and HANGUP / OUTCOME are never spoken.
func TestHangupRecordedReplies(t *testing.T) {
	raw, err := os.ReadFile("testdata/e44-calls.json")
	if err != nil {
		t.Fatal(err)
	}
	var d e44Calls
	if err := json.Unmarshal(raw, &d); err != nil {
		t.Fatal(err)
	}
	if len(d.Runs) != 8 {
		t.Fatalf("runs %d", len(d.Runs))
	}
	hangups := 0
	for _, run := range d.Runs {
		callers := d.Callers[run.Call]
		outcome := ""
		for i, reply := range run.Replies {
			for _, step := range []int{1, 5, 1000} {
				var p ReplyParser
				said := feed(&p, reply, step)
				rest, r := p.Finish()
				said = append(said, rest...)
				for _, s := range said {
					if strings.Contains(s, "HANGUP") || strings.Contains(s, "OUTCOME") {
						t.Errorf("%s %s #%d: spoke %q", run.Model, run.Call, i, s)
					}
				}
				o := outcome
				if ValidOutcome(r.Outcome) {
					o = r.Outcome
				}
				if step == 1000 {
					outcome = o
				}
				if r.Hangup {
					if step == 1000 {
						hangups++
					}
					c := HangupCheck{CallerTurns: i, Say: strings.Join(r.Say, " "), Outcome: o}
					if i > 0 {
						c.LastCaller = callers[i-1]
					}
					ok, why := AllowHangup(c)
					if last := i == len(run.Replies)-1; ok != last {
						t.Errorf("%s %s #%d (step %d): hang up %v (%s) for %q", run.Model, run.Call, i, step, ok, why, reply)
					}
				}
			}
		}
	}
	if hangups < 10 {
		t.Errorf("only %d HANGUP replies: the evidence did not load", hangups)
	}
}

func TestAllowHangup(t *testing.T) {
	for _, c := range []struct {
		check HangupCheck
		want  bool
		why   string
	}{
		{HangupCheck{Say: "Hi, I'm calling to book a table. Goodbye!"}, false, HangupOpening},
		{HangupCheck{CallerTurns: 1, LastCaller: "We're closed Friday, sorry. Bye!"}, true, HangupCallerBye},
		{HangupCheck{CallerTurns: 1, LastCaller: "Luigi's, hello?", Say: "Thanks, goodbye!"}, false, HangupTooEarly},
		{HangupCheck{CallerTurns: 1, LastCaller: "Sure, 7 pm is fine.", Say: "Thank you, goodbye!", Outcome: OutcomeBooked}, true, HangupFinished},
		{HangupCheck{CallerTurns: 3, LastCaller: "Any dietary needs?", Say: "No, thank you. Would 8 work?", Outcome: OutcomeBooked}, false, HangupNoGoodbye},
		{HangupCheck{CallerTurns: 3, LastCaller: "Anything else?", Say: "No, that's all, thank you.", Outcome: OutcomeNeedsYou}, true, HangupFinished},
		{HangupCheck{CallerTurns: 2, LastCaller: "好的，帮您约了十点半。", Say: "好的，谢谢您，再见。"}, true, HangupFinished},
		{HangupCheck{CallerTurns: 2, LastCaller: "请问贵姓？", Say: "我叫李明。", Outcome: OutcomeNotDone}, false, HangupNoGoodbye},
		{HangupCheck{CallerTurns: 1, LastCaller: "Hold on.", Say: "Sure.", WrapUp: true}, true, HangupWrapUp},
	} {
		ok, why := AllowHangup(c.check)
		if ok != c.want || why != c.why {
			t.Errorf("%+v: %v %s, want %v %s", c.check, ok, why, c.want, c.why)
		}
	}
	for s, want := range map[string]bool{"Bye!": true, "Okay, bye-bye": true, "See you Friday.": true, "Have a great day": true, "明天见。": true,
		"What's the byelaw?": false, "Good evening, Luigi's": false, "Thanks, how much is it?": false} {
		if IsGoodbye(s) != want && Closing(s) != want {
			t.Errorf("goodbye %q: want %v", s, want)
		}
	}
}

// TestReplyParserInlineHangup: an end-of-call token inside speech is not
// spoken but noted.
func TestReplyParserInlineHangup(t *testing.T) {
	for _, step := range []int{1, 4, 1000} {
		for in, want := range map[string]string{
			"SAY: Thanks so much. Goodbye! HANGUP":      "Thanks so much.|Goodbye!",
			"SAY: Thank you, bye. [HANGUP]":             "Thank you, bye.",
			"好的，谢谢您，再见。HANGUP":                          "好的，谢谢您，再见。",
			"SAY: Goodbye!\n[END_CALL]":                 "Goodbye!",
			"SAY: Please don't hang up, I'll be quick.": "Please don't hang up, I'll be quick.",
		} {
			var p ReplyParser
			got := feed(&p, in, step)
			rest, r := p.Finish()
			got = append(got, rest...)
			if strings.Join(got, "|") != want {
				t.Errorf("step %d %q: said %q", step, in, got)
			}
			if r.Hangup != !strings.Contains(in, "hang up") {
				t.Errorf("step %d %q: hangup %v", step, in, r.Hangup)
			}
		}
	}
}
