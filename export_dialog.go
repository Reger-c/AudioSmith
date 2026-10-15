package main

// The Export audio dialog (thread #31): a take exported with settings.
//
//   - Format: WAV always; M4A (AAC) through the engine's media.encode; MP3,
//     FLAC and Opus through ffmpeg, greyed out with a tip pointing at
//     Settings → Audio tools while it is not set up.
//   - By format: WAV's bit depth (16, 24, 32 float), FLAC's (16, 24), the
//     lossy formats' bitrate; the sample rate (not Opus: always 48 kHz) and
//     the channels.
//   - Processing (services.ExportAudio, internal/dsp): a loudness target
//     with a true-peak ceiling, and trimmed leading and trailing silence.
//   - The file's name and, for formats with tags, its title and artist;
//     files saved beside it (the script, subtitles from the chunks).
//   - The size and length it will have, then Export…: the save panel, the
//     export in the background with toasts, the record in Projects →
//     Exports.
//
// What it offers and how it opens depend on the kind of take (exportKinds:
// a Studio take, a voice's preview, a converted recording, a take of the
// Local API); the last settings are remembered per kind (settings.Export).
// History, the Studio's latest take, the History's details and Projects
// open it (a.openExport).

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"github.com/hehehai/AudioSmith/internal/core"
	"github.com/hehehai/AudioSmith/internal/doc/subtitle"
	"github.com/hehehai/AudioSmith/internal/engine/proto"
	"github.com/hehehai/AudioSmith/internal/ffmpeg"
	"github.com/hehehai/AudioSmith/internal/i18n"
	"github.com/hehehai/AudioSmith/internal/jobs"
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

// ---- State ----

// exportState is the app's part: the dialog, while open.
type exportState struct {
	exp *exportForm
}

// exportForm is the dialog's form. The controls bind to its fields
// directly (indices into their options), so a choice survives the builds
// that follow it.
type exportForm struct {
	open bool
	kind exportKind
	take core.Take
	src  string // the take's audio

	format   int                   // into exportFormats
	bits     [5]int                // per format: into its bits
	bitrate  [5]int                // per format: into its bitrates
	rate     [5]int                // per format: into its rateOptions
	channels int                   // 0 the source's, 1 the other
	loud     string                // the loudness choice's label (ui.Select)
	trim     bool                  // leading and trailing silence
	name     string                // without the extension
	title    string                // tag
	artist   string                // tag
	extras   map[string]*bool      // files beside the audio, by extra
	info     *services.AudioInfo   // the source, once read
	availOK  map[string]bool       // formats that can be written now
	availWhy map[string]string     // why the others cannot
	encode   services.AudioEncoder // tests' encoder; nil: a.exportEncoder()
}

// openExport opens the dialog for a take.
func (a *app) openExport(t *core.Take) {
	if t == nil {
		return
	}
	k := exportKindOf(t)
	prefs, ok := a.prefs.Export.Audio[k.id]
	if !ok {
		prefs = k.defaults
	}
	f := &exportForm{open: true, kind: k, take: *t, src: a.takeStore.AudioPath(*t),
		availOK: map[string]bool{}, availWhy: map[string]string{}, extras: map[string]*bool{}}
	a.exportAvailability(f)
	f.apply(prefs, k.defaults)
	f.name = strings.TrimSuffix(exportName(t), ".wav")
	f.title, f.artist = exportTitle(t.Text), "AudioSmith"
	for _, x := range k.extras {
		on := slices.Contains(prefs.Extras, x)
		f.extras[x] = &on
	}
	a.exp = f
	src := f.src
	a.background(func() {
		info, err := services.AnalyzeAudio(src)
		a.post(func() {
			if a.exp != f {
				return
			}
			if err != nil {
				a.toast(i18n.T("Could not read the audio: %v", err))
				return
			}
			f.info = &info
		})
	})
}

// exportAvailability says which formats can be written now, and why not.
func (a *app) exportAvailability(f *exportForm) {
	exportAvailable(f, a.lib.encodeExecutor() != nil && a.engineEncodes(), a.mediaTool())
}

