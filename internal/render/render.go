// Package render turns an HTML page into a PNG and/or a PDF with the pinned
// chrome-headless-shell, guarded against the ways Chrome fails without
// saying so: an output that is missing, the wrong size or silently cut
// short; a PNG that claims 72 dpi; a PDF printed on Letter because the page
// has no matching @page; and a page that rendered its own error text.
package render

import (
	"context"
	"errors"
	"flag"
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
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/lockyc/plate/internal/cli"
	"github.com/lockyc/plate/internal/engine"
	"github.com/lockyc/plate/internal/pdf"
	"github.com/lockyc/plate/internal/raster"
)

const usage = "render [--png out.png [--transparent]] [--jpeg out.jpg [--quality Q]] [--pdf out.pdf] [--html out.html] [--size WxH] [--scale S] [--blank-margin PX] [--budget MS] [--fail-if TEXT]... [--force] <page.html|URL>"

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

// DefaultBudget is --budget's default, in ms. Run takes a Budget of 0 as
// 0, so a caller without its own value passes this.
const DefaultBudget = 5000

// DefaultQuality is --quality's default.
const DefaultQuality = 90

// pdfQuantum is how far, in pt, Chrome's PDF page may sit from the size
// asked for: it rounds page sizes to multiples of 8 CSS px (6 pt).
const pdfQuantum = 6.0

type job struct {
	local       string   // the local page, "" for a remote one
	url         string   // the page as Chrome loads it, set by run from its Load
	args        []string // the Load's base flags, set by run
	png, pdf    string
	html, jpeg  string
	quality     int
	transparent bool
	w, h        int
	scale       float64
	budget      int
	blankMargin int
	failIf      []string
	warn        io.Writer
}

// Options is one render: the page (a path, a file URL that may carry a
// query, or an http(s) URL), its outputs and the flags that shape them.
type Options struct {
	Page           string
	PNG, PDF, HTML string
	JPEG           string // the PNG screenshot converted by ImageMagick; needs a size
	Quality        *int   // the JPEG's quality, 1–100; nil is DefaultQuality
	W, H           int
	Scale          float64
	Budget         int
	BlankMargin    int // refuse ink within this many CSS px of the page edge; 0 is off
	FailIf         []string
	Transparent    bool
	Force          bool // render even when the cache says nothing changed
}

// usageError is a wrong combination of options, which Main reports with the
// usage line and exit status 2.
type usageError string

func (e usageError) Error() string { return string(e) }

