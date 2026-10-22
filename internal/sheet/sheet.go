// Package sheet rewrites a PDF's pages as new sheets, each drawing one
// source page through a transform: the machinery under impose (two up on A4)
// and marks (trim, bleed and crop marks).
//
// Each source page becomes a form XObject holding its own content stream
// and resources, drawn as /F on its sheet. qpdf writes the result from a
// JSON update to the source, so every font, image and pattern is the
// source's object, unchanged and stored once; Ghostscript's pdfwrite, the
// alternative, cuts an image under a curved clip into one-pixel strips and
// inflates a photo flyer ninefold.
package sheet

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"strings"

	"github.com/lockyc/plate/internal/atomicfile"
	"github.com/lockyc/plate/internal/engine"
	"github.com/lockyc/plate/internal/pdf"
)

// Sheet is one output page: its MediaBox, any further page boxes (TrimBox,
// BleedBox) by key, the content stream that draws the source page as /F, and
// any colour spaces that stream names, by resource name.
type Sheet struct {
	Media       [4]float64
	Boxes       map[string][4]float64
	Content     []byte
	ColorSpaces map[string]any
}

// Layout turns the source's pages into one sheet per page, in order, or
// refuses them.
type Layout func(pages []pdf.Page) ([]Sheet, error)

// Matrix is a PDF transform [a b c d e f]: x' = ax + cy + e, y' = bx + dy + f.
type Matrix [6]float64

// Quarter is the exact cosine and sine of a multiple of 90 degrees.
func Quarter(deg int) (cos, sin float64) {
	switch (deg%360 + 360) % 360 {
	case 90:
		return 0, 1
	case 180:
		return -1, 0
	case 270:
		return 0, -1
	}
	return 1, 0
}

// Upright turns m (a rotation) so page p reads as a viewer shows it, and
// returns the turned CropBox's lower-left and upper-right corners.
func Upright(p pdf.Page, m Matrix) (lo, hi [2]float64) {
	b := p.Crop
	lo = [2]float64{math.Inf(1), math.Inf(1)}
	hi = [2]float64{math.Inf(-1), math.Inf(-1)}
	for _, xy := range [][2]float64{{b[0], b[1]}, {b[2], b[1]}, {b[0], b[3]}, {b[2], b[3]}} {
		x, y := m[0]*xy[0]+m[2]*xy[1], m[1]*xy[0]+m[3]*xy[1]
		lo = [2]float64{min(lo[0], x), min(lo[1], y)}
		hi = [2]float64{max(hi[0], x), max(hi[1], y)}
	}
	return lo, hi
}

