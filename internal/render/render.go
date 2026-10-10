// Package render turns an HTML page into a PNG and/or a PDF with the pinned
// chrome-headless-shell, guarded against the ways Chrome fails without
// saying so: an output that is missing, the wrong size or silently cut
// short; a PNG that claims 72 dpi; a PDF printed on Letter because the page
// has no matching @page; and a page that rendered its own error text.
package render

import (
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	_ "image/png"
	"io"
	"io/fs"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/lockyc/plate/internal/cli"
	"github.com/lockyc/plate/internal/engine"
	"github.com/lockyc/plate/internal/pdf"
	"github.com/lockyc/plate/internal/raster"
)

const usage = "render [--png out.png [--transparent]] [--pdf out.pdf] [--html out.html] [--size WxH] [--scale S] [--budget MS] [--fail-if TEXT]... <page.html|URL>"

const chrome = "chrome-headless-shell"

// maxSide and maxPixels bound a PNG to the largest screenshot verified whole
// on the pinned chrome-headless-shell: 16384×16384 device px of a full-bleed
// photo, with a sentinel in the far corner. Chrome has cut larger
// screenshots short with a valid header and exit 0, so a size nobody has
// verified is refused rather than trusted.
const (
	maxSide   = 16384
	maxPixels = 16384 * 16384
)

// defaultBudget is --budget's default, in ms.
const defaultBudget = 5000

// pdfQuantum is how far, in pt, Chrome's PDF page may sit from the size
// asked for: it rounds page sizes to multiples of 8 CSS px (6 pt).
const pdfQuantum = 6.0

type multi []string

func (m *multi) String() string     { return strings.Join(*m, ", ") }
func (m *multi) Set(s string) error { *m = append(*m, s); return nil }

type job struct {
	url, local  string
	png, pdf    string
	html        string
	transparent bool
	w, h        int
	scale       float64
	budget      int
	failIf      []string
	warn        io.Writer
}

// Main runs `plate render`.
func Main(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := cli.Flags("render", usage, stderr)
	pngOut := fs.String("png", "", "write a PNG screenshot here")
	pdfOut := fs.String("pdf", "", "write a PDF here")
	htmlOut := fs.String("html", "", "write the page's DOM here once scripts settle (a static copy with every script's work done)")
	transparent := fs.Bool("transparent", false, "give the PNG an alpha channel, with the page's transparent areas left transparent rather than white")
	size := fs.String("size", "", "viewport and expected page size in CSS px, WxH (required with --png)")
	scale := cli.Float(fs, "scale", 1, "device scale factor; the PNG is size × scale px at 96 × scale dpi")
	budget := cli.Int(fs, "budget", defaultBudget, "virtual-time budget in ms for scripts, fonts and images to settle")
	var failIf multi
	fs.Var(&failIf, "fail-if", "fail when `TEXT` appears in the page: its markup (dumped DOM) for --png, its extracted text for --pdf (repeatable)")
	rest, code, ok := cli.Parse(fs, args, 1)
	if !ok {
		return code
	}
	j := job{png: *pngOut, pdf: *pdfOut, html: *htmlOut, transparent: *transparent, scale: *scale, budget: *budget, failIf: failIf, warn: stderr}
	usageErr := func(msg string) int {
		defer fs.Usage()
		return cli.Usage(stderr, "render", "%s", msg)
	}
	if j.png == "" && j.pdf == "" && j.html == "" {
		return usageErr("give --png, --pdf, --html or a combination")
	}
	if j.transparent && j.png == "" {
		return usageErr("--transparent needs --png")
	}
	if *size != "" {
		w, h, err := parseSize(*size)
		if err != nil {
			return usageErr(err.Error())
		}
		j.w, j.h = w, h
	}
	if j.png != "" && j.w == 0 {
		return usageErr("--png needs --size")
	}
	if math.IsNaN(j.scale) || math.IsInf(j.scale, 0) || j.scale <= 0 || j.budget < 0 {
		return usageErr("--scale must be a finite number above 0 and --budget not below 0")
	}
	for _, p := range [][2]string{{"png", j.png}, {"pdf", j.pdf}, {"html", j.html}} {
		for _, q := range [][2]string{{"png", j.png}, {"pdf", j.pdf}, {"html", j.html}} {
			if p[0] < q[0] && p[1] != "" && q[1] != "" && engine.SameFile(p[1], q[1]) {
				return usageErr("--" + p[0] + " and --" + q[0] + " name the same file")
			}
		}
	}
	u, local, err := pageURL(rest[0])
	if err != nil {
		return cli.Fail(stderr, "render", err)
	}
	j.url, j.local = u, local
	if err := j.run(ctx); err != nil {
		return cli.Fail(stderr, "render", err)
	}
	return 0
}

