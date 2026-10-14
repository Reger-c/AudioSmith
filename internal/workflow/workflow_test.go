package workflow

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"
)

var now = time.Date(2026, 10, 14, 10, 0, 0, 0, time.UTC)

func codes(r *Report) []string {
	var out []string
	for _, d := range r.Diags {
		out = append(out, d.Code)
	}
	return out
}

func hasCode(r *Report, code, node string) bool {
	for _, d := range r.Diags {
		if d.Code == code && (node == "" || d.Node == node) {
			return true
		}
	}
	return false
}

func TestNewIsValidOnceSetUp(t *testing.T) {
	w := New("w1", "Untitled", now)
	r := Validate(w, nil)
	// A new workflow needs a script and a model.
	if !hasCode(r, CodeStartEmpty, "") || !hasCode(r, CodeNoModel, "") || r.OK() {
		t.Fatalf("new: %v", codes(r))
	}
	w.Nodes[0].P.Scripts[0].Text = "Hello.\n---\nWorld."
	w.Nodes[1].P.Model = "kokoro"
	r = Validate(w, nil)
	if !r.OK() || r.Items != 2 || len(r.Order) != 3 {
		t.Fatalf("set up: %v, items %d, order %v", codes(r), r.Items, r.Order)
	}
	if !hasCode(r, CodeNoFolder, "") {
		t.Errorf("no folder warning: %v", codes(r))
	}
	if r.In[w.Nodes[2].ID] != TypeAudio || r.Out[w.Nodes[1].ID] != TypeAudio {
		t.Errorf("types %v %v", r.In, r.Out)
	}
}

func TestValidateGraph(t *testing.T) {
	w := &Workflow{}
	if r := Validate(w, nil); !hasCode(r, CodeEmpty, "") {
		t.Fatal(codes(r))
	}
	s := w.AddNode(KindStart, 0, 0)
	w.Node(s).P.Scripts = []Script{{Text: "Hi"}}
	if r := Validate(w, nil); !hasCode(r, CodeNoSteps, "") {
		t.Error(codes(r))
	}
	tr := w.AddNode(KindTranscribe, 0, 0) // takes audio, gets text
	if _, err := w.Connect(s, PortOut, tr, PortIn); err != nil {
		t.Fatal(err)
	}
	sp := w.AddNode(KindSpeak, 0, 0)
	w.Node(sp).P.Model = "m"
	lone := w.AddNode(KindNormalize, 0, 0)
	r := Validate(w, nil)
	if !hasCode(r, CodeType, tr) || !hasCode(r, CodeUnconnected, sp) || !hasCode(r, CodeUnconnected, lone) {
		t.Errorf("types and links: %v", codes(r))
	}
	// A loop between two steps fed from nowhere else.
	a := w.AddNode(KindNormalize, 0, 0)
	b := w.AddNode(KindNormalize, 0, 0)
	w.Connect(a, PortOut, b, PortIn)
	w.Connect(b, PortOut, a, PortIn)
	r = Validate(w, nil)
	if !hasCode(r, CodeCycle, "") {
		t.Errorf("cycle: %v", codes(r))
	}
	if hasCode(r, CodeUnconnected, a) {
		t.Errorf("a step in a loop is fed: %v", r.For(a))
	}
	if _, err := w.Connect(a, PortOut, a, PortIn); err == nil {
		t.Error("a step fed itself")
	}
	if _, err := w.Connect(s, PortYes, sp, PortIn); err == nil {
		t.Error("start has no yes")
	}
	// A second input step.
	src := w.AddNode(KindSource, 0, 0)
	if r := Validate(w, nil); !hasCode(r, CodeExtraInput, src) {
		t.Errorf("extra input: %v", codes(r))
	}
}

func TestUnreachable(t *testing.T) {
	w := New("w", "x", now)
	w.Nodes[0].P.Scripts[0].Text = "a"
	w.Nodes[1].P.Model = "m"
	// Two steps feeding each other and a third: in a loop, not reached.
	a := w.AddNode(KindNormalize, 0, 0)
	b := w.AddNode(KindExport, 0, 0)
	w.Connect(a, PortOut, b, PortIn)
	c := w.AddNode(KindNormalize, 0, 0)
	w.Connect(c, PortOut, a, PortIn)
	w.Connect(a, PortOut, c, PortIn)
	r := Validate(w, nil)
	if !hasCode(r, CodeUnreachable, b) {
		t.Errorf("%v", r.Diags)
	}
}

