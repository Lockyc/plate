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

const usage = "render [--png out.png [--transparent]] [--pdf out.pdf] [--html out.html] [--size WxH] [--scale S] [--budget MS] [--fail-if TEXT]... [--force] <page.html|URL>"

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
	url, local  string // url is the remote page, or the file URL before run swaps in the proxied one
	query       string // the local page's "?…", kept
	proxy       string // the --proxy-server flag, set by run for a local page
	png, pdf    string
	html        string
	transparent bool
	w, h        int
	scale       float64
	budget      int
	failIf      []string
	warn        io.Writer
}

// Options is one render: the page (a path, a file URL that may carry a
// query, or an http(s) URL), its outputs and the flags that shape them.
type Options struct {
	Page           string
	PNG, PDF, HTML string
	W, H           int
	Scale          float64
	Budget         int
	FailIf         []string
	Transparent    bool
	Force          bool // render even when the cache says nothing changed
}

// usageError is a wrong combination of options, which Main reports with the
// usage line and exit status 2.
type usageError string

func (e usageError) Error() string { return string(e) }

// outputs lists o's outputs in flag order, PNG, PDF then HTML.
func (o Options) outputs() []string {
	var out []string
	for _, p := range []string{o.PNG, o.PDF, o.HTML} {
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func (o Options) validate() error {
	if len(o.outputs()) == 0 {
		return usageError("give --png, --pdf, --html or a combination")
	}
	if o.Transparent && o.PNG == "" {
		return usageError("--transparent needs --png")
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
	for _, p := range [][2]string{{"png", o.PNG}, {"pdf", o.PDF}, {"html", o.HTML}} {
		for _, q := range [][2]string{{"png", o.PNG}, {"pdf", o.PDF}, {"html", o.HTML}} {
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
	if err := o.validate(); err != nil {
		return Result{}, err
	}
	u, local, query, err := pageURL(o.Page)
	if err != nil {
		return Result{}, err
	}
	j := job{url: u, local: local, query: query, png: o.PNG, pdf: o.PDF, html: o.HTML, transparent: o.Transparent,
		w: o.W, h: o.H, scale: o.Scale, budget: o.Budget, failIf: o.FailIf, warn: warn}
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
	transparent := fs.Bool("transparent", false, "give the PNG an alpha channel, with the page's transparent areas left transparent rather than white")
	size := fs.String("size", "", "viewport and expected page size in CSS px, WxH (required with --png)")
	scale := cli.Float(fs, "scale", 1, "device scale factor; the PNG is size × scale px at 96 × scale dpi")
	budget := cli.Int(fs, "budget", defaultBudget, "virtual-time budget in ms for scripts, fonts and images to settle")
	var failIf multi
	fs.Var(&failIf, "fail-if", "fail when `TEXT` appears in the page: its markup (dumped DOM) for --png, its extracted text for --pdf (repeatable)")
	force := fs.Bool("force", false, "render even when nothing the page loaded and no output has changed since its last success")
	rest, code, ok := cli.Parse(fs, args, 1)
	if !ok {
		return code
	}
	usageErr := func(msg string) int {
		defer fs.Usage()
		return cli.Usage(stderr, "render", "%s", msg)
	}
	o := Options{Page: rest[0], PNG: *pngOut, PDF: *pdfOut, HTML: *htmlOut, Transparent: *transparent,
		Scale: *scale, Budget: *budget, FailIf: failIf, Force: *force}
	if *size != "" {
		w, h, err := parseSize(*size)
		if err != nil {
			return usageErr(err.Error())
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
	_, _, err = job{url: u, local: local, query: query, pdf: out, scale: 1, budget: defaultBudget, warn: warn}.run(ctx)
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
	_, _, err = job{url: u, local: local, query: query, png: out, w: w, h: h, scale: scale, budget: defaultBudget, warn: io.Discard}.run(ctx)
	return err
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
	type output struct {
		slot      *string // the job field, pointed at tmp while the stages run
		dest, tmp string
	}
	var outputs []output
	for _, p := range []*string{&j.png, &j.pdf, &j.html} {
		if *p != "" {
			outputs = append(outputs, output{slot: p, dest: *p})
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
	defer func() {
		for _, o := range outputs {
			if o.tmp != "" {
				os.Remove(o.tmp)
			}
			if err != nil {
				os.Remove(o.dest)
			}
		}
	}()
	for i := range outputs {
		if outputs[i].tmp, err = tempName(outputs[i].dest); err != nil {
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
	hashes := make([]fileHash, len(outputs))
	for i, o := range outputs {
		hashes[i] = fileHash{o.dest, hashFile(o.tmp)}
		if hashes[i].SHA256 == "" {
			return loads(), nil, fmt.Errorf("%s vanished before it could be moved to %s", o.tmp, o.dest)
		}
	}
	for _, o := range outputs {
		if err := os.Rename(o.tmp, o.dest); err != nil {
			return loads(), nil, err
		}
	}
	return loads(), hashes, nil
}

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
	res, err := engine.Run(ctx, engine.Cmd{Engine: chrome, Args: append(args, j.url), Inputs: j.inputs(), Outputs: []string{j.pdf}, Timeout: 15 * time.Minute})
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
