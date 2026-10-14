package services

import (
	"context"
	"errors"
	"math"
	"os"
	"path/filepath"
	"testing"

	"github.com/hehehai/AudioSmith/internal/dsp"
	"github.com/hehehai/AudioSmith/internal/dsp/loudness"
	"github.com/hehehai/AudioSmith/internal/dsp/wav"
)

// exportSource writes 0.5 s of silence, 2 s of a 220 Hz tone at −20 dBFS
// peak, then 1 s of silence: 24 kHz mono, 16-bit, as a take.
func exportSource(t *testing.T) string {
	t.Helper()
	const rate = 24000
	b := dsp.New(rate, 1, int(3.5*rate))
	amp := dsp.Amp(-20)
	for i := int(0.5 * rate); i < int(2.5*rate); i++ {
		b.Samples[i] = float32(amp * math.Sin(2*math.Pi*220*float64(i)/rate))
	}
	path := filepath.Join(t.TempDir(), "take.wav")
	if err := wav.WriteFile(path, b, wav.PCM16, wav.Options{}); err != nil {
		t.Fatal(err)
	}
	return path
}

func readWAV(t *testing.T, path string) (dsp.Buffer, wav.Format) {
	t.Helper()
	b, f, err := wav.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b, f
}

// WAV keeps the bit depth asked for; processing applies in order.
func TestExportAudioWAV(t *testing.T) {
	src := exportSource(t)
	dir := t.TempDir()
	ctx := context.Background()
	for _, c := range []struct {
		bits int
		enc  wav.Encoding
	}{{16, wav.PCM16}, {24, wav.PCM24}, {32, wav.Float32}} {
		dst := filepath.Join(dir, "out.wav")
		res, err := ExportAudio(ctx, src, dst, AudioExport{Format: FormatWAV, Bits: c.bits}, nil)
		if err != nil {
			t.Fatal(err)
		}
		_, f := readWAV(t, dst)
		if f.Encoding != c.enc || f.Rate != 24000 || f.Channels != 1 || math.Abs(res.Seconds-3.5) > 0.001 || res.Offset != 0 || res.Bytes == 0 {
			t.Errorf("%d bits: %+v %+v", c.bits, f, res)
		}
	}

	// Trim, 48 kHz stereo, −16 LUFS.
	dst := filepath.Join(dir, "podcast.wav")
	res, err := ExportAudio(ctx, src, dst, AudioExport{Format: FormatWAV, Bits: 24, SampleRate: 48000, Channels: 2, Loudness: "podcast", Trim: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	b, f := readWAV(t, dst)
	if f.Rate != 48000 || f.Channels != 2 || f.Encoding != wav.PCM24 {
		t.Errorf("format %+v", f)
	}
	// 2 s of tone and 0.08 s kept at each end.
	if math.Abs(res.Seconds-2.16) > 0.03 || math.Abs(res.Offset-0.42) > 0.02 {
		t.Errorf("trim: %.3f s, offset %.3f", res.Seconds, res.Offset)
	}
	st := loudness.Measure(b)
	if math.Abs(st.Integrated+16) > 0.3 || st.TruePeak > -1.4 {
		t.Errorf("loudness %.2f LUFS, %.2f dBTP", st.Integrated, st.TruePeak)
	}
	if math.Abs(res.Loudness+16) > 0.3 {
		t.Errorf("reported %.2f", res.Loudness)
	}

	// −23 LUFS without trimming leaves the length.
	res, err = ExportAudio(ctx, src, dst, AudioExport{Format: FormatWAV, Bits: 32, Loudness: "broadcast"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	b, _ = readWAV(t, dst)
	if st := loudness.Measure(b); math.Abs(st.Integrated+23) > 0.3 || math.Abs(res.Seconds-3.5) > 0.001 {
		t.Errorf("broadcast: %.2f LUFS, %.3f s", st.Integrated, res.Seconds)
	}
}

// Other formats go to the encoder as a WAV: floats for lossy formats,
// FLAC's bit depth for FLAC; without an encoder they fail.
func TestExportAudioEncoder(t *testing.T) {
	src := exportSource(t)
	dir := t.TempDir()
	ctx := context.Background()
	var got []wav.Format
	var opts []AudioExport
	enc := func(ctx context.Context, in, dst string, o AudioExport) error {
		_, f := readWAV(t, in)
		got, opts = append(got, f), append(opts, o)
		return os.WriteFile(dst, []byte("encoded"), 0o644)
	}
	for _, o := range []AudioExport{
		{Format: FormatMP3, Bitrate: 320000, SampleRate: 44100, Metadata: map[string]string{"title": "Hi"}},
		{Format: FormatFLAC, Bits: 24},
		{Format: FormatFLAC, Bits: 16},
		{Format: FormatM4A, Bitrate: 128000, Channels: 2},
	} {
		res, err := ExportAudio(ctx, src, filepath.Join(dir, "out."+o.Format), o, enc)
		if err != nil || res.Bytes != int64(len("encoded")) {
			t.Fatalf("%s: %+v %v", o.Format, res, err)
		}
	}
	want := []wav.Format{{Rate: 44100, Channels: 1, Encoding: wav.Float32}, {Rate: 24000, Channels: 1, Encoding: wav.PCM24},
		{Rate: 24000, Channels: 1, Encoding: wav.PCM16}, {Rate: 24000, Channels: 2, Encoding: wav.Float32}}
	for i, w := range want {
		if got[i].Rate != w.Rate || got[i].Channels != w.Channels || got[i].Encoding != w.Encoding {
			t.Errorf("%s: the encoder read %+v, want %+v", opts[i].Format, got[i], w)
		}
	}
	if opts[0].Bitrate != 320000 || opts[0].Metadata["title"] != "Hi" {
		t.Errorf("options %+v", opts[0])
	}
	if _, err := ExportAudio(ctx, src, filepath.Join(dir, "x.opus"), AudioExport{Format: FormatOpus}, nil); !errors.Is(err, ErrNoEncoder) {
		t.Errorf("no encoder: %v", err)
	}
	// An encoder that writes nothing is an error.
	none := func(context.Context, string, string, AudioExport) error { return nil }
	if _, err := ExportAudio(ctx, src, filepath.Join(dir, "none.mp3"), AudioExport{Format: FormatMP3}, none); err == nil {
		t.Error("no file, no error")
	}
}

func TestAnalyzeAudio(t *testing.T) {
	info, err := AnalyzeAudio(exportSource(t))
	if err != nil {
		t.Fatal(err)
	}
	if info.Rate != 24000 || info.Channels != 1 || math.Abs(info.Seconds-3.5) > 0.001 || math.Abs(info.Trimmed-2.16) > 0.03 ||
		info.Integrated > -20 || info.Integrated < -30 {
		t.Errorf("%+v", info)
	}
}
