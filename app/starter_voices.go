package main

// Starter voices: designed voices that ship with the app
// (assets/starter-voices/*.asvoice, see its README), imported into the library
// once, so a new user has a saved voice for the Base models the presets
// recommend for audiobooks and dubbing (thread #41, option A).

import (
	"context"
	"io/fs"
	"os"
	"path"
	"slices"

	"github.com/hehehai/AudioSmith/assets"

	"github.com/hehehai/AudioSmith/internal/core"
	"github.com/hehehai/AudioSmith/internal/settings"
)

var starterFS = assets.StarterVoices

// starterFiles are the bundled starter voices by file name (tests
// replace it).
var starterFiles = func() map[string][]byte {
	out := map[string][]byte{}
	names, _ := fs.Glob(starterFS, "starter-voices/*.asvoice")
	for _, n := range names {
		if b, err := starterFS.ReadFile(n); err == nil {
			out[path.Base(n)] = b
		}
	}
	return out
}

// importStarterVoices imports the starter voices not imported before, in
// the background; the new voices then join the library as any import.
func (a *app) importStarterVoices() {
	files := starterFiles()
	var todo []string
	for name := range files {
		if _, done := a.prefs.Setup.Starters[name]; !done {
			todo = append(todo, name)
		}
	}
	if len(todo) == 0 {
		return
	}
	slices.Sort(todo)
	svc := a.lib.svc.Voices
	dir := a.lib.store.Dir()
	a.background(func() {
		got := map[string]string{}
		var voices []core.Voice
		for _, name := range todo {
			tmp, err := os.CreateTemp(dir, "starter-*.asvoice")
			if err != nil {
				continue
			}
			_, err = tmp.Write(files[name])
			tmp.Close()
			if err == nil {
				var res core.VoiceImport
				if res, err = svc.Import(context.Background(), tmp.Name()); err == nil {
					got[name] = res.Voice.ID
					voices = append(voices, res.Voice)
				}
			}
			os.Remove(tmp.Name())
		}
		a.post(func() {
			if len(got) == 0 {
				return
			}
			a.updateSettings(func(s *settings.Settings) {
				if s.Setup.Starters == nil {
					s.Setup.Starters = map[string]string{}
				}
				for k, v := range got {
					s.Setup.Starters[k] = v
				}
			})
			for _, v := range voices {
				a.voiceChanged(core.VoiceChanged{Voice: v})
			}
		})
	})
}

// starterVoice is the first starter voice still in the library, or nil.
func (a *app) starterVoice() *core.Voice {
	names := make([]string, 0, len(a.prefs.Setup.Starters))
	for n := range a.prefs.Setup.Starters {
		names = append(names, n)
	}
	slices.Sort(names)
	for _, n := range names {
		if v := a.findVoice(a.prefs.Setup.Starters[n]); v != nil {
			return v
		}
	}
	return nil
}
