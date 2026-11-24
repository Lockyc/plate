package count

import (
	"bytes"
	"context"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/lockyc/plate/internal/enginetest"
	"github.com/lockyc/plate/internal/pins"
)

// uvStub writes $COUNTS (tab-separated, \n between rows) as counts.tsv in
// --out, or fails with $COUNT_FAIL.
const uvStub = `[ -n "$COUNT_FAIL" ] && { echo "$COUNT_FAIL" >&2; exit 1; }
while [ "$1" != "--out" ]; do shift; done
printf "$COUNTS" > "$2/counts.tsv"`

// frame writes a blank w×h PNG named name in dir.
func frame(t *testing.T, dir, name string, w, h int) string {
	t.Helper()
	p := filepath.Join(dir, name)
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := png.Encode(f, image.NewGray(image.Rect(0, 0, w, h))); err != nil {
		t.Fatal(err)
	}
	return p
}

type run struct {
	uv             string
	out            string
	code           int
	stdout, stderr string
}

func count(t *testing.T, args ...string) run {
	t.Helper()
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	if os.Getenv("COUNTS") == "" {
		t.Setenv("COUNTS", `frame\tdetected\tdensity\testimate\nx\t0\t0\t0\n`)
	}
	r := run{uv: enginetest.Stub(t, "uv", uvStub), out: filepath.Join(t.TempDir(), "out")}
	var o, e bytes.Buffer
	r.code = Main(context.Background(), append([]string{"--out", r.out}, args...), &o, &e)
	r.stdout, r.stderr = o.String(), e.String()
	return r
}

func (r run) call(t *testing.T) []string {
	t.Helper()
	calls := enginetest.Calls(t, r.uv)
	if len(calls) != 1 {
		t.Fatalf("uv ran %d times, want once; stderr %q", len(calls), r.stderr)
	}
	return calls[0]
}

// after reports whether flag is followed by value somewhere in args.
func after(args []string, flag, value string) bool {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == flag && args[i+1] == value {
			return true
		}
	}
	return false
}

func TestEquirectAuto(t *testing.T) {
	in := frame(t, t.TempDir(), "pano.png", 4000, 2000)
	r := count(t, in)
	if r.code != 0 {
		t.Fatalf("code %d: %s", r.code, r.stderr)
	}
	c := r.call(t)
	if !slices.ContainsFunc(c, func(a string) bool { return strings.HasSuffix(a, "count.py") }) {
		t.Errorf("no count.py in %q", c)
	}
	if !after(c, "--equirect", in) || slices.Contains(c, "--photo") {
		t.Errorf("a 2:1 frame must go as --equirect: %q", c)
	}
	for _, want := range [][2]string{
		{"--nadir-deg", "35"}, {"--zenith-deg", "30"},
		{"--detector", pins.CountDetector.Repo}, {"--detector-revision", pins.CountDetector.Revision},
		{"--density", pins.CountDensity.Repo}, {"--density-revision", pins.CountDensity.Revision}, {"--density-file", pins.CountDensity.File},
	} {
		if !after(c, want[0], want[1]) {
			t.Errorf("args lack %s %s: %q", want[0], want[1], c)
		}
	}
	if slices.Contains(c, "--marked") {
		t.Errorf("--marked passed unasked: %q", c)
	}
}

func TestPhoto(t *testing.T) {
	in := frame(t, t.TempDir(), "street.png", 3000, 2000)
	r := count(t, "--marked", "--nadir-deg", "20", in)
	if r.code != 0 {
		t.Fatalf("code %d: %s", r.code, r.stderr)
	}
	c := r.call(t)
	if !after(c, "--photo", in) || slices.Contains(c, "--equirect") {
		t.Errorf("a 3:2 photo must go as --photo: %q", c)
	}
	if !slices.Contains(c, "--marked") || !after(c, "--nadir-deg", "20") {
		t.Errorf("flags not passed on: %q", c)
	}
}

