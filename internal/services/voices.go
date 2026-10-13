package services

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/hehehai/AudioSmith/internal/core"
	"github.com/hehehai/AudioSmith/internal/gallery/persona"
	"github.com/hehehai/AudioSmith/internal/i18n"
	"github.com/hehehai/AudioSmith/internal/store"
	vd "github.com/hehehai/AudioSmith/internal/voicedesign"
)

// Voices implements core.VoiceService: profiles in the store, their files
// in VoiceDir/<id>/, voice packages (.asvoice, VoiceStudio .ovsvoice).
//
// File names inside a voice's directory:
//
//	reference.<ext>  the clone reference (as given: wav, flac, mp3, m4a…)
//	sample.<ext>     a rendered sample (designed voices, imports' preview)
//	locked.wav       the locked take's audio (Lock)
//	consent.<ext>    the consent recording
//	portrait.png     the portrait (portrait.<ext> for imports)
//	prompt-<h>.bin   cached voice prompts by model (tts.encodeVoice)
//
// Files are written under a temporary name and renamed into place before
// the profile that names them is saved; replaced files are removed after.
type Voices struct {
	store store.Voices
	takes store.Takes
	bus   *core.Bus
	now   func() time.Time
	mu    sync.Mutex // serializes changes (read-modify-write, files)
}

var _ core.VoiceService = (*Voices)(nil)

func newVoices(cfg Config) *Voices {
	return &Voices{store: cfg.Store.Voices(), takes: cfg.Store.Takes(), bus: cfg.Bus, now: cfg.Now}
}

// Voice file names.
const (
	lockedName   = "locked.wav"
	portraitName = "portrait.png"
)

func (s *Voices) List(ctx context.Context, q core.VoiceQuery) ([]core.Voice, error) {
	return s.store.List(ctx, q)
}

func (s *Voices) Get(ctx context.Context, id string) (core.Voice, error) {
	return s.store.Get(ctx, id)
}

func (s *Voices) dir(id string) string { return filepath.Join(s.store.VoiceDir(), id) }

// FilePath is the absolute path of a voice's file.
func (s *Voices) FilePath(v core.Voice, name string) string {
	if name == "" || v.ID == "" || filepath.Base(name) != name {
		return ""
	}
	return filepath.Join(s.dir(v.ID), name)
}

func (s *Voices) Usage(ctx context.Context, id string) (core.VoiceUsage, error) {
	return s.store.Usage(ctx, id)
}

// Save creates a profile (empty ID) or updates one, keeping its creation
// time and its files; updating an unknown id fails with ErrNotFound.
func (s *Voices) Save(ctx context.Context, v core.Voice) (core.Voice, error) {
	return s.SaveFiles(ctx, v, core.VoiceFiles{})
}

func validVoice(v *core.Voice) error {
	v.Name = strings.TrimSpace(v.Name)
	if v.Name == "" {
		return fmt.Errorf("voice: no name: %w", core.ErrInvalid)
	}
	if !slices.Contains([]core.VoiceKind{core.VoiceBuiltin, core.VoiceCloned, core.VoiceDesigned}, v.Kind) {
		return fmt.Errorf("voice: kind %q: %w", v.Kind, core.ErrInvalid)
	}
	if v.Seed < 0 || v.Seed > vd.MaxSeed {
		return fmt.Errorf("voice: seed %d: %w", v.Seed, core.ErrInvalid)
	}
	if len(v.DesignAttrs) > 0 {
		var a vd.Attributes
		if err := json.Unmarshal(v.DesignAttrs, &a); err != nil {
			return fmt.Errorf("voice: design attributes: %w: %w", core.ErrInvalid, err)
		}
	}
	v.Style, v.RefText, v.Design = strings.TrimSpace(v.Style), strings.TrimSpace(v.RefText), strings.TrimSpace(v.Design)
	return nil
}

// keepFiles copies the fields that name files (and the lock and consent
// that go with them) from the stored profile old into v.
func keepFiles(v *core.Voice, old core.Voice) {
	v.RefAudio, v.RefSeconds = old.RefAudio, old.RefSeconds
	v.Sample, v.SampleText = old.Sample, old.SampleText
	v.Portrait, v.Consent, v.Prompts = old.Portrait, old.Consent, maps.Clone(old.Prompts)
	v.Locked, v.LockTake = old.Locked, old.LockTake
	if old.Locked {
		v.Seed = old.Seed
	}
}

