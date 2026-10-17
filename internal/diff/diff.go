// Package diff answers "did this layout break", not "is this pixel
// perfect". Both images are cropped to their overlap, never scaled, since
// scaling smears every edge. b is searched over vertical offsets before
// scoring, because a two-pixel change in a header's height shifts every
// line below it and would otherwise score a correct page at a third
// different. A pixel counts as changed when the luma of its per-channel
// difference exceeds the tolerance, which keeps antialiasing and hinting
// noise out of the count.
package diff

import (
	"context"
	"flag"
	"fmt"
	"image"
	"image/color"
	"io"

	"github.com/lockyc/plate/internal/cli"
	"github.com/lockyc/plate/internal/engine"
	"github.com/lockyc/plate/internal/raster"
)

const usage = "diff [--tolerance N] [--max-shift PX] [--step PX] [--threshold PCT] [--out diff.png] <a> <b>"

// Result is the best-aligned comparison. Shift is how many px lower b's
// content sits than a's.
type Result struct {
	Percent float64
	Shift   int
}

type rgb8 struct {
	w, h int
	px   []uint8
}

func to8(img *image.RGBA64, w, h int) rgb8 {
	o := rgb8{w: w, h: h, px: make([]uint8, w*h*3)}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			c := img.RGBA64At(img.Bounds().Min.X+x, img.Bounds().Min.Y+y)
			i := (y*w + x) * 3
			o.px[i], o.px[i+1], o.px[i+2] = uint8(c.R>>8), uint8(c.G>>8), uint8(c.B>>8)
		}
	}
	return o
}

func absd(a, b uint8) int {
	if a > b {
		return int(a - b)
	}
	return int(b - a)
}

// changed is Pillow's L conversion of the absolute difference, compared with tol.
func changed(a, b []uint8, i, j int, tol uint8) bool {
	l := (absd(a[i], b[j])*19595 + absd(a[i+1], b[j+1])*38470 + absd(a[i+2], b[j+2])*7471 + 0x8000) >> 16
	return l > int(tol)
}

// Compare scores b against a over their overlap at the best vertical shift
// in [-maxShift, maxShift] (multiples of step), considering only shifts that
// keep at least half the rows. It returns the mask of changed pixels over
// b's compared rows, and those rows.
func Compare(a, b *image.RGBA64, tol uint8, maxShift, step int) (Result, *image.Gray, image.Rectangle, error) {
	w := min(a.Bounds().Dx(), b.Bounds().Dx())
	h := min(a.Bounds().Dy(), b.Bounds().Dy())
	if w == 0 || h == 0 {
		return Result{}, nil, image.Rectangle{}, fmt.Errorf("nothing to compare: an image is empty")
	}
	if step < 1 {
		step = 1
	}
	maxShift = min(maxShift, h)
	A, B := to8(a, w, h), to8(b, w, h)
	best, bestTop, bestBottom := Result{Percent: -1}, 0, 0
	for k := -maxShift / step; k <= maxShift/step; k++ {
		shift := k * step
		top, bottom := max(0, shift), h+min(0, shift)
		if bottom <= top || bottom-top < h/2 {
			continue
		}
		n := 0
		for y := top; y < bottom; y++ {
			for x := 0; x < w; x++ {
				if changed(A.px, B.px, ((y-shift)*w+x)*3, (y*w+x)*3, tol) {
					n++
				}
			}
		}
		pct := 100 * float64(n) / float64(w*(bottom-top))
		// A tie goes to the smaller shift, which compares more rows.
		if best.Percent < 0 || pct < best.Percent || pct == best.Percent && max(shift, -shift) < max(best.Shift, -best.Shift) {
			best, bestTop, bestBottom = Result{Percent: pct, Shift: shift}, top, bottom
		}
	}
	if best.Percent < 0 {
		return Result{}, nil, image.Rectangle{}, fmt.Errorf("no shifts to evaluate: consider increasing --max-shift")
	}
	rows := image.Rect(0, bestTop, w, bestBottom)
	mask := image.NewGray(rows)
	for y := bestTop; y < bestBottom; y++ {
		for x := 0; x < w; x++ {
			if changed(A.px, B.px, ((y-best.Shift)*w+x)*3, (y*w+x)*3, tol) {
				mask.SetGray(x, y, color.Gray{255})
			}
		}
	}
	return best, mask, rows, nil
}

