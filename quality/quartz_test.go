package quality

import (
	"fmt"
	"image"
	"image/color"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/lockyc/plate/internal/raster"
)

// halfBlackPDF is a 20x10 pt page, its left half black.
func halfBlackPDF(t *testing.T) string {
	t.Helper()
	content := "0 0 0 rg 0 0 10 10 re f"
	objs := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 20 10] /Contents 4 0 R >>",
		fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(content), content),
	}
	b := []byte("%PDF-1.4\n")
	var offs []int
	for i, o := range objs {
		offs = append(offs, len(b))
		b = fmt.Appendf(b, "%d 0 obj\n%s\nendobj\n", i+1, o)
	}
	xref := len(b)
	b = fmt.Appendf(b, "xref\n0 %d\n0000000000 65535 f \n", len(objs)+1)
	for _, o := range offs {
		b = fmt.Appendf(b, "%010d 00000 n \n", o)
	}
	b = fmt.Appendf(b, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(objs)+1, xref)
	p := filepath.Join(t.TempDir(), "half.pdf")
	if err := os.WriteFile(p, b, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func refPNG(t *testing.T, name string, black func(x int) bool) string {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 20, 10))
	for y := range 10 {
		for x := range 20 {
			c := color.RGBA{255, 255, 255, 255}
			if black(x) {
				c = color.RGBA{0, 0, 0, 255}
			}
			img.Set(x, y, c)
		}
	}
	p := filepath.Join(t.TempDir(), name)
	if err := raster.SavePNG(p, img); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestQuartzRMSE(t *testing.T) {
	pdf := halfBlackPDF(t)
	same := refPNG(t, "same.png", func(x int) bool { return x < 10 })
	if runtime.GOOS != "darwin" {
		if _, err := Metrics["quartz-rmse"](Params{"pdf": pdf, "ref": same}); err == nil {
			t.Error("quartz-rmse measured off macOS")
		}
		return
	}
	if v, err := Metrics["quartz-rmse"](Params{"pdf": pdf, "ref": same}); err != nil || v > 0.01 {
		t.Errorf("against its own drawing: %v, %v", v, err)
	}
	white := refPNG(t, "white.png", func(int) bool { return false })
	if v, err := Metrics["quartz-rmse"](Params{"pdf": pdf, "ref": white}); err != nil || v < 0.6 {
		t.Errorf("against a white page, half of it black: %v, %v", v, err)
	}
	big := filepath.Join(t.TempDir(), "big.png")
	raster.SavePNG(big, image.NewRGBA(image.Rect(0, 0, 40, 20)))
	if v, err := Metrics["quartz-rmse"](Params{"pdf": pdf, "ref": big}); err == nil {
		t.Errorf("a reference of another size measured %v", v)
	}
}
