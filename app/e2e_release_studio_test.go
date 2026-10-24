package main

// TestV02's Studio, Clone and Design steps; split from e2e_release_test.go.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hehehai/AudioSmith/internal/core"
	"github.com/hehehai/AudioSmith/internal/dsp/loudness"
	"github.com/hehehai/AudioSmith/internal/dsp/wav"
	"github.com/hehehai/AudioSmith/internal/engine/enginetest"
)

// studio: long English and Chinese scripts on Kokoro with "Even out
// volume", 24-bit, and Save as… 24-bit WAV.
func (v *v02) studio(t *testing.T) (string, string) {
	a := v.a
	a.selectModel(v.model(t, "kokoro-82m-bf16"))
	a.evenOut = true // takes are 24-bit
	var notes []string
	var first string
	for _, c := range []struct{ name, voice, text string }{
		{"en", "af_heart", longText(enSentences, 3000)},
		{"zh", "zf_xiaobei", longText(zhSentences, 1500)},
		{"mixed", "zf_xiaobei", "今天我们来聊一聊 AudioSmith 这个应用。It runs every model on your Mac, 不需要联网。"},
	} {
		a.text, a.voice, a.language = c.text, c.voice, ""
		started := time.Now()
		take := v.generate(t, "studio "+c.name, 30*time.Minute)
		if first == "" {
			first = take.ID
		}
		path := a.takeStore.AudioPath(*take)
		buf, _, err := wav.ReadFile(path)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		lufs := loudness.Integrated(buf)
		chunks := 0
		if take.Render != nil {
			chunks = len(take.Render.Chunks)
		}
		notes = append(notes, fmt.Sprintf("%s %d chars → %.0f s audio in %.0f s, %d chunks, %.1f LUFS, %d-bit, wm=%v",
			c.name, len([]rune(c.text)), take.AudioSeconds, time.Since(started).Seconds(), chunks, lufs, wavBits(path), take.Render != nil && take.Render.Watermarked))
		if lufs < -20 || lufs > -16 {
			t.Errorf("%s: %.1f LUFS", c.name, lufs)
		}
		if b := wavBits(path); b != 24 {
			t.Errorf("%s: %d-bit take, want 24", c.name, b)
		}
		if take.Render != nil {
			for _, n := range take.Render.Warnings {
				t.Logf("%s warning %+v", c.name, n)
			}
		}
	}
	// Save as… a 24-bit WAV.
	out := filepath.Join(v.dir, "studio-en-24.wav")
	if err := a.lib.svc.Render.ExportAs(context.Background(), first, out, 24); err != nil {
		t.Errorf("export: %v", err)
	} else if b := wavBits(out); b != 24 {
		t.Errorf("exported %d-bit", b)
	}
	return strings.Join(notes, "; "), first
}

