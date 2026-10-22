// Package impose puts two A5 pages, a flyer's front and back, two up on
// both sides of an A4 sheet: printed double-sided with flip on short edge
// and cut down the middle, one sheet makes two flyers. Each side draws its
// source page twice, through package sheet.
package impose

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"math"
	"strconv"

	"github.com/lockyc/plate/internal/cli"
	"github.com/lockyc/plate/internal/pdf"
	"github.com/lockyc/plate/internal/sheet"
)

const usage = "impose [--cut-mark] [--force] -o out.pdf <in.pdf>"

func mm(v float64) float64 { return v * 72 / 25.4 }

var (
	// sheetW and sheetH are A4 landscape, in points.
	sheetW, sheetH = mm(297), mm(210)
	// a5Short and a5Long are A5's sides.
	a5Short, a5Long = mm(148), mm(210)
	// slack is how far a page may be from A5 and still count as A5. Chrome
	// prints A5 as 420 x 594 pt, rounding to whole multiples of 8 CSS px.
	slack = mm(1)
	// tick is a cut mark's length from the sheet's edge: long enough to
	// clear the few millimetres an office printer cannot reach.
	tick = mm(8)
)

type matrix = sheet.Matrix

// place maps page p's user space into one half of the sheet (cell 0 left, 1
// right) for the front or the back: unscaled, centred in the half, and
// turned upright. A portrait page goes in as a viewer shows it. A landscape
// page is turned a quarter to fit a portrait half, the front anticlockwise
// and the back clockwise: turned over about the sheet's vertical axis, as
// flip on short edge does, the cut flyer then turns over like a landscape
// page printed on its own.
func place(p pdf.Page, back bool, cell int) matrix {
	turn := -p.Rotate // anticlockwise degrees; /Rotate is clockwise
	if w, h := p.Size(); w > h {
		if back {
			turn -= 90
		} else {
			turn += 90
		}
	}
	cos, sin := sheet.Quarter(turn)
	m := matrix{cos, sin, -sin, cos, 0, 0}
	lo, hi := sheet.Upright(p, m)
	half := sheetW / 2
	m[4] = float64(cell)*half + (half-(hi[0]-lo[0]))/2 - lo[0]
	m[5] = (sheetH-(hi[1]-lo[1]))/2 - lo[1]
	return m
}

func isA5(p pdf.Page) bool {
	w, h := p.Size()
	short, long := min(w, h), max(w, h)
	return math.Abs(short-a5Short) <= slack && math.Abs(long-a5Long) <= slack
}

func pt(v float64) string { return strconv.FormatFloat(math.Round(v*100)/100, 'f', -1, 64) }

// check refuses anything but a front and a back, each A5, both portrait or
// both landscape.
func check(pages []pdf.Page) error {
	if len(pages) == 0 {
		return fmt.Errorf("want 2 A5 pages, found 0 pages")
	}
	bad := pages[0]
	for _, p := range pages {
		if !isA5(p) {
			bad = p
			break
		}
	}
	if len(pages) != 2 || !isA5(bad) {
		w, h := bad.Size()
		return fmt.Errorf("want 2 A5 pages, found %d pages of %sx%spt", len(pages), pt(w), pt(h))
	}
	orient := func(p pdf.Page) string {
		if w, h := p.Size(); w > h {
			return "landscape"
		}
		return "portrait"
	}
	if f, b := orient(pages[0]), orient(pages[1]); f != b {
		return fmt.Errorf("the front is %s and the back %s; want both the same", f, b)
	}
	return nil
}

func num(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) }

// sideContent draws form /F twice, once in each half of the sheet, each copy
// clipped to its half, then the cut marks.
func sideContent(p pdf.Page, back, cutMark bool) []byte {
	var b bytes.Buffer
	half := sheetW / 2
	for cell := 0; cell < 2; cell++ {
		m := place(p, back, cell)
		fmt.Fprintf(&b, "q %s 0 %s %s re W n %s %s %s %s %s %s cm /F Do Q\n",
			num(float64(cell)*half), num(half), num(sheetH),
			num(m[0]), num(m[1]), num(m[2]), num(m[3]), num(m[4]), num(m[5]))
	}
	if cutMark {
		fmt.Fprintf(&b, "q 0 G 0.5 w %[1]s 0 m %[1]s %[2]s l %[1]s %[3]s m %[1]s %[4]s l S Q\n",
			num(half), num(tick), num(sheetH-tick), num(sheetH))
	}
	return b.Bytes()
}

// layout is the two sides of the sheet, the front then the back.
func layout(cutMark bool) sheet.Layout {
	return func(pages []pdf.Page) ([]sheet.Sheet, error) {
		if err := check(pages); err != nil {
			return nil, err
		}
		sides := make([]sheet.Sheet, len(pages))
		for i, p := range pages {
			sides[i] = sheet.Sheet{Media: [4]float64{0, 0, sheetW, sheetH}, Content: sideContent(p, i == 1, cutMark)}
		}
		return sides, nil
	}
}

// Main runs `plate impose`.
func Main(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := cli.Flags("impose", usage, stderr)
	cutMark := fs.Bool("cut-mark", false, "draw short ticks at each end of the centre line, where the sheet is cut")
	force := fs.Bool("force", false, "replace an existing output")
	out := fs.String("o", "", "the A4 sheet to write (PDF)")
	rest, code, ok := cli.Parse(fs, args, 1)
	if !ok {
		return code
	}
	if *out == "" {
		return cli.Usage(stderr, "impose", "-o out.pdf is required")
	}
	if err := Run(ctx, rest[0], *out, *cutMark, *force); err != nil {
		return cli.Fail(stderr, "impose", err)
	}
	fmt.Fprintf(stdout, "wrote %s\n", *out)
	return 0
}

// Run imposes the two A5 pages of in onto the A4 sheet out, as `plate
// impose` does; an existing out is replaced only when force is set.
func Run(ctx context.Context, in, out string, cutMark, force bool) error {
	return sheet.Write(ctx, in, out, force, layout(cutMark))
}
