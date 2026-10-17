package infill

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/color"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/lockyc/plate/internal/enginetest"
	"github.com/lockyc/plate/internal/frame"
	"github.com/lockyc/plate/internal/raster"
)

func pngOf(t *testing.T, dir, name string, w, h int) string {
	p := filepath.Join(dir, name)
	raster.SavePNG(p, image.NewRGBA(image.Rect(0, 0, w, h)))
	return p
}

// holedPNG writes a w x h grey image with faint grain and a transparent
// square in the middle, and with band a dark band over the bottom quarter: what
// the magick stub hands back for every call, so the Go steps have a hole and
// a ground to work on.
func holedPNG(t *testing.T, dir string, w, h int, band bool) string {
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			if x < w/2-5 || x >= w/2+5 || y < h/2-5 || y >= h/2+5 {
				v := uint8(120 + (x*7+y*13)%5)
				if band && y >= 3*h/4 {
					v = 20
				}
				img.Set(x, y, color.NRGBA{v, v, v, 255})
			}
		}
	}
	p := filepath.Join(dir, "fixture.png")
	if err := raster.SavePNG(p, img); err != nil {
		t.Fatal(err)
	}
	return p
}

// calls is the stub's log less the opacity checks, which every run makes
// first.
func calls(t *testing.T, log string) [][]string {
	return slices.DeleteFunc(enginetest.Calls(t, log), func(c []string) bool { return slices.Equal(c, frame.ProbeArgs(c[0])) })
}

func TestInfillRefusesTransparentSource(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	t.Setenv("OPAQUE", "False")
	log := enginetest.Stub(t, "magick", orientStub)
	var o, e bytes.Buffer
	if code := Main(context.Background(), []string{"--mask", pngOf(t, dir, "m.png", 40, 30), pngOf(t, dir, "in.png", 40, 30), filepath.Join(dir, "out.png")}, &o, &e); code != 1 || !strings.Contains(e.String(), "transparent") {
		t.Errorf("code %d, %q", code, e.String())
	}
	if n := len(calls(t, log)); n != 0 {
		t.Errorf("%d magick calls after the refusal", n)
	}
}

// The PNG result cannot hold CMYK, and converting without the profile
// shifts every colour.
func TestInfillRefusesCMYK(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	t.Setenv("COLORSPACE", "CMYK")
	log := enginetest.Stub(t, "magick", orientStub)
	var o, e bytes.Buffer
	if code := Main(context.Background(), []string{"--mask", pngOf(t, dir, "m.png", 40, 30), pngOf(t, dir, "in.png", 40, 30), filepath.Join(dir, "out.png")}, &o, &e); code != 1 || !strings.Contains(e.String(), "CMYK") {
		t.Errorf("code %d, %q", code, e.String())
	}
	if n := len(calls(t, log)); n != 0 {
		t.Errorf("%d magick calls after the refusal", n)
	}
}

func TestInfillRefusesAnUnreachedHole(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	t.Setenv("FIXTURE", holedPNG(t, dir, 80, 60, false))
	t.Setenv("REACH", "0")
	enginetest.Stub(t, "magick", `case "$*" in *"%[opaque]"*) echo "True 1 1 sRGB"; exit 0 ;; esac
case "$*" in *fx:minima*) echo "$REACH"; exit 0 ;; esac
for a in "$@"; do last=$a; done; cp "$FIXTURE" "$last"`)
	out := filepath.Join(dir, "out.png")
	var o, e bytes.Buffer
	if code := Main(context.Background(), []string{"--mask", pngOf(t, dir, "m.png", 80, 60), "--levels", "30,6", pngOf(t, dir, "in.png", 80, 60), out}, &o, &e); code != 1 || !strings.Contains(e.String(), "coarser than 30") {
		t.Fatalf("code %d, %q", code, e.String())
	}
	if _, err := os.Stat(out); !os.IsNotExist(err) {
		t.Error("an unfilled result was written")
	}
}

