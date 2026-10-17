package grade

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

// magickStub answers frame.Probe from $CLUT_INFO for h.png and $IN_INFO
// for anything else, and otherwise writes its last argument.
const magickStub = `case "$*" in *"%[opaque]"*) [ "$1" = h.png ] && i=${CLUT_INFO:-True 512 512} || i=${IN_INFO:-False 200 100}; printf '%s sRGB\n' "$i"; exit 0 ;; esac
for a in "$@"; do last=$a; done
printf png > "$last"`

func run(t *testing.T, args ...string) (int, string, [][]string) {
	t.Helper()
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	log := enginetest.Stub(t, "magick", magickStub)
	var o, e bytes.Buffer
	code := Main(context.Background(), args, &o, &e)
	return code, e.String(), enginetest.Calls(t, log)
}

func TestApplyKeepsAlpha(t *testing.T) {
	t.Setenv("IN_INFO", "False 200 100")
	out := filepath.Join(t.TempDir(), "o.png")
	code, stderr, calls := run(t, "apply", "--clut", "h.png", "in.png", out)
	if code != 0 {
		t.Fatalf("code %d: %s", code, stderr)
	}
	want := append(srgbPrefix(t, "in.png", calls[2]), "-write", "mpr:src", "-alpha", "off", "h.png", "-hald-clut",
		"(", "mpr:src", "-alpha", "extract", ")", "-alpha", "off", "-compose", "CopyOpacity", "-composite", out)
	if !slices.Equal(calls[2], want) {
		t.Errorf("args %q\nwant %q", calls[2], want)
	}
}

func TestApplyOpaque(t *testing.T) {
	t.Setenv("IN_INFO", "True 200 100")
	out := filepath.Join(t.TempDir(), "o.png")
	code, _, calls := run(t, "apply", "--clut", "h.png", "in.jpg", out)
	if code != 0 || !slices.Equal(calls[2], append(srgbPrefix(t, "in.jpg", calls[2]), "-alpha", "off", "h.png", "-hald-clut", out)) {
		t.Fatalf("code %d, args %q", code, calls)
	}
}

// srgbPrefix is the source read upright and converted to sRGB, as fit's
// frames are, so the table fit built is applied to the values it was built
// from. No -strip and no -depth: the output keeps the source's depth and is
// tagged sRGB.
func srgbPrefix(t *testing.T, in string, call []string) []string {
	t.Helper()
	if len(call) < 4 || !strings.HasSuffix(call[3], ".icc") {
		t.Fatalf("no sRGB profile in %q", call)
	}
	if slices.Contains(call, "-strip") || slices.Contains(call, "-depth") {
		t.Errorf("apply must keep the source's depth and tag: %q", call)
	}
	return []string{in, "-auto-orient", "-profile", call[3]}
}

func TestApplyRefuses(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLUT_INFO", "True 500 500")
	if code, stderr, _ := run(t, "apply", "--clut", "h.png", "in.png", filepath.Join(dir, "o.png")); code != 1 || !strings.Contains(stderr, "HALD") {
		t.Errorf("non-HALD clut: code %d, %q", code, stderr)
	}
	t.Setenv("CLUT_INFO", "True 512 512")
	if code, stderr, _ := run(t, "apply", "--clut", "h.png", "in.png", filepath.Join(dir, "o.jpg")); code != 2 || !strings.Contains(stderr, ".png") {
		t.Errorf("jpg out: code %d, %q", code, stderr)
	}
	if code, _, _ := run(t, "bogus"); code != 2 {
		t.Errorf("unknown subcommand: code %d", code)
	}
}

func TestApplyRefuseSameFile(t *testing.T) {
	dir := t.TempDir()
	infile := filepath.Join(dir, "in.png")
	// Create a file with content
	if err := os.WriteFile(infile, []byte("original content"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("IN_INFO", "True 200 100")
	t.Setenv("CLUT_INFO", "True 512 512")
	code, stderr, _ := run(t, "apply", "--clut", "h.png", infile, infile)
	if code != 1 || !strings.Contains(stderr, "both an input and an output") {
		t.Errorf("same file: code %d, %q", code, stderr)
	}
	// Check that the file contents survived
	content, err := os.ReadFile(infile)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "original content" {
		t.Errorf("file was modified; got %q", string(content))
	}
}

func TestApplyMalformedIdentify(t *testing.T) {
	for _, info := range []string{"True", "True 512", "True x y"} {
		t.Setenv("CLUT_INFO", info)
		code, stderr, _ := run(t, "apply", "--clut", "h.png", "in.png", filepath.Join(t.TempDir(), "o.png"))
		if code != 1 || !strings.Contains(stderr, "h.png") {
			t.Errorf("CLUT_INFO %q: code %d, %q", info, code, stderr)
		}
	}
}
