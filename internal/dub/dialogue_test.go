package dub

import (
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"testing"
	"time"

	"github.com/hehehai/AudioSmith/internal/dialogue"
)

// conversation is a dub of n lines by S1 and S2 taking turns, 2 s each
// with 0.5 s between them, translated into zh-Hans.
func conversation(n int) *Project {
	p := New("d1", time.Unix(0, 0))
	p.SourceLang = "en"
	p.ChooseTargets([]string{"zh-Hans"})
	at := 0.5
	for i := range n {
		s := p.NewSegment(at, at+2, fmt.Sprintf("S%d", i%2+1), fmt.Sprintf("Line %d.", i+1))
		s.Lines = map[string]Line{"zh-Hans": {Text: fmt.Sprintf("第%d句话。", i+1)}}
		p.Segments = append(p.Segments, s)
		at += 2.5
	}
	p.Source.Duration = at + 1
	return p
}

func stretchIDs(sts []Stretch) [][]string {
	var out [][]string
	for _, st := range sts {
		out = append(out, st.IDs)
	}
	return out
}

var soulx = dialogue.LimitsFor("soulx_podcast")

func TestDialogueStretches(t *testing.T) {
	p := conversation(6)
	sts := p.DialogueStretches("zh-Hans", soulx)
	if len(sts) != 1 || len(sts[0].IDs) != 6 || sts[0].N != 1 || !slices.Equal(sts[0].Voices, []string{"auto:S1", "auto:S2"}) {
		t.Fatalf("one conversation: %+v", sts)
	}
	// A silence over 4 s ends a stretch; the second half is still a conversation.
	q := conversation(6)
	for i := 3; i < 6; i++ {
		q.Segments[i].Start += 5
		q.Segments[i].End += 5
	}
	got := stretchIDs(q.DialogueStretches("zh-Hans", soulx))
	if !slices.EqualFunc(got, [][]string{{"s1", "s2", "s3"}, {"s4", "s5", "s6"}}, slices.Equal) {
		t.Errorf("gap: %v", got)
	}
	// NoDialogue and another language end it; a line without text does not.
	q = conversation(9)
	q.Segments[3].NoDialogue = true
	q.Segments[5].Lines = nil // not translated: not said
	got = stretchIDs(q.DialogueStretches("zh-Hans", soulx))
	if !slices.EqualFunc(got, [][]string{{"s1", "s2", "s3"}, {"s5", "s7", "s8", "s9"}}, slices.Equal) {
		t.Errorf("no dialogue / no text: %v", got)
	}
	q.Segments[3].NoDialogue = false
	q.Segments[3].Lang = "en" // kept in the source's language
	if got2 := stretchIDs(q.DialogueStretches("zh-Hans", soulx)); !slices.EqualFunc(got2, got, slices.Equal) {
		t.Errorf("another language: %v", got2)
	}
	// One speaker: no conversation.
	m := conversation(4)
	for i := range m.Segments {
		m.Segments[i].Speaker = "S1"
	}
	if sts := m.DialogueStretches("zh-Hans", soulx); len(sts) != 0 {
		t.Errorf("monologue: %v", stretchIDs(sts))
	}
	// Two labels cast with one voice are one voice.
	m = conversation(4)
	m.Cast = map[string]string{"S1": "v1", "S2": "v1"}
	if sts := m.DialogueStretches("zh-Hans", soulx); len(sts) != 0 {
		t.Errorf("one voice: %v", stretchIDs(sts))
	}
	// A monologue of 4 lines in a row is cut out.
	m = conversation(8)
	for _, i := range []int{2, 3, 4, 5} {
		m.Segments[i].Speaker = "S1"
	}
	got = stretchIDs(m.DialogueStretches("zh-Hans", soulx))
	if len(got) != 0 {
		// s1 s2 | s3–s6 (S1) | s7 s8: neither side changes speaker twice.
		t.Errorf("monologue cut: %v", got)
	}
	// Out of order segments group in time order.
	o := conversation(4)
	o.Segments[0], o.Segments[1] = o.Segments[1], o.Segments[0]
	if got := stretchIDs(o.DialogueStretches("zh-Hans", soulx)); len(got) != 1 || !slices.Equal(got[0], []string{"s1", "s2", "s3", "s4"}) {
		t.Errorf("order: %v", got)
	}
	// The limits: 16 lines at most.
	l := conversation(20)
	if got := l.DialogueStretches("zh-Hans", soulx); len(got) != 2 || len(got[0].IDs) != 16 || got[1].N != 2 {
		t.Errorf("limits: %v", stretchIDs(got))
	}
	if st, ok := StretchOf(l.DialogueStretches("zh-Hans", soulx), "s18"); !ok || st.N != 2 || st.Index("s18") != 1 {
		t.Errorf("StretchOf: %+v %v", st, ok)
	}
}

