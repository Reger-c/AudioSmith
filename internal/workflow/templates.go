package workflow

import (
	"time"

	"github.com/hehehai/AudioSmith/internal/i18n"
)

// Template is a ready-made workflow.
type Template struct {
	ID string
	// Name and About are English; the UI translates them (i18n.T).
	Name, About string
	build       func(w *Workflow, o TemplateOptions)
}

// TemplateOptions fill in what a template leaves to the app.
type TemplateOptions struct {
	Model    string // the TTS model of Speak and Convert steps
	Voice    string // their voice ("" = the model's default)
	Language string // their language ("" = auto)
}

// Templates are AudioSmith's templates, in the library's order.
var Templates = []Template{
	{ID: "narration", Name: "Batch narration", About: "Scripts → speech, one file each.", build: func(w *Workflow, o TemplateOptions) {
		s := w.add(KindStart, 0)
		w.Node(s).P.Scripts = []Script{{Text: i18n.T("Welcome to this lesson. Let's get started.")}}
		w.chain(s, w.speak(1, o), w.add(KindExport, 2))
	}},
	{ID: "narration-normalize", Name: "Narration + normalize", About: "Scripts → speech at an even loudness.", build: func(w *Workflow, o TemplateOptions) {
		s := w.add(KindStart, 0)
		w.Node(s).P.Scripts = []Script{{Text: i18n.T("Welcome to this lesson. Let's get started.")}}
		w.chain(s, w.speak(1, o), w.add(KindNormalize, 2), w.add(KindExport, 3))
	}},
	{ID: "transcript", Name: "Audio to transcript", About: "Recordings → text files.", build: func(w *Workflow, o TemplateOptions) {
		w.chain(w.add(KindSource, 0), w.add(KindTranscribe, 1), w.add(KindExport, 2))
	}},
	{ID: "change-voice", Name: "Change the voice", About: "Recordings → the same words in another voice.", build: func(w *Workflow, o TemplateOptions) {
		c := w.add(KindConvert, 1)
		n := w.Node(c)
		n.P.Model, n.P.Voice, n.P.Language = o.Model, o.Voice, o.Language
		w.chain(w.add(KindSource, 0), c, w.add(KindExport, 2))
	}},
	{ID: "translate-narrate", Name: "Translate and narrate", About: "Recordings → transcribed, translated and spoken.", build: func(w *Workflow, o TemplateOptions) {
		tr := w.add(KindTranslate, 2)
		w.Node(tr).P.To = DefaultParams(KindTranslate).To
		sp := w.speak(3, o)
		w.Node(sp).P.Language = DefaultParams(KindTranslate).To
		w.chain(w.add(KindSource, 0), w.add(KindTranscribe, 1), tr, sp, w.add(KindExport, 4))
	}},
	{ID: "subtitles", Name: "Transcript → subtitles", About: "Recordings → SubRip subtitles.", build: func(w *Workflow, o TemplateOptions) {
		e := w.add(KindExport, 2)
		w.Node(e).P.Format = FormatSRT
		w.chain(w.add(KindSource, 0), w.add(KindTranscribe, 1), e)
	}},
}

// TemplateByID returns a template, or false.
func TemplateByID(id string) (Template, bool) {
	for _, t := range Templates {
		if t.ID == id {
			return t, true
		}
	}
	return Template{}, false
}

// Make builds a workflow from the template.
func (t Template) Make(id, name string, now time.Time, o TemplateOptions) *Workflow {
	w := &Workflow{Version: Version, ID: id, Name: name, CreatedAt: now, UpdatedAt: now, View: View{Zoom: 1}}
	t.build(w, o)
	return w
}

// Column spacing of templates and new steps.
const (
	GridX = 280
	GridY = 170
)

func (w *Workflow) add(k Kind, col int) string {
	return w.AddNode(k, 40+float64(col)*GridX, 80)
}

func (w *Workflow) speak(col int, o TemplateOptions) string {
	id := w.add(KindSpeak, col)
	n := w.Node(id)
	n.P.Model, n.P.Voice, n.P.Language = o.Model, o.Voice, o.Language
	return id
}

func (w *Workflow) chain(ids ...string) {
	for i := 1; i < len(ids); i++ {
		w.Connect(ids[i-1], PortOut, ids[i], PortIn)
	}
}

// FreeSpot is where a new step goes: the first cell of a three-column
// grid that no step covers.
func (w *Workflow) FreeSpot() (float64, float64) {
	for i := 0; ; i++ {
		x, y := 40+float64(i%3)*GridX, 80+float64(i/3)*GridY
		free := true
		for _, n := range w.Nodes {
			if n.X > x-GridX/2 && n.X < x+GridX/2 && n.Y > y-GridY/2 && n.Y < y+GridY/2 {
				free = false
				break
			}
		}
		if free {
			return x, y
		}
	}
}
