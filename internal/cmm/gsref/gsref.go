// Package gsref converts CMYK patches to sRGB with Ghostscript's own colour
// management, the reference plate's colour engine is measured against, in
// Intent: the intent press converts with.
package gsref

import (
	"bytes"
	"context"
	"fmt"
	"math"
	"os"
	"path/filepath"

	"github.com/lockyc/plate/internal/engine"
	"github.com/lockyc/plate/internal/raster"
)

// Intent is the Ghostscript flags for press's conversion intent: relative
// colorimetric with black point compensation, as cmm's ToSRGB proofs.
var Intent = []string{"-dRenderIntent=1", "-dBlackPtComp=1"}

// patch is each patch's side in points, rendered at 72 dpi: one pixel a
// point, so a patch centre is far from any edge.
const patch = 8

// SRGB renders each CMYK patch as a DeviceCMYK fill in a PDF, through icc
// as Ghostscript's default CMYK profile, to png16m, and returns each
// patch's sRGB in 0..1.
func SRGB(ctx context.Context, icc string, cmyk [][4]byte) ([][3]float64, error) {
	if len(cmyk) == 0 {
		return nil, nil
	}
	dir, err := os.MkdirTemp("", "plate-gsref-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	cols := int(math.Ceil(math.Sqrt(float64(len(cmyk)))))
	rows := (len(cmyk) + cols - 1) / cols
	w, h := cols*patch, rows*patch
	var content bytes.Buffer
	for i, c := range cmyk {
		x, y := i%cols*patch, h-(i/cols+1)*patch
		fmt.Fprintf(&content, "%.6f %.6f %.6f %.6f k %d %d %d %d re f\n",
			float64(c[0])/255, float64(c[1])/255, float64(c[2])/255, float64(c[3])/255, x, y, patch, patch)
	}
	pdf := filepath.Join(dir, "patches.pdf")
	if err := os.WriteFile(pdf, pdfDoc(w, h, content.Bytes()), 0o644); err != nil {
		return nil, err
	}
	png := filepath.Join(dir, "patches.png")
	args := append([]string{"-q", "-dBATCH", "-dNOPAUSE", "-dSAFER", "-sDEVICE=png16m", "-r72",
		"--permit-file-read=" + icc, "-sDefaultCMYKProfile=" + icc}, Intent...)
	args = append(args, "-sOutputFile="+png, pdf)
	if _, err := engine.Run(ctx, engine.Cmd{Engine: "gs", Args: args, StderrFatal: true, Inputs: []string{pdf, icc}, Outputs: []string{png}}); err != nil {
		return nil, err
	}
	img, err := raster.Load(png)
	if err != nil {
		return nil, err
	}
	if b := img.Bounds(); b.Dx() != w || b.Dy() != h {
		return nil, fmt.Errorf("gs rendered %dx%d, want %dx%d", b.Dx(), b.Dy(), w, h)
	}
	out := make([][3]float64, len(cmyk))
	for i := range cmyk {
		px := img.RGBA64At(i%cols*patch+patch/2, i/cols*patch+patch/2)
		out[i] = [3]float64{float64(px.R) / 65535, float64(px.G) / 65535, float64(px.B) / 65535}
	}
	return out, nil
}

// pdfDoc is a one-page PDF of the given size whose page draws content.
func pdfDoc(w, h int, content []byte) []byte {
	objs := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		fmt.Sprintf("<< /Type /Page /Parent 2 0 R /MediaBox [0 0 %d %d] /Contents 4 0 R /Resources << >> >>", w, h),
		fmt.Sprintf("<< /Length %d >>\nstream\n%sendstream", len(content), content),
	}
	var b bytes.Buffer
	b.WriteString("%PDF-1.4\n")
	offs := make([]int, len(objs))
	for i, o := range objs {
		offs[i] = b.Len()
		fmt.Fprintf(&b, "%d 0 obj\n%s\nendobj\n", i+1, o)
	}
	xref := b.Len()
	fmt.Fprintf(&b, "xref\n0 %d\n0000000000 65535 f \n", len(objs)+1)
	for _, o := range offs {
		fmt.Fprintf(&b, "%010d 00000 n \n", o)
	}
	fmt.Fprintf(&b, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(objs)+1, xref)
	return b.Bytes()
}
