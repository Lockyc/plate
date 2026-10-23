package build

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/lockyc/plate/internal/animate"
	"github.com/lockyc/plate/internal/cli"
	"github.com/lockyc/plate/internal/render"
	"github.com/lockyc/plate/internal/video"
)

// Target is one manifest line: the name its outputs are called by, the line
// it came from and the render it asks for.
type Target struct {
	Name string
	Line int
	Opts render.Options
	// Anim is the capture a video line (one with dur=) asks for, in place of
	// Opts.
	Anim *animate.Options
	// TwoUp is the A4 sheet the 2up option imposes Opts.PDF onto, or "".
	TwoUp string
	// Marks is the print shop's copy of Opts.PDF the marks option writes,
	// with bleed and crop marks, or "".
	Marks string
}

// kinds maps each output kind a manifest names to its file extension and
// the render option it sets.
var kinds = map[string]struct {
	ext  string
	slot func(*render.Options) *string
}{
	"pdf":  {".pdf", func(o *render.Options) *string { return &o.PDF }},
	"png":  {".png", func(o *render.Options) *string { return &o.PNG }},
	"jpeg": {".jpg", func(o *render.Options) *string { return &o.JPEG }},
	"html": {".html", func(o *render.Options) *string { return &o.HTML }},
}

// ParseManifest reads the manifest at path. Each line is
//
//	name  page  WxH  scale  outputs  [option...]
//
// with a field starting "#" commenting out the rest of the line. The page is
// relative to the manifest's directory and may carry a ?query; outputs is a
// comma list of pdf, png, jpeg and html, written to outDir/name.{pdf,png,jpg,html};
// the options are blank-margin=PX, transparent, budget=MS, quality=Q, 2up,
// which imposes the target's PDF onto outDir/name-2up-a4.pdf as `plate
// impose` does, and marks, which writes it to outDir/name-marks.pdf with
// bleed and crop marks as `plate marks` does.
//
// A line with dur=S (seconds, 8 or 8s) is a video line, captured as `plate
// animate` does: its outputs are mp4, webm, gif, webp and poster (poster
// is name.poster.png), with png and jpeg the last frame, and name.json
// beside them; fps=N, loop=none|crossfade|dip and budget-kb=N tune it, and
// budget=MS is the settle wait. A video output needs dur=, dur= needs a
// video output, and pdf, html, transparent, blank-margin, 2up and marks
// are refused on a video line. No two targets may write the same file.
// Every target gets failIf. Each target is checked by render.Validate (a
// video line by animate's Validate) here,
// so a manifest with one bad target is refused before anything renders.
// A bad line is a *LineError; any other error is the manifest's file
// failing to read.
func ParseManifest(path, outDir string, failIf []string) ([]Target, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	dir, err := filepath.Abs(filepath.Dir(path))
	if err != nil {
		return nil, err
	}
	var ts []Target
	// Each name's index in ts, keyed lower-cased: on a case-insensitive
	// filesystem Promo and promo write the same files.
	first := map[string]int{}
	// The same for each output file: name-2up-a4.pdf is also target
	// name-2up-a4's PDF.
	writer := map[string]int{}
	for i, line := range strings.Split(string(b), "\n") {
		fields := strings.Fields(line)
		for k, f := range fields {
			if strings.HasPrefix(f, "#") {
				fields = fields[:k]
				break
			}
		}
		if len(fields) == 0 {
			continue
		}
		n := i + 1
		t, err := parseLine(fields, dir, outDir, failIf)
		key := strings.ToLower(t.Name)
		if err == nil {
			if prev, dup := first[key]; dup {
				err = fmt.Errorf("target %q is already on line %d, as %q", t.Name, ts[prev].Line, ts[prev].Name)
			}
		}
		if err == nil {
			for _, out := range t.Outputs() {
				if prev, dup := writer[strings.ToLower(out)]; dup {
					err = fmt.Errorf("%s is also written by %q on line %d", filepath.Base(out), ts[prev].Name, ts[prev].Line)
					break
				}
			}
		}
		if err != nil {
			return nil, &LineError{Path: path, Line: n, Err: err}
		}
		t.Line = n
		first[key] = len(ts)
		for _, out := range t.Outputs() {
			writer[strings.ToLower(out)] = len(ts)
		}
		ts = append(ts, t)
	}
	return ts, nil
}

