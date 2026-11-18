package pdf

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/lockyc/plate/internal/enginetest"
)

// qpdfStub writes its output, the last argument.
const qpdfStub = `for a in "$@"; do out=$a; done; printf '%%PDF-1.4 extracted' > "$out"`

func TestExtractPassesRangeAndRenames(t *testing.T) {
	in := fixture(t)
	log := enginetest.Stub(t, "qpdf", qpdfStub)
	out := filepath.Join(t.TempDir(), "sign 3.pdf")
	var r run
	if code := r.main("extract", "--pages", "3", "-o", out, in); code != 0 {
		t.Fatalf("code %d: %s", code, r.stderr.String())
	}
	if b, _ := os.ReadFile(out); string(b) != "%PDF-1.4 extracted" {
		t.Errorf("out %q", b)
	}
	call := enginetest.Calls(t, log)[0]
	if want := []string{"--warning-exit-0", in, "--pages", ".", "3-3", "--"}; !slices.Equal(call[:6], want) {
		t.Errorf("args %q, want %q then the temporary output", call, want)
	}
	if left, _ := filepath.Glob(filepath.Join(filepath.Dir(out), ".qpdf-*")); len(left) != 0 {
		t.Errorf("temporary files left: %q", left)
	}
}

func TestExtractRefusesExistingWithoutForce(t *testing.T) {
	in := fixture(t)
	enginetest.Stub(t, "qpdf", qpdfStub)
	out := filepath.Join(t.TempDir(), "out.pdf")
	os.WriteFile(out, []byte("keep"), 0o644)
	var r run
	if code := r.main("extract", "--pages", "1-2", "-o", out, in); code == 0 || !strings.Contains(r.stderr.String(), "--force") {
		t.Fatalf("code %d stderr %q", code, r.stderr.String())
	}
	if b, _ := os.ReadFile(out); string(b) != "keep" {
		t.Error("existing output overwritten")
	}
	var f run
	if code := f.main("extract", "--pages", "1-2", "--force", "-o", out, in); code != 0 {
		t.Fatalf("--force: code %d: %s", code, f.stderr.String())
	}
}

func TestExtractNeedsPagesAndOut(t *testing.T) {
	in := fixture(t)
	for _, args := range [][]string{{"extract", "-o", "x.pdf", in}, {"extract", "--pages", "1", in}} {
		var r run
		if code := r.main(args...); code != 2 {
			t.Errorf("%q: code %d, want 2", args, code)
		}
	}
}

func TestExtractLeavesNothingOnFailure(t *testing.T) {
	in := fixture(t)
	enginetest.Stub(t, "qpdf", `echo 'qpdf: range out of bounds' >&2; exit 2`)
	dir := t.TempDir()
	out := filepath.Join(dir, "out.pdf")
	var r run
	if code := r.main("extract", "--pages", "9", "-o", out, in); code == 0 {
		t.Fatal("succeeded past the last page")
	}
	if left, _ := os.ReadDir(dir); len(left) != 0 {
		t.Errorf("left behind: %v", left)
	}
}
