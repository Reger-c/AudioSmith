package historyjson

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hehehai/AudioSmith/internal/core"
)

// mvpIndex is a history.json as the MVP wrote it.
const mvpIndex = `[
  {
    "id": "20261013-215200.000",
    "createdAt": "2026-10-13T21:52:00+08:00",
    "file": "20261013-215200.000.wav",
    "text": "Newer",
    "model": "mlx-community/Qwen3-TTS-12Hz-0.6B-CustomVoice-8bit",
    "voice": "Vivian",
    "language": "English",
    "temperature": 0.4,
    "topP": 0.8,
    "maxTokens": 900,
    "sampleRate": 24000,
    "audioSeconds": 2.5,
    "genSeconds": 1.25
  },
  {
    "id": "20261013-215000.000",
    "createdAt": "2026-10-13T21:50:00+08:00",
    "file": "20261013-215000.000.wav",
    "text": "Older",
    "model": "mlx-community/Kokoro-82M-bf16",
    "voice": "af_heart",
    "speed": 1.25,
    "sampleRate": 24000,
    "audioSeconds": 1,
    "genSeconds": 0.5
  },
  {
    "id": "gone",
    "createdAt": "2026-10-13T21:55:00+08:00",
    "file": "gone.wav",
    "text": "Audio deleted by hand",
    "model": "m",
    "sampleRate": 0,
    "audioSeconds": 0,
    "genSeconds": 0
  }
]`

func TestMVPHistory(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "clips"), 0o755)
	for _, f := range []string{"20261013-215200.000.wav", "20261013-215000.000.wav"} {
		os.WriteFile(filepath.Join(dir, "clips", f), []byte("RIFF"), 0o644)
	}
	os.WriteFile(filepath.Join(dir, "history.json"), []byte(mvpIndex), 0o644)

	l, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	all, _ := l.List(ctx, core.TakeQuery{})
	if len(all) != 2 || all[0].Text != "Newer" || all[1].Speed != 1.25 || all[0].RTF() != 2 {
		t.Fatalf("takes %+v", all)
	}
	if got, _ := l.List(ctx, core.TakeQuery{Search: "OLD"}); len(got) != 1 || got[0].Text != "Older" {
		t.Errorf("search %+v", got)
	}
	if got, _ := l.List(ctx, core.TakeQuery{Limit: 1, Offset: 1}); len(got) != 1 || got[0].Text != "Older" {
		t.Errorf("paging %+v", got)
	}
	if _, err := l.Get(ctx, "nope"); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("Get missing: %v", err)
	}

	// Put adds first and replaces in place; the file keeps the MVP format.
	at := time.Date(2026, 10, 14, 9, 0, 0, 0, time.UTC)
	n := core.Take{ID: NewTakeID(at), CreatedAt: at, File: NewTakeID(at) + ".wav", Text: "New", Model: "m"}
	if err := l.Put(ctx, n); err != nil {
		t.Fatal(err)
	}
	n.Starred = true
	l.Put(ctx, n)
	all, _ = l.List(ctx, core.TakeQuery{})
	if len(all) != 3 || all[0].ID != n.ID || !all[0].Starred {
		t.Fatalf("after put %+v", all)
	}
	data, _ := os.ReadFile(filepath.Join(dir, "history.json"))
	if strings.Contains(string(data), `"voiceId"`) || !strings.Contains(string(data), `"starred": true`) {
		t.Errorf("index %s", data)
	}

	// Delete removes the audio too.
	if err := l.Delete(ctx, "20261013-215000.000"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "clips", "20261013-215000.000.wav")); !os.IsNotExist(err) {
		t.Error("audio of a deleted take remains")
	}
	again, err := Read(dir)
	if err != nil || len(again) != 1 || again[0].Text != "Newer" {
		// "New" has no audio file, so Read drops it, as Open did in the MVP.
		t.Fatalf("reread %+v %v", again, err)
	}
}

func TestEmpty(t *testing.T) {
	l, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if all, _ := l.List(context.Background(), core.TakeQuery{}); len(all) != 0 || all == nil {
		t.Errorf("%v", all)
	}
	if NewTakeID(time.Date(2026, 1, 2, 3, 4, 5, 6e6, time.UTC)) != "20260102-030405.006" {
		t.Error(NewTakeID(time.Date(2026, 1, 2, 3, 4, 5, 6e6, time.UTC)))
	}
}
