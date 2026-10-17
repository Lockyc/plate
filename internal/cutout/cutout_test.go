package cutout

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/lockyc/plate/internal/enginetest"
	"github.com/lockyc/plate/internal/pins"
	"github.com/lockyc/plate/internal/raster"
)

// magick: the frame.Probe call answers $SRC_OPAQUE and $SRC_DIMS (WxH); any
// other call copies $FIXTURE_FRAME to its last argument, less a
// PNG24:/PNG32: prefix.
const magickStub = `case "$*" in
  *info:*) d=${SRC_DIMS:-800x1200}; printf '%s %s %s sRGB\n' "${SRC_OPAQUE:-True}" "${d%x*}" "${d#*x}" ;;
  *) for a in "$@"; do last=$a; done; last=${last#PNG24:}; cp "$FIXTURE_FRAME" "${last#PNG32:}" ;;
esac`

// uv: birefnet.py copies $FIXTURE_MASK to its 2nd argument; matte.py writes
// $FIXTURE_FRAME to --out, or fails with $MATTE_FAIL.
const uvStub = `script=$4; shift 4
case "$script" in
  *birefnet.py) cp "$FIXTURE_MASK" "$2" ;;
  *matte.py)
    [ -n "$MATTE_FAIL" ] && { echo "$MATTE_FAIL" >&2; exit 1; }
    while [ "$1" != "--out" ]; do shift; done; cp "$FIXTURE_FRAME" "$2" ;;
esac`

func png(t *testing.T, fill color.Color) string {
	return pngSized(t, fill, 8, 12)
}

func pngSized(t *testing.T, fill color.Color, w, h int) string {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, fill)
		}
	}
	p := filepath.Join(t.TempDir(), "f.png")
	if err := raster.SavePNG(p, img); err != nil {
		t.Fatal(err)
	}
	return p
}

type env struct {
	magick, uv, in, out string
	stdout, stderr      bytes.Buffer
}

func setup(t *testing.T) *env {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	e := &env{magick: enginetest.Stub(t, "magick", magickStub), uv: enginetest.Stub(t, "uv", uvStub)}
	t.Setenv("FIXTURE_FRAME", png(t, color.RGBA{200, 150, 100, 255}))
	t.Setenv("FIXTURE_MASK", png(t, color.White))
	t.Setenv("SRC_DIMS", "800x1200")
	t.Setenv("MATTE_FAIL", "")
	dir := t.TempDir()
	e.in, e.out = filepath.Join(dir, "in.jpg"), filepath.Join(dir, "out.png")
	if err := os.WriteFile(e.in, []byte("jpg"), 0o644); err != nil {
		t.Fatal(err)
	}
	return e
}

func (e *env) run(args ...string) int {
	return Main(context.Background(), append(args, e.in, e.out), &e.stdout, &e.stderr)
}

func TestCutoutBirefnet(t *testing.T) {
	e := setup(t)
	t.Setenv("FIXTURE_FRAME", pngSized(t, color.RGBA{200, 150, 100, 255}, 400, 600))
	if code := e.run("--coarse", "birefnet", "--height", "600"); code != 0 {
		t.Fatalf("code %d: %s", code, e.stderr.String())
	}
	matte := matteCall(t, e)
	wantFlags(t, matte, map[string]string{"--model": pins.ViTMatte.Repo, "--revision": pins.ViTMatte.Revision, "--tile": "1024", "--overlap": strconv.Itoa(overlap), "--context-width": "400", "--context-height": "600", "--band-in": "12", "--band-out": "40"})
	for _, a := range matte {
		if strings.HasPrefix(a, "--despill") {
			t.Errorf("despill ran without being asked for: %q", matte)
		}
	}
}

// matteCall returns the arguments of the last uv call, the matte script's.
func matteCall(t *testing.T, e *env) []string {
	t.Helper()
	calls := enginetest.Calls(t, e.uv)
	if len(calls) == 0 {
		t.Fatal("no uv call")
	}
	return calls[len(calls)-1]
}

// wantFlags checks that each flag is followed by its value.
func wantFlags(t *testing.T, args []string, want map[string]string) {
	t.Helper()
	for k, v := range want {
		if i := slices.Index(args, k); i < 0 || i+1 >= len(args) || args[i+1] != v {
			t.Errorf("%s: want %s in %q", k, v, args)
		}
	}
}

