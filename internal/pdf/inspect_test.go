package pdf

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/lockyc/plate/internal/enginetest"
)

type run struct{ stdout, stderr bytes.Buffer }

func (r *run) main(args ...string) int {
	return Main(context.Background(), args, &r.stdout, &r.stderr)
}

// fixture is a file standing in for a PDF; the stubs never read it.
func fixture(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "in.pdf")
	os.WriteFile(p, []byte("%PDF-1.4 stub"), 0o644)
	return p
}

// stubPages stands pdfinfo in for a document of n pages.
func stubPages(t *testing.T, n string) {
	enginetest.Stub(t, "pdfinfo", `printf 'Pages:          `+n+`\nPage size:      595 x 842 pts (A4)\n'`)
}

// pdftoppmStub writes "<root>.png", the -singlefile name; its root is the
// last argument.
const pdftoppmStub = `for a in "$@"; do root=$a; done; printf png > "$root.png"`

func TestInfoReportsTextLayer(t *testing.T) {
	in := fixture(t)
	stubPages(t, "2")
	for text, want := range map[string]string{"  hello \n": "Text layer:      yes", " \n\f\n": "Text layer:      no"} {
		enginetest.Stub(t, "pdftotext", `printf '`+text+`'`)
		var r run
		if code := r.main("info", in); code != 0 {
			t.Fatalf("code %d: %s", code, r.stderr.String())
		}
		if !strings.Contains(r.stdout.String(), "Pages:          2") || !strings.Contains(r.stdout.String(), want) {
			t.Errorf("text %q: stdout %q, want %q", text, r.stdout.String(), want)
		}
	}
}

func TestInfoMissingFile(t *testing.T) {
	log := enginetest.Stub(t, "pdfinfo", `exit 0`)
	var r run
	if code := r.main("info", filepath.Join(t.TempDir(), "absent.pdf")); code != 1 || !strings.Contains(r.stderr.String(), "no such file") {
		t.Fatalf("code %d stderr %q", code, r.stderr.String())
	}
	if len(enginetest.Calls(t, log)) != 0 {
		t.Error("pdfinfo ran on a missing file")
	}
}

func TestTextPassesRangeAndLayout(t *testing.T) {
	in := fixture(t)
	log := enginetest.Stub(t, "pdftotext", `printf 'page two'`)
	var r run
	if code := r.main("text", "--pages", "2-3", in); code != 0 || r.stdout.String() != "page two" {
		t.Fatalf("code %d stdout %q stderr %q", code, r.stdout.String(), r.stderr.String())
	}
	if got, want := enginetest.Calls(t, log)[0], []string{"-layout", "-f", "2", "-l", "3", in, "-"}; !slices.Equal(got, want) {
		t.Errorf("args %q, want %q", got, want)
	}
}

func TestPagesFlagRefusesBadRanges(t *testing.T) {
	in := fixture(t)
	enginetest.Stub(t, "pdftotext", `exit 0`)
	for _, spec := range []string{"0", "3-2", "x", "1-", "-1", "1-2-3", ""} {
		var r run
		if code := r.main("text", "--pages", spec, in); code != 2 {
			t.Errorf("--pages %q: code %d, want 2", spec, code)
		}
	}
}

// pdftoppm pads page numbers to the width of the document's page count, so
// plate names each page itself with -singlefile.
func TestPagesNamesEachPage(t *testing.T) {
	in := fixture(t)
	stubPages(t, "12")
	log := enginetest.Stub(t, "pdftoppm", pdftoppmStub)
	out := filepath.Join(t.TempDir(), "out")
	var r run
	if code := r.main("pages", "--pages", "3-4", "--dpi", "0200", "--out", out, in); code != 0 {
		t.Fatalf("code %d: %s", code, r.stderr.String())
	}
	want := filepath.Join(out, "page-03.png") + "\n" + filepath.Join(out, "page-04.png") + "\n"
	if r.stdout.String() != want {
		t.Errorf("stdout %q, want %q", r.stdout.String(), want)
	}
	calls := enginetest.Calls(t, log)
	if len(calls) != 2 {
		t.Fatalf("%d pdftoppm calls, want one per page", len(calls))
	}
	slices.SortFunc(calls, func(a, b []string) int { return strings.Compare(a[4], b[4]) })
	if want := []string{"-png", "-r", "200", "-f", "3", "-l", "3", "-singlefile", in, filepath.Join(out, "page-03")}; !slices.Equal(calls[0], want) {
		t.Errorf("args %q, want %q", calls[0], want)
	}
}

