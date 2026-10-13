package audio

import "testing"

func TestPeaksOf(t *testing.T) {
	p := PeaksOf([]float32{0, 0.5, 0.25, 1, 0, 0}, 3)
	if len(p) != 3 || p[0] != 0.5 || p[1] != 1 || p[2] != 0 {
		t.Errorf("peaks %v", p)
	}
	if PeaksOf(nil, 3) != nil || PeaksOf([]float32{1}, 0) != nil {
		t.Error("empty input or n=0 must give nil")
	}
}

func TestPlayerIdle(t *testing.T) {
	var pl Player
	if pl.Active() || pl.Playing("x") || pl.Progress("x") != 0 || pl.Current() != "" {
		t.Error("a new player plays nothing")
	}
	pl.Seek("x", 0.5)
	pl.Stop()
	if err := pl.Toggle("x", "/does/not/exist.wav"); err == nil {
		t.Error("missing file played")
	}
}
