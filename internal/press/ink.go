package press

import (
	"context"
	"fmt"
	"path/filepath"
	"strconv"

	"github.com/lockyc/plate/internal/engine"
	"github.com/lockyc/plate/internal/pdf"
	"github.com/lockyc/plate/internal/raster"
)

// inkSpeckMM2 is the total area of over-limit pixels a page may carry.
// Anti-aliasing between two in-limit colours stays in limit, so the
// allowance covers gs's rounding on hairlines, not real artwork.
const inkSpeckMM2 = 1.0

// inkOver is where a page exceeds the ink limit: its highest total ink in
// percent, the area over the limit in mm², and the box around it.
type inkOver struct {
	Peak float64
	Area float64
	Box  raster.Frac
}

// inkLimit finds pixels whose C+M+Y+K exceeds maxPct percent, at dpi. The
// bool is true when they cover more than inkSpeckMM2 in total. Peak is
// the page's highest total ink, over the limit or not.
func inkLimit(img raster.PAM, maxPct, dpi float64) (inkOver, bool) {
	var over inkOver
	var n int
	x0, y0, x1, y1 := img.W, img.H, -1, -1
	for i := 0; i < img.W*img.H; i++ {
		p := img.Pix[4*i : 4*i+4]
		pct := float64(int(p[0])+int(p[1])+int(p[2])+int(p[3])) * 100 / 255
		over.Peak = max(over.Peak, pct)
		if pct <= maxPct {
			continue
		}
		n++
		x, y := i%img.W, i/img.W
		x0, y0, x1, y1 = min(x0, x), min(y0, y), max(x1, x), max(y1, y)
	}
	mm := 25.4 / dpi
	over.Area = float64(n) * mm * mm
	if n > 0 {
		w, h := float64(img.W), float64(img.H)
		over.Box = raster.Frac{X: float64(x0) / w, Y: float64(y0) / h, W: float64(x1-x0+1) / w, H: float64(y1-y0+1) / h}
	}
	return over, over.Area > inkSpeckMM2
}

// renderCMYK renders each page of master to tmp/cmyk-N.pam as device CMYK
// at dpi with overprint honoured, and returns the paths in page order. A
// master's colours are already device CMYK, so the render needs no profile.
func renderCMYK(ctx context.Context, tmp, master, dpi string, pages int) ([]string, error) {
	paths := make([]string, pages)
	for p := range paths {
		paths[p] = filepath.Join(tmp, "cmyk-"+strconv.Itoa(p+1)+".pam")
	}
	args := []string{"-q", "-dBATCH", "-dNOPAUSE", "-dSAFER", "-sDEVICE=pam", "-dOverprint=/enable", "-r" + dpi, "-dTextAlphaBits=4", "-dGraphicsAlphaBits=4",
		"-sOutputFile=" + gsLiteral(tmp) + string(filepath.Separator) + "cmyk-%d.pam", master}
	_, err := engine.Run(ctx, engine.Cmd{Engine: "gs", Args: args, StderrFatal: true, Inputs: []string{master}, Outputs: paths, Timeout: gsTimeout})
	return paths, err
}

// dpi is the resolution that renders src proofWidth px wide.
func (j job) dpi(src pdf.Info) float64 { return float64(j.proofWidth) * 72 / src.W }

// boxSpec writes f as --region does, x,y,w,h.
func boxSpec(f raster.Frac) string {
	return fmt.Sprintf("%.2f,%.2f,%.2f,%.2f", f.X, f.Y, f.W, f.H)
}
