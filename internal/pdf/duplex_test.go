package pdf

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/lockyc/plate/internal/enginetest"
)

// sides writes front.pdf and back.pdf and stands pdfinfo in for them: the
// front has front pages, the back back pages at backSize.
func sides(t *testing.T, front, back, backSize string) (string, string) {
	t.Helper()
	dir := t.TempDir()
	f, b := filepath.Join(dir, "front.pdf"), filepath.Join(dir, "back.pdf")
	os.WriteFile(f, []byte("%PDF-1.4 stub"), 0o644)
	os.WriteFile(b, []byte("%PDF-1.4 stub"), 0o644)
	enginetest.Stub(t, "pdfinfo", `case "$1" in
*front.pdf) printf 'Pages:          `+front+`\nPage size:      840 x 594 pts\n' ;;
*) printf 'Pages:          `+back+`\nPage size:      `+backSize+` pts\n' ;;
esac`)
	return f, b
}

func TestDuplexRepeatsOneBack(t *testing.T) {
	f, b := sides(t, "3", "1", "840 x 594")
	log := enginetest.Stub(t, "qpdf", qpdfStub)
	out := filepath.Join(t.TempDir(), "cards.pdf")
	var r run
	if code := r.main("duplex", "--back", b, "-o", out, f); code != 0 {
		t.Fatalf("code %d: %s", code, r.stderr.String())
	}
	call := enginetest.Calls(t, log)[0]
	want := []string{"--warning-exit-0", "--empty", "--pages", f, "1", b, "1", f, "2", b, "1", f, "3", b, "1", "--"}
	if !slices.Equal(call[:len(want)], want) || len(call) != len(want)+1 {
		t.Errorf("args %q, want %q then the temporary output", call, want)
	}
	if left, _ := filepath.Glob(filepath.Join(filepath.Dir(out), ".qpdf-*")); len(left) != 0 {
		t.Errorf("temporary files left: %q", left)
	}
}

func TestDuplexPairsPageForPage(t *testing.T) {
	f, b := sides(t, "2", "2", "840 x 594")
	log := enginetest.Stub(t, "qpdf", qpdfStub)
	var r run
	if code := r.main("duplex", "--back", b, "-o", filepath.Join(t.TempDir(), "o.pdf"), f); code != 0 {
		t.Fatalf("code %d: %s", code, r.stderr.String())
	}
	call := enginetest.Calls(t, log)[0]
	if want := []string{"--warning-exit-0", "--empty", "--pages", f, "1", b, "1", f, "2", b, "2", "--"}; !slices.Equal(call[:len(want)], want) {
		t.Errorf("args %q, want %q", call, want)
	}
}

func TestDuplexRefusesMismatch(t *testing.T) {
	for _, c := range []struct{ back, size, says string }{
		{"2", "840 x 594", "one for each front page"},
		{"1", "595 x 842", "one size"},
	} {
		f, b := sides(t, "3", c.back, c.size)
		log := enginetest.Stub(t, "qpdf", qpdfStub)
		var r run
		if code := r.main("duplex", "--back", b, "-o", filepath.Join(t.TempDir(), "o.pdf"), f); code == 0 || !strings.Contains(r.stderr.String(), c.says) {
			t.Errorf("back %s pages %s: code %d stderr %q", c.back, c.size, code, r.stderr.String())
		}
		if len(enginetest.Calls(t, log)) != 0 {
			t.Error("qpdf ran on a mismatch")
		}
	}
}

func TestDuplexNeedsBackAndOut(t *testing.T) {
	f, b := sides(t, "1", "1", "840 x 594")
	for _, args := range [][]string{{"duplex", "-o", "x.pdf", f}, {"duplex", "--back", b, f}} {
		var r run
		if code := r.main(args...); code != 2 {
			t.Errorf("%q: code %d, want 2", args, code)
		}
	}
}
