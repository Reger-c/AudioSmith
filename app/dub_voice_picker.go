package main

// The dub's voice pickers (the cast's and a line's) in the shared voice
// picker (voice_picker.go, thread #60): a model's speakers and the saved
// voices have a ▶; Auto, the default voice and the design presets have
// nothing to play.

import (
	"strings"

	"github.com/hehehai/AudioSmith/internal/catalog"
	"github.com/hehehai/AudioSmith/internal/dub"
)

// dubVoiceOptions are the picker's rows for choices.
func (a *app) dubVoiceOptions(choices []dubVoiceChoice) []voiceOption {
	var e catalog.Entry // the dub's model, whose speakers these are
	if a.dub != nil {
		e = entryFor(a.dub.Settings.Model)
	}
	opts := make([]voiceOption, 0, len(choices))
	for _, ch := range choices {
		op := voiceOption{value: ch.ref, label: ch.label, group: ch.group}
		if id, ok := strings.CutPrefix(ch.ref, dub.SpeakerPrefix); ok {
			op.preview = speakerPreview(e, id)
		} else if v := a.findVoice(ch.ref); v != nil && ch.ref != "" {
			op.voice, op.sub, op.preview = v, voiceKindLabel(v), a.savedVoicePreview(v)
		}
		opts = append(opts, op)
	}
	return opts
}
