package services

// The chunk cache's pruning (LRU under its cap) and the WAV file helpers
// the pipelines share (eachBlock, convertWAV, CopyFile, ConvertWAV);
// split from render.go.

import (
	"context"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/hehehai/AudioSmith/internal/dsp/wav"
)

// eachBlock reads a WAV file in blocks.
func eachBlock(ctx context.Context, path string, fn func([]float32) error) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	rd, err := wav.NewReader(f)
	if err != nil {
		return err
	}
	buf := make([]float32, 16384*rd.Format().Channels)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		n, err := rd.Read(buf)
		if n > 0 {
			if err := fn(buf[:n]); err != nil {
				return err
			}
		}
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

// pruneEvery is how often pruneCache walks the chunk cache at most: every
// render ends with it (a dub's every line), and walking 10,000 chunks
// takes 10 ms or more (review #102). The cache may pass its cap by what
// renders write meanwhile.
const pruneEvery = time.Minute

// pruneCache keeps the chunk cache under its cap, dropping the least
// recently used chunks; at most once every pruneEvery.
func (r *renderer) pruneCache() {
	r.pruneMu.Lock()
	if !r.pruneAt.IsZero() && time.Since(r.pruneAt) < pruneEvery {
		r.pruneMu.Unlock()
		return
	}
	r.pruneAt = time.Now()
	r.pruneMu.Unlock()
	limit := r.cacheMax
	if limit <= 0 {
		limit = 2 << 30
	}
	type file struct {
		path string
		size int64
		at   time.Time
	}
	var files []file
	var total int64
	filepath.WalkDir(r.chunkDir(), func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".wav") {
			return nil
		}
		if fi, err := d.Info(); err == nil {
			files = append(files, file{path, fi.Size(), fi.ModTime()})
			total += fi.Size()
		}
		return nil
	})
	if total <= limit {
		return
	}
	slices.SortFunc(files, func(a, b file) int { return a.at.Compare(b.at) })
	for _, f := range files {
		if total <= limit*9/10 {
			break
		}
		if os.Remove(f.path) == nil {
			total -= f.size
		}
	}
}

// convertWAV writes src to dst (atomically) as a WAV of bits.
func convertWAV(ctx context.Context, src, dst string, bits int) error {
	f, err := os.Open(src)
	if err != nil {
		return err
	}
	rd, err := wav.NewReader(f)
	if err != nil {
		f.Close()
		return err
	}
	format := rd.Format()
	f.Close()
	enc := encodingOf(bits)
	out, err := wav.Create(dst, wav.Format{Rate: format.Rate, Channels: format.Channels, Encoding: enc}, wav.Options{Dither: enc == wav.PCM16 && format.Encoding != wav.PCM16})
	if err != nil {
		return err
	}
	if err := eachBlock(ctx, src, out.Write); err != nil {
		out.Abort()
		return err
	}
	return out.Close()
}

// CopyFile copies src to dst through a temporary file beside it (exports).
func CopyFile(ctx context.Context, src, dst string) error { return copyFile(ctx, src, dst) }

// ConvertWAV writes src to dst as a WAV of bits (16 with dither, 24, 32
// float): exports of renders.
func ConvertWAV(ctx context.Context, src, dst string, bits int) error {
	return convertWAV(ctx, src, dst, bits)
}