func TestContextSize(t *testing.T) {
	for _, c := range []struct{ w, h, cw, ch int }{
		{400, 600, 400, 600},     // under the bound: never upsampled
		{1024, 1024, 1024, 1024}, // exactly the bound
		{2400, 2206, 1068, 981},  // portrait-ivy.jpg
		{3000, 1000, 1773, 591},  // 3:1 panorama
		{4000, 200, 4000, 200},   // short wide strip under the bound
		{20000, 200, 10240, 102}, // long strip over it
	} {
		cw, ch := contextSize(c.w, c.h)
		if cw != c.cw || ch != c.ch {
			t.Errorf("contextSize(%d, %d) = %dx%d, want %dx%d", c.w, c.h, cw, ch, c.cw, c.ch)
		}
		if cw*ch > contextSide*contextSide {
			t.Errorf("contextSize(%d, %d) = %dx%d, over %d² px", c.w, c.h, cw, ch, contextSide)
		}
	}
}

// The frame drops alpha, so whatever colour a transparent source stores
// under its transparency would be cut out as picture.
func TestCutoutRefusesTransparentSource(t *testing.T) {
	e := setup(t)
	t.Setenv("SRC_OPAQUE", "False")
	if code := e.run("--coarse", "birefnet"); code != 1 || !strings.Contains(e.stderr.String(), "transparent pixels") {
		t.Fatalf("code %d: %s", code, e.stderr.String())
	}
}

func TestCutoutWideFrameContext(t *testing.T) {
	e := setup(t)
	t.Setenv("SRC_DIMS", "3000x1000")
	t.Setenv("FIXTURE_FRAME", pngSized(t, color.RGBA{200, 150, 100, 255}, 3000, 1000))
	if code := e.run("--coarse", "birefnet"); code != 0 {
		t.Fatalf("code %d: %s", code, e.stderr.String())
	}
	wantFlags(t, matteCall(t, e), map[string]string{"--context-width": "1773", "--context-height": "591"})
}

// TestCutoutContextReadsFrame: the context pass is sized from the frame
// magick wrote, not from an estimate of its width, which rounding can miss.
func TestCutoutContextReadsFrame(t *testing.T) {
	e := setup(t)
	t.Setenv("SRC_DIMS", "1001x1000")
	t.Setenv("FIXTURE_FRAME", pngSized(t, color.RGBA{200, 150, 100, 255}, 599, 600))
	if code := e.run("--coarse", "birefnet", "--height", "600"); code != 0 {
		t.Fatalf("code %d: %s", code, e.stderr.String())
	}
	wantFlags(t, matteCall(t, e), map[string]string{"--context-width": "599", "--context-height": "600"})
}

func TestWholeFrameTile(t *testing.T) {
	e := setup(t)
	if code := e.run("--coarse", "birefnet", "--tile", "0"); code != 0 {
		t.Fatalf("code %d: %s", code, e.stderr.String())
	}
	wantFlags(t, matteCall(t, e), map[string]string{"--tile": "0"})
}

func TestTileAtOrBelowOverlapRefused(t *testing.T) {
	for _, tile := range []string{"-1", "100", strconv.Itoa(overlap)} {
		e := setup(t)
		if code := e.run("--coarse", "birefnet", "--tile", tile); code != 2 {
			t.Errorf("--tile %s: code %d", tile, code)
		}
		if !strings.Contains(e.stderr.String(), strconv.Itoa(overlap)) {
			t.Errorf("--tile %s: stderr %q does not name the overlap", tile, e.stderr.String())
		}
		if c := enginetest.Calls(t, e.uv); len(c) != 0 {
			t.Errorf("--tile %s: uv ran: %q", tile, c)
		}
	}
}

func TestCustomDespill(t *testing.T) {
	e := setup(t)
	want := map[string]string{"--despill-hue": "60:90", "--despill-clean": "10:50", "--despill-chroma": "5:30", "--despill-lmax": "85", "--despill-hue-end": "190"}
	args := []string{"--coarse", "birefnet"}
	for k, v := range want {
		args = append(args, k, v)
	}
	if code := e.run(args...); code != 0 {
		t.Fatalf("code %d: %s", code, e.stderr.String())
	}
	wantFlags(t, matteCall(t, e), want)
}