// Outputs lists o's outputs as given, PNG, PDF, HTML then JPEG.
func (o Options) Outputs() []string {
	var out []string
	for _, p := range []string{o.PNG, o.PDF, o.HTML, o.JPEG} {
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// Validate refuses a combination of options no render can satisfy, as a
// usage error. Run calls it first; a caller that queues renders calls it up
// front, so a bad one is refused before any of them start. The messages
// name render's flags.
func (o Options) Validate() error {
	if len(o.Outputs()) == 0 {
		return usageError("give --png, --jpeg, --pdf, --html or a combination")
	}
	if o.Transparent && o.JPEG != "" {
		return usageError("--transparent can't be combined with --jpeg: a JPEG has no alpha channel")
	}
	if o.Transparent && o.PNG == "" {
		return usageError("--transparent needs --png")
	}
	if o.JPEG == "" && o.Quality != nil {
		return usageError("--quality needs --jpeg")
	}
	if o.JPEG != "" && o.Quality != nil && (*o.Quality < 1 || *o.Quality > 100) {
		return usageError("--quality must be between 1 and 100")
	}
	if o.JPEG != "" && o.W == 0 {
		return usageError("--jpeg needs --size")
	}
	if o.W < 0 || o.H < 0 || (o.W == 0) != (o.H == 0) {
		return usageError("--size wants WxH, both above 0")
	}
	if o.PNG != "" && o.W == 0 {
		return usageError("--png needs --size")
	}
	if math.IsNaN(o.Scale) || math.IsInf(o.Scale, 0) || o.Scale <= 0 || o.Budget < 0 {
		return usageError("--scale must be a finite number above 0 and --budget not below 0")
	}
	if o.BlankMargin < 0 {
		return usageError("--blank-margin must not be below 0")
	}
	if o.BlankMargin > 0 && o.PNG == "" && o.PDF == "" && o.JPEG == "" {
		return usageError("--blank-margin needs --png, --jpeg or --pdf: it checks a raster")
	}
	named := [][2]string{{"png", o.PNG}, {"pdf", o.PDF}, {"html", o.HTML}, {"jpeg", o.JPEG}}
	for _, p := range named {
		for _, q := range named {
			if p[0] < q[0] && p[1] != "" && q[1] != "" && engine.SameFile(p[1], q[1]) {
				return usageError("--" + p[0] + " and --" + q[0] + " name the same file")
			}
		}
	}
	return nil
}

// Run renders o through Cached, so an unchanged render is skipped without
// starting Chrome. Its key is o with the page canonical, outputs absolute
// and Force left out, since Force only says whether to look.
//
// Only the CLI goes through Run; the library entry points PDF and PNG stay
// uncached.
func Run(ctx context.Context, o Options, warn io.Writer) (Result, error) {
	if err := o.Validate(); err != nil {
		return Result{}, err
	}
	if o.JPEG != "" && o.Quality == nil {
		q := DefaultQuality
		o.Quality = &q
	}
	u, local, query, err := pageURL(o.Page)
	if err != nil {
		return Result{}, err
	}
	key := o
	key.Page, key.Force = address(u, local, query), false
	for _, p := range []*string{&key.PNG, &key.PDF, &key.HTML, &key.JPEG} {
		if *p != "" {
			if *p, err = filepath.Abs(*p); err != nil {
				return Result{}, err
			}
		}
	}
	j := job{local: local, png: o.PNG, pdf: o.PDF, html: o.HTML, jpeg: o.JPEG, quality: qualityOf(o), transparent: o.Transparent,
		w: o.W, h: o.H, scale: o.Scale, budget: o.Budget, blankMargin: o.BlankMargin, failIf: o.FailIf, warn: warn}
	return Cached(ctx, "render", o.Page, key, key.Outputs(), o.Force, warn, j.run)
}

// Main runs `plate render`.
func Main(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := cli.Flags("render", usage, stderr)
	pngOut := fs.String("png", "", "write a PNG screenshot here")
	pdfOut := fs.String("pdf", "", "write a PDF here")
	htmlOut := fs.String("html", "", "write the page's DOM here once scripts settle (a static copy with every script's work done)")
	jpegOut := fs.String("jpeg", "", "write the screenshot as a JPEG here (needs --size); the PNG is kept only with --png")
	quality := cli.Int(fs, "quality", DefaultQuality, "JPEG quality, 1–100 (needs --jpeg)")
	transparent := fs.Bool("transparent", false, "give the PNG an alpha channel, with the page's transparent areas left transparent rather than white")
	size := fs.String("size", "", "viewport and expected page size in CSS px, WxH (required with --png)")
	scale := cli.Float(fs, "scale", 1, "device scale factor; the PNG is size × scale px at 96 × scale dpi")
	budget := cli.Int(fs, "budget", DefaultBudget, "virtual-time budget in ms for scripts, fonts and images to settle")
	var failIf cli.Multi
	fs.Var(&failIf, "fail-if", "fail when `TEXT` appears in the page's markup (dumped DOM) or, with --pdf, the PDF's extracted text (repeatable)")
	blankMargin := cli.Int(fs, "blank-margin", 0, "fail when ink sits within `PX` CSS px of a page edge, checked on the PDF's pages at 150 dpi or else the PNG (0 is off)")
	force := fs.Bool("force", false, "render even when nothing the page loaded and no output has changed since its last success")
	rest, code, ok := cli.Parse(fs, args, 1)
	if !ok {
		return code
	}
	usageErr := func(msg string) int {
		defer fs.Usage()
		return cli.Usage(stderr, "render", "%s", msg)
	}
	o := Options{Page: rest[0], PNG: *pngOut, PDF: *pdfOut, HTML: *htmlOut, JPEG: *jpegOut, Transparent: *transparent,
		Scale: *scale, Budget: *budget, BlankMargin: *blankMargin, FailIf: failIf, Force: *force}
	// An explicit --quality is passed as given; Validate and Run own the rules and the default.
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "quality" {
			o.Quality = quality
		}
	})
	if *size != "" {
		w, h, err := ParseSize(*size)
		if err != nil {
			return usageErr("--" + err.Error())
		}
		o.W, o.H = w, h
	}
	res, err := Run(ctx, o, stderr)
	if ue := usageError(""); errors.As(err, &ue) {
		return usageErr(ue.Error())
	}
	if err != nil {
		return cli.Fail(stderr, "render", err)
	}
	if res.Outcome == Unchanged {
		fmt.Fprintf(stderr, "plate render: unchanged: %s\n", o.Outputs()[0])
	}
	return 0
}

