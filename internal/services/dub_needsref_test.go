package services

import (
	"context"
	"testing"

	"github.com/hehehai/AudioSmith/internal/dub"
)

// A model that only clones (DubModelCaps.NeedsReference): a line cast with
// the default voice or a voice without a reference clones its speaker; a
// speaker without a reference says so (the render then fails with
// needs_reference). Other models keep the cast.
func TestDubVoiceNeedsReference(t *testing.T) {
	s := newDubEnv(t, 1).dubs // its voices are "speaker-<ref>", without a reference
	p := &dub.Project{ID: "p1", Cast: map[string]string{"S1": dub.VoiceDefault, "S2": "preset:x", "S3": dub.VoiceDefault},
		References: map[string]dub.Reference{"S1": {File: "refs/S1.wav", Text: "Hi."}, "S2": {File: "refs/S2.wav", Text: "Yo."}}}
	moss := DubModelCaps{Clone: true, NeedsReference: true}
	for _, c := range []struct {
		speaker string
		caps    DubModelCaps
		ref     bool
		warn    string
	}{
		{"S1", moss, true, ""},
		{"S2", moss, true, ""},
		{"S3", moss, false, DubWarnNoReference},
		{"S1", DubModelCaps{Clone: true}, false, ""},
	} {
		v, warn := s.voiceFor(context.Background(), p, dub.Segment{ID: "a", Speaker: c.speaker}, "es", c.caps, map[string]dub.VoiceSpec{})
		if (v.RefAudio != "") != c.ref || warn != c.warn {
			t.Errorf("%s %+v: %+v %q", c.speaker, c.caps, v, warn)
		}
	}
}