func TestDialogueBlocked(t *testing.T) {
	langs := []string{"zh", "en"}
	p := conversation(4)
	for _, c := range []struct {
		lang, want string
		edit       func(*Project)
	}{
		{"zh-Hans", "", nil},
		{"zh-Hant", "", nil},
		{"en-GB", "", nil},
		{"ja", DialogueWhyLanguage, nil},
		{"zh-Hans", DialogueWhyOff, func(p *Project) { p.Settings.Dialogue = "" }},
		{"zh-Hans", DialogueWhyOff, func(p *Project) { p.Settings.Dialogue = DialogueOff }},
		{"zh-Hans", DialogueWhyVoiceMatch, func(p *Project) { p.Settings.VoiceMatch = MatchLine }},
		{"zh-Hans", DialogueWhySpeakers, func(p *Project) {
			for i := range p.Segments {
				p.Segments[i].Speaker = "S1"
			}
		}},
	} {
		q := p.Clone()
		if c.edit != nil {
			c.edit(q)
		}
		if got := q.DialogueBlocked(c.lang, langs); got != c.want {
			t.Errorf("%s: %q, want %q", c.lang, got, c.want)
		}
	}
}

// New projects ask for the dialogue model; older documents (no field) do not.
func TestDialogueDefault(t *testing.T) {
	if !New("x", time.Now()).Settings.DialogueOn() {
		t.Error("a new project should be auto")
	}
	var old Project
	if err := json.Unmarshal([]byte(`{"version":1,"id":"o","settings":{"model":"m","keepBackground":true,"review":true,"export":{}}}`), &old); err != nil {
		t.Fatal(err)
	}
	old.Normalize()
	if old.Settings.DialogueOn() || old.Settings.Dialogue != "" {
		t.Errorf("an old project: %q", old.Settings.Dialogue)
	}
	// A line rendered before stays as it was written.
	b, _ := json.Marshal(Render{Key: "k", File: "f.wav", Raw: 1, Length: 1, Start: 2, Verdict: VerdictOK, Text: "t"})
	if string(b) != `{"key":"k","file":"f.wav","raw":1,"length":1,"start":2,"verdict":"ok","text":"t"}` {
		t.Errorf("render JSON: %s", b)
	}
}

func TestDialogueRefLines(t *testing.T) {
	p := New("r", time.Unix(0, 0))
	add := func(start, end float64, sp string) {
		p.Segments = append(p.Segments, p.NewSegment(start, end, sp, fmt.Sprintf("%s %.0f.", sp, start)))
	}
	add(0, 3, "S1")     // 0: in the reference
	add(3.2, 6, "S1")   // 1: in the reference
	add(6.5, 8, "S2")   // 2
	add(9, 13, "S1")    // 3: longest of the rest
	add(14, 14.5, "S1") // 4: too short
	add(15, 17, "S1")   // 5
	add(16.5, 18, "S2") // 6: overlaps 5
	add(20, 22.5, "S1") // 7
	ref := &Reference{Start: 0, End: 6}
	got := DialogueRefLines(p.Segments, "S1", ref)
	// 3 + 2.8 + 4 (+ gaps) = 10.3 s: the reference's lines, then the longest.
	if !slices.Equal(got, []int{0, 1, 3}) {
		t.Fatalf("lines %v", got)
	}
	if txt := DialogueRefText(p.Segments, got); txt != "S1 0. S1 3. S1 9." {
		t.Errorf("text %q", txt)
	}
	// Never past 14 s: a 4 s line that would pass it is left for a shorter one.
	p.Segments[7].End = 21
	p.Segments[3].End = 11.5 // 2.5 s
	got = DialogueRefLines(p.Segments, "S1", ref)
	total := 0.0
	for _, i := range got {
		total += p.Segments[i].Duration()
	}
	if total > DialogueRefMax || total < 8 || slices.Contains(got, 4) || slices.Contains(got, 5) {
		t.Errorf("lines %v (%.1f s)", got, total)
	}
	if got := DialogueRefLines(p.Segments, "S9", nil); got != nil {
		t.Errorf("nobody: %v", got)
	}
}

func TestDialogueKeysAndRate(t *testing.T) {
	a, b := DialogueKey("fp1", "st1"), DialogueKey("fp1", "st2")
	if a == b || a == DialogueKey("fp2", "st1") || len(a) != 24 {
		t.Errorf("keys %s %s", a, b)
	}
	if DialogueSeed("s1", 0) == DialogueSeed("s1", 1) || DialogueSeed("s1", 0) == DialogueSeed("s2", 0) || DialogueSeed("s1", 0) != DialogueSeed("s1", 0) {
		t.Error("seeds")
	}
	if r := FitRate(1.08, 0.5); math.Abs(r-1.04) > 1e-9 {
		t.Errorf("rate %v", r)
	}
	if FitRate(0, 1) != 1 || FitRate(1.1, 0) != 1 || FitRate(1.1, 2) != 1.1 {
		t.Error("rate bounds")
	}
	p := conversation(6)
	p.Segments[2].Lines = nil
	if s := p.DialogueShare("zh-Hans", soulx); s != 1 {
		t.Errorf("share with a line not translated: %v", s)
	}
	p.Segments[5].NoDialogue = true
	if s := p.DialogueShare("zh-Hans", soulx); math.Abs(s-5.0/6) > 1e-9 {
		t.Errorf("share %v", s)
	}
}