// PDF renders the page at path (a file or URL) to a PDF at out, under the
// checks `plate render --pdf` runs. warn receives its warnings.
func PDF(ctx context.Context, page, out string, warn io.Writer) error {
	u, local, query, err := pageURL(page)
	if err != nil {
		return err
	}
	_, _, err = loadPage(ctx, u, local, query, job{local: local, pdf: out, scale: 1, budget: DefaultBudget, warn: warn}.run)
	return err
}

// PNG renders the page at path (a file or URL) to a w×h CSS px PNG at out,
// at device scale factor scale, under the checks `plate render --png` runs.
func PNG(ctx context.Context, page, out string, w, h int, scale float64) error {
	return pageToPNG(ctx, page, out, w, h, scale, false)
}

// PNGAlpha is PNG with the page's transparent areas left transparent, under
// the checks `plate render --png --transparent` runs.
func PNGAlpha(ctx context.Context, page, out string, w, h int, scale float64) error {
	return pageToPNG(ctx, page, out, w, h, scale, true)
}

func pageToPNG(ctx context.Context, page, out string, w, h int, scale float64, transparent bool) error {
	if w <= 0 || h <= 0 || !(scale > 0) || math.IsInf(scale, 0) {
		return fmt.Errorf("render %dx%d at scale %g: want a positive size and scale", w, h, scale)
	}
	u, local, query, err := pageURL(page)
	if err != nil {
		return err
	}
	_, _, err = loadPage(ctx, u, local, query, job{local: local, png: out, w: w, h: h, scale: scale, transparent: transparent, budget: DefaultBudget, warn: io.Discard}.run)
	return err
}

// ParseSize reads a size written WxH in CSS px, both sides above 0. A
// written size is never 0x0: Options takes 0x0 as no size at all, which
// Validate cannot tell from a size someone wrote, so the refusal is here.
func ParseSize(s string) (w, h int, err error) {
	ws, hs, ok := strings.Cut(s, "x")
	w, err1 := strconv.Atoi(ws)
	h, err2 := strconv.Atoi(hs)
	if !ok || err1 != nil || err2 != nil || w <= 0 || h <= 0 {
		return 0, 0, fmt.Errorf("size %q: want WxH in CSS px, e.g. 1200x900", s)
	}
	return w, h, nil
}

// pageURL returns the page's URL, the local file behind it ("" for http and
// https) and, for a file URL, its query with the "?" (a plain path has none).
func pageURL(arg string) (string, string, string, error) {
	if u, err := url.Parse(arg); err == nil {
		switch u.Scheme {
		case "http", "https":
			return arg, "", "", nil
		case "file":
			local := filepath.Clean(filepath.FromSlash(u.Path))
			if _, err := os.Stat(local); err != nil {
				return "", "", "", err
			}
			query := ""
			if u.RawQuery != "" || u.ForceQuery {
				query = "?" + u.RawQuery
			}
			return arg, local, query, nil
		}
	}
	abs, err := filepath.Abs(arg)
	if err != nil {
		return "", "", "", err
	}
	if _, err := os.Stat(abs); err != nil {
		return "", "", "", err
	}
	return (&url.URL{Scheme: "file", Path: abs}).String(), abs, "", nil
}

// PageAddress is page's canonical address, as a cache key names it: PlateURL
// of a local page's absolute path and query, or the remote URL.
func PageAddress(page string) (string, error) {
	u, local, query, err := pageURL(page)
	if err != nil {
		return "", err
	}
	return address(u, local, query), nil
}

// address is the canonical address of what pageURL returned: PlateURL of a
// local page, or the remote URL.
func address(u, local, query string) string {
	if local == "" {
		return u
	}
	return PlateURL(local, query)
}

