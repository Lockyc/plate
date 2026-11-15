package pdf

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/lockyc/plate/internal/cli"
	"github.com/lockyc/plate/internal/engine"
)

const extractUsage = "pdf extract --pages N|N-M [--force] -o out.pdf <in.pdf>"

// extractMain writes the pages --pages names to their own PDF, unchanged:
// one artwork of a multi-page file, sent to a print shop as its own file.
func extractMain(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := cli.Flags("pdf extract", extractUsage, stderr)
	pages := pagesFlag(fs)
	force := fs.Bool("force", false, "replace an existing output")
	out := fs.String("o", "", "the PDF to write")
	in, code, ok := input(fs, "pdf extract", args, stderr)
	if !ok {
		return code
	}
	if *out == "" {
		return cli.Usage(stderr, "pdf extract", "-o out.pdf is required")
	}
	if pages.first == 0 {
		return cli.Usage(stderr, "pdf extract", "--pages is required")
	}
	if err := Extract(ctx, in, *out, *pages, *force); err != nil {
		return cli.Fail(stderr, "pdf extract", err)
	}
	fmt.Fprintf(stdout, "wrote %s\n", *out)
	return 0
}

// Extract copies pages r of in to out through qpdf, which keeps each page's
// content, boxes and fonts as they are. qpdf refuses a range past the last
// page. out is written beside itself and renamed, so a failed run leaves no
// partial file, and an existing out is replaced only when force is set.
func Extract(ctx context.Context, in, out string, r pageRange, force bool) error {
	if _, err := os.Stat(out); err == nil && !force {
		return fmt.Errorf("%s exists (--force replaces it)", out)
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(out), ".extract-*.pdf")
	if err != nil {
		return err
	}
	tmp.Close()
	defer os.Remove(tmp.Name())
	_, err = engine.Run(ctx, engine.Cmd{
		Engine:  "qpdf",
		Args:    []string{"--warning-exit-0", in, "--pages", ".", r.String(), "--", tmp.Name()},
		Inputs:  []string{in},
		Outputs: []string{tmp.Name()},
	})
	if err != nil {
		return err
	}
	return os.Rename(tmp.Name(), out)
}
