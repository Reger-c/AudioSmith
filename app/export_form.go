package main

// The Export audio dialog's kinds and formats, and the form's derived
// values: options by format and source, the remembered settings, the
// estimate and the file name; split from export_dialog.go.

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/hehehai/AudioSmith/internal/core"
	"github.com/hehehai/AudioSmith/internal/i18n"
	"github.com/hehehai/AudioSmith/internal/services"
	"github.com/hehehai/AudioSmith/internal/settings"
)

// ---- Kinds and formats ----

// exportKind is a kind of take, with what the dialog offers for it.
type exportKind struct {
	id       string               // settings.Export's key
	defaults settings.AudioExport // before anything was remembered
	tags     bool                 // title and artist
	extras   []string             // files beside the audio: "script", "subtitles"
}

// exportKinds are the kinds of takes History holds.
var exportKinds = map[string]exportKind{
	// A take of the Studio: for editing (24-bit), with its script and
	// subtitles.
	core.TakeStudio: {id: "take", defaults: settings.AudioExport{Format: services.FormatWAV, Bits: 24}, tags: true, extras: []string{"script", "subtitles"}},
	// A take an agent asked for (Local API, MCP): as the API writes them.
	core.TakeAPI: {id: "api", defaults: settings.AudioExport{Format: services.FormatWAV, Bits: 16}, tags: true, extras: []string{"script", "subtitles"}},
	// A recording spoken again in another voice (Tools → Convert).
	core.TakeConvert: {id: "convert", defaults: settings.AudioExport{Format: services.FormatWAV, Bits: 24}, tags: true, extras: []string{"script"}},
	// A voice's preview or sample: a short clip, plain audio.
	core.TakeVoice: {id: "voice", defaults: settings.AudioExport{Format: services.FormatWAV, Bits: 16}},
}

// exportKindOf is a take's kind (takes from before origins were kept are
// Studio takes).
func exportKindOf(t *core.Take) exportKind {
	if k, ok := exportKinds[t.Origin]; ok {
		return k
	}
	return exportKinds[core.TakeStudio]
}

// kindLabel names a take's kind in the dialog's header.
func kindLabel(t *core.Take) string {
	switch t.Origin {
	case core.TakeAPI:
		if t.Client != "" {
			return i18n.T("Local API take (%s)", t.Client)
		}
		return i18n.T("Local API take")
	case core.TakeConvert:
		return i18n.T("Converted recording")
	case core.TakeVoice:
		return i18n.T("Voice preview")
	}
	return i18n.T("Studio take")
}

// exportFormat is a format the dialog writes.
type exportFormat struct {
	id, label, ext string
	bits           []int // bit depths offered (WAV, FLAC)
	bitrates       []int // kb/s offered (lossy)
	defBitrate     int
	rates          bool // the sample rate can be chosen
	// minRate is the lowest rate that holds the bitrates: MP3 under 32 kHz
	// tops out at 160 kb/s; Apple's AAC encoder takes no more than 64
	// kb/s for 24 kHz mono (TestRealEncodeAACRates). The source's rate is
	// offered only from it.
	minRate int
	tags    bool    // title and artist are written
	ratio   float64 // FLAC: the size against the PCM, for the estimate
}

// exportFormats are the formats, in the dialog's order.
var exportFormats = []exportFormat{
	{id: services.FormatWAV, label: "WAV", ext: "wav", bits: []int{16, 24, 32}, rates: true},
	{id: services.FormatM4A, label: "M4A", ext: "m4a", bitrates: []int{128, 192, 256}, defBitrate: 128, rates: true, tags: true, minRate: 44100},
	{id: services.FormatMP3, label: "MP3", ext: "mp3", bitrates: []int{128, 192, 320}, defBitrate: 192, rates: true, tags: true, minRate: 32000},
	{id: services.FormatFLAC, label: "FLAC", ext: "flac", bits: []int{16, 24}, rates: true, tags: true, ratio: 0.6},
	{id: services.FormatOpus, label: "Opus", ext: "opus", bitrates: []int{64, 96, 128}, defBitrate: 96, tags: true},
}

