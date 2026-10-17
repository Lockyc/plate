package pdf

import (
	"context"
	"testing"

	"github.com/lockyc/plate/internal/enginetest"
)

func TestRead(t *testing.T) {
	enginetest.Stub(t, "pdfinfo", `printf 'Title:          x\nPages:          3\nPage size:      595.276 x 841.89 pts (A4)\n'`)
	info, err := Read(context.Background(), "in.pdf")
	if err != nil || info != (Info{Pages: 3, W: 595.276, H: 841.89}) {
		t.Fatalf("Read = %+v, %v", info, err)
	}
}

func TestPageSizes(t *testing.T) {
	log := enginetest.Stub(t, "pdfinfo", `printf 'Page    1 size: 900 x 675 pts\nPage    2 size: 612 x 792 pts (letter)\n'`)
	got, err := PageSizes(context.Background(), "in.pdf", 2)
	if err != nil || len(got) != 2 || got[1] != [2]float64{612, 792} {
		t.Fatalf("PageSizes = %v, %v", got, err)
	}
	if c := enginetest.Calls(t, log)[0]; c[0] != "-f" || c[1] != "1" || c[2] != "-l" || c[3] != "2" {
		t.Errorf("args %q", c)
	}
}

func TestReadNoBox(t *testing.T) {
	enginetest.Stub(t, "pdfinfo", `printf 'Pages: 1\n'`)
	if _, err := Read(context.Background(), "in.pdf"); err == nil {
		t.Fatal("missing page size accepted")
	}
}

func TestReadRejectsZeroPages(t *testing.T) {
	enginetest.Stub(t, "pdfinfo", `printf 'Pages:          0\nPage size:      595 x 842 pts\n'`)
	if _, err := Read(context.Background(), "in.pdf"); err == nil {
		t.Fatal("0 pages accepted")
	}
}

func TestReadRejectsUnparsableNumbers(t *testing.T) {
	enginetest.Stub(t, "pdfinfo", `printf 'Pages:          1\nPage size:      1.2.3 x 842 pts\n'`)
	if _, err := Read(context.Background(), "in.pdf"); err == nil {
		t.Fatal("unparsable page size accepted")
	}
}

func TestPageSizesRejectsZeroPages(t *testing.T) {
	enginetest.Stub(t, "pdfinfo", `printf 'x\n'`)
	if _, err := PageSizes(context.Background(), "in.pdf", 0); err == nil {
		t.Fatal("0 pages accepted")
	}
}

func TestPages(t *testing.T) {
	log := enginetest.Stub(t, "pdfinfo", `case "$1" in
  -box) printf 'Page    1 rot:   0\nPage    2 rot:   90\nPage    1 CropBox:       0.00     0.00   420.00   594.00\nPage    2 CropBox:      10.00    20.00   604.00   440.00\n' ;;
  *) printf 'Pages:          2\nPage size:      420 x 594 pts\n' ;;
esac`)
	got, err := Pages(context.Background(), "in.pdf")
	want := []Page{{Crop: [4]float64{0, 0, 420, 594}}, {Crop: [4]float64{10, 20, 604, 440}, Rotate: 90}}
	if err != nil || len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("Pages = %v, %v", got, err)
	}
	if w, h := got[1].Size(); w != 420 || h != 594 {
		t.Errorf("rotated Size = %gx%g, want 420x594", w, h)
	}
	if c := enginetest.Calls(t, log)[1]; c[0] != "-box" || c[4] != "2" {
		t.Errorf("args %q", c)
	}
}

func TestPagesRefusesAMissingBox(t *testing.T) {
	enginetest.Stub(t, "pdfinfo", `case "$1" in
  -box) printf 'Page    1 rot:   0\n' ;;
  *) printf 'Pages:          1\nPage size:      420 x 594 pts\n' ;;
esac`)
	if _, err := Pages(context.Background(), "in.pdf"); err == nil {
		t.Fatal("a page without a crop box accepted")
	}
}
