package main

import (
	"os"
	"time"

	"github.com/egoist/mygo/audio"
)

// player plays one clip at a time with MyGo's audio package.
type player struct {
	clipID string
	p      *audio.Player
}

func (pl *player) load(id, path string) error {
	pl.stop()
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	snd, err := audio.DecodeSound(data)
	if err != nil {
		return err
	}
	pl.clipID = id
	pl.p = snd.NewPlayer()
	return nil
}

// toggle plays the clip, loading it first, or pauses it while it plays.
func (pl *player) toggle(id, path string) error {
	if pl.clipID != id || pl.p == nil {
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

func (pl *player) stop() {
	if pl.p != nil {
		pl.p.Stop()
	}
	pl.p = nil
	pl.clipID = ""
}

func (pl *player) playing(id string) bool {
	return pl.p != nil && pl.clipID == id && pl.p.Playing()
}

// progress is where the clip id is, from 0 to 1.
func (pl *player) progress(id string) float32 {
	if pl.p == nil || pl.clipID != id {
		return 0
	}
	d := pl.p.Duration()
	if d <= 0 {
		return 0
	}
	return float32(pl.p.Position()) / float32(d)
}

func (pl *player) seek(id string, fraction float32) {
	if pl.p == nil || pl.clipID != id {
		return
	}
	pl.p.Seek(time.Duration(float64(pl.p.Duration()) * float64(fraction)))
}
