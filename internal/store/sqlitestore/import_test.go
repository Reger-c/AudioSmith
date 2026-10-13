package sqlitestore

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/hehehai/AudioSmith/internal/core"
)

// mvpHistory is a history.json as the MVP wrote it (see historyjson's tests).
const mvpHistory = `[
  {
    "id": "20261013-215200.000",
    "createdAt": "2026-10-13T21:52:00+08:00",
    "file": "20261013-215200.000.wav",
    "text": "Newer, with Qwen",
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
    "text": "你好，旧的一条",
    "model": "mlx-community/Kokoro-82M-bf16",
    "voice": "zf_xiaobei",
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

func writeMVP(t *testing.T, dir string) {
	t.Helper()
	os.MkdirAll(filepath.Join(dir, "clips"), 0o755)
	for _, f := range []string{"20261013-215200.000.wav", "20261013-215000.000.wav"} {
		os.WriteFile(filepath.Join(dir, "clips", f), []byte("RIFF"), 0o644)
	}
	os.WriteFile(filepath.Join(dir, "history.json"), []byte(mvpHistory), 0o644)
}

func TestImportHistoryJSON(t *testing.T) {
	dir := t.TempDir()
	writeMVP(t, dir)
	s := openTest(t, dir)

	n, err := s.ImportHistoryJSON(ctx)
	if err != nil || n != 2 {
		t.Fatalf("imported %d, %v", n, err)
	}
	takes, _ := s.Takes().List(ctx, core.TakeQuery{})
	if len(takes) != 2 || takes[0].ID != "20261013-215200.000" || takes[1].Speed != 1.25 {
		t.Fatalf("takes %+v", takes)
	}
	q := takes[0]
	if q.Voice != "Vivian" || q.Temperature != 0.4 || q.TopP != 0.8 || q.MaxTokens != 900 || q.RTF() != 2 ||
		q.CreatedAt.Format("2006-01-02T15:04:05-07:00") != "2026-10-13T21:52:00+08:00" {
		t.Errorf("recipe %+v", q)
	}
	if _, err := os.Stat(s.Takes().AudioPath(q)); err != nil {
		t.Errorf("audio moved: %v", err)
	}
	if l, _ := s.Takes().List(ctx, core.TakeQuery{Search: "旧的"}); len(l) != 1 {
		t.Error("imported takes are not searchable")
	}

	// The marker is set and history.json is kept.
	doc, ok, _ := s.Documents().Get(ctx, HistoryImportKey)
	var marker HistoryImport
	if !ok || json.Unmarshal(doc, &marker) != nil || marker.Takes != 2 || marker.ImportedAt.IsZero() {
		t.Errorf("marker %s", doc)
	}
	if _, err := os.Stat(filepath.Join(dir, "history.json")); err != nil {
		t.Error("history.json removed")
	}

	// Only once: a take deleted after the import does not come back.
	s.Takes().Delete(ctx, "20261013-215000.000")
	if n, err := s.ImportHistoryJSON(ctx); n != 0 || err != nil {
		t.Errorf("second import %d %v", n, err)
	}
	s.Close()
	s = openTest(t, dir)
	if n, _ := s.ImportHistoryJSON(ctx); n != 0 {
		t.Errorf("import after reopening: %d", n)
	}
	if takes, _ := s.Takes().List(ctx, core.TakeQuery{}); len(takes) != 1 {
		t.Errorf("after reopen %d takes", len(takes))
	}
}

func TestImportWithoutHistory(t *testing.T) {
	s := openTest(t, t.TempDir())
	if n, err := s.ImportHistoryJSON(ctx); n != 0 || err != nil {
		t.Fatal(n, err)
	}
	if _, ok, _ := s.Documents().Get(ctx, HistoryImportKey); !ok {
		t.Error("no marker: a history.json written later by an old build would be imported")
	}
}

func TestImportBrokenHistory(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "history.json"), []byte("[{"), 0o644)
	s := openTest(t, dir)
	if _, err := s.ImportHistoryJSON(ctx); err == nil {
		t.Fatal("broken history imported")
	}
	if _, ok, _ := s.Documents().Get(ctx, HistoryImportKey); ok {
		t.Error("marker set after a failed import")
	}
	// Fixed by hand, it imports on the next start.
	os.WriteFile(filepath.Join(dir, "history.json"), []byte("[]"), 0o644)
	if n, err := s.ImportHistoryJSON(ctx); n != 0 || err != nil {
		t.Fatal(n, err)
	}
}
