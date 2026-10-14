package workflow

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// StepStatus is where one step is for one item.
type StepStatus string

const (
	StepPending StepStatus = ""
	StepRunning StepStatus = "running"
	StepDone    StepStatus = "done"
	StepFailed  StepStatus = "failed"
	// StepSkipped: the item did not reach the step (the other branch of
	// a condition, or a step before it failed: Code says which).
	StepSkipped StepStatus = "skipped"
)

// Skip codes (Step.Code of skipped steps).
const (
	SkipBranch   = "branch"   // the other branch of a condition
	SkipUpstream = "upstream" // a step before it failed
)

// Value is a step's result for an item.
type Value struct {
	Type Type `json:"type"`
	// File holds the value: audio (WAV), text (UTF-8) or a transcript
	// (JSON, core.Transcript); relative to the run's folder, or absolute
	// for files exported elsewhere.
	File string `json:"file,omitempty"`
	// Text is the start of a text or transcript, to show.
	Text    string  `json:"text,omitempty"`
	Chars   int     `json:"chars,omitempty"`
	Seconds float64 `json:"seconds,omitempty"` // audio, transcripts
	Lang    string  `json:"lang,omitempty"`
}

// Step is one step's state for one item: the checkpoint a run keeps.
type Step struct {
	Status StepStatus `json:"status"`
	// Key is the recipe of the result (StepKeys): a result whose key
	// still matches is reused by the next run.
	Key string `json:"key"`
	Out *Value `json:"out,omitempty"`
	// Branch is the branch a condition took ("yes"/"no").
	Branch string `json:"branch,omitempty"`
	// Code and Err say why it failed (or was skipped: Code).
	Code     string    `json:"code,omitempty"`
	Err      string    `json:"err,omitempty"`
	Warnings []string  `json:"warnings,omitempty"`
	Seconds  float64   `json:"seconds,omitempty"` // how long it took
	At       time.Time `json:"at,omitzero"`
}

// Finished reports whether the step needs no more work.
func (s *Step) Finished() bool {
	return s != nil && (s.Status == StepDone || s.Status == StepSkipped)
}

// RunItem is an item's state in a run.
type RunItem struct {
	Key  string `json:"key"`
	Name string `json:"name"`
	// Path is a clip's file (Source).
	Path  string           `json:"path,omitempty"`
	Steps map[string]*Step `json:"steps"`
}

// ItemFile is where a run keeps a script item's text, in its folder.
func ItemFile(key string) string { return "items/" + key + ".txt" }

// RunState is a run's state.
type RunState string

const (
	RunIdle      RunState = ""          // never ran
	RunRunning   RunState = "running"   // jobs are queued or running
	RunDone      RunState = "done"      // every item went through
	RunFailed    RunState = "failed"    // some items failed
	RunCancelled RunState = "cancelled" // stopped by the user
)

// Run is a workflow's last run, with every item's checkpoints.
type Run struct {
	ID string `json:"id"`
	// Signature sums up what the run was asked to make (RunSignature):
	// a workflow whose signature differs has changed since.
	Signature string    `json:"signature"`
	State     RunState  `json:"state"`
	StartedAt time.Time `json:"startedAt,omitzero"`
	EndedAt   time.Time `json:"endedAt,omitzero"`
	Items     []RunItem `json:"items"`
	// Steps are the step ids the run made jobs for.
	Steps []string `json:"steps,omitempty"`
	// Code and Err say why the run stopped before its end (a model not
	// downloaded, the engine gone…).
	Code string `json:"code,omitempty"`
	Err  string `json:"err,omitempty"`
}

// Item returns the run's item with key, or nil.
func (r *Run) Item(key string) *RunItem {
	if r == nil {
		return nil
	}
	for i := range r.Items {
		if r.Items[i].Key == key {
			return &r.Items[i]
		}
	}
	return nil
}

// Clone deep-copies the run.
func (r *Run) Clone() *Run {
	if r == nil {
		return nil
	}
	c := *r
	c.Steps = slices.Clone(r.Steps)
	c.Items = make([]RunItem, len(r.Items))
	for i, it := range r.Items {
		steps := make(map[string]*Step, len(it.Steps))
		for k, s := range it.Steps {
			s2 := *s
			if s.Out != nil {
				o := *s.Out
				s2.Out = &o
			}
			s2.Warnings = slices.Clone(s.Warnings)
			steps[k] = &s2
		}
		it.Steps = steps
		c.Items[i] = it
	}
	return &c
}

// ItemState sums up an item: running, failed, done, or pending ("").
func (it *RunItem) State() StepStatus {
	if len(it.Steps) == 0 {
		return StepPending
	}
	done := true
	failed := false
	for _, s := range it.Steps {
		switch s.Status {
		case StepRunning:
			return StepRunning
		case StepFailed:
			failed = true
		case StepDone, StepSkipped:
		default:
			done = false
		}
	}
	switch {
	case failed:
		return StepFailed
	case done:
		return StepDone
	}
	return StepPending
}

// Counts are how many items are done and failed, of all.
func (r *Run) Counts() (done, failed, total int) {
	if r == nil {
		return 0, 0, 0
	}
	for i := range r.Items {
		switch r.Items[i].State() {
		case StepDone:
			done++
		case StepFailed:
			failed++
		}
	}
	return done, failed, len(r.Items)
}

// Salt adds what else a step's result depends on, besides its settings
// and inputs (the speech-to-text model of Settings, the translator…).
type Salt func(n *Node) string

