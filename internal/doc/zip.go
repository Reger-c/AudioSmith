package doc

import (
	"archive/zip"
	"errors"
	"fmt"
	"io"
	"net/url"
	"path"
	"strings"
)

// zipBox reads entries of a ZIP container within the limits.
type zipBox struct {
	zr    *zip.Reader
	files map[string]*zip.File // by cleaned name; also lower-cased
	used  int64
	lim   Limits
}

func openZip(r io.ReaderAt, size int64, lim Limits) (*zipBox, error) {
	zr, err := zip.NewReader(r, size)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrCorrupt, err)
	}
	if lim.MaxEntries > 0 && len(zr.File) > lim.MaxEntries {
		return nil, fmt.Errorf("%w: %d entries (limit %d)", ErrTooLarge, len(zr.File), lim.MaxEntries)
	}
	z := &zipBox{zr: zr, files: map[string]*zip.File{}, lim: lim}
	for _, f := range zr.File {
		name := path.Clean(strings.TrimPrefix(f.Name, "/"))
		z.files[name] = f
		if _, ok := z.files[strings.ToLower(name)]; !ok {
			z.files[strings.ToLower(name)] = f
		}
	}
	return z, nil
}

// has reports whether the container holds name.
func (z *zipBox) has(name string) bool {
	_, ok := z.lookup(name)
	return ok
}

func (z *zipBox) lookup(name string) (*zip.File, bool) {
	name = path.Clean(strings.TrimPrefix(name, "/"))
	if f, ok := z.files[name]; ok {
		return f, true
	}
	if u, err := url.PathUnescape(name); err == nil {
		if f, ok := z.files[u]; ok {
			return f, true
		}
		name = u
	}
	f, ok := z.files[strings.ToLower(name)]
	return f, ok
}

// read inflates one entry, counting it against MaxUncompressed.
func (z *zipBox) read(name string) ([]byte, error) {
	f, ok := z.lookup(name)
	if !ok {
		return nil, fmt.Errorf("%w: missing %s", ErrCorrupt, name)
	}
	budget := int64(1<<62 - 1)
	if z.lim.MaxUncompressed > 0 {
		budget = z.lim.MaxUncompressed - z.used
	}
	if budget <= 0 || (f.UncompressedSize64 > uint64(budget)) {
		return nil, fmt.Errorf("%w: more than %d bytes uncompressed", ErrTooLarge, z.lim.MaxUncompressed)
	}
	rc, err := f.Open()
	if err != nil {
		return nil, fmt.Errorf("%w: %s: %v", ErrCorrupt, name, err)
	}
	defer rc.Close()
	// The header's size may lie: read at most budget+1 bytes.
	b, err := io.ReadAll(io.LimitReader(rc, budget+1))
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("%w: %s: %v", ErrCorrupt, name, err)
	}
	if int64(len(b)) > budget {
		return nil, fmt.Errorf("%w: more than %d bytes uncompressed", ErrTooLarge, z.lim.MaxUncompressed)
	}
	z.used += int64(len(b))
	return b, nil
}

// resolve joins a reference found in document base with base's directory,
// dropping any fragment and query.
func resolve(base, ref string) string {
	if i := strings.IndexAny(ref, "#?"); i >= 0 {
		ref = ref[:i]
	}
	if u, err := url.PathUnescape(ref); err == nil {
		ref = u
	}
	if ref == "" {
		return path.Clean(base)
	}
	if strings.HasPrefix(ref, "/") {
		return path.Clean(ref[1:])
	}
	return path.Clean(path.Join(path.Dir(base), ref))
}
