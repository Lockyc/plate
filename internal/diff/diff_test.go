package diff

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lockyc/plate/internal/raster"
)

func noise(w, h int, seed uint64) *image.RGBA64 {
	r := rand.New(rand.NewPCG(seed, 1))
	img := image.NewRGBA64(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{uint8(r.IntN(256)), uint8(r.IntN(256)), uint8(r.IntN(256)), 255})
		}
	}
	return img
}

// shiftDown returns a with its content moved down by n rows, white above.
func shiftDown(a *image.RGBA64, n int) *image.RGBA64 {
	b := image.NewRGBA64(a.Bounds())
	for y := 0; y < a.Bounds().Dy(); y++ {
		for x := 0; x < a.Bounds().Dx(); x++ {
			if y < n {
				b.Set(x, y, color.White)
			} else {
				b.Set(x, y, a.At(x, y-n))
			}
		}
	}
	return b
}

func TestCompareFindsShift(t *testing.T) {
	a := noise(120, 200, 1)
	res, _, _, err := Compare(a, shiftDown(a, 4), 28, 60, 2)
	if err != nil {
		t.Fatal(err)
	}
	if res.Shift != 4 || res.Percent != 0 {
		t.Fatalf("Compare = %+v, want shift 4 and 0%%", res)
	}
}

// Every shift of two blank images scores 0%; the smallest one wins, so the
// compared rows are the whole height.
func TestCompareTiesPreferNoShift(t *testing.T) {
	a := image.NewRGBA64(image.Rect(0, 0, 50, 200))
	res, _, rows, err := Compare(a, a, 28, 60, 2)
	if err != nil {
		t.Fatal(err)
	}
	if res.Shift != 0 || rows.Dy() != 200 {
		t.Fatalf("Compare = %+v over %v, want shift 0 over all 200 rows", res, rows)
	}
}

func TestCompareScoresChange(t *testing.T) {
	a := noise(100, 100, 2)
	b := image.NewRGBA64(a.Bounds())
	copy(b.Pix, a.Pix)
	// Move every channel by 128 in the top tenth: a luma difference of 128,
	// far over the tolerance, for every pixel there.
	for y := 0; y < 10; y++ {
		for x := 0; x < 100; x++ {
			c := a.RGBA64At(x, y)
			shift := func(v uint16) uint16 { return uint16((uint8(v>>8)+128)&0xff) * 257 }
			b.SetRGBA64(x, y, color.RGBA64{shift(c.R), shift(c.G), shift(c.B), 65535})
		}
	}
	res, _, _, err := Compare(a, b, 28, 0, 2)
	if err != nil {
		t.Fatal(err)
	}
	if res.Percent != 10 {
		t.Fatalf("Percent = %v, want 10", res.Percent)
	}
}

func TestCompareTinyAndMismatched(t *testing.T) {
	if res, _, _, err := Compare(noise(5, 1, 3), noise(5, 1, 3), 28, 60, 2); err != nil || res.Percent != 0 {
		t.Fatalf("1-row images: %+v, %v", res, err)
	}
	a := noise(100, 100, 4)
	b := image.NewRGBA64(image.Rect(0, 0, 80, 120))
	for y := 0; y < 100; y++ {
		for x := 0; x < 80; x++ {
			b.Set(x, y, a.At(x, y))
		}
	}
	if res, _, _, err := Compare(a, b, 28, 60, 2); err != nil || res.Percent != 0 || res.Shift != 0 {
		t.Fatalf("overlap: %+v, %v", res, err)
	}
	if _, _, _, err := Compare(noise(0, 0, 1), a, 28, 60, 2); err == nil {
		t.Error("empty image accepted")
	}
}