// PDF renders the page at path (a file or URL) to a PDF at out, under the
// checks `plate render --pdf` runs. warn receives its warnings.
func PDF(ctx context.Context, page, out string, warn io.Writer) error {
	u, local, err := pageURL(page)
	if err != nil {
		return err
	}
	return job{url: u, local: local, pdf: out, scale: 1, budget: defaultBudget, warn: warn}.run(ctx)
}

// PNG renders the page at path (a file or URL) to a w×h CSS px PNG at out,
// at device scale factor scale, under the checks `plate render --png` runs.
func PNG(ctx context.Context, page, out string, w, h int, scale float64) error {
	if w <= 0 || h <= 0 || !(scale > 0) || math.IsInf(scale, 0) {
		return fmt.Errorf("render %dx%d at scale %g: want a positive size and scale", w, h, scale)
	}
	u, local, err := pageURL(page)
	if err != nil {
		return err
	}
	return job{url: u, local: local, png: out, w: w, h: h, scale: scale, budget: defaultBudget, warn: io.Discard}.run(ctx)
}

func parseSize(s string) (int, int, error) {
	ws, hs, ok := strings.Cut(s, "x")
	w, err1 := strconv.Atoi(ws)
	h, err2 := strconv.Atoi(hs)
	if !ok || err1 != nil || err2 != nil || w <= 0 || h <= 0 {
		return 0, 0, fmt.Errorf("--size %q: want WxH in CSS px, e.g. 1200x900", s)
	}
	return w, h, nil
}

// pageURL returns the URL Chrome loads and the local file behind it ("" for
// http and https).
func pageURL(arg string) (string, string, error) {
	if u, err := url.Parse(arg); err == nil {
		switch u.Scheme {
		case "http", "https":
			return arg, "", nil
		case "file":
			local := filepath.FromSlash(u.Path)
			if _, err := os.Stat(local); err != nil {
				return "", "", err
			}
			return arg, local, nil
		}
	}
	abs, err := filepath.Abs(arg)
	if err != nil {
		return "", "", err
	}
	if _, err := os.Stat(abs); err != nil {
		return "", "", err
	}
	return (&url.URL{Scheme: "file", Path: abs}).String(), abs, nil
}

// inputs lists the local page as an engine input, so the engine refuses an
// output that is the page itself.
func (j job) inputs() []string {
	if j.local == "" {
		return nil
	}
	return []string{j.local}
}

