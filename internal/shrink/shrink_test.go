package shrink

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/lockyc/plate/internal/enginetest"
)

// magickStub answers the size probe from $INFO, and otherwise writes to its
// last argument, less any FORMAT: prefix: a long body for zlib strategy 0, a
// short one for the rest, so the smallest-PNG choice is visible.
const magickStub = `for a in "$@"; do last=$a; done
if [ "$last" = info: ]; then printf '%s\n' "${INFO:-True 1080 720}"; exit 0; fi
body=small
for a in "$@"; do [ "$a" = png:compression-strategy=0 ] && body=larger-body; done
printf %s "$body" > "${last#*:}"`

func call(t *testing.T, args ...string) (int, string, string, [][]string) {
	t.Helper()
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	log := enginetest.Stub(t, "magick", magickStub)
	var o, e bytes.Buffer
	code := Main(context.Background(), args, &o, &e)
	return code, o.String(), e.String(), enginetest.Calls(t, log)
}

func TestTarget(t *testing.T) {
	for _, c := range []struct{ w, h, bw, bh, ww, wh int }{
		{1080, 1080, 256, 256, 256, 256},
		{1080, 720, 256, 256, 256, 171},
		{720, 1080, 256, 256, 171, 256},
		{1080, 720, 300, 0, 300, 200},
		{1080, 720, 0, 72, 108, 72},
		{1080, 720, 540, 100, 150, 100},
		{4000, 10, 100, 100, 100, 1},
	} {
		if w, h := Target(c.w, c.h, c.bw, c.bh); w != c.ww || h != c.wh {
			t.Errorf("Target(%d, %d, %d, %d) = %dx%d, want %dx%d", c.w, c.h, c.bw, c.bh, w, h, c.ww, c.wh)
		}
	}
}

// TestShrink: the source is read upright and in sRGB, stripped, resampled
// with Filter to the exact target, alpha dropped when opaque, and kept at 16
// bits for the encoders. The PNG keeps the smaller zlib result; the WebP is
// lossy with sharp YUV and lossless alpha.
func TestShrink(t *testing.T) {
	dir := t.TempDir()
	png, webp := filepath.Join(dir, "o.png"), filepath.Join(dir, "o.webp")
	code, stdout, stderr, calls := call(t, "--fit", "256", "in.jpg", png, webp)
	if code != 0 {
		t.Fatalf("code %d: %s", code, stderr)
	}
	if len(calls) != 5 {
		t.Fatalf("want probe, resample, two PNG tries and a WebP; got %q", calls)
	}
	if want := []string{"in.jpg", "-auto-orient", "-format", "%[opaque] %w %h\n", "info:"}; !slices.Equal(calls[0], want) {
		t.Errorf("probe %q, want %q", calls[0], want)
	}
	r := calls[1]
	if len(r) < 4 || !strings.HasSuffix(r[3], ".icc") {
		t.Fatalf("no sRGB profile in %q", r)
	}
	want := []string{"in.jpg", "-auto-orient", "-profile", r[3], "-strip", "-filter", "Catrom", "-resize", "256x171!", "-alpha", "off", "-depth", "16"}
	if !slices.Equal(r[:len(r)-1], want) || !strings.HasPrefix(r[len(r)-1], "MIFF:") {
		t.Errorf("resample %q\nwant %q MIFF:…", r, want)
	}
	for _, c := range calls[2:4] {
		if !slices.Contains(c, "png:compression-level=9") || !strings.HasPrefix(c[len(c)-1], "PNG:") || !slices.Contains(c, "8") {
			t.Errorf("PNG try %q", c)
		}
	}
	w := calls[4]
	for _, a := range []string{"90", "webp:use-sharp-yuv=true", "webp:alpha-quality=100", "webp:method=6"} {
		if !slices.Contains(w, a) {
			t.Errorf("WebP args lack %s: %q", a, w)
		}
	}
	if b, _ := os.ReadFile(png); string(b) != "small" {
		t.Errorf("PNG is %q, want the smaller try", b)
	}
	if b, _ := os.ReadFile(webp); len(b) == 0 {
		t.Error("no WebP written")
	}
	if !strings.Contains(stdout, "o.png (256x171, 5 bytes)") || !strings.Contains(stdout, "o.webp (256x171") {
		t.Errorf("stdout %q", stdout)
	}
}

func TestKeepsAlpha(t *testing.T) {
	t.Setenv("INFO", "False 1080 1080")
	code, _, stderr, calls := call(t, "--width", "128", "in.png", filepath.Join(t.TempDir(), "o.png"))
	if code != 0 {
		t.Fatalf("code %d: %s", code, stderr)
	}
	if slices.Contains(calls[1], "-alpha") || !slices.Contains(calls[1], "128x128!") {
		t.Errorf("resample %q", calls[1])
	}
}

func TestRefuses(t *testing.T) {
	dir := t.TempDir()
	o := filepath.Join(dir, "o.png")
	in := filepath.Join(dir, "in.png")
	if err := os.WriteFile(in, []byte("source"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		args []string
		code int
		msg  string
	}{
		{[]string{"--fit", "2000", "in.png", o}, 1, "never enlarges"},
		{[]string{"--width", "1081", "in.png", o}, 1, "never enlarges"},
		{[]string{"--fit", "256", "in.png", filepath.Join(dir, "o.jpg")}, 2, ".png or .webp"},
		{[]string{"--fit", "256", "--width", "100", "in.png", o}, 2, "not both"},
		{[]string{"in.png", o}, 2, "positive"},
		{[]string{"--height", "-5", "in.png", o}, 2, "positive"},
		{[]string{"--fit", "256", "--quality", "0", "in.png", o}, 2, "1 to 100"},
		{[]string{"--fit", "256", in, in}, 1, "both an input and an output"},
		{[]string{"--fit", "256", "in.png", o, o}, 1, "twice"},
	} {
		code, _, stderr, _ := call(t, c.args...)
		if code != c.code || !strings.Contains(stderr, c.msg) {
			t.Errorf("%q: code %d, %q; want %d and %q", c.args, code, stderr, c.code, c.msg)
		}
	}
	if b, _ := os.ReadFile(in); string(b) != "source" {
		t.Errorf("the source was overwritten: %q", b)
	}
	if code, _, _, _ := call(t, "--fit", "256", "in.png"); code != 2 {
		t.Errorf("no output: code %d", code)
	}
}

func TestMultiFrame(t *testing.T) {
	t.Setenv("INFO", "True 10 10\nTrue 10 10")
	code, _, stderr, _ := call(t, "--fit", "5", "in.gif", filepath.Join(t.TempDir(), "o.png"))
	if code != 1 || !strings.Contains(stderr, "one frame") {
		t.Errorf("code %d, %q", code, stderr)
	}
}
