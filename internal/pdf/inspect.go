package pdf

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"

	"github.com/lockyc/plate/internal/cli"
	"github.com/lockyc/plate/internal/engine"
)

const usage = "pdf info <in.pdf>\n" +
	"       plate pdf text [--pages N|N-M] <in.pdf>\n" +
	"       plate pdf pages [--pages N|N-M] [--out DIR] [--dpi N] <in.pdf>\n" +
	"       plate pdf images [--pages N|N-M] [--out DIR] <in.pdf>"

// maxDPI bounds --dpi. pdftoppm takes any -r in silence: 0 becomes its own
// 150, and a huge value writes a 1x1 PNG and exits 0.
const maxDPI = 1200

// maxProcs bounds the concurrent pdftoppm calls of renderPages.
const maxProcs = 4

// Main dispatches `plate pdf <verb>`.
func Main(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) > 0 {
		switch args[0] {
		case "info":
			return infoMain(ctx, args[1:], stdout, stderr)
		case "text":
			return textMain(ctx, args[1:], stdout, stderr)
		case "pages":
			return pagesMain(ctx, args[1:], stdout, stderr)
		case "images":
			return imagesMain(ctx, args[1:], stdout, stderr)
		}
	}
	fmt.Fprintf(stderr, "usage: plate %s\n", usage)
	return cli.VerbUsageCode(args)
}

// pageRange is --pages: 1-based and inclusive; zero when not given.
type pageRange struct{ first, last int }

func (r *pageRange) String() string {
	if r.first == 0 {
		return ""
	}
	return fmt.Sprintf("%d-%d", r.first, r.last)
}

func (r *pageRange) Set(s string) error {
	a, b, isRange := strings.Cut(s, "-")
	if !isRange {
		b = a
	}
	first, errA := strconv.ParseUint(a, 10, 31)
	last, errB := strconv.ParseUint(b, 10, 31)
	switch {
	case errA != nil || errB != nil:
		return errors.New("want N or N-M")
	case first < 1:
		return errors.New("pages are 1-based")
	case first > last:
		return errors.New("first page exceeds last")
	}
	r.first, r.last = int(first), int(last)
	return nil
}

// args is the poppler -f/-l pair, or nothing for the whole document.
func (r pageRange) args() []string {
	if r.first == 0 {
		return nil
	}
	return []string{"-f", strconv.Itoa(r.first), "-l", strconv.Itoa(r.last)}
}

func pagesFlag(fs *flag.FlagSet) *pageRange {
	r := new(pageRange)
	fs.Var(r, "pages", "only these pages, N or N-M (1-based)")
	return r
}

// input parses a verb's flags and its one <in.pdf>, which must exist.
func input(fs *flag.FlagSet, op string, args []string, stderr io.Writer) (string, int, bool) {
	rest, code, ok := cli.Parse(fs, args, 1)
	if !ok {
		return "", code, false
	}
	if _, err := os.Stat(rest[0]); err != nil {
		return "", cli.Fail(stderr, op, err), false
	}
	return rest[0], 0, true
}

func infoMain(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := cli.Flags("pdf info", "pdf info <in.pdf>", stderr)
	in, code, ok := input(fs, "pdf info", args, stderr)
	if !ok {
		return code
	}
	res, err := engine.Run(ctx, engine.Cmd{Engine: "pdfinfo", Args: []string{in}, Inputs: []string{in}})
	if err != nil {
		return cli.Fail(stderr, "pdf info", err)
	}
	has, err := HasText(ctx, in)
	if err != nil {
		return cli.Fail(stderr, "pdf info", err)
	}
	stdout.Write(res.Stdout)
	if has {
		fmt.Fprintln(stdout, "Text layer:      yes")
	} else {
		fmt.Fprintln(stdout, "Text layer:      no (scanned? render it with `plate pdf pages` and look)")
	}
	return 0
}

// HasText reports whether any page of path carries extractable text, which
// pdfinfo does not say: it decides between reading the text and rendering
// the pages to look at them.
func HasText(ctx context.Context, path string) (bool, error) {
	text, err := Text(ctx, path)
	return strings.TrimSpace(text) != "", err
}

func textMain(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := cli.Flags("pdf text", "pdf text [--pages N|N-M] <in.pdf>", stderr)
	pages := pagesFlag(fs)
	in, code, ok := input(fs, "pdf text", args, stderr)
	if !ok {
		return code
	}
	a := append([]string{"-layout"}, pages.args()...)
	res, err := engine.Run(ctx, engine.Cmd{Engine: "pdftotext", Args: append(a, in, "-"), Inputs: []string{in}})
	if err != nil {
		return cli.Fail(stderr, "pdf text", err)
	}
	stdout.Write(res.Stdout)
	return 0
}

// outDir makes the destination: --out, created if missing (poppler creates
// none and dies with a message that reads like a corrupt file), else a fresh
// temporary directory. made says the directory is plate's own, to remove
// when the verb fails.
func outDir(out, verb string) (dir string, made bool, err error) {
	if out == "" {
		dir, err = os.MkdirTemp("", "plate-pdf-"+verb+"-")
		return dir, err == nil, err
	}
	if err := os.MkdirAll(out, 0o755); err != nil {
		return "", false, err
	}
	// Absolute, so a directory named like a flag never reaches an engine as one.
	dir, err = filepath.Abs(out)
	return dir, false, err
}

// refuseExisting fails when any of names is already in dir. A destination
// that holds an earlier run's files would mix them with this run's; it is
// refused, never cleared.
func refuseExisting(dir string, names []string) error {
	for _, n := range names {
		if _, err := os.Lstat(filepath.Join(dir, n)); err == nil {
			return fmt.Errorf("%s already holds %s; pass a fresh --out (nothing was deleted)", dir, n)
		}
	}
	return nil
}

