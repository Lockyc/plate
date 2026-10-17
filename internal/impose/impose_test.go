package impose

import (
	"bytes"
	"context"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lockyc/plate/internal/diff"
	"github.com/lockyc/plate/internal/engine"
	"github.com/lockyc/plate/internal/pdf"
	"github.com/lockyc/plate/internal/raster"
	"github.com/lockyc/plate/quality"
)

var ctx = context.Background()

// flyer is the quality case's synthetic two-page A5 flyer, as Chrome
// prints A5 (420 x 594 pt).
var flyer = filepath.Join("..", "..", "quality", "assets", "two-a5.pdf")

// needEngines skips a test that runs real engines when one is missing.
func needEngines(t *testing.T, names ...string) {
	t.Helper()
	for _, n := range names {
		if _, err := engine.Resolve(n); err != nil {
			t.Skip(err)
		}
	}
}

func impose(t *testing.T, args ...string) (code int, stderr string) {
	t.Helper()
	var out, errb bytes.Buffer
	code = Main(ctx, args, &out, &errb)
	return code, errb.String()
}

// sheet imposes in and returns the output's path.
func sheet(t *testing.T, in string, flags ...string) string {
	t.Helper()
	out := filepath.Join(t.TempDir(), "sheet.pdf")
	if code, stderr := impose(t, append(flags, "-o", out, in)...); code != 0 {
		t.Fatalf("impose exited %d: %s", code, stderr)
	}
	return out
}

// tpage is one page of a PDF written by writePDF: its MediaBox, any further
// page dictionary entries, and its content stream.
type tpage struct{ box, extra, content string }

// writePDF writes a minimal PDF whose text is Helvetica.
func writePDF(t *testing.T, pages ...tpage) string {
	t.Helper()
	return writePDFCatalog(t, "", pages...)
}

// writePDFCatalog is writePDF with further catalog entries.
func writePDFCatalog(t *testing.T, catalog string, pages ...tpage) string {
	t.Helper()
	objs := []string{"<< /Type /Catalog /Pages 2 0 R " + catalog + " >>", ""}
	var kids []string
	for _, p := range pages {
		page := len(objs) + 1
		kids = append(kids, fmt.Sprintf("%d 0 R", page))
		objs = append(objs,
			fmt.Sprintf("<< /Type /Page /Parent 2 0 R /MediaBox [%s] %s /Contents %d 0 R /Resources << /Font << /F1 << /Type /Font /Subtype /Type1 /BaseFont /Helvetica >> >> >> >>", p.box, p.extra, page+1),
			fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(p.content)+1, p.content))
	}
	objs[1] = fmt.Sprintf("<< /Type /Pages /Kids [%s] /Count %d >>", strings.Join(kids, " "), len(pages))
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

const a5 = "0 0 419.53 595.28"

func blank(box string, n int) []tpage {
	pages := make([]tpage, n)
	for i := range pages {
		pages[i] = tpage{box: box, content: "0 0 m"}
	}
	return pages
}

func TestImposeTwoA5(t *testing.T) {
	needEngines(t, "qpdf", "pdfinfo")
	pages, err := pdf.Pages(ctx, sheet(t, flyer))
	if err != nil {
		t.Fatal(err)
	}
	if len(pages) != 2 {
		t.Fatalf("%d pages, want 2", len(pages))
	}
	for i, p := range pages {
		if w, h := p.Size(); math.Abs(w-842) > 1 || math.Abs(h-595) > 1 {
			t.Errorf("page %d is %gx%g pt, want 842x595", i+1, w, h)
		}
	}
}

func TestImposeKeepsText(t *testing.T) {
	needEngines(t, "qpdf", "pdfinfo", "pdftotext")
	text, err := pdf.Text(ctx, sheet(t, flyer))
	if err != nil {
		t.Fatal(err)
	}
	pages := strings.Split(text, "\f")
	for i, word := range []string{"Front", "Back"} {
		if n := strings.Count(pages[i], word); n != 2 {
			t.Errorf("page %d has %q %d times, want 2:\n%s", i+1, word, n, pages[i])
		}
	}
}

