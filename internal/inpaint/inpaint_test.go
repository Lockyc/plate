package inpaint

import (
	"bytes"
	"context"
	"image"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/lockyc/plate/internal/enginetest"
	"github.com/lockyc/plate/internal/raster"
)

func pngOf(t *testing.T, dir, name string, w, h int) string {
	p := filepath.Join(dir, name)
	raster.SavePNG(p, image.NewRGBA(image.Rect(0, 0, w, h)))
	return p
}

// magick copies its first argument to its last, less a PNG24: prefix, or
// the image at $ROT for the image's frame call when ROT is set, playing a source
// whose EXIF rotation turns it; uv plays iopaint, writing the image into
// --output under its own name, and answers the device probe with mps.
func stubs(t *testing.T) (magick, uv string) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	return enginetest.Stub(t, "magick", `case "$*" in *"%[opaque]"*) echo "${OPAQUE:-True} 1 1 sRGB"; exit 0 ;; esac
for a in "$@"; do last=$a; done
src=$1; [ -n "$ROT" ] && [ "$(basename "$1")" = in.png ] && src=$ROT
cp "$src" "${last#PNG24:}"`), enginetest.Stub(t, "uv", `[ "$7" = python ] && { echo mps; exit 0; }
img=; out=
for a in "$@"; do case "$a" in --image=*) img=${a#--image=};; --output=*) out=${a#--output=};; esac; done
cp "$img" "$out/$(basename "$img")"`)
}

func TestInpaint(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	magick, log := stubs(t)
	in, mask, out := pngOf(t, dir, "in.png", 40, 30), pngOf(t, dir, "m.png", 40, 30), filepath.Join(dir, "out.png")
	var o, e bytes.Buffer
	if code := Main(context.Background(), []string{"--mask", mask, in, out}, &o, &e); code != 0 {
		t.Fatalf("code %d: %s", code, e.String())
	}
	m := enginetest.Calls(t, magick)
	if len(m) != 4 || m[0][len(m[0])-1] != "info:" {
		t.Fatalf("%d magick calls, want the opacity check, frame, mask, output: %q", len(m), m)
	}
	m = m[1:]
	if len(m) != 3 {
		t.Fatalf("%d magick calls, want frame, mask, output: %q", len(m), m)
	}
	// The image and the mask are both opaque 8-bit frames: iopaint reads
	// the mask through PIL's convert("L"), which clips a 16-bit grey PNG
	// to white rather than scaling it.
	frame := strings.TrimPrefix(m[0][len(m[0])-1], "PNG24:")
	idx := func(s string) int { return slices.Index(m[0], s) }
	if m[0][0] != in || !(0 < idx("-auto-orient") && idx("-auto-orient") < idx("-profile") && idx("-profile") < idx("-strip") && idx("-strip") < idx("off")) || !strings.HasPrefix(m[0][len(m[0])-1], "PNG24:") {
		t.Errorf("frame args %q", m[0])
	}
	orientedMask := strings.TrimPrefix(m[1][len(m[1])-1], "PNG24:")
	if m[1][0] != mask || !slices.Contains(m[1], "-auto-orient") || !slices.Contains(m[1], "8") || !strings.HasPrefix(m[1][len(m[1])-1], "PNG24:") {
		t.Errorf("mask args %q", m[1])
	}
	if m[2][len(m[2])-1] != out {
		t.Errorf("output args %q", m[2])
	}
	calls := enginetest.Calls(t, log)
	if len(calls) != 2 || calls[0][6] != "python" {
		t.Fatalf("want the device probe, then iopaint: %q", calls)
	}
	c := calls[1]
	for _, want := range []string{"--from", "iopaint==1.6.0", "--model=lama", "--device=mps", "--image=" + frame, "--mask=" + orientedMask} {
		if !slices.Contains(c, want) {
			t.Errorf("args lack %s: %q", want, c)
		}
	}
	if _, err := os.Stat(out); err != nil {
		t.Error("no output")
	}
}

// TestInpaintRotatedSource: a mask drawn on the displayed image of a phone
// JPEG stored on its side lines up with the oriented frame.
func TestInpaintRotatedSource(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	_, log := stubs(t)
	t.Setenv("ROT", pngOf(t, dir, "rot.png", 30, 40))
	in, mask, out := pngOf(t, dir, "in.png", 40, 30), pngOf(t, dir, "m.png", 30, 40), filepath.Join(dir, "out.png")
	var o, e bytes.Buffer
	if code := Main(context.Background(), []string{"--mask", mask, in, out}, &o, &e); code != 0 {
		t.Fatalf("code %d: %s", code, e.String())
	}
	if len(enginetest.Calls(t, log)) != 2 {
		t.Error("iopaint did not run")
	}
}

func TestInpaintRefuses(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	_, log := stubs(t)
	in, out := pngOf(t, dir, "in.png", 40, 30), filepath.Join(dir, "out.png")
	var o, e bytes.Buffer
	if code := Main(context.Background(), []string{"--mask", pngOf(t, dir, "m.png", 20, 15), in, out}, &o, &e); code != 1 || !strings.Contains(e.String(), "40x30") {
		t.Errorf("mismatched mask: code %d, %q", code, e.String())
	}
	if len(enginetest.Calls(t, log)) != 0 {
		t.Error("iopaint ran for a refused inpaint")
	}
}

func TestInpaintRefusesSameFile(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	magick, log := stubs(t)
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
	if len(enginetest.Calls(t, magick))+len(enginetest.Calls(t, log)) != 0 {
		t.Error("an engine ran")
	}
}

func TestInpaintRefusesTransparentSource(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	_, log := stubs(t)
	t.Setenv("OPAQUE", "False")
	var o, e bytes.Buffer
	if code := Main(context.Background(), []string{"--mask", pngOf(t, dir, "m.png", 40, 30), pngOf(t, dir, "in.png", 40, 30), filepath.Join(dir, "out.png")}, &o, &e); code != 1 || !strings.Contains(e.String(), "transparent") {
		t.Errorf("code %d, %q", code, e.String())
	}
	if len(enginetest.Calls(t, log)) != 0 {
		t.Error("iopaint ran on a transparent source")
	}
}
