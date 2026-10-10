package quality

import (
	"image"
	"image/color"
	"math"
	"os"
	"path/filepath"
	"testing"

	"github.com/lockyc/plate/internal/raster"
)

func TestPixelAndDPI(t *testing.T) {
	p := filepath.Join(t.TempDir(), "a.png")
	img := image.NewRGBA(image.Rect(0, 0, 10, 10))
	img.Set(9, 9, color.RGBA{0, 204, 0, 255})
	raster.SavePNG(p, img)
	v, err := Metrics["pixel"](Params{"image": p, "at": "0.99,0.99", "color": "#00cc00"})
	if err != nil || v != 0 {
		t.Fatalf("pixel = %v, %v", v, err)
	}
	if v, _ := Metrics["pixel"](Params{"image": p, "at": "0,0", "color": "#00cc00"}); v < 0.4 {
		t.Errorf("pixel at a black corner = %v", v)
	}
	raster.SetDPI(p, 192)
	if v, _ := Metrics["dpi"](Params{"image": p}); v < 191.99 || v > 192.01 {
		t.Errorf("dpi = %v", v)
	}
}

func TestPixelRejectsOutOfRangeAt(t *testing.T) {
	p := filepath.Join(t.TempDir(), "a.png")
	raster.SavePNG(p, image.NewRGBA(image.Rect(0, 0, 10, 10)))
	for _, at := range []string{"-0.1,0", "0,1.5", "2,2", "NaN,0", "0,NaN"} {
		if _, err := Metrics["pixel"](Params{"image": p, "at": at, "color": "#000000"}); err == nil {
			t.Errorf("at %q accepted", at)
		}
	}
}

func TestGreenFringe(t *testing.T) {
	p := filepath.Join(t.TempDir(), "f.png")
	img := image.NewNRGBA(image.Rect(0, 0, 10, 10))
	for y := 0; y < 10; y++ {
		for x := 0; x < 10; x++ {
			img.Set(x, y, color.NRGBA{200, 150, 100, 255})
		}
	}
	img.Set(0, 0, color.NRGBA{60, 160, 50, 255}) // green, opaque
	img.Set(1, 0, color.NRGBA{60, 160, 50, 0})   // green, transparent: ignored
	raster.SavePNG(p, img)
	v, err := Metrics["green-fringe"](Params{"image": p, "region": "0,0,1,1", "bg": "#062d5f"})
	if err != nil || v < 0.0099 || v > 0.0101 {
		t.Fatalf("green-fringe = %v, %v; want 0.01", v, err)
	}
}

func TestSeethrough(t *testing.T) {
	p := filepath.Join(t.TempDir(), "s.png")
	img := image.NewNRGBA(image.Rect(0, 0, 10, 10))
	for y := 0; y < 10; y++ {
		for x := 0; x < 10; x++ {
			a := uint8(255)
			if x < 5 {
				a = 100
			}
			img.Set(x, y, color.NRGBA{9, 9, 9, a})
		}
	}
	raster.SavePNG(p, img)
	v, err := Metrics["seethrough"](Params{"image": p, "solid": "0,0 1,0 1,1 0,1"})
	if err != nil || v < 0.49 || v > 0.51 {
		t.Fatalf("seethrough = %v, %v; want 0.5", v, err)
	}
}

func TestStdRatio(t *testing.T) {
	dir := t.TempDir()
	flat, noisy, mask := filepath.Join(dir, "f.png"), filepath.Join(dir, "n.png"), filepath.Join(dir, "m.png")
	fi, ni, mi := image.NewGray(image.Rect(0, 0, 40, 40)), image.NewGray(image.Rect(0, 0, 40, 40)), image.NewGray(image.Rect(0, 0, 40, 40))
	for y := 0; y < 40; y++ {
		for x := 0; x < 40; x++ {
			fi.SetGray(x, y, color.Gray{128})
			ni.SetGray(x, y, color.Gray{uint8(100 + 56*((x+y)%2))})
			if x >= 15 && x < 25 && y >= 15 && y < 25 {
				mi.SetGray(x, y, color.Gray{255})
			}
		}
	}
	raster.SavePNG(flat, fi)
	raster.SavePNG(noisy, ni)
	raster.SavePNG(mask, mi)
	if v, _ := Metrics["std-ratio"](Params{"a": flat, "b": noisy, "mask": mask, "ring": int64(5)}); v != 0 {
		t.Errorf("flat fill ratio = %v, want 0", v)
	}
	if v, _ := Metrics["std-ratio"](Params{"a": noisy, "b": noisy, "mask": mask, "ring": int64(5)}); v < 0.99 || v > 1.01 {
		t.Errorf("same texture ratio = %v, want 1", v)
	}
}