// exportAvailable fills a form's formats that can be written: M4A when
// m4a (the engine encodes), the others through ffmpeg (tool, nil = none).
func exportAvailable(f *exportForm, m4a bool, tool *ffmpeg.Tool) {
	for _, ef := range exportFormats {
		switch ef.id {
		case services.FormatWAV:
			f.availOK[ef.id] = true
		case services.FormatM4A:
			f.availOK[ef.id] = m4a
			f.availWhy[ef.id] = i18n.T("M4A needs the speech engine; it is not running.")
		default:
			f.availOK[ef.id] = tool.CanEncode(ef.id)
			f.availWhy[ef.id] = i18n.T("%s needs ffmpeg: set it up in Settings → Audio tools.", ef.label)
		}
	}
}

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

// ---- The view ----

// exportDialog is the dialog, while open (shell.go draws it over the page).
func (a *app) exportDialog(c *ui.Context) {
	f := a.exp
	if f == nil {
		return
	}
	if !f.open {
		a.exp = nil
		return
	}
	t := c.Theme()
	k := tok(t)
	ef := f.fmt_()
	sub := kindLabel(&f.take) + " · " + fileDetails(&f.take)
	dialogFrame(c, "export-dialog", &f.open, i18n.T("Export audio"), sub, px(t, 560), func() {
		sectionTitle(c, i18n.T("Audio"))
		formRow(c, "ex-format", i18n.T("Format"), "", func() {
			labels := make([]string, len(exportFormats))
			off := make([]bool, len(exportFormats))
			tips := make([]string, len(exportFormats))
			for i, x := range exportFormats {
				labels[i], off[i] = x.label, !f.availOK[x.id]
				if off[i] {
					tips[i] = f.availWhy[x.id]
				}
			}
			segmentedSome(c.Key("ex-format-seg"), &f.format, labels, off, tips).Label(i18n.T("Format"))
			if slices.Contains(off, true) {
				infoTip(c.Key("ex-format-why"), exportMissing(f))
			}
		})
		formRow(c, "ex-loud", i18n.T("Loudness"), i18n.T("Brings the whole file to a loudness target, with peaks held under a ceiling (−1.5 dBTP for podcasts, −1 dBTP otherwise). −16 LUFS suits podcasts and streaming, −23 LUFS broadcast (EBU R128)."), func() {
			opts := make([]string, len(exportLoudness))
			for i, key := range exportLoudness {
				opts[i] = loudnessLabel(key)
			}
			if f.info != nil && f.info.Integrated > -70 {
				caption(c, i18n.T("Now %s LUFS", signedDB(f.info.Integrated))).SingleLine()
			}
			ui.Select(c.Key("ex-loud-select"), &f.loud, opts).Label(i18n.T("Loudness")).Width(px(t, 200)).Shrink(1).MinWidth(px(t, 120))
		})

		sectionTitle(c, i18n.T("File"))
		formRow(c, "ex-name", i18n.T("File name"), "", func() {
			ui.TextInput(c.Key("ex-name-input"), &f.name).Label(i18n.T("File name")).Width(px(t, 220)).Shrink(1).MinWidth(px(t, 120))
			ui.Text(c, "."+ef.ext).TextColor(k.Text2).SingleLine()
		})
		if ef.tags && f.kind.tags {
			formRow(c, "ex-title", i18n.T("Title"), i18n.T("Written into the file, for players and libraries."), func() {
				ui.TextInput(c.Key("ex-title-input"), &f.title).Label(i18n.T("Title")).Width(px(t, 240)).Shrink(1).MinWidth(px(t, 120))
			})
			formRow(c, "ex-artist", i18n.T("Artist"), "", func() {
				ui.TextInput(c.Key("ex-artist-input"), &f.artist).Label(i18n.T("Artist")).Width(px(t, 240)).Shrink(1).MinWidth(px(t, 120))
			})
		}
		if extras := f.extrasShown(); len(extras) > 0 {
			sectionTitle(c, i18n.T("Also save"))
			ui.Column(c.Key("ex-extras")).Gap(px(t, sp2)).Padding(px(t, 2), 0).Children(func() {
				for _, x := range extras {
					ui.Checkbox(c.Key("ex-extra-"+x), f.extras[x], extraLabel(x, ef.ext))
				}
			})
		}
		ui.Box(c).Height(px(t, sp1))
		a.advancedSection(c, "export", f.changes(), false, f.restore, func() {
			if len(ef.bits) > 0 {
				formRow(c, "ex-bits", i18n.T("Bit depth"), i18n.T("16-bit plays everywhere; 24-bit keeps more headroom for editing; 32-bit float keeps the model's own samples."), func() {
					labels := make([]string, len(ef.bits))
					for i, b := range ef.bits {
						labels[i] = bitsLabel(b)
					}
					segmentedFit(c.Key("ex-bits-"+ef.id), &f.bits[f.format], labels...).Label(i18n.T("Bit depth"))
				})
			}
			if len(ef.bitrates) > 0 {
				formRow(c, "ex-bitrate", i18n.T("Bitrate"), i18n.T("Higher keeps more detail and makes a larger file. Speech sounds clean from the lowest."), func() {
					labels := make([]string, len(ef.bitrates))
					for i, b := range ef.bitrates {
						labels[i] = i18n.T("%d kb/s", b)
					}
					segmentedFit(c.Key("ex-bitrate-"+ef.id), &f.bitrate[f.format], labels...).Label(i18n.T("Bitrate"))
				})
			}
			if ef.rates {
				tip := i18n.T("Raising it does not add detail; choose 44.1 or 48 kHz when an app or a platform asks for it.")
				if f.srcRate() < ef.minRate {
					tip = i18n.T("%[1]s at %[2]s cannot hold these bitrates, so it is written at 44.1 or 48 kHz.", ef.label, khz(f.srcRate()))
				}
				formRow(c, "ex-rate", i18n.T("Sample rate"), tip, func() {
					opts := f.rateOptions(ef)
					labels := make([]string, len(opts))
					for i, r := range opts {
						if r == 0 {
							labels[i] = i18n.X("audio", "Original (%s)", khz(f.srcRate()))
						} else {
							labels[i] = khz(r)
						}
					}
					f.rate[f.format] = min(max(f.rate[f.format], 0), len(opts)-1)
					segmentedFit(c.Key("ex-rate-"+ef.id), &f.rate[f.format], labels...).Label(i18n.T("Sample rate"))
				})
			}
			formRow(c, "ex-channels", i18n.T("Channels"), i18n.T("Stereo puts the same voice in both channels, for players and editors that expect two."), func() {
				orig, other := i18n.T("Original (mono)"), i18n.T("Stereo")
				if f.srcChannels() > 1 {
					orig, other = i18n.T("Original (stereo)"), i18n.T("Mono")
				}
				segmentedFit(c.Key("ex-ch-seg"), &f.channels, orig, other).Label(i18n.T("Channels"))
			})
			formRow(c, "ex-trim", i18n.T("Trim silence"), i18n.T("Cuts the silence before the first sound and after the last, keeping a short breath at each end."), func() {
				if f.trim && f.info != nil && f.info.Seconds-f.info.Trimmed >= 0.05 {
					caption(c, i18n.T("−%.1f s", f.info.Seconds-f.info.Trimmed)).SingleLine()
				}
				ui.Switch(c.Key("ex-trim-switch"), &f.trim).Label(i18n.T("Trim silence"))
			})
		})
	}, func() {
		size := humanBytes(f.estimate())
		ui.Text(c, i18n.T("About %s · %s", size, i18n.T("%.1f s", f.seconds()))).FontSize(px(t, fsCaption)).TextColor(k.Text2).
			SingleLine().Ellipsis("…").MinWidth(0).Grow(1)
		if ui.Button(c.Key("ex-cancel"), i18n.T("Cancel")).Clicked() {
			f.open = false
		}
		if primaryButton(c.Key("ex-export"), i18n.T("Export…")).Disabled(!f.availOK[ef.id]).Clicked() {
			a.exportChoose(f)
		}
	})
}

