package main

import (
	"fmt"
	"image/png"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/hehehai/AudioSmith/internal/core"
	"github.com/hehehai/AudioSmith/internal/i18n"
	"github.com/hehehai/AudioSmith/internal/services"
	"github.com/hehehai/AudioSmith/internal/settings"
)

// TestSnapshotsReview renders the shell, the Studio, Settings, the
// palette and the icon rail at the default window size (1100×720), at
// the minimum (820×520) and at the minimum with 125 % text, light and
// dark, English and Chinese: the design review's before/after set.
//
//	AUDIOSMITH_SNAPSHOTS=/tmp/shots go test -run TestSnapshotsReview .
func TestSnapshotsReview(t *testing.T) {
	out := os.Getenv("AUDIOSMITH_SNAPSHOTS")
	if out == "" {
		t.Skip("set AUDIOSMITH_SNAPSHOTS to a directory")
	}
	os.MkdirAll(out, 0o755)
	t.Cleanup(func() { i18n.SetLocale("en") })

	script := "Welcome back to the show. Today we talk about 3 things, and it takes 25 minutes.[pause 500ms]\n" +
		"First, the weather: it is 18°C in Shanghai. Not bad for October!"
	sizes := []struct {
		name  string
		w, h  int
		scale float64
	}{{"1100", 1100, 720, 1}, {"min", 820, 520, 1}, {"min125", 820, 520, 1.25}}
	for _, run := range []struct {
		lang string
		dark bool
	}{{"en", false}, {"en", true}, {"zh-Hans", false}, {"zh-Hans", true}} {
		for _, sz := range sizes {
			i18n.SetLocale("en")
			a, eng, tt := newTestApp(t)
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
			suffix := "light"
			if run.dark {
				suffix = "dark"
			}
			if run.lang != "en" {
				suffix += "-" + run.lang
			}
			suffix = sz.name + "-" + suffix
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

			at := time.Date(2026, 10, 13, 21, 50, 0, 0, time.Local)
			tk := core.Take{ID: "T1", CreatedAt: at, Text: script, Model: kokoro, Voice: "af_heart", Speed: 1, SampleRate: 24000,
				AudioSeconds: 2.4, GenSeconds: 0.6, Seed: 81723, Render: &core.TakeRender{Bits: 24, Channels: 1, Bytes: 172844,
					Master: services.MasterDefault, QCDone: true}}
			addTake(t, a, tk, 2.4)
			a.lastClipID = "T1"
			a.text = script
			a.toasts = nil
			save("studio")

			a.generate()
			req := eng.last(t)
			eng.progress(req.ID, "generating", 0.38, 9.6)
			a.bus.Publish(services.RenderProgress{Job: req.ID, Owner: req.Spec.Owner, Chunk: 3, Chunks: 7, Cached: 2})
			a.toasts = nil
			save("studio-rendering")
			eng.cancelled(req.ID)
			a.toasts = nil

			a.openPalette()
			save("palette")
			a.paletteOpen = false

			a.setRail(true)
			save("rail")
			a.setRail(false)

			a.switchOpen = true
			save("quickswitch")
			a.switchOpen = false

			a.notes.Post(core.Notification{Severity: core.SeveritySuccess, Title: i18n.T("%s is downloaded", "Kokoro 82M"), Link: "/models"})
			a.toasts = nil
			a.noticesOpen = true
			a.logsPanel = true
			save("notifications")
			a.noticesOpen = false
			a.logsPanel = false

			for _, s := range []string{"general", "appearance", "models", "about"} {
				a.navigate("/settings/" + s)
				save("settings-" + s)
			}
			a.navigate("/studio")
		}
	}
	_ = ui.Cmd
}

// TestSnapshotsAccents renders the Studio while it renders (progress, the
// playing take, a switch on, the focus) with each candidate accent, light
// and dark, for the design review to pick one.
func TestSnapshotsAccents(t *testing.T) {
	out := os.Getenv("AUDIOSMITH_SNAPSHOTS")
	if out == "" {
		t.Skip("set AUDIOSMITH_SNAPSHOTS to a directory")
	}
	os.MkdirAll(out, 0o755)
	t.Cleanup(func() { setAccent("teal") })
	for _, name := range []string{"blue", "indigo", "teal"} {
		for _, dark := range []bool{false, true} {
			setAccent(name)
			a, eng, tt := newTestApp(t)
			tt.SetDark(dark)
			tt.SetScale(2)
			at := time.Date(2026, 10, 13, 21, 50, 0, 0, time.Local)
			tk := core.Take{ID: "T1", CreatedAt: at, Text: "Hello", Model: kokoro, Voice: "af_heart", Speed: 1, SampleRate: 24000,
				AudioSeconds: 2.4, GenSeconds: 0.6, Render: &core.TakeRender{Bits: 24, Channels: 1, Bytes: 172844, Master: services.MasterDefault}}
			addTake(t, a, tk, 2.4)
			a.lastClipID = "T1"
			a.text = "Welcome back to the show.[pause 500ms] Today we talk about 3 things."
			a.autoplay = true
			a.generate()
			req := eng.last(t)
			eng.progress(req.ID, "generating", 0.62, 9.6)
			a.toasts = nil
			suffix := "light"
			if dark {
				suffix = "dark"
			}
			tt.Frame()
			tt.Frame()
			f, err := os.Create(filepath.Join(out, "accent-"+name+"-"+suffix+".png"))
			if err != nil {
				t.Fatal(err)
			}
			png.Encode(f, tt.Image())
			f.Close()
			eng.cancelled(req.ID)
		}
	}
}
