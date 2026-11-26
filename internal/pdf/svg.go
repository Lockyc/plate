package pdf

import (
	"context"
	"errors"
	"fmt"
	"image"
	"image/png"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"

	"github.com/lockyc/plate/internal/cli"
	"github.com/lockyc/plate/internal/engine"
)

const svgUsage = "pdf svg [--page N] [--trim] [--force] -o out.svg <in.pdf>"

// trimDPI is the resolution --trim finds the artwork's edges at: a quarter
// point, finer than any edge a screen draws the SVG at.
const trimDPI = 288

// svgMain writes one page of a PDF as an SVG, its vector paths kept as
// paths: a logo from a print-ready PDF, drawn sharp at any size on a web
// page where a PNG shrunk by the browser goes jagged.
func svgMain(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := cli.Flags("pdf svg", svgUsage, stderr)
	page := cli.Int(fs, "page", 1, "the page to write (1-based)")
	trim := fs.Bool("trim", false, "fit the SVG to the page's painted artwork, dropping the blank margin around it")
	force := fs.Bool("force", false, "replace an existing output")
	out := fs.String("o", "", "the SVG to write")
	in, code, ok := input(fs, "pdf svg", args, stderr)
	if !ok {
		return code
	}
	if *out == "" {
		return cli.Usage(stderr, "pdf svg", "-o out.svg is required")
	}
	if *page < 1 {
		return cli.Usage(stderr, "pdf svg", "--page %d: pages are 1-based", *page)
	}
	if err := SVG(ctx, in, *out, *page, *trim, *force); err != nil {
		return cli.Fail(stderr, "pdf svg", err)
	}
	fmt.Fprintf(stdout, "wrote %s\n", *out)
	return 0
}

// SVG writes page n of in to out through pdftocairo, written beside out and
// renamed, so a failed run leaves no partial file. With trim, the SVG's
// viewBox is narrowed to the painted artwork, found on a transparent render
// of the same page.
func SVG(ctx context.Context, in, out string, n int, trim, force bool) error {
	if _, err := os.Stat(out); err == nil && !force {
		return fmt.Errorf("%s exists (--force replaces it)", out)
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	info, err := Read(ctx, in)
	if err != nil {
		return err
	}
	if n > info.Pages {
		return fmt.Errorf("--page %d: %s has %d pages", n, in, info.Pages)
	}
	dir, err := os.MkdirTemp(filepath.Dir(out), ".svg-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	raw := filepath.Join(dir, "page.svg")
	page := strconv.Itoa(n)
	if _, err := engine.Run(ctx, engine.Cmd{
		Engine: "pdftocairo", Args: []string{"-svg", "-f", page, "-l", page, in, raw},
		Inputs: []string{in}, Outputs: []string{raw},
	}); err != nil {
		return err
	}
	svg, err := os.ReadFile(raw)
	if err != nil {
		return err
	}
	if trim {
		root := filepath.Join(dir, "page")
		if _, err := engine.Run(ctx, engine.Cmd{
			Engine: "pdftocairo", Args: []string{"-png", "-transp", "-r", strconv.Itoa(trimDPI), "-f", page, "-l", page, "-singlefile", in, root},
			Inputs: []string{in}, Outputs: []string{root + ".png"},
		}); err != nil {
			return err
		}
		box, err := paintedBox(root + ".png")
		if err != nil {
			return err
		}
		if svg, err = fitViewBox(svg, box, 72.0/trimDPI); err != nil {
			return err
		}
	}
	tmp := filepath.Join(dir, "out.svg")
	if err := os.WriteFile(tmp, svg, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, out)
}

// paintedBox is the smallest rectangle holding every pixel of the PNG at
// path that is not fully transparent.
func paintedBox(path string) (image.Rectangle, error) {
	f, err := os.Open(path)
	if err != nil {
		return image.Rectangle{}, err
	}
	defer f.Close()
	img, err := png.Decode(f)
	if err != nil {
		return image.Rectangle{}, err
	}
	b := img.Bounds()
	box := image.Rectangle{Min: b.Max, Max: b.Min}
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			if _, _, _, a := img.At(x, y).RGBA(); a == 0 {
				continue
			}
			box = box.Union(image.Rect(x, y, x+1, y+1))
		}
	}
	if box.Empty() {
		return image.Rectangle{}, errors.New("--trim: the page has no painted artwork")
	}
	return box, nil
}

var (
	svgOpen = regexp.MustCompile(`<svg\b[^>]*>`)
	svgDims = regexp.MustCompile(`\s(width|height|viewBox)="[^"]*"`)
)

// fitViewBox sets the root <svg>'s viewBox to box, a pixel rectangle that
// scale (points per pixel) turns into the SVG's own point units, and its
// width and height to match, so the drawing keeps its size and shape.
func fitViewBox(svg []byte, box image.Rectangle, scale float64) ([]byte, error) {
	at := svgOpen.FindIndex(svg)
	if at == nil {
		return nil, errors.New("--trim: pdftocairo wrote no <svg> element")
	}
	num := func(v float64) string { return strconv.FormatFloat(v, 'f', 2, 64) }
	x, y := float64(box.Min.X)*scale, float64(box.Min.Y)*scale
	w, h := float64(box.Dx())*scale, float64(box.Dy())*scale
	tag := svgDims.ReplaceAll(svg[at[0]:at[1]], nil)
	tag = append(tag[:len(tag)-1], fmt.Sprintf(` width="%spt" height="%spt" viewBox="%s %s %s %s">`, num(w), num(h), num(x), num(y), num(w), num(h))...)
	return append(append(append([]byte{}, svg[:at[0]]...), tag...), svg[at[1]:]...), nil
}