func (s *Voices) SaveFiles(ctx context.Context, v core.Voice, f core.VoiceFiles) (core.Voice, error) {
	if err := validVoice(&v); err != nil {
		return v, err
	}
	s.mu.Lock()
	now := s.now()
	created := v.ID == ""
	var old core.Voice
	if created {
		v.ID, v.CreatedAt = core.NewID(now), now
		refSeconds := v.RefSeconds
		keepFiles(&v, core.Voice{}) // a new profile has no files until f adds them
		if f.Reference != "" {
			v.RefSeconds = refSeconds
		}
	} else {
		var err error
		if old, err = s.store.Get(ctx, v.ID); err != nil {
			s.mu.Unlock()
			return v, err
		}
		v.CreatedAt = old.CreatedAt
		refSeconds := v.RefSeconds
		keepFiles(&v, old)
		if f.Reference != "" {
			v.RefSeconds = refSeconds
		}
	}
	v.UpdatedAt = now
	var obsolete []string // files to remove once the profile is saved
	var added []string    // files to remove if it isn't
	put := func(src, base string) (string, error) {
		name := base + strings.ToLower(filepath.Ext(src))
		if err := copyInto(ctx, src, s.FilePath(v, name)); err != nil {
			return "", err
		}
		added = append(added, name)
		return name, nil
	}
	err := func() error {
		if f.Reference != "" {
			name, err := put(f.Reference, "reference")
			if err != nil {
				return err
			}
			obsolete = append(obsolete, v.RefAudio)
			v.RefAudio = name
			// A new clip may be another speaker: what vouched for the old
			// one goes.
			if v.Locked {
				obsolete = append(obsolete, lockSample(&v)...)
			}
			if v.Consent.State != core.ConsentNone {
				obsolete = append(obsolete, v.Consent.Recording)
				v.Consent = core.VoiceConsent{}
			}
			for _, p := range v.Prompts {
				obsolete = append(obsolete, p)
			}
			v.Prompts = nil
		}
		if f.Sample != "" {
			name, err := put(f.Sample, "sample")
			if err != nil {
				return err
			}
			obsolete = append(obsolete, v.Sample)
			v.Sample = name
		}
		if f.RemovePortrait || f.Portrait != nil {
			obsolete = append(obsolete, v.Portrait)
			v.Portrait = ""
		}
		if f.Portrait != nil {
			if err := os.MkdirAll(s.dir(v.ID), 0o755); err != nil {
				return err
			}
			if err := writeFileAtomic(s.FilePath(v, portraitName), f.Portrait); err != nil {
				return err
			}
			added = append(added, portraitName)
			v.Portrait = portraitName
		}
		if f.Prompt != "" && f.PromptModel != "" {
			h := sha256.Sum256([]byte(f.PromptModel))
			name := "prompt-" + hex.EncodeToString(h[:6]) + ".bin"
			if err := copyInto(ctx, f.Prompt, s.FilePath(v, name)); err != nil {
				return err
			}
			added = append(added, name)
			if v.Prompts == nil {
				v.Prompts = map[string]string{}
			}
			v.Prompts[f.PromptModel] = name
		}
		return s.store.Put(ctx, v)
	}()
	if err != nil {
		for _, n := range added {
			if !slices.Contains(fileNames(old), n) { // never remove a file the stored profile names
				os.Remove(s.FilePath(v, n))
			}
		}
		if created {
			os.RemoveAll(s.dir(v.ID))
		}
		s.mu.Unlock()
		return v, err
	}
	s.removeUnused(v, obsolete)
	s.mu.Unlock()
	s.bus.Publish(core.VoiceChanged{Voice: v})
	return v, nil
}

// fileNames lists the files a profile names.
func fileNames(v core.Voice) []string {
	out := []string{v.RefAudio, v.Sample, v.Portrait, v.Consent.Recording}
	for _, p := range v.Prompts {
		out = append(out, p)
	}
	return slices.DeleteFunc(out, func(s string) bool { return s == "" })
}

// removeUnused deletes the files of names that v no longer names.
func (s *Voices) removeUnused(v core.Voice, names []string) {
	keep := fileNames(v)
	for _, n := range names {
		if n != "" && !slices.Contains(keep, n) {
			os.Remove(s.FilePath(v, n))
		}
	}
}

// lockSample clears a voice's lock and returns the file it made obsolete.
func lockSample(v *core.Voice) []string {
	var out []string
	if v.Sample == lockedName {
		out = append(out, v.Sample)
		v.Sample, v.SampleText = "", ""
	}
	v.Locked, v.LockTake = false, ""
	return out
}

