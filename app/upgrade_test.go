package main

import (
	"os"
	"path/filepath"
	"testing"
)

// An MVP data folder (history.json and its clips) opened by v0.2: the
// takes show in the History with their audio, once, also after a restart.
func TestUpgradeFromMVPDataFolder(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "clips"), 0o755)
	writeTestWAV(t, filepath.Join(dir, "clips", "20261013-215200.000.wav"), 24000, 1.5)
	writeTestWAV(t, filepath.Join(dir, "clips", "20261013-215000.000.wav"), 24000, 1)
	os.WriteFile(filepath.Join(dir, "history.json"), []byte(`[
  {"id": "20261013-215200.000", "createdAt": "2026-10-13T21:52:00+08:00", "file": "20261013-215200.000.wav",
   "text": "Newer, with Qwen", "model": "mlx-community/Qwen3-TTS-12Hz-0.6B-CustomVoice-8bit", "voice": "Vivian",
   "language": "English", "sampleRate": 24000, "audioSeconds": 1.5, "genSeconds": 1},
  {"id": "20261013-215000.000", "createdAt": "2026-10-13T21:50:00+08:00", "file": "20261013-215000.000.wav",
   "text": "你好，旧的一条", "model": "mlx-community/Kokoro-82M-bf16", "voice": "zf_xiaobei", "speed": 1.25,
   "sampleRate": 24000, "audioSeconds": 1, "genSeconds": 0.5}
]`), 0o644)
	for run := range 2 {
		func() {
			a, _, tt := newTestAppIn(t, dir)
			a.navigate("/history")
			tt.Frame()
			if len(a.takes) != 2 || a.takes[0].Text != "Newer, with Qwen" {
				t.Fatalf("run %d: takes %+v", run, a.takes)
			}
			for _, tk := range a.takes {
				if _, err := os.Stat(a.takeStore.AudioPath(tk)); err != nil {
					t.Errorf("run %d: audio of %s: %v", run, tk.ID, err)
				}
			}
			if !tt.HasText("Newer, with Qwen") || !tt.HasText("你好，旧的一条") {
				t.Errorf("run %d: history page %q", run, tt.Texts())
			}
		}()
	}
}