func formatIndex(id string) int {
	return max(slices.IndexFunc(exportFormats, func(f exportFormat) bool { return f.id == id }), 0)
}

// exportRates are the sample rates offered besides the source's.
var exportRates = []int{44100, 48000}

// exportLoudness are the loudness choices, in order (keys of
// services.ExportLoudness; "" is off).
var exportLoudness = settings.ExportLoudness

func loudnessLabel(key string) string {
	switch key {
	case "podcast":
		return i18n.T("−16 LUFS (podcast)")
	case "speech":
		return i18n.T("−18 LUFS (speech)")
	case "broadcast":
		return i18n.T("−23 LUFS (broadcast)")
	}
	return i18n.T("Off")
}

// ---- The form ----

// apply sets the form from remembered settings (defaults fill the gaps;
// a format that cannot be written now falls back to the kind's).
func (f *exportForm) apply(p, def settings.AudioExport) {
	id := p.Format
	if id == "" || !f.availOK[id] {
		id = def.Format
	}
	f.format = formatIndex(id)
	for i, ef := range exportFormats {
		f.bits[i] = indexOr(ef.bits, def.Bits, 0)
		f.bitrate[i] = indexOr(ef.bitrates, ef.defBitrate, 0)
		if ef.id == id {
			f.bits[i] = indexOr(ef.bits, p.Bits, f.bits[i])
			f.bitrate[i] = indexOr(ef.bitrates, p.Bitrate, f.bitrate[i])
		}
	}
	for i, ef := range exportFormats {
		f.rate[i] = max(slices.Index(f.rateOptions(ef), p.SampleRate), 0)
	}
	f.channels = 0
	if p.Stereo {
		f.channels = 1
	}
	f.loud = loudnessLabel(p.Loudness)
	f.trim = p.Trim
}

// indexOr is v's index in list, or def's when v is not there (0 when
// neither is).
func indexOr(list []int, v int, def int) int {
	if i := slices.Index(list, v); i >= 0 {
		return i
	}
	if def >= 0 && def < len(list) {
		return def
	}
	if i := slices.Index(list, def); i >= 0 {
		return i
	}
	return 0
}

// fmt_ is the chosen format.
func (f *exportForm) fmt_() exportFormat {
	return exportFormats[min(max(f.format, 0), len(exportFormats)-1)]
}

// srcRate and srcChannels are the source's (the take's until it is read).
func (f *exportForm) srcRate() int {
	if f.info != nil {
		return f.info.Rate
	}
	if f.take.SampleRate > 0 {
		return f.take.SampleRate
	}
	return 24000
}

func (f *exportForm) srcChannels() int {
	if f.info != nil {
		return f.info.Channels
	}
	if f.take.Render != nil && f.take.Render.Channels > 0 {
		return f.take.Render.Channels
	}
	return 1
}

// rateOptions are the sample rates offered for a format: the source's (0)
// unless it is under the format's minimum, and those of exportRates it is
// not.
func (f *exportForm) rateOptions(ef exportFormat) []int {
	var out []int
	if f.srcRate() >= ef.minRate {
		out = append(out, 0)
	}
	for _, r := range exportRates {
		if r != f.srcRate() {
			out = append(out, r)
		}
	}
	return out
}

// loudKey is the loudness choice's key.
func (f *exportForm) loudKey() string {
	for _, k := range exportLoudness {
		if loudnessLabel(k) == f.loud {
			return k
		}
	}
	return ""
}

// extraOn says whether an extra is chosen.
func (f *exportForm) extraOn(x string) bool { return f.extras[x] != nil && *f.extras[x] }

