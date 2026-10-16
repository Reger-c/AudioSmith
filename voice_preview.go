package main

// Voice previews (thread #60): what a voice picker's ▶ plays. A model's
// built-in speaker plays its bundled clip (assets/voice-previews, made by
// scripts/make-voice-previews.sh), even before the model is downloaded;
// a saved voice plays the start of its sample or reference. A voice with
// neither (a description, an unknown speaker) has no ▶.

import (
	"embed"
	"io/fs"
	"path"
	"strings"
	"time"

	_ "github.com/egoist/mygo/audio/vorbis" // the bundled previews are Ogg Vorbis
	"github.com/hehehai/AudioSmith/internal/catalog"
	"github.com/hehehai/AudioSmith/internal/core"
	"github.com/hehehai/AudioSmith/internal/i18n"
)

//go:embed assets/voice-previews
var previewFS embed.FS

// previewDir is where the clips are in previewFS.
const previewDir = "assets/voice-previews"

// previewLimit is how much of a saved voice's audio a preview plays.
const previewLimit = 6 * time.Second

// previewName is the name of a built-in speaker's clip, without its
// extension: "qwen3_tts-Vivian".
func previewName(family, speaker string) string { return family + "-" + speaker }

// builtinPreview is the bundled clip of speaker of model e, or nil.
func builtinPreview(e catalog.Entry, speaker string) []byte {
	if speaker == "" || e.Family == "" || strings.ContainsAny(speaker, "/\\") {
		return nil
	}
	b, err := fs.ReadFile(previewFS, path.Join(previewDir, previewName(e.Family, speaker)+".ogg"))
	if err != nil {
		return nil
	}
	return b
}

// voicePreview is what a picker row's ▶ plays: a bundled clip (data) or
// the start of a file (path); the zero value has none.
type voicePreview struct {
	id   string // the player's id: "vp:" + what it is
	data []byte
	path string
}

// ok reports whether there is something to play.
func (p voicePreview) ok() bool { return p.id != "" && (len(p.data) > 0 || p.path != "") }

// speakerPreview is the preview of a built-in speaker of model e.
func speakerPreview(e catalog.Entry, speaker string) voicePreview {
	if b := builtinPreview(e, speaker); b != nil {
		return voicePreview{id: "vp:" + e.Family + ":" + speaker, data: b}
	}
	return voicePreview{}
}

// savedVoicePreview is the preview of a saved voice: its sample, else
// its reference; a built-in speaker's saved voice without either plays
// the speaker's clip (of the voice's model, or any model with that
// speaker).
func (a *app) savedVoicePreview(v *core.Voice) voicePreview {
	if v == nil {
		return voicePreview{}
	}
	if v.Sample != "" || v.RefAudio != "" {
		if p := a.voiceAudio(v); p != "" {
			return voicePreview{id: "vp:voice:" + v.ID, path: p}
		}
	}
	if v.Kind == core.VoiceBuiltin && v.Speaker != "" {
		if v.Model != "" {
			if p := speakerPreview(entryFor(v.Model), v.Speaker); p.ok() {
				return p
			}
		}
		for _, e := range catalog.Builtin().Entries {
			if p := speakerPreview(e, v.Speaker); p.ok() {
				return p
			}
		}
	}
	return voicePreview{}
}

// previewTip is the ▶'s tooltip: "Hear Vivian".
func previewTip(name string, playing bool) string {
	if playing {
		return i18n.T("Stop the preview")
	}
	return i18n.T("Hear %s", name)
}