func (s *Voices) Delete(ctx context.Context, id string) error {
	s.mu.Lock()
	v, err := s.store.Get(ctx, id)
	if err == nil {
		err = s.store.Delete(ctx, id)
	}
	s.mu.Unlock()
	if err != nil {
		return err
	}
	s.bus.Publish(core.VoiceChanged{Voice: v, Deleted: true})
	return nil
}

// update applies fn to the stored profile and saves it; fn returns the
// files it made obsolete.
func (s *Voices) update(ctx context.Context, id string, fn func(v *core.Voice) ([]string, error)) (core.Voice, error) {
	s.mu.Lock()
	v, err := s.store.Get(ctx, id)
	var obsolete []string
	if err == nil {
		obsolete, err = fn(&v)
	}
	if err == nil {
		v.UpdatedAt = s.now()
		err = s.store.Put(ctx, v)
	}
	if err == nil {
		s.removeUnused(v, obsolete)
	}
	s.mu.Unlock()
	if err != nil {
		return v, err
	}
	s.bus.Publish(core.VoiceChanged{Voice: v})
	return v, nil
}

// Lock makes one of the voice's takes its identity: the take's audio is
// copied in as the voice's sample (so deleting the take keeps the lock)
// and its seed becomes the voice's.
func (s *Voices) Lock(ctx context.Context, id, takeID string) (core.Voice, error) {
	t, err := s.takes.Get(ctx, takeID)
	if err != nil {
		return core.Voice{}, err
	}
	if t.VoiceID != id {
		return core.Voice{}, fmt.Errorf("lock voice %s: take %s was not made with it: %w", id, takeID, core.ErrInvalid)
	}
	return s.update(ctx, id, func(v *core.Voice) ([]string, error) {
		if err := copyInto(ctx, s.takes.AudioPath(t), s.FilePath(*v, lockedName+".new")); err != nil {
			return nil, err
		}
		if err := os.Rename(s.FilePath(*v, lockedName+".new"), s.FilePath(*v, lockedName)); err != nil {
			return nil, err
		}
		obsolete := []string{v.Sample}
		v.Locked, v.LockTake, v.Seed = true, t.ID, t.Seed
		v.Sample, v.SampleText, v.CreatedWith = lockedName, t.Text, t.Model
		return obsolete, nil
	})
}

func (s *Voices) Unlock(ctx context.Context, id string) (core.Voice, error) {
	return s.update(ctx, id, func(v *core.Voice) ([]string, error) {
		return lockSample(v), nil
	})
}

func (s *Voices) SetConsent(ctx context.Context, id string, c core.ConsentRecording) (core.Voice, error) {
	if c.Audio == "" || strings.TrimSpace(c.Statement) == "" {
		return core.Voice{}, fmt.Errorf("consent: recording and statement are required: %w", core.ErrInvalid)
	}
	return s.update(ctx, id, func(v *core.Voice) ([]string, error) {
		name := "consent" + strings.ToLower(filepath.Ext(c.Audio))
		if err := copyInto(ctx, c.Audio, s.FilePath(*v, name)); err != nil {
			return nil, err
		}
		obsolete := []string{v.Consent.Recording}
		v.Consent = core.VoiceConsent{State: core.ConsentVerified, Recording: name, Statement: strings.TrimSpace(c.Statement),
			Locale: c.Locale, At: s.now()}
		return obsolete, nil
	})
}

func (s *Voices) RevokeConsent(ctx context.Context, id string) (core.Voice, error) {
	return s.update(ctx, id, func(v *core.Voice) ([]string, error) {
		obsolete := []string{v.Consent.Recording}
		v.Consent = core.VoiceConsent{}
		return obsolete, nil
	})
}

// --- voice packages -----------------------------------------------------

