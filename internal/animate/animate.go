package animate

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/lockyc/plate/internal/atomicfile"
	"github.com/lockyc/plate/internal/cli"
	"github.com/lockyc/plate/internal/engine"
	"github.com/lockyc/plate/internal/outdir"
	"github.com/lockyc/plate/internal/raster"
	"github.com/lockyc/plate/internal/render"
	"github.com/lockyc/plate/internal/video"
)

const usage = "animate --size WxH [--scale F] --duration S [--fps N] [--format mp4,webm,poster,webp,gif] [--loop none|crossfade|dip] [--budget-kb N] [--budget MS] --out DIR [--name N] [--fail-if TEXT]... [--force] <page.html|http(s) or file URL>"

// DefaultFPS is --fps's default, and MaxFPS its limit.
const (
	DefaultFPS = 30
	MaxFPS     = 120
)

// frameSlack is how long beyond the budget a frame may take to seek and
// capture before it is refused as never finishing.
const frameSlack = 5 * time.Second

// Options is one capture: the page, its size, how long and how fast, the
// outputs written into Out as Name, and the optional last-frame stills PNG
// and JPEG.
type Options struct {
	Page      string
	W, H      int
	Scale     float64
	DurationS float64
	FPS       int
	Formats   []string
	Loop      string // none, or one of video.Loops
	BudgetKB  int    // the mp4's size limit in KB; 0 is none
	Budget    int    // render's settle wait in ms, spent before frame 0
	Out, Name string // Name defaults to the page's base name without extension
	PNG, JPEG string
	Quality   *int // the JPEG still's quality, 1–100; nil is render.DefaultQuality
	FailIf    []string
	Force     bool // capture even when the cache says nothing changed
}

// usageError is a wrong combination of options, which Main reports with the
// usage line and exit status 2.
type usageError string

func (e usageError) Error() string { return string(e) }

// frames is the capture's frame count.
func (o Options) frames() int { return int(math.Round(o.DurationS * float64(o.FPS))) }

// name is Name, or the page's base name without its extension.
func (o Options) name() string {
	if o.Name != "" {
		return o.Name
	}
	p := filepath.Base(o.Page)
	if u, err := url.Parse(o.Page); err == nil && len(u.Scheme) > 1 {
		p = path.Base(u.Path)
	}
	if n := strings.TrimSuffix(p, path.Ext(p)); n != "." && n != "/" {
		return n
	}
	return ""
}

