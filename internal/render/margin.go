package render

import (
	"context"
	"fmt"
	"image"
	"math"
	"os"
	"strings"

	"github.com/lockyc/plate/internal/pdf"
	"github.com/lockyc/plate/internal/raster"
)

// marginDPI is the resolution a PDF's pages are rasterised at for the margin
// check, and cssDPI the density of a CSS px, which turns --blank-margin's
// CSS px into raster px.
const (
	marginDPI = 150
	cssDPI    = 96
)

// inkBelow is the Rec.709 luma, on 16-bit values, under which a pixel is ink:
// anything darker than 99% of full scale. Transparent pixels count as white.
const inkBelow = 0.99 * 65535

// checkMargin returns the sides of img whose outermost band px hold ink, in
// the order top, bottom, left, right. The band is the device pixels lying
// wholly inside the margin (the floor of the margin in device px), so the
// anti-aliased row of content placed exactly at the margin's edge is not ink.
func checkMargin(img image.Image, band int) []string {
	b := img.Bounds()
	if band > b.Dx() {
		band = b.Dx()
	}
	if band > b.Dy() {
		band = b.Dy()
	}
	if band <= 0 {
		return nil
	}
	inked := func(x0, y0, x1, y1 int) bool {
		for y := y0; y < y1; y++ {
			for x := x0; x < x1; x++ {
				var r, g, bl, a uint32
				if p, ok := img.(*image.RGBA64); ok {
					i := p.PixOffset(x, y)
					r = uint32(p.Pix[i])<<8 | uint32(p.Pix[i+1])
					g = uint32(p.Pix[i+2])<<8 | uint32(p.Pix[i+3])
					bl = uint32(p.Pix[i+4])<<8 | uint32(p.Pix[i+5])
					a = uint32(p.Pix[i+6])<<8 | uint32(p.Pix[i+7])
				} else {
					r, g, bl, a = img.At(x, y).RGBA()
				}
				// The values are premultiplied: adding the missing alpha
				// composites the pixel on white.
				w := 65535 - float64(a)
				luma := 0.2126*(float64(r)+w) + 0.7152*(float64(g)+w) + 0.0722*(float64(bl)+w)
				if luma < inkBelow {
					return true
				}
			}
		}
		return false
	}
	var sides []string
	for _, s := range []struct {
		name           string
		x0, y0, x1, y1 int
	}{
		{"top", b.Min.X, b.Min.Y, b.Max.X, b.Min.Y + band},
		{"bottom", b.Min.X, b.Max.Y - band, b.Max.X, b.Max.Y},
		{"left", b.Min.X, b.Min.Y, b.Min.X + band, b.Max.Y},
		{"right", b.Max.X - band, b.Min.Y, b.Max.X, b.Max.Y},
	} {
		if inked(s.x0, s.y0, s.x1, s.y1) {
			sides = append(sides, s.name)
		}
	}
	return sides
}

// checkMargins fails when ink sits within px CSS px of a page edge in the
// PDF (every page at marginDPI) or, with no PDF, in the PNG at scale.
func (j job) checkMargins(ctx context.Context) error {
	var found []string
	switch {
	case j.pdf != "":
		dir, err := os.MkdirTemp("", "plate-margin-")
		if err != nil {
			return err
		}
		defer os.RemoveAll(dir)
		pages, err := pdf.RenderPages(ctx, j.pdf, dir, marginDPI)
		if err != nil {
			return err
		}
		band := j.blankMargin * marginDPI / cssDPI
		for i, p := range pages {
			img, err := raster.Load(p)
			if err != nil {
				return err
			}
			for _, s := range checkMargin(img, band) {
				found = append(found, fmt.Sprintf("page %d %s", i+1, s))
			}
		}
	default:
		img, err := raster.Load(j.png)
		if err != nil {
			return err
		}
		found = checkMargin(img, int(math.Floor(float64(j.blankMargin)*j.scale+1e-9)))
	}
	if len(found) > 0 {
		return fmt.Errorf("ink in the %dpx blank margin: %s", j.blankMargin, strings.Join(found, ", "))
	}
	return nil
}
