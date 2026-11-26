package pdf

import (
	"context"
	"fmt"
	"image"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/lockyc/plate/internal/engine"
)

func TestFitViewBoxReplacesTheRootSizeOnly(t *testing.T) {
	in := []byte(`<?xml version="1.0"?>` + "\n" +
		`<svg xmlns="http://www.w3.org/2000/svg" width="72pt" height="72pt" viewBox="0 0 72 72" version="1.1">` +
		`<g><svg width="5" height="5"/></g></svg>`)
	got, err := fitViewBox(in, image.Rect(80, 148, 120, 168), 0.25)
	if err != nil {
		t.Fatal(err)
	}
	want := `<svg xmlns="http://www.w3.org/2000/svg" version="1.1" width="10.00pt" height="5.00pt" viewBox="20.00 37.00 10.00 5.00">`
	if !strings.Contains(string(got), want) {
		t.Errorf("root\n%s\nwant\n%s", got, want)
	}
	if !strings.Contains(string(got), `<g><svg width="5" height="5"/></g>`) {
		t.Errorf("an inner element was changed: %s", got)
	}
}

func TestFitViewBoxRefusesAFileWithNoSVG(t *testing.T) {
	if _, err := fitViewBox([]byte("<html/>"), image.Rect(0, 0, 1, 1), 1); err == nil {
		t.Error("no error for a file with no <svg>")
	}
}

// A one-page PDF of w×h points with a filled rectangle at x, y (from the
// page's bottom left), rw×rh points.
func rectPDF(t *testing.T, w, h, x, y, rw, rh int) string {
	t.Helper()
	content := fmt.Sprintf("0 0 1 rg %d %d %d %d re f", x, y, rw, rh)
	objs := []string{
		"<</Type/Catalog/Pages 2 0 R>>",
		"<</Type/Pages/Kids[3 0 R]/Count 1>>",
		fmt.Sprintf("<</Type/Page/Parent 2 0 R/MediaBox[0 0 %d %d]/Contents 4 0 R>>", w, h),
		fmt.Sprintf("<</Length %d>>stream\n%s\nendstream", len(content)+1, content),
	}
	var b strings.Builder
	b.WriteString("%PDF-1.4\n")
	offsets := make([]int, len(objs))
	for i, o := range objs {
		offsets[i] = b.Len()
		fmt.Fprintf(&b, "%d 0 obj%s endobj\n", i+1, o)
	}
	xref := b.Len()
	fmt.Fprintf(&b, "xref\n0 %d\n0000000000 65535 f \n", len(objs)+1)
	for _, o := range offsets {
		fmt.Fprintf(&b, "%010d 00000 n \n", o)
	}
	fmt.Fprintf(&b, "trailer<</Size %d/Root 1 0 R>>\nstartxref\n%d\n%%%%EOF\n", len(objs)+1, xref)
	path := filepath.Join(t.TempDir(), "rect.pdf")
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// The real pdftocairo: the page comes out as vector paths, and --trim fits
// the viewBox to the painted rectangle, within a quarter point.
func TestSVGTrimsToThePaintedArtwork(t *testing.T) {
	for _, n := range []string{"pdfinfo", "pdftocairo"} {
		if _, err := engine.Resolve(n); err != nil {
			t.Skip(err)
		}
	}
	in := rectPDF(t, 72, 72, 20, 30, 10, 5)
	out := filepath.Join(t.TempDir(), "logo.svg")
	if err := SVG(context.Background(), in, out, 1, true, false); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(out)
	if strings.Contains(string(b), "<image") {
		t.Error("the SVG carries a raster image, not paths")
	}
	m := regexp.MustCompile(`<svg\b[^>]*viewBox="([^"]+)"`).FindSubmatch(b)
	if m == nil {
		t.Fatalf("no viewBox: %s", b)
	}
	want := []float64{20, 37, 10, 5} // y from the top: 72 - 30 - 5
	for i, f := range strings.Fields(string(m[1])) {
		v, _ := strconv.ParseFloat(f, 64)
		if d := v - want[i]; d < -0.5 || d > 0.5 {
			t.Errorf("viewBox %s, want about %v", m[1], want)
			break
		}
	}
	if left, _ := filepath.Glob(filepath.Join(filepath.Dir(out), ".svg-*")); len(left) != 0 {
		t.Errorf("temporary files left: %q", left)
	}
}

func TestSVGRefusesExistingWithoutForceAndAPagePastTheEnd(t *testing.T) {
	for _, n := range []string{"pdfinfo", "pdftocairo"} {
		if _, err := engine.Resolve(n); err != nil {
			t.Skip(err)
		}
	}
	in := rectPDF(t, 72, 72, 20, 30, 10, 5)
	out := filepath.Join(t.TempDir(), "logo.svg")
	os.WriteFile(out, []byte("keep"), 0o644)
	if err := SVG(context.Background(), in, out, 1, false, false); err == nil {
		t.Error("an existing output was replaced without --force")
	}
	if err := SVG(context.Background(), in, out, 2, false, true); err == nil || !strings.Contains(err.Error(), "has 1 pages") {
		t.Errorf("page 2 of a one-page PDF: %v", err)
	}
}
