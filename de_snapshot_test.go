package main

import (
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/hehehai/AudioSmith/internal/dub"
	"github.com/hehehai/AudioSmith/internal/i18n"
	"github.com/hehehai/AudioSmith/internal/settings"
)

// TestSnapshotsDE renders the simplified Stories, Audiobook and Dub
// (thread #42, phases D and E): inspectors and step panels with their
// 「高级」 closed and open, a story on a model that hears directions and
// one that does not; Chinese at 1100×720 and 820×520 @125 %, English at
// 1100×720:
//
//	AUDIOSMITH_SNAPSHOTS=/tmp/shots go test -run TestSnapshotsDE .
func TestSnapshotsDE(t *testing.T) { runSnapshots(t, snapshotsDE) }

func snapshotsDE(t *testing.T, out string) {
	t.Cleanup(func() { i18n.SetLocale("en") })
	useMemKeys(t)
	stubMachine(t, 16, true)
	for _, cfg := range []b2Config{
		{"1100-zh", 1100, 720, 1, false, "zh-Hans"},
		{"min125-zh", 820, 520, 1.25, false, "zh-Hans"},
		{"1100-en", 1100, 720, 1, false, "en"},
	} {
		setup := func(a *app, tt *ui.Tester) {
			i18n.SetLocale("en")
			a.updateSettings(func(s *settings.Settings) {
				s.General.Language = cfg.lang
				s.Appearance.Scale = cfg.scale
			})
			tt.SetSize(cfg.w, cfg.h)
			tt.SetScale(2)
		}
		shoot := func(a *app, tt *ui.Tester, name string, fn func()) {
			fn()
			a.toasts = nil
			tt.Frame()
			tt.Frame()
			tt.Frame()
			f, err := os.Create(filepath.Join(out, name+"-"+cfg.name+".png"))
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			if err := png.Encode(f, tt.Image()); err != nil {
				t.Fatal(err)
			}
		}

		a, _, tt := newTestApp(t)
		acceptLicences(t, a)
		useLfSynth(a)
		setup(a, tt)
		a.lfEncode = func() bool { return true }
		a.navigate("/stories")
		p := a.lfStories
		kittenDoc(&p.doc)
		p.doc.Scene = "傍晚的家里，孩子想领养小猫，妈妈犹豫"
		p.doc.Cast[0].Persona = "八岁女孩，天真活泼"
		p.doc.Lines[1].Direction, p.doc.Lines[1].Intensity = "撒娇央求", 2
		p.panel.set, p.panel.open = true, true
		only := func(pp *lfPage, keys ...string) {
			for k := range pp.open {
				pp.open[k] = false
			}
			for _, k := range keys {
				pp.open[k] = true
			}
		}
		shoot(a, tt, "d01-stories", func() { only(p, "setup") })
		shoot(a, tt, "d02-stories-cast-performance", func() { only(p, "cast", "scene") })
		shoot(a, tt, "d03-stories-advanced", func() { only(p); a.setAdvancedOpen("story", true) })
		a.setAdvancedOpen("story", false)
		shoot(a, tt, "d04-stories-line", func() { only(p); p.sel = 1 })
		p.sel = -1
		shoot(a, tt, "d05-stories-kokoro", func() {
			only(p, "cast", "scene")
			p.doc.Model, p.doc.Voice = kokoro, "speaker:af_heart"
			p.doc.Cast[0].Voice, p.doc.Cast[1].Voice = "speaker:af_bella", "speaker:af_heart"
		})

		a.navigate("/audiobook")
		b := a.lfBook
		a.loadSample(b)
		b.panel.set, b.panel.open = true, true
		shoot(a, tt, "d06-audiobook", func() { only(b, "setup") })
		shoot(a, tt, "d07-audiobook-advanced", func() { only(b); a.setAdvancedOpen("audiobook", true) })

		d := newDubTest(t, 6)
		setup(d.a, d.tt)
		da, dt := d.a, d.tt
		da.dubSide.set, da.dubSide.open = true, true
		da.dubSourceFile(d.src)
		da.dubSet(func(p *dub.Project) { p.Targets = []string{"zh-Hans"} })
		shoot(da, dt, "e01-dub-source", func() { da.dubStep = dubStepSource })
		shoot(da, dt, "e02-dub-source-advanced", func() { da.setAdvancedOpen("dub", true) })
		da.setAdvancedOpen("dub", false)
		da.dubStart()
		shoot(da, dt, "e03-dub-transcribe", func() { da.dubStep = dubStepTranscribe })
		da.dubTranslate(false)
		da.dubLang = "zh-Hans"
		shoot(da, dt, "e04-dub-translate", func() { da.dubStep = dubStepTranslate })
		shoot(da, dt, "e05-dub-translate-advanced", func() { da.setAdvancedOpen("dub", true) })
		da.setAdvancedOpen("dub", false)
		shoot(da, dt, "e06-dub-cast", func() { da.dubStep = dubStepCast })
		shoot(da, dt, "e07-dub-generate", func() {
			da.dubSettings(func(s *dub.Settings) { s.Model = entry(t, "omnivoice-bf16").Repo })
			da.dubStep = dubStepGenerate
			da.setAdvancedOpen("dub", true)
		})
		da.setAdvancedOpen("dub", false)
		shoot(da, dt, "e08-dub-export", func() { da.dubStep = dubStepExport })
	}
}
