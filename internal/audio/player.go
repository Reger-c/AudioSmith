package audio

import (
	"os"
	"time"

	myaudio "github.com/egoist/mygo/audio"
)

// Player plays one take at a time, identified by an id. It is used from
// the UI thread only (not goroutine-safe). Load decodes the whole file
// into memory: thread A switches it to a stream (myaudio.OpenStream) so
// long audio plays without loading it all.
type Player struct {
	id string
	p  *myaudio.Player
}

func (pl *Player) load(id, path string) error {
	pl.Stop()
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	snd, err := myaudio.DecodeSound(data)
	if err != nil {
		return err
	}
	pl.id = id
	pl.p = snd.NewPlayer()
	return nil
}

// Toggle plays id's audio at path, loading it first, or pauses it while it plays.
func (pl *Player) Toggle(id, path string) error {
	if pl.id != id || pl.p == nil {
		if err := pl.load(id, path); err != nil {
			return err
		}
	}
	if pl.p.Playing() {
		pl.p.Pause()
	} else {
		pl.p.Play()
	}
	return nil
}

// Stop stops and forgets the loaded audio.
func (pl *Player) Stop() {
	if pl.p != nil {
		pl.p.Stop()
	}
	pl.p = nil
	pl.id = ""
}

// Current is the id of the loaded audio, playing or paused; "" when none.
func (pl *Player) Current() string { return pl.id }

// Active reports whether anything is playing.
func (pl *Player) Active() bool { return pl.p != nil && pl.p.Playing() }

// Playing reports whether id is playing.
func (pl *Player) Playing(id string) bool {
	return pl.p != nil && pl.id == id && pl.p.Playing()
}

// Progress is where id is, from 0 to 1 (0 when it is not loaded).
func (pl *Player) Progress(id string) float32 {
	if pl.p == nil || pl.id != id {
		return 0
	}
	d := pl.p.Duration()
	if d <= 0 {
		return 0
	}
	return float32(pl.p.Position()) / float32(d)
}

// Seek moves id to fraction (0–1) of its length, if it is loaded.
func (pl *Player) Seek(id string, fraction float32) {
	if pl.p == nil || pl.id != id {
		return
	}
	pl.p.Seek(time.Duration(float64(pl.p.Duration()) * float64(fraction)))
}
