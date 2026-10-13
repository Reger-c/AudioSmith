package main

// Voice portraits: an image the user drops or picks (PNG, JPEG or WebP,
// at most 5 MB) becomes a 256×256 PNG, cropped to its centre square.
// Re-encoding drops every metadata chunk (EXIF, GPS, colour profiles).

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	_ "image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/image/draw"
	_ "golang.org/x/image/webp"
)

const (
	portraitSize     = 256
	portraitMaxBytes = 5 << 20
	portraitMaxPix   = 16_000_000 // 16 MP
)

var portraitExts = []string{".png", ".jpg", ".jpeg", ".webp"}

var (
	errPortraitType  = errors.New("not a PNG, JPEG or WebP image")
	errPortraitLarge = errors.New("image larger than 5 MB")
)

// isPortraitPath reports whether a file looks like a portrait image.
func isPortraitPath(p string) bool {
	ext := strings.ToLower(filepath.Ext(p))
	for _, e := range portraitExts {
		if ext == e {
			return true
		}
	}
	return false
}

// portraitPNG reads an image file and returns the portrait PNG.
func portraitPNG(path string) ([]byte, error) {
	if !isPortraitPath(path) {
		return nil, errPortraitType
	}
	st, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if st.Size() > portraitMaxBytes {
		return nil, errPortraitLarge
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return portraitFrom(data)
}

// portraitFrom crops image data to its centre square and scales it to
// portraitSize, as a PNG without metadata.
func portraitFrom(data []byte) ([]byte, error) {
	cfg, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return nil, errPortraitType
	}
	if format != "png" && format != "jpeg" && format != "webp" {
		return nil, errPortraitType
	}
	if cfg.Width <= 0 || cfg.Height <= 0 || cfg.Width*cfg.Height > portraitMaxPix {
		return nil, fmt.Errorf("image of %d×%d pixels: too large", cfg.Width, cfg.Height)
	}
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, errPortraitType
	}
	b := img.Bounds()
	side := min(b.Dx(), b.Dy())
	crop := image.Rect(b.Min.X+(b.Dx()-side)/2, b.Min.Y+(b.Dy()-side)/2, 0, 0)
	crop.Max = crop.Min.Add(image.Pt(side, side))
	dst := image.NewRGBA(image.Rect(0, 0, portraitSize, portraitSize))
	draw.CatmullRom.Scale(dst, dst.Bounds(), img, crop, draw.Src, nil)
	var out bytes.Buffer
	if err := png.Encode(&out, dst); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}