// extrasShown are the kind's extras that apply to this take.
func (f *exportForm) extrasShown() []string {
	var out []string
	for _, x := range f.kind.extras {
		if x == "subtitles" && (f.take.Render == nil || len(f.take.Render.Chunks) == 0) {
			continue
		}
		out = append(out, x)
	}
	return out
}

// extraLabel names an extra.
func extraLabel(x, ext string) string {
	switch x {
	case "script":
		return i18n.T("The script, as a text file (.txt)")
	case "subtitles":
		return i18n.T("Subtitles timed to the audio (.srt)")
	}
	return x
}

// exportMissing says what greys formats out.
func exportMissing(f *exportForm) string {
	var lines []string
	if !f.availOK[services.FormatM4A] {
		lines = append(lines, f.availWhy[services.FormatM4A])
	}
	if !f.availOK[services.FormatMP3] {
		lines = append(lines, i18n.T("MP3, FLAC and Opus need ffmpeg: set it up in Settings → Audio tools."))
	}
	return strings.Join(lines, "\n")
}

// khz is "24 kHz", "44.1 kHz".
func khz(rate int) string {
	return i18n.T("%s kHz", strings.TrimSuffix(fmt.Sprintf("%.1f", float64(rate)/1000), ".0"))
}

// ---- Exporting ----

