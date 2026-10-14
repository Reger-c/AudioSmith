package ffmpeg

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
)

// Asset is one downloadable file of a build.
type Asset struct {
	URL    string
	SHA256 string
	Size   int64
}

// Build is a downloadable static ffmpeg build.
type Build struct {
	Name    string // who builds it
	Home    string // its page
	Version string // ffmpeg's version
	// Licence of the binaries (shown before the download).
	Licence string
	FFmpeg  Asset
	FFprobe Asset
}

// Pinned is the build "Download ffmpeg" fetches: static macOS arm64
// binaries that need only system frameworks, built from source in
// public CI by Google's shaka-project (GitHub release n8.1.2-1, digests
// from the release's assets, checked against the binaries).
//
// No reputable LGPL-only static arm64 build exists (checked 2026-10-14:
// BtbN has no macOS builds; evermeet, OSXExperts, Martin Riedl, Tyrrrz,
// eugeneware and shaka all enable GPL parts such as x264). This one is
// GPL-3.0: AudioSmith does not ship or link it — the user downloads it
// from its publisher and AudioSmith runs it as a separate program.
// Update all fields together, after checking the new digests.
var Pinned = Build{
	Name:    "shaka-project/static-ffmpeg-binaries",
	Home:    "https://github.com/shaka-project/static-ffmpeg-binaries/releases/tag/n8.1.2-1",
	Version: "n8.1.2",
	Licence: "GPL-3.0",
	FFmpeg: Asset{
		URL:    "https://github.com/shaka-project/static-ffmpeg-binaries/releases/download/n8.1.2-1/ffmpeg-osx-arm64",
		SHA256: "e7b9fcd97f95f333512d6e8b8ac24d9dbc08f189f36047695499bd7b57214b22",
		Size:   34074040,
	},
	FFprobe: Asset{
		URL:    "https://github.com/shaka-project/static-ffmpeg-binaries/releases/download/n8.1.2-1/ffprobe-osx-arm64",
		SHA256: "ded4c698b8ff38d0bc1fd30fcc5e768dc46f58bc15a8dfd61f98615ba49cde5c",
		Size:   33882408,
	},
}

// Size is the download's size in bytes.
func (b Build) Size() int64 { return b.FFmpeg.Size + b.FFprobe.Size }

// ChecksumError: a download did not match its pinned digest.
type ChecksumError struct{ URL, Got string }

func (e *ChecksumError) Error() string {
	return fmt.Sprintf("%s: checksum mismatch (got %s)", e.URL, e.Got)
}

// Install downloads b into dir as "ffmpeg" and "ffprobe", checks both
// against their digests, makes them executable and returns ffmpeg's
// path. progress gets the bytes done of the total. Files are written
// under temporary names and renamed only once checked; a failure leaves
// what was there before.
func Install(ctx context.Context, client *http.Client, dir string, b Build, progress func(done, total int64)) (string, error) {
	if client == nil {
		client = http.DefaultClient
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	total := b.Size()
	var base int64
	var tmps []string
	defer func() {
		for _, t := range tmps {
			os.Remove(t)
		}
	}()
	for _, a := range []Asset{b.FFmpeg, b.FFprobe} {
		tmp, err := fetch(ctx, client, dir, a, func(n int64) {
			if progress != nil {
				progress(base+n, total)
			}
		})
		if err != nil {
			return "", err
		}
		tmps = append(tmps, tmp)
		base += a.Size
	}
	names := []string{"ffmpeg", "ffprobe"}
	for i, tmp := range tmps {
		if err := os.Chmod(tmp, 0o755); err != nil {
			return "", err
		}
		prepare(tmp)
		if err := os.Rename(tmp, filepath.Join(dir, names[i])); err != nil {
			return "", err
		}
	}
	tmps = nil
	return filepath.Join(dir, "ffmpeg"), nil
}

// fetch downloads a into a temporary file in dir and checks it.
func fetch(ctx context.Context, client *http.Client, dir string, a Asset, progress func(int64)) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.URL, nil)
	if err != nil {
		return "", err
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("%s: %s", a.URL, resp.Status)
	}
	f, err := os.CreateTemp(dir, ".download-*")
	if err != nil {
		return "", err
	}
	h := sha256.New()
	var done int64
	buf := make([]byte, 256<<10)
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			if _, err := f.Write(buf[:n]); err != nil {
				f.Close()
				os.Remove(f.Name())
				return "", err
			}
			h.Write(buf[:n])
			done += int64(n)
			progress(done)
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			f.Close()
			os.Remove(f.Name())
			return "", rerr
		}
	}
	if err := f.Close(); err != nil {
		os.Remove(f.Name())
		return "", err
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != a.SHA256 {
		os.Remove(f.Name())
		return "", &ChecksumError{URL: a.URL, Got: got}
	}
	return f.Name(), nil
}

// prepare lets macOS run a downloaded binary: no quarantine flag (Go's
// downloads set none, but a copy from a browser may), and an ad-hoc
// signature when it has none (Apple silicon refuses unsigned code).
var prepare = func(path string) {
	if runtime.GOOS != "darwin" {
		return
	}
	exec.Command("/usr/bin/xattr", "-d", "com.apple.quarantine", path).Run()
	if exec.Command("/usr/bin/codesign", "-v", path).Run() != nil {
		exec.Command("/usr/bin/codesign", "--force", "-s", "-", path).Run()
	}
}

// Verify checks installed files against b's digests.
func Verify(dir string, b Build) error {
	for name, a := range map[string]Asset{"ffmpeg": b.FFmpeg, "ffprobe": b.FFprobe} {
		f, err := os.Open(filepath.Join(dir, name))
		if err != nil {
			return err
		}
		h := sha256.New()
		_, err = io.Copy(h, f)
		f.Close()
		if err != nil {
			return err
		}
		if got := hex.EncodeToString(h.Sum(nil)); got != a.SHA256 {
			return &ChecksumError{URL: a.URL, Got: got}
		}
	}
	return nil
}

// Remove deletes a build Install put in dir.
func Remove(dir string) error {
	for _, n := range []string{"ffmpeg", "ffprobe"} {
		if err := os.Remove(filepath.Join(dir, n)); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	os.Remove(dir) // when empty
	return nil
}
