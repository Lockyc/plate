// Package marks makes a print shop's file from a finished PDF: each page,
// unchanged, set at its trim size in the middle of a larger sheet, with a
// bleed round the trim and crop marks outside the bleed. The trim and the
// bleed are also the sheet's TrimBox and BleedBox, which prepress software
// reads in place of the marks.
//
// The trim is the ISO A size the page is within a millimetre of, so a page
// Chrome rounded to its 8 CSS px grid (A4 as 209.6 x 296.3 mm) is scaled
// that fraction to the exact size; any other page is its own trim. The
// bleed carries what the page has past its edge, which is nothing: exact
// for a page with a white margin, as an office print has. A page inked to
// its edge needs its artwork extended into the bleed before it reaches
// marks.
package marks

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

const usage = "marks [--bleed MM] [--force] -o out.pdf <in.pdf>"

func mm(v float64) float64 { return v * 72 / 25.4 }

var (
	// slack is how far a page may be from an ISO A size and still be set
	// at it.
	slack = mm(1)
	// mark is a crop mark's length; it starts at the bleed's edge, and
	// clear is the paper left beyond it.
	mark, clear = mm(5), mm(2)
	// stroke is the marks' line width: hairline, as prepress software
	// draws them.
	stroke = 0.25
)

// isoA is A0 to A8, short side first, in mm.
var isoA = [][2]float64{{841, 1189}, {594, 841}, {420, 594}, {297, 420}, {210, 297}, {148, 210}, {105, 148}, {74, 105}, {52, 74}}

// trimOf is the trim a page w x h pt is set at: the ISO A size within slack,
// turned as the page is, or else the page itself.
func trimOf(w, h float64) (tw, th float64) {
	short, long := min(w, h), max(w, h)
	for _, a := range isoA {
		s, l := mm(a[0]), mm(a[1])
		if math.Abs(short-s) <= slack && math.Abs(long-l) <= slack {
			if w > h {
				return l, s
			}
			return s, l
		}
	}
	return w, h
}

// registration is the colour the marks are drawn in: every separation at
// full strength, so they print on each plate.
var registration = []any{"/Separation", "/All", "/DeviceCMYK", map[string]any{
	"/FunctionType": 2, "/Domain": []float64{0, 1}, "/C0": []float64{0, 0, 0, 0}, "/C1": []float64{1, 1, 1, 1}, "/N": 1,
}}

func num(v float64) string { return strconv.FormatFloat(math.Round(v*1e4)/1e4, 'f', -1, 64) }

// place is page p's sheet: the page turned upright, scaled to its trim,
// centred, with the trim and bleed boxes and the marks.
func place(p pdf.Page, bleed float64) sheet.Sheet {
	cos, sin := sheet.Quarter(-p.Rotate) // /Rotate is clockwise
	r := sheet.Matrix{cos, sin, -sin, cos, 0, 0}
	lo, hi := sheet.Upright(p, r)
	w, h := hi[0]-lo[0], hi[1]-lo[1]
	tw, th := trimOf(w, h)
	sx, sy := tw/w, th/h
	edge := bleed + mark + clear
	x, y := edge, edge
	m := sheet.Matrix{sx * r[0], sy * r[1], sx * r[2], sy * r[3], x - sx*lo[0], y - sy*lo[1]}

	var b bytes.Buffer
	fmt.Fprintf(&b, "q %s %s %s %s %s %s cm /F Do Q\n", num(m[0]), num(m[1]), num(m[2]), num(m[3]), num(m[4]), num(m[5]))
	fmt.Fprintf(&b, "q /Reg CS 1 SC %s w\n", num(stroke))
	l, rt, bt, tp := x, x+tw, y, y+th
	for _, c := range [][4]float64{{l, bt, -1, -1}, {rt, bt, 1, -1}, {l, tp, -1, 1}, {rt, tp, 1, 1}} {
		fmt.Fprintf(&b, "%s %s m %s %s l %s %s m %s %s l\n",
			num(c[0]+c[2]*bleed), num(c[1]), num(c[0]+c[2]*(bleed+mark)), num(c[1]),
			num(c[0]), num(c[1]+c[3]*bleed), num(c[0]), num(c[1]+c[3]*(bleed+mark)))
	}
	b.WriteString("S Q\n")
	return sheet.Sheet{
		Media: [4]float64{0, 0, tw + 2*edge, th + 2*edge},
		Boxes: map[string][4]float64{
			"TrimBox":  {x, y, x + tw, y + th},
			"BleedBox": {x - bleed, y - bleed, x + tw + bleed, y + th + bleed},
		},
		Content:     b.Bytes(),
		ColorSpaces: map[string]any{"Reg": registration},
	}
}

// Main runs `plate marks`.
func Main(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := cli.Flags("marks", usage, stderr)
	bleed := cli.Float(fs, "bleed", DefaultBleed, "the bleed past the trim, in `MM`")
	force := fs.Bool("force", false, "replace an existing output")
	out := fs.String("o", "", "the print shop's PDF to write")
	rest, code, ok := cli.Parse(fs, args, 1)
	if !ok {
		return code
	}
	if *out == "" {
		return cli.Usage(stderr, "marks", "-o out.pdf is required")
	}
	if *bleed < 0 {
		return cli.Usage(stderr, "marks", "--bleed must not be negative")
	}
	if err := Run(ctx, rest[0], *out, *bleed, *force); err != nil {
		return cli.Fail(stderr, "marks", err)
	}
	fmt.Fprintf(stdout, "wrote %s\n", *out)
	return 0
}

// DefaultBleed is the bleed `plate marks` and the build's marks option
// give, in mm: what print shops ask for.
const DefaultBleed = 3

// Run writes in's pages to out with a bleed of bleedMM and crop marks, as
// `plate marks` does; an existing out is replaced only when force is set.
func Run(ctx context.Context, in, out string, bleedMM float64, force bool) error {
	return sheet.Write(ctx, in, out, force, func(pages []pdf.Page) ([]sheet.Sheet, error) {
		if len(pages) == 0 {
			return nil, fmt.Errorf("%s has no pages", in)
		}
		sheets := make([]sheet.Sheet, len(pages))
		for i, p := range pages {
			sheets[i] = place(p, mm(bleedMM))
		}
		return sheets, nil
	})
}
