package persona

import (
	"archive/zip"
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"path/filepath"
	"slices"
	"testing"
	"time"

	vd "github.com/hehehai/AudioSmith/internal/voicedesign"
)

// wav is a valid little mono 16-bit 24 kHz WAV with n silent samples.
func wav(n int) []byte {
	var b bytes.Buffer
	le := func(v any) { _ = binary.Write(&b, binary.LittleEndian, v) }
	b.WriteString("RIFF")
	le(uint32(36 + 2*n))
	b.WriteString("WAVEfmt ")
	le(uint32(16))
	le(uint16(1))
	le(uint16(1))
	le(uint32(24000))
	le(uint32(48000))
	le(uint16(2))
	le(uint16(16))
	b.WriteString("data")
	le(uint32(2 * n))
	b.Write(make([]byte, 2*n))
	return b.Bytes()
}

var png = []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\x0dIHDR")

func designBundle() *Bundle {
	seed := int64(77)
	return &Bundle{
		Manifest: Manifest{
			ID: "01JDESIGN", Name: "Velvet Narrator", Kind: KindDesign, Language: "en",
			Tags:      []string{"narration", "warm"},
			Recipe:    &vd.Recipe{Description: "a warm  British narrator", Attrs: vd.Attributes{Gender: "female", Accent: "british", Styles: []string{"warm"}}, Seed: 77},
			Engine:    Engine{Model: "omnivoice-bf16", Family: "omnivoice", Voice: "female, british accent", Seed: &seed, Params: map[string]any{"steps": 32}},
			CreatedBy: CreatedBy{App: "AudioSmith", Version: "0.2.0"},
		},
		Preview: wav(2400),
	}
}

func cloneBundle() *Bundle {
	b := &Bundle{
		Manifest: Manifest{
			ID: "01JCLONE", Name: "My voice", Kind: KindClone, Language: "zh",
			Licence:   Licence{SPDX: "CC-BY-4.0", Attribution: "Me"},
			Consent:   &Consent{Attested: true, Method: "recording", Statement: "I confirm this is my own voice.", AttestedBy: "me", AttestedAt: time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)},
			CreatedBy: CreatedBy{App: "AudioSmith"},
			Files:     Files{Reference: &FileRef{Text: "你好，这是我的声音。"}},
		},
		Preview:      wav(1000),
		Reference:    append([]byte("fLaC"), make([]byte, 500)...),
		ReferenceExt: "flac",
		Portrait:     png,
		PortraitExt:  "png",
		ConsentAudio: wav(800),
	}
	return b
}

func roundTrip(t *testing.T, b *Bundle) *Bundle {
	t.Helper()
	var buf bytes.Buffer
	if err := Write(&buf, b, DefaultLimits); err != nil {
		t.Fatal(err)
	}
	got, err := Read(bytes.NewReader(buf.Bytes()), int64(buf.Len()), DefaultLimits)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func TestRoundTripDesign(t *testing.T) {
	b := designBundle()
	got := roundTrip(t, b)
	m := got.Manifest
	if m.Format != Format || m.SchemaVersion != SchemaVersion || m.CreatedAt.IsZero() || m.Licence.SPDX != DefaultLicence {
		t.Errorf("defaults: %+v", m)
	}
	if m.Recipe == nil || m.Recipe.Description != "a warm British narrator" || m.Recipe.Seed != 77 || m.Recipe.Attrs.Accent != "british" {
		t.Errorf("recipe: %+v", m.Recipe)
	}
	if m.Engine.Params["steps"].(float64) != 32 || *m.Engine.Seed != 77 {
		t.Errorf("engine: %+v", m.Engine)
	}
	if !bytes.Equal(got.Preview, b.Preview) || got.Reference != nil || m.Files.Preview.SHA256 == "" {
		t.Error("members")
	}
	if got.ConsentComplete() {
		t.Error("no consent but complete")
	}
}

func TestRoundTripClone(t *testing.T) {
	b := cloneBundle()
	got := roundTrip(t, b)
	if !bytes.Equal(got.Reference, b.Reference) || got.ReferenceExt != "flac" || !bytes.Equal(got.Portrait, png) || got.PortraitExt != "png" || !bytes.Equal(got.ConsentAudio, b.ConsentAudio) {
		t.Error("members differ")
	}
	if got.Manifest.Files.Reference.Text != "你好，这是我的声音。" || got.Manifest.Files.Reference.Path != "reference.flac" {
		t.Errorf("reference ref %+v", got.Manifest.Files.Reference)
	}
	if !got.ConsentComplete() || got.Manifest.Licence.Attribution != "Me" {
		t.Error("consent/licence")
	}
	// ReadManifest only
	var buf bytes.Buffer
	_ = Write(&buf, cloneBundle(), DefaultLimits)
	m, err := ReadManifest(bytes.NewReader(buf.Bytes()), int64(buf.Len()), DefaultLimits)
	if err != nil || m.Name != "My voice" || m.Files.Portrait == nil {
		t.Errorf("manifest: %+v %v", m, err)
	}
	// manifest.json is the first member
	zr, _ := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if zr.File[0].Name != ManifestName {
		t.Errorf("first member %s", zr.File[0].Name)
	}
}

func TestWriteFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "voice"+Ext)
	if err := WriteFile(p, cloneBundle(), DefaultLimits); err != nil {
		t.Fatal(err)
	}
	got, err := ReadFile(p, DefaultLimits)
	if err != nil || got.Manifest.ID != "01JCLONE" {
		t.Fatal(err)
	}
	small := DefaultLimits
	small.MaxFile = 100
	if _, err := ReadFile(p, small); !errors.Is(err, ErrTooLarge) {
		t.Errorf("limit: %v", err)
	}
}

