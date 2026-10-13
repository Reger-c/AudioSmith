package main

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/hehehai/AudioSmith/internal/catalog"
	"github.com/hehehai/AudioSmith/internal/core"
	"github.com/hehehai/AudioSmith/internal/engine/proto"
	"github.com/hehehai/AudioSmith/internal/gallery/persona"
	"github.com/hehehai/AudioSmith/internal/jobs"
	vd "github.com/hehehai/AudioSmith/internal/voicedesign"
)

const (
	qwenBase = "mlx-community/Qwen3-TTS-12Hz-0.6B-Base-8bit"
	omni     = "mlx-community/OmniVoice-bf16"
)

func designAttrs(a vd.Attributes) json.RawMessage {
	b, _ := json.Marshal(a)
	return b
}

func TestVoicesLibrary(t *testing.T) {
	a, _ := newVoiceTestApp(t)
	tt := ui.NewTester(a.view, 1200, 1800)
	addVoice(t, a, core.Voice{Name: "Narrator", Kind: core.VoiceCloned, RefText: "Once upon a time.", Style: "calm"}, 6)
	addVoice(t, a, core.Voice{Name: "Grandma", Kind: core.VoiceDesigned, DesignAttrs: designAttrs(vd.Attributes{Gender: "female", Age: "elderly"}), Seed: 7}, 0)
	a.navigate("/voices")
	tt.Frame()
	if !tt.HasText("Narrator") || !tt.HasText("Grandma") || !tt.HasText("2 voices") {
		t.Fatalf("library: %q", tt.Texts())
	}
	if !strings.Contains(strings.Join(tt.Texts(), "|"), "Cloned voice · ") {
		t.Error("kind not shown")
	}

	// Search covers styles; the kind filter.
	a.voiceQuery = "calm"
	tt.Frame()
	if !tt.HasText("Narrator") || tt.HasText("Grandma") {
		t.Errorf("search: %q", tt.Texts())
	}
	a.voiceQuery = ""
	tt.Click("Designed")
	tt.Frame()
	if tt.HasText("Narrator") || !tt.HasText("Grandma") {
		t.Errorf("kind filter: %q", tt.Texts())
	}
	tt.Click("All")
	a.voiceQuery = "zzz"
	tt.Frame()
	if !tt.HasText("No matches") {
		t.Errorf("no matches: %q", tt.Texts())
	}
	a.voiceQuery = ""
	tt.Frame()

	// The editor: rename and save.
	if err := tt.Click("Narrator"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if a.edit == nil || !tt.HasText("Edit voice profile") || !tt.HasText("Replace reference…") {
		t.Fatalf("editor: %q", tt.Texts())
	}
	a.edit.name = "Old Narrator"
	tt.Frame()
	if err := tt.Click("Save"); err != nil {
		t.Fatal(err)
	}
	if v, _ := a.lib.svc.Voices.Get(context.Background(), a.edit.id); v.Name != "Old Narrator" || v.RefText != "Once upon a time." {
		t.Fatalf("saved %+v", v)
	}
	tt.Frame()
	if !tt.HasText("Old Narrator") {
		t.Errorf("snapshot not updated: %q", tt.Texts())
	}

	// Delete, after confirming.
	id := a.edit.id
	tt.Click("Delete")
	tt.Frame()
	if !a.deleteOpen {
		t.Fatal("no confirmation")
	}
	tt.Click("Cancel")
	tt.Frame()
	if a.findVoice(id) == nil {
		t.Fatal("deleted without confirming")
	}
	tt.Click("Delete")
	tt.Frame()
	tt.Click("Delete voice") // the alert's button
	tt.Frame()
	if a.findVoice(id) != nil || a.edit != nil {
		t.Fatal("not deleted")
	}
	if _, err := os.Stat(filepath.Join(a.lib.store.Voices().VoiceDir(), id)); !os.IsNotExist(err) {
		t.Error("files remain")
	}
}

func TestVoicesEmptyAndReload(t *testing.T) {
	dir := t.TempDir()
	a, _, _ := newTestAppIn(t, dir)
	tt := ui.NewTester(a.view, 1200, 1800)
	a.navigate("/voices")
	tt.Frame()
	if !tt.HasText("No saved voices yet") || !tt.HasText("Open the gallery") {
		t.Fatalf("empty: %q", tt.Texts())
	}
	tt.Click("Design a voice")
	if a.router.Path() != "/design" {
		t.Errorf("path %s", a.router.Path())
	}
	addVoice(t, a, core.Voice{Name: "Kept", Kind: core.VoiceCloned}, 3)

	// The next start reads it back.
	b, _, _ := newTestAppIn(t, dir)
	if len(b.voices) != 1 || b.voices[0].Name != "Kept" {
		t.Fatalf("reloaded %+v", b.voices)
	}
}

func TestCloneFlowUpload(t *testing.T) {
	a, eng := newVoiceTestApp(t)
	tt := ui.NewTester(a.view, 1200, 1800)
	a.navigate("/voices")
	tt.Frame()
	tt.Click("New voice from a clip")
	tt.Frame()
	if a.router.Path() != "/voices/new" || !tt.HasText("Drop a clip here, or click to choose") || !tt.HasText("Record") {
		t.Fatalf("clone page: %s %q", a.router.Path(), tt.Texts())
	}
	// A 30 s clip: by energy, its best 15 s start at 12 s, at once.
	clip := filepath.Join(t.TempDir(), "long.wav")
	writeSpeech(clip, 30, []span{{0.5, 1.2}, {12, 18}, {18.4, 26}, {27, 29}})
	a.cloneDropped([]string{clip})
	tt.Frame()
	s := &a.clone
	if !s.hasClip() || s.seconds < 29.9 || len(s.peaks) != peakCount {
		t.Fatalf("clip %+v", s)
	}
	if s.selLo < 11 || s.selLo > 12 || s.sel().Len() > refIdealHigh+0.01 {
		t.Errorf("best window %.2f–%.2f", s.selLo, s.selHi)
	}
	// Then the engine's VAD (Silero) has its say, while the user hasn't.
	vad := eng.lastOf(t, proto.MethodVADDetect)
	if p := params[proto.VADParams](t, vad); p.Audio != s.work || p.Model != defaultModel(catalog.DefaultVAD) {
		t.Errorf("vad params %+v", p)
	}
	eng.succeed(vad.ID, proto.VADResult{Regions: []proto.Region{{Start: 2, End: 9}, {Start: 9.5, End: 16}}})
	if s.selLo < 1.8 || s.selLo > 2 {
		t.Errorf("VAD window %.2f–%.2f", s.selLo, s.selHi)
	}
	// The kept part is transcribed.
	p := answerTranscribe(t, eng, "This is what my voice sounds like.")
	if p.Model != defaultModel(catalog.DefaultSTT) {
		t.Errorf("stt model %q", p.Model)
	}
	if s.transcript != "This is what my voice sounds like." || s.asr != "done" {
		t.Errorf("transcript %q %s", s.transcript, s.asr)
	}
	if !tt.HasText("long.wav") || !tt.HasText("Save as voice profile") {
		t.Errorf("clip view: %q", tt.Texts())
	}

	// Typing wins over a later machine transcript.
	s.transcript = "My own words."
	tt.Frame()
	tt.Click("Transcribe the part kept")
	answerTranscribe(t, eng, "Machine words.")
	if s.transcript != "My own words." || s.autoText != "Machine words." {
		t.Errorf("transcript overwritten: %q", s.transcript)
	}

	// Keep all: 30 s needs no transcript; over 75 s would.
	tt.Frame()
	tt.Click("Keep all")
	if s.sel().Len() < 29.9 || a.cloneProblem() != "" {
		t.Errorf("keep all: %v %q", s.sel(), a.cloneProblem())
	}
	tt.Click("Best part")
	if s.sel().Len() > refIdealHigh+0.01 {
		t.Error("best part")
	}

	// Saving needs a name.
	tt.Frame()
	tt.Click("Save voice")
	if len(a.voices) != 0 {
		t.Fatal("saved without a name")
	}
	s.name = "My Narrator"
	a.clonePortrait(testPNG(t, 300, 200))
	tt.Frame()
	if s.portrait == nil {
		t.Fatal("portrait not taken")
	}
	if err := tt.Click("Save voice"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if len(a.voices) != 1 {
		t.Fatalf("voices %+v", a.voices)
	}
	v := a.voices[0]
	if v.Name != "My Narrator" || v.RefText != "My own words." || v.Portrait != "portrait.png" || v.RefSeconds > refIdealHigh+0.01 || v.RefSeconds < 5 {
		t.Fatalf("voice %+v", v)
	}
	if secs, err := decodeWAV(a.voicePath(&v, v.RefAudio), filepath.Join(t.TempDir(), "x.wav")); err != nil || secs > 15.1 {
		t.Errorf("reference %v %v", secs, err)
	}
	if a.router.Path() != "/voices" || a.edit == nil || a.edit.id != v.ID {
		t.Errorf("after save: %s %+v", a.router.Path(), a.edit)
	}
	// The Studio's model (Kokoro) can't reuse prompts: none is encoded.
	for _, r := range eng.sent {
		if r.Cmd == proto.MethodTTSEncodeVoice {
			t.Error("encoded a prompt for Kokoro")
		}
	}
	// A model that can: the prompt is cached in the voice.
	e, _ := catalog.Builtin().ByRepo(qwenBase)
	e.Capabilities = append(e.Capabilities, catalog.CapVoicePrompt)
	a.encodePrompt(v, e)
	enc := eng.lastOf(t, proto.MethodTTSEncodeVoice)
	ep := params[proto.EncodeVoiceParams](t, enc)
	if ep.Model != qwenBase || ep.RefAudio != a.voicePath(&v, v.RefAudio) || ep.RefText != "My own words." {
		t.Fatalf("encode %+v", ep)
	}
	os.WriteFile(ep.PromptPath, []byte("prompt"), 0o644)
	eng.succeed(enc.ID, proto.EncodeVoiceResult{VoicePrompt: ep.PromptPath, Model: qwenBase, Format: "qwen3_tts/1"})
	if got := a.findVoice(v.ID); got.Prompts[qwenBase] == "" {
		t.Errorf("prompt not kept: %+v", got.Prompts)
	}
}

func TestCloneRules(t *testing.T) {
	for _, c := range []struct {
		secs  float64
		text  bool
		tone  string
		check string
	}{
		{0.5, false, "danger", "Too short"},
		{3, false, "warning", "Short"},
		{10, false, "ok", "Good length"},
		{40, false, "warning", "Longer than 15"},
		{80, false, "danger", "add the transcript"},
		{80, true, "warning", "long references"},
	} {
		msg, tone := refAdvice(c.secs, c.text)
		if tone != c.tone || !strings.Contains(msg, c.check) {
			t.Errorf("%v s: %s %q", c.secs, tone, msg)
		}
	}
	// No speech found: the first 15 s.
	if w := bestWindow(nil, 40, 15); w != (span{0, 15}) {
		t.Errorf("no speech %v", w)
	}
	// Short clips are kept whole.
	if w := bestWindow([]span{{1, 2}}, 9, 15); w != (span{0, 9}) {
		t.Errorf("short %v", w)
	}
	// The most speech wins…
	w := bestWindow([]span{{2, 9}, {10, 20}}, 40, 15)
	if w.Start < 9.89 || w.Start > 9.91 || w.End < 24.89 || w.End > 24.91 {
		t.Errorf("most speech %v", w)
	}
	// …and a word straddling the end is left out.
	for _, speech := range [][]span{
		{{0.5, 3}, {4, 11}, {11.6, 19}, {20, 27}, {28, 31}},
		{{1, 8}, {8.5, 12}, {12.5, 19}},
		{{2, 6}, {7, 13}, {14, 18}, {30, 33}},
	} {
		w := bestWindow(speech, 40, 15)
		if w.Len() > 15.0001 || w.Len() < 5 {
			t.Errorf("%v: length %v", speech, w)
		}
		for _, s := range speech {
			if s.Start < w.End && s.End > w.End && s.Start-w.Start >= refIdealLow {
				t.Errorf("%v: %v cuts %v", speech, w, s)
			}
		}
	}

	a, eng := newVoiceTestApp(t)
	a.startClone("")
	long := filepath.Join(t.TempDir(), "long.wav")
	writeSpeech(long, 80, []span{{1, 79}})
	a.cloneLoad(long, "long.wav", false)
	// The engine's VAD fails (no model): the energy VAD's window stays.
	eng.fail(eng.lastOf(t, proto.MethodVADDetect).ID, proto.CodeModelNotDownloaded, "no model")
	if a.clone.sel().Len() > 15.01 || a.clone.asr != "running" {
		t.Errorf("after VAD failure: %v %s", a.clone.sel(), a.clone.asr)
	}
	a.clone.selLo, a.clone.selHi = 0, 80
	a.clone.transcript = ""
	if p := a.cloneProblem(); !strings.Contains(p, "75 seconds") {
		t.Errorf("over 75 s: %q", p)
	}
	a.clone.transcript = "The whole text."
	if p := a.cloneProblem(); p != "" {
		t.Errorf("with transcript: %q", p)
	}
	a.clone.selLo, a.clone.selHi = 10, 10.5
	if p := a.cloneProblem(); !strings.Contains(p, "too short") {
		t.Errorf("short: %q", p)
	}

	// Anything else is decoded by the engine's media worker.
	a.cloneLoad("/x/interview.mp4", "interview.mp4", false)
	if a.clone.busy == "" {
		t.Error("not busy while decoding")
	}
	dec := eng.lastOf(t, proto.MethodMediaDecode)
	dp := params[proto.DecodeParams](t, dec)
	if dp.Input != "/x/interview.mp4" || dp.SampleRate != refRate || dp.Channels != 1 || dec.Spec.Lane != jobs.LaneMedia {
		t.Fatalf("decode %+v %s", dp, dec.Spec.Lane)
	}
	writeSpeech(dp.Output.Path, 8, []span{{0.5, 7.5}})
	eng.succeed(dec.ID, proto.DecodeResult{AudioOut: proto.AudioOut{Output: dp.Output.Path, SampleRate: refRate, Channels: 1, AudioSeconds: 8}})
	if a.clone.err != "" || a.clone.seconds != 8 || !a.clone.hasClip() || a.clone.asr != "running" {
		t.Errorf("mp4: %q %v", a.clone.err, a.clone.seconds)
	}
	// A file the engine can't read.
	a.cloneLoad("/x/broken.m4a", "broken.m4a", false)
	eng.fail(eng.lastOf(t, proto.MethodMediaDecode).ID, proto.CodeUnsupportedMedia, "bad")
	if !strings.Contains(a.clone.err, "format cannot be read") || a.clone.hasClip() {
		t.Errorf("broken: %q", a.clone.err)
	}
	// A slow, older clip loses to a newer one.
	a.cloneLoad("/x/a.mp3", "a.mp3", false)
	old := eng.lastOf(t, proto.MethodMediaDecode)
	short := filepath.Join(t.TempDir(), "short.wav")
	writeSpeech(short, 6, []span{{0.3, 5.7}})
	a.cloneLoad(short, "short.wav", false)
	op := params[proto.DecodeParams](t, old)
	writeSpeech(op.Output.Path, 8, nil)
	eng.succeed(old.ID, proto.DecodeResult{AudioOut: proto.AudioOut{Output: op.Output.Path, AudioSeconds: 8}})
	if a.clone.srcName != "short.wav" || a.clone.seconds > 6.1 {
		t.Errorf("older clip won: %s %v", a.clone.srcName, a.clone.seconds)
	}
}

func TestCloneNoASR(t *testing.T) {
	a, eng := newVoiceTestApp(t)
	tt := ui.NewTester(a.view, 1200, 1800)
	a.startClone("")
	clip := filepath.Join(t.TempDir(), "c.wav")
	writeSpeech(clip, 8, []span{{0.5, 7}})
	a.cloneLoad(clip, "c.wav", false)
	eng.fail(eng.lastOf(t, proto.MethodSTTTranscribe).ID, proto.CodeModelNotDownloaded, "Whisper is not downloaded")
	tt.Frame()
	if a.clone.asr != "noasr" || !tt.HasText("No speech-to-text model is installed: transcribing needs one.") {
		t.Fatalf("no ASR: %s %q", a.clone.asr, tt.Texts())
	}
	tt.Click("Retry transcription")
	eng.fail(eng.lastOf(t, proto.MethodSTTTranscribe).ID, proto.CodeOOM, "oom")
	tt.Frame()
	if a.clone.asr != "failed" || !tt.HasText("Transcription failed") {
		t.Errorf("failed: %s %q", a.clone.asr, tt.Texts())
	}
	tt.Click("Retry transcription")
	answerTranscribe(t, eng, "Hello.")
	if a.clone.transcript != "Hello." {
		t.Errorf("retry: %q", a.clone.transcript)
	}
	tt.Frame()
	tt.Click("Clean reference noise")
	eng.fail(eng.lastOf(t, proto.MethodEnhanceRun).ID, proto.CodeModelNotDownloaded, "no model")
	tt.Frame()
	if !tt.HasText("Could not clean the clip: The model is not downloaded. Download it on the Models page.") {
		t.Errorf("clean failure: %q", tt.Texts())
	}
	tt.Click("Models")
	if a.router.Path() != "/models" {
		t.Errorf("Models link: %s", a.router.Path())
	}
}

func TestCloneRecordAndUseOnce(t *testing.T) {
	a, eng := newVoiceTestApp(t)
	tt := ui.NewTester(a.view, 1200, 1800)
	a.startClone("")
	tt.Frame()
	if len(a.clone.devices) != 3 {
		t.Fatalf("devices %+v", a.clone.devices)
	}
	a.clone.mic.device = a.clone.devices[1].ID
	a.clone.mic.channels = 1
	tt.Frame()
	if !tt.HasText("Fake USB Interface") {
		t.Errorf("device picker: %q", tt.Texts())
	}
	if err := tt.Click("Record"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	m := &a.clone.mic
	if m.phase != "recording" || !tt.HasText("Input detected") || !tt.HasText("Stop recording") {
		t.Fatalf("recording: %+v %q", m, tt.Texts())
	}
	start := params[proto.CaptureParams](t, eng.lastOf(t, proto.MethodCaptureStart))
	if start.Device != a.clone.devices[1].ID || start.Channels != 1 || !strings.HasPrefix(start.Output.Path, a.referencesDir()) {
		t.Errorf("capture params %+v", start)
	}
	writeSpeech(start.Output.Path, 6, []span{{0.3, 5.7}})
	tt.Click("Stop recording")
	tt.Frame()
	s := &a.clone
	if !s.hasClip() || s.srcName != "Recording" || s.busy == "" {
		t.Fatalf("after recording: %+v", s)
	}
	// A recording is cleaned, then transcribed.
	answerEnhance(t, eng)
	answerTranscribe(t, eng, "Recorded words.")
	if !s.cleaned || s.transcript != "Recorded words." {
		t.Fatalf("cleaned/transcribed: %+v", s)
	}
	tt.Frame()
	tt.Click("Undo")
	if s.cleaned || s.original != "" {
		t.Error("undo clean")
	}

	// Use it once in the Studio: the model switches to one that clones.
	tt.Frame()
	if err := tt.Click("Use without saving"); err != nil {
		t.Fatal(err)
	}
	if a.router.Path() != "/studio" || a.studioClip == nil || a.preset().Repo != qwenBase {
		t.Fatalf("use once: %s %+v %s", a.router.Path(), a.studioClip, a.preset().Repo)
	}
	if !a.studioUsesSavedVoice(a.preset()) {
		t.Error("the Studio doesn't know")
	}
	take := a.recipe(time.Now())
	a.applyStudioVoice(&take, a.preset())
	if take.RefAudio != a.studioClip.Path || take.RefText != "Recorded words." || take.VoiceID != "" {
		t.Errorf("take %+v", take)
	}
	if _, err := os.Stat(take.RefAudio); err != nil {
		t.Error(err)
	}

	// The microphone refused.
	a.startClone("")
	eng.callErr[proto.MethodCaptureStart] = &proto.Error{Code: proto.CodePermissionDenied, Message: "denied"}
	tt.Frame()
	tt.Click("Record")
	tt.Frame()
	if !tt.HasText("AudioSmith may not use the microphone. Allow it in System Settings → Privacy & Security → Microphone.") {
		t.Errorf("denied: %q", tt.Texts())
	}
}

func TestReplaceReference(t *testing.T) {
	a, eng := newVoiceTestApp(t)
	tt := ui.NewTester(a.view, 1200, 1800)
	v := addVoice(t, a, core.Voice{Name: "Me", Kind: core.VoiceCloned, RefText: "old"}, 5)
	// Lock it to a take and verify it.
	tk := core.Take{ID: "take1", Text: "Locked.", Model: qwenBase, Seed: 9, VoiceID: v.ID, SampleRate: 24000, AudioSeconds: 1}
	addTake(t, a, tk, 1)
	ctx := context.Background()
	if _, err := a.lib.svc.Voices.Lock(ctx, v.ID, "take1"); err != nil {
		t.Fatal(err)
	}
	rec := filepath.Join(t.TempDir(), "consent.wav")
	writeSpeech(rec, 3, []span{{0, 3}})
	if _, err := a.lib.svc.Voices.SetConsent(ctx, v.ID, core.ConsentRecording{Audio: rec, Statement: consentStatement()}); err != nil {
		t.Fatal(err)
	}
	a.openVoiceEditor(v.ID)
	tt.Frame()
	if !tt.HasText("Verified") || !tt.HasText("Locked") || !tt.HasText("Locked take") {
		t.Fatalf("badges: %q", tt.Texts())
	}
	tt.Click("Replace reference…")
	tt.Frame()
	if !tt.HasText("Replace the reference of “Me”") {
		t.Fatalf("replace page: %q", tt.Texts())
	}
	clip := filepath.Join(t.TempDir(), "new.wav")
	writeSpeech(clip, 7, []span{{0.3, 6.5}})
	a.cloneLoad(clip, "new.wav", false)
	tt.Frame()
	if err := tt.Click("Replace reference"); err != nil {
		t.Fatal(err)
	}
	got := a.findVoice(v.ID)
	if got == nil || got.Locked || got.Verified() || got.RefText != a.clone.transcript || got.RefSeconds < 6.9 {
		t.Fatalf("after replace %+v", got)
	}
	_ = eng
}

func TestVoiceTryAndLock(t *testing.T) {
	a, eng := newVoiceTestApp(t)
	tt := ui.NewTester(a.view, 1200, 1800)
	v := addVoice(t, a, core.Voice{Name: "Me", Kind: core.VoiceCloned, RefText: "Reference words.", Style: "whisper", Language: "en"}, 5)
	a.openVoiceEditor(v.ID)
	tt.Frame()
	tt.Click("Try this voice")
	tt.Frame()
	if !tt.HasText("Test phrase") {
		t.Fatalf("try: %q", tt.Texts())
	}
	om, _ := catalog.Builtin().ByRepo(omni)
	a.edit.tryModel = om.Label
	acceptLicences(t, a)
	tt.Frame()
	if err := tt.Click("Generate preview"); err != nil {
		t.Fatal(err)
	}
	req := eng.last(t)
	if req.Cmd != "synthesize" || req.Model != omni || req.RefAudio != a.voicePath(&v, v.RefAudio) || req.RefText != "Reference words." {
		t.Fatalf("request %+v", req.SynthesizeParams)
	}
	// OmniVoice reads tags: the style becomes "whisper".
	if req.Voice != "whisper" {
		t.Errorf("style sent as %q", req.Voice)
	}
	j := a.voiceJob("try", v.ID)
	if !j.running() {
		t.Fatal("no job")
	}
	eng.progress(req.ID, "generating", 0.5, 1)
	tt.Frame()
	writeTestWAV(t, req.Output.Path, 24000, 1)
	seed := int64(1234)
	eng.succeed(req.ID, proto.SynthesizeResult{Output: req.Output.Path, SampleRate: 24000, AudioSeconds: 1, Seconds: 0.5, Seed: &seed})
	tt.Frame()
	if j.take == nil || j.take.VoiceID != v.ID || !tt.HasText("1.0 s · seed 1234") {
		t.Fatalf("take %+v %q", j.take, tt.Texts())
	}
	if err := tt.Click("Lock voice identity"); err != nil {
		t.Fatal(err)
	}
	got := a.findVoice(v.ID)
	if !got.Locked || got.Seed != 1234 || got.LockTake != j.take.ID || got.CreatedWith != omni {
		t.Fatalf("locked %+v", got)
	}
	// A locked voice clones its locked take in the Studio.
	a.studioVoice = v.ID
	e, _ := catalog.Builtin().ByRepo(qwenBase)
	tk := core.Take{Text: "x", Model: qwenBase}
	a.applyStudioVoice(&tk, e)
	if tk.RefAudio != a.voicePath(got, "locked.wav") || tk.Seed != 1234 || tk.VoiceID != v.ID || tk.Voice != "" {
		t.Errorf("studio take %+v", tk)
	}
	// Unlock, confirmed.
	tt.Frame()
	tt.Click("Unlock")
	tt.Frame()
	tt.Click("Unlock voice")
	if a.findVoice(v.ID).Locked {
		t.Error("still locked")
	}
}

func TestVoiceConsent(t *testing.T) {
	a, eng := newVoiceTestApp(t)
	tt := ui.NewTester(a.view, 1200, 1800)
	v := addVoice(t, a, core.Voice{Name: "Me", Kind: core.VoiceCloned}, 5)
	a.openVoiceEditor(v.ID)
	tt.Frame()
	tt.Click("Voice ownership")
	tt.Frame()
	if !tt.HasText("“" + consentStatement() + "”") {
		t.Fatalf("statement: %q", tt.Texts())
	}
	if err := tt.Click("Record"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	start := params[proto.CaptureParams](t, eng.lastOf(t, proto.MethodCaptureStart))
	writeSpeech(start.Output.Path, 4, []span{{0.2, 3.8}})
	tt.Click("Stop recording")
	tt.Frame()
	got := a.findVoice(v.ID)
	if !got.Verified() || got.Consent.Statement != consentStatement() || got.Consent.Locale != "en" {
		t.Fatalf("consent %+v", got.Consent)
	}
	if !tt.HasText("Verified") {
		t.Errorf("badge: %q", tt.Texts())
	}
	tt.Click("Revoke")
	tt.Frame()
	tt.Click("Revoke verification")
	if a.findVoice(v.ID).Verified() {
		t.Error("not revoked")
	}
}

// testPNG writes a w×h PNG with EXIF-like text chunk and returns its path.
func testPNG(t *testing.T, w, h int) string {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			img.Set(x, y, color.RGBA{uint8(x), uint8(y), 128, 255})
		}
	}
	var buf bytes.Buffer
	png.Encode(&buf, img)
	path := filepath.Join(t.TempDir(), "face.png")
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestPortraits(t *testing.T) {
	data, err := portraitPNG(testPNG(t, 640, 360))
	if err != nil {
		t.Fatal(err)
	}
	img, format, err := image.Decode(bytes.NewReader(data))
	if err != nil || format != "png" || img.Bounds().Dx() != portraitSize || img.Bounds().Dy() != portraitSize {
		t.Fatalf("%v %s %v", err, format, img.Bounds())
	}
	// The centre square: the left 140 columns are cropped away, so the
	// first pixel's red is about 140.
	if r, _, _, _ := img.At(0, 0).RGBA(); r>>8 < 130 || r>>8 > 150 {
		t.Errorf("crop: red %d", r>>8)
	}
	if _, err := portraitPNG("/x/face.gif"); err != errPortraitType {
		t.Errorf("gif: %v", err)
	}
	big := filepath.Join(t.TempDir(), "big.png")
	os.WriteFile(big, make([]byte, portraitMaxBytes+1), 0o644)
	if _, err := portraitPNG(big); err != errPortraitLarge {
		t.Errorf("big: %v", err)
	}
	fake := filepath.Join(t.TempDir(), "fake.jpg")
	os.WriteFile(fake, []byte("not an image"), 0o644)
	if _, err := portraitPNG(fake); err != errPortraitType {
		t.Errorf("fake: %v", err)
	}

	// On a saved voice.
	a, _ := newVoiceTestApp(t)
	tt := ui.NewTester(a.view, 1200, 1800)
	v := addVoice(t, a, core.Voice{Name: "Face", Kind: core.VoiceCloned}, 3)
	a.setVoicePortrait(v.ID, testPNG(t, 100, 100))
	got := a.findVoice(v.ID)
	if got.Portrait != "portrait.png" {
		t.Fatalf("portrait %+v", got)
	}
	a.navigate("/voices")
	tt.Frame()
	tt.Frame()
	if a.portraits[v.ID] == nil {
		t.Error("portrait not decoded")
	}
	a.setVoicePortrait(v.ID, fake)
	tt.Frame()
	if !tt.HasText("Choose a PNG, JPEG or WebP image.") {
		t.Errorf("toast: %q", tt.Texts())
	}
}

func TestVoiceFilesUI(t *testing.T) {
	a, _ := newVoiceTestApp(t)
	tt := ui.NewTester(a.view, 1200, 1800)
	ctx := context.Background()
	v := addVoice(t, a, core.Voice{Name: "Shared", Kind: core.VoiceCloned, RefText: "Hello."}, 4)
	out := filepath.Join(t.TempDir(), "Shared.asvoice")
	if err := a.lib.svc.Voices.Export(ctx, v.ID, out, core.VoiceExport{IncludeReference: true}); err != nil {
		t.Fatal(err)
	}
	// Opened from Finder.
	a.openVoiceFile(out)
	tt.Frame()
	if len(a.voices) != 2 || a.edit == nil || a.edit.id == v.ID || a.router.Path() != "/voices" {
		t.Fatalf("import: %d %+v", len(a.voices), a.edit)
	}
	if !tt.HasText("Imported “Shared”") {
		t.Errorf("toast: %q", tt.Texts())
	}
	// Dropped on the library: a voice file imports, a clip starts a voice.
	a.voicesDropped([]string{out})
	if len(a.voices) != 3 {
		t.Errorf("drop import: %d", len(a.voices))
	}
	clip := filepath.Join(t.TempDir(), "drop.wav")
	writeSpeech(clip, 6, []span{{0.2, 5.8}})
	a.voicesDropped([]string{clip})
	if a.router.Path() != "/voices/new" || !a.clone.hasClip() {
		t.Errorf("drop clip: %s", a.router.Path())
	}
	// A broken file.
	bad := filepath.Join(t.TempDir(), "bad.asvoice")
	os.WriteFile(bad, []byte("zip?"), 0o644)
	a.importVoice(bad)
	tt.Frame()
	if !tt.HasText("bad.asvoice is not a voice file, or it is damaged.") {
		t.Errorf("bad: %q", tt.Texts())
	}

	// The non-commercial note on export.
	nc := core.Voice{CreatedWith: omni}
	if !strings.Contains(ncNote(&nc), "non-commercial") || ncNote(&core.Voice{CreatedWith: qwenBase}) != "" {
		t.Error("nc note")
	}
	labels, ids := licenceChoices()
	if len(labels) != len(persona.Licences) || ids[0] != "" || labels[0] != "Personal use (no sharing)" {
		t.Errorf("licences %v %v", labels, ids)
	}
}

func TestStudioSeam(t *testing.T) {
	a, _ := newVoiceTestApp(t)
	kok, _ := catalog.Builtin().ByRepo(kokoro)
	om, _ := catalog.Builtin().ByRepo(omni)
	base, _ := catalog.Builtin().ByRepo(qwenBase)
	d := addVoice(t, a, core.Voice{Name: "Elder", Kind: core.VoiceDesigned, Language: "en",
		DesignAttrs: designAttrs(vd.Attributes{Gender: "male", Age: "elderly", Accent: "british"}), Seed: 42}, 0)
	c := addVoice(t, a, core.Voice{Name: "Clone", Kind: core.VoiceCloned, RefText: "Words.", Style: "calm"}, 4)

	a.studioVoice = d.ID
	tk := core.Take{Model: omni}
	a.applyStudioVoice(&tk, om)
	if tk.VoiceID != d.ID || tk.Seed != 42 || !strings.Contains(tk.Voice, "elderly") || !strings.Contains(tk.Voice, "british accent") {
		t.Errorf("design on OmniVoice %+v", tk)
	}
	tk = core.Take{Model: kokoro, Voice: "af_heart"}
	a.applyStudioVoice(&tk, kok)
	if tk.Voice != "af_heart" || tk.VoiceID != "" || a.studioUsesSavedVoice(kok) {
		t.Errorf("design on Kokoro %+v", tk)
	}
	// Without a sample a designed voice can't be cloned by Qwen3 Base.
	if ok, _ := voiceUsable(a.findVoice(d.ID), base); ok {
		t.Error("design usable on base")
	}

	a.studioVoice = c.ID
	tk = core.Take{Model: qwenBase}
	a.applyStudioVoice(&tk, base)
	if tk.RefAudio != a.voicePath(&c, c.RefAudio) || tk.RefText != "Words." || tk.Voice != "" || tk.VoiceID != c.ID {
		t.Errorf("clone on Qwen3 Base %+v", tk)
	}
	tk = core.Take{Model: omni}
	a.applyStudioVoice(&tk, om)
	if tk.Voice != "" || tk.RefAudio == "" {
		// "calm" has no OmniVoice tag: nothing is sent as style.
		t.Errorf("clone on OmniVoice %+v", tk)
	}

	// A cached prompt is offered for models that reuse one.
	pr := filepath.Join(t.TempDir(), "p.bin")
	os.WriteFile(pr, []byte("prompt"), 0o644)
	cc, _ := a.lib.svc.Voices.SaveFiles(context.Background(), *a.findVoice(c.ID), core.VoiceFiles{Prompt: pr, PromptModel: qwenBase})
	withPrompt := base
	withPrompt.Capabilities = append(withPrompt.Capabilities, catalog.CapVoicePrompt)
	if got := a.studioVoicePrompt(withPrompt); got != a.voicePath(&cc, cc.Prompts[qwenBase]) || got == "" || a.studioVoicePrompt(base) != "" {
		t.Errorf("voice prompt %q", got)
	}

	// The picker lists only the voices the model can use.
	a.studioVoice = ""
	labels, _ := a.studioVoiceOptions(kok)
	if len(labels) != 1 {
		t.Errorf("Kokoro options %v", labels)
	}
	labels, ids := a.studioVoiceOptions(om)
	if len(labels) != 3 || ids[1] == "" {
		t.Errorf("OmniVoice options %v", labels)
	}
	// It renders in a view, and Manage voices… opens the library.
	a.studioVoice = c.ID
	tt := ui.NewTester(func(ctx *ui.Context) {
		ui.Column(ctx).Fill().Children(func() { a.voicePicker(ctx, kok) })
	}, 400, 300)
	if !tt.HasText("Kokoro 82M can't clone voices. Choose a model that clones, such as Qwen3-TTS Base or OmniVoice.") && !strings.Contains(strings.Join(tt.Texts(), "|"), "can't clone voices") {
		t.Errorf("picker: %q", tt.Texts())
	}
	tt.Click("Manage voices…")
	if a.router.Path() != "/voices" {
		t.Errorf("manage: %s", a.router.Path())
	}
	// Use in Studio picks a model that can use the voice.
	a.useVoiceInStudio(a.findVoice(d.ID))
	if a.preset().Repo != omni || a.studioVoice != d.ID || a.router.Path() != "/studio" {
		t.Errorf("use in studio: %s", a.preset().Repo)
	}
	// Deleting the voice clears the choice.
	a.lib.svc.Voices.Delete(context.Background(), d.ID)
	if a.studioVoice != "" {
		t.Error("deleted voice still chosen")
	}
}