// Import reads a .asvoice or .ovsvoice file into a new profile: the
// reference, preview (as the sample), portrait and consent recording are
// copied into its directory. A consent record in the file is the
// exporter's claim: the voice is ConsentIncluded until the user records
// the statement here.
func (s *Voices) Import(ctx context.Context, path string) (core.VoiceImport, error) {
	var out core.VoiceImport
	var b *persona.Bundle
	var err error
	switch strings.ToLower(filepath.Ext(path)) {
	case persona.Ext:
		out.Format = "asvoice"
		b, err = persona.ReadFile(path, persona.DefaultLimits)
	case persona.OVSExt:
		out.Format = "ovsvoice"
		var data []byte
		if data, err = readLimited(path, persona.DefaultLimits.MaxFile); err == nil {
			b, out.Warnings, err = persona.ReadOVS(bytes.NewReader(data), int64(len(data)), persona.DefaultLimits)
		}
	default:
		return out, fmt.Errorf("import %s: not a voice file: %w", filepath.Base(path), core.ErrInvalid)
	}
	if errors.Is(err, os.ErrNotExist) {
		return out, err
	}
	if err != nil {
		return out, fmt.Errorf("import %s: %w", filepath.Base(path), errors.Join(core.ErrInvalid, err))
	}
	m := b.Manifest
	v := core.Voice{
		Name: m.Name, Language: m.Language, Tags: slices.Clone(m.Tags),
		Style: strings.TrimSpace(m.Engine.Voice), CreatedWith: m.Engine.Repo,
		Origin: out.Format + ":" + m.ID,
	}
	switch m.Kind {
	case persona.KindDesign:
		v.Kind, v.Style = core.VoiceDesigned, ""
		if m.Recipe != nil {
			v.Design, v.Seed = m.Recipe.Description, m.Recipe.Seed
			v.DesignAttrs, _ = json.Marshal(m.Recipe.Attrs)
		}
	default:
		v.Kind = core.VoiceCloned
		if b.Reference == nil {
			out.Warnings = append(out.Warnings, i18n.T("The file has no reference clip: the voice is cloned from its preview."))
		}
	}
	if m.Engine.Seed != nil && v.Seed == 0 {
		v.Seed = *m.Engine.Seed
	}
	if v.Seed < 0 || v.Seed > vd.MaxSeed {
		v.Seed = vd.FoldSeed(v.Seed)
	}
	if m.Files.Reference != nil {
		v.RefText = m.Files.Reference.Text
	}
	if err := validVoice(&v); err != nil {
		return out, err
	}

	s.mu.Lock()
	now := s.now()
	v.ID, v.CreatedAt, v.UpdatedAt = core.NewID(now), now, now
	err = func() error {
		if err := os.MkdirAll(s.dir(v.ID), 0o755); err != nil {
			return err
		}
		write := func(name string, data []byte) error {
			return writeFileAtomic(s.FilePath(v, name), data)
		}
		if b.Reference != nil {
			v.RefAudio = "reference." + b.ReferenceExt
			if err := write(v.RefAudio, b.Reference); err != nil {
				return err
			}
		} else if v.Kind == core.VoiceCloned {
			v.RefAudio, v.RefText = "reference.wav", m.Files.Preview.Text
			if err := write(v.RefAudio, b.Preview); err != nil {
				return err
			}
		}
		v.Sample, v.SampleText = "sample.wav", m.Files.Preview.Text
		if err := write(v.Sample, b.Preview); err != nil {
			return err
		}
		if m.Engine.Locked {
			v.Locked = true
		}
		if b.Portrait != nil {
			v.Portrait = "portrait." + b.PortraitExt
			if err := write(v.Portrait, b.Portrait); err != nil {
				return err
			}
		}
		if b.ConsentComplete() {
			v.Consent = core.VoiceConsent{State: core.ConsentIncluded, Recording: "consent.wav",
				Statement: m.Consent.Statement, At: m.Consent.AttestedAt}
			if err := write(v.Consent.Recording, b.ConsentAudio); err != nil {
				return err
			}
		}
		return s.store.Put(ctx, v)
	}()
	if err != nil {
		os.RemoveAll(s.dir(v.ID))
		s.mu.Unlock()
		return out, err
	}
	s.mu.Unlock()
	out.Voice = v
	s.bus.Publish(core.VoiceChanged{Voice: v})
	return out, nil
}