func (j job) run(ctx context.Context) (err error) {
	var outputs []string
	for _, o := range []string{j.png, j.pdf, j.html} {
		if o != "" {
			outputs = append(outputs, o)
		}
	}
	if j.local != "" {
		for _, o := range outputs {
			if engine.SameFile(j.local, o) {
				return fmt.Errorf("%s is the page being rendered; write the result elsewhere", o)
			}
		}
	}
	// Both outputs go first: whichever stage fails, no stale file from an
	// earlier render may sit beside a fresh one.
	for _, o := range outputs {
		if err := os.Remove(o); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	defer func() {
		if err != nil {
			for _, o := range outputs {
				os.Remove(o)
			}
		}
	}()
	if j.png != "" {
		if err := j.screenshot(ctx); err != nil {
			return err
		}
	}
	if j.pdf != "" {
		if err := j.print(ctx); err != nil {
			return err
		}
	}
	if j.html != "" {
		if err := j.dump(ctx); err != nil {
			return err
		}
	}
	return nil
}

// base is every Chrome call's flags. Never --user-data-dir: with it,
// headless Chrome hangs after writing the screenshot. --virtual-time-budget
// fast-forwards timers only on chrome-headless-shell, which is why that is
// the pinned engine and not full Chrome.
func (j job) base() []string {
	return []string{"--headless", "--disable-gpu", "--no-sandbox", "--allow-file-access-from-files",
		"--hide-scrollbars", "--virtual-time-budget=" + strconv.Itoa(j.budget)}
}

func (j job) screenshot(ctx context.Context) error {
	fw, fh := math.Round(float64(j.w)*j.scale), math.Round(float64(j.h)*j.scale)
	if fw > maxSide || fh > maxSide || fw*fh > maxPixels {
		return fmt.Errorf("render would be %gx%g px (%.1f MP), beyond the %dx%d px verified whole on %s; render smaller or at a lower --scale",
			fw, fh, fw*fh/1e6, maxSide, maxSide, chrome)
	}
	pw, ph := int(fw), int(fh)
	args := append(j.base(),
		"--force-device-scale-factor="+strconv.FormatFloat(j.scale, 'f', -1, 64),
		fmt.Sprintf("--window-size=%d,%d", j.w, j.h),
		"--screenshot="+j.png)
	if j.transparent {
		// Chrome paints an opaque white ground under the page unless its
		// default background is itself transparent.
		args = append(args, "--default-background-color=00000000")
	}
	if len(j.failIf) > 0 {
		// The DOM comes from the same page load the screenshot shows.
		args = append(args, "--dump-dom")
	}
	res, err := engine.Run(ctx, engine.Cmd{Engine: chrome, Args: append(args, j.url), Inputs: j.inputs(), Outputs: []string{j.png}, Timeout: 15 * time.Minute})
	if err != nil {
		return err
	}
	if err := sentinel(string(res.Stdout), j.failIf); err != nil {
		return err
	}
	f, err := os.Open(j.png)
	if err != nil {
		return err
	}
	cfg, _, err := image.DecodeConfig(f)
	f.Close()
	if err != nil {
		return fmt.Errorf("%s: %w", j.png, err)
	}
	if cfg.Width != pw || cfg.Height != ph {
		return fmt.Errorf("Chrome rendered %dx%d, expected %dx%d (size × scale)", cfg.Width, cfg.Height, pw, ph)
	}
	if j.transparent && cfg.ColorModel != color.NRGBAModel && cfg.ColorModel != color.NRGBA64Model {
		return fmt.Errorf("Chrome wrote a PNG without an alpha channel for --transparent")
	}
	// Chrome writes no pHYs, so the PNG would claim 72 dpi and misstate its
	// print size. A CSS px is 1/96 in, so the true density is 96 × scale.
	return raster.SetDPI(j.png, 96*j.scale)
}

func (j job) print(ctx context.Context) error {
	if j.w != 0 && (j.w%8 != 0 || j.h%8 != 0) {
		fmt.Fprintf(j.warn, "plate render: warning: Chrome rounds PDF page sizes to multiples of 8 CSS px, so %dx%d will not print at exactly that size\n", j.w, j.h)
	}
	args := append(j.base(), "--no-pdf-header-footer", "--print-to-pdf="+j.pdf, j.url)
	if _, err := engine.Run(ctx, engine.Cmd{Engine: chrome, Args: args, Inputs: j.inputs(), Outputs: []string{j.pdf}, Timeout: 15 * time.Minute}); err != nil {
		return err
	}
	if len(j.failIf) > 0 {
		text, err := pdf.Text(ctx, j.pdf)
		if err != nil {
			return err
		}
		if err := sentinel(text, j.failIf); err != nil {
			return err
		}
	}
	if j.w == 0 {
		return nil
	}
	info, err := pdf.Read(ctx, j.pdf)
	if err != nil {
		return err
	}
	ww, wh := float64(j.w)*0.75, float64(j.h)*0.75
	if math.Abs(info.W-ww) > pdfQuantum || math.Abs(info.H-wh) > pdfQuantum {
		return fmt.Errorf("the PDF page is %gx%g pt, not the %dx%d CSS px (%gx%g pt) asked for; give the page `@page { size: %dpx %dpx; margin: 0 }`",
			info.W, info.H, j.w, j.h, ww, wh, j.w, j.h)
	}
	return nil
}

// dump writes the DOM Chrome holds once the virtual-time budget has run.
// Chrome's exit status is not the verdict: its teardown watchdog can exit
// non-zero after writing the whole document, so a complete page — one that
// ends in </html> — is the test.
func (j job) dump(ctx context.Context) error {
	args := append(j.base(), "--dump-dom", j.url)
	res, err := engine.Run(ctx, engine.Cmd{Engine: chrome, Args: args, Inputs: j.inputs(), Timeout: 15 * time.Minute, AllowExit: true})
	if err != nil {
		return err
	}
	dom := strings.TrimSpace(string(res.Stdout))
	if !strings.HasSuffix(dom, "</html>") {
		return fmt.Errorf("Chrome produced no complete page (%d bytes, no closing </html>)", len(dom))
	}
	if err := sentinel(dom, j.failIf); err != nil {
		return err
	}
	return os.WriteFile(j.html, []byte(dom), 0o644)
}

func sentinel(text string, failIf []string) error {
	for _, s := range failIf {
		if strings.Contains(text, s) {
			return fmt.Errorf("the rendered page contains %q", s)
		}
	}
	return nil
}
