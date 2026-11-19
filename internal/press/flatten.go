package press

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/lockyc/plate/internal/engine"
)

// flattens reports whether press flattens in's transparency: when a tiling
// pattern in it holds transparency. pdfwrite keeps such a pattern live
// inside a transparency-group form drawn at its own scale, and Quartz (macOS
// Preview) places the pattern by the page's space instead of the form's, so
// the tile lands large and off its mark: a strip down one edge, a second
// tile in a corner. Flattened, the area is a CMYK image every renderer draws
// alike.
func flattens(ctx context.Context, in string) (bool, error) {
	res, err := engine.Run(ctx, engine.Cmd{Engine: "qpdf", Args: []string{"--warning-exit-0", "--json=2", "--json-key=qpdf", "--json-stream-data=none", in, "-"}, Inputs: []string{in}})
	if err != nil {
		return false, err
	}
	var doc struct {
		QPDF []json.RawMessage `json:"qpdf"`
	}
	var objs map[string]json.RawMessage
	if err := json.Unmarshal(res.Stdout, &doc); err != nil || len(doc.QPDF) != 2 {
		return false, fmt.Errorf("%s: qpdf's JSON is not what press reads: %v", in, err)
	}
	if err := json.Unmarshal(doc.QPDF[1], &objs); err != nil {
		return false, fmt.Errorf("%s: qpdf's JSON objects: %v", in, err)
	}
	return transparentTiles(objs), nil
}

// pdfDict is a dictionary in qpdf's JSON: names as "/Name", references as
// "N 0 R".
type pdfDict map[string]json.RawMessage

// transparentTiles reports whether a tiling pattern among objs (qpdf JSON,
// by "obj:N 0 R") draws with transparency, through its resources or any
// form's they reach: an image with a soft mask, a graphics state with a
// soft mask, an alpha below 1 or a blend mode other than Normal, or a
// transparency group.
func transparentTiles(objs map[string]json.RawMessage) bool {
	// dict reads v as a dictionary, following a reference to an object's
	// value or stream dictionary.
	dict := func(v json.RawMessage) pdfDict {
		var ref string
		if json.Unmarshal(v, &ref) == nil {
			v = objs["obj:"+ref]
		}
		var o struct {
			Value  pdfDict `json:"value"`
			Stream struct {
				Dict pdfDict `json:"dict"`
			} `json:"stream"`
		}
		var d pdfDict
		if json.Unmarshal(v, &o) == nil && (o.Value != nil || o.Stream.Dict != nil) {
			if o.Value != nil {
				return o.Value
			}
			return o.Stream.Dict
		}
		json.Unmarshal(v, &d)
		return d
	}
	is := func(v json.RawMessage, name string) bool {
		var s string
		return json.Unmarshal(v, &s) == nil && s == name
	}
	softMask := func(v json.RawMessage) bool { return v != nil && !is(v, "/None") }
	// blends reports whether a /BM blends: a name, or an array whose first
	// entry is the mode a reader that knows it uses.
	blends := func(v json.RawMessage) bool {
		if v == nil {
			return false
		}
		var modes []json.RawMessage
		if json.Unmarshal(v, &modes) == nil && len(modes) > 0 {
			v = modes[0]
		}
		return !is(v, "/Normal") && !is(v, "/Compatible")
	}
	seen := map[string]bool{}
	var transparent func(res json.RawMessage) bool
	transparent = func(res json.RawMessage) bool {
		var ref string
		if json.Unmarshal(res, &ref) == nil {
			if seen[ref] {
				return false
			}
			seen[ref] = true
		}
		r := dict(res)
		for _, gs := range dict(r["/ExtGState"]) {
			g := dict(gs)
			for _, k := range []string{"/ca", "/CA"} {
				var a float64
				if json.Unmarshal(g[k], &a) == nil && a < 1 {
					return true
				}
			}
			if softMask(g["/SMask"]) || blends(g["/BM"]) {
				return true
			}
		}
		for _, x := range dict(r["/XObject"]) {
			var ref string
			if json.Unmarshal(x, &ref) == nil {
				if seen[ref] {
					continue
				}
				seen[ref] = true
			}
			d := dict(x)
			if softMask(d["/SMask"]) || is(dict(d["/Group"])["/S"], "/Transparency") {
				return true
			}
			if d["/Resources"] != nil && transparent(d["/Resources"]) {
				return true
			}
		}
		return false
	}
	for _, raw := range objs {
		d := dict(raw)
		var kind float64
		if json.Unmarshal(d["/PatternType"], &kind) == nil && kind == 1 && transparent(d["/Resources"]) {
			return true
		}
	}
	return false
}
