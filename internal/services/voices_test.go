package services

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/hehehai/AudioSmith/internal/core"
	"github.com/hehehai/AudioSmith/internal/gallery/persona"
	vd "github.com/hehehai/AudioSmith/internal/voicedesign"
)

// testWAV is a short silent mono 16-bit WAV.
func testWAV(frames int) []byte {
	b := make([]byte, 44+2*frames)
	copy(b, "RIFF")
	binary.LittleEndian.PutUint32(b[4:], uint32(36+2*frames))
	copy(b[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(b[16:], 16)
	binary.LittleEndian.PutUint16(b[20:], 1)
	binary.LittleEndian.PutUint16(b[22:], 1)
	binary.LittleEndian.PutUint32(b[24:], 24000)
	binary.LittleEndian.PutUint32(b[28:], 48000)
	binary.LittleEndian.PutUint16(b[32:], 2)
	binary.LittleEndian.PutUint16(b[34:], 16)
	copy(b[36:], "data")
	binary.LittleEndian.PutUint32(b[40:], uint32(2*frames))
	return b
}

func writeFile(t *testing.T, path string, data []byte) string {
	t.Helper()
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func TestVoiceFiles(t *testing.T) {
	e := newEnv(t, nil)
	vs := e.svc.Voices
	dir := t.TempDir()
	ref := writeFile(t, filepath.Join(dir, "Clip.WAV"), testWAV(2400))

	// A new clone: its reference is copied in; fields naming files that
	// the caller made up are ignored.
	v, err := vs.SaveFiles(ctx, core.Voice{Name: "Narrator", Kind: core.VoiceCloned, RefText: " Hello there. ", RefSeconds: 0.1,
		Style: "calm", Sample: "../etc/passwd", Locked: true, Consent: core.VoiceConsent{State: core.ConsentVerified}},
		core.VoiceFiles{Reference: ref, Portrait: []byte("\x89PNG\r\n\x1a\nxx")})
	if err != nil {
		t.Fatal(err)
	}
	if v.RefAudio != "reference.wav" || v.RefText != "Hello there." || v.RefSeconds != 0.1 || v.Portrait != "portrait.png" ||
		v.Sample != "" || v.Locked || v.Verified() {
		t.Fatalf("voice %+v", v)
	}
	if !exists(vs.FilePath(v, "reference.wav")) || !exists(vs.FilePath(v, "portrait.png")) {
		t.Fatal("files not copied in")
	}
	if vs.FilePath(v, "../x") != "" || vs.FilePath(v, "") != "" {
		t.Error("FilePath accepts paths")
	}

	// Search covers style and transcript.
	for _, q := range []string{"calm", "hello", "narr", "ca"} {
		if l, _ := vs.List(ctx, core.VoiceQuery{Search: q}); len(l) != 1 {
			t.Errorf("search %q: %d", q, len(l))
		}
	}

	// Save keeps the files; the user's edits apply.
	v.Name, v.RefAudio, v.Portrait = "Narrator 2", "", ""
	if v, err = vs.Save(ctx, v); err != nil || v.RefAudio != "reference.wav" || v.Portrait != "portrait.png" || v.Name != "Narrator 2" {
		t.Fatalf("%v %+v", err, v)
	}

	// Usage, lock: a take made with the voice.
	clips := e.st.Takes().AudioDir()
	take := core.Take{ID: "t1", CreatedAt: time.Now(), File: "t1.wav", Text: "Locked line.", Model: "m/omni", Seed: 77, VoiceID: v.ID}
	writeFile(t, filepath.Join(clips, "t1.wav"), testWAV(4800))
	if _, err := e.svc.Takes.Add(ctx, take); err != nil {
		t.Fatal(err)
	}
	other := core.Take{ID: "t2", CreatedAt: time.Now(), File: "t2.wav", Text: "x", Model: "m"}
	writeFile(t, filepath.Join(clips, "t2.wav"), testWAV(10))
	e.svc.Takes.Add(ctx, other)
	if u, err := vs.Usage(ctx, v.ID); err != nil || u.Takes != 1 || u.LatestTake != "t1" {
		t.Fatalf("usage %+v %v", u, err)
	}
	if _, err := vs.Lock(ctx, v.ID, "t2"); !errors.Is(err, core.ErrInvalid) {
		t.Errorf("lock to another voice's take: %v", err)
	}
	if v, err = vs.Lock(ctx, v.ID, "t1"); err != nil || !v.Locked || v.LockTake != "t1" || v.Seed != 77 ||
		v.Sample != "locked.wav" || v.SampleText != "Locked line." || v.CreatedWith != "m/omni" {
		t.Fatalf("lock %v %+v", err, v)
	}
	// A locked seed can't be edited by Save.
	v.Seed = 5
	if v, _ = vs.Save(ctx, v); v.Seed != 77 {
		t.Errorf("seed %d", v.Seed)
	}
	// The lock survives deleting the take.
	e.svc.Takes.Delete(ctx, "t1")
	if !exists(vs.FilePath(v, "locked.wav")) {
		t.Fatal("locked sample gone")
	}

	// Consent.
	rec := writeFile(t, filepath.Join(dir, "me.wav"), testWAV(100))
	if _, err := vs.SetConsent(ctx, v.ID, core.ConsentRecording{Audio: rec}); !errors.Is(err, core.ErrInvalid) {
		t.Error("consent without statement")
	}
	if v, err = vs.SetConsent(ctx, v.ID, core.ConsentRecording{Audio: rec, Statement: "I confirm.", Locale: "en"}); err != nil || !v.Verified() ||
		v.Consent.Recording != "consent.wav" || v.Consent.At.IsZero() {
		t.Fatalf("consent %v %+v", err, v.Consent)
	}

	// A new reference clears the lock and the consent, and their files.
	ref2 := writeFile(t, filepath.Join(dir, "other.flac"), []byte("fLaC...."))
	if v, err = vs.SaveFiles(ctx, v, core.VoiceFiles{Reference: ref2}); err != nil {
		t.Fatal(err)
	}
	if v.RefAudio != "reference.flac" || v.Locked || v.Sample != "" || v.Verified() {
		t.Fatalf("after replace %+v", v)
	}
	for _, gone := range []string{"reference.wav", "locked.wav", "consent.wav"} {
		if exists(vs.FilePath(v, gone)) {
			t.Errorf("%s remains", gone)
		}
	}

	// Remove the portrait; revoke; unlock.
	if v, _ = vs.SaveFiles(ctx, v, core.VoiceFiles{RemovePortrait: true}); v.Portrait != "" || exists(vs.FilePath(v, "portrait.png")) {
		t.Error("portrait not removed")
	}
	if v, err = vs.RevokeConsent(ctx, v.ID); err != nil || v.Consent != (core.VoiceConsent{}) {
		t.Error(err)
	}

	if _, err := vs.Save(ctx, core.Voice{Name: "x", Kind: core.VoiceDesigned, Seed: -1}); !errors.Is(err, core.ErrInvalid) {
		t.Error("negative seed")
	}
	if _, err := vs.Save(ctx, core.Voice{Name: "x", Kind: core.VoiceDesigned, DesignAttrs: json.RawMessage(`[1]`)}); !errors.Is(err, core.ErrInvalid) {
		t.Error("bad attrs")
	}
	if err := vs.Delete(ctx, v.ID); err != nil || exists(filepath.Join(e.st.Voices().VoiceDir(), v.ID)) {
		t.Errorf("delete: %v", err)
	}
}

func TestVoiceExportImport(t *testing.T) {
	e := newEnv(t, nil)
	vs := e.svc.Voices
	dir := t.TempDir()

	// A designed voice with a sample.
	attrs, _ := json.Marshal(vd.Attributes{Gender: "female", Age: "elderly", Styles: []string{"warm"}})
	sample := writeFile(t, filepath.Join(dir, "s.wav"), testWAV(2400))
	d, err := vs.SaveFiles(ctx, core.Voice{Name: "Grandma", Kind: core.VoiceDesigned, Design: "a warm old lady", DesignAttrs: attrs,
		Seed: 42, Language: "en", Tags: []string{"story"}, CreatedWith: "mlx-community/OmniVoice-bf16"}, core.VoiceFiles{Sample: sample})
	if err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "grandma.asvoice")
	if err := vs.Export(ctx, d.ID, out, core.VoiceExport{AppVersion: "0.1.0"}); err != nil {
		t.Fatal(err)
	}
	b, err := persona.ReadFile(out, persona.DefaultLimits)
	if err != nil {
		t.Fatal(err)
	}
	m := b.Manifest
	if m.Kind != persona.KindDesign || m.Recipe == nil || m.Recipe.Seed != 42 || m.Recipe.Attrs.Age != "elderly" ||
		m.Engine.Repo != "mlx-community/OmniVoice-bf16" || m.CreatedBy.App != "AudioSmith" || m.Licence.SPDX != persona.DefaultLicence {
		t.Fatalf("manifest %+v", m)
	}

	imp, err := vs.Import(ctx, out)
	if err != nil {
		t.Fatal(err)
	}
	g := imp.Voice
	if g.ID == d.ID || g.Kind != core.VoiceDesigned || g.Seed != 42 || g.Design != "a warm old lady" || g.Sample != "sample.wav" ||
		g.Origin != "asvoice:"+d.ID || imp.Format != "asvoice" {
		t.Fatalf("imported %+v", g)
	}
	var ga vd.Attributes
	json.Unmarshal(g.DesignAttrs, &ga)
	if ga.Gender != "female" || !slices.Contains(ga.Styles, "warm") {
		t.Errorf("attrs %+v", ga)
	}
	if l, _ := vs.List(ctx, core.VoiceQuery{Origin: "asvoice:" + d.ID}); len(l) != 1 {
		t.Errorf("origin query: %d", len(l))
	}
	// Re-exporting keeps the persona id.
	out2 := filepath.Join(dir, "again.asvoice")
	if err := vs.Export(ctx, g.ID, out2, core.VoiceExport{}); err != nil {
		t.Fatal(err)
	}
	if b2, _ := persona.ReadFile(out2, persona.DefaultLimits); b2.Manifest.ID != d.ID {
		t.Errorf("persona id %q", b2.Manifest.ID)
	}

	// A verified clone with its reference: consent comes back as
	// "included, not verified".
	ref := writeFile(t, filepath.Join(dir, "ref.wav"), testWAV(1200))
	c, _ := vs.SaveFiles(ctx, core.Voice{Name: "Me", Kind: core.VoiceCloned, RefText: "My words."}, core.VoiceFiles{Reference: ref})
	c, _ = vs.SetConsent(ctx, c.ID, core.ConsentRecording{Audio: ref, Statement: "I consent."})
	out3 := filepath.Join(dir, "me.asvoice")
	if err := vs.Export(ctx, c.ID, out3, core.VoiceExport{IncludeReference: true, Licence: "CC-BY-4.0"}); err != nil {
		t.Fatal(err)
	}
	imp, err = vs.Import(ctx, out3)
	if err != nil {
		t.Fatal(err)
	}
	if v := imp.Voice; v.Consent.State != core.ConsentIncluded || v.Verified() || v.RefAudio != "reference.wav" || v.RefText != "My words." {
		t.Fatalf("clone import %+v", v)
	}
	// Without the reference the preview stands in, with a warning.
	out4 := filepath.Join(dir, "noref.asvoice")
	vs.Export(ctx, c.ID, out4, core.VoiceExport{})
	if imp, err = vs.Import(ctx, out4); err != nil || len(imp.Warnings) != 1 || imp.Voice.RefAudio != "reference.wav" {
		t.Fatalf("%v %+v", err, imp)
	}

	// No audio at all: no preview.
	empty, _ := vs.Save(ctx, core.Voice{Name: "Empty", Kind: core.VoiceDesigned})
	if err := vs.Export(ctx, empty.ID, filepath.Join(dir, "e.asvoice"), core.VoiceExport{}); !errors.Is(err, ErrNoPreview) {
		t.Errorf("export without audio: %v", err)
	}
	// Not a voice file.
	junk := writeFile(t, filepath.Join(dir, "x.asvoice"), []byte("nope"))
	if _, err := vs.Import(ctx, junk); !errors.Is(err, core.ErrInvalid) {
		t.Errorf("junk: %v", err)
	}
	if _, err := vs.Import(ctx, writeFile(t, filepath.Join(dir, "x.zip"), []byte("x"))); !errors.Is(err, core.ErrInvalid) {
		t.Errorf("zip: %v", err)
	}
	if n := len(e.st.Voices().VoiceDir()); n == 0 || strings.Contains(e.st.Voices().VoiceDir(), "..") {
		t.Fatal("voice dir")
	}
}

func TestVoiceConsentM0(t *testing.T) {
	var v core.Voice
	if err := json.Unmarshal([]byte(`{"id":"a","consent":true}`), &v); err != nil || !v.Verified() {
		t.Fatalf("%v %+v", err, v)
	}
	if err := json.Unmarshal([]byte(`{"id":"a","consent":{"state":"included-unverified"}}`), &v); err != nil || v.Consent.State != core.ConsentIncluded {
		t.Fatalf("%v %+v", err, v)
	}
}

func TestGalleryFavorites(t *testing.T) {
	e := newEnv(t, nil)
	f := GalleryFavorites{Store: e.st.Favorites()}
	f.SaveFavorite(ctx, "b", true)
	f.SaveFavorite(ctx, "a", true)
	f.SaveFavorite(ctx, "a", true)
	f.SaveFavorite(ctx, "c", true)
	f.SaveFavorite(ctx, "c", false)
	ids, err := f.LoadFavorites(ctx)
	slices.Sort(ids)
	if err != nil || !slices.Equal(ids, []string{"a", "b"}) {
		t.Fatalf("%v %v", ids, err)
	}
	if err := e.st.Favorites().Set(ctx, "", "x", true); !errors.Is(err, core.ErrInvalid) {
		t.Error(err)
	}
}
