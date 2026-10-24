package main

import (
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/hehehai/AudioSmith/internal/dub"
	"github.com/hehehai/AudioSmith/internal/i18n"
	"github.com/hehehai/AudioSmith/internal/services"
	"github.com/hehehai/AudioSmith/internal/settings"
	"github.com/hehehai/AudioSmith/internal/translate"
)

// TestSnapshotsM4 renders the Dub and Batch pages (M4) light and dark,
// in English and Chinese, as TestSnapshots does the others:
//
//	AUDIOSMITH_SNAPSHOTS=/tmp/shots go test -run TestSnapshots .
func TestSnapshotsM4(t *testing.T) { runSnapshots(t, snapshotsM4) }

func snapshotsM4(t *testing.T, out string) {
	t.Cleanup(func() { i18n.SetLocale("en") })
	for _, run := range []struct {
		lang string
		dark bool
	}{{"en", false}, {"en", true}, {"zh-Hans", false}, {"zh-Hans", true}} {
		i18n.SetLocale("en")
		d := newDubTest(t, 6)
		a, tt := d.a, d.tt
		if run.lang != "en" {
			a.updateSettings(func(s *settings.Settings) { s.General.Language = run.lang })
		}
		tt.SetDark(run.dark)
		tt.SetScale(2)
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
		a.navigate("/dub")
		save("dub-empty")

		a.dubSourceFile(d.src)
		a.dubSet(func(p *dub.Project) { p.Targets = []string{"es", "fr"} })
		save("dub-source")

		a.dubStart()
		a.dubStep = dubStepTranscribe
		save("dub-transcript")

		a.dubTranslate(false)
		a.dubLang = "es"
		a.dubStep = dubStepTranslate
		// A line that would not fit, one that failed.
		a.dubSet(func(p *dub.Project) {
			l := p.Segments[1].Lines["es"]
			l.Fit = &translate.Fit{Predicted: 3.9, Slot: 2.2, Ratio: 1.77, Over: 1.7, Verdict: translate.FitOverflow}
			l.Shorter = "¿Qué tal hoy?"
			p.Segments[1].Lines["es"] = l
			p.Segments[3].Lines["es"] = dub.Line{Err: translate.CodeLineFailed}
		})
		save("dub-translate")

		a.dubStep = dubStepCast
		save("dub-cast")

		a.dubSet(func(p *dub.Project) { p.Segments[3].Lines["es"] = dub.Line{Text: "Me alegro de oírlo."} })
		a.dubGenerate()
		a.dubStep = dubStepGenerate
		a.dubOpen = a.dub.Segments[0].ID
		a.dubSeek(4)
		save("dub-generate")
		a.dubOpen = ""

		a.dubStep = dubStepExport
		save("dub-export")

		a.dubPaste.open = true
		a.dubPaste.text = "1\n00:00:00,500 --> 00:00:02,000\nHola.\n\n2\n00:00:02,800 --> 00:00:05,000\n¿Cómo estás hoy?\n"
		save("dub-paste")
		a.dubPaste = dubPasteState{}

		a.dubGloss.open = true
		a.dubSet(func(p *dub.Project) {
			p.Glossary = translate.Glossary{{Source: "AudioSmith", Target: "AudioSmith", Note: "brand"}, {Source: "dubbing", Target: "doblaje", Auto: true}}
		})
		save("dub-glossary")
		a.dubGloss.open = false

		a.navigate("/batch")
		a.batchDropped([]string{d.src})
		a.batchSetSettings(func(s *services.BatchSettings) { s.Targets = []string{"es", "de"} })
		a.batchEnqueue()
		a.batchDropped([]string{filepath.Join(filepath.Dir(d.src), "Episode 2.mov")})
		a.loadBatch()
		a.batch.tab = 1
		save("batch")

		a.navigate("/settings/dubbing")
		save("settings-dubbing")
		_ = ui.Cmd
	}
}
