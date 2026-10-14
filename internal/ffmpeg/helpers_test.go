package ffmpeg

import (
	"image"
	"image/color"
	"os"
	"testing"

	"golang.org/x/image/math/fixed"
)

func fixed20(px int) fixed.Int26_6 { return fixed.I(px) }

func stripTestImage() image.Image {
	img := image.NewNRGBA(image.Rect(0, 0, 64, 64))
	for y := 0; y < 64; y++ {
		for x := 0; x < 64; x++ {
			img.Set(x, y, color.NRGBA{uint8(x * 4), uint8(y * 4), 128, 255})
		}
	}
	return img
}

// whiteBottom counts near-white pixels in the bottom quarter of an image.
func whiteBottom(t *testing.T, path string) int {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	img, _, err := image.Decode(f)
	if err != nil {
		t.Fatal(err)
	}
	b := img.Bounds()
	n := 0
	for y := b.Max.Y - b.Dy()/4; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			r, g, bl, _ := img.At(x, y).RGBA()
			if r > 0xf000 && g > 0xf000 && bl > 0xf000 {
				n++
			}
		}
	}
	return n
}