func TestWriteRejects(t *testing.T) {
	cases := map[string]func(b *Bundle){
		"no preview":      func(b *Bundle) { b.Preview = nil },
		"preview not wav": func(b *Bundle) { b.Preview = []byte("ID3xxxxxxxxxxxx") },
		"no recipe":       func(b *Bundle) { b.Manifest.Recipe = nil },
		"bad recipe":      func(b *Bundle) { b.Manifest.Recipe.Attrs.Dialect = "sichuan" },
		"kind":            func(b *Bundle) { b.Manifest.Kind = "robot" },
		"id":              func(b *Bundle) { b.Manifest.ID = "../x" },
		"name":            func(b *Bundle) { b.Manifest.Name = "  " },
		"licence":         func(b *Bundle) { b.Manifest.Licence.SPDX = "do what you want!" },
		"ref ext":         func(b *Bundle) { b.Reference, b.ReferenceExt = wav(10), "mp3" },
		"portrait":        func(b *Bundle) { b.Portrait, b.PortraitExt = []byte("GIF89a"), "gif" },
		"consent not wav": func(b *Bundle) { b.ConsentAudio = []byte("nope") },
		"too many tags":   func(b *Bundle) { b.Manifest.Tags = make([]string, MaxTags+1) },
		"seed":            func(b *Bundle) { s := int64(-3); b.Manifest.Engine.Seed = &s },
		"no app":          func(b *Bundle) { b.Manifest.CreatedBy.App = "" },
		"newer schema":    func(b *Bundle) { b.Manifest.SchemaVersion = SchemaVersion + 1 },
	}
	for name, mutate := range cases {
		b := designBundle()
		mutate(b)
		if err := Write(&bytes.Buffer{}, b, DefaultLimits); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	lim := DefaultLimits
	lim.MaxPreview = 100
	if err := Write(&bytes.Buffer{}, designBundle(), lim); !errors.Is(err, ErrTooLarge) {
		t.Errorf("preview limit: %v", err)
	}
}

// rawZip builds a ZIP from name/data pairs.
func rawZip(t *testing.T, members ...[2]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, m := range members {
		f, err := zw.CreateHeader(&zip.FileHeader{Name: m[0], Method: zip.Deflate})
		if err != nil {
			t.Fatal(err)
		}
		f.Write([]byte(m[1]))
	}
	zw.Close()
	return buf.Bytes()
}

func members(t *testing.T, b *Bundle) [][2]string {
	var buf bytes.Buffer
	if err := Write(&buf, b, DefaultLimits); err != nil {
		t.Fatal(err)
	}
	zr, _ := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	var out [][2]string
	for _, f := range zr.File {
		rc, _ := f.Open()
		var d bytes.Buffer
		d.ReadFrom(rc)
		out = append(out, [2]string{f.Name, d.String()})
	}
	return out
}

func TestReadRejects(t *testing.T) {
	read := func(data []byte, lim Limits) error {
		_, err := Read(bytes.NewReader(data), int64(len(data)), lim)
		return err
	}
	if err := read([]byte("not a zip"), DefaultLimits); !errors.Is(err, ErrNotPersona) {
		t.Errorf("not a zip: %v", err)
	}
	if err := read(rawZip(t, [2]string{"hello.txt", "x"}), DefaultLimits); !errors.Is(err, ErrNotPersona) {
		t.Errorf("no manifest: %v", err)
	}
	good := members(t, cloneBundle())

	// a tampered member fails its checksum
	bad := slices.Clone(good)
	for i := range bad {
		if bad[i][0] == PreviewName {
			p := []byte(bad[i][1])
			p[len(p)-1] ^= 1
			bad[i][1] = string(p)
		}
	}
	if err := read(rawZip(t, bad...), DefaultLimits); !errors.Is(err, ErrCorrupt) {
		t.Errorf("tampered: %v", err)
	}
	// newer schema
	var m map[string]any
	json.Unmarshal([]byte(good[0][1]), &m)
	m["schemaVersion"] = SchemaVersion + 1
	nm, _ := json.Marshal(m)
	newer := slices.Clone(good)
	newer[0][1] = string(nm)
	if err := read(rawZip(t, newer...), DefaultLimits); !errors.Is(err, ErrUnsupported) {
		t.Errorf("newer: %v", err)
	}
	// unknown fields and members are fine
	m["schemaVersion"] = SchemaVersion
	m["futureField"] = map[string]any{"x": 1}
	nm, _ = json.Marshal(m)
	extra := append(slices.Clone(good), [2]string{"extras/notes.txt", "hi"})
	extra[0][1] = string(nm)
	if err := read(rawZip(t, extra...), DefaultLimits); err != nil {
		t.Errorf("forward compatible: %v", err)
	}
	// path traversal, duplicates, too many entries
	if err := read(rawZip(t, append(slices.Clone(good), [2]string{"../evil", "x"})...), DefaultLimits); !errors.Is(err, ErrCorrupt) {
		t.Errorf("traversal: %v", err)
	}
	if err := read(rawZip(t, append(slices.Clone(good), good[1])...), DefaultLimits); !errors.Is(err, ErrCorrupt) {
		t.Errorf("duplicate: %v", err)
	}
	lim := DefaultLimits
	lim.MaxEntries = 2
	if err := read(rawZip(t, good...), lim); !errors.Is(err, ErrTooLarge) {
		t.Errorf("entries: %v", err)
	}
	// a ZIP bomb can't expand past MaxFile
	bomb := designBundle()
	bomb.Preview = wav(3 << 20) // 6 MB of zeros, compresses to a few KB
	var buf bytes.Buffer
	if err := Write(&buf, bomb, DefaultLimits); err != nil {
		t.Fatal(err)
	}
	lim = DefaultLimits
	lim.MaxFile = 1 << 20
	if int64(buf.Len()) > lim.MaxFile {
		t.Fatalf("bomb compressed to %d", buf.Len())
	}
	if err := read(buf.Bytes(), lim); !errors.Is(err, ErrTooLarge) {
		t.Errorf("bomb: %v", err)
	}
}

func TestValidSPDX(t *testing.T) {
	for s, want := range map[string]bool{
		"CC-BY-4.0": true, "MIT OR Apache-2.0": true, "LicenseRef-AudioSmith-Personal": true, "GPL-2.0+": true,
		"(MIT AND CC0-1.0)": true, "": false, "free for all!": false, "a/b": false,
	} {
		if ValidSPDX(s) != want {
			t.Errorf("ValidSPDX(%q) = %v", s, !want)
		}
	}
	for _, l := range Licences {
		if !ValidSPDX(l) {
			t.Errorf("offered licence %q invalid", l)
		}
	}
}

func TestReadOVSDesign(t *testing.T) {
	manifest := `{"format":"ovsvoice","schema_version":2,
	  "persona":{"id":"a1b2c3d4","name":"Narrator Nell","language":"English","seed":1234,"kind":"design",
	    "vd_states":{"Gender":"female","Age":"elderly","Style":"whisper","EnglishAccent":"british accent","Pitch":"Auto"},
	    "instruct":"female, elderly, whisper, british accent"},
	  "license":{"spdx":"CC-BY-4.0"},"tags":["narration"],"preview":{"sample_rate":24000}}`
	data := rawZip(t, [2]string{"manifest.json", manifest}, [2]string{"metadata.json", `{"name":"Narrator Nell"}`}, [2]string{"preview.wav", string(wav(500))})
	b, warn, err := ReadOVS(bytes.NewReader(data), int64(len(data)), DefaultLimits)
	if err != nil {
		t.Fatal(err, warn)
	}
	m := b.Manifest
	want := vd.Attributes{Gender: "female", Age: "elderly", Styles: []string{"whisper"}, Accent: "british"}
	if m.Kind != KindDesign || m.Recipe == nil || m.Recipe.Seed != 1234 || !equalAttrs(m.Recipe.Attrs, want) {
		t.Errorf("design: %+v", m.Recipe)
	}
	if m.Name != "Narrator Nell" || m.Language != "en" || m.Licence.SPDX != "CC-BY-4.0" || !slices.Equal(m.Tags, []string{"narration"}) || m.ID != "ovs-a1b2c3d4" {
		t.Errorf("identity: %+v", m)
	}
	if len(warn) != 0 {
		t.Errorf("warnings %v", warn)
	}
	// and it re-exports as .asvoice
	roundTrip(t, b)
}

func equalAttrs(a, b vd.Attributes) bool {
	ja, _ := json.Marshal(a.Canonical())
	jb, _ := json.Marshal(b.Canonical())
	return string(ja) == string(jb)
}

func TestReadOVSClone(t *testing.T) {
	manifest := `{"name":"Grandpa","ref_text":"Hello there.","language":"Auto","license":{"spdx":"Do whatever!"},"kind":"clone"}`
	consent := `{"method":"recorded","statement":"I confirm this is my own voice.","verified":true,"timestamp":"2026-09-01T10:00:00Z"}`
	data := rawZip(t,
		[2]string{"manifest.json", manifest},
		[2]string{"preview.wav", string(wav(100))},
		[2]string{"ref_audio.wav", string(wav(300))},
		[2]string{"locked_audio.wav", string(wav(200))},
		[2]string{"consent.json", consent},
		[2]string{"consent_audio.wav", string(wav(150))},
	)
	b, warn, err := ReadOVS(bytes.NewReader(data), int64(len(data)), DefaultLimits)
	if err != nil {
		t.Fatal(err)
	}
	m := b.Manifest
	if m.Kind != KindClone || b.ReferenceExt != "wav" || len(b.Reference) != len(wav(300)) || !m.Engine.Locked {
		t.Errorf("clone: %+v", m)
	}
	if m.Files.Reference == nil || m.Files.Reference.Text != "Hello there." || m.Language != "" {
		t.Errorf("transcript/lang: %+v", m.Files.Reference)
	}
	if m.Licence.SPDX != "LicenseRef-VoiceStudio-Personal" || len(warn) != 1 {
		t.Errorf("licence %q warn %v", m.Licence.SPDX, warn)
	}
	if !b.ConsentComplete() || !m.Consent.AttestedAt.Equal(time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)) {
		t.Errorf("consent %+v", m.Consent)
	}
	roundTrip(t, b)

	// incomplete consent → unverified; preview-only bundle
	data = rawZip(t, [2]string{"manifest.json", `{"name":"X"}`}, [2]string{"preview.wav", string(wav(10))}, [2]string{"consent.json", `{"statement":"s"}`})
	b, warn, err = ReadOVS(bytes.NewReader(data), int64(len(data)), DefaultLimits)
	if err != nil || b.Manifest.Consent != nil || b.Manifest.Kind != KindClone || len(warn) != 2 {
		t.Errorf("preview-only: %+v %v %v", b, warn, err)
	}
	// no preview
	data = rawZip(t, [2]string{"manifest.json", `{"name":"X"}`})
	if _, _, err := ReadOVS(bytes.NewReader(data), int64(len(data)), DefaultLimits); err == nil {
		t.Error("no preview accepted")
	}
	if _, _, err := ReadOVS(bytes.NewReader([]byte("zzz")), 3, DefaultLimits); !errors.Is(err, ErrNotPersona) {
		t.Errorf("garbage: %v", err)
	}
}

func FuzzRead(f *testing.F) {
	var buf bytes.Buffer
	_ = Write(&buf, cloneBundle(), DefaultLimits)
	f.Add(buf.Bytes())
	f.Add([]byte("PK\x03\x04"))
	f.Fuzz(func(t *testing.T, data []byte) {
		lim := DefaultLimits
		lim.MaxFile = 1 << 20
		b, err := Read(bytes.NewReader(data), int64(len(data)), lim)
		if err == nil && (b.Manifest.Validate() != nil || !isWAV(b.Preview)) {
			t.Fatal("accepted an invalid bundle")
		}
		_, _, _ = ReadOVS(bytes.NewReader(data), int64(len(data)), lim)
	})
}