// options are the services' settings for the export.
func (f *exportForm) options() services.AudioExport {
	ef := f.fmt_()
	o := services.AudioExport{Format: ef.id, Loudness: f.loudKey(), Trim: f.trim}
	if len(ef.bits) > 0 {
		o.Bits = ef.bits[min(f.bits[f.format], len(ef.bits)-1)]
	}
	if len(ef.bitrates) > 0 {
		o.Bitrate = ef.bitrates[min(f.bitrate[f.format], len(ef.bitrates)-1)] * 1000
	}
	if ef.rates {
		opts := f.rateOptions(ef)
		o.SampleRate = opts[min(max(f.rate[f.format], 0), len(opts)-1)]
	}
	if f.channels == 1 {
		if f.srcChannels() == 1 {
			o.Channels = 2
		} else {
			o.Channels = 1
		}
	}
	if ef.tags && f.kind.tags {
		o.Metadata = map[string]string{}
		if s := strings.TrimSpace(f.title); s != "" {
			o.Metadata["title"] = s
		}
		if s := strings.TrimSpace(f.artist); s != "" {
			o.Metadata["artist"] = s
		}
	}
	return o
}

// prefs are the settings to remember for the kind.
func (f *exportForm) prefs() settings.AudioExport {
	o := f.options()
	p := settings.AudioExport{Format: o.Format, Bits: o.Bits, Bitrate: o.Bitrate / 1000, SampleRate: o.SampleRate,
		Stereo: f.channels == 1 && f.srcChannels() == 1, Loudness: o.Loudness, Trim: o.Trim}
	for _, x := range f.kind.extras {
		if f.extraOn(x) {
			p.Extras = append(p.Extras, x)
		}
	}
	return p
}

// seconds is the output's length: the source's, or what trimming leaves.
func (f *exportForm) seconds() float64 {
	if f.info == nil {
		return f.take.AudioSeconds
	}
	if f.trim {
		return f.info.Trimmed
	}
	return f.info.Seconds
}

// estimate is the output's size, in bytes.
func (f *exportForm) estimate() int64 {
	o := f.options()
	ef := f.fmt_()
	secs := f.seconds()
	rate := o.SampleRate
	if rate == 0 {
		rate = f.srcRate()
	}
	ch := o.Channels
	if ch == 0 {
		ch = f.srcChannels()
	}
	switch {
	case o.Bitrate > 0:
		return int64(secs*float64(o.Bitrate)/8) + 1024 // the container's header and tags
	case ef.ratio > 0:
		return int64(secs * float64(rate*ch*o.Bits/8) * ef.ratio)
	}
	return int64(secs*float64(rate*ch*max(o.Bits, 16)/8)) + 44
}

// fileName is the name the save panel proposes.
func (f *exportForm) fileName() string {
	base := strings.TrimSpace(f.name)
	base = strings.TrimSuffix(base, "."+f.fmt_().ext)
	if base == "" {
		base = "AudioSmith"
	}
	return safeName(base) + "." + f.fmt_().ext
}

// safeName replaces the characters a file name may not hold.
func safeName(s string) string {
	return strings.Map(func(r rune) rune {
		if strings.ContainsRune(`/\:*?"<>|`, r) || unicode.IsControl(r) {
			return '-'
		}
		return r
	}, s)
}

// markupRE matches the script's markup ([pause 500ms], [voice:Mara]).
var markupRE = regexp.MustCompile(`\[[^\]]*\]`)

// exportTitle is a title tag from a take's script: its first sentence
// without markup, at most 60 characters.
func exportTitle(text string) string {
	s := strings.Join(strings.Fields(markupRE.ReplaceAllString(text, " ")), " ")
	if i := sentenceEnd(s); i >= 0 {
		_, n := utf8.DecodeRuneInString(s[i:])
		s = s[:i+n]
	}
	if r := []rune(s); len(r) > 60 {
		s = strings.TrimSpace(string(r[:60])) + "…"
	}
	return s
}

// signedDB writes a level with a real minus: "−12.7".
func signedDB(v float64) string { return strings.Replace(fmt.Sprintf("%.1f", v), "-", "−", 1) }