// StepKeys are the recipe keys of every step reachable from the input,
// for every item: a hash of the step's kind and settings, salt, and the
// keys of the steps feeding it (the input step's: the item's content).
// A result whose key is unchanged was made from the same recipe.
func StepKeys(w *Workflow, rep *Report, items []Item, salt Salt) map[string]map[string]string {
	keys := map[string]map[string]string{}
	settings := map[string]string{}
	for _, id := range rep.Order {
		n := w.Node(id)
		settings[id] = recipe(w, n, salt)
		keys[id] = map[string]string{}
	}
	for _, it := range items {
		for _, id := range rep.Order {
			n := w.Node(id)
			h := sha256.New()
			fmt.Fprintf(h, "%s\x00%s\x00", n.Kind, settings[id])
			if n.Kind.IsInput() {
				fmt.Fprintf(h, "item\x00%s\x00", it.Key)
			}
			for _, e := range w.Incoming(id) {
				fmt.Fprintf(h, "%s\x00%s\x00", e.FromPort, keys[e.From][it.Key])
			}
			keys[id][it.Key] = hex.EncodeToString(h.Sum(nil)[:12])
		}
	}
	return keys
}

// recipe is what of a step's settings changes its results.
func recipe(w *Workflow, n *Node, salt Salt) string {
	p := n.P
	p.Scripts, p.Clips = nil, nil
	if n.Kind != KindExport {
		p.Folder, p.Template, p.Format = "", "", ""
	}
	data, _ := json.Marshal(p)
	s := string(data)
	if n.Kind == KindExport {
		// The file names use them.
		s += "\x00" + w.Name + "\x00" + n.Name
	}
	if salt != nil {
		s += "\x00" + salt(n)
	}
	return s
}

// RunSignature sums up the keys of every step for every item.
func RunSignature(keys map[string]map[string]string, items []Item) string {
	h := sha256.New()
	ids := make([]string, 0, len(keys))
	for id := range keys {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	for _, it := range items {
		fmt.Fprintf(h, "%s\x00", it.Key)
		for _, id := range ids {
			fmt.Fprintf(h, "%s=%s\x00", id, keys[id][it.Key])
		}
	}
	return hex.EncodeToString(h.Sum(nil)[:12])
}

// Plan makes the next run from the last one: an item per input item, in
// order, keeping each step whose result is still good (finished, same
// key, its file still there, and nothing before it running again) and
// resetting the others. force starts over. It returns the run and, in
// run order, the steps that have work.
func Plan(w *Workflow, rep *Report, items []Item, keys map[string]map[string]string, prev *Run, force bool, exists func(*Value) bool) (*Run, []string) {
	run := &Run{Signature: RunSignature(keys, items)}
	work := map[string]bool{}
	for _, it := range items {
		ri := RunItem{Key: it.Key, Name: it.Name, Path: it.Path, Steps: map[string]*Step{}}
		var old *RunItem
		if !force {
			old = prev.Item(it.Key)
		}
		redo := map[string]bool{}
		for _, id := range rep.Order {
			n := w.Node(id)
			key := keys[id][it.Key]
			if n.Kind == KindStart {
				// The script is the result.
				ri.Steps[id] = &Step{Status: StepDone, Key: key, Out: &Value{Type: TypeText, File: ItemFile(it.Key),
					Text: Excerpt(it.Text, 400), Chars: utf8.RuneCountInString(it.Text)}}
				continue
			}
			again := false
			for _, e := range w.Incoming(id) {
				if redo[e.From] {
					again = true
				}
			}
			var s *Step
			if old != nil {
				s = old.Steps[id]
			}
			keep := !again && s.Finished() && s.Key == key && (s.Status != StepDone || s.Out == nil || exists == nil || exists(s.Out))
			if keep {
				c := *s
				ri.Steps[id] = &c
				continue
			}
			redo[id] = true
			work[id] = true
			ri.Steps[id] = &Step{Key: key}
		}
		run.Items = append(run.Items, ri)
	}
	var steps []string
	for _, id := range rep.Order {
		if work[id] {
			steps = append(steps, id)
		}
	}
	run.Steps = steps
	return run, steps
}

// Evaluate reports whether a condition matches text: trimmed, ignoring
// case.
func Evaluate(m Match, want, text string) bool {
	want = strings.ToLower(strings.TrimSpace(want))
	text = strings.ToLower(strings.TrimSpace(text))
	switch m {
	case MatchEquals:
		return text == want
	case MatchPrefix:
		return strings.HasPrefix(text, want)
	case MatchSuffix:
		return strings.HasSuffix(text, want)
	}
	return strings.Contains(text, want)
}

// NameVars are the values of a file name template's tokens.
type NameVars struct {
	Workflow string
	N        int // the item's number, 1-based
	Item     string
	Step     string
	Date     time.Time
	Lang     string
}

// FileName fills a template and makes it a safe file name (without its
// extension): characters files cannot have become "_", at most 120 runes.
func FileName(template string, v NameVars) string {
	if !ValidTemplate(template) {
		template = DefaultTemplate
	}
	r := strings.NewReplacer(
		"{workflow}", v.Workflow,
		"{n}", fmt.Sprintf("%02d", v.N),
		"{item}", v.Item,
		"{step}", v.Step,
		"{date}", v.Date.Format("2006-01-02"),
		"{lang}", v.Lang,
	)
	return SafeName(r.Replace(template))
}

// SafeName replaces what file names cannot hold and caps the length.
func SafeName(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r == '/' || r == '\\' || r == ':' || r == '*' || r == '?' || r == '"' || r == '<' || r == '>' || r == '|':
			b.WriteByte('_')
		case unicode.IsControl(r):
		case r == '\n' || r == '\t':
			b.WriteByte(' ')
		default:
			b.WriteRune(r)
		}
	}
	out := strings.Trim(strings.Join(strings.Fields(b.String()), " "), " .")
	if utf8.RuneCountInString(out) > 120 {
		out = string([]rune(out)[:120])
	}
	if out == "" {
		out = "untitled"
	}
	return out
}