// inputs lists the local page as an engine input, so the engine refuses an
// output that is the page itself.
func (j job) inputs() []string {
	if j.local == "" {
		return nil
	}
	return []string{j.local}
}

// run renders the page as l loads it and returns the sha256 of each output
// it wrote under its absolute destination.
//
// Each output is written and checked under a temporary name beside its
// destination, hashed there, and only then renamed into place. The hash is
// therefore of this render's bytes even when another writer (a parallel
// target with the same output) lands on the destination meanwhile; hashing
// the destination afterwards would record that writer's bytes under this
// render's key.
func (j job) run(ctx context.Context, l Load) (_ []FileHash, err error) {
	j.url, j.args = l.URL, l.ChromeArgs
	var outputs []outFile
	for _, p := range []*string{&j.png, &j.pdf, &j.html, &j.jpeg} {
		if *p != "" {
			outputs = append(outputs, outFile{slot: p, dest: *p})
		}
	}
	if j.local != "" {
		for _, o := range outputs {
			if engine.SameFile(j.local, o.dest) {
				return nil, fmt.Errorf("%s is the page being rendered; write the result elsewhere", o.dest)
			}
		}
	}
	// Every output goes first: whichever stage fails, no stale file from an
	// earlier render may sit beside a fresh one.
	for _, o := range outputs {
		if err := os.Remove(o.dest); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return nil, err
		}
	}
	// A JPEG without a PNG is converted from a screenshot that is only a
	// scratch file: temporary like the outputs, but never hashed, recorded or
	// moved into place. Its dest names the JPEG, so an error from the
	// screenshot points at the file the caller asked for.
	if j.jpeg != "" && j.png == "" {
		outputs = append(outputs, outFile{slot: &j.png, dest: j.jpeg, scratch: true})
	}
	defer func() {
		for _, o := range outputs {
			if o.tmp != "" {
				os.Remove(o.tmp)
			}
			if err != nil && !o.scratch {
				os.Remove(o.dest)
			}
		}
		if err != nil {
			err = destErr{err, outputs}
		}
	}()
	for i := range outputs {
		name := outputs[i].dest
		if outputs[i].scratch {
			name += ".png"
		}
		if outputs[i].tmp, err = tempName(name); err != nil {
			return nil, err
		}
		*outputs[i].slot = outputs[i].tmp
	}
	if j.png != "" {
		if err := j.screenshot(ctx); err != nil {
			return nil, err
		}
	}
	if j.pdf != "" {
		if err := j.print(ctx); err != nil {
			return nil, err
		}
	}
	if j.html != "" {
		if err := j.dump(ctx); err != nil {
			return nil, err
		}
	}
	if j.jpeg != "" {
		if err := JPEG(ctx, j.png, j.jpeg, j.quality); err != nil {
			return nil, err
		}
	}
	if j.blankMargin > 0 {
		if err := j.checkMargins(ctx); err != nil {
			return nil, err
		}
	}
	var hashes []FileHash
	for _, o := range outputs {
		if o.scratch {
			continue
		}
		abs, err := filepath.Abs(o.dest)
		if err != nil {
			return nil, err
		}
		h := FileHash{abs, HashFile(o.tmp)}
		if h.SHA256 == "" {
			return nil, fmt.Errorf("%s vanished before it could be moved to %s", o.tmp, o.dest)
		}
		hashes = append(hashes, h)
	}
	for _, o := range outputs {
		if o.scratch {
			continue
		}
		if err := os.Rename(o.tmp, o.dest); err != nil {
			return nil, err
		}
	}
	return hashes, nil
}

// outFile is one output of a run: the destination asked for and the
// temporary name it is written under until every check has passed.
type outFile struct {
	slot      *string // the job field, pointed at tmp while the stages run
	dest, tmp string
	scratch   bool // an intermediate: removed at the end, never an output
}

// destErr is a run's error with each temporary name replaced by its
// destination, so the message names the file the caller asked for. Unwrap
// keeps the original for errors.Is and errors.As.
type destErr struct {
	err  error
	outs []outFile
}

func (e destErr) Error() string {
	msg := e.err.Error()
	for _, o := range e.outs {
		if o.tmp != "" {
			msg = strings.ReplaceAll(msg, o.tmp, o.dest)
		}
	}
	return msg
}

