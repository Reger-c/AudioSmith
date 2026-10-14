package torchpt

import (
	"os"
	"path/filepath"
	"testing"
)

// AUDIOSMITH_AUDIOSEAL_OUT converts the real checkpoints there (a helper
// for engine work on the Mac).
func TestConvertAudioSealTo(t *testing.T) {
	in, out := os.Getenv("AUDIOSMITH_AUDIOSEAL_DIR"), os.Getenv("AUDIOSMITH_AUDIOSEAL_OUT")
	if in == "" || out == "" {
		t.Skip("set AUDIOSMITH_AUDIOSEAL_DIR and AUDIOSMITH_AUDIOSEAL_OUT")
	}
	for src, dst := range map[string]string{"generator_base.pth": "generator.safetensors", "detector_base.pth": "detector.safetensors"} {
		if err := ConvertFile(filepath.Join(in, src), "model", filepath.Join(out, dst), nil); err != nil {
			t.Fatal(err)
		}
	}
}
