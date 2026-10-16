package main

import (
	"image/png"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/hehehai/AudioSmith/internal/dub"
	"github.com/hehehai/AudioSmith/internal/i18n"
	"github.com/hehehai/AudioSmith/internal/settings"
)

// A new dub is made in Chinese unless Settings → Dubbing chose a language.
func TestNewDubProjectDefaultTarget(t *testing.T) {
	src := dub.Source{Kind: dub.SourceFile, Path: "/x/Talk.mp4", Name: "Talk.mp4"}
	p := newDubProject(src, settings.Dubbing{}, "m")
	if !p.TargetsAuto || !slices.Equal(p.Targets, []string{"zh-Hans"}) {
		t.Errorf("default %v auto %v", p.Targets, p.TargetsAuto)
	}
	p = newDubProject(src, settings.Dubbing{Targets: []string{"zh-Hans"}}, "m")
	if p.TargetsAuto || !slices.Equal(p.Targets, []string{"zh-Hans"}) {
		t.Errorf("chosen Chinese %v auto %v", p.Targets, p.TargetsAuto)
	}
	p = newDubProject(src, settings.Dubbing{Targets: []string{"es"}}, "m")
	if p.TargetsAuto || !slices.Equal(p.Targets, []string{"es"}) {
		t.Errorf("chosen Spanish %v auto %v", p.Targets, p.TargetsAuto)
	}
}

// Settings → Dubbing shows the default as its first choice.
func TestSettingsDubTargetDefault(t *testing.T) {
	a, _, tt := newTestApp(t)
	a.navigate("/settings/dubbing")
	tt.Frame()
	tt.Frame()
	if !tt.HasText("🇨🇳 Chinese (default)") || !tt.HasText(dubAutoTargetHint()) && !hasPrefixText(tt.Texts(), "The first target language of a new dub.") {
		t.Errorf("default not shown: %q", tt.Texts())
	}
	a.updateSettings(func(s *settings.Settings) { s.Dubbing.Targets = []string{"fr"} })
	tt.Frame()
	if !tt.HasText("🇫🇷 French") {
		t.Errorf("chosen language not shown: %q", tt.Texts())
	}
}

func hasPrefixText(texts []string, prefix string) bool {
	return slices.ContainsFunc(texts, func(s string) bool { return len(s) >= len(prefix) && s[:len(prefix)] == prefix })
}

// A Chinese video with the default target is dubbed into English, and
// the project says so; choosing a target there keeps it.
func TestDubDefaultTargetPage(t *testing.T) {
	d := newDubTest(t, 3)
	a, tt := d.a, d.tt
	a.updateSettings(func(s *settings.Settings) { s.Dubbing.Targets = nil })
	a.dubSourceFile(d.src)
	if !a.dub.TargetsAuto || a.dub.Primary() != "zh-Hans" {
		t.Fatalf("new dub %v auto %v", a.dub.Targets, a.dub.TargetsAuto)
	}
	a.dubSet(func(p *dub.Project) { p.SourceLang = "zh" })
	a.dubStart()
	tt.Frame()
	if !slices.Equal(a.dub.Targets, []string{"en"}) || a.dub.TargetSwitched != "zh-Hans" || a.dubLang != "en" {
		t.Fatalf("after the transcript %v switched %q shown %q", a.dub.Targets, a.dub.TargetSwitched, a.dubLang)
	}
	note := "The video is in Chinese, so it is dubbed into English."
	if dubRetargetNote(a.dub) != note {
		t.Errorf("note %q", dubRetargetNote(a.dub))
	}
	a.dubStep = dubStepTranslate
	tt.Frame()
	tt.Frame()
	if !tt.HasText(note) {
		t.Errorf("note not shown: %q", tt.Texts())
	}
	i18n.SetLocale("zh-Hans")
	got := dubRetargetNote(a.dub)
	i18n.SetLocale("en")
	if got != "原视频是中文，已改为配成英语。" {
		t.Errorf("zh note %q", got)
	}
	// Choosing the target on the project: kept, no note.
	a.dubSet(func(p *dub.Project) { p.ChooseTargets([]string{"zh-Hans"}) })
	tt.Frame()
	if dubRetargetNote(a.dub) != "" || tt.HasText(note) {
		t.Error("note after choosing")
	}
}

// TestSnapshotsDubTarget renders Settings → Dubbing with the default
// target and a dub switched to English, at 1100×720 in English and
// Chinese:
//
//	AUDIOSMITH_SNAPSHOTS=/tmp/shots go test -run TestSnapshotsDubTarget .
func TestSnapshotsDubTarget(t *testing.T) { runSnapshots(t, snapshotsDubTarget) }

func snapshotsDubTarget(t *testing.T, out string) {
	t.Cleanup(func() { i18n.SetLocale("en") })
	for _, l := range []string{"en", "zh-Hans"} {
		i18n.SetLocale("en")
		d := newDubTest(t, 3)
		a, tt := d.a, d.tt
		a.updateSettings(func(s *settings.Settings) {
			s.Dubbing.Targets = nil
			if l != "en" {
				s.General.Language = l
			}
		})
		tt.SetSize(1100, 720)
		save := func(name string) {
			tt.Frame()
			tt.Frame()
			f, err := os.Create(filepath.Join(out, name+"-1100-"+l+".png"))
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			if err := png.Encode(f, tt.Image()); err != nil {
				t.Fatal(err)
			}
		}
		a.navigate("/settings/dubbing")
		save("settings-dubbing-target")
		a.navigate("/dub")
		a.dubSourceFile(d.src)
		a.dubSet(func(p *dub.Project) { p.SourceLang = "zh" })
		a.dubStart()
		a.dubStep = dubStepTranslate
		save("dub-switched-target")
		a.navigate("/batch")
		save("batch-default-target")
	}
}