// exportSaveDir is where tests export (no save panel).
var exportSaveDir string

// exportChoose asks where to save, then exports there.
func (a *app) exportChoose(f *exportForm) {
	name, ext := f.fileName(), f.fmt_().ext
	if a.win == nil {
		if exportSaveDir != "" {
			a.exportTo(f, filepath.Join(exportSaveDir, name))
		}
		return
	}
	go func() {
		dst, err := mygo.Dialog.Save(mygo.SaveDialogOptions{Parent: a.win, Title: i18n.T("Export audio"), DefaultPath: name,
			Filters: []mygo.FileFilter{{Name: strings.ToUpper(ext), Extensions: []string{ext}}}})
		if err != nil || dst == "" {
			return
		}
		a.post(func() { a.exportTo(f, dst) })
	}()
}

// exportTo exports the form's take to dst in the background, remembers
// the settings for the kind and closes the dialog.
func (a *app) exportTo(f *exportForm, dst string) {
	ef := f.fmt_()
	if filepath.Ext(dst) == "" {
		dst += "." + ef.ext
	}
	o := f.options()
	kind, prefs := f.kind.id, f.prefs()
	a.updateSettings(func(s *settings.Settings) {
		if s.Export.Audio == nil {
			s.Export.Audio = map[string]settings.AudioExport{}
		}
		s.Export.Audio[kind] = prefs
	})
	tk, src := f.take, f.src
	extras := map[string]bool{}
	for _, x := range f.extrasShown() {
		extras[x] = f.extraOn(x)
	}
	enc := f.encode
	if enc == nil {
		enc = a.exportEncoder()
	}
	f.open = false
	if a.exp == f {
		a.exp = nil
	}
	a.toast(i18n.T("Exporting %s…", filepath.Base(dst)))
	a.background(func() {
		ctx := context.Background()
		res, err := services.ExportAudio(ctx, src, dst, o, enc)
		if err == nil {
			err = writeExtras(tk, dst, res, extras)
		}
		if err == nil {
			a.recordTakeExport(tk.ID, dst, ef.ext) // Projects → Exports
		}
		a.post(func() {
			if err != nil {
				a.toast(i18n.T("Export failed: %v", err))
			} else {
				a.toast(i18n.T("Saved %s", filepath.Base(dst)))
			}
		})
	})
}

// writeExtras saves the chosen extras beside dst: the script (.txt) and
// subtitles from the take's chunks (.srt), timed to the audio written.
func writeExtras(t core.Take, dst string, res services.ExportedAudio, on map[string]bool) error {
	base := strings.TrimSuffix(dst, filepath.Ext(dst))
	if on["script"] {
		if err := os.WriteFile(base+".txt", []byte(strings.TrimSpace(t.Text)+"\n"), 0o644); err != nil {
			return err
		}
	}
	if on["subtitles"] && t.Render != nil && len(t.Render.Chunks) > 0 {
		var segs []subtitle.Segment
		for _, ch := range t.Render.Chunks {
			start, end := max(ch.Start-res.Offset, 0), min(ch.End-res.Offset, res.Seconds)
			if txt := strings.TrimSpace(markupRE.ReplaceAllString(ch.Text, " ")); txt != "" && end > start {
				segs = append(segs, subtitle.Segment{Start: secs(start), End: secs(end), Text: strings.Join(strings.Fields(txt), " ")})
			}
		}
		cues, _ := subtitle.Split(segs, subtitle.Defaults(subtitleLang(t)))
		var b strings.Builder
		if err := subtitle.WriteSRT(&b, cues); err != nil {
			return err
		}
		if err := os.WriteFile(base+".srt", []byte(b.String()), 0o644); err != nil {
			return err
		}
	}
	return nil
}