// Write lays in's pages out as layout says and writes the result to out; an
// existing out is replaced only when force is set.
func Write(ctx context.Context, in, out string, force bool, layout Layout) error {
	if engine.SameFile(in, out) {
		return fmt.Errorf("%s is both the input and the output; write it elsewhere", out)
	}
	if _, err := os.Stat(out); err == nil && !force {
		return fmt.Errorf("%s exists; pass --force to replace it", out)
	} else if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	pages, err := pdf.Pages(ctx, in)
	if err != nil {
		return err
	}
	sheets, err := layout(pages)
	if err != nil {
		return err
	}
	s, err := readSource(ctx, in)
	if err != nil {
		return err
	}
	if len(s.pages) != len(pages) {
		return fmt.Errorf("%s: qpdf found %d pages, pdfinfo %d", in, len(s.pages), len(pages))
	}
	patch, err := update(ctx, in, s, pages, sheets)
	if err != nil {
		return err
	}
	dir, err := os.MkdirTemp("", "plate-sheet-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	patchFile, result := filepath.Join(dir, "update.json"), filepath.Join(dir, "out.pdf")
	if err := os.WriteFile(patchFile, patch, 0o600); err != nil {
		return err
	}
	// --deterministic-id derives the /ID from the content, so one input
	// writes the same bytes every run.
	if _, err := engine.Run(ctx, engine.Cmd{Engine: "qpdf", Args: []string{
		"--deterministic-id", "--object-streams=generate", "--update-from-json=" + patchFile, in, result,
	}, Inputs: []string{in, patchFile}, Outputs: []string{result}}); err != nil {
		return err
	}
	b, err := os.ReadFile(result)
	if err != nil {
		return err
	}
	return atomicfile.WriteFile(out, b, 0o644)
}

// dict is a PDF dictionary in qpdf's JSON: names as "/Name", references as
// "N 0 R".
type dict map[string]json.RawMessage

// source is the part of `qpdf --json` Write reads: the page objects in
// order, and every object by "obj:N 0 R" key.
type source struct {
	header  json.RawMessage
	objects map[string]json.RawMessage
	pages   []string
	maxID   int
}

func readSource(ctx context.Context, in string) (*source, error) {
	res, err := engine.Run(ctx, engine.Cmd{Engine: "qpdf", Args: []string{"--json=2", "--json-key=pages", "--json-key=qpdf", "--json-stream-data=none", in, "-"}, Inputs: []string{in}})
	if err != nil {
		return nil, err
	}
	var raw struct {
		Pages []struct {
			Object string `json:"object"`
		} `json:"pages"`
		QPDF []json.RawMessage `json:"qpdf"`
	}
	if err := json.Unmarshal(res.Stdout, &raw); err != nil || len(raw.QPDF) != 2 {
		return nil, fmt.Errorf("%s: qpdf's JSON is not what plate reads: %v", in, err)
	}
	s := &source{header: raw.QPDF[0]}
	var head struct {
		MaxID int `json:"maxobjectid"`
	}
	if err := json.Unmarshal(raw.QPDF[0], &head); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(raw.QPDF[1], &s.objects); err != nil {
		return nil, err
	}
	s.maxID = head.MaxID
	for _, p := range raw.Pages {
		s.pages = append(s.pages, p.Object)
	}
	return s, nil
}

// value is object ref's dictionary, or a stream's dictionary.
func (s *source) value(ref string) (dict, error) {
	var o struct {
		Value  dict `json:"value"`
		Stream struct {
			Dict dict `json:"dict"`
		} `json:"stream"`
	}
	raw, ok := s.objects["obj:"+ref]
	if !ok {
		return nil, fmt.Errorf("object %s is missing", ref)
	}
	if err := json.Unmarshal(raw, &o); err != nil {
		return nil, fmt.Errorf("object %s: %v", ref, err)
	}
	if o.Value != nil {
		return o.Value, nil
	}
	return o.Stream.Dict, nil
}

// resolve follows a reference to a non-stream object's value; a reference
// to a stream, and a direct value, come back as they are.
func (s *source) resolve(v json.RawMessage) (json.RawMessage, error) {
	var ref string
	if json.Unmarshal(v, &ref) != nil || !strings.HasSuffix(ref, " R") {
		return v, nil
	}
	var o struct {
		Value  json.RawMessage `json:"value"`
		Stream json.RawMessage `json:"stream"`
	}
	if err := json.Unmarshal(s.objects["obj:"+ref], &o); err != nil || (o.Value == nil && o.Stream == nil) {
		return nil, fmt.Errorf("object %s is missing", ref)
	}
	if o.Stream != nil {
		return v, nil
	}
	return o.Value, nil
}

// inherited is key from the page or the nearest Pages node above it.
func (s *source) inherited(page dict, key string) (json.RawMessage, error) {
	for range 64 {
		if v, ok := page[key]; ok {
			return v, nil
		}
		var parent string
		if json.Unmarshal(page["/Parent"], &parent) != nil {
			return nil, nil
		}
		var err error
		if page, err = s.value(parent); err != nil {
			return nil, err
		}
	}
	return nil, fmt.Errorf("the page tree is too deep or has a cycle")
}

// form is page ref as a form XObject: its content streams, decoded and
// joined, with its resources and transparency group, clipped to its
// CropBox.
func (s *source) form(ctx context.Context, in, ref string, p pdf.Page) (any, error) {
	page, err := s.value(ref)
	if err != nil {
		return nil, err
	}
	d := dict{"/Type": raw("/XObject"), "/Subtype": raw("/Form"), "/BBox": raw(p.Crop)}
	if res, err := s.inherited(page, "/Resources"); err != nil {
		return nil, err
	} else if res != nil {
		d["/Resources"] = res
	}
	if g, ok := page["/Group"]; ok {
		d["/Group"] = g
	}
	var refs []string
	if c, ok := page["/Contents"]; ok {
		c, err := s.resolve(c)
		if err != nil {
			return nil, err
		}
		var one string
		if json.Unmarshal(c, &one) == nil {
			refs = []string{one}
		} else if err := json.Unmarshal(c, &refs); err != nil {
			return nil, fmt.Errorf("page %s: unreadable /Contents", ref)
		}
	}
	var content bytes.Buffer
	for _, r := range refs {
		id := strings.Join(strings.Fields(strings.TrimSuffix(r, " R")), ",")
		res, err := engine.Run(ctx, engine.Cmd{Engine: "qpdf", Args: []string{in, "--show-object=" + id, "--filtered-stream-data"}, Inputs: []string{in}})
		if err != nil {
			return nil, err
		}
		content.Write(res.Stdout)
		content.WriteByte('\n')
	}
	return stream(d, content.Bytes()), nil
}

func raw(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

func stream(d dict, data []byte) any {
	return map[string]any{"stream": map[string]any{"dict": d, "data": base64.StdEncoding.EncodeToString(data)}}
}

// carried are the catalog entries that still describe the sheets: the
// output intent a print master names, and the optional-content groups its
// forms' content may switch on and off.
var carried = []string{"/OutputIntents", "/OCProperties"}

// update is the qpdf JSON update that turns the source into the sheets: a
// form per source page, a sheet drawing it, a new page tree, and a catalog
// holding that tree and the carried entries. The source's pages, its
// structure tree and its outline become unreferenced, so qpdf leaves them
// out.
func update(ctx context.Context, in string, s *source, pages []pdf.Page, sheets []Sheet) ([]byte, error) {
	if len(sheets) != len(pages) {
		return nil, fmt.Errorf("%d sheets for %d pages", len(sheets), len(pages))
	}
	var root struct {
		Value struct {
			Root string `json:"/Root"`
		} `json:"value"`
	}
	if err := json.Unmarshal(s.objects["trailer"], &root); err != nil || root.Value.Root == "" {
		return nil, fmt.Errorf("%s has no document catalog", in)
	}
	next := s.maxID
	id := func() string { next++; return fmt.Sprintf("%d 0 R", next) }
	objs := map[string]any{}
	tree := id()
	var kids []string
	for i, p := range pages {
		f, err := s.form(ctx, in, s.pages[i], p)
		if err != nil {
			return nil, err
		}
		sh := sheets[i]
		formRef, contentRef, pageRef := id(), id(), id()
		objs["obj:"+formRef] = f
		objs["obj:"+contentRef] = stream(dict{}, sh.Content)
		res := map[string]any{"/XObject": map[string]string{"/F": formRef}}
		if len(sh.ColorSpaces) > 0 {
			cs := map[string]any{}
			for k, v := range sh.ColorSpaces {
				cs["/"+k] = v
			}
			res["/ColorSpace"] = cs
		}
		pd := dict{
			"/Type":      raw("/Page"),
			"/Parent":    raw(tree),
			"/MediaBox":  raw(sh.Media),
			"/Resources": raw(res),
			"/Contents":  raw(contentRef),
		}
		for k, v := range sh.Boxes {
			pd["/"+k] = raw(v)
		}
		objs["obj:"+pageRef] = map[string]any{"value": pd}
		kids = append(kids, pageRef)
	}
	objs["obj:"+tree] = map[string]any{"value": dict{"/Type": raw("/Pages"), "/Kids": raw(kids), "/Count": raw(len(kids))}}
	catalog := dict{"/Type": raw("/Catalog"), "/Pages": raw(tree)}
	old, err := s.value(root.Value.Root)
	if err != nil {
		return nil, err
	}
	for _, k := range carried {
		if v, ok := old[k]; ok {
			catalog[k] = v
		}
	}
	objs["obj:"+root.Value.Root] = map[string]any{"value": catalog}
	return json.Marshal(map[string]any{"qpdf": []any{s.header, objs}})
}