// Pages render concurrently, so each file must hold its own page and a
// failure must name the page that failed.
func TestRenderPagesMapsPagesToFiles(t *testing.T) {
	in := fixture(t)
	stubPages(t, "12")
	enginetest.Stub(t, "pdftoppm", `while [ $# -gt 0 ]; do case "$1" in -f) f=$2;; esac; root=$1; shift; done
sleep 0.0$((RANDOM % 5)); printf "page$f" > "$root.png"`)
	out := t.TempDir()
	paths, err := RenderPages(context.Background(), in, out, 150)
	if err != nil || len(paths) != 12 {
		t.Fatalf("%d paths, err %v", len(paths), err)
	}
	for i, p := range paths {
		if got, _ := os.ReadFile(p); string(got) != "page"+strconv.Itoa(i+1) {
			t.Errorf("%s holds %q, want page%d", p, got, i+1)
		}
	}
}

func TestRenderPagesFailureNamesPage(t *testing.T) {
	in := fixture(t)
	stubPages(t, "12")
	enginetest.Stub(t, "pdftoppm", `while [ $# -gt 0 ]; do case "$1" in -f) f=$2;; esac; root=$1; shift; done
if [ "$f" = 7 ]; then echo "bad page $root" >&2; exit 1; fi
printf x > "$root.png"`)
	out := t.TempDir()
	_, err := RenderPages(context.Background(), in, out, 150)
	if err == nil || !strings.Contains(err.Error(), "page-07") {
		t.Fatalf("err %v, want it to name page-07", err)
	}
	if left, _ := filepath.Glob(filepath.Join(out, "*.png")); len(left) != 0 {
		t.Errorf("left behind: %q", left)
	}
}

// pdftoppm does not create its output directory.
func TestPagesCreatesOut(t *testing.T) {
	in := fixture(t)
	stubPages(t, "1")
	enginetest.Stub(t, "pdftoppm", pdftoppmStub)
	t.Chdir(t.TempDir())
	var r run
	if code := r.main("pages", "--out", "-dash/deep", in); code != 0 {
		t.Fatalf("code %d: %s", code, r.stderr.String())
	}
	if _, err := os.Stat(filepath.Join("-dash", "deep", "page-1.png")); err != nil {
		t.Fatal(err)
	}
}

func TestPagesDefaultsToAFreshTempDir(t *testing.T) {
	in := fixture(t)
	stubPages(t, "1")
	enginetest.Stub(t, "pdftoppm", pdftoppmStub)
	t.Setenv("TMPDIR", t.TempDir())
	var r run
	if code := r.main("pages", in); code != 0 {
		t.Fatalf("code %d: %s", code, r.stderr.String())
	}
	p := strings.TrimSpace(r.stdout.String())
	if !strings.HasPrefix(p, os.Getenv("TMPDIR")) || filepath.Base(p) != "page-1.png" {
		t.Errorf("wrote %q", p)
	}
}

// A reused --out would mix an earlier run's pages with this run's.
func TestPagesRefusesAReusedOut(t *testing.T) {
	in := fixture(t)
	stubPages(t, "2")
	log := enginetest.Stub(t, "pdftoppm", pdftoppmStub)
	out := t.TempDir()
	os.WriteFile(filepath.Join(out, "page-2.png"), []byte("earlier"), 0o644)
	var r run
	if code := r.main("pages", "--out", out, in); code != 1 || !strings.Contains(r.stderr.String(), "nothing was deleted") {
		t.Fatalf("code %d stderr %q", code, r.stderr.String())
	}
	if b, _ := os.ReadFile(filepath.Join(out, "page-2.png")); string(b) != "earlier" {
		t.Error("an earlier file was touched")
	}
	if len(enginetest.Calls(t, log)) != 0 {
		t.Error("pdftoppm ran")
	}
}

// pdftoppm takes any -r in silence.
func TestPagesBoundsDPI(t *testing.T) {
	in := fixture(t)
	log := enginetest.Stub(t, "pdftoppm", pdftoppmStub)
	for _, dpi := range []string{"0", "1201", "-5", "1e3", "0x40"} {
		var r run
		if code := r.main("pages", "--dpi", dpi, in); code != 2 {
			t.Errorf("--dpi %s: code %d, want 2", dpi, code)
		}
	}
	if len(enginetest.Calls(t, log)) != 0 {
		t.Error("pdftoppm ran")
	}
}

