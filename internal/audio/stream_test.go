package audio

import (
	"encoding/binary"
	"io"
	"testing"
	"time"
)

func TestStreamReadsWhatArrives(t *testing.T) {
	s := NewStream(1000, 1, 0)
	r := s.NewReader()
	got := make(chan []float32)
	go func() {
		var all []float32
		buf := make([]float32, 64)
		for {
			n, err := r.Read(buf)
			all = append(all, buf[:n]...)
			if err == io.EOF {
				break
			}
		}
		got <- all
	}()
	s.Append([]float32{0.5, -0.5, 2}) // clipped
	pcm := make([]byte, 4)
	binary.LittleEndian.PutUint16(pcm, uint16(int16(16384)))
	binary.LittleEndian.PutUint16(pcm[2:], uint16(0x8000))
	time.Sleep(10 * time.Millisecond) // the reader waits meanwhile
	s.AppendPCM16(pcm)
	if r.Len() != -1 {
		t.Error("length known before Finish")
	}
	s.Finish()
	all := <-got
	want := []float32{16383.5 / 32768, -16383.5 / 32768, 32767.0 / 32768, 0.5, -1}
	if len(all) != len(want) {
		t.Fatalf("read %v", all)
	}
	for i := range want {
		if d := all[i] - want[i]; d > 1e-4 || d < -1e-4 {
			t.Errorf("sample %d: %v want %v", i, all[i], want[i])
		}
	}
	if r.Len() != 5 || !s.Done() || s.Seconds() != 0.005 {
		t.Errorf("len %d done %v seconds %v", r.Len(), s.Done(), s.Seconds())
	}
	// Seeking back replays; past the end stops at the end.
	r.SetPosition(3)
	buf := make([]float32, 8)
	if n, _ := r.Read(buf); n != 2 {
		t.Errorf("after seek read %d", n)
	}
	r.SetPosition(99)
	if _, err := r.Read(buf); err != io.EOF {
		t.Errorf("past the end: %v", err)
	}
	// A second reader starts at the start; closing one leaves the other.
	r2 := s.NewReader()
	r.Close()
	if n, _ := r2.Read(buf); n != 5 {
		t.Errorf("second reader read %d", n)
	}
	// Appending after Finish is ignored.
	s.Append([]float32{1})
	if r2.Len() != 5 {
		t.Error("appended after Finish")
	}
}

func TestStreamCloseWakesReader(t *testing.T) {
	s := NewStream(1000, 2, 0)
	r := s.NewReader()
	done := make(chan error)
	go func() {
		_, err := r.Read(make([]float32, 8))
		done <- err
	}()
	time.Sleep(5 * time.Millisecond)
	r.Close()
	select {
	case err := <-done:
		if err != io.EOF {
			t.Errorf("err %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Close did not wake the reader")
	}
}

func TestStreamCap(t *testing.T) {
	s := NewStream(10, 2, 1) // 10 frames of stereo
	s.Append(make([]float32, 30))
	s.AppendPCM16(make([]byte, 40))
	if s.Seconds() != 1 {
		t.Errorf("kept %v s", s.Seconds())
	}
}

func TestPlayerPlaysAStream(t *testing.T) {
	var pl Player
	s := NewStream(8000, 1, 0)
	pl.PlayStream("take", s)
	if !pl.Streaming("take") || pl.Current() != "take" {
		t.Fatal("not streaming")
	}
	s.Append(make([]float32, 800))
	if p := pl.Progress("take"); p < 0 || p > 1 {
		t.Errorf("progress %v", p)
	}
	pl.Seek("take", 0.5)
	pl.Stop()
	if pl.Streaming("take") || pl.Current() != "" {
		t.Error("still streaming after Stop")
	}
}