func pagesMain(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := cli.Flags("pdf pages", "pdf pages [--pages N|N-M] [--out DIR] [--dpi N] <in.pdf>", stderr)
	pages := pagesFlag(fs)
	out := fs.String("out", "", "write the PNGs here, creating it if missing (default: a fresh temporary directory)")
	dpi := cli.Int(fs, "dpi", 150, fmt.Sprintf("render resolution, 1-%d", maxDPI))
	in, code, ok := input(fs, "pdf pages", args, stderr)
	if !ok {
		return code
	}
	if *dpi < 1 || *dpi > maxDPI {
		return cli.Usage(stderr, "pdf pages", "--dpi %d: want 1-%d", *dpi, maxDPI)
	}
	paths, err := renderPages(ctx, in, *pages, *out, *dpi)
	if err != nil {
		return cli.Fail(stderr, "pdf pages", err)
	}
	fmt.Fprintln(stdout, strings.Join(paths, "\n"))
	return 0
}

// renderPages renders one page per pdftoppm call with -singlefile, so plate
// names each file: pdftoppm's own names pad the page number to the width of
// the document's page count, which a caller cannot know in advance.
func renderPages(ctx context.Context, in string, r pageRange, out string, dpi int) (paths []string, err error) {
	info, err := Read(ctx, in)
	if err != nil {
		return nil, err
	}
	if r.first == 0 {
		r = pageRange{1, info.Pages}
	}
	if r.last > info.Pages {
		return nil, fmt.Errorf("--pages %s: %s has %d pages", r.String(), in, info.Pages)
	}
	width := len(strconv.Itoa(info.Pages))
	var names []string
	for n := r.first; n <= r.last; n++ {
		names = append(names, fmt.Sprintf("page-%0*d.png", width, n))
	}
	dir, made, err := outDir(out, "pages")
	if err != nil {
		return nil, err
	}
	defer func() {
		if err == nil {
			return
		}
		if made {
			os.RemoveAll(dir)
			return
		}
		for _, p := range paths {
			os.Remove(p)
		}
	}()
	if err := refuseExisting(dir, names); err != nil {
		return nil, err
	}
	// Pages render concurrently, each its own pdftoppm call: rendering, not
	// parsing, is the cost, and a single call over the document is no faster.
	// Each page owns its file name, so a failure names the page that failed.
	errs := make([]error, len(names))
	sem := make(chan struct{}, min(maxProcs, runtime.NumCPU()))
	var wg sync.WaitGroup
	for i, name := range names {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			n := strconv.Itoa(r.first + i)
			png := filepath.Join(dir, name)
			root := strings.TrimSuffix(png, ".png")
			_, errs[i] = engine.Run(ctx, engine.Cmd{
				Engine:  "pdftoppm",
				Args:    []string{"-png", "-r", strconv.Itoa(dpi), "-f", n, "-l", n, "-singlefile", in, root},
				Inputs:  []string{in},
				Outputs: []string{png},
			})
		}()
	}
	wg.Wait()
	for _, name := range names {
		paths = append(paths, filepath.Join(dir, name))
	}
	if err := errors.Join(errs...); err != nil {
		return paths, err
	}
	return paths, nil
}

func imagesMain(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := cli.Flags("pdf images", "pdf images [--pages N|N-M] [--out DIR] <in.pdf>", stderr)
	pages := pagesFlag(fs)
	out := fs.String("out", "", "write the images here, creating it if missing (default: a fresh temporary directory)")
	in, code, ok := input(fs, "pdf images", args, stderr)
	if !ok {
		return code
	}
	paths, err := extractImages(ctx, in, *pages, *out)
	if err != nil {
		return cli.Fail(stderr, "pdf images", err)
	}
	fmt.Fprintln(stdout, strings.Join(paths, "\n"))
	return 0
}

// extractImages runs pdfimages into a fresh staging directory inside the
// destination, so what it wrote is exactly what the staging directory holds,
// then moves the files out. pdfimages exits 0 having written nothing when
// the PDF holds no rasters; that is refused here.
func extractImages(ctx context.Context, in string, r pageRange, out string) (paths []string, err error) {
	dir, made, err := outDir(out, "images")
	if err != nil {
		return nil, err
	}
	defer func() {
		if err == nil {
			return
		}
		if made {
			os.RemoveAll(dir)
			return
		}
		for _, p := range paths {
			os.Remove(p)
		}
	}()
	stage, err := os.MkdirTemp(dir, ".plate-images-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(stage)
	a := append([]string{"-all", "-p"}, r.args()...)
	if _, err := engine.Run(ctx, engine.Cmd{Engine: "pdfimages", Args: append(a, in, filepath.Join(stage, "img")), Inputs: []string{in}}); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(stage)
	if err != nil {
		return nil, err
	}
	if len(entries) == 0 {
		return nil, fmt.Errorf("%s holds no embedded images; render its pages with `plate pdf pages`", in)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	if err := refuseExisting(dir, names); err != nil {
		return nil, err
	}
	for _, n := range names {
		p := filepath.Join(dir, n)
		if err := os.Rename(filepath.Join(stage, n), p); err != nil {
			return paths, err
		}
		paths = append(paths, p)
	}
	return paths, nil
}

// RenderPages renders every page of in as a PNG at dpi into the directory
// out, creating it if missing, and returns the paths in page order.
func RenderPages(ctx context.Context, in, out string, dpi int) ([]string, error) {
	return renderPages(ctx, in, pageRange{}, out, dpi)
}