// Export writes the voice as a .asvoice file at dst (atomically).
func (s *Voices) Export(ctx context.Context, id, dst string, o core.VoiceExport) error {
	v, err := s.store.Get(ctx, id)
	if err != nil {
		return err
	}
	pid := v.ID
	if p, ok := strings.CutPrefix(v.Origin, "asvoice:"); ok && p != "" {
		pid = p // keep the persona's identity across re-exports
	}
	app := o.App
	if app == "" {
		app = "AudioSmith"
	}
	m := persona.Manifest{
		ID: pid, Name: v.Name, Language: v.Language, Tags: slices.Clone(v.Tags),
		Engine:    persona.Engine{Repo: firstNonEmpty(v.CreatedWith, v.Model), Voice: v.Style, Locked: v.Locked},
		Licence:   persona.Licence{SPDX: o.Licence},
		CreatedBy: persona.CreatedBy{App: app, Version: o.AppVersion},
	}
	if v.Seed != 0 || v.Locked || v.Kind == core.VoiceDesigned {
		seed := v.Seed
		m.Engine.Seed = &seed
	}
	switch v.Kind {
	case core.VoiceDesigned:
		m.Kind = persona.KindDesign
		r := vd.Recipe{Description: v.Design, Seed: v.Seed}
		if len(v.DesignAttrs) > 0 {
			json.Unmarshal(v.DesignAttrs, &r.Attrs)
		}
		r = r.Normalize()
		m.Recipe = &r
		m.Description = v.Design
	default:
		m.Kind = persona.KindClone
		m.Description = v.Style
	}
	b := &persona.Bundle{Manifest: m}

	// The preview: what the caller chose, else the voice's sample, its
	// newest take or its reference, whichever is a WAV small enough.
	var candidates []previewSource
	if o.Preview != "" {
		candidates = append(candidates, previewSource{o.Preview, ""})
	}
	if v.Sample != "" {
		candidates = append(candidates, previewSource{s.FilePath(v, v.Sample), v.SampleText})
	}
	if u, err := s.store.Usage(ctx, id); err == nil && u.LatestTake != "" {
		if t, err := s.takes.Get(ctx, u.LatestTake); err == nil {
			candidates = append(candidates, previewSource{s.takes.AudioPath(t), t.Text})
		}
	}
	if v.RefAudio != "" {
		candidates = append(candidates, previewSource{s.FilePath(v, v.RefAudio), v.RefText})
	}
	for _, c := range candidates {
		data, err := readLimited(c.path, persona.DefaultLimits.MaxPreview)
		if err == nil && isWAV(data) {
			b.Preview, b.Manifest.Files.Preview.Text = data, c.text
			break
		}
	}
	if b.Preview == nil {
		return fmt.Errorf("export %s: %w", v.Name, ErrNoPreview)
	}
	if o.IncludeReference && v.RefAudio != "" {
		ext := strings.TrimPrefix(strings.ToLower(filepath.Ext(v.RefAudio)), ".")
		data, err := readLimited(s.FilePath(v, v.RefAudio), persona.DefaultLimits.MaxReference)
		if err != nil {
			return fmt.Errorf("export %s: reference: %w", v.Name, err)
		}
		b.Reference, b.ReferenceExt = data, ext
		b.Manifest.Files.Reference = &persona.FileRef{Text: v.RefText}
	}
	if v.Portrait != "" {
		ext := strings.TrimPrefix(strings.ToLower(filepath.Ext(v.Portrait)), ".")
		if ext == "jpeg" {
			ext = "jpg"
		}
		if data, err := readLimited(s.FilePath(v, v.Portrait), persona.DefaultLimits.MaxPortrait); err == nil {
			b.Portrait, b.PortraitExt = data, ext
		}
	}
	if v.Verified() {
		if data, err := readLimited(s.FilePath(v, v.Consent.Recording), persona.DefaultLimits.MaxConsent); err == nil && isWAV(data) {
			b.ConsentAudio = data
			b.Manifest.Consent = &persona.Consent{Attested: true, Method: "recording", Statement: v.Consent.Statement, AttestedAt: v.Consent.At}
		}
	}
	return persona.WriteFile(dst, b, persona.DefaultLimits)
}

// ErrNoPreview: a voice has no WAV to ship as an export's preview (no
// sample, take or WAV reference): generate a take with it first.
var ErrNoPreview = errors.New("the voice has no audio to build a preview from")

type previewSource struct{ path, text string }

func isWAV(b []byte) bool {
	return len(b) >= 12 && (string(b[:4]) == "RIFF" || string(b[:4]) == "RF64") && string(b[8:12]) == "WAVE"
}

func firstNonEmpty(xs ...string) string {
	for _, x := range xs {
		if x != "" {
			return x
		}
	}
	return ""
}

// --- files --------------------------------------------------------------

// copyInto copies src to dst (creating its directory) through a temporary file.
func copyInto(ctx context.Context, src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	return copyFile(ctx, src, dst)
}

func writeFileAtomic(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*.part")
	if err != nil {
		return err
	}
	_, err = tmp.Write(data)
	if err == nil {
		err = tmp.Sync()
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Chmod(tmp.Name(), 0o644)
	}
	if err == nil {
		err = os.Rename(tmp.Name(), path)
	}
	if err != nil {
		os.Remove(tmp.Name())
	}
	return err
}

// readLimited reads a file of at most max bytes.
func readLimited(path string, max int64) ([]byte, error) {
	st, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if st.Size() > max {
		return nil, fmt.Errorf("%s: %w", filepath.Base(path), persona.ErrTooLarge)
	}
	return os.ReadFile(path)
}
