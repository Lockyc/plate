package render

import (
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/lockyc/plate/internal/enginetest"
)

func whiteImg(w, h int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for i := range img.Pix {
		img.Pix[i] = 255
	}
	return img
}

func TestCheckMarginClean(t *testing.T) {
	if got := checkMargin(whiteImg(200, 200), 10); len(got) != 0 {
		t.Errorf("clean page = %v", got)
	}
}

func TestCheckMarginInk(t *testing.T) {
	img := whiteImg(200, 200)
	img.Set(1, 100, color.Gray{128})
	if got := checkMargin(img, 10); !slices.Equal(got, []string{"left"}) {
		t.Errorf("got %v, want [left]", got)
	}
	img = whiteImg(200, 200)
	img.Set(100, 198, color.Gray{128})
	if got := checkMargin(img, 3); !slices.Equal(got, []string{"bottom"}) {
		t.Errorf("got %v, want [bottom]", got)
	}
}

func TestCheckMarginJustInside(t *testing.T) {
	img := whiteImg(200, 200)
	img.Set(10, 100, color.Black)
	if got := checkMargin(img, 10); len(got) != 0 {
		t.Errorf("ink at x=band = %v, want none", got)
	}
}

func TestCheckMarginTransparentIsWhite(t *testing.T) {
	img := image.NewNRGBA(image.Rect(0, 0, 50, 50))
	if got := checkMargin(img, 5); len(got) != 0 {
		t.Errorf("transparent = %v, want none", got)
	}
}

func cornerPNG(t *testing.T, w, h int) string {
	t.Helper()
	img := whiteImg(w, h)
	img.Set(0, 0, color.Black)
	p := filepath.Join(t.TempDir(), "corner.png")
	f, _ := os.Create(p)
	png.Encode(f, img)
	f.Close()
	return p
}

func TestRenderBlankMarginPNG(t *testing.T) {
	e := setup(t, 100, 50)
	t.Setenv("FIXTURE_PNG", cornerPNG(t, 100, 50))
	if code := e.run("--size", "100x50", "--blank-margin", "23", "--png", e.out("p.png"), e.page); code != 1 {
		t.Fatalf("code %d: %s", code, e.stderr.String())
	}
	if !strings.Contains(e.stderr.String(), "ink in the 23px blank margin: top, left") {
		t.Errorf("stderr %q", e.stderr.String())
	}
	if left, _ := filepath.Glob(filepath.Join(e.dir, ".plate-*")); len(left) != 0 || exists(e.out("p.png")) {
		t.Errorf("left behind: %q", left)
	}
}

func TestRenderBlankMarginPDFPages(t *testing.T) {
	e := setup(t, 100, 50)
	enginetest.Stub(t, "pdfinfo", `printf 'Pages: 2\nPage size: 900 x 675 pts\n'`)
	clean := filepath.Join(t.TempDir(), "clean.png")
	f, _ := os.Create(clean)
	png.Encode(f, whiteImg(100, 100))
	f.Close()
	t.Setenv("PAGE_CLEAN", clean)
	t.Setenv("PAGE_INK", cornerPNG(t, 100, 100))
	enginetest.Stub(t, "pdftoppm", `for a in "$@"; do root=$a; done
case "$root" in *-1) cp "$PAGE_CLEAN" "$root.png" ;; *) cp "$PAGE_INK" "$root.png" ;; esac`)
	if code := e.run("--blank-margin", "23", "--pdf", e.out("p.pdf"), e.page); code != 1 {
		t.Fatalf("code %d: %s", code, e.stderr.String())
	}
	if !strings.Contains(e.stderr.String(), "ink in the 23px blank margin: page 2 top, page 2 left") {
		t.Errorf("stderr %q", e.stderr.String())
	}
	if exists(e.out("p.pdf")) {
		t.Error("PDF left behind")
	}
}

func TestBlankMarginUsage(t *testing.T) {
	e := setup(t, 100, 50)
	if code := e.run("--blank-margin", "5", "--html", e.out("p.html"), e.page); code != 2 {
		t.Errorf("html only: code %d", code)
	}
	e = setup(t, 100, 50)
	if code := e.run("--size", "100x50", "--blank-margin=-1", "--png", e.out("p.png"), e.page); code != 2 {
		t.Errorf("negative: code %d", code)
	}
}

func TestCacheKeyIncludesBlankMargin(t *testing.T) {
	e := cacheEnv(t)
	o := e.pngOpts()
	e.render(t, o)
	o.BlankMargin = 5
	if e.render(t, o) {
		t.Error("a new --blank-margin skipped on the old record")
	}
}

func TestCheckMarginBandIsFloor(t *testing.T) {
	// 23 CSS px at 150 dpi is 35.94 device px: the band is 35, so row 35 (the
	// anti-aliased edge of content at the margin) passes and row 34 fails.
	band := 23 * marginDPI / cssDPI
	if band != 35 {
		t.Fatalf("band %d, want 35", band)
	}
	img := whiteImg(300, 300)
	img.Set(100, band, color.Gray{200})
	if got := checkMargin(img, band); len(got) != 0 {
		t.Errorf("ink at row %d = %v, want none", band, got)
	}
	img = whiteImg(300, 300)
	img.Set(100, band-1, color.Gray{200})
	if got := checkMargin(img, band); !slices.Equal(got, []string{"top"}) {
		t.Errorf("ink at row %d = %v, want [top]", band-1, got)
	}
}
