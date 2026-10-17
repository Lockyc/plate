package build

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/lockyc/plate/internal/cli"
	"github.com/lockyc/plate/internal/render"
)

// Target is one manifest line: the name its outputs are called by, the line
// it came from and the render it asks for.
type Target struct {
	Name string
	Line int
	Opts render.Options
	// TwoUp is the A4 sheet the 2up option imposes Opts.PDF onto, or "".
	TwoUp string
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
// the options are blank-margin=PX, transparent, budget=MS, quality=Q and
// 2up, which imposes the target's PDF onto outDir/name-2up-a4.pdf as `plate
// impose` does. No two targets may write the same file. Every target gets
// failIf. Each target is checked by render.Validate here,
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
	for _, kind := range strings.Split(outputs, ",") {
		k, ok := kinds[kind]
		if !ok {
			return Target{}, fmt.Errorf("output %q: want pdf, png, jpeg or html", kind)
		}
		slot := k.slot(&o)
		if *slot != "" {
			return Target{}, fmt.Errorf("output %s is listed twice", kind)
		}
		*slot = filepath.Join(outDir, name+k.ext)
	}
	t := Target{Name: name}
	seen := map[string]bool{}
	for _, opt := range fields[5:] {
		key, val, hasVal := strings.Cut(opt, "=")
		if seen[key] {
			return Target{}, fmt.Errorf("option %s is given twice", key)
		}
		seen[key] = true
		var num *int
		switch key {
		case "transparent", "2up":
			if hasVal {
				return Target{}, fmt.Errorf("%s takes no value", key)
			}
			if key == "transparent" {
				o.Transparent = true
			} else {
				t.TwoUp = filepath.Join(outDir, name+twoUpSuffix)
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
			return Target{}, fmt.Errorf("option %q: want blank-margin=PX, transparent, budget=MS, quality=Q or 2up", opt)
		}
		v, err := strconv.ParseInt(val, 10, strconv.IntSize)
		if !hasVal || err != nil {
			return Target{}, fmt.Errorf("option %q: want %s=N, a whole number", opt, key)
		}
		*num = int(v)
	}
	if t.TwoUp != "" && o.PDF == "" {
		return Target{}, errors.New("2up needs the pdf output: it imposes the target's PDF")
	}
	if err := o.Validate(); err != nil {
		return Target{}, err
	}
	t.Opts = o
	return t, nil
}

// twoUpSuffix ends the name of the sheet the 2up option writes.
const twoUpSuffix = "-2up-a4.pdf"

// Outputs lists every file t writes: its render's outputs, then the 2up
// sheet.
func (t Target) Outputs() []string {
	out := t.Opts.Outputs()
	if t.TwoUp != "" {
		out = append(out, t.TwoUp)
	}
	return out
}
