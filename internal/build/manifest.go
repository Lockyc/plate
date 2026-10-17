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
}

// ext maps each output kind a manifest names to its file extension.
var ext = map[string]string{"pdf": ".pdf", "png": ".png", "jpeg": ".jpg", "html": ".html"}

// ParseManifest reads the manifest at path. Each line is
//
//	name  page  WxH  scale  outputs  [option...]
//
// with a field starting "#" commenting out the rest of the line. The page is
// relative to the manifest's directory and may carry a ?query; outputs is a
// comma list of pdf, png, jpeg and html, written to outDir/name.{pdf,png,jpg,html};
// the options are blank-margin=PX, transparent, budget=MS and quality=Q.
// Every target gets failIf. Each target is checked by render.Validate here,
// so a manifest with one bad target is refused before anything renders.
// An error names the line as path:line.
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
	first := map[string]int{}
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
		if err == nil {
			if prev, dup := first[t.Name]; dup {
				err = fmt.Errorf("target %q is already on line %d", t.Name, prev)
			}
		}
		if err != nil {
			return nil, fmt.Errorf("%s:%d: %w", path, n, err)
		}
		t.Line = n
		first[t.Name] = n
		ts = append(ts, t)
	}
	return ts, nil
}

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
	o.Page = filepath.Join(dir, filepath.FromSlash(file))
	if hasQuery {
		o.Page = (&url.URL{Scheme: "file", Path: filepath.ToSlash(o.Page), RawQuery: query}).String()
	}
	ws, hs, ok := strings.Cut(size, "x")
	w, err1 := strconv.Atoi(ws)
	h, err2 := strconv.Atoi(hs)
	if !ok || err1 != nil || err2 != nil {
		return Target{}, fmt.Errorf("size %q: want WxH in CSS px, e.g. 1200x900", size)
	}
	o.W, o.H = w, h
	s, ok := cli.Finite(scale)
	if !ok {
		return Target{}, fmt.Errorf("scale %q: want a number", scale)
	}
	o.Scale = s
	for _, kind := range strings.Split(outputs, ",") {
		slot := map[string]*string{"pdf": &o.PDF, "png": &o.PNG, "jpeg": &o.JPEG, "html": &o.HTML}[kind]
		if slot == nil {
			return Target{}, fmt.Errorf("output %q: want pdf, png, jpeg or html", kind)
		}
		if *slot != "" {
			return Target{}, fmt.Errorf("output %s is listed twice", kind)
		}
		*slot = filepath.Join(outDir, name+ext[kind])
	}
	seen := map[string]bool{}
	for _, opt := range fields[5:] {
		key, val, hasVal := strings.Cut(opt, "=")
		if seen[key] {
			return Target{}, fmt.Errorf("option %s is given twice", key)
		}
		seen[key] = true
		var num *int
		switch key {
		case "transparent":
			if hasVal {
				return Target{}, errors.New("transparent takes no value")
			}
			o.Transparent = true
			continue
		case "blank-margin":
			num = &o.BlankMargin
		case "budget":
			num = &o.Budget
		case "quality":
			o.Quality = new(int)
			num = o.Quality
		default:
			return Target{}, fmt.Errorf("option %q: want blank-margin=PX, transparent, budget=MS or quality=Q", opt)
		}
		v, err := strconv.ParseInt(val, 10, strconv.IntSize)
		if !hasVal || err != nil {
			return Target{}, fmt.Errorf("option %q: want %s=N, a whole number", opt, key)
		}
		*num = int(v)
	}
	if err := o.Validate(); err != nil {
		return Target{}, err
	}
	return Target{Name: name, Opts: o}, nil
}