func TestPagesRefusesARangePastTheEnd(t *testing.T) {
	in := fixture(t)
	stubPages(t, "3")
	enginetest.Stub(t, "pdftoppm", pdftoppmStub)
	var r run
	if code := r.main("pages", "--pages", "2-5", in); code != 1 || !strings.Contains(r.stderr.String(), "has 3 pages") {
		t.Fatalf("code %d stderr %q", code, r.stderr.String())
	}
}

// A failed page leaves nothing behind: not its own file, not the pages
// before it.
func TestPagesFailureRemovesThisRunsPages(t *testing.T) {
	in := fixture(t)
	stubPages(t, "3")
	enginetest.Stub(t, "pdftoppm", `for a in "$@"; do root=$a; done; case $root in *page-3) exit 1;; esac; printf png > "$root.png"`)
	out := t.TempDir()
	var r run
	if code := r.main("pages", "--out", out, in); code != 1 {
		t.Fatalf("code %d", code)
	}
	if left, _ := os.ReadDir(out); len(left) != 0 {
		t.Errorf("left %v", left)
	}
}

// pdfimages exits 0 having written nothing when the PDF holds no rasters.
func TestImagesRefusesAnEmptyResult(t *testing.T) {
	in := fixture(t)
	enginetest.Stub(t, "pdfimages", `exit 0`)
	out := filepath.Join(t.TempDir(), "imgs")
	var r run
	if code := r.main("images", "--out", out, in); code != 1 || !strings.Contains(r.stderr.String(), "plate pdf pages") {
		t.Fatalf("code %d stderr %q", code, r.stderr.String())
	}
	if left, _ := os.ReadDir(out); len(left) != 0 {
		t.Errorf("left %v", left)
	}
}

func TestImagesReportsWhatThisRunWrote(t *testing.T) {
	in := fixture(t)
	log := enginetest.Stub(t, "pdfimages", `for a in "$@"; do root=$a; done; printf x > "$root-002-000.jpg"; printf x > "$root-002-001.png"`)
	out := t.TempDir()
	os.WriteFile(filepath.Join(out, "unrelated.png"), nil, 0o644)
	var r run
	if code := r.main("images", "--pages", "2", "--out", out, in); code != 0 {
		t.Fatalf("code %d: %s", code, r.stderr.String())
	}
	want := filepath.Join(out, "img-002-000.jpg") + "\n" + filepath.Join(out, "img-002-001.png") + "\n"
	if r.stdout.String() != want {
		t.Errorf("stdout %q, want %q", r.stdout.String(), want)
	}
	if c := enginetest.Calls(t, log)[0]; !slices.Equal(c[:6], []string{"-all", "-p", "-f", "2", "-l", "2"}) {
		t.Errorf("args %q", c)
	}
	if entries, _ := os.ReadDir(out); len(entries) != 3 {
		t.Errorf("out holds %v; the staging directory should be gone", entries)
	}
}

func TestImagesRefusesAReusedOut(t *testing.T) {
	in := fixture(t)
	enginetest.Stub(t, "pdfimages", `for a in "$@"; do root=$a; done; printf new > "$root-001-000.png"`)
	out := t.TempDir()
	os.WriteFile(filepath.Join(out, "img-001-000.png"), []byte("earlier"), 0o644)
	var r run
	if code := r.main("images", "--out", out, in); code != 1 || !strings.Contains(r.stderr.String(), "nothing was deleted") {
		t.Fatalf("code %d stderr %q", code, r.stderr.String())
	}
	if b, _ := os.ReadFile(filepath.Join(out, "img-001-000.png")); string(b) != "earlier" {
		t.Error("an earlier file was overwritten")
	}
}

func TestVerbsRefuseFlagsTheyDoNotTake(t *testing.T) {
	in := fixture(t)
	for _, args := range [][]string{
		{"info", "--pages", "1", in},
		{"text", "--dpi", "300", in},
		{"images", "--dpi", "300", in},
		{"info", in, "extra"},
		{"bogus", in},
		{},
	} {
		var r run
		if code := r.main(args...); code != 2 {
			t.Errorf("%q: code %d, want 2", args, code)
		}
	}
}
