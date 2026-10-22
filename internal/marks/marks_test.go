package marks

import (
	"bytes"
	"context"
	"fmt"
	"image/color"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lockyc/plate/internal/engine"
	"github.com/lockyc/plate/internal/pdf"
	"github.com/lockyc/plate/internal/raster"
)

var ctx = context.Background()

// flyer is the impose quality case's synthetic two-page A5 flyer, as Chrome
// prints A5 (420 x 594 pt).
var flyer = filepath.Join("..", "..", "quality", "assets", "two-a5.pdf")

func needEngines(t *testing.T, names ...string) {
	t.Helper()
	for _, n := range names {
		if _, err := engine.Resolve(n); err != nil {
			t.Skip(err)
		}
	}
}

func marked(t *testing.T, in string, flags ...string) string {
	t.Helper()
	out := filepath.Join(t.TempDir(), "marked.pdf")
	var stdout, stderr bytes.Buffer
	if code := Main(ctx, append(flags, "-o", out, in), &stdout, &stderr); code != 0 {
		t.Fatalf("marks exited %d: %s", code, stderr.String())
	}
	return out
}

// writePDF writes a one-page PDF with MediaBox box and content.
func writePDF(t *testing.T, box, extra, content string) string {
	t.Helper()
	objs := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		fmt.Sprintf("<< /Type /Page /Parent 2 0 R /MediaBox [%s] %s /Contents 4 0 R >>", box, extra),
		fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(content)+1, content),
	}
	var b bytes.Buffer
	b.WriteString("%PDF-1.7\n")
	var offs []int
	for i, o := range objs {
		offs = append(offs, b.Len())
		fmt.Fprintf(&b, "%d 0 obj\n%s\nendobj\n", i+1, o)
	}
	xref := b.Len()
	fmt.Fprintf(&b, "xref\n0 %d\n0000000000 65535 f \n", len(objs)+1)
	for _, o := range offs {
		fmt.Fprintf(&b, "%010d 00000 n \n", o)
	}
	fmt.Fprintf(&b, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(objs)+1, xref)
	path := filepath.Join(t.TempDir(), "in.pdf")
	if err := os.WriteFile(path, b.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func near(a, b float64) bool { return math.Abs(a-b) < 0.01 }

func TestTrimSnapsToISO(t *testing.T) {
	cases := []struct {
		name         string
		w, h, tw, th float64
	}{
		{"A4 as Chrome prints it", 594, 840, mm(210), mm(297)},
		{"A5 landscape", 594, 420, mm(210), mm(148)},
		{"A3", mm(297), mm(420), mm(297), mm(420)},
		{"not ISO", 500, 700, 500, 700},
		{"2 mm off A4", mm(212), mm(297), mm(212), mm(297)},
	}
	for _, c := range cases {
		if tw, th := trimOf(c.w, c.h); !near(tw, c.tw) || !near(th, c.th) {
			t.Errorf("%s: trim %gx%g, want %gx%g", c.name, tw, th, c.tw, c.th)
		}
	}
}

func box(t *testing.T, info, name string, page int) [4]float64 {
	t.Helper()
	prefix := fmt.Sprintf("Page %4d %s:", page, name)
	for _, line := range strings.Split(info, "\n") {
		if strings.HasPrefix(line, prefix) {
			var b [4]float64
			if _, err := fmt.Sscan(strings.TrimPrefix(line, prefix), &b[0], &b[1], &b[2], &b[3]); err != nil {
				t.Fatal(err)
			}
			return b
		}
	}
	t.Fatalf("no %s for page %d in:\n%s", name, page, info)
	return [4]float64{}
}

func TestMarksBoxes(t *testing.T) {
	needEngines(t, "qpdf", "pdfinfo")
	out := marked(t, flyer)
	res, err := engine.Run(ctx, engine.Cmd{Engine: "pdfinfo", Args: []string{"-box", "-f", "1", "-l", "2", out}, Inputs: []string{out}})
	if err != nil {
		t.Fatal(err)
	}
	info := string(res.Stdout)
	edge := mm(DefaultBleed) + mark + clear
	for page := 1; page <= 2; page++ {
		trim, bleed, media := box(t, info, "TrimBox", page), box(t, info, "BleedBox", page), box(t, info, "MediaBox", page)
		if w, h := trim[2]-trim[0], trim[3]-trim[1]; math.Abs(w-mm(148)) > 0.05 || math.Abs(h-mm(210)) > 0.05 {
			t.Errorf("page %d: trim %gx%g pt, want A5", page, w, h)
		}
		if math.Abs(trim[0]-bleed[0]-mm(3)) > 0.05 || math.Abs(bleed[2]-trim[2]-mm(3)) > 0.05 {
			t.Errorf("page %d: bleed %v round trim %v, want 3 mm", page, bleed, trim)
		}
		if math.Abs(media[2]-trim[2]-edge) > 0.05 || math.Abs(trim[0]-edge) > 0.05 {
			t.Errorf("page %d: media %v, trim %v, want the trim %g pt in", page, media, trim, edge)
		}
	}
}

func TestMarksKeepsText(t *testing.T) {
	needEngines(t, "qpdf", "pdfinfo", "pdftotext")
	text, err := pdf.Text(ctx, marked(t, flyer))
	if err != nil {
		t.Fatal(err)
	}
	pages := strings.Split(text, "\f")
	for i, word := range []string{"Front", "Back"} {
		if !strings.Contains(pages[i], word) {
			t.Errorf("page %d lacks %q:\n%s", i+1, word, pages[i])
		}
	}
}

// TestMarksPlacesPageOnTrim draws a square in each corner of an A4 page as
// Chrome prints it, and checks each lands just inside the trim's corner, the
// bleed past it is paper, and a crop mark runs beyond the bleed.
func TestMarksPlacesPageOnTrim(t *testing.T) {
	needEngines(t, "qpdf", "pdfinfo", "pdftoppm")
	const w, h, s = 594.0, 840.0, 20.0
	content := fmt.Sprintf("0 0 0 rg 0 0 %[1]g %[1]g re f %[2]g 0 %[1]g %[1]g re f 0 %[3]g %[1]g %[1]g re f %[2]g %[3]g %[1]g %[1]g re f",
		s, w-s, h-s)
	out := marked(t, writePDF(t, fmt.Sprintf("0 0 %g %g", w, h), "/Resources << >>", content))
	pngs, err := pdf.RenderPages(ctx, out, filepath.Join(t.TempDir(), "pages"), 72)
	if err != nil {
		t.Fatal(err)
	}
	img, err := raster.Load(pngs[0])
	if err != nil {
		t.Fatal(err)
	}
	dark := func(x, y float64) bool {
		r, g, b, _ := color.GrayModel.Convert(img.At(int(x), img.Bounds().Dy()-1-int(y))).RGBA()
		return r+g+b < 3*0x8000
	}
	edge := mm(DefaultBleed) + mark + clear
	tw, th := mm(210), mm(297)
	for _, c := range [][2]float64{{edge, edge}, {edge + tw, edge}, {edge, edge + th}, {edge + tw, edge + th}} {
		in := [2]float64{c[0] + math.Copysign(4, edge+tw/2-c[0]), c[1] + math.Copysign(4, edge+th/2-c[1])}
		outside := [2]float64{c[0] - math.Copysign(4, edge+tw/2-c[0]), c[1] - math.Copysign(4, edge+th/2-c[1])}
		if !dark(in[0], in[1]) {
			t.Errorf("corner %v: the page's square is not just inside the trim", c)
		}
		if dark(outside[0], outside[1]) {
			t.Errorf("corner %v: ink in the bleed", c)
		}
	}
	// The bottom-left corner's vertical mark, in line with the trim's left
	// edge, from the bleed's edge outwards.
	if y := edge - mm(DefaultBleed) - mark/2; !dark(edge, y) {
		t.Errorf("no crop mark at %g,%g", edge, y)
	}
}