func TestInfill(t *testing.T) {
	for _, c := range []struct {
		grain string
		band  bool
	}{{"1", false}, {"0", false}, {"1", true}} {
		t.Run(fmt.Sprintf("grain %s band %v", c.grain, c.band), func(t *testing.T) {
			dir := t.TempDir()
			t.Chdir(dir)
			t.Setenv("FIXTURE", holedPNG(t, dir, 80, 60, c.band))
			log := enginetest.Stub(t, "magick", `case "$*" in *"%[opaque]"*) echo "${OPAQUE:-True} 1 1 ${COLORSPACE:-sRGB}"; exit 0 ;; esac
case "$*" in *fx:minima*) echo "${REACH:-1}"; exit 0 ;; esac
for a in "$@"; do last=$a; done; cp "$FIXTURE" "$last"`)
			in, mask, out := pngOf(t, dir, "in.png", 80, 60), pngOf(t, dir, "m.png", 80, 60), filepath.Join(dir, "out.png")
			var o, e bytes.Buffer
			if code := Main(context.Background(), []string{"--mask", mask, "--levels", "30,6", "--grain", c.grain, in, out}, &o, &e); code != 0 {
				t.Fatalf("code %d: %s", code, e.String())
			}
			calls := calls(t, log)
			// The image and mask are oriented as displayed, nothing else,
			// and the holes are cut from those copies.
			if len(calls) < 3 || !slices.Equal(calls[0][:2], []string{in, "-auto-orient"}) || !slices.Equal(calls[1][:2], []string{mask, "-auto-orient"}) ||
				calls[2][0] != calls[0][2] || calls[2][2] != calls[1][2] {
				t.Fatalf("want the image and mask oriented, then the holes cut from them: %q", calls)
			}
			calls = calls[2:]
			blurSrc := "holes.png"
			if c.band { // holes, edge stops, 2 blurs, fill, reach check, composite
				if len(calls) != 7 || filepath.Base(calls[1][len(calls[1])-1]) != "reached.png" {
					t.Fatalf("want an edge-stop call writing reached.png: %q", calls)
				}
				calls = append(calls[:1], calls[2:]...)
				blurSrc = "reached.png"
			}
			if len(calls) != 6 { // holes, 2 blurs, fill, reach check, composite
				t.Fatalf("%d magick calls, want 6: %q", len(calls), calls)
			}
			if !slices.Contains(calls[1], "0x30") || !slices.Contains(calls[2], "0x6") {
				t.Errorf("blur levels: %q %q", calls[1], calls[2])
			}
			if filepath.Base(calls[1][0]) != blurSrc || filepath.Base(calls[2][0]) != blurSrc {
				t.Errorf("blurs read %q and %q, want %s", calls[1][0], calls[2][0], blurSrc)
			}
			if slices.Contains(calls[3], "+noise") {
				t.Errorf("fill adds magick noise: %q", calls[3])
			}
			if !slices.Contains(calls[4], "%[fx:minima]") {
				t.Errorf("no reach check after the fill: %q", calls[4])
			}
			comp := calls[5]
			if filepath.Base(comp[0]) != "holes.png" || filepath.Base(comp[2]) != "fill.png" || comp[len(comp)-1] != out {
				t.Errorf("composite takes %q, want holes.png, the fill, then %s", comp, out)
			}
			pieces := slices.ContainsFunc(comp, func(a string) bool { return strings.HasPrefix(filepath.Base(a), "grain") })
			if pieces != (c.grain == "1") {
				t.Errorf("grain %s: composite has grained pieces = %v: %q", c.grain, pieces, comp)
			}
		})
	}
}

