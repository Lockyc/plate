// Package impose puts two A5 pages, a flyer's front and back, two up on
// both sides of an A4 sheet: printed double-sided with flip on short edge
// and cut down the middle, one sheet makes two flyers.
//
// Each source page becomes a form XObject holding its own content stream
// and resources, drawn twice on its side of the sheet through the transform
// place computes. qpdf writes the result from a JSON update to the source,
// so every font, image and pattern is the source's object, unchanged and
// stored once; Ghostscript's pdfwrite, the alternative, cuts an image under
// a curved clip into one-pixel strips and inflates a photo flyer ninefold.
package impose

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/lockyc/plate/internal/atomicfile"
	"github.com/lockyc/plate/internal/cli"
	"github.com/lockyc/plate/internal/engine"
	"github.com/lockyc/plate/internal/pdf"
)

const usage = "impose [--cut-mark] [--force] -o out.pdf <in.pdf>"

func mm(v float64) float64 { return v * 72 / 25.4 }

var (
	// sheetW and sheetH are A4 landscape, in points.
	sheetW, sheetH = mm(297), mm(210)
	// a5Short and a5Long are A5's sides.
	a5Short, a5Long = mm(148), mm(210)
	// slack is how far a page may be from A5 and still count as A5. Chrome
	// prints A5 as 420 x 594 pt, rounding to whole multiples of 8 CSS px.
	slack = mm(1)
	// tick is a cut mark's length from the sheet's edge: long enough to
	// clear the few millimetres an office printer cannot reach.
	tick = mm(8)
)

// matrix is a PDF transform [a b c d e f]: x' = ax + cy + e, y' = bx + dy + f.
type matrix [6]float64

// place maps page p's user space into one half of the sheet (cell 0 left, 1
// right) for the front or the back: unscaled, centred in the half, and
// turned upright. A portrait page goes in as a viewer shows it. A landscape
// page is turned a quarter to fit a portrait half, the front anticlockwise
// and the back clockwise: turned over about the sheet's vertical axis, as
// flip on short edge does, the cut flyer then turns over like a landscape
// page printed on its own.
func place(p pdf.Page, back bool, cell int) matrix {
	turn := -p.Rotate // anticlockwise degrees; /Rotate is clockwise
	if w, h := p.Size(); w > h {
		if back {
			turn -= 90
		} else {
			turn += 90
		}
	}
	cos, sin := quarter(turn)
	m := matrix{cos, sin, -sin, cos, 0, 0}
	b := p.Crop
	lo := [2]float64{math.Inf(1), math.Inf(1)}
	hi := [2]float64{math.Inf(-1), math.Inf(-1)}
	for _, xy := range [][2]float64{{b[0], b[1]}, {b[2], b[1]}, {b[0], b[3]}, {b[2], b[3]}} {
		x, y := m[0]*xy[0]+m[2]*xy[1], m[1]*xy[0]+m[3]*xy[1]
		lo = [2]float64{min(lo[0], x), min(lo[1], y)}
		hi = [2]float64{max(hi[0], x), max(hi[1], y)}
	}
	half := sheetW / 2
	m[4] = float64(cell)*half + (half-(hi[0]-lo[0]))/2 - lo[0]
	m[5] = (sheetH-(hi[1]-lo[1]))/2 - lo[1]
	return m
}

// quarter is the exact cosine and sine of a multiple of 90 degrees.
func quarter(deg int) (cos, sin float64) {
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

func isA5(p pdf.Page) bool {
	w, h := p.Size()
	short, long := min(w, h), max(w, h)
	return math.Abs(short-a5Short) <= slack && math.Abs(long-a5Long) <= slack
}

func pt(v float64) string { return strconv.FormatFloat(math.Round(v*100)/100, 'f', -1, 64) }

// check refuses anything but a front and a back, each A5, both portrait or
// both landscape.
func check(pages []pdf.Page) error {
	if len(pages) == 0 {
		return fmt.Errorf("want 2 A5 pages, found 0 pages")
	}
	bad := pages[0]
	for _, p := range pages {
		if !isA5(p) {
			bad = p
			break
		}
	}
	if len(pages) != 2 || !isA5(bad) {
		w, h := bad.Size()
		return fmt.Errorf("want 2 A5 pages, found %d pages of %sx%spt", len(pages), pt(w), pt(h))
	}
	orient := func(p pdf.Page) string {
		if w, h := p.Size(); w > h {
			return "landscape"
		}
		return "portrait"
	}
	if f, b := orient(pages[0]), orient(pages[1]); f != b {
		return fmt.Errorf("the front is %s and the back %s; want both the same", f, b)
	}
	return nil
}

func num(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) }