func TestRMSEMask(t *testing.T) {
	dir := t.TempDir()
	a, b, mask := filepath.Join(dir, "a.png"), filepath.Join(dir, "b.png"), filepath.Join(dir, "m.png")
	ai, bi, mi := image.NewGray(image.Rect(0, 0, 4, 4)), image.NewGray(image.Rect(0, 0, 4, 4)), image.NewGray(image.Rect(0, 0, 4, 4))
	for y := 0; y < 4; y++ {
		for x := 0; x < 4; x++ {
			if y < 2 {
				bi.SetGray(x, y, color.Gray{255}) // differs only where the mask is black
			} else {
				mi.SetGray(x, y, color.Gray{255})
			}
		}
	}
	raster.SavePNG(a, ai)
	raster.SavePNG(b, bi)
	raster.SavePNG(mask, mi)
	if v, err := Metrics["rmse"](Params{"a": a, "b": b, "mask": mask}); err != nil || v != 0 {
		t.Errorf("masked rmse = %v, %v; want 0", v, err)
	}
	if v, _ := Metrics["rmse"](Params{"a": a, "b": b}); v == 0 {
		t.Error("unmasked rmse = 0, want the top-half difference")
	}
}

func TestGreenFringeCompositing(t *testing.T) {
	p := filepath.Join(t.TempDir(), "h.png")
	img := image.NewNRGBA(image.Rect(0, 0, 2, 1))
	img.Set(0, 0, color.NRGBA{0, 200, 0, 128}) // half-alpha green
	img.Set(1, 0, color.NRGBA{0, 200, 0, 5})   // under the 0.02 alpha gate
	raster.SavePNG(p, img)
	for _, c := range []struct {
		bg   string
		want float64
	}{
		{"#062d5f", 0.5}, // navy leaves the half-alpha pixel green-dominant
		{"#ff00ff", 0},   // magenta bleeds through and cancels it
	} {
		v, err := Metrics["green-fringe"](Params{"image": p, "bg": c.bg})
		if err != nil || v != c.want {
			t.Errorf("bg %s: green-fringe = %v, %v; want %v", c.bg, v, err, c.want)
		}
	}
}

func TestGreenFringeEmptyRegion(t *testing.T) {
	p := filepath.Join(t.TempDir(), "e.png")
	raster.SavePNG(p, image.NewNRGBA(image.Rect(0, 0, 4, 4)))
	if v, err := Metrics["green-fringe"](Params{"image": p, "bg": "#000000", "region": "0,0,0.01,0.01"}); err == nil {
		t.Errorf("zero-pixel region gave %v, want an error", v)
	}
}

func TestRMSERejectsMaskWithRegion(t *testing.T) {
	p := filepath.Join(t.TempDir(), "a.png")
	white := image.NewGray(image.Rect(0, 0, 4, 4))
	for i := range white.Pix {
		white.Pix[i] = 255
	}
	raster.SavePNG(p, white) // a full mask would otherwise succeed
	if _, err := Metrics["rmse"](Params{"a": p, "b": p, "mask": p, "region": "0,0,1,1"}); err == nil {
		t.Error("mask with region accepted, want an error")
	}
}