func (e destErr) Unwrap() error { return e.err }

// tempName is an unused name beside dest, keeping its extension, for an
// output to be written under before it is moved into place. The name is
// reserved and then freed, so the writer creates the file with its usual
// mode rather than CreateTemp's 0600.
func tempName(dest string) (string, error) {
	ext := filepath.Ext(dest)
	if strings.Contains(ext, "*") {
		ext = ""
	}
	f, err := os.CreateTemp(filepath.Dir(dest), ".plate-*"+ext)
	if err != nil {
		return "", err
	}
	f.Close()
	return f.Name(), os.Remove(f.Name())
}

// baseArgs is every Chrome call's base flags, with proxy the
// --proxy-server flag for a local page and "" for a remote one. Never
// --user-data-dir: with it, headless Chrome hangs after writing the
// screenshot.
//
// A local page is an http page on plate's proxy, so there is no
// --allow-file-access-from-files: it would only let a remote page read local
// files. Chrome skips a proxy for loopback hosts unless told otherwise, so
// the bypass list is emptied of them.
func baseArgs(proxy string) []string {
	args := []string{"--headless", "--disable-gpu", "--no-sandbox", "--hide-scrollbars"}
	if proxy != "" {
		args = append(args, proxy, "--proxy-bypass-list=<-loopback>")
	}
	return args
}

// base is the Load's flags with the virtual-time budget. That fast-forwards
// timers only on chrome-headless-shell, which is why that is the pinned
// engine and not full Chrome.
func (j job) base() []string {
	return slices.Concat(j.args, []string{"--virtual-time-budget=" + strconv.Itoa(j.budget)})
}

// CheckSize refuses a w×h CSS px capture at scale whose pixels go beyond
// the largest screenshot verified whole.
func CheckSize(w, h int, scale float64) error {
	fw, fh := math.Round(float64(w)*scale), math.Round(float64(h)*scale)
	if fw > maxSide || fh > maxSide || fw*fh > maxPixels {
		return fmt.Errorf("render would be %gx%g px (%.1f MP), beyond the %dx%d px verified whole on %s; render smaller or at a lower --scale",
			fw, fh, fw*fh/1e6, maxSide, maxSide, chrome)
	}
	return nil
}

func (j job) screenshot(ctx context.Context) error {
	if err := CheckSize(j.w, j.h, j.scale); err != nil {
		return err
	}
	pw, ph := int(math.Round(float64(j.w)*j.scale)), int(math.Round(float64(j.h)*j.scale))
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
	res, err := j.chromeRun(ctx, engine.Cmd{Args: append(args, j.url), Outputs: []string{j.png}})
	if err != nil {
		return err
	}
	if err := Sentinel(string(res.Stdout), j.failIf); err != nil {
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
		return fmt.Errorf("%s: Chrome rendered %dx%d, expected %dx%d (size × scale)", j.png, cfg.Width, cfg.Height, pw, ph)
	}
	if j.transparent && cfg.ColorModel != color.NRGBAModel && cfg.ColorModel != color.NRGBA64Model {
		return fmt.Errorf("%s: Chrome wrote a PNG without an alpha channel for --transparent", j.png)
	}
	// Chrome writes no pHYs, so the PNG would claim 72 dpi and misstate its
	// print size. A CSS px is 1/96 in, so the true density is 96 × scale.
	return raster.SetDPI(j.png, 96*j.scale)
}

// JPEG converts the PNG png to the JPEG jpeg at quality, stripped of
// metadata.
func JPEG(ctx context.Context, png, jpeg string, quality int) error {
	_, err := engine.Run(ctx, engine.Cmd{Engine: "magick",
		Args:   []string{png, "-strip", "-quality", strconv.Itoa(quality), jpeg},
		Inputs: []string{png}, Outputs: []string{jpeg}, Timeout: 15 * time.Minute})
	return err
}

