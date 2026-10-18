package raster

import (
	"errors"
	"image"
	"image/color"
	"math"
	"testing"
)

// lines is a black w×h image with a 1-px vertical line of c every 4 px.
func lines(w, h int, c color.RGBA64) *image.RGBA64 {
	img := solid(w, h, color.Black)
	for y := 0; y < h; y++ {
		for x := 1; x < w; x += 4 {
			img.SetRGBA64(x, y, c)
		}
	}
	return img
}

func TestDetailPerChannel(t *testing.T) {
	ref := lines(16, 8, color.RGBA64{0xffff, 0, 0xffff, 0xffff})
	if v, err := Detail(ref, ref, 1, Luma); err != nil || v != 1 {
		t.Fatalf("luma of the reference itself = %v, %v; want 1", v, err)
	}
	// Green is flat in a magenta reference: nothing to recover there.
	if _, err := Detail(ref, ref, 1, Green); !errors.Is(err, ErrNoDetail) {
		t.Fatalf("green of magenta: err = %v, want ErrNoDetail", err)
	}
	// Losing the red stroke only shows in red.
	got := lines(16, 8, color.RGBA64{0, 0, 0xffff, 0xffff})
	if v, _ := Detail(got, ref, 1, Blue); v != 1 {
		t.Fatalf("blue kept = %v, want 1", v)
	}
	if v, _ := Detail(got, ref, 1, Red); math.Abs(v) > 1e-9 {
		t.Fatalf("red lost = %v, want 0", v)
	}
	if _, err := Detail(got, solid(4, 4, color.Black), 1, Luma); err == nil {
		t.Fatal("mismatched sizes gave no error")
	}
	if _, err := Detail(ref, ref, 0, Luma); err == nil {
		t.Fatal("sigma 0 gave no error")
	}
}

func TestDetailMinSkipsFlatChannels(t *testing.T) {
	ref := lines(16, 8, color.RGBA64{0xffff, 0, 0xffff, 0xffff})
	got := lines(16, 8, color.RGBA64{0xffff, 0, 0x8000, 0xffff})
	blue, _ := Detail(got, ref, 1, Blue)
	if v, err := DetailMin(got, ref, 1); err != nil || v != blue || blue >= 1 {
		t.Fatalf("DetailMin = %v, %v; want blue's %v (green, flat, skipped)", v, err, blue)
	}
	flat := solid(16, 8, color.Black)
	if _, err := DetailMin(flat, flat, 1); !errors.Is(err, ErrNoDetail) {
		t.Fatalf("all flat: err = %v, want ErrNoDetail", err)
	}
}

// TestDetailMinSkipsWeakChannels: neon green text on near black barely
// moves blue, and a channel normalised by its own tiny energy scores any
// codec noise orders of magnitude negative. A channel with under
// weakChannel of the strongest channel's energy is left out.
func TestDetailMinSkipsWeakChannels(t *testing.T) {
	ref := lines(16, 8, color.RGBA64{0, 0xffff, 0x0400, 0xffff})
	got := lines(16, 8, color.RGBA64{0, 0xffff, 0, 0xffff})
	if v, _ := Detail(got, ref, 1, Blue); v > 0.5 {
		t.Fatalf("blue alone = %v; the fixture should lose the weak channel", v)
	}
	if v, err := DetailMin(got, ref, 1); err != nil || v != 1 {
		t.Fatalf("DetailMin = %v, %v; want green's 1, blue (under 5%% of green's energy) skipped", v, err)
	}
	// At 25% of the strongest a channel counts.
	ref = lines(16, 8, color.RGBA64{0, 0xffff, 0x8000, 0xffff})
	if v, _ := DetailMin(got, ref, 1); v != 0 {
		t.Fatalf("DetailMin = %v; a channel well above 5%% must count, and blue was lost", v)
	}
}