// orientStub plays magick: an -auto-orient call copies its input, or the
// image at $ROT for in.png when ROT is set, playing a source whose EXIF
// rotation turns it; the probe answers $OPAQUE and $COLORSPACE; any other call fails with
// status 3.
const orientStub = `case "$*" in *"%[opaque]"*) echo "${OPAQUE:-True} 1 1 ${COLORSPACE:-sRGB}"; exit 0 ;; esac
for a in "$@"; do last=$a; done
[ "$2" = -auto-orient ] || exit 3
src=$1; [ -n "$ROT" ] && [ "$(basename "$1")" = in.png ] && src=$ROT
cp "$src" "$last"`

// TestInfillRotatedSource: a mask drawn on the displayed image of a phone
// JPEG stored on its side passes the size check.
func TestInfillRotatedSource(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	t.Setenv("ROT", pngOf(t, dir, "rot.png", 30, 40))
	log := enginetest.Stub(t, "magick", orientStub)
	in, mask := pngOf(t, dir, "in.png", 40, 30), pngOf(t, dir, "m.png", 30, 40)
	var o, e bytes.Buffer
	Main(context.Background(), []string{"--mask", mask, in, filepath.Join(dir, "out.png")}, &o, &e)
	if strings.Contains(e.String(), "the mask is") || len(calls(t, log)) != 3 {
		t.Errorf("rotated source refused before the holes: %q", e.String())
	}
}

func TestInfillRefuses(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	log := enginetest.Stub(t, "magick", orientStub)
	in, out := pngOf(t, dir, "in.png", 40, 30), filepath.Join(dir, "out.png")
	var o, e bytes.Buffer
	if code := Main(context.Background(), []string{"--mask", pngOf(t, dir, "m.png", 41, 30), in, out}, &o, &e); code != 1 || !strings.Contains(e.String(), "41x30") {
		t.Errorf("mismatched mask: code %d, %q", code, e.String())
	}
	if n := len(calls(t, log)); n != 2 {
		t.Errorf("%d magick calls for a mismatched mask, want the two orients", n)
	}
	if code := Main(context.Background(), []string{"--mask", pngOf(t, dir, "m2.png", 40, 30), "--levels", "6,20", in, out}, &o, &e); code != 2 {
		t.Errorf("ascending levels: code %d", code)
	}
	if n := len(calls(t, log)); n != 2 {
		t.Errorf("magick ran %d times, want only the mismatched mask's two orients", n)
	}
}

func TestInfillRefusesSameFile(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	log := enginetest.Stub(t, "magick", `exit 0`)
	in, mask := pngOf(t, dir, "in.png", 40, 30), pngOf(t, dir, "m.png", 40, 30)
	before, _ := os.ReadFile(in)
	mbefore, _ := os.ReadFile(mask)
	for name, args := range map[string][]string{
		"in==out":   {"--mask", mask, in, in},
		"mask==out": {"--mask", mask, in, mask},
	} {
		var o, e bytes.Buffer
		if code := Main(context.Background(), args, &o, &e); code != 1 || !strings.Contains(e.String(), "also an input") {
			t.Errorf("%s: code %d, %q", name, code, e.String())
		}
	}
	if after, _ := os.ReadFile(in); !bytes.Equal(before, after) {
		t.Error("source changed")
	}
	if after, _ := os.ReadFile(mask); !bytes.Equal(mbefore, after) {
		t.Error("mask changed")
	}
	if len(calls(t, log)) != 0 {
		t.Error("magick ran")
	}
}

func TestInfillRefusesNegativeGrain(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	log := enginetest.Stub(t, "magick", `exit 0`)
	in, mask := pngOf(t, dir, "in.png", 40, 30), pngOf(t, dir, "m.png", 40, 30)
	var o, e bytes.Buffer
	if code := Main(context.Background(), []string{"--mask", mask, "--grain", "-0.5", in, filepath.Join(dir, "out.png")}, &o, &e); code != 2 || !strings.Contains(e.String(), "--grain of 0 or more") {
		t.Errorf("code %d, %q", code, e.String())
	}
	if len(calls(t, log)) != 0 {
		t.Error("magick ran")
	}
}
