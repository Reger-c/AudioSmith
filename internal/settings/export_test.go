package settings

import (
	"context"
	"slices"
	"testing"
)

// The Export dialog's choices: kept per kind, out-of-range values dropped,
// and Get hands out copies.
func TestExportSettings(t *testing.T) {
	if Defaults().Export.Audio != nil {
		t.Error("a fresh install remembers an export")
	}
	s := NewMemory()
	ctx := context.Background()
	err := s.Update(ctx, func(st *Settings) {
		st.Export.Audio = map[string]AudioExport{
			"take":  {Format: "m4a", Bitrate: 192, SampleRate: 48000, Stereo: true, Loudness: "podcast", Trim: true, Extras: []string{"script", "script", "nope"}},
			"voice": {Format: "aiff", Bits: 12, SampleRate: 22050, Loudness: "loud", Bitrate: 9999},
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	got := s.Get().Export.Audio
	if tk := got["take"]; tk.Format != "m4a" || tk.Bitrate != 192 || tk.SampleRate != 48000 || !tk.Stereo || tk.Loudness != "podcast" ||
		!tk.Trim || !slices.Equal(tk.Extras, []string{"script"}) {
		t.Errorf("take %+v", tk)
	}
	if v := got["voice"]; v.Format != "" || v.Bits != 0 || v.SampleRate != 0 || v.Loudness != "" || v.Bitrate != 512 {
		t.Errorf("voice %+v", v)
	}
	got["take"] = AudioExport{}
	if s.Get().Export.Audio["take"].Format != "m4a" {
		t.Error("Get shares its map")
	}
}