func TestEquirectOverride(t *testing.T) {
	dir := t.TempDir()
	wide := frame(t, dir, "wide.png", 400, 200)
	tall := frame(t, dir, "tall.png", 300, 200)
	r := count(t, "--equirect", "no", wide)
	if c := r.call(t); !after(c, "--photo", wide) {
		t.Errorf("--equirect no: %q", c)
	}
	r = count(t, "--equirect", "yes", tall)
	if c := r.call(t); !after(c, "--equirect", tall) {
		t.Errorf("--equirect yes: %q", c)
	}
	if r := count(t, "--equirect", "maybe", tall); r.code != 2 {
		t.Errorf("--equirect maybe: code %d", r.code)
	}
}

// TestDirectory: a directory stands for its images, in name order.
func TestDirectory(t *testing.T) {
	dir := t.TempDir()
	b := frame(t, dir, "b.png", 400, 200)
	a := frame(t, dir, "a.png", 400, 200)
	os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("x"), 0o644)
	c := count(t, dir).call(t)
	ia, ib := slices.Index(c, a), slices.Index(c, b)
	if ia < 0 || ib < 0 || ia > ib || slices.ContainsFunc(c, func(s string) bool { return strings.HasSuffix(s, "notes.txt") }) {
		t.Errorf("directory not expanded to its images in order: %q", c)
	}
}

func TestRefusesSameStem(t *testing.T) {
	a := frame(t, t.TempDir(), "x.png", 400, 200)
	b := frame(t, t.TempDir(), "x.png", 400, 200)
	r := count(t, a, b)
	if r.code != 2 || !strings.Contains(r.stderr, "x") || len(enginetest.Calls(t, r.uv)) != 0 {
		t.Errorf("code %d, %q", r.code, r.stderr)
	}
}

func TestRefusesUsedOut(t *testing.T) {
	in := frame(t, t.TempDir(), "x.png", 400, 200)
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	uv := enginetest.Stub(t, "uv", uvStub)
	out := t.TempDir()
	os.WriteFile(filepath.Join(out, "counts.tsv"), []byte("old"), 0o644)
	var o, e bytes.Buffer
	if code := Main(context.Background(), []string{"--out", out, in}, &o, &e); code != 1 || !strings.Contains(e.String(), "counts.tsv") {
		t.Errorf("code %d, %q", code, e.String())
	}
	if len(enginetest.Calls(t, uv)) != 0 {
		t.Error("uv ran")
	}
}

func TestOutputs(t *testing.T) {
	dir := t.TempDir()
	frame(t, dir, "120706_005.png", 400, 200)
	frame(t, dir, "120736_002.png", 400, 200)
	t.Setenv("COUNTS", `frame\tdetected\tdensity\testimate\n120706_005\t3\t2.5\t3\n120736_002\t12\t14.2\t14\n`)
	r := count(t, dir)
	if r.code != 0 {
		t.Fatalf("code %d: %s", r.code, r.stderr)
	}
	if !strings.Contains(r.stdout, "frames 2  peak 14 at 120736_002\n") {
		t.Errorf("stdout %q", r.stdout)
	}
	if !strings.Contains(r.stdout, filepath.Join(r.out, "counts.tsv")) {
		t.Errorf("stdout does not name counts.tsv: %q", r.stdout)
	}
}

func TestScriptFailure(t *testing.T) {
	in := frame(t, t.TempDir(), "x.png", 400, 200)
	t.Setenv("COUNT_FAIL", "model went wrong")
	r := count(t, in)
	if r.code != 1 || !strings.Contains(r.stderr, "model went wrong") {
		t.Errorf("code %d, %q", r.code, r.stderr)
	}
}

func TestUsage(t *testing.T) {
	for _, args := range [][]string{{}, {"--nadir-deg", "95", "x.png"}, {"--zenith-deg", "-1", "x.png"}} {
		var o, e bytes.Buffer
		if code := Main(context.Background(), args, &o, &e); code != 2 {
			t.Errorf("%q: code %d", args, code)
		}
	}
}
