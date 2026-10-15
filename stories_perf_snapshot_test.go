package main

import (
	"fmt"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"github.com/hehehai/AudioSmith/internal/i18n"
	"github.com/hehehai/AudioSmith/internal/services"
	"github.com/hehehai/AudioSmith/internal/settings"
)

// TestSnapshotsStoryPerformance renders the Stories page's dialogue
// continuity (thread #39): the Performance section (continuity level,
// pacing, scene, AI director), personas on the cast, direction chips on
// the lines and a line's direction editor; on CustomVoice (directed
// delivery) and Kokoro (line by line); at 1100×720 and at the minimum
// with 125 % text, light and dark, English and Chinese.
//
//	AUDIOSMITH_SNAPSHOTS=/tmp/shots go test -run TestSnapshotsStoryPerformance .
func TestSnapshotsStoryPerformance(t *testing.T) {
	out := os.Getenv("AUDIOSMITH_SNAPSHOTS")
	if out == "" {
		t.Skip("set AUDIOSMITH_SNAPSHOTS to a directory")
	}
	os.MkdirAll(out, 0o755)
	t.Cleanup(func() { i18n.SetLocale("en") })
	for _, run := range []struct {
		lang string
		dark bool
	}{{"zh-Hans", false}, {"en", false}, {"zh-Hans", true}} {
		for _, sz := range []struct {
			name  string
			w, h  int
			scale float64
		}{{"1100", 1100, 720, 1}, {"min125", 820, 520, 1.25}} {
			i18n.SetLocale("en")
			a, _, tt := newTestApp(t)
			lang, scale := run.lang, sz.scale
			a.updateSettings(func(s *settings.Settings) {
				if lang != "en" {
					s.General.Language = lang
				}
				s.Appearance.Scale = scale
			})
			tt.SetSize(sz.w, sz.h)
			tt.SetDark(run.dark)
			tt.SetScale(2)
			suffix := sz.name + "-light"
			if run.dark {
				suffix = sz.name + "-dark"
			}
			if run.lang != "en" {
				suffix += "-" + run.lang
			}
			save := func(name string) {
				tt.Frame()
				tt.Frame()
				f, err := os.Create(filepath.Join(out, fmt.Sprintf("%s-%s.png", name, suffix)))
				if err != nil {
					t.Fatal(err)
				}
				defer f.Close()
				if err := png.Encode(f, tt.Image()); err != nil {
					t.Fatal(err)
				}
			}
			a.navigate("/stories")
			p := a.lfStories
			kittenDoc(&p.doc)
			d := &p.doc
			d.Lines = append(d.Lines,
				services.StoryLine{ID: "l5", Character: "mom", Text: "谁带它去打针、看病呢？"},
				services.StoryLine{ID: "l6", Character: "kid", Text: "我和你一起去。我用零花钱买猫粮。"})
			d.Scene = "傍晚的家里，孩子想领养小猫，妈妈犹豫"
			d.Cast[0].Persona, d.Cast[1].Persona = "八岁女孩，天真活泼，语速稍快", "温和耐心的妈妈，略疲惫"
			for i, dir := range []struct {
				d string
				n int
			}{{"天真又期待", 2}, {"撒娇央求", 2}, {"犹豫但温和", 2}, {"急切地保证", 3}, {"担心地追问", 2}, {"主动承诺，语气坚定", 2}} {
				d.Lines[i].Direction, d.Lines[i].Intensity = dir.d, dir.n
			}
			p.open["setup"], p.open["cast"], p.open["scene"] = false, false, true
			p.panel.set, p.panel.open = true, true // the inspector, a drawer at the minimum size
			a.toasts = nil
			save("story-performance")
			p.sel = 1
			p.panel.open = false
			save("story-line-direction")
			p.panel.open = true
			p.sel = -1
			p.open["scene"], p.open["cast"] = false, true
			save("story-cast-persona")
			d.Model, d.Voice = kokoro, "speaker:af_heart"
			d.Cast[0].Voice, d.Cast[1].Voice = "speaker:af_bella", "speaker:af_heart"
			p.open["scene"], p.open["cast"] = true, false
			save("story-performance-kokoro")
		}
	}
}