func (j job) print(ctx context.Context) error {
	if j.w != 0 && (j.w%8 != 0 || j.h%8 != 0) {
		fmt.Fprintf(j.warn, "plate render: warning: Chrome rounds PDF page sizes to multiples of 8 CSS px, so %dx%d will not print at exactly that size\n", j.w, j.h)
	}
	stamp, err := pdf.SourceDate()
	if err != nil {
		return err
	}
	args := append(j.base(), "--no-pdf-header-footer", "--print-to-pdf="+j.pdf)
	if len(j.failIf) > 0 {
		// The DOM comes from the same load the PDF prints. Attribute
		// markers never reach the PDF's text, so the text alone cannot see
		// them.
		args = append(args, "--dump-dom")
	}
	res, err := j.chromeRun(ctx, engine.Cmd{Args: append(args, j.url), Outputs: []string{j.pdf}})
	if err != nil {
		return err
	}
	if err := pdf.PinDates(j.pdf, stamp); err != nil {
		return err
	}
	if len(j.failIf) > 0 {
		if err := Sentinel(string(res.Stdout), j.failIf); err != nil {
			return err
		}
		text, err := pdf.Text(ctx, j.pdf)
		if err != nil {
			return err
		}
		if err := Sentinel(text, j.failIf); err != nil {
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
	// A named @page can size one page apart from the rest, so every page
	// is checked, not only the first pdf.Read reports.
	sizes := [][2]float64{{info.W, info.H}}
	if info.Pages > 1 {
		if sizes, err = pdf.PageSizes(ctx, j.pdf, info.Pages); err != nil {
			return err
		}
	}
	ww, wh := float64(j.w)*0.75, float64(j.h)*0.75
	for i, s := range sizes {
		if math.Abs(s[0]-ww) > pdfQuantum || math.Abs(s[1]-wh) > pdfQuantum {
			return fmt.Errorf("%s: PDF page %d is %gx%g pt, not the %dx%d CSS px (%gx%g pt) asked for; give the page `@page { size: %dpx %dpx; margin: 0 }`",
				j.pdf, i+1, s[0], s[1], j.w, j.h, ww, wh, j.w, j.h)
		}
	}
	return nil
}

// dump writes the DOM Chrome holds once the virtual-time budget has run.
// Chrome's exit status is not the verdict: its teardown watchdog can exit
// non-zero after writing the whole document, so a complete page — one that
// ends in </html> — is the test.
func (j job) dump(ctx context.Context) error {
	args := append(j.base(), "--dump-dom", j.url)
	res, err := j.chromeRun(ctx, engine.Cmd{Args: args, AllowExit: true})
	if err != nil {
		return err
	}
	dom := strings.TrimSpace(string(res.Stdout))
	if !strings.HasSuffix(dom, "</html>") {
		return fmt.Errorf("Chrome produced no complete page (%d bytes, no closing </html>)", len(dom))
	}
	if err := Sentinel(dom, j.failIf); err != nil {
		return err
	}
	return os.WriteFile(j.html, []byte(dom), 0o644)
}

// blockedRe is the console line Chrome logs on stderr, and nowhere else,
// when it refuses a file:// subresource of an http page, which every local
// page is under plate's proxy.
var blockedRe = regexp.MustCompile(`Not allowed to load local resource: ([^\s"]+)`)

// chromeRun runs Chrome with args and fails when it refused a file://
// subresource: Chrome draws the page without it and exits 0.
func (j job) chromeRun(ctx context.Context, c engine.Cmd) (engine.Result, error) {
	c.Engine, c.Inputs, c.Timeout = chrome, j.inputs(), 15*time.Minute
	res, err := engine.Run(ctx, c)
	if err != nil {
		return res, err
	}
	var urls []string
	for _, m := range blockedRe.FindAllStringSubmatch(string(res.Stderr), -1) {
		if !slices.Contains(urls, m[1]) {
			urls = append(urls, m[1])
		}
	}
	if len(urls) > 0 {
		return res, fmt.Errorf("Chrome blocked %s: a local page cannot load file:// URLs; reference the file by a relative or absolute path", strings.Join(urls, ", "))
	}
	return res, nil
}

// Sentinel refuses text that contains any of the --fail-if strings.
func Sentinel(text string, failIf []string) error {
	for _, s := range failIf {
		if strings.Contains(text, s) {
			return fmt.Errorf("the rendered page contains %q", s)
		}
	}
	return nil
}

// qualityOf is the JPEG quality of o after Run has applied the default, 0
// when there is no JPEG.
func qualityOf(o Options) int {
	if o.Quality == nil {
		return 0
	}
	return *o.Quality
}