func TestStdRatioErrors(t *testing.T) {
	dir := t.TempDir()
	big, small, mask := filepath.Join(dir, "b.png"), filepath.Join(dir, "s.png"), filepath.Join(dir, "m.png")
	bi, mi := image.NewGray(image.Rect(0, 0, 10, 10)), image.NewGray(image.Rect(0, 0, 10, 10))
	for y := 0; y < 10; y++ {
		for x := 0; x < 10; x++ {
			mi.SetGray(x, y, color.Gray{255}) // the mask is the whole frame: no ring
		}
	}
	raster.SavePNG(big, bi)
	raster.SavePNG(small, image.NewGray(image.Rect(0, 0, 5, 5)))
	raster.SavePNG(mask, mi)
	if v, err := Metrics["std-ratio"](Params{"a": big, "b": big, "mask": mask, "ring": int64(3)}); err == nil {
		t.Errorf("empty ring gave %v, want an error", v)
	}
	if v, err := Metrics["std-ratio"](Params{"a": big, "b": small, "mask": mask}); err == nil {
		t.Errorf("mismatched sizes gave %v, want an error", v)
	}
}

func TestDetail(t *testing.T) {
	dir := t.TempDir()
	save := func(name string, f func(x, y int) uint8) string {
		img := image.NewGray(image.Rect(0, 0, 48, 48))
		for y := 0; y < 48; y++ {
			for x := 0; x < 48; x++ {
				img.SetGray(x, y, color.Gray{f(x, y)})
			}
		}
		p := filepath.Join(dir, name)
		raster.SavePNG(p, img)
		return p
	}
	noise := func(seed int) func(x, y int) uint8 {
		return func(x, y int) uint8 {
			h := uint32(x*73856093 ^ y*19349663 ^ seed*83492791)
			h ^= h >> 13
			h *= 0x5bd1e995
			h ^= h >> 15
			return uint8(64 + h%128)
		}
	}
	orig := save("o.png", noise(1))
	flat := save("f.png", func(x, y int) uint8 { return 127 })
	// A Gaussian passes every frequency at a gain between 0 and 1, so the
	// detail it keeps is a share strictly between none and all.
	gauss := func(sigma float64) func(x, y int) uint8 {
		r := int(math.Ceil(3 * sigma))
		return func(x, y int) uint8 {
			var s, n float64
			for dy := -r; dy <= r; dy++ {
				for dx := -r; dx <= r; dx++ {
					if x+dx >= 0 && x+dx < 48 && y+dy >= 0 && y+dy < 48 {
						k := math.Exp(-float64(dx*dx+dy*dy) / (2 * sigma * sigma))
						s += k * float64(noise(1)(x+dx, y+dy))
						n += k
					}
				}
			}
			return uint8(math.Round(s / n))
		}
	}
	light, _ := Metrics["detail"](Params{"a": save("l.png", gauss(0.6)), "b": orig})
	heavy, _ := Metrics["detail"](Params{"a": save("h.png", gauss(2)), "b": orig})
	if !(0 < heavy && heavy < light && light < 1) {
		t.Errorf("detail: light blur %v, heavy blur %v; want 0 < heavy < light < 1", light, heavy)
	}
	wrong := save("w.png", noise(2))
	for _, c := range []struct {
		name, a string
		lo, hi  float64
	}{
		{"the original itself", orig, 1, 1},
		{"a flat image, all detail lost", flat, -1e-9, 1e-9},
		{"detail in the wrong places", wrong, -2, -0.1},
	} {
		v, err := Metrics["detail"](Params{"a": c.a, "b": orig})
		if err != nil || v < c.lo || v > c.hi {
			t.Errorf("%s: detail = %v, %v; want %v..%v", c.name, v, err, c.lo, c.hi)
		}
	}
	if v, err := Metrics["detail"](Params{"a": orig, "b": flat}); err == nil {
		t.Errorf("an original with no detail gave %v, want an error", v)
	}
	small := filepath.Join(dir, "s.png")
	raster.SavePNG(small, image.NewGray(image.Rect(0, 0, 5, 5)))
	if v, err := Metrics["detail"](Params{"a": small, "b": orig}); err == nil {
		t.Errorf("mismatched sizes gave %v, want an error", v)
	}
}

func TestBytes(t *testing.T) {
	p := filepath.Join(t.TempDir(), "a.webp")
	if err := os.WriteFile(p, []byte("12345"), 0o644); err != nil {
		t.Fatal(err)
	}
	if v, err := Metrics["bytes"](Params{"image": p}); err != nil || v != 5 {
		t.Errorf("bytes = %v, %v", v, err)
	}
	if _, err := Metrics["bytes"](Params{"image": p + "x"}); err == nil {
		t.Error("a missing file measured")
	}
}