func TestConditionBranchesAndMerges(t *testing.T) {
	w := &Workflow{}
	s := w.AddNode(KindStart, 0, 0)
	w.Node(s).P.Scripts = []Script{{Text: "Hello world"}, {Text: "Goodbye"}}
	c := w.AddNode(KindCondition, 0, 0)
	w.Node(c).P.Text = "hello"
	a := w.AddNode(KindSpeak, 0, 0)
	b := w.AddNode(KindSpeak, 0, 0)
	w.Node(a).P.Model, w.Node(b).P.Model = "m", "m"
	ex := w.AddNode(KindExport, 0, 0)
	w.Connect(s, PortOut, c, PortIn)
	w.Connect(c, PortYes, a, PortIn)
	w.Connect(c, PortNo, b, PortIn)
	w.Connect(a, PortOut, ex, PortIn)
	w.Connect(b, PortOut, ex, PortIn)
	r := Validate(w, nil)
	if !r.OK() {
		t.Fatalf("branches merging: %v", r.Diags)
	}
	if r.Out[c] != TypeText {
		t.Errorf("condition passes its type: %v", r.Out[c])
	}
	// The script also straight into the export: not an alternative.
	w.Connect(s, PortOut, ex, PortIn)
	r = Validate(w, nil)
	if !hasCode(r, CodeMerge, ex) && !hasCode(r, CodeMergeTypes, ex) {
		t.Errorf("merge: %v", codes(r))
	}
	if !Evaluate(MatchContains, " HELLO ", "Hello world") || Evaluate(MatchEquals, "hello", "hello world") ||
		!Evaluate(MatchPrefix, "hel", "Hello") || !Evaluate(MatchSuffix, "WORLD", "hello world ") {
		t.Error("evaluate")
	}
}

func TestExportFormats(t *testing.T) {
	w := New("w", "x", now)
	w.Nodes[0].P.Scripts[0].Text = "a"
	w.Nodes[1].P.Model = "m"
	w.Nodes[2].P.Format = FormatSRT // audio can't be subtitles
	w.Nodes[2].P.Template = "a/b"
	r := Validate(w, nil)
	if !hasCode(r, CodeFormat, w.Nodes[2].ID) || !hasCode(r, CodeTemplate, w.Nodes[2].ID) {
		t.Error(codes(r))
	}
	if got := FileName("{workflow}-{n}-{item}", NameVars{Workflow: "My: flow", N: 3, Item: "Hello / there?"}); got != "My_ flow-03-Hello _ there_" {
		t.Error(got)
	}
	if got := SafeName("  ..  "); got != "untitled" {
		t.Error(got)
	}
}

func TestLimits(t *testing.T) {
	w := New("w", "x", now)
	w.Nodes[1].P.Model = "m"
	var parts []string
	for i := range 51 {
		parts = append(parts, "Line "+string(rune('a'+i%26)))
	}
	w.Nodes[0].P.Scripts[0].Text = strings.Join(parts, "\n---\n")
	r := Validate(w, nil)
	if !hasCode(r, CodeStartTooMany, "") {
		t.Error(codes(r))
	}
	w.Nodes[0].P.Scripts[0].Text = strings.Repeat("x", MaxScriptChars+1)
	if r := Validate(w, nil); !hasCode(r, CodeStartTooLong, "") {
		t.Error(codes(r))
	}
	src := &Workflow{}
	id := src.AddNode(KindSource, 0, 0)
	src.Node(id).P.Clips = []Clip{{Name: "big.wav", Size: MaxClipBytes + 1}, {Name: "empty.wav"}}
	tr := src.AddNode(KindTranscribe, 0, 0)
	src.Connect(id, PortOut, tr, PortIn)
	if r := Validate(src, nil); !hasCode(r, CodeSourceLarge, id) || !hasCode(r, CodeSourceZero, id) {
		t.Error(codes(r))
	}
}

func TestCheckHook(t *testing.T) {
	w := New("w", "x", now)
	w.Nodes[0].P.Scripts[0].Text = "a"
	w.Nodes[1].P.Model = "m"
	var saw []Type
	r := Validate(w, func(n *Node, in Type) []Diagnostic {
		saw = append(saw, in)
		if n.Kind == KindSpeak {
			return []Diagnostic{{Code: "app.not_downloaded", Node: n.ID}}
		}
		return nil
	})
	if r.OK() || !slices.Contains(saw, TypeText) {
		t.Errorf("%v %v", r.Diags, saw)
	}
}

