package video

import (
	"image"
	"image/color"
	"image/png"
	"math"
	"os"
	"testing"
)

func writePNG(t *testing.T, path string, img image.Image) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := png.Encode(f, img); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

// solid is a w×h image of one opaque colour.
func solid(w, h int, c color.RGBA64) *image.RGBA64 {
	img := image.NewRGBA64(image.Rect(0, 0, w, h))
	fill(img, img.Bounds(), c)
	return img
}

// fill paints r of img one opaque colour.
func fill(img *image.RGBA64, r image.Rectangle, c color.RGBA64) {
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			img.SetRGBA64(x, y, c)
		}
	}
}

func grey(v float64) color.RGBA64 {
	g := uint16(math.Round(v * 0xffff))
	return color.RGBA64{g, g, g, 0xffff}
}

var red = color.RGBA64{0xffff, 0, 0, 0xffff}

// nearRGB reports whether c is opaque and within tol/255 of want on every colour channel.
func nearRGB(c color.RGBA64, want color.RGBA64, tol float64) bool {
	d := func(a, b uint16) bool { return math.Abs(float64(a)-float64(b))/0xffff <= tol/255 }
	return d(c.R, want.R) && d(c.G, want.G) && d(c.B, want.B) && c.A == 0xffff
}

func writeFile(t *testing.T, path, body string) string {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}
