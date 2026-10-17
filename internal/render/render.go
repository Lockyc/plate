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

// defaultQuality is --quality's default.
const defaultQuality = 90

// pdfQuantum is how far, in pt, Chrome's PDF page may sit from the size
// asked for: it rounds page sizes to multiples of 8 CSS px (6 pt).
const pdfQuantum = 6.0

type multi []string

func (m *multi) String() string     { return strings.Join(*m, ", ") }
func (m *multi) Set(s string) error { *m = append(*m, s); return nil }

type job struct {
	url, local  string // url is the remote page, or the file URL before run swaps in the proxied one
	query       string // the local page's "?…", kept
	proxy       string // the --proxy-server flag, set by run for a local page
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
	Quality        *int   // the JPEG's quality, 1–100; nil is defaultQuality
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

// outputs lists o's outputs in the order run hashes them: PNG, PDF, HTML
// then JPEG.
func (o Options) outputs() []string {
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
	if len(o.outputs()) == 0 {
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

// Run renders o through the render cache: when the record of its last
// success still matches every file it loaded and every file it wrote, the
// render is skipped without starting Chrome and skipped is true. Force
// renders regardless and still records. A remote page, a page that reached
// another host, or a file that changed while it was served leaves no
// record, so it renders every time. The old record goes before Chrome
// starts, so a render that fails or is killed never leaves one behind.
//
// Only the CLI goes through Run; the library entry points PDF and PNG stay
// uncached.
func Run(ctx context.Context, o Options, warn io.Writer) (Result, error) {
	if err := o.Validate(); err != nil {
		return Result{}, err
	}
	if o.JPEG != "" && o.Quality == nil {
		q := defaultQuality
		o.Quality = &q
	}
	u, local, query, err := pageURL(o.Page)
	if err != nil {
		return Result{}, err
	}
	j := job{url: u, local: local, query: query, png: o.PNG, pdf: o.PDF, html: o.HTML, jpeg: o.JPEG, quality: qualityOf(o), transparent: o.Transparent,
		w: o.W, h: o.H, scale: o.Scale, budget: o.Budget, blankMargin: o.BlankMargin, failIf: o.FailIf, warn: warn}
	cacheWarn := func(err error) { fmt.Fprintf(warn, "plate render: warning: render cache: %v\n", err) }
	page := u
	if local != "" {
		page = PlateURL(local, query)
	}
	key, err := cacheKey(o, page)
	if err != nil {
		// No key, no record to trust or keep: render uncached.
		cacheWarn(err)
		if _, _, err := j.run(ctx); err != nil {
			return Result{}, err
		}
		return Result{Uncacheable, "no cache key: " + err.Error()}, nil
	}
	var outs []string
	for _, p := range o.outputs() {
		abs, err := filepath.Abs(p)
		if err != nil {
			return Result{}, err
		}
		outs = append(outs, abs)
	}
	if local != "" && !o.Force {
		if r, ok := loadRecord(key); ok && r.writes(outs) && r.current() {
			return Result{Outcome: Unchanged}, nil
		}
	}
	if err := dropRecord(key); err != nil {
		cacheWarn(err)
	}
	loads, hashes, err := j.run(ctx)
	if err != nil {
		return Result{}, err
	}
	if local == "" {
		return Result{Uncacheable, "remote page"}, nil
	}
	for i := range hashes {
		hashes[i].Path = outs[i]
	}
	r, why := newRecord(loads, local, hashes)
	if why != "" {
		return Result{Uncacheable, why}, nil
	}
	if err := saveRecord(key, r); err != nil {
		cacheWarn(err)
	}
	return Result{Outcome: Rendered}, nil
}

// Outcome is what Run did with a render that succeeded.
type Outcome int

const (
	Rendered    Outcome = iota // rendered, and recorded for next time
	Unchanged                  // skipped: nothing it loaded or wrote has changed
	Uncacheable                // rendered, but no record can vouch for it; Result.Reason says why
)

// Result is Run's outcome, with the reason when it is Uncacheable.
type Result struct {
	Outcome Outcome
	Reason  string
}

// Main runs `plate render`.
func Main(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := cli.Flags("render", usage, stderr)
	pngOut := fs.String("png", "", "write a PNG screenshot here")
	pdfOut := fs.String("pdf", "", "write a PDF here")
	htmlOut := fs.String("html", "", "write the page's DOM here once scripts settle (a static copy with every script's work done)")
	jpegOut := fs.String("jpeg", "", "write the screenshot as a JPEG here (needs --size); the PNG is kept only with --png")
	quality := cli.Int(fs, "quality", defaultQuality, "JPEG quality, 1–100 (needs --jpeg)")
	transparent := fs.Bool("transparent", false, "give the PNG an alpha channel, with the page's transparent areas left transparent rather than white")
	size := fs.String("size", "", "viewport and expected page size in CSS px, WxH (required with --png)")
	scale := cli.Float(fs, "scale", 1, "device scale factor; the PNG is size × scale px at 96 × scale dpi")
	budget := cli.Int(fs, "budget", DefaultBudget, "virtual-time budget in ms for scripts, fonts and images to settle")
	var failIf multi
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
		fmt.Fprintf(stderr, "plate render: unchanged: %s\n", o.outputs()[0])
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
	_, _, err = job{url: u, local: local, query: query, pdf: out, scale: 1, budget: DefaultBudget, warn: warn}.run(ctx)
	return err
}

// PNG renders the page at path (a file or URL) to a w×h CSS px PNG at out,
// at device scale factor scale, under the checks `plate render --png` runs.
func PNG(ctx context.Context, page, out string, w, h int, scale float64) error {
	if w <= 0 || h <= 0 || !(scale > 0) || math.IsInf(scale, 0) {
		return fmt.Errorf("render %dx%d at scale %g: want a positive size and scale", w, h, scale)
	}
	u, local, query, err := pageURL(page)
	if err != nil {
		return err
	}
	_, _, err = job{url: u, local: local, query: query, png: out, w: w, h: h, scale: scale, budget: DefaultBudget, warn: io.Discard}.run(ctx)
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
			local := filepath.FromSlash(u.Path)
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

// inputs lists the local page as an engine input, so the engine refuses an
// output that is the page itself.
func (j job) inputs() []string {
	if j.local == "" {
		return nil
	}
	return []string{j.local}
}

// run renders the page and returns everything it loaded, the union of every
// Chrome call, and the sha256 of each output it wrote, in PNG, PDF, HTML
// order. A local page is served through a loopback proxy for the length of
// the run so each load is recorded; a remote page goes to Chrome directly
// and loads nothing local.
//
// Each output is written and checked under a temporary name beside its
// destination, hashed there, and only then renamed into place. The hash is
// therefore of this render's bytes even when another writer (a parallel
// target with the same output) lands on the destination meanwhile; hashing
// the destination afterwards would record that writer's bytes under this
// render's key.
func (j job) run(ctx context.Context) (_ loadSet, _ []fileHash, err error) {
	var outputs []outFile
	for _, p := range []*string{&j.png, &j.pdf, &j.html, &j.jpeg} {
		if *p != "" {
			outputs = append(outputs, outFile{slot: p, dest: *p})
		}
	}
	if j.local != "" {
		for _, o := range outputs {
			if engine.SameFile(j.local, o.dest) {
				return loadSet{}, nil, fmt.Errorf("%s is the page being rendered; write the result elsewhere", o.dest)
			}
		}
	}
	// Every output goes first: whichever stage fails, no stale file from an
	// earlier render may sit beside a fresh one.
	for _, o := range outputs {
		if err := os.Remove(o.dest); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return loadSet{}, nil, err
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
			return loadSet{}, nil, err
		}
		*outputs[i].slot = outputs[i].tmp
	}
	var srv *server
	if j.local != "" {
		if srv, err = serve(); err != nil {
			return loadSet{}, nil, err
		}
		defer srv.Close()
		j.url = PlateURL(j.local, j.query)
		j.proxy = srv.proxyFlag()
	}
	loads := func() loadSet {
		if srv == nil {
			return loadSet{}
		}
		return srv.loads()
	}
	if j.png != "" {
		if err := j.screenshot(ctx); err != nil {
			return loads(), nil, err
		}
	}
	if j.pdf != "" {
		if err := j.print(ctx); err != nil {
			return loads(), nil, err
		}
	}
	if j.html != "" {
		if err := j.dump(ctx); err != nil {
			return loads(), nil, err
		}
	}
	if j.jpeg != "" {
		if err := j.convert(ctx); err != nil {
			return loads(), nil, err
		}
	}
	if j.blankMargin > 0 {
		if err := j.checkMargins(ctx); err != nil {
			return loads(), nil, err
		}
	}
	var hashes []fileHash
	for _, o := range outputs {
		if o.scratch {
			continue
		}
		h := fileHash{o.dest, hashFile(o.tmp)}
		if h.SHA256 == "" {
			return loads(), nil, fmt.Errorf("%s vanished before it could be moved to %s", o.tmp, o.dest)
		}
		hashes = append(hashes, h)
	}
	for _, o := range outputs {
		if o.scratch {
			continue
		}
		if err := os.Rename(o.tmp, o.dest); err != nil {
			return loads(), nil, err
		}
	}
	return loads(), hashes, nil
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

// base is every Chrome call's flags. Never --user-data-dir: with it,
// headless Chrome hangs after writing the screenshot. --virtual-time-budget
// fast-forwards timers only on chrome-headless-shell, which is why that is
// the pinned engine and not full Chrome.
//
// A local page is an http page on plate's proxy, so there is no
// --allow-file-access-from-files: it would only let a remote page read local
// files. Chrome skips a proxy for loopback hosts unless told otherwise, so
// the bypass list is emptied of them.
func (j job) base() []string {
	args := []string{"--headless", "--disable-gpu", "--no-sandbox",
		"--hide-scrollbars", "--virtual-time-budget=" + strconv.Itoa(j.budget)}
	if j.proxy != "" {
		args = append(args, j.proxy, "--proxy-bypass-list=<-loopback>")
	}
	return args
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
	res, err := j.chromeRun(ctx, engine.Cmd{Args: append(args, j.url), Outputs: []string{j.png}})
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
		return fmt.Errorf("%s: Chrome rendered %dx%d, expected %dx%d (size × scale)", j.png, cfg.Width, cfg.Height, pw, ph)
	}
	if j.transparent && cfg.ColorModel != color.NRGBAModel && cfg.ColorModel != color.NRGBA64Model {
		return fmt.Errorf("%s: Chrome wrote a PNG without an alpha channel for --transparent", j.png)
	}
	// Chrome writes no pHYs, so the PNG would claim 72 dpi and misstate its
	// print size. A CSS px is 1/96 in, so the true density is 96 × scale.
	return raster.SetDPI(j.png, 96*j.scale)
}

// convert writes the checked screenshot j.png as the JPEG j.jpeg, stripped
// of metadata, at j.quality.
func (j job) convert(ctx context.Context) error {
	_, err := engine.Run(ctx, engine.Cmd{Engine: "magick",
		Args:   []string{j.png, "-strip", "-quality", strconv.Itoa(j.quality), j.jpeg},
		Inputs: []string{j.png}, Outputs: []string{j.jpeg}, Timeout: 15 * time.Minute})
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
		if err := sentinel(string(res.Stdout), j.failIf); err != nil {
			return err
		}
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
		return fmt.Errorf("%s: the PDF page is %gx%g pt, not the %dx%d CSS px (%gx%g pt) asked for; give the page `@page { size: %dpx %dpx; margin: 0 }`",
			j.pdf, info.W, info.H, j.w, j.h, ww, wh, j.w, j.h)
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
	if err := sentinel(dom, j.failIf); err != nil {
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

func sentinel(text string, failIf []string) error {
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
