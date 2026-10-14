package settings

// The Export audio dialog's last choices, kept per kind of audio (a
// Studio take, a voice preview, a converted recording, a take of the
// Local API): each kind opens with what was used for it last time.

import (
	"maps"
	"slices"
)

// Export: the dialog's last choices by kind ("take", "voice", "convert",
// "api"). A kind without an entry opens with the dialog's defaults for
// it, so the zero value is a fresh install's.
type Export struct {
	Audio map[string]AudioExport `json:"audio,omitempty"`
}

// AudioExport is what the dialog was last set to for one kind.
type AudioExport struct {
	Format     string   `json:"format,omitempty"`     // "wav", "m4a", "mp3", "flac", "opus"
	Bits       int      `json:"bits,omitempty"`       // WAV and FLAC: 16, 24, 32 (float, WAV only)
	Bitrate    int      `json:"bitrate,omitempty"`    // lossy formats, kb/s
	SampleRate int      `json:"sampleRate,omitempty"` // Hz; 0 = the source's
	Stereo     bool     `json:"stereo,omitempty"`     // mono sources become two channels
	Loudness   string   `json:"loudness,omitempty"`   // "" (off), "podcast" (−16), "speech" (−18), "broadcast" (−23)
	Trim       bool     `json:"trim,omitempty"`       // leading and trailing silence
	Extras     []string `json:"extras,omitempty"`     // files saved beside the audio: "script", "subtitles"
}

// ExportFormats are the formats the dialog writes.
var ExportFormats = []string{"wav", "m4a", "mp3", "flac", "opus"}

// ExportLoudness are the loudness targets the dialog offers ("" is off).
var ExportLoudness = []string{"", "podcast", "speech", "broadcast"}

func normalizeExport(s *Settings) {
	if len(s.Export.Audio) == 0 {
		s.Export.Audio = nil
		return
	}
	for k, e := range s.Export.Audio {
		if !slices.Contains(ExportFormats, e.Format) {
			e.Format = ""
		}
		if !slices.Contains([]int{0, 16, 24, 32}, e.Bits) {
			e.Bits = 0
		}
		e.Bitrate = min(max(e.Bitrate, 0), 512)
		if !slices.Contains([]int{0, 44100, 48000}, e.SampleRate) {
			e.SampleRate = 0
		}
		if !slices.Contains(ExportLoudness, e.Loudness) {
			e.Loudness = ""
		}
		var extras []string
		for _, x := range e.Extras {
			if (x == "script" || x == "subtitles") && !slices.Contains(extras, x) {
				extras = append(extras, x)
			}
		}
		e.Extras = extras
		s.Export.Audio[k] = e
	}
}

func cloneExport(s *Settings) {
	if s.Export.Audio == nil {
		return
	}
	m := maps.Clone(s.Export.Audio)
	for k, e := range m {
		e.Extras = slices.Clone(e.Extras)
		m[k] = e
	}
	s.Export.Audio = m
}
