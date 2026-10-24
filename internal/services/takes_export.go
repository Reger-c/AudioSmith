package services

// Exporting a take's audio: as it is, or as a WAV of another depth.

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"

	"github.com/hehehai/AudioSmith/internal/core"
)

// ExportAs writes a take's audio to dst as a WAV of bits (16, 24 or 32
// float; 0 copies the file as it is). 16 bits get TPDF dither.
func (s *Takes) ExportAs(ctx context.Context, id, dst string, bits int) error {
	err := s.exportAs(ctx, id, dst, bits)
	logOp(ctx, "take.export", id, "", err, "file", filepath.Base(dst), "bits", bits)
	return err
}

func (s *Takes) exportAs(ctx context.Context, id, dst string, bits int) error {
	if bits == 0 {
		return s.export(ctx, id, dst)
	}
	t, err := s.store.Get(ctx, id)
	if err != nil {
		return err
	}
	return convertWAV(ctx, s.store.AudioPath(t), dst, bits)
}

// Export streams the take's audio into dst through a temporary file
// beside it, renamed into place when complete.
func (s *Takes) Export(ctx context.Context, id, dst string) error {
	err := s.export(ctx, id, dst)
	logOp(ctx, "take.export", id, "", err, "file", filepath.Base(dst))
	return err
}

func (s *Takes) export(ctx context.Context, id, dst string) error {
	t, err := s.store.Get(ctx, id)
	if err != nil {
		return err
	}
	return copyFile(ctx, s.store.AudioPath(t), dst)
}

func copyFile(ctx context.Context, src, dst string) (err error) {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.CreateTemp(filepath.Dir(dst), "."+filepath.Base(dst)+".*.part")
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			out.Close()
			os.Remove(out.Name())
		}
	}()
	if _, err = io.Copy(out, ctxReader{ctx, in}); err != nil {
		return err
	}
	if err = out.Sync(); err != nil {
		return err
	}
	if err = out.Close(); err != nil {
		return err
	}
	if err = os.Chmod(out.Name(), 0o644); err != nil {
		return err
	}
	return os.Rename(out.Name(), dst)
}

// ctxReader stops a copy when its context ends.
type ctxReader struct {
	ctx context.Context
	r   io.Reader
}

func (c ctxReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, errors.Join(core.ErrCancelled, err)
	}
	return c.r.Read(p)
}