func TestItemsAndKeys(t *testing.T) {
	w := New("w", "Flow", now)
	w.Nodes[0].P.Scripts = []Script{{Text: "One.\n---\nTwo.\n  ---  \n\nOne."}, {Name: "chapter.txt", Text: "Three."}}
	w.Nodes[1].P.Model = "m"
	items := w.Items()
	if len(items) != 4 || items[3].Name != "chapter.txt" || items[1].Text != "Two." {
		t.Fatalf("%+v", items)
	}
	if items[0].Key == items[2].Key {
		t.Error("duplicates share a key")
	}
	r := Validate(w, nil)
	keys := StepKeys(w, r, items, nil)
	sp, ex := w.Nodes[1].ID, w.Nodes[2].ID
	exists := func(*Value) bool { return true }
	run, steps := Plan(w, r, items, keys, nil, false, exists)
	if len(run.Items) != 4 || !slices.Equal(steps, []string{sp, ex}) {
		t.Fatalf("first plan: %d items, steps %v", len(run.Items), steps)
	}
	// Everything done: nothing to do.
	for i := range run.Items {
		for id, s := range run.Items[i].Steps {
			if id != w.Nodes[0].ID {
				s.Status, s.Out = StepDone, &Value{Type: TypeAudio, File: "x.wav"}
			}
		}
	}
	if _, steps := Plan(w, r, items, keys, run, false, exists); len(steps) != 0 {
		t.Errorf("all done: %v", steps)
	}
	// Moving the node or renaming the Speak step changes nothing.
	w.Nodes[1].X, w.Nodes[1].Name = 500, "Narrator"
	if sig := RunSignature(StepKeys(w, Validate(w, nil), items, nil), items); sig != run.Signature {
		t.Error("layout changed the signature")
	}
	// Editing one script runs that item only; the speed, every item.
	w.Nodes[0].P.Scripts[1].Text = "Three!"
	items2 := w.Items()
	r2 := Validate(w, nil)
	run2, steps := Plan(w, r2, items2, StepKeys(w, r2, items2, nil), run, false, exists)
	pending := 0
	for _, it := range run2.Items {
		if it.Steps[sp].Status == StepPending {
			pending++
		}
	}
	if pending != 1 || len(steps) != 2 || run2.Signature == run.Signature {
		t.Errorf("one item changed: %d pending, steps %v", pending, steps)
	}
	w.Nodes[1].P.Speed = 1.2
	r3 := Validate(w, nil)
	run3, _ := Plan(w, r3, items2, StepKeys(w, r3, items2, nil), run2, false, exists)
	for _, it := range run3.Items {
		if it.Steps[sp].Status != StepPending || it.Steps[ex].Status != StepPending {
			t.Errorf("speed changed: %+v", it.Steps[sp])
		}
	}
	// A missing file runs the step and the ones after it again.
	run4, _ := Plan(w, r, items, keys, run, false, func(v *Value) bool { return false })
	if run4.Items[0].Steps[ex].Status != StepPending {
		t.Error("missing file")
	}
	// Salt: the speech-to-text model of Settings.
	k1 := StepKeys(w, r, items, func(*Node) string { return "a" })
	k2 := StepKeys(w, r, items, func(*Node) string { return "b" })
	if k1[sp][items[0].Key] == k2[sp][items[0].Key] {
		t.Error("salt")
	}
}

func TestTemplatesValidate(t *testing.T) {
	for _, tp := range Templates {
		w := tp.Make("w", tp.Name, now, TemplateOptions{Model: "m"})
		w.Normalize()
		for i := range w.Nodes {
			if w.Nodes[i].Kind == KindSource {
				w.Nodes[i].P.Clips = []Clip{{Name: "a.wav", Path: "/a.wav", Size: 10}}
			}
		}
		r := Validate(w, nil)
		if !r.OK() {
			t.Errorf("%s: %v", tp.ID, r.Diags)
		}
	}
}

func TestNormalize(t *testing.T) {
	raw := `{"name":"  x  ","nodes":[{"id":"n1","kind":"start"},{"id":"n1","kind":"speak"},{"id":"n4","kind":"bogus"},
	{"id":"n9","kind":"normalize","params":{"level":"peak","target":5}},{"id":"n3","kind":"speak","params":{"speed":9}}],
	"edges":[{"id":"e1","from":"n1","fromPort":"out","to":"n3","toPort":"in"},{"id":"e2","from":"n1","fromPort":"out","to":"n3","toPort":"in"},
	{"id":"e3","from":"n1","fromPort":"nope","to":"n3","toPort":"in"},{"id":"e4","from":"n7","fromPort":"out","to":"n3","toPort":"in"}],
	"view":{"zoom":40}}`
	var w Workflow
	if err := json.Unmarshal([]byte(raw), &w); err != nil {
		t.Fatal(err)
	}
	w.Normalize()
	if w.Name != "x" || len(w.Nodes) != 3 || len(w.Edges) != 1 || w.View.Zoom != MaxZoom || w.Seq != 9 {
		t.Fatalf("%+v", w)
	}
	if n := w.Node("n9"); n.P.Target != MaxPeak {
		t.Errorf("peak %v", n.P.Target)
	}
	if n := w.Node("n3"); n.P.Speed != MaxSpeed {
		t.Errorf("speed %v", n.P.Speed)
	}
	if id := w.AddNode(KindExport, 0, 0); id != "n10" {
		t.Errorf("next id %s", id)
	}
}

func TestDuplicateAndNames(t *testing.T) {
	w := New("w", "Flow", now)
	w.Nodes[0].P.Scripts[0].Text = "a"
	d := w.Duplicate("w2", CopyName(w.Name, []string{"Flow", "Flow (2)"}), now)
	if d.Name != "Flow (3)" || d.ID != "w2" {
		t.Fatal(d.Name)
	}
	d.Nodes[0].P.Scripts[0].Text = "b"
	if w.Nodes[0].P.Scripts[0].Text != "a" {
		t.Error("shared scripts")
	}
	if got := CopyName("Flow (3)", nil); got != "Flow (2)" {
		t.Error(got)
	}
	x, y := w.FreeSpot()
	if x == w.Nodes[0].X && y == w.Nodes[0].Y {
		t.Error("free spot taken")
	}
}