func TestDiffMain(t *testing.T) {
	dir := t.TempDir()
	a := noise(60, 60, 5)
	pa, pb, pd := filepath.Join(dir, "a.png"), filepath.Join(dir, "b.png"), filepath.Join(dir, "d.png")
	raster.SavePNG(pa, a)
	raster.SavePNG(pb, shiftDown(a, 2))
	var out, errb bytes.Buffer
	if code := Main(context.Background(), []string{"--threshold", "1", "--out", pd, pa, pb}, &out, &errb); code != 0 {
		t.Fatalf("code %d: %s %s", code, out.String(), errb.String())
	}
	if !strings.HasPrefix(out.String(), "ok 0.0% differ (threshold 1.0%, best fit at +2px vertical)") {
		t.Errorf("stdout %q", out.String())
	}
	if _, err := os.Stat(pd); err != nil {
		t.Error("no diff picture")
	}
	raster.SavePNG(pb, noise(60, 60, 6))
	out.Reset()
	if code := Main(context.Background(), []string{"--threshold", "1", pa, pb}, &out, &errb); code != 1 || !strings.HasPrefix(out.String(), "OVER ") {
		t.Fatalf("code %d, stdout %q", code, out.String())
	}
	if code := Main(context.Background(), []string{pa, filepath.Join(dir, "missing.png")}, &out, &errb); code != 2 {
		t.Fatalf("missing file: code %d, want 2", code)
	}
}

func TestDiffMainOutputSameAsInput(t *testing.T) {
	dir := t.TempDir()
	a := noise(60, 60, 5)
	b := noise(60, 60, 6)
	pa, pb := filepath.Join(dir, "a.png"), filepath.Join(dir, "b.png")
	raster.SavePNG(pa, a)
	raster.SavePNG(pb, b)

	// Store original a.png content
	beforeContent, err := os.ReadFile(pa)
	if err != nil {
		t.Fatal(err)
	}

	// Test --out==<a>: a.png should be unchanged
	var out, errb bytes.Buffer
	if code := Main(context.Background(), []string{"--out", pa, pa, pb}, &out, &errb); code != 2 {
		t.Fatalf("--out same as <a>: code %d, want 2", code)
	}
	afterContent, err := os.ReadFile(pa)
	if err != nil {
		t.Fatal(err)
	}
	if string(beforeContent) != string(afterContent) {
		t.Error("a.png was modified")
	}

	// Test --out==<b>: b.png should be unchanged
	beforeContent, err = os.ReadFile(pb)
	if err != nil {
		t.Fatal(err)
	}
	out.Reset()
	errb.Reset()
	if code := Main(context.Background(), []string{"--out", pb, pa, pb}, &out, &errb); code != 2 {
		t.Fatalf("--out same as <b>: code %d, want 2", code)
	}
	afterContent, err = os.ReadFile(pb)
	if err != nil {
		t.Fatal(err)
	}
	if string(beforeContent) != string(afterContent) {
		t.Error("b.png was modified")
	}
}

func TestDiffMainValidation(t *testing.T) {
	dir := t.TempDir()
	a := noise(60, 60, 5)
	b := noise(60, 60, 6)
	pa, pb := filepath.Join(dir, "a.png"), filepath.Join(dir, "b.png")
	raster.SavePNG(pa, a)
	raster.SavePNG(pb, b)

	var out, errb bytes.Buffer

	// Test negative --max-shift
	if code := Main(context.Background(), []string{"--max-shift", "-1", pa, pb}, &out, &errb); code != 2 {
		t.Fatalf("negative --max-shift: code %d, want 2", code)
	}

	// Test --step < 1
	out.Reset()
	errb.Reset()
	if code := Main(context.Background(), []string{"--step", "0", pa, pb}, &out, &errb); code != 2 {
		t.Fatalf("--step < 1: code %d, want 2", code)
	}

	// Test negative --threshold when explicitly set
	out.Reset()
	errb.Reset()
	if code := Main(context.Background(), []string{"--threshold", "-1", pa, pb}, &out, &errb); code != 2 {
		t.Fatalf("negative --threshold: code %d, want 2", code)
	}
}

func TestCompareError(t *testing.T) {
	// Test that Compare properly validates inputs
	a := noise(100, 100, 7)

	// Empty b image should error
	b := image.NewRGBA64(image.Rect(0, 0, 0, 0))
	if _, _, _, err := Compare(a, b, 28, 60, 2); err == nil {
		t.Error("empty image should return error")
	}
}

func TestCompareHugeMaxShift(t *testing.T) {
	a := noise(20, 40, 3)
	done := make(chan Result, 1)
	go func() {
		res, _, _, _ := Compare(a, shiftDown(a, 2), 28, 1<<40, 1)
		done <- res
	}()
	select {
	case res := <-done:
		if res.Shift != 2 {
			t.Errorf("shift %d, want 2", res.Shift)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Compare spun on a huge --max-shift")
	}
}