// picture marks changed pixels magenta over a faded copy of b's compared rows.
func picture(b *image.RGBA64, mask *image.Gray, rows image.Rectangle) *image.RGBA {
	out := image.NewRGBA(image.Rect(0, 0, rows.Dx(), rows.Dy()))
	for y := rows.Min.Y; y < rows.Max.Y; y++ {
		for x := rows.Min.X; x < rows.Max.X; x++ {
			c := color.RGBA{255, 0, 200, 255}
			if mask.GrayAt(x, y).Y == 0 {
				p := b.RGBA64At(x, y)
				fade := func(v uint16) uint8 { return uint8(float64(v>>8)*0.28 + 255*0.72) }
				c = color.RGBA{fade(p.R), fade(p.G), fade(p.B), 255}
			}
			out.SetRGBA(x-rows.Min.X, y-rows.Min.Y, c)
		}
	}
	return out
}

// Main runs `plate diff`. Exit 0: within the threshold (or none given).
// Exit 1: over it. Exit 2: could not compare.
func Main(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := cli.Flags("diff", usage, stderr)
	tol := cli.Int(fs, "tolerance", 28, "per-pixel luma difference, 0-255, at or below which pixels count as equal")
	maxShift := cli.Int(fs, "max-shift", 60, "px of vertical misalignment to search")
	step := cli.Int(fs, "step", 2, "search granularity in px")
	threshold := cli.Float(fs, "threshold", 0, "exit 1 when more than this percent of pixels differ")
	outPath := fs.String("out", "", "write a picture of the changed pixels here (PNG)")
	rest, code, ok := cli.Parse(fs, args, 2)
	if !ok {
		return code
	}
	fail := func(err error) int { fmt.Fprintf(stderr, "plate diff: %v\n", err); return 2 }
	if *tol < 0 || *tol > 255 {
		return fail(fmt.Errorf("--tolerance must be 0-255"))
	}
	if *maxShift < 0 {
		return fail(fmt.Errorf("--max-shift must be >= 0"))
	}
	if *step < 1 {
		return fail(fmt.Errorf("--step must be >= 1"))
	}
	thresholdSet := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "threshold" {
			thresholdSet = true
		}
	})
	if thresholdSet && *threshold < 0 {
		return fail(fmt.Errorf("--threshold must be >= 0"))
	}
	if *outPath != "" && (engine.SameFile(*outPath, rest[0]) || engine.SameFile(*outPath, rest[1])) {
		return fail(fmt.Errorf("--out must not be the same file as an input"))
	}
	a, err := raster.Load(rest[0])
	if err != nil {
		return fail(err)
	}
	b, err := raster.Load(rest[1])
	if err != nil {
		return fail(err)
	}
	res, mask, rows, err := Compare(a, b, uint8(*tol), *maxShift, *step)
	if err != nil {
		return fail(err)
	}
	if *outPath != "" {
		if err := raster.SavePNG(*outPath, picture(b, mask, rows)); err != nil {
			return fail(err)
		}
	}
	if !thresholdSet {
		fmt.Fprintf(stdout, "%.1f%% differ (best fit at %+dpx vertical)\n", res.Percent, res.Shift)
		return 0
	}
	verdict, code := "ok", 0
	if res.Percent > *threshold {
		verdict, code = "OVER", 1
	}
	fmt.Fprintf(stdout, "%s %.1f%% differ (threshold %.1f%%, best fit at %+dpx vertical)\n", verdict, res.Percent, *threshold, res.Shift)
	return code
}
