package persona

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"slices"
	"strings"
	"time"
)

func sum(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// Write validates the bundle, fills in Manifest.Files (and Format,
// SchemaVersion, CreatedAt when zero) and writes the ZIP to w.
func Write(w io.Writer, b *Bundle, lim Limits) error {
	m := &b.Manifest
	if m.Format == "" {
		m.Format = Format
	}
	if m.SchemaVersion == 0 {
		m.SchemaVersion = SchemaVersion
	}
	if m.CreatedAt.IsZero() {
		m.CreatedAt = time.Now().UTC().Truncate(time.Second)
	}
	if m.Licence.SPDX == "" {
		m.Licence.SPDX = DefaultLicence
	}
	if m.Recipe != nil {
		r := m.Recipe.Normalize()
		m.Recipe = &r
	}
	type member struct {
		name string
		data []byte
		max  int64
	}
	if !isWAV(b.Preview) {
		return invalid("the preview must be a WAV file")
	}
	members := []member{{PreviewName, b.Preview, lim.MaxPreview}}
	m.Files.Preview = FileRef{Path: PreviewName, Size: int64(len(b.Preview)), SHA256: sum(b.Preview), Text: m.Files.Preview.Text}
	refText := ""
	if m.Files.Reference != nil {
		refText = m.Files.Reference.Text
	}
	m.Files.Reference, m.Files.Portrait, m.Files.Consent = nil, nil, nil
	if len(b.Reference) > 0 {
		ext := strings.ToLower(strings.TrimPrefix(b.ReferenceExt, "."))
		if !slices.Contains(referenceExts, ext) || !sniffAudio(b.Reference, ext) {
			return invalid("reference audio is not a %s file", ext)
		}
		name := "reference." + ext
		members = append(members, member{name, b.Reference, lim.MaxReference})
		m.Files.Reference = &FileRef{Path: name, Size: int64(len(b.Reference)), SHA256: sum(b.Reference), Text: refText}
	}
	if len(b.Portrait) > 0 {
		ext := strings.ToLower(strings.TrimPrefix(b.PortraitExt, "."))
		if ext == "jpeg" {
			ext = "jpg"
		}
		if !slices.Contains(portraitExts, ext) || !sniffImage(b.Portrait, ext) {
			return invalid("portrait is not a %s image", ext)
		}
		name := "portrait." + ext
		members = append(members, member{name, b.Portrait, lim.MaxPortrait})
		m.Files.Portrait = &FileRef{Path: name, Size: int64(len(b.Portrait)), SHA256: sum(b.Portrait)}
	}
	if len(b.ConsentAudio) > 0 {
		if !isWAV(b.ConsentAudio) {
			return invalid("the consent recording must be a WAV file")
		}
		members = append(members, member{ConsentName, b.ConsentAudio, lim.MaxConsent})
		m.Files.Consent = &FileRef{Path: ConsentName, Size: int64(len(b.ConsentAudio)), SHA256: sum(b.ConsentAudio)}
	}
	if err := m.Validate(); err != nil {
		return err
	}
	var total int64
	for _, mb := range members {
		if mb.max > 0 && int64(len(mb.data)) > mb.max {
			return fmt.Errorf("%w: %s is %d bytes (limit %d)", ErrTooLarge, mb.name, len(mb.data), mb.max)
		}
		total += int64(len(mb.data))
	}
	man, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	if lim.MaxManifest > 0 && int64(len(man)) > lim.MaxManifest {
		return fmt.Errorf("%w: manifest is %d bytes", ErrTooLarge, len(man))
	}
	if lim.MaxFile > 0 && total+int64(len(man)) > lim.MaxFile {
		return fmt.Errorf("%w: %d bytes (limit %d)", ErrTooLarge, total+int64(len(man)), lim.MaxFile)
	}

	zw := zip.NewWriter(w)
	write := func(name string, data []byte, method uint16) error {
		f, err := zw.CreateHeader(&zip.FileHeader{Name: name, Method: method, Modified: m.CreatedAt})
		if err != nil {
			return err
		}
		_, err = f.Write(data)
		return err
	}
	if err := write(ManifestName, man, zip.Deflate); err != nil {
		return err
	}
	for _, mb := range members {
		method := zip.Store // audio and images are already dense
		if strings.HasSuffix(mb.name, ".wav") {
			method = zip.Deflate
		}
		if err := write(mb.name, mb.data, method); err != nil {
			return err
		}
	}
	return zw.Close()
}

// WriteFile writes the bundle to path atomically (temp file + rename).
func WriteFile(path string, b *Bundle, lim Limits) error {
	tmp, err := os.CreateTemp(dirOf(path), ".asvoice-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := Write(tmp, b, lim); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

func dirOf(p string) string {
	if i := strings.LastIndexAny(p, `/\`); i >= 0 {
		return p[:i+1]
	}
	return "."
}

// container is a ZIP opened within limits.
type container struct {
	zr    *zip.Reader
	files map[string]*zip.File
	lim   Limits
	used  int64
}

func openContainer(r io.ReaderAt, size int64, lim Limits) (*container, error) {
	if lim.MaxFile > 0 && size > lim.MaxFile {
		return nil, fmt.Errorf("%w: %d bytes (limit %d)", ErrTooLarge, size, lim.MaxFile)
	}
	zr, err := zip.NewReader(r, size)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrNotPersona, err)
	}
	if lim.MaxEntries > 0 && len(zr.File) > lim.MaxEntries {
		return nil, fmt.Errorf("%w: %d entries", ErrTooLarge, len(zr.File))
	}
	c := &container{zr: zr, files: map[string]*zip.File{}, lim: lim}
	for _, f := range zr.File {
		name := f.Name
		if name == "" || strings.Contains(name, `\`) || strings.HasPrefix(name, "/") || path.Clean(name) != name || strings.HasPrefix(name, "../") || name == ".." {
			if strings.HasSuffix(name, "/") { // directory entries are harmless
				continue
			}
			return nil, fmt.Errorf("%w: bad member name %q", ErrCorrupt, name)
		}
		if _, dup := c.files[name]; dup {
			return nil, fmt.Errorf("%w: duplicate member %q", ErrCorrupt, name)
		}
		c.files[name] = f
	}
	return c, nil
}

// read returns a member's bytes, at most max (0 = no member limit), and
// counts it against MaxFile so a ZIP bomb can't expand past it.
func (c *container) read(name string, max int64) ([]byte, error) {
	f, ok := c.files[name]
	if !ok {
		return nil, fmt.Errorf("%w: %s is missing", ErrCorrupt, name)
	}
	if max > 0 && int64(f.UncompressedSize64) > max {
		return nil, fmt.Errorf("%w: %s is %d bytes (limit %d)", ErrTooLarge, name, f.UncompressedSize64, max)
	}
	rc, err := f.Open()
	if err != nil {
		return nil, fmt.Errorf("%w: %s: %v", ErrCorrupt, name, err)
	}
	defer rc.Close()
	budget := max
	if c.lim.MaxFile > 0 {
		left := c.lim.MaxFile - c.used
		if budget <= 0 || left < budget {
			budget = left
		}
	}
	var src io.Reader = rc
	if budget > 0 {
		src = io.LimitReader(rc, budget+1)
	}
	data, err := io.ReadAll(src)
	if err != nil {
		return nil, fmt.Errorf("%w: %s: %v", ErrCorrupt, name, err)
	}
	if budget > 0 && int64(len(data)) > budget {
		return nil, fmt.Errorf("%w: %s expands past the limit", ErrTooLarge, name)
	}
	c.used += int64(len(data))
	return data, nil
}

func (c *container) readRef(ref *FileRef, max int64) ([]byte, error) {
	data, err := c.read(ref.Path, max)
	if err != nil {
		return nil, err
	}
	if int64(len(data)) != ref.Size || sum(data) != ref.SHA256 {
		return nil, fmt.Errorf("%w: %s does not match its checksum", ErrCorrupt, ref.Path)
	}
	return data, nil
}

// ReadManifest reads and validates only the manifest (inspection: show
// what a file contains before importing it).
func ReadManifest(r io.ReaderAt, size int64, lim Limits) (*Manifest, error) {
	c, err := openContainer(r, size, lim)
	if err != nil {
		return nil, err
	}
	return c.manifest()
}

func (c *container) manifest() (*Manifest, error) {
	if _, ok := c.files[ManifestName]; !ok {
		return nil, fmt.Errorf("%w: no %s", ErrNotPersona, ManifestName)
	}
	raw, err := c.read(ManifestName, c.lim.MaxManifest)
	if err != nil {
		return nil, err
	}
	var m Manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("%w: manifest: %v", ErrCorrupt, err)
	}
	if err := m.Validate(); err != nil {
		return nil, err
	}
	return &m, nil
}

// Read reads and verifies a whole .asvoice file.
func Read(r io.ReaderAt, size int64, lim Limits) (*Bundle, error) {
	c, err := openContainer(r, size, lim)
	if err != nil {
		return nil, err
	}
	m, err := c.manifest()
	if err != nil {
		return nil, err
	}
	b := &Bundle{Manifest: *m}
	fs := m.Files
	if fs.Preview.Path != PreviewName {
		return nil, invalid("preview path %q", fs.Preview.Path)
	}
	if b.Preview, err = c.readRef(&fs.Preview, lim.MaxPreview); err != nil {
		return nil, err
	}
	if !isWAV(b.Preview) {
		return nil, invalid("preview is not a WAV file")
	}
	if ref := fs.Reference; ref != nil {
		ext := strings.TrimPrefix(path.Ext(ref.Path), ".")
		if ref.Path != "reference."+ext || !slices.Contains(referenceExts, ext) {
			return nil, invalid("reference path %q", ref.Path)
		}
		if b.Reference, err = c.readRef(ref, lim.MaxReference); err != nil {
			return nil, err
		}
		if !sniffAudio(b.Reference, ext) {
			return nil, invalid("reference is not a %s file", ext)
		}
		b.ReferenceExt = ext
	}
	if p := fs.Portrait; p != nil {
		ext := strings.TrimPrefix(path.Ext(p.Path), ".")
		if p.Path != "portrait."+ext || !slices.Contains(portraitExts, ext) {
			return nil, invalid("portrait path %q", p.Path)
		}
		if b.Portrait, err = c.readRef(p, lim.MaxPortrait); err != nil {
			return nil, err
		}
		if !sniffImage(b.Portrait, ext) {
			return nil, invalid("portrait is not a %s image", ext)
		}
		b.PortraitExt = ext
	}
	if cr := fs.Consent; cr != nil {
		if cr.Path != ConsentName {
			return nil, invalid("consent path %q", cr.Path)
		}
		if b.ConsentAudio, err = c.readRef(cr, lim.MaxConsent); err != nil {
			return nil, err
		}
		if !isWAV(b.ConsentAudio) {
			return nil, invalid("consent recording is not a WAV file")
		}
	}
	return b, nil
}

// ReadFile reads a .asvoice file from disk.
func ReadFile(name string, lim Limits) (*Bundle, error) {
	data, err := readAllLimited(name, lim.MaxFile)
	if err != nil {
		return nil, err
	}
	return Read(bytes.NewReader(data), int64(len(data)), lim)
}

func readAllLimited(name string, max int64) ([]byte, error) {
	f, err := os.Open(name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if st, err := f.Stat(); err == nil && max > 0 && st.Size() > max {
		return nil, fmt.Errorf("%w: %d bytes (limit %d)", ErrTooLarge, st.Size(), max)
	}
	data, err := io.ReadAll(f)
	if err != nil {
		return nil, err
	}
	if max > 0 && int64(len(data)) > max {
		return nil, errors.New("persona: file grew while reading")
	}
	return data, nil
}