// render rasterises page of in at 150 dpi onto a w x h pt canvas whose
// origin is (x, y) in the page, through Ghostscript's Install procedure,
// which moves the page by exact, sub-pixel amounts.
func render(t *testing.T, in string, page int, x, y, w, h float64) string {
	t.Helper()
	out := filepath.Join(t.TempDir(), "r.png")
	n := fmt.Sprint(page)
	if _, err := engine.Run(ctx, engine.Cmd{Engine: "gs", Args: []string{
		"-q", "-dNOPAUSE", "-dBATCH", "-dSAFER", "-sDEVICE=png16m", "-r150",
		fmt.Sprintf("-dDEVICEWIDTHPOINTS=%g", w), fmt.Sprintf("-dDEVICEHEIGHTPOINTS=%g", h), "-dFIXEDMEDIA",
		"-dFirstPage=" + n, "-dLastPage=" + n, "-sOutputFile=" + out,
		"-c", fmt.Sprintf("<</Install {%g %g translate}>> setpagedevice", -x, -y), "-f", in,
	}, Inputs: []string{in}, Outputs: []string{out}}); err != nil {
		t.Fatal(err)
	}
	return out
}

// threshold is the quality case's maximum, so this test and `just quality`
// hold the halves to one number.
func threshold(t *testing.T, name string) float64 {
	t.Helper()
	cases, err := quality.LoadCases(filepath.Join("..", "..", "quality", "cases.toml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range cases {
		if c.Name == name && c.Max != nil {
			return *c.Max
		}
	}
	t.Fatalf("no quality case %q with a max", name)
	return 0
}

func TestImposeHalvesMatchSource(t *testing.T) {
	needEngines(t, "qpdf", "gs", "pdfinfo")
	out := sheet(t, flyer)
	max := threshold(t, "impose-halves-match-source")
	pages, err := pdf.Pages(ctx, flyer)
	if err != nil {
		t.Fatal(err)
	}
	for page := 1; page <= 2; page++ {
		w, h := pages[page-1].Size()
		want, err := raster.Load(render(t, flyer, page, 0, 0, w, h))
		if err != nil {
			t.Fatal(err)
		}
		for cell := 0; cell < 2; cell++ {
			m := place(pages[page-1], page == 2, cell)
			got, err := raster.Load(render(t, out, page, m[4], m[5], w, h))
			if err != nil {
				t.Fatal(err)
			}
			res, _, _, err := diff.Compare(want, got, 28, 0, 1)
			if err != nil {
				t.Fatal(err)
			}
			if res.Percent > max {
				t.Errorf("page %d, cell %d: %.3f%% differ from the source, above %.3f%%", page, cell, res.Percent, max)
			}
		}
	}
}

// apply maps (x, y) through m as PostScript does.
func apply(m matrix, x, y float64) (float64, float64) {
	return m[0]*x + m[2]*y + m[4], m[1]*x + m[3]*y + m[5]
}

// TestImposeBackForShortEdge pins the turn each side gets. Printed duplex
// with flip on short edge, a landscape sheet turns over about its vertical
// axis, so a page drawn upright prints upright on the back: a portrait
// flyer's back is placed exactly as its front. A landscape flyer is turned a
// quarter to fit a portrait half, front anticlockwise and back clockwise, the
// back half a turn from the front, so the cut flyer turns over like a
// landscape page printed on its own with flip on short edge.
func TestImposeBackForShortEdge(t *testing.T) {
	portrait := pdf.Page{Crop: [4]float64{0, 0, 419.53, 595.28}}
	landscape := pdf.Page{Crop: [4]float64{0, 0, 595.28, 419.53}}
	turned := pdf.Page{Crop: [4]float64{0, 0, 595.28, 419.53}, Rotate: 90}
	cases := []struct {
		name        string
		p           pdf.Page
		front, back [4]float64 // a b c d
	}{
		{"portrait", portrait, [4]float64{1, 0, 0, 1}, [4]float64{1, 0, 0, 1}},
		{"landscape", landscape, [4]float64{0, 1, -1, 0}, [4]float64{0, -1, 1, 0}},
		// /Rotate 90 already shows this page portrait: undo it, add nothing.
		{"rotate 90", turned, [4]float64{0, -1, 1, 0}, [4]float64{0, -1, 1, 0}},
	}
	for _, c := range cases {
		for cell := 0; cell < 2; cell++ {
			for _, back := range []bool{false, true} {
				m := place(c.p, back, cell)
				want := c.front
				if back {
					want = c.back
				}
				if [4]float64(m[:4]) != want {
					t.Errorf("%s cell %d back=%v: rotation %v, want %v", c.name, cell, back, m[:4], want)
				}
				// The page lands centred in its half, unscaled.
				lo, hi := [2]float64{math.Inf(1), math.Inf(1)}, [2]float64{math.Inf(-1), math.Inf(-1)}
				b := c.p.Crop
				for _, xy := range [][2]float64{{b[0], b[1]}, {b[2], b[1]}, {b[0], b[3]}, {b[2], b[3]}} {
					x, y := apply(m, xy[0], xy[1])
					lo, hi = [2]float64{min(lo[0], x), min(lo[1], y)}, [2]float64{max(hi[0], x), max(hi[1], y)}
				}
				half := sheetW / 2
				if cx, cy := (lo[0]+hi[0])/2, (lo[1]+hi[1])/2; math.Abs(cx-(float64(cell)+0.5)*half) > 1e-9 || math.Abs(cy-sheetH/2) > 1e-9 {
					t.Errorf("%s cell %d back=%v: centred at %g,%g", c.name, cell, back, cx, cy)
				}
				if w, h := hi[0]-lo[0], hi[1]-lo[1]; math.Abs(w-419.53) > 1e-9 || math.Abs(h-595.28) > 1e-9 {
					t.Errorf("%s cell %d back=%v: %gx%g pt on the sheet", c.name, cell, back, w, h)
				}
			}
		}
	}
}

// TestImposeHonoursPageRotation draws a mark at a page's user-space origin
// under /Rotate 90, where a viewer shows it top left, and checks it is top
// left on the sheet, so Ghostscript and place do not both apply the turn.
func TestImposeHonoursPageRotation(t *testing.T) {
	needEngines(t, "qpdf", "pdfinfo", "pdftoppm")
	mark := tpage{box: "0 0 595.28 419.53", extra: "/Rotate 90", content: "0 0 0 rg 0 0 60 60 re f"}
	out := sheet(t, writePDF(t, mark, mark))
	png, err := pdf.RenderPages(ctx, out, filepath.Join(t.TempDir(), "pages"), 72)
	if err != nil {
		t.Fatal(err)
	}
	img, err := raster.Load(png[0])
	if err != nil {
		t.Fatal(err)
	}
	for _, at := range []struct {
		x, y int
		dark bool
	}{{20, 20, true}, {20, 570, false}, {400, 20, false}, {441, 20, true}} {
		if c := img.RGBA64At(at.x, at.y); (c.R < 0x8000) != at.dark {
			t.Errorf("pixel %d,%d: R %#x, want dark=%v", at.x, at.y, c.R, at.dark)
		}
	}
}

func TestImposeCutMark(t *testing.T) {
	needEngines(t, "qpdf", "pdfinfo", "pdftoppm")
	in := writePDF(t, blank(a5, 2)...)
	for _, mark := range []bool{false, true} {
		var flags []string
		if mark {
			flags = []string{"--cut-mark"}
		}
		pngs, err := pdf.RenderPages(ctx, sheet(t, in, flags...), filepath.Join(t.TempDir(), "pages"), 144)
		if err != nil {
			t.Fatal(err)
		}
		for _, png := range pngs {
			img, err := raster.Load(png)
			if err != nil {
				t.Fatal(err)
			}
			b := img.Bounds()
			// The hairline may fall across two pixel columns at the centre.
			darkest := func(y int) uint16 {
				return min(img.RGBA64At(b.Dx()/2-1, y).R, img.RGBA64At(b.Dx()/2, y).R)
			}
			for _, y := range []int{4, b.Dy() - 5} {
				if r := darkest(y); (r < 0xc000) != mark {
					t.Errorf("--cut-mark=%v %s: centre at y %d has R %#x", mark, filepath.Base(png), y, r)
				}
			}
			if r := darkest(b.Dy() / 2); r < 0xc000 {
				t.Errorf("--cut-mark=%v: the mark runs into the middle of the sheet", mark)
			}
		}
	}
}

func TestImposeRefusesWrongInput(t *testing.T) {
	needEngines(t, "qpdf", "pdfinfo")
	cases := []struct {
		pages []tpage
		want  string
	}{
		{blank(a5, 1), "want 2 A5 pages, found 1 pages of 419.53x595.28pt"},
		{blank(a5, 3), "want 2 A5 pages, found 3 pages of 419.53x595.28pt"},
		{blank("0 0 595.28 841.89", 2), "want 2 A5 pages, found 2 pages of 595.28x841.89pt"},
		{[]tpage{{box: a5, content: "0 0 m"}, {box: "0 0 595.28 419.53", content: "0 0 m"}}, "the front is portrait and the back landscape"},
	}
	for _, c := range cases {
		out := filepath.Join(t.TempDir(), "sheet.pdf")
		code, stderr := impose(t, "-o", out, writePDF(t, c.pages...))
		if code != 1 || !strings.Contains(stderr, c.want) {
			t.Errorf("exit %d, stderr %q; want 1 and %q", code, stderr, c.want)
		}
		if _, err := os.Stat(out); err == nil {
			t.Errorf("%s: an output was written for a refused input", c.want)
		}
	}
}

func TestImposeOutput(t *testing.T) {
	needEngines(t, "qpdf", "pdfinfo")
	in := writePDF(t, blank(a5, 2)...)
	out := filepath.Join(t.TempDir(), "sheet.pdf")
	os.WriteFile(out, []byte("keep"), 0o644)
	if code, stderr := impose(t, "-o", out, in); code != 1 || !strings.Contains(stderr, "--force") {
		t.Errorf("over an existing output without --force: exit %d, %q", code, stderr)
	}
	if b, _ := os.ReadFile(out); string(b) != "keep" {
		t.Error("the existing output was changed without --force")
	}
	if code, stderr := impose(t, "--force", "-o", out, in); code != 0 {
		t.Fatalf("--force: exit %d, %q", code, stderr)
	}
	first, _ := os.ReadFile(out)
	if code, _ := impose(t, "--force", "-o", out, in); code != 0 {
		t.Fatal("second run failed")
	}
	if again, _ := os.ReadFile(out); !bytes.Equal(first, again) {
		t.Error("two runs over one input wrote different bytes")
	}
	if code, stderr := impose(t, "--force", "-o", in, in); code != 1 || !strings.Contains(stderr, "both") {
		t.Errorf("-o the input: exit %d, %q", code, stderr)
	}
	if code, _ := impose(t, in); code != 2 {
		t.Errorf("no -o: exit %d, want 2", code)
	}
}

// TestImposeLandscapeOnPaper marks a landscape page's bottom-left corner (its
// user-space origin, no /Rotate) on both sides. The front turns
// anticlockwise, which carries that corner to the bottom right of each
// half; the back turns clockwise, to the top left.
func TestImposeLandscapeOnPaper(t *testing.T) {
	needEngines(t, "qpdf", "pdfinfo", "pdftoppm")
	mark := tpage{box: "0 0 595.28 419.53", content: "0 0 0 rg 0 0 60 60 re f"}
	pngs, err := pdf.RenderPages(ctx, sheet(t, writePDF(t, mark, mark)), filepath.Join(t.TempDir(), "pages"), 72)
	if err != nil {
		t.Fatal(err)
	}
	type px struct {
		x, y int
		dark bool
	}
	want := [][]px{
		{{400, 575, true}, {821, 575, true}, {20, 575, false}, {400, 20, false}, {20, 20, false}},
		{{20, 20, true}, {441, 20, true}, {400, 20, false}, {20, 575, false}, {400, 575, false}},
	}
	for i, png := range pngs {
		img, err := raster.Load(png)
		if err != nil {
			t.Fatal(err)
		}
		for _, at := range want[i] {
			if c := img.RGBA64At(at.x, at.y); (c.R < 0x8000) != at.dark {
				t.Errorf("page %d pixel %d,%d: R %#x, want dark=%v", i+1, at.x, at.y, c.R, at.dark)
			}
		}
	}
}

// TestImposeKeepsOutputIntents imposes a page whose catalog names an output
// intent, as a `plate press` master does, and checks the sheet's catalog
// still carries it, with the optional-content properties.
func TestImposeKeepsOutputIntents(t *testing.T) {
	needEngines(t, "qpdf", "pdfinfo")
	catalog := "/OutputIntents [<< /Type /OutputIntent /S /GTS_PDFX /OutputConditionIdentifier (FOGRA39) >>] " +
		"/OCProperties << /OCGs [] /D << /Order [] >> >>"
	out := sheet(t, writePDFCatalog(t, catalog, blank(a5, 2)...))
	res, err := engine.Run(ctx, engine.Cmd{Engine: "qpdf", Args: []string{"--json=2", "--json-key=qpdf", "--json-stream-data=none", out, "-"}, Inputs: []string{out}})
	if err != nil {
		t.Fatal(err)
	}
	s := string(res.Stdout)
	for _, want := range []string{`"/OutputIntents"`, `"u:FOGRA39"`, `"/GTS_PDFX"`, `"/OCProperties"`} {
		if !strings.Contains(s, want) {
			t.Errorf("the sheet lacks %s", want)
		}
	}
}

func TestCheckNoPages(t *testing.T) {
	if err := check(nil); err == nil || !strings.Contains(err.Error(), "want 2 A5 pages, found 0 pages") {
		t.Errorf("check(nil) = %v", err)
	}
}
