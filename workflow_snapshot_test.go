package main

import (
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"github.com/hehehai/AudioSmith/internal/dub/dubtest"
	"github.com/hehehai/AudioSmith/internal/i18n"
	"github.com/hehehai/AudioSmith/internal/settings"
	"github.com/hehehai/AudioSmith/internal/workflow"
)

// TestSnapshotsM6 renders the Workflows page (M6) light and dark, in
// English and Chinese, as TestSnapshots does the others:
//
//	AUDIOSMITH_SNAPSHOTS=/tmp/shots go test -run TestSnapshots .
func TestSnapshotsM6(t *testing.T) {
	out := os.Getenv("AUDIOSMITH_SNAPSHOTS")
	if out == "" {
		t.Skip("set AUDIOSMITH_SNAPSHOTS to a directory")
	}
	os.MkdirAll(out, 0o755)
	t.Cleanup(func() { i18n.SetLocale("en") })
	for _, run := range []struct {
		lang string
		dark bool
	}{{"en", false}, {"en", true}, {"zh-Hans", false}, {"zh-Hans", true}} {
		i18n.SetLocale("en")
		d := newWfTest(t)
		a, tt := d.a, d.tt
		if run.lang != "en" {
			a.updateSettings(func(s *settings.Settings) { s.General.Language = run.lang })
		}
		tt.SetDark(run.dark)
		tt.SetScale(2)
		tt.SetSize(1280, 760)
		suffix := "light"
		if run.dark {
			suffix = "dark"
		}
		if run.lang != "en" {
			suffix += "-" + run.lang
		}
		save := func(name string) {
			tt.Frame()
			tt.Frame()
			f, err := os.Create(filepath.Join(out, name+"-"+suffix+".png"))
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			if err := png.Encode(f, tt.Image()); err != nil {
				t.Fatal(err)
			}
		}
		a.navigate("/workflows")
		save("workflows-empty")

		// A template, its Start step selected with two scripts.
		a.wfNew("narration-normalize")
		start := a.wf.InputNode()
		start.P.Scripts = []workflow.Script{{Text: "Welcome to the course.\n---\nLesson one: sound."}, {Name: "chapter-2.md", Text: "Lesson two: light."}}
		a.wfEdit(func(*workflow.Workflow) {})
		save("workflows-template")

		// A bigger graph: clips → transcribe → condition → translate / export.
		a.wfNew("translate-narrate")
		w := a.wf
		var src, tr, tl, sp, ex *workflow.Node
		for i := range w.Nodes {
			n := &w.Nodes[i]
			switch n.Kind {
			case workflow.KindSource:
				src = n
			case workflow.KindTranscribe:
				tr = n
			case workflow.KindTranslate:
				tl = n
			case workflow.KindSpeak:
				sp = n
			case workflow.KindExport:
				ex = n
			}
		}
		clip := filepath.Join(t.TempDir(), "interview.wav")
		dubtest.Speech(clip, 2)
		a.wfClipFiles(src.ID, []string{clip})
		a.wfEdit(func(w *workflow.Workflow) {
			for _, e := range w.Incoming(tl.ID) {
				w.RemoveEdge(e.ID)
			}
			tr.X, tr.Y, tl.X, tl.Y, sp.Y, ex.Y = 300, 80, 820, 20, 20, 20
			sp.X, ex.X = 1080, 1340
			c := w.AddNode(workflow.KindCondition, 560, 80)
			w.Node(c).P.Text = "hello"
			w.Connect(tr.ID, workflow.PortOut, c, workflow.PortIn)
			w.Connect(c, workflow.PortYes, tl.ID, workflow.PortIn)
			subs := w.AddNode(workflow.KindExport, 820, 200)
			w.Node(subs).P.Format = workflow.FormatSRT
			w.Node(subs).Name = "Subtitles"
			w.Connect(c, workflow.PortNo, subs, workflow.PortIn)
			// A step left unlinked shows its problem.
			w.AddNode(workflow.KindNormalize, 1080, 200)
		})
		a.wfSel = tl.ID
		a.wf.View = workflow.View{X: 250, Y: -40, Zoom: 0.8}
		tt.Frame()
		a.wfCanvas.fit = false
		a.wf.View = workflow.View{X: 250, Y: -40, Zoom: 0.8}
		save("workflows-graph")
		a.wfFit()

		// Linking: a link being drawn.
		a.wfCanvas.gesture, a.wfCanvas.from, a.wfCanvas.fromPort = wfGestureLink, sp.ID, workflow.PortOut
		a.wfCanvas.px, a.wfCanvas.py = sp.X+wfCardW+120, sp.Y+220
		save("workflows-linking")
		a.wfCanvas.gesture = wfGestureNone

		// Run it: the Run panel with results.
		for _, n := range a.wf.Nodes {
			if n.Kind == workflow.KindNormalize {
				id := n.ID
				a.wfEdit(func(w *workflow.Workflow) { w.RemoveNode(id) })
			}
		}
		a.wfStart(false)
		for k := range a.wfRun.Items {
			a.wfExpanded[a.wfRun.Items[k].Key] = true
		}
		save("workflows-run")

		// Speak's details.
		a.wfSel, a.wfPanel = sp.ID, wfPanelStep
		save("workflows-speak")
	}
}
