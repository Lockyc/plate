package clip

import (
	"image"
	"image/color"
	"math"
	"math/rand/v2"
	"testing"
)

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

// level is the red channel of img at x,y, as a fraction of full scale.
func level(img *image.RGBA64, x, y int) float64 { return float64(img.RGBA64At(x, y).R) / 0xffff }

func TestResampleIdentity(t *testing.T) {
	rng := rand.New(rand.NewPCG(1, 2))
	src := image.NewRGBA64(image.Rect(0, 0, 40, 30))
	for i := range src.Pix {
		src.Pix[i] = uint8(rng.IntN(256))
	}
	for i := 6; i < len(src.Pix); i += 8 { // opaque
		src.Pix[i], src.Pix[i+1] = 0xff, 0xff
	}
	dst := image.NewRGBA64(image.Rect(0, 0, 20, 12))
	dr := image.Rect(2, 1, 18, 11)
	Resample(dst, dr, src, Rect{7, 9, 16, 10})
	for y := dr.Min.Y; y < dr.Max.Y; y++ {
		for x := dr.Min.X; x < dr.Max.X; x++ {
			if got, want := dst.RGBA64At(x, y), src.RGBA64At(x+5, y+8); got != want {
				t.Fatalf("(%d,%d) = %v, want %v", x, y, got, want)
			}
		}
	}
}

func TestResampleHalfPixelShiftIsSymmetric(t *testing.T) {
	src := image.NewRGBA64(image.Rect(0, 0, 16, 4))
	fill(src, image.Rect(0, 0, 8, 4), grey(0.25))
	fill(src, image.Rect(8, 0, 16, 4), grey(0.75))
	dst := image.NewRGBA64(image.Rect(0, 0, 15, 4))
	Resample(dst, dst.Bounds(), src, Rect{0.5, 0, 15, 4})
	// dst x samples src at x+1, so the edge (src x=8) lands on dst pixel 7's centre.
	if mid := level(dst, 7, 2); math.Abs(mid-0.5) > 1.0/255 {
		t.Errorf("edge pixel = %.4f, want 0.5", mid)
	}
	for d := 1; d <= 3; d++ {
		lo, hi := level(dst, 7-d, 2), level(dst, 7+d, 2)
		if math.Abs((0.5-lo)-(hi-0.5)) > 1.0/255 {
			t.Errorf("±%d px: %.4f and %.4f are not equidistant from mid-grey", d, lo, hi)
		}
	}
}

func TestResampleDownscaleNoAlias(t *testing.T) {
	src := image.NewRGBA64(image.Rect(0, 0, 64, 64))
	for y := range 64 {
		for x := range 64 {
			src.SetRGBA64(x, y, grey(float64((x+y)%2)))
		}
	}
	dst := image.NewRGBA64(image.Rect(0, 0, 32, 32))
	Resample(dst, dst.Bounds(), src, Rect{0, 0, 64, 64})
	// The border reads clamped edge pixels; the interior is the alias test.
	for y := 2; y < 30; y++ {
		for x := 2; x < 30; x++ {
			if v := level(dst, x, y); math.Abs(v-0.5) > 2.0/255 {
				t.Fatalf("(%d,%d) = %.4f, want mid-grey", x, y, v)
			}
		}
	}
}

func TestResampleClampsToTheSourceRect(t *testing.T) {
	src := image.NewRGBA64(image.Rect(0, 0, 20, 20))
	fill(src, src.Bounds(), grey(0))
	fill(src, image.Rect(0, 10, 20, 20), grey(1))
	dst := image.NewRGBA64(image.Rect(0, 0, 5, 5))
	Resample(dst, dst.Bounds(), src, Rect{0, 10, 20, 10})
	for y := range 5 {
		if v := level(dst, 2, y); v != 1 {
			t.Fatalf("row %d = %.4f: the strip took in pixels outside its rect", y, v)
		}
	}
}