func TestPartialCustomDespillRefused(t *testing.T) {
	for _, args := range [][]string{
		{"--despill-hue", "60:90"},
		{"--despill-lmax", "80"},
		{"--despill-hue", "60:90", "--despill-clean", "10:50", "--despill-chroma", "5:30", "--despill-lmax", "85"},
	} {
		e := setup(t)
		if code := e.run(append([]string{"--coarse", "birefnet"}, args...)...); code != 2 {
			t.Errorf("%q: code %d", args, code)
		}
		if c := enginetest.Calls(t, e.uv); len(c) != 0 {
			t.Errorf("%q: uv ran: %q", args, c)
		}
	}
}

func TestInvalidValuesRefused(t *testing.T) {
	for _, args := range [][]string{
		{"--band-in", "0"}, {"--band-out", "0"}, {"--height", "-1"},
		{"--despill", "warm-on-green", "--despill-lmax", "x"},
		{"--despill", "warm-on-green", "--despill-hue-end", "x"},
		{"--despill", "warm-on-green", "--despill-hue", "100:70"},
		{"--despill", "warm-on-green", "--despill-clean", "50:50"},
		{"--despill", "warm-on-green", "--despill-lmax", "nan"},
		{"--despill", "warm-on-green", "--despill-hue-end", "inf"},
		{"--despill", "warm-on-green", "--despill-hue", "-inf:100"},
		{"--despill", "warm-on-green", "--despill-chroma", "6:+Inf"},
	} {
		e := setup(t)
		if code := e.run(append([]string{"--coarse", "birefnet"}, args...)...); code != 2 {
			t.Errorf("%q: code %d", args, code)
		}
	}
}

func TestFrameOrder(t *testing.T) {
	e := setup(t)
	e.run("--coarse", "birefnet", "--height", "600")
	var frame []string
	for _, c := range enginetest.Calls(t, e.magick) {
		if slices.Contains(c, "-strip") {
			frame = c
		}
	}
	idx := func(s string) int { return slices.Index(frame, s) }
	if !(idx("-auto-orient") < idx("-profile") && idx("-profile") < idx("-strip") && idx("-strip") < idx("-resize")) {
		t.Errorf("frame args out of order: %q", frame)
	}
	if !slices.Contains(frame, "x600") {
		t.Errorf("no resize to the working height: %q", frame)
	}
	if first := enginetest.Calls(t, e.magick)[0]; slices.Index(first, "-auto-orient") < 0 {
		t.Errorf("dimensions read before orienting: %q", first)
	}
}

func TestCutoutRefusesUpsample(t *testing.T) {
	e := setup(t)
	if code := e.run("--coarse", "birefnet", "--height", "1300"); code != 1 {
		t.Fatalf("code %d", code)
	}
	if !strings.Contains(e.stderr.String(), "upsample") {
		t.Errorf("stderr %q", e.stderr.String())
	}
	if len(enginetest.Calls(t, e.uv)) != 0 {
		t.Error("a model ran for a refused cut-out")
	}
}

func TestCutoutNoForeground(t *testing.T) {
	e := setup(t)
	t.Setenv("FIXTURE_MASK", png(t, color.Black))
	if code := e.run("--coarse", "birefnet"); code != 1 || !strings.Contains(e.stderr.String(), "no foreground") || !strings.Contains(e.stderr.String(), "try --coarse vision") {
		t.Fatalf("code %d, stderr %q", code, e.stderr.String())
	}
}

func TestCutoutScriptFailureLeavesNothing(t *testing.T) {
	e := setup(t)
	os.WriteFile(e.out, []byte("stale"), 0o644)
	t.Setenv("MATTE_FAIL", "OSError: couldn't connect to huggingface.co")
	if code := e.run("--coarse", "birefnet"); code != 1 {
		t.Fatalf("code %d", code)
	}
	if !strings.Contains(e.stderr.String(), "uv") || !strings.Contains(e.stderr.String(), "huggingface") {
		t.Errorf("stderr %q", e.stderr.String())
	}
	if _, err := os.Stat(e.out); err == nil {
		t.Error("output left behind")
	}
}

func TestDespillPreset(t *testing.T) {
	e := setup(t)
	if code := e.run("--coarse", "birefnet", "--despill", "warm-on-green", "--despill-lmax", "80"); code != 0 {
		t.Fatalf("code %d: %s", code, e.stderr.String())
	}
	wantFlags(t, matteCall(t, e), map[string]string{"--despill-hue": "70:100", "--despill-hue-end": "200", "--despill-chroma": "6:40", "--despill-lmax": "80", "--despill-clean": "15:65"})
}