// clone: a voice from a WAV file (auto transcript, noise cleaning), saved,
// then a take in it on Qwen3-TTS 0.6B Base, read back by Whisper.
func (v *v02) clone(t *testing.T) (string, string) {
	a := v.a
	refText := "I've been recording audiobooks for nearly ten years, and every morning I start with a cup of tea and a quiet room."
	ref := say(t, v.dir, "clone-ref", "Samantha", refText, 44100)
	a.startClone("")
	v.drain()
	a.cloneLoad(ref, filepath.Base(ref), false)
	v.wait(t, "clone transcript", 10*time.Minute, func() bool {
		s := &a.clone
		return s.busy == "" && s.hasClip() && s.asr != "" && s.asr != "running"
	})
	s := &a.clone
	if s.asr != "done" || overlap(refText, s.transcript) < 0.8 {
		t.Errorf("auto transcript %s %q (%s)", s.asr, s.transcript, s.asrErr)
	}
	transcript := s.transcript
	a.cloneClean(false)
	v.wait(t, "clone enhance", 5*time.Minute, func() bool { return s.busy == "" })
	if !s.cleaned {
		t.Errorf("enhance: %s", s.cleanErr)
	}
	s.name = "V02 Clone"
	n := len(a.voices)
	a.cloneSave()
	v.wait(t, "clone save", 2*time.Minute, func() bool { return len(a.voices) > n && !s.saving })
	var vo *core.Voice
	for i := range a.voices {
		if a.voices[i].Name == "V02 Clone" {
			vo = &a.voices[i]
		}
	}
	if vo == nil {
		t.Fatalf("no saved voice: %+v", a.voices)
	}
	if vo.RefAudio == "" || vo.RefText == "" {
		t.Errorf("voice %+v", vo)
	}
	id := vo.ID
	// Render with it.
	a.selectModel(v.model(t, "qwen3-tts-0.6b-base-8bit"))
	a.studioVoice, a.studioClip, a.voice, a.language = id, nil, "", ""
	a.evenOut = true
	text := "Thank you for listening. This sentence is spoken in a cloned voice made from a short recording."
	a.text = text
	started := time.Now()
	take := v.generate(t, "clone take", 10*time.Minute)
	if take.VoiceID != id || take.RefAudio == "" && len(take.VoicePrompt) == 0 {
		t.Errorf("take not in the cloned voice: voice %q ref %q prompt %q", take.VoiceID, take.RefAudio, take.VoicePrompt)
	}
	heard := v.sttCheck(t, a.takeStore.AudioPath(*take), "en")
	if overlap(text, heard) < 0.7 {
		t.Errorf("clone take says %q", heard)
	}
	return fmt.Sprintf("transcript %.0f%% words, cleaned=%v; take %.1f s in %.0f s, read back %.0f%%",
		100*overlap(refText, transcript), s.cleaned, take.AudioSeconds, time.Since(started).Seconds(), 100*overlap(text, heard)), id
}

// design: a voice from a description (Qwen3-TTS VoiceDesign, or the
// model in AUDIOSMITH_V02_DESIGN), previewed, saved, then used in the Studio.
func (v *v02) design(t *testing.T) string {
	a := v.a
	id := firstOf(os.Getenv("AUDIOSMITH_V02_DESIGN"), "qwen3-tts-1.7b-voicedesign-4bit")
	e := v.model(t, id)
	a.design.reset()
	a.navigate("/design")
	v.drain()
	d := &a.design
	d.model = e.Label
	d.description = "A calm, warm middle-aged male narrator with a deep voice, speaking slowly."
	d.reparse()
	d.script = "Welcome back. Tonight's story begins on a quiet road, far from the city."
	a.designGenerate()
	v.wait(t, "design preview", 15*time.Minute, func() bool { j := a.voiceJob("design", ""); return j != nil && !j.running() })
	j := a.voiceJob("design", "")
	if j.err != "" || j.take == nil {
		t.Fatalf("design preview: %s", j.err)
	}
	preview := j.take
	d.saveName = "V02 Narrator"
	n := len(a.voices)
	a.designSave()
	v.wait(t, "design save", 2*time.Minute, func() bool { return len(a.voices) > n && d.linked != "" })
	vo := a.findVoice(d.linked)
	if vo == nil || vo.Kind != core.VoiceDesigned {
		t.Fatalf("designed voice %+v", vo)
	}
	if vo.Sample == "" {
		t.Errorf("no sample kept with the designed voice")
	}
	// Use it in the Studio.
	a.selectModel(e)
	a.studioVoice, a.studioClip, a.voice = vo.ID, nil, ""
	a.text = "The road was empty, and the only sound was the wind in the trees."
	take := v.generate(t, "design take", 15*time.Minute)
	au, _ := enginetest.ReadWAV(a.takeStore.AudioPath(*take))
	f0 := medianF0(au.Mono(), au.Rate)
	heard := v.sttCheck(t, a.takeStore.AudioPath(*take), "en")
	if overlap(a.text, heard) < 0.7 {
		t.Errorf("design take says %q", heard)
	}
	if f0 > 180 {
		t.Errorf("a deep male voice at %.0f Hz", f0)
	}
	return fmt.Sprintf("%s: preview %.1f s; take %.1f s, F0 %.0f Hz, read back %.0f%%", e.ID, preview.AudioSeconds, take.AudioSeconds, f0, 100*overlap(a.text, heard))
}