// LineError is a manifest line that is not a valid target.
type LineError struct {
	Path string // the manifest, as given
	Line int
	Err  error
}

func (e *LineError) Error() string { return fmt.Sprintf("%s:%d: %v", e.Path, e.Line, e.Err) }

func (e *LineError) Unwrap() error { return e.Err }

func parseLine(fields []string, dir, outDir string, failIf []string) (Target, error) {
	if len(fields) < 5 {
		return Target{}, errors.New("want name  page  WxH  scale  outputs  [option...]")
	}
	name, page, size, scale, outputs := fields[0], fields[1], fields[2], fields[3], fields[4]
	if name == "." || name == ".." || strings.ContainsAny(name, `/\`) {
		return Target{}, fmt.Errorf("name %q must be a plain file name", name)
	}
	o := render.Options{Budget: render.DefaultBudget, FailIf: failIf}
	file, query, hasQuery := strings.Cut(page, "?")
	if filepath.IsAbs(file) || strings.HasPrefix(file, "/") || strings.Contains(file, "://") {
		return Target{}, fmt.Errorf("page %q must be relative to the manifest", page)
	}
	o.Page = filepath.Join(dir, filepath.FromSlash(file))
	if hasQuery {
		o.Page = (&url.URL{Scheme: "file", Path: filepath.ToSlash(o.Page), RawQuery: query}).String()
	}
	w, h, err := render.ParseSize(size)
	if err != nil {
		return Target{}, err
	}
	o.W, o.H = w, h
	s, ok := cli.Finite(scale)
	if !ok {
		return Target{}, fmt.Errorf("scale %q: want a number", scale)
	}
	o.Scale = s
	var formats []string
	for _, kind := range strings.Split(outputs, ",") {
		if slices.Contains(video.Formats, kind) {
			if slices.Contains(formats, kind) {
				return Target{}, fmt.Errorf("output %s is listed twice", kind)
			}
			formats = append(formats, kind)
			continue
		}
		k, ok := kinds[kind]
		if !ok {
			return Target{}, fmt.Errorf("output %q: want pdf, png, jpeg, html, %s", kind, strings.Join(video.Formats, ", "))
		}
		slot := k.slot(&o)
		if *slot != "" {
			return Target{}, fmt.Errorf("output %s is listed twice", kind)
		}
		*slot = filepath.Join(outDir, name+k.ext)
	}
	t := Target{Name: name}
	var (
		dur      float64
		loop     = "none"
		fps      = animate.DefaultFPS
		budgetKB int
	)
	seen := map[string]bool{}
	for _, opt := range fields[5:] {
		key, val, hasVal := strings.Cut(opt, "=")
		if seen[key] {
			return Target{}, fmt.Errorf("option %s is given twice", key)
		}
		seen[key] = true
		var num *int
		switch key {
		case "dur":
			d, err := parseDur(val)
			if err != nil || !hasVal {
				return Target{}, fmt.Errorf("dur=%q: want seconds, e.g. dur=8s", val)
			}
			dur = d
			continue
		case "loop":
			if !hasVal {
				return Target{}, fmt.Errorf("option %q: want loop=none, crossfade or dip", opt)
			}
			loop = val
			continue
		case "fps":
			num = &fps
		case "budget-kb":
			num = &budgetKB
		case "transparent", "2up", "marks":
			if hasVal {
				return Target{}, fmt.Errorf("%s takes no value", key)
			}
			switch key {
			case "transparent":
				o.Transparent = true
			case "2up":
				t.TwoUp = filepath.Join(outDir, name+twoUpSuffix)
			case "marks":
				t.Marks = filepath.Join(outDir, name+marksSuffix)
			}
			continue
		case "blank-margin":
			num = &o.BlankMargin
		case "budget":
			num = &o.Budget
		case "quality":
			o.Quality = new(int)
			num = o.Quality
		default:
			return Target{}, fmt.Errorf("option %q: want blank-margin=PX, transparent, budget=MS, quality=Q, 2up, marks, dur=S, fps=N, loop=L or budget-kb=N", opt)
		}
		v, err := strconv.ParseInt(val, 10, strconv.IntSize)
		if !hasVal || err != nil {
			return Target{}, fmt.Errorf("option %q: want %s=N, a whole number", opt, key)
		}
		*num = int(v)
	}
	if len(formats) > 0 && !seen["dur"] {
		return Target{}, fmt.Errorf("%s needs dur=S", formats[0])
	}
	if seen["dur"] && len(formats) == 0 {
		return Target{}, errors.New("dur= needs a video output (mp4, webm, gif, webp or poster)")
	}
	if !seen["dur"] {
		for _, k := range []string{"fps", "loop", "budget-kb"} {
			if seen[k] {
				return Target{}, fmt.Errorf("%s needs dur=S", k)
			}
		}
	}
	if seen["dur"] {
		return videoLine(t, o, formats, dur, fps, loop, budgetKB, outDir, name, seen)
	}
	if t.TwoUp != "" && o.PDF == "" {
		return Target{}, errors.New("2up needs the pdf output: it imposes the target's PDF")
	}
	if t.Marks != "" && o.PDF == "" {
		return Target{}, errors.New("marks needs the pdf output: it marks the target's PDF")
	}
	if err := o.Validate(); err != nil {
		return Target{}, err
	}
	t.Opts = o
	return t, nil
}

// twoUpSuffix ends the name of the sheet the 2up option writes, and
// marksSuffix the print shop's copy the marks option writes.
const (
	twoUpSuffix = "-2up-a4.pdf"
	marksSuffix = "-marks.pdf"
)

// Outputs lists every file t writes: its render's outputs, then the 2up
// sheet and the marked copy.
func (t Target) Outputs() []string {
	if t.Anim != nil {
		return t.Anim.Outputs()
	}
	out := t.Opts.Outputs()
	for _, d := range []string{t.TwoUp, t.Marks} {
		if d != "" {
			out = append(out, d)
		}
	}
	return out
}

// parseDur reads dur=S: seconds above 0, with an optional trailing "s".
func parseDur(val string) (float64, error) {
	d, ok := cli.Finite(strings.TrimSuffix(val, "s"))
	if !ok || d <= 0 {
		return 0, errors.New("not seconds")
	}
	return d, nil
}

// videoLine is the target a line with dur= asks for: a capture of o's page
// into outDir. png and jpeg are the capture's stills; the options and
// outputs that read a settled page are refused.
func videoLine(t Target, o render.Options, formats []string, dur float64, fps int, loop string, budgetKB int, outDir, name string, seen map[string]bool) (Target, error) {
	for _, r := range []struct {
		opt string
		set bool
	}{{"pdf", o.PDF != ""}, {"html", o.HTML != ""}, {"transparent", o.Transparent}, {"blank-margin", seen["blank-margin"]},
		{"2up", t.TwoUp != ""}, {"marks", t.Marks != ""}} {
		if r.set {
			return Target{}, fmt.Errorf("%s reads a settled page; a video line has one only at its end", r.opt)
		}
	}
	a := animate.Options{Page: o.Page, W: o.W, H: o.H, Scale: o.Scale, DurationS: dur, FPS: fps, Formats: formats,
		Loop: loop, BudgetKB: budgetKB, Budget: o.Budget, Out: outDir, Name: name, PNG: o.PNG, JPEG: o.JPEG, FailIf: o.FailIf}
	if err := a.Validate(); err != nil {
		return Target{}, err
	}
	t.Anim = &a
	return t, nil
}