// Validate refuses options no capture can satisfy, as a usage error, before
// Chrome starts. The messages name animate's flags.
func (o Options) Validate() error {
	if o.W <= 0 || o.H <= 0 {
		return usageError("--size wants WxH, both above 0")
	}
	if math.IsNaN(o.Scale) || math.IsInf(o.Scale, 0) || o.Scale <= 0 {
		return usageError("--scale must be a finite number above 0")
	}
	if err := render.CheckSize(o.W, o.H, o.Scale); err != nil {
		return usageError(err.Error())
	}
	if o.FPS < 1 || o.FPS > MaxFPS {
		return usageError(fmt.Sprintf("--fps must be between 1 and %d", MaxFPS))
	}
	if math.IsNaN(o.DurationS) || math.IsInf(o.DurationS, 0) || o.DurationS <= 0 || o.frames() < 2 {
		return usageError("--duration must be above 0 and give at least 2 frames at --fps")
	}
	if len(o.Formats) == 0 {
		return usageError("--format wants one or more of " + strings.Join(video.Formats, ", "))
	}
	for i, f := range o.Formats {
		if !slices.Contains(video.Formats, f) {
			return usageError(fmt.Sprintf("--format %q: want one or more of %s", f, strings.Join(video.Formats, ", ")))
		}
		if slices.Contains(o.Formats[:i], f) {
			return usageError(fmt.Sprintf("--format lists %s twice", f))
		}
	}
	pw, ph := render.Pixels(o.W, o.H, o.Scale)
	for _, f := range []string{"mp4", "webm"} {
		if slices.Contains(o.Formats, f) && (pw%2 != 0 || ph%2 != 0) {
			return usageError(fmt.Sprintf("%s needs even pixel sides, not %dx%d (--size × --scale)", f, pw, ph))
		}
	}
	if o.Loop != "none" && !slices.Contains(video.Loops, o.Loop) {
		return usageError(fmt.Sprintf("--loop %q: want none, %s", o.Loop, strings.Join(video.Loops, " or ")))
	}
	if o.BudgetKB < 0 || o.Budget < 0 {
		return usageError("--budget-kb and --budget must not be below 0")
	}
	if o.Out == "" {
		return usageError("--out is required")
	}
	if o.name() == "" {
		return usageError("the page gives no file name; pass --name")
	}
	if n := o.name(); strings.ContainsAny(n, `/\`) || strings.HasPrefix(n, ".") {
		return usageError(fmt.Sprintf("--name %q: want a file name, not a path or a dot file", n))
	}
	if o.JPEG == "" && o.Quality != nil {
		return usageError("quality needs a JPEG still")
	}
	if o.Quality != nil && (*o.Quality < 1 || *o.Quality > 100) {
		return usageError("quality must be between 1 and 100")
	}
	if o.PNG != "" && o.JPEG != "" && engine.SameFile(o.PNG, o.JPEG) {
		return usageError("the PNG and the JPEG name the same file")
	}
	return nil
}

// Outputs is every file a capture writes, as absolute paths: one per listed
// format in Out, the sidecar, then PNG and JPEG when given.
func (o Options) Outputs() []string {
	name := o.name()
	listed := map[string]bool{name + ".json": true}
	for _, f := range o.Formats {
		listed[name+"."+video.Ext(f)] = true
	}
	var out []string
	for _, n := range video.Outputs(name) {
		if listed[n] {
			out = append(out, absPath(filepath.Join(o.Out, n)))
		}
	}
	for _, p := range []string{o.PNG, o.JPEG} {
		if p != "" {
			out = append(out, absPath(p))
		}
	}
	return out
}

func absPath(p string) string {
	if a, err := filepath.Abs(p); err == nil {
		return a
	}
	return p
}

// Run captures o's page through render.Cached, so an unchanged capture is
// skipped without starting Chrome. Its key is o with the page canonical,
// every path absolute, the name resolved and Force left out, since Force
// only says whether to look. The measurement is the zero value when the
// capture was skipped.
func Run(ctx context.Context, o Options, warn io.Writer) (render.Result, video.Measurement, error) {
	if err := o.Validate(); err != nil {
		return render.Result{}, video.Measurement{}, err
	}
	if err := video.RequireEncoders(ctx, o.Formats); err != nil {
		return render.Result{}, video.Measurement{}, err
	}
	key := o
	var err error
	if key.Page, err = render.PageAddress(o.Page); err != nil {
		return render.Result{}, video.Measurement{}, err
	}
	key.Force, key.Name = false, o.name()
	for _, p := range []*string{&key.Out, &key.PNG, &key.JPEG} {
		if *p != "" {
			if *p, err = filepath.Abs(*p); err != nil {
				return render.Result{}, video.Measurement{}, err
			}
		}
	}
	var meas video.Measurement
	res, err := render.Cached(ctx, "animate", o.Page, key, key.Outputs(), o.Force, warn,
		func(ctx context.Context, l render.Load) (hashes []render.FileHash, err error) {
			meas, hashes, err = key.capture(ctx, l)
			return hashes, err
		})
	if err != nil {
		return render.Result{}, video.Measurement{}, err
	}
	return res, meas, nil
}

// capture is one run of o, every path absolute, as l loads the page: the
// frames, their refusals, the loop close, the encodes and their checks, the
// stills and the published set. It returns the hash of every output, each
// taken before the file is moved into place, as render.Cached asks. A
// failure leaves none of o's outputs.
func (o Options) capture(ctx context.Context, l render.Load) (_ video.Measurement, _ []render.FileHash, err error) {
	var none video.Measurement
	name := o.name()
	out, _, err := outdir.Make(o.Out, "plate-animate-")
	if err != nil {
		return none, nil, err
	}
	// Every output goes first: whichever stage fails, no stale file from an
	// earlier capture may sit beside a fresh one.
	if err := video.RemoveOutputs(out, name); err != nil {
		return none, nil, err
	}
	var stills []string
	for _, p := range []string{o.PNG, o.JPEG} {
		if p != "" {
			if err := os.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
				return none, nil, err
			}
			stills = append(stills, p)
		}
	}
	defer func() {
		if err != nil {
			for _, p := range stills {
				os.Remove(p)
			}
		}
	}()
	tmp, err := os.MkdirTemp(out, "."+name+"-")
	if err != nil {
		return none, nil, err
	}
	defer os.RemoveAll(tmp)
	work := func(name string) (string, error) {
		d := filepath.Join(tmp, name)
		return d, os.Mkdir(d, 0o755)
	}
	refDir, err := work("refs")
	if err != nil {
		return none, nil, err
	}
	refs, err := o.shoot(ctx, l, refDir)
	if err != nil {
		return none, nil, err
	}
	if err := refuseStill(refs); err != nil {
		return none, nil, err
	}
	var lc video.LoopClose
	if o.Loop != "none" {
		if lc, err = video.CloseLoop(refs, o.FPS, o.Loop); err != nil {
			return none, nil, err
		}
	}

	pw, ph := render.Pixels(o.W, o.H, o.Scale)
	meas := video.Measurement{Name: name, Width: pw, Height: ph, FPS: o.FPS, Frames: len(refs),
		DurationMS: float64(len(refs)) * 1000 / float64(o.FPS), Formats: map[string]video.FormatResult{},
		LoopHeadFrames: lc.Head, LoopTailFrames: lc.Tail}
	// The last frame the loop close left as it was: the poster and the stills.
	poster := len(refs) - 1 - lc.Tail
	last := refs[poster]
	var files []string
	for _, f := range o.Formats {
		file := name + "." + video.Ext(f)
		p := filepath.Join(tmp, file)
		if f == "poster" {
			err = video.Poster(refs, poster, p)
		} else {
			err = video.Encode(ctx, f, refDir, o.FPS, pw, ph, p)
		}
		if err != nil {
			return none, nil, fmt.Errorf("%s: %w", f, err)
		}
		st, err := os.Stat(p)
		if err != nil {
			return none, nil, err
		}
		meas.Formats[f] = video.FormatResult{File: file, Bytes: st.Size()}
		files = append(files, file)
	}
	stillFrom := map[string]string{} // still destination → its file in tmp
	if o.PNG != "" {
		// A still line's PNG carries its density; the screenshot bytes do not.
		p := filepath.Join(tmp, "still.png")
		b, err := os.ReadFile(last)
		if err != nil {
			return none, nil, err
		}
		if err := os.WriteFile(p, b, 0o644); err != nil {
			return none, nil, err
		}
		if err := raster.SetDPI(p, 96*o.Scale); err != nil {
			return none, nil, err
		}
		stillFrom[o.PNG] = p
	}
	if o.JPEG != "" {
		quality := render.DefaultQuality
		if o.Quality != nil {
			quality = *o.Quality
		}
		p := filepath.Join(tmp, "still.jpg")
		if err := render.JPEG(ctx, last, p, quality); err != nil {
			return none, nil, err
		}
		stillFrom[o.JPEG] = p
	}

	if meas.LoopSeam, err = video.LoopSeam(refs); err != nil {
		return none, nil, err
	}
	if err := video.ScoreSharpness(ctx, &meas, tmp, refs, lc, work); err != nil {
		return none, nil, err
	}
	if err := video.Check(ctx, meas, tmp, video.Checks{BudgetKB: o.BudgetKB, Seam: o.Loop != "none"}); err != nil {
		return none, nil, err
	}
	sidecar, err := json.MarshalIndent(meas, "", "  ")
	if err != nil {
		return none, nil, err
	}
	files = append(files, name+".json")
	if err := atomicfile.WriteFile(filepath.Join(tmp, name+".json"), append(sidecar, '\n'), 0o644); err != nil {
		return none, nil, err
	}

	from := map[string]string{}
	for _, f := range files {
		from[filepath.Join(out, f)] = filepath.Join(tmp, f)
	}
	var stillData [][]byte
	for _, p := range stills {
		from[p] = stillFrom[p]
		b, err := os.ReadFile(stillFrom[p])
		if err != nil {
			return none, nil, err
		}
		stillData = append(stillData, b)
	}
	var hashes []render.FileHash
	for _, p := range o.Outputs() {
		h := render.HashFile(from[p])
		if h == "" {
			return none, nil, fmt.Errorf("%s was not written", p)
		}
		hashes = append(hashes, render.FileHash{Path: p, SHA256: h})
	}
	for i, p := range stills {
		if err := atomicfile.WriteFile(p, stillData[i], 0o644); err != nil {
			return none, nil, err
		}
	}
	if err := video.Publish(tmp, out, name, files); err != nil {
		return none, nil, err
	}
	return meas, hashes, nil
}

// shoot opens the page on plate's clock and writes frame n, at n/fps, to
// r%05d.png in dir, then holds the last frame's markup to FailIf. Opening
// the page, and each frame's seek and capture, get the budget and
// frameSlack: a page can keep
// Chrome itself busy, which no wait inside the page can see.
func (o Options) shoot(ctx context.Context, l render.Load, dir string) ([]string, error) {
	budget := time.Duration(o.Budget) * time.Millisecond
	wait := budget + frameSlack
	// Chrome lives as long as the context it starts with, so Open's bound
	// cancels that context only when the bound passes, never once Open is done.
	cctx, cancel := context.WithCancel(ctx)
	defer cancel()
	late := time.AfterFunc(wait, cancel)
	c, err := Open(cctx, l, o.W, o.H, o.Scale, budget)
	if !late.Stop() && ctx.Err() == nil {
		if err == nil {
			c.Close()
		}
		return nil, fmt.Errorf("page did not open within %d ms", wait.Milliseconds())
	}
	if err != nil {
		return nil, err
	}
	defer c.Close()
	refs := make([]string, o.frames())
	for n := range refs {
		refs[n] = filepath.Join(dir, fmt.Sprintf("r%05d.png", n))
		fctx, cancel := context.WithTimeout(ctx, wait)
		err := c.Seek(fctx, float64(n)*1000/float64(o.FPS))
		if err == nil {
			err = c.Frame(fctx, refs[n])
		}
		cancel()
		if errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil {
			return nil, fmt.Errorf("frame %d did not finish within %d ms", n, wait.Milliseconds())
		}
		if err != nil {
			return nil, err
		}
	}
	if len(o.FailIf) > 0 {
		var dom string
		if err := c.Eval(ctx, "document.documentElement.outerHTML", &dom); err != nil {
			return nil, err
		}
		if err := render.Sentinel(dom, o.FailIf); err != nil {
			return nil, err
		}
	}
	return refs, nil
}

// refuseStill refuses frames none of which differs from the first in its
// pixels: a page that drew nothing, or whose animation never ran on plate's
// clock.
func refuseStill(refs []string) error {
	first, err := os.ReadFile(refs[0])
	if err != nil {
		return err
	}
	firstImg, err := video.LoadRGBA64(refs[0])
	if err != nil {
		return err
	}
	for _, r := range refs[1:] {
		b, err := os.ReadFile(r)
		if err != nil {
			return err
		}
		if bytes.Equal(b, first) {
			continue
		}
		img, err := video.LoadRGBA64(r)
		if err != nil {
			return err
		}
		if !bytes.Equal(img.Pix, firstImg.Pix) {
			return nil
		}
	}
	px := firstImg.Pix
	for i := 8; i < len(px); i += 8 {
		if !bytes.Equal(px[i:i+8], px[:8]) {
			return errors.New("no frame differs from the first: the page's animation never ran on plate's clock")
		}
	}
	return errors.New("every frame is one colour: the page drew nothing")
}

// Main runs `plate animate`.
func Main(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := cli.Flags("animate", usage, stderr)
	size := fs.String("size", "", "viewport in CSS px, WxH (required)")
	scale := cli.Float(fs, "scale", 1, "device scale factor; the video is size × scale px")
	duration := cli.Float(fs, "duration", 0, "length in seconds (required)")
	fps := cli.Int(fs, "fps", DefaultFPS, fmt.Sprintf("frames per second, 1–%d", MaxFPS))
	formats := fs.String("format", "mp4", "comma-separated outputs: "+strings.Join(video.Formats, ", "))
	loop := fs.String("loop", "none", "close the loop: none, "+strings.Join(video.Loops, " or "))
	budgetKB := cli.Int(fs, "budget-kb", 0, "fail when the mp4 is over this many KB (0 is off)")
	budget := cli.Int(fs, "budget", render.DefaultBudget, "ms for the page to settle before frame 0")
	out := fs.String("out", "", "the directory the files are written to (required)")
	name := fs.String("name", "", "the files' base name (default: the page's, without extension)")
	var failIf cli.Multi
	fs.Var(&failIf, "fail-if", "fail when `TEXT` appears in the last frame's markup (repeatable)")
	force := fs.Bool("force", false, "capture even when nothing the page loaded and no output has changed since its last success")
	rest, code, ok := cli.Parse(fs, args, 1)
	if !ok {
		return code
	}
	usageErr := func(msg string) int {
		defer fs.Usage()
		return cli.Usage(stderr, "animate", "%s", msg)
	}
	if *size == "" {
		return usageErr("--size is required")
	}
	w, h, err := render.ParseSize(*size)
	if err != nil {
		return usageErr("--" + err.Error())
	}
	var list []string
	for f := range strings.SplitSeq(*formats, ",") {
		list = append(list, strings.TrimSpace(f))
	}
	o := Options{Page: rest[0], W: w, H: h, Scale: *scale, DurationS: *duration, FPS: *fps,
		Formats: list, Loop: *loop, BudgetKB: *budgetKB, Budget: *budget,
		Out: *out, Name: *name, FailIf: failIf, Force: *force}
	res, _, err := Run(ctx, o, stderr)
	if ue := usageError(""); errors.As(err, &ue) {
		return usageErr(ue.Error())
	}
	if err != nil {
		return cli.Fail(stderr, "animate", err)
	}
	if res.Outcome == render.Unchanged {
		fmt.Fprintf(stdout, "unchanged %s\n", o.name())
		return 0
	}
	for _, p := range o.Outputs() {
		st, err := os.Stat(p)
		if err != nil {
			return cli.Fail(stderr, "animate", err)
		}
		fmt.Fprintf(stdout, "wrote %s (%d bytes)\n", p, st.Size())
	}
	return 0
}