func TestCutoutUsage(t *testing.T) {
	e := setup(t)
	for _, args := range [][]string{{"--coarse", "sam"}, {"--despill", "nope"}, {"--despill-hue", "70"}} {
		e.stderr.Reset()
		if code := e.run(args...); code != 2 {
			t.Errorf("%q: code %d", args, code)
		}
	}
	var o, errb bytes.Buffer
	if code := Main(context.Background(), []string{e.in, filepath.Join(t.TempDir(), "o.webp")}, &o, &errb); code != 2 {
		t.Errorf("webp out: code %d", code)
	}
}

func TestInputEqualToOutputIsRefused(t *testing.T) {
	e := setup(t)
	same := filepath.Join(t.TempDir(), "in.png")
	if err := os.WriteFile(same, []byte("png"), 0o644); err != nil {
		t.Fatal(err)
	}
	var o, errb bytes.Buffer
	if code := Main(context.Background(), []string{"--coarse", "birefnet", same, same}, &o, &errb); code != 1 {
		t.Fatalf("code %d, stderr %q", code, errb.String())
	}
	if b, err := os.ReadFile(same); err != nil || string(b) != "png" {
		t.Errorf("source did not survive: %q %v", b, err)
	}
	if c := enginetest.Calls(t, e.uv); len(c) != 0 {
		t.Errorf("a model ran for a refused cut-out: %q", c)
	}
}

// maskFile writes a w×h grey mask whose value at (x, y) is v(x, y).
func maskFile(t *testing.T, w, h int, v func(x, y int) uint8) string {
	t.Helper()
	img := image.NewGray(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.SetGray(x, y, color.Gray{v(x, y)})
		}
	}
	p := filepath.Join(t.TempDir(), "mask.png")
	if err := raster.SavePNG(p, img); err != nil {
		t.Fatal(err)
	}
	return p
}

// disc is a disc of radius r at (cx, cy) with a soft band of width soft.
func disc(cx, cy, r, soft int) func(x, y int) uint8 {
	return func(x, y int) uint8 {
		d := (x-cx)*(x-cx) + (y-cy)*(y-cy)
		switch {
		case d < r*r:
			return 255
		case d < (r+soft)*(r+soft):
			return 128
		}
		return 0
	}
}

// strip is a 1000×20 mask: 100 confident columns, then soft uncertain ones,
// so its band is exactly soft px, soft/10 % of the long side.
func strip(soft int) func(x, y int) uint8 {
	return func(x, y int) uint8 {
		switch {
		case x < 100:
			return 255
		case x < 100+soft:
			return 128
		}
		return 0
	}
}

func TestHasForeground(t *testing.T) {
	for _, c := range []struct {
		name string
		w, h int
		v    func(x, y int) uint8
		want string // "" = a subject
	}{
		{"hard disc", 100, 100, disc(50, 50, 30, 0), ""},
		{"soft-edged disc", 100, 100, disc(50, 50, 30, 2), ""},
		// 3% of the frame with a 3 px soft edge: 14% of its claimed pixels
		// are uncertain, yet the band is 0.75% of the frame.
		{"small disc", 400, 400, disc(200, 200, 39, 3), ""},
		{"band just under the limit", 1000, 20, strip(24), ""},
		{"band just over the limit", 1000, 20, strip(26), "2.6% of the frame"},
		{"empty", 100, 100, func(x, y int) uint8 { return 0 }, "empty"},
		{"uniform half", 100, 100, func(x, y int) uint8 { return 128 }, "clearly subject"},
		{"smooth ramp", 100, 100, func(x, y int) uint8 { return uint8(x * 255 / 99) }, "band"},
		{"stray confident pixel in haze", 100, 100, func(x, y int) uint8 {
			if x == 50 && y == 50 {
				return 255
			}
			return 40
		}, "band"},
	} {
		t.Run(c.name, func(t *testing.T) {
			err := hasForeground(maskFile(t, c.w, c.h, c.v))
			if c.want == "" {
				if err != nil {
					t.Fatalf("subject refused: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), "no foreground found") || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("want a refusal containing %q, got %v", c.want, err)
			}
		})
	}
}
