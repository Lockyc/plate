package pdf

import (
	"context"
	"fmt"
	"io"
	"math"
	"os"
	"strconv"

	"github.com/lockyc/plate/internal/cli"
)

const duplexUsage = "pdf duplex --back back.pdf [--force] -o out.pdf <front.pdf>"

// duplexMain writes a double-sided print file: each page of the front PDF
// followed by its back.
func duplexMain(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := cli.Flags("pdf duplex", duplexUsage, stderr)
	back := fs.String("back", "", "the PDF printed on the back of each front page")
	force := fs.Bool("force", false, "replace an existing output")
	out := fs.String("o", "", "the PDF to write")
	front, code, ok := input(fs, "pdf duplex", args, stderr)
	if !ok {
		return code
	}
	if *out == "" {
		return cli.Usage(stderr, "pdf duplex", "-o out.pdf is required")
	}
	if *back == "" {
		return cli.Usage(stderr, "pdf duplex", "--back back.pdf is required")
	}
	if _, err := os.Stat(*back); err != nil {
		return cli.Fail(stderr, "pdf duplex", err)
	}
	if err := Duplex(ctx, front, *back, *out, *force); err != nil {
		return cli.Fail(stderr, "pdf duplex", err)
	}
	fmt.Fprintf(stdout, "wrote %s\n", *out)
	return 0
}

// Duplex writes out through qpdf with every page of front followed by its
// back, unchanged: back's one page behind every front page (numbered cards
// with the same terms on the back), or back's page n behind front's page n
// when the two have the same count. Any other count is refused, as is a
// back whose first page is not the front's size, since a printer would
// scale it. A front page's back sits on the sheet's other side when the
// file is printed double-sided.
func Duplex(ctx context.Context, front, back, out string, force bool) error {
	fi, err := Read(ctx, front)
	if err != nil {
		return err
	}
	bi, err := Read(ctx, back)
	if err != nil {
		return err
	}
	if bi.Pages != 1 && bi.Pages != fi.Pages {
		return fmt.Errorf("%s has %d pages and %s %d: the back needs one page, or one for each front page", back, bi.Pages, front, fi.Pages)
	}
	if math.Abs(fi.W-bi.W) > 0.5 || math.Abs(fi.H-bi.H) > 0.5 {
		return fmt.Errorf("%s is %gx%g pts and %s %gx%g pts: both sides must be one size", back, bi.W, bi.H, front, fi.W, fi.H)
	}
	args := []string{"--empty", "--pages"}
	for n := 1; n <= fi.Pages; n++ {
		b := 1
		if bi.Pages > 1 {
			b = n
		}
		args = append(args, front, strconv.Itoa(n), back, strconv.Itoa(b))
	}
	return qpdfTo(ctx, out, force, []string{front, back}, append(args, "--"))
}