// sideContent draws form /F twice, once in each half of the sheet, each copy
// clipped to its half, then the cut marks.
func sideContent(p pdf.Page, back, cutMark bool) []byte {
	var b bytes.Buffer
	half := sheetW / 2
	for cell := 0; cell < 2; cell++ {
		m := place(p, back, cell)
		fmt.Fprintf(&b, "q %s 0 %s %s re W n %s %s %s %s %s %s cm /F Do Q\n",
			num(float64(cell)*half), num(half), num(sheetH),
			num(m[0]), num(m[1]), num(m[2]), num(m[3]), num(m[4]), num(m[5]))
	}
	if cutMark {
		fmt.Fprintf(&b, "q 0 G 0.5 w %[1]s 0 m %[1]s %[2]s l %[1]s %[3]s m %[1]s %[4]s l S Q\n",
			num(half), num(tick), num(sheetH-tick), num(sheetH))
	}
	return b.Bytes()
}

// dict is a PDF dictionary in qpdf's JSON: names as "/Name", references as
// "N 0 R".
type dict map[string]json.RawMessage

// source is the part of `qpdf --json` impose reads: the page objects in
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
		return nil, fmt.Errorf("%s: qpdf's JSON is not what impose reads: %v", in, err)
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

// carried are the catalog entries that still describe the sheet: the output
// intent a print master names, and the optional-content groups its forms'
// content may switch on and off.
var carried = []string{"/OutputIntents", "/OCProperties"}

// update is the qpdf JSON update that turns the source into the sheet: a
// form per source page, a sheet side drawing each twice, a new page tree,
// and a catalog holding that tree and the carried entries. The source's
// pages, its structure tree and its outline become unreferenced, so qpdf
// leaves them out.
func update(ctx context.Context, in string, s *source, pages []pdf.Page, cutMark bool) ([]byte, error) {
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
		formRef, contentRef, pageRef := id(), id(), id()
		objs["obj:"+formRef] = f
		objs["obj:"+contentRef] = stream(dict{}, sideContent(p, i == 1, cutMark))
		objs["obj:"+pageRef] = map[string]any{"value": dict{
			"/Type":      raw("/Page"),
			"/Parent":    raw(tree),
			"/MediaBox":  raw([]float64{0, 0, sheetW, sheetH}),
			"/Resources": raw(map[string]any{"/XObject": map[string]string{"/F": formRef}}),
			"/Contents":  raw(contentRef),
		}}
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

// Main runs `plate impose`.
func Main(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := cli.Flags("impose", usage, stderr)
	cutMark := fs.Bool("cut-mark", false, "draw short ticks at each end of the centre line, where the sheet is cut")
	force := fs.Bool("force", false, "replace an existing output")
	out := fs.String("o", "", "the A4 sheet to write (PDF)")
	rest, code, ok := cli.Parse(fs, args, 1)
	if !ok {
		return code
	}
	if *out == "" {
		return cli.Usage(stderr, "impose", "-o out.pdf is required")
	}
	if err := Run(ctx, rest[0], *out, *cutMark, *force); err != nil {
		return cli.Fail(stderr, "impose", err)
	}
	fmt.Fprintf(stdout, "wrote %s\n", *out)
	return 0
}

// Run imposes the two A5 pages of in onto the A4 sheet out, as `plate
// impose` does; an existing out is replaced only when force is set.
func Run(ctx context.Context, in, out string, cutMark, force bool) error {
	if engine.SameFile(in, out) {
		return fmt.Errorf("%s is both the input and the output; write the sheet elsewhere", out)
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
	if err := check(pages); err != nil {
		return err
	}
	s, err := readSource(ctx, in)
	if err != nil {
		return err
	}
	if len(s.pages) != len(pages) {
		return fmt.Errorf("%s: qpdf found %d pages, pdfinfo %d", in, len(s.pages), len(pages))
	}
	patch, err := update(ctx, in, s, pages, cutMark)
	if err != nil {
		return err
	}
	dir, err := os.MkdirTemp("", "plate-impose-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	patchFile, sheet := filepath.Join(dir, "update.json"), filepath.Join(dir, "sheet.pdf")
	if err := os.WriteFile(patchFile, patch, 0o600); err != nil {
		return err
	}
	// --deterministic-id derives the /ID from the content, so one input
	// imposes to the same bytes every run.
	if _, err := engine.Run(ctx, engine.Cmd{Engine: "qpdf", Args: []string{
		"--deterministic-id", "--object-streams=generate", "--update-from-json=" + patchFile, in, sheet,
	}, Inputs: []string{in, patchFile}, Outputs: []string{sheet}}); err != nil {
		return err
	}
	result, err := os.ReadFile(sheet)
	if err != nil {
		return err
	}
	return atomicfile.WriteFile(out, result, 0o644)
}
