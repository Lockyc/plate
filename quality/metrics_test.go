package quality

import (
	"fmt"
	"image"
	"image/color"
	"math"
	"os"
	"path/filepath"
	"strings"
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
	if v, err := Metrics["detail"](Params{"a": orig, "b": orig, "channel": "chroma"}); err == nil {
		t.Errorf("an unknown channel gave %v, want an error", v)
	}
}

func TestDetailRGBMin(t *testing.T) {
	dir := t.TempDir()
	save := func(name string, c color.RGBA) (string, *image.RGBA64) {
		img := image.NewRGBA(image.Rect(0, 0, 16, 16))
		for y := 0; y < 16; y++ {
			for x := 0; x < 16; x++ {
				if x%4 == 1 {
					img.SetRGBA(x, y, c)
				} else {
					img.SetRGBA(x, y, color.RGBA{0, 0, 0, 255})
				}
			}
		}
		p := filepath.Join(dir, name)
		raster.SavePNG(p, img)
		rgba, err := raster.Load(p)
		if err != nil {
			t.Fatal(err)
		}
		return p, rgba
	}
	b, bi := save("b.png", color.RGBA{255, 0, 255, 255})
	a, ai := save("a.png", color.RGBA{255, 0, 96, 255})
	want, err := raster.DetailMin(ai, bi, 2)
	if err != nil || want >= 1 {
		t.Fatalf("DetailMin = %v, %v", want, err)
	}
	if v, err := Metrics["detail"](Params{"a": a, "b": b, "channel": "rgb-min"}); err != nil || v != want {
		t.Errorf("rgb-min = %v, %v; want raster.DetailMin's %v", v, err, want)
	}
	if v, err := Metrics["detail"](Params{"a": a, "b": b}); err != nil || v == want {
		t.Errorf("luma = %v, %v; want a different score from rgb-min's %v", v, err, want)
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

func TestJSONMetric(t *testing.T) {
	p := filepath.Join(t.TempDir(), "m.json")
	if err := os.WriteFile(p, []byte(`{"a":{"b":3,"s":"x"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if v, err := Metrics["json"](Params{"file": p, "field": "a.b"}); err != nil || v != 3 {
		t.Errorf("a.b = %v, %v", v, err)
	}
	for _, field := range []string{"a.c", "a.b.c", "a.s", "a", ""} {
		if v, err := Metrics["json"](Params{"file": p, "field": field}); err == nil {
			t.Errorf("field %q = %v, want an error", field, v)
		}
	}
}

func TestStepDriftMetric(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	a := write("a.json", `{"fps":30,"steps":[{"start_ms":0,"end_ms":1400},{"start_ms":1400,"end_ms":2800}]}`)
	b := write("b.json", `{"fps":30,"steps":[{"start_ms":0,"end_ms":1410},{"start_ms":1410,"end_ms":2860}]}`)
	short := write("c.json", `{"fps":30,"steps":[{"start_ms":0,"end_ms":1400}]}`)
	// The worst disagreement is step 2's end, 60 ms: 1.8 frames at 30 fps.
	if v, err := Metrics["step-drift"](Params{"a": a, "b": b}); err != nil || math.Abs(v-1.8) > 1e-9 {
		t.Errorf("step-drift = %v, %v; want 1.8", v, err)
	}
	if v, err := Metrics["step-drift"](Params{"a": a, "b": short}); err == nil || !strings.Contains(err.Error(), "same steps") {
		t.Errorf("step-drift over unequal steps = %v, %v; want a steps error", v, err)
	}
	nofps := write("d.json", `{"steps":[{"start_ms":0,"end_ms":1400},{"start_ms":1400,"end_ms":2800}]}`)
	if v, err := Metrics["step-drift"](Params{"a": nofps, "b": a}); err == nil || !strings.Contains(err.Error(), "no fps") {
		t.Errorf("step-drift without fps = %v, %v; want an fps error", v, err)
	}
}

// codeFrames writes one PNG per code into dir, prefix%05d.png: a 12-cell
// code strip (bit 0 leftmost, white for a 1) in the top half over grey.
func codeFrames(t *testing.T, dir, prefix string, codes []int) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for i, c := range codes {
		img := image.NewRGBA(image.Rect(0, 0, 120, 20))
		for y := range 20 {
			for x := range 120 {
				v := uint8(128)
				if y < 10 {
					v = 16
					if c>>(x/10)&1 == 1 {
						v = 235
					}
				}
				img.Set(x, y, color.RGBA{v, v, v, 255})
			}
		}
		if err := raster.SavePNG(filepath.Join(dir, fmt.Sprintf("%s%05d.png", prefix, i)), img); err != nil {
			t.Fatal(err)
		}
	}
	return filepath.Join(dir, prefix+"*.png")
}

func TestFrameCode(t *testing.T) {
	seq := func(a, b int) []int {
		var s []int
		for i := a; i <= b; i++ {
			s = append(s, i)
		}
		return s
	}
	ms := func(frames float64) float64 { return frames * 1000 / 60 }
	rows := []struct {
		name        string
		out, master []int
		start, want float64
	}{
		{"steady", []int{5, 6, 7, 8}, seq(5, 8), ms(5), 0},
		{"dup and drop", []int{5, 6, 6, 8}, seq(5, 8), ms(5), 2},
		{"vfr hold", []int{5, 5, 5, 8}, []int{5, 8}, ms(5), 0},
		{"anchored on time", []int{3, 3, 7, 8, 9}, append(seq(0, 3), seq(7, 10)...), ms(5), 0},
	}
	for _, r := range rows {
		t.Run(r.name, func(t *testing.T) {
			dir := t.TempDir()
			p := Params{"frames": codeFrames(t, dir, "o", r.out), "master": codeFrames(t, dir, "m", r.master),
				"y": 0.25, "master_y": 0.25, "start_ms": r.start}
			v, err := Metrics["frame-code"](p)
			if err != nil || v != r.want {
				t.Fatalf("frame-code = %v, %v; want %v", v, err, r.want)
			}
		})
	}
}

func TestFrameCodeSidecar(t *testing.T) {
	dir := t.TempDir()
	// The first frame and the last two are the loop close's: their codes are
	// blends.
	side := filepath.Join(dir, "clip.json")
	if err := os.WriteFile(side, []byte(`{"fps":60,"trim":{"start_ms":83.33333333333333,"end_ms":150},"loop_head_frames":1,"loop_tail_frames":2}`), 0o644); err != nil {
		t.Fatal(err)
	}
	p := Params{"frames": codeFrames(t, dir, "o", []int{0, 6, 7, 8, 0, 0}), "master": codeFrames(t, dir, "m", []int{5, 6, 7, 8, 9, 10}),
		"y": 0.25, "master_y": 0.25, "sidecar": side}
	if v, err := Metrics["frame-code"](p); err != nil || v != 0 {
		t.Fatalf("frame-code = %v, %v", v, err)
	}
	with := func(k string, v any) Params {
		q := Params{}
		for kk, vv := range p {
			q[kk] = vv
		}
		if v == nil {
			delete(q, k)
		} else {
			q[k] = v
		}
		return q
	}
	for name, q := range map[string]Params{
		"no start_ms or sidecar": with("sidecar", nil),
		"sidecar not a path":     with("sidecar", 3.0),
		"empty sidecar":          with("sidecar", ""),
		"sidecar and start_ms":   with("start_ms", 83.3),
		"sidecar and fps":        with("fps", 60.0),
	} {
		if _, err := Metrics["frame-code"](q); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// twoBlockFrames writes one 20x10 frame per pair of levels: the left half
// at the first, the right at the second, out of a ground of 16.
func twoBlockFrames(t *testing.T, dir string, levels [][2]uint8) string {
	t.Helper()
	for i, l := range levels {
		img := image.NewRGBA(image.Rect(0, 0, 20, 10))
		for y := range 10 {
			for x := range 20 {
				v := l[x/10]
				img.Set(x, y, color.RGBA{v, v, v, 255})
			}
		}
		if err := raster.SavePNG(filepath.Join(dir, fmt.Sprintf("f%05d.png", i)), img); err != nil {
			t.Fatal(err)
		}
	}
	return filepath.Join(dir, "f*.png")
}

func TestDoubleExposure(t *testing.T) {
	ab := Params{"a": "0,0,0.5,1", "b": "0.5,0,0.5,1", "ground": "#101010"}
	with := func(frames string) Params {
		q := Params{"frames": frames}
		for k, v := range ab {
			q[k] = v
		}
		return q
	}
	// A dip: the left block fades out to the ground, then the right fades in.
	dip := twoBlockFrames(t, t.TempDir(), [][2]uint8{{230, 16}, {120, 16}, {16, 16}, {16, 120}, {16, 230}})
	if v, err := Metrics["double-exposure"](with(dip)); err != nil || v != 0 {
		t.Fatalf("dip: double-exposure = %v, %v; want 0", v, err)
	}
	// A crossfade: the middle frame shows both at half strength.
	fade := twoBlockFrames(t, t.TempDir(), [][2]uint8{{230, 16}, {123, 123}, {16, 230}})
	v, err := Metrics["double-exposure"](with(fade))
	if want := (123.0 - 16) / 255; err != nil || math.Abs(v-want) > 1e-9 {
		t.Fatalf("crossfade: double-exposure = %v, %v; want %v", v, err, want)
	}
	for name, q := range map[string]Params{
		"no frames":  with(filepath.Join(t.TempDir(), "f*.png")),
		"bad region": {"frames": dip, "a": "0,0,2,1", "b": "0.5,0,0.5,1", "ground": "#101010"},
		"no ground":  {"frames": dip, "a": "0,0,0.5,1", "b": "0.5,0,0.5,1"},
	} {
		if _, err := Metrics["double-exposure"](q); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestFogra39RefusesUnknownParams(t *testing.T) {
	base := Params{"chart": "assets/FOGRA39.txt", "icc": "assets/Coated_Fogra39L_VIGC_300.icc", "mode": "absolute", "stat": "p95"}
	for k, v := range map[string]string{"mode": "proof", "stat": "median"} {
		p := Params{}
		for kk, vv := range base {
			p[kk] = vv
		}
		p[k] = v
		if _, err := Metrics["fogra39-de00"](p); err == nil || !strings.Contains(err.Error(), v) {
			t.Errorf("%s = %s: err %v", k, v, err)
		}
	}
}

func TestCountMetric(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	counts := write("counts.tsv", "frame\tdetected\tdensity\testimate\ncrowd\t90\t97.2\t95\nempty\t0\t0.4\t1\n")
	var hundred strings.Builder
	hundred.WriteString("x\ty\n")
	for i := range 100 {
		fmt.Fprintf(&hundred, "%d\t%d\n", i, i*2)
	}
	truth := write("crowd.points.tsv", hundred.String())
	none := write("empty.points.tsv", "x\ty\n")
	if v, err := Metrics["count"](Params{"tsv": counts, "truth": truth, "frame": "crowd"}); err != nil || math.Abs(v-0.05) > 1e-9 {
		t.Errorf("95 against 100 = %v, %v; want 0.05", v, err)
	}
	if v, err := Metrics["count"](Params{"tsv": counts, "truth": none, "frame": "empty"}); err != nil || v != 1 {
		t.Errorf("1 against 0 = %v, %v; want 1", v, err)
	}
	if _, err := Metrics["count"](Params{"tsv": counts, "truth": truth, "frame": "nobody"}); err == nil || !strings.Contains(err.Error(), "nobody") {
		t.Errorf("unknown frame: err = %v, want one naming it", err)
	}
	noEstimate := write("old.tsv", "frame\tdetected\ncrowd\t90\n")
	if _, err := Metrics["count"](Params{"tsv": noEstimate, "truth": truth, "frame": "crowd"}); err == nil || !strings.Contains(err.Error(), "estimate") {
		t.Errorf("no estimate column: err = %v, want one naming it", err)
	}
	for _, est := range []string{"NaN", "Inf", "-Inf", "-1"} {
		bad := write("bad.tsv", "frame\tdetected\tdensity\testimate\ncrowd\t1\t1\t"+est+"\n")
		if _, err := Metrics["count"](Params{"tsv": bad, "truth": truth, "frame": "crowd"}); err == nil || !strings.Contains(err.Error(), "crowd") {
			t.Errorf("estimate %s: err = %v, want one naming the frame", est, err)
		}
	}
	for _, header := range []string{"", "y\tx\n", "x\n", "x\ty\tz\n"} {
		p := write("h.points.tsv", header+"1\t2\n")
		if _, err := Metrics["count"](Params{"tsv": counts, "truth": p, "frame": "crowd"}); err == nil || !strings.Contains(err.Error(), "header") {
			t.Errorf("truth header %q: err = %v, want a header error", header, err)
		}
	}
}

func TestPointsMetric(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	truth := write("crowd.points.tsv", "x\ty\n100\t100\n200\t100\n500\t500\n")
	// Two near the first marked person (one matches), one near the second,
	// one far from everyone; the third marked person is missed.
	got := write("crowd.tsv", "x\ty\tfrom\n105\t100\tdetector\n110\t100\tdensity\n200\t108\tdetector\n900\t900\tdensity\n")
	if v, err := Metrics["points"](Params{"points": got, "truth": truth, "tol": 20.0}); err != nil || math.Abs(v-1) > 1e-9 {
		t.Errorf("2 false + 1 missed over 3 = %v, %v; want 1", v, err)
	}
	if v, err := Metrics["points"](Params{"points": got, "truth": truth, "tol": 4.0}); err != nil || math.Abs(v-7.0/3) > 1e-9 {
		t.Errorf("tol 4, nothing matched: %v, %v; want (4 false + 3 missed) / 3", v, err)
	}

	frame := filepath.Join(dir, "pano.png")
	raster.SavePNG(frame, image.NewRGBA(image.Rect(0, 0, 400, 200)))
	// (200,100) is lon 0 lat 0; (300,50) lon 90 lat 45; (0,100) lon -180 lat 0.
	eqTruth := write("pano.points.tsv", "x\ty\n200\t100\n300\t50\n0\t100\n")
	eqGot := write("pano.tsv", "lon\tlat\tfrom\n0.5\t0.2\tdetector\n90\t44\tdetector\n179.5\t0\tdensity\n-90\t0\tdensity\n")
	if v, err := Metrics["points"](Params{"points": eqGot, "truth": eqTruth, "tol": 2.0, "image": frame}); err != nil || math.Abs(v-1.0/3) > 1e-9 {
		t.Errorf("equirect: 1 false over 3 (across the seam too) = %v, %v; want 1/3", v, err)
	}
	if _, err := Metrics["points"](Params{"points": eqGot, "truth": eqTruth, "tol": 2.0}); err == nil || !strings.Contains(err.Error(), "image") {
		t.Errorf("lon/lat without image: err = %v", err)
	}
	if _, err := Metrics["points"](Params{"points": got, "truth": truth}); err == nil || !strings.Contains(err.Error(), "tol") {
		t.Errorf("no tol: err = %v", err)
	}
	none := write("empty.points.tsv", "x\ty\n")
	if v, err := Metrics["points"](Params{"points": write("e.tsv", "x\ty\tfrom\n1\t1\tdensity\n"), "truth": none, "tol": 5.0}); err != nil || v != 1 {
		t.Errorf("1 against no one = %v, %v; want 1", v, err)
	}
}

func TestRangeMetric(t *testing.T) {
	dir := t.TempDir()
	write := func(body string) string {
		p := filepath.Join(dir, "distinct.tsv")
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	head := "low\thigh\ttracks\tmin_seconds\tuntracked_share\n"
	for _, c := range []struct {
		low, high string
		want      float64
	}{{"8", "12", 4}, {"10", "10", 0}, {"10", "13", 3}, {"7", "10", 3}} {
		p := write(head + c.low + "\t" + c.high + "\t15\t3\t0.1\n")
		if v, err := Metrics["range"](Params{"tsv": p, "truth": int64(10)}); err != nil || v != c.want {
			t.Errorf("%s to %s around 10 = %v, %v; want %v", c.low, c.high, v, err, c.want)
		}
	}
	for _, r := range [][2]string{{"11", "14"}, {"5", "9"}} {
		p := write(head + r[0] + "\t" + r[1] + "\t15\t3\t0.1\n")
		if _, err := Metrics["range"](Params{"tsv": p, "truth": 10.0}); err == nil || !strings.Contains(err.Error(), "outside") {
			t.Errorf("%s to %s misses 10: err %v", r[0], r[1], err)
		}
	}
	for name, body := range map[string]string{
		"no row":       head,
		"no high":      "low\ttracks\n1\t2\n",
		"not a number": head + "x\t12\t15\t3\t0.1\n",
		"low > high":   head + "12\t8\t15\t3\t0.1\n",
		"two rows":     head + "8\t12\t15\t3\t0.1\n8\t12\t15\t3\t0.1\n",
	} {
		if _, err := Metrics["range"](Params{"tsv": write(body), "truth": 10.0}); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
	if _, err := Metrics["range"](Params{"tsv": write(head + "8\t12\t15\t3\t0.1\n")}); err == nil || !strings.Contains(err.Error(), "truth") {
		t.Errorf("no truth: err %v", err)
	}
}