func secs(s float64) time.Duration { return time.Duration(s * float64(time.Second)) }

// subtitleLang is the language the subtitles' line lengths follow: the
// take's when it is a BCP-47 tag, else "zh" for a script in Chinese.
func subtitleLang(t core.Take) string {
	if l := strings.ToLower(t.Language); len(l) >= 2 && len(l) <= 7 && !strings.Contains(l, " ") {
		return l
	}
	for _, r := range t.Text {
		if unicode.Is(unicode.Han, r) {
			return "zh"
		}
		if unicode.Is(unicode.Hiragana, r) || unicode.Is(unicode.Katakana, r) {
			return "ja"
		}
	}
	return "en"
}

// exportEncoder writes the formats other than WAV: M4A with the engine's
// media.encode, MP3, FLAC and Opus with ffmpeg.
func (a *app) exportEncoder() services.AudioEncoder {
	return exportEncoderOf(a.lib.encodeExecutor(), a.mediaTool())
}

// exportEncoderOf is the encoder of exports over the engine's
// media.encode (ex, nil = none) and ffmpeg (tool, nil = none); the API's
// export_audio uses it too.
func exportEncoderOf(ex jobs.Executor, tool *ffmpeg.Tool) services.AudioEncoder {
	return func(ctx context.Context, in, dst string, o services.AudioExport) error {
		if o.Format == services.FormatM4A {
			if ex == nil {
				return fmt.Errorf("%s", i18n.T("M4A needs the speech engine; it is not running."))
			}
			p := proto.EncodeParams{Input: in, Output: proto.Output{Path: dst, Format: services.FormatM4A}, Codec: "aac", Bitrate: o.Bitrate,
				Metadata: proto.EncodeMetadata{Title: o.Metadata["title"], Author: o.Metadata["artist"]}}
			raw, err := json.Marshal(p)
			if err != nil {
				return err
			}
			job := jobs.Job{ID: "export-" + core.NewID(time.Now()), Spec: jobs.Spec{Kind: services.KindEncode, Lane: jobs.LaneMedia, Params: raw}}
			_, err = ex.Run(ctx, job, func(jobs.Progress) {}, nil)
			return err
		}
		if tool == nil {
			return ffmpeg.ErrNotFound
		}
		return tool.EncodeAudio(ctx, ffmpeg.EncodeOptions{Input: in, Output: dst, Format: o.Format, Bitrate: o.Bitrate, Metadata: o.Metadata}, nil)
	}
}

// changes names what the form changes from the kind's defaults for the
// chosen format (the 「高级」 settings: bit depth or bitrate, sample rate,
// channels, trimming).
func (f *exportForm) changes() []string {
	ef, i := f.fmt_(), f.format
	def := f.kind.defaults
	var out []string
	add := func(on bool, name string) {
		if on {
			out = append(out, name)
		}
	}
	add(len(ef.bits) > 0 && f.bits[i] != indexOr(ef.bits, def.Bits, 0), i18n.T("Bit depth"))
	add(len(ef.bitrates) > 0 && f.bitrate[i] != indexOr(ef.bitrates, ef.defBitrate, 0), i18n.T("Bitrate"))
	add(ef.rates && f.rate[i] != 0, i18n.T("Sample rate"))
	add(f.channels != 0, i18n.T("Channels"))
	add(f.trim, i18n.T("Trim silence"))
	return out
}

// restore puts the 「高级」 settings back to the kind's defaults.
func (f *exportForm) restore() {
	ef, i := f.fmt_(), f.format
	def := f.kind.defaults
	f.bits[i] = indexOr(ef.bits, def.Bits, 0)
	f.bitrate[i] = indexOr(ef.bitrates, ef.defBitrate, 0)
	f.rate[i], f.channels, f.trim = 0, 0, false
}
