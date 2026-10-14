// Package workflow is WorkflowStudio's model (M6): a workflow is a graph
// of steps (nodes) with typed inputs and outputs joined by links (edges),
// run once per input item (a script, or an audio clip). The package is
// pure: the graph, its validation, the input items, the recipe keys that
// tell which results a run can keep, and the built-in templates. The
// services run workflows (internal/services/workflow*.go); the UI draws
// and edits them (workflow*.go).
//
// Imports: lang (language codes), text (script splitting), i18n (texts of
// diagnostics).
package workflow

import (
	"fmt"
	"math"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

// Version is the document version Normalize writes.
const Version = 1

// Limits.
const (
	MaxNodes       = 100
	MaxScripts     = 50       // scripts (items) a Start step takes
	MaxScriptChars = 20000    // runes per script
	MaxClips       = 50       // audio clips a Source step takes
	MaxClipBytes   = 64 << 20 // per clip
	MaxName        = 120      // runes of a workflow's or a step's name
	MaxMatchText   = 200      // runes a condition looks for
	MaxTemplate    = 200      // runes of an export's file name template
)

// Type is the type of the value a port carries.
type Type string

const (
	TypeText       Type = "text"
	TypeAudio      Type = "audio"
	TypeTranscript Type = "transcript" // text with times (segments, words)
	// TypeSame, as an output's type, is the type of the step's input
	// (Translate, Condition).
	TypeSame Type = "same"
)

// Kind is a step's kind.
type Kind string

const (
	KindStart      Kind = "start"      // scripts → text
	KindSource     Kind = "source"     // audio clips → audio
	KindSpeak      Kind = "speak"      // text → audio (the render pipeline)
	KindTranscribe Kind = "transcribe" // audio → transcript
	KindTranslate  Kind = "translate"  // text → text, transcript → transcript
	KindConvert    Kind = "convert"    // audio → audio in another voice
	KindNormalize  Kind = "normalize"  // audio → audio at a loudness or peak level
	KindCondition  Kind = "condition"  // text → yes / no (the same text)
	KindExport     Kind = "export"     // anything → files in a folder
)

// Kinds lists the step kinds in the palette's order.
var Kinds = []Kind{KindStart, KindSource, KindSpeak, KindTranscribe, KindTranslate, KindConvert, KindNormalize, KindCondition, KindExport}

// Port names.
const (
	PortIn  = "in"
	PortOut = "out"
	PortYes = "yes"
	PortNo  = "no"
)

// Port is an input or output of a step.
type Port struct {
	Name string
	// Type is an output's type (TypeSame: the input's).
	Type Type
	// Accepts are the types an input takes.
	Accepts []Type
}

// Spec describes a kind of step.
type Spec struct {
	Kind Kind
	In   []Port // none (Start, Source) or one
	Out  []Port // none (Export), one, or yes and no (Condition)
}

var specs = map[Kind]Spec{
	KindStart:      {KindStart, nil, []Port{{Name: PortOut, Type: TypeText}}},
	KindSource:     {KindSource, nil, []Port{{Name: PortOut, Type: TypeAudio}}},
	KindSpeak:      {KindSpeak, []Port{{Name: PortIn, Accepts: []Type{TypeText, TypeTranscript}}}, []Port{{Name: PortOut, Type: TypeAudio}}},
	KindTranscribe: {KindTranscribe, []Port{{Name: PortIn, Accepts: []Type{TypeAudio}}}, []Port{{Name: PortOut, Type: TypeTranscript}}},
	KindTranslate:  {KindTranslate, []Port{{Name: PortIn, Accepts: []Type{TypeText, TypeTranscript}}}, []Port{{Name: PortOut, Type: TypeSame}}},
	KindConvert:    {KindConvert, []Port{{Name: PortIn, Accepts: []Type{TypeAudio}}}, []Port{{Name: PortOut, Type: TypeAudio}}},
	KindNormalize:  {KindNormalize, []Port{{Name: PortIn, Accepts: []Type{TypeAudio}}}, []Port{{Name: PortOut, Type: TypeAudio}}},
	KindCondition:  {KindCondition, []Port{{Name: PortIn, Accepts: []Type{TypeText, TypeTranscript}}}, []Port{{Name: PortYes, Type: TypeSame}, {Name: PortNo, Type: TypeSame}}},
	KindExport:     {KindExport, []Port{{Name: PortIn, Accepts: []Type{TypeText, TypeAudio, TypeTranscript}}}, nil},
}

// SpecOf describes a kind; ok is false for unknown kinds.
func SpecOf(k Kind) (Spec, bool) {
	s, ok := specs[k]
	return s, ok
}

// IsInput reports whether a kind brings the items (Start, Source).
func (k Kind) IsInput() bool { return k == KindStart || k == KindSource }

// Output returns the spec's output port name, or false.
func (s Spec) Output(name string) (Port, bool) {
	for _, p := range s.Out {
		if p.Name == name {
			return p, true
		}
	}
	return Port{}, false
}

// Input returns the spec's input port name, or false.
func (s Spec) Input(name string) (Port, bool) {
	for _, p := range s.In {
		if p.Name == name {
			return p, true
		}
	}
	return Port{}, false
}

// Workflow is a saved workflow: its graph and its inputs.
type Workflow struct {
	Version   int       `json:"version"`
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
	Nodes     []Node    `json:"nodes"`
	Edges     []Edge    `json:"edges"`
	// View is where the canvas looked last.
	View View `json:"view"`
	// Seq numbers new nodes and edges ("n7", "e12").
	Seq int `json:"seq"`
}

// View is a canvas's pan and zoom: the content point at the canvas's
// top-left corner, and the scale.
type View struct {
	X    float64 `json:"x"`
	Y    float64 `json:"y"`
	Zoom float64 `json:"zoom"`
}

// Zoom limits.
const (
	MinZoom = 0.25
	MaxZoom = 2.0
)

// Node is a step.
type Node struct {
	ID   string `json:"id"`
	Kind Kind   `json:"kind"`
	// Name is the user's name for the step ("" = the kind's).
	Name string  `json:"name,omitempty"`
	X    float64 `json:"x"`
	Y    float64 `json:"y"`
	P    Params  `json:"params"`
}

// Edge links an output of one step to the input of another.
type Edge struct {
	ID       string `json:"id"`
	From     string `json:"from"`
	FromPort string `json:"fromPort"`
	To       string `json:"to"`
	ToPort   string `json:"toPort"`
}

// Script is one script of a Start step. Its text may hold several
// scripts separated by lines of "---".
type Script struct {
	Name string `json:"name,omitempty"` // the imported file's name, or ""
	Text string `json:"text"`
}

// Clip is one audio file of a Source step.
type Clip struct {
	Name string `json:"name"`
	Path string `json:"path"` // absolute
	Size int64  `json:"size"`
	// Mod is the file's modification time when it was added: a changed
	// file is a new item.
	Mod time.Time `json:"mod"`
}

// Match is how a condition compares.
type Match string

const (
	MatchContains Match = "contains"
	MatchEquals   Match = "equals"
	MatchPrefix   Match = "prefix"
	MatchSuffix   Match = "suffix"
)

// Matches lists the comparisons in the inspector's order.
var Matches = []Match{MatchContains, MatchEquals, MatchPrefix, MatchSuffix}

// Normalize modes.
const (
	LevelLoudness = "loudness" // integrated LUFS, true peak under −1 dBTP
	LevelPeak     = "peak"     // sample peak, dBFS
)

// Export formats.
const (
	FormatWAV  = "wav"
	FormatM4A  = "m4a"
	FormatTXT  = "txt"
	FormatSRT  = "srt"
	FormatVTT  = "vtt"
	FormatJSON = "json"
)

// FormatsFor are the export formats of a value type.
func FormatsFor(t Type) []string {
	switch t {
	case TypeAudio:
		return []string{FormatWAV, FormatM4A}
	case TypeTranscript:
		return []string{FormatTXT, FormatSRT, FormatVTT, FormatJSON}
	case TypeText:
		return []string{FormatTXT}
	}
	return nil
}

// DefaultTemplate is an export's file name template.
const DefaultTemplate = "{workflow}-{n}-{item}"

// TemplateTokens are the tokens a file name template may use.
var TemplateTokens = []string{"{workflow}", "{n}", "{item}", "{step}", "{date}", "{lang}"}

// Params are a step's settings; each kind reads its own.
type Params struct {
	// Start.
	Scripts []Script `json:"scripts,omitempty"`
	// Source.
	Clips []Clip `json:"clips,omitempty"`
	// Speak, Convert: the TTS model (repo), the voice (a saved voice's
	// id, "speaker:<id>" for the model's own, "preset:<id>"…; "" = the
	// model's default), the language (BCP-47; "" = auto) and the speed.
	Model    string  `json:"model,omitempty"`
	Voice    string  `json:"voice,omitempty"`
	Language string  `json:"language,omitempty"`
	Speed    float64 `json:"speed,omitempty"`
	// Convert: keep the source's length (the model's duration control,
	// else time-stretching).
	MatchDuration bool `json:"matchDuration,omitempty"`
	// Transcribe: the language spoken (""= detect). Translate: From
	// ("" = detect) and To.
	From string `json:"from,omitempty"`
	To   string `json:"to,omitempty"`
	// Translate: "" = the language model of Settings; "deepl", "google",
	// "microsoft".
	Translator string `json:"translator,omitempty"`
	// Normalize.
	Level  string  `json:"level,omitempty"` // LevelLoudness | LevelPeak
	Target float64 `json:"target,omitempty"`
	// Condition.
	Match Match  `json:"match,omitempty"`
	Text  string `json:"text,omitempty"`
	// Export: the folder ("" = none: the results stay in the app), the
	// file name template and the format ("" = the first for the type).
	Folder   string `json:"folder,omitempty"`
	Template string `json:"template,omitempty"`
	Format   string `json:"format,omitempty"`
}

// Defaults of the normalize step.
const (
	DefaultLoudness = -16.0
	DefaultPeak     = -2.0
	MinLoudness     = -30.0
	MaxLoudness     = -10.0
	MinPeak         = -24.0
	MaxPeak         = -1.0
	MinSpeed        = 0.5
	MaxSpeed        = 2.0
)

// DefaultParams are a new step's settings.
func DefaultParams(k Kind) Params {
	switch k {
	case KindSpeak:
		return Params{Speed: 1}
	case KindConvert:
		return Params{Speed: 1}
	case KindTranslate:
		return Params{To: "es"}
	case KindNormalize:
		return Params{Level: LevelLoudness, Target: DefaultLoudness}
	case KindCondition:
		return Params{Match: MatchContains}
	case KindExport:
		return Params{Template: DefaultTemplate}
	}
	return Params{}
}

// New makes an empty workflow: Start → Speak → Export.
func New(id, name string, now time.Time) *Workflow {
	w := &Workflow{Version: Version, ID: id, Name: name, CreatedAt: now, UpdatedAt: now, View: View{Zoom: 1}}
	s := w.AddNode(KindStart, 40, 80)
	w.Node(s).P.Scripts = []Script{{}}
	sp := w.AddNode(KindSpeak, 320, 80)
	e := w.AddNode(KindExport, 600, 80)
	w.Connect(s, PortOut, sp, PortIn)
	w.Connect(sp, PortOut, e, PortIn)
	return w
}

// Node returns the node id, or nil.
func (w *Workflow) Node(id string) *Node {
	for i := range w.Nodes {
		if w.Nodes[i].ID == id {
			return &w.Nodes[i]
		}
	}
	return nil
}

// Edge returns the edge id, or nil.
func (w *Workflow) Edge(id string) *Edge {
	for i := range w.Edges {
		if w.Edges[i].ID == id {
			return &w.Edges[i]
		}
	}
	return nil
}

func (w *Workflow) nextID(prefix string) string {
	w.Seq++
	return fmt.Sprintf("%s%d", prefix, w.Seq)
}

// AddNode adds a step of kind k at x, y with its default settings and
// returns its id ("" past MaxNodes).
func (w *Workflow) AddNode(k Kind, x, y float64) string {
	if len(w.Nodes) >= MaxNodes {
		return ""
	}
	n := Node{ID: w.nextID("n"), Kind: k, X: x, Y: y, P: DefaultParams(k)}
	w.Nodes = append(w.Nodes, n)
	return n.ID
}

// RemoveNode removes a step and its links.
func (w *Workflow) RemoveNode(id string) {
	w.Nodes = slices.DeleteFunc(w.Nodes, func(n Node) bool { return n.ID == id })
	w.Edges = slices.DeleteFunc(w.Edges, func(e Edge) bool { return e.From == id || e.To == id })
}

// RemoveEdge removes a link.
func (w *Workflow) RemoveEdge(id string) {
	w.Edges = slices.DeleteFunc(w.Edges, func(e Edge) bool { return e.ID == id })
}

// Connect links from's output port to to's input port and returns the
// link's id. It refuses ("" and why) links to the step itself, between
// ports that do not exist, and duplicates. Types are checked by Validate,
// so that a link can be made before the step feeding it is set up.
func (w *Workflow) Connect(from, fromPort, to, toPort string) (string, error) {
	if from == to {
		return "", fmt.Errorf("a step cannot feed itself")
	}
	a, b := w.Node(from), w.Node(to)
	if a == nil || b == nil {
		return "", fmt.Errorf("no such step")
	}
	sa, _ := SpecOf(a.Kind)
	sb, _ := SpecOf(b.Kind)
	if _, ok := sa.Output(fromPort); !ok {
		return "", fmt.Errorf("%s has no output %q", a.Kind, fromPort)
	}
	if _, ok := sb.Input(toPort); !ok {
		return "", fmt.Errorf("%s has no input %q", b.Kind, toPort)
	}
	for _, e := range w.Edges {
		if e.From == from && e.FromPort == fromPort && e.To == to && e.ToPort == toPort {
			return e.ID, nil
		}
	}
	e := Edge{ID: w.nextID("e"), From: from, FromPort: fromPort, To: to, ToPort: toPort}
	w.Edges = append(w.Edges, e)
	return e.ID, nil
}

// Incoming are the links into node id, in order.
func (w *Workflow) Incoming(id string) []Edge {
	var out []Edge
	for _, e := range w.Edges {
		if e.To == id {
			out = append(out, e)
		}
	}
	return out
}

// Outgoing are the links out of node id, in order.
func (w *Workflow) Outgoing(id string) []Edge {
	var out []Edge
	for _, e := range w.Edges {
		if e.From == id {
			out = append(out, e)
		}
	}
	return out
}

// InputNode is the workflow's Start or Source step (the first), or nil.
func (w *Workflow) InputNode() *Node {
	for i := range w.Nodes {
		if w.Nodes[i].Kind.IsInput() {
			return &w.Nodes[i]
		}
	}
	return nil
}

// Clone deep-copies the workflow.
func (w *Workflow) Clone() *Workflow {
	c := *w
	c.Nodes = make([]Node, len(w.Nodes))
	for i, n := range w.Nodes {
		n.P.Scripts = slices.Clone(n.P.Scripts)
		n.P.Clips = slices.Clone(n.P.Clips)
		c.Nodes[i] = n
	}
	c.Edges = slices.Clone(w.Edges)
	return &c
}

// Duplicate copies a workflow under a new id and name.
func (w *Workflow) Duplicate(id, name string, now time.Time) *Workflow {
	c := w.Clone()
	c.ID, c.Name, c.CreatedAt, c.UpdatedAt = id, name, now, now
	return c
}

// CopyName is the first of "base (2)", "base (3)"… not in taken.
func CopyName(base string, taken []string) string {
	base = strings.TrimSpace(base)
	// "Narration (2)" copies as "Narration (3)", not "Narration (2) (2)".
	if i := strings.LastIndex(base, " ("); i > 0 && strings.HasSuffix(base, ")") {
		var n int
		if _, err := fmt.Sscanf(base[i:], " (%d)", &n); err == nil && n > 1 {
			base = base[:i]
		}
	}
	for n := 2; ; n++ {
		name := fmt.Sprintf("%s (%d)", base, n)
		if !slices.Contains(taken, name) {
			return name
		}
	}
}

// Normalize makes a document read from storage safe to use: unknown
// kinds, links to missing steps or ports and duplicates go; strings,
// numbers and lists are clamped to their limits; missing defaults are
// filled. It never fails.
func (w *Workflow) Normalize() {
	w.Version = Version
	w.Name = clampRunes(strings.TrimSpace(w.Name), MaxName)
	if !finite(w.View.X) || !finite(w.View.Y) {
		w.View.X, w.View.Y = 0, 0
	}
	if !finite(w.View.Zoom) || w.View.Zoom == 0 {
		w.View.Zoom = 1
	}
	w.View.Zoom = clamp(w.View.Zoom, MinZoom, MaxZoom)
	seen := map[string]bool{}
	nodes := w.Nodes[:0]
	for _, n := range w.Nodes {
		if _, ok := specs[n.Kind]; !ok || n.ID == "" || seen[n.ID] || len(nodes) >= MaxNodes {
			continue
		}
		seen[n.ID] = true
		if !finite(n.X) || !finite(n.Y) {
			n.X, n.Y = 0, 0
		}
		n.Name = clampRunes(strings.TrimSpace(n.Name), MaxName)
		n.P.normalize(n.Kind)
		nodes = append(nodes, n)
	}
	w.Nodes = nodes
	edges := w.Edges[:0]
	ids := map[string]bool{}
	type link struct{ a, ap, b, bp string }
	links := map[link]bool{}
	for _, e := range w.Edges {
		a, b := w.Node(e.From), w.Node(e.To)
		if a == nil || b == nil || e.From == e.To || e.ID == "" || ids[e.ID] {
			continue
		}
		sa, _ := SpecOf(a.Kind)
		sb, _ := SpecOf(b.Kind)
		if _, ok := sa.Output(e.FromPort); !ok {
			continue
		}
		if _, ok := sb.Input(e.ToPort); !ok {
			continue
		}
		l := link{e.From, e.FromPort, e.To, e.ToPort}
		if links[l] {
			continue
		}
		ids[e.ID], links[l] = true, true
		edges = append(edges, e)
	}
	w.Edges = edges
	// Seq stays ahead of every id.
	for _, n := range w.Nodes {
		w.Seq = max(w.Seq, idNumber(n.ID))
	}
	for _, e := range w.Edges {
		w.Seq = max(w.Seq, idNumber(e.ID))
	}
}

func idNumber(id string) int {
	var n int
	if len(id) > 1 {
		fmt.Sscanf(id[1:], "%d", &n)
	}
	return n
}

func (p *Params) normalize(k Kind) {
	if len(p.Scripts) > MaxScripts {
		p.Scripts = p.Scripts[:MaxScripts]
	}
	for i := range p.Scripts {
		p.Scripts[i].Name = clampRunes(p.Scripts[i].Name, MaxName)
		// Over-long scripts are kept (Validate says so) up to a hard cap,
		// so that an import can be trimmed by hand.
		p.Scripts[i].Text = clampRunes(p.Scripts[i].Text, 4*MaxScriptChars)
	}
	if len(p.Clips) > MaxClips {
		p.Clips = p.Clips[:MaxClips]
	}
	p.Model = clampRunes(p.Model, 300)
	p.Voice = clampRunes(p.Voice, 300)
	p.Language = clampRunes(p.Language, 40)
	p.From = clampRunes(p.From, 40)
	p.To = clampRunes(p.To, 40)
	switch k {
	case KindSpeak, KindConvert:
		if !finite(p.Speed) || p.Speed == 0 {
			p.Speed = 1
		}
		p.Speed = clamp(p.Speed, MinSpeed, MaxSpeed)
	case KindNormalize:
		if p.Level != LevelPeak {
			p.Level = LevelLoudness
		}
		if !finite(p.Target) || p.Target == 0 {
			p.Target = DefaultLoudness
			if p.Level == LevelPeak {
				p.Target = DefaultPeak
			}
		}
		if p.Level == LevelPeak {
			p.Target = clamp(p.Target, MinPeak, MaxPeak)
		} else {
			p.Target = clamp(p.Target, MinLoudness, MaxLoudness)
		}
	case KindCondition:
		if !slices.Contains(Matches, p.Match) {
			p.Match = MatchContains
		}
		p.Text = clampRunes(p.Text, MaxMatchText)
	case KindExport:
		p.Template = clampRunes(p.Template, MaxTemplate)
		if strings.TrimSpace(p.Template) == "" {
			p.Template = DefaultTemplate
		}
	}
}

// StepName is a step's display name: its own, or label (the kind's,
// translated by the caller).
func (n *Node) StepName(label string) string {
	if n.Name != "" {
		return n.Name
	}
	return label
}

func clampRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n])
}

func clamp(v, lo, hi float64) float64 { return math.Min(math.Max(v, lo), hi) }

func finite(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }
