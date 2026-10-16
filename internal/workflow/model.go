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

// The workflow model's types: types, kinds, ports and specs, the graph
// (workflow, view, nodes, links), scripts and clips, and step parameters
// with their defaults. Building and editing a workflow is in model_edit.go.

import (
	"time"
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
		return Params{To: "zh-Hans"} // dub.DefaultTarget
	case KindNormalize:
		return Params{Level: LevelLoudness, Target: DefaultLoudness}
	case KindCondition:
		return Params{Match: MatchContains}
	case KindExport:
		return Params{Template: DefaultTemplate}
	}
	return Params{}
}
