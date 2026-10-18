package services

// The voice library: saved voices with their files (reference, sample,
// portrait, consent), locking and consent; voice packages (import and
// export) are in voices_package.go.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/hehehai/AudioSmith/internal/core"
	"github.com/hehehai/AudioSmith/internal/store"
	vd "github.com/hehehai/AudioSmith/internal/voicedesign"
)

// Voices implements core.VoiceService: profiles in the store, their files
// in VoiceDir/<id>/, voice packages (.asvoice, VoiceStudio .ovsvoice).
//
// File names inside a voice's directory:
//
//	reference.<ext>  the clone reference (as given: wav, flac, mp3, m4a…)
//	original.<ext>   the reference before it was cleaned (Undo)
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
	v.RefAudio, v.RefSeconds, v.RefOriginal, v.RefCheck = old.RefAudio, old.RefSeconds, old.RefOriginal, old.RefCheck
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
	in := v
	var old core.Voice
	if created {
		v.ID, v.CreatedAt = core.NewID(now), now
		keepFiles(&v, core.Voice{}) // a new profile has no files until f adds them
	} else {
		var err error
		if old, err = s.store.Get(ctx, v.ID); err != nil {
			s.mu.Unlock()
			return v, err
		}
		v.CreatedAt = old.CreatedAt
		keepFiles(&v, old)
	}
	// What describes a new file comes with it.
	if f.Reference != "" {
		v.RefSeconds, v.RefCheck = in.RefSeconds, in.RefCheck
	}
	if f.Sample != "" {
		v.SampleText = in.SampleText
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
		if f.RemoveReference && f.Reference == "" {
			obsolete = append(obsolete, v.RefAudio, v.RefOriginal)
			v.RefAudio, v.RefOriginal, v.RefSeconds, v.RefCheck = "", "", 0, nil
			if v.Kind == core.VoiceDesigned {
				v.RefText = ""
			}
		}
		if f.Reference != "" {
			name, err := put(f.Reference, "reference")
			if err != nil {
				return err
			}
			obsolete = append(obsolete, v.RefAudio, v.RefOriginal)
			v.RefAudio, v.RefOriginal = name, ""
			if f.Original != "" {
				if v.RefOriginal, err = put(f.Original, "original"); err != nil {
					return err
				}
			}
			// A new clip may be another speaker: what vouched for the old
			// one goes (a designed voice's is rendered from itself).
			if v.Locked && v.Kind != core.VoiceDesigned {
				obsolete = append(obsolete, lockSample(&v)...)
			}
			if v.Consent.State != core.ConsentNone && v.Kind != core.VoiceDesigned {
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
	out := []string{v.RefAudio, v.RefOriginal, v.Sample, v.Portrait, v.Consent.Recording}
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

// CleanReference makes the cleaned clip at path the voice's reference;
// the current one becomes RefOriginal (its cached prompts go).
func (s *Voices) CleanReference(ctx context.Context, id, path string, seconds float64, q *core.RefQuality) (core.Voice, error) {
	return s.update(ctx, id, func(v *core.Voice) ([]string, error) {
		if v.RefAudio == "" || v.RefOriginal != "" {
			return nil, fmt.Errorf("voice %s: no reference to clean: %w", id, core.ErrInvalid)
		}
		name := "cleaned.wav"
		if err := copyInto(ctx, path, s.FilePath(*v, name)); err != nil {
			return nil, err
		}
		var obsolete []string
		for _, p := range v.Prompts {
			obsolete = append(obsolete, p)
		}
		v.RefOriginal, v.RefAudio, v.Prompts = v.RefAudio, name, nil
		v.RefSeconds, v.RefCheck = seconds, q
		return obsolete, nil
	})
}

// RevertReference undoes the automatic cleaning of a voice's reference:
// the original becomes the reference again (its cached prompts go).
func (s *Voices) RevertReference(ctx context.Context, id string) (core.Voice, error) {
	return s.update(ctx, id, func(v *core.Voice) ([]string, error) {
		if v.RefOriginal == "" {
			return nil, fmt.Errorf("voice %s: no original reference: %w", id, core.ErrInvalid)
		}
		obsolete := []string{v.RefAudio}
		for _, p := range v.Prompts {
			obsolete = append(obsolete, p)
		}
		v.RefAudio, v.RefOriginal, v.Prompts = v.RefOriginal, "", nil
		if v.RefCheck != nil {
			q := *v.RefCheck
			v.RefSeconds = firstPositive(q.OriginalSeconds, v.RefSeconds)
			q.Cleaned, q.OriginalSeconds = nil, 0
			v.RefCheck = &q
		}
		return obsolete, nil
	})
}

func firstPositive(xs ...float64) float64 {
	for _, x := range xs {
		if x > 0 {
			return x
		}
	}
	return 0
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
