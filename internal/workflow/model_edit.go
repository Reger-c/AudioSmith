package workflow

// Building and editing a workflow: New, finding nodes and links, adding,
// removing and connecting them, cloning and duplicating, and Normalize;
// split from model.go.

import (
	"fmt"
	"math"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

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
