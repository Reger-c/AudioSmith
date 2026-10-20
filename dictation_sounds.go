package main

// The sounds of dictation (Settings → Dictation → Sounds): listening
// starts, stops, an error. Each is one of the ActNow family's sounds
// (actnowkit's sounds package; the audio is in official builds only, in
// resources/sounds, fetched by scripts/sync-sounds.sh), a macOS system
// sound or none, at the sounds' volume. A build without the family's
// sounds plays AudioSmith's earlier Tink, Pop and Basso.

import (
	"os"

	"github.com/egoist/mygo"
	"github.com/hehehai/AudioSmith/internal/i18n"
	"github.com/hehehai/AudioSmith/internal/settings"
	"github.com/hehehai/actnowkit/sounds"
)

// Dictation's sounds: the family's Tally, Wrap and Shrug.
var (
	dictStartEvent = sounds.Event{ID: "dictation.start", Name: sounds.Text{En: "Listening starts", Zh: "开始听写"},
		Default: "tally", Fallback: "Tink", Uses: []string{"record.start"}}
	dictStopEvent = sounds.Event{ID: "dictation.stop", Name: sounds.Text{En: "Listening stops", Zh: "停止听写"},
		Default: "wrap", Fallback: "Pop", Uses: []string{"record.stop"}}
	dictErrorEvent = sounds.Event{ID: "dictation.error", Name: sounds.Text{En: "An error", Zh: "出错"},
		Default: "shrug", Fallback: "Basso", Uses: []string{"error", "warning"}}
)

// dictSoundsState is dictation's sounds: the family's this build has and
// their player (tests set both), the Mac's system sounds.
type dictSoundsState struct {
	dictSoundCat  *sounds.Catalog
	dictPlayer    *sounds.Player
	dictSysSounds []string
	dictSysRead   bool
}

// openDictSounds loads the family's sounds the app ships and makes the
// player, unless a test did.
func (a *app) openDictSounds() {
	if a.dictPlayer != nil {
		return
	}
	if res, err := mygo.App.Path(mygo.PathResources); err == nil {
		a.dictSoundCat, _ = sounds.Load(os.DirFS(res), "sounds")
	}
	a.dictPlayer = sounds.NewPlayer(a.dictSoundCat, nil)
	if a.win != nil {
		p, prefs := a.dictPlayer, a.dictSoundPrefs()
		go p.Preload(prefs.Choice(dictStartEvent), prefs.Choice(dictStopEvent))
	}
}

// dictSoundPrefs are Settings → Dictation's sounds as actnowkit's prefs.
func (a *app) dictSoundPrefs() sounds.Prefs {
	d := a.prefs.Dictation
	v := d.SoundVolume
	if v <= 0 {
		v = sounds.DefaultVolume
	}
	p := sounds.Prefs{On: d.Sounds, Volume: min(v, 100)}
	p.Choose(dictStartEvent, d.StartSound)
	p.Choose(dictStopEvent, d.StopSound)
	p.Choose(dictErrorEvent, d.ErrorSound)
	return p
}

// dictCue plays ev's sound, when sounds are on: a family sound on the
// player, a system sound as before (dictSys).
func (a *app) dictCue(ev sounds.Event) {
	t := a.dictSoundPrefs().Target(a.dictSoundCat, ev)
	a.playDictTarget(t)
}

func (a *app) playDictTarget(t sounds.Target) {
	switch t.Kind {
	case sounds.KindCatalog:
		a.dictPlayer.Play(t)
	case sounds.KindSystem:
		a.dictSys.PlaySound(t.Name)
	}
}

// dictPreview plays choice for ev (the picker's ▶, Try), sounds on or off.
func (a *app) dictPreview(ev sounds.Event, choice string) {
	t := sounds.Resolve(a.dictSoundCat, choice, ev.Fallback, a.dictSoundPrefs().Volume)
	if t.Kind == sounds.KindCatalog {
		a.dictPlayer.Preview(choice, ev.Fallback, a.dictSoundPrefs().Volume)
		return
	}
	a.dictPlayer.StopPreview()
	a.playDictTarget(t)
}

// chooseDictSound keeps choice as ev's sound.
func (a *app) chooseDictSound(ev sounds.Event, choice string) {
	p := a.dictSoundPrefs()
	p.Choose(ev, choice)
	a.updateSettings(func(s *settings.Settings) {
		s.Dictation.StartSound = p.Events[dictStartEvent.ID]
		s.Dictation.StopSound = p.Events[dictStopEvent.ID]
		s.Dictation.ErrorSound = p.Events[dictErrorEvent.ID]
	})
}

// dictSoundLabel names a choice: a family sound's name in the interface's
// language, a system sound's, None; a family sound this build lacks, the
// system sound it falls back to.
func (a *app) dictSoundLabel(ev sounds.Event) func(string) string {
	return func(choice string) string {
		switch {
		case choice == sounds.None:
			return i18n.T("None")
		case sounds.IsSystem(choice):
			return sounds.SystemName(choice)
		}
		if s, ok := a.dictSoundCat.Sound(choice); ok {
			return s.Name.In(i18n.Locale())
		}
		return ev.Fallback
	}
}

// systemSounds are the Mac's system sounds, for the picker.
func (a *app) systemSounds() []string {
	if !a.dictSysRead {
		a.dictSysSounds, a.dictSysRead = sounds.SystemSounds(""), true
		if len(a.dictSysSounds) == 0 {
			a.dictSysSounds = sounds.KnownSystemSounds // off macOS
		}
	}
	return a.dictSysSounds
}
