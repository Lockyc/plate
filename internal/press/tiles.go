package press

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"

	"github.com/lockyc/plate/internal/engine"
)

// snapTiles sets the step of every tiling pattern in master to its tile:
// XStep to the BBox's width and YStep to its height, keeping each step's
// sign. Chrome writes a CSS gradient with a transparent stop as a
// soft-masked tiling pattern whose steps are two units wider than its BBox,
// and pdfwrite keeps them; Quartz draws that gap as a seam and the start of
// a second tile. It returns how many patterns it changed, and leaves master
// as it is when there are none.
func snapTiles(ctx context.Context, master string) (int, error) {
	res, err := engine.Run(ctx, engine.Cmd{Engine: "qpdf", Args: []string{"--warning-exit-0", "--json=2", "--json-key=qpdf", "--json-stream-data=none", master, "-"}, Inputs: []string{master}})
	if err != nil {
		return 0, err
	}
	var doc struct {
		QPDF []json.RawMessage `json:"qpdf"`
	}
	var objs map[string]json.RawMessage
	if err := json.Unmarshal(res.Stdout, &doc); err != nil || len(doc.QPDF) != 2 {
		return 0, fmt.Errorf("%s: qpdf's JSON is not what press reads: %v", master, err)
	}
	if err := json.Unmarshal(doc.QPDF[1], &objs); err != nil {
		return 0, fmt.Errorf("%s: qpdf's JSON objects: %v", master, err)
	}
	patch, err := tileSteps(objs)
	if err != nil || len(patch) == 0 {
		return 0, err
	}
	tmp, err := os.MkdirTemp("", "press-tiles-")
	if err != nil {
		return 0, err
	}
	defer os.RemoveAll(tmp)
	b, err := json.Marshal(map[string]any{"qpdf": []any{doc.QPDF[0], patch}})
	if err != nil {
		return 0, err
	}
	patchFile, out := filepath.Join(tmp, "update.json"), filepath.Join(tmp, "out.pdf")
	if err := os.WriteFile(patchFile, b, 0o600); err != nil {
		return 0, err
	}
	// The update leaves every stream's data as it is, so the images keep
	// their JPEG and Flate bytes.
	if _, err := engine.Run(ctx, engine.Cmd{Engine: "qpdf", Args: []string{"--warning-exit-0", "--deterministic-id", "--update-from-json=" + patchFile, master, out}, Inputs: []string{master, patchFile}, Outputs: []string{out}}); err != nil {
		return 0, err
	}
	if err := os.Rename(out, master); err != nil {
		return 0, err
	}
	return len(patch), nil
}

// tileSteps is a qpdf JSON update, by "obj:N 0 R" key, for each tiling
// pattern among objs whose XStep or YStep is not its BBox's size. Each
// update carries the pattern's whole dictionary, since an update replaces
// it, and no data, so the stream's own stays.
func tileSteps(objs map[string]json.RawMessage) (map[string]any, error) {
	patch := map[string]any{}
	for ref, raw := range objs {
		var o struct {
			Stream *struct {
				Dict map[string]json.RawMessage `json:"dict"`
			} `json:"stream"`
		}
		if json.Unmarshal(raw, &o) != nil || o.Stream == nil {
			continue
		}
		d := o.Stream.Dict
		var kind float64
		if json.Unmarshal(d["/PatternType"], &kind) != nil || kind != 1 {
			continue
		}
		var box []float64
		var x, y float64
		if json.Unmarshal(d["/BBox"], &box) != nil || len(box) != 4 ||
			json.Unmarshal(d["/XStep"], &x) != nil || json.Unmarshal(d["/YStep"], &y) != nil {
			return nil, fmt.Errorf("tiling pattern %s: want a direct BBox, XStep and YStep, got %s %s %s", ref, d["/BBox"], d["/XStep"], d["/YStep"])
		}
		w, h := cell(box[0], box[2], x), cell(box[1], box[3], y)
		if x == w && y == h {
			continue
		}
		dict := map[string]any{}
		for k, v := range d {
			dict[k] = v
		}
		dict["/XStep"], dict["/YStep"] = w, h
		patch[ref] = map[string]any{"stream": map[string]any{"dict": dict}}
	}
	return patch, nil
}

// cell is the tile's size from lo to hi with step's sign, rounded to the
// 1e-4 a PDF real carries, so a BBox of 266.66666 to 306.66666 is 40.
func cell(lo, hi, step float64) float64 {
	return math.Copysign(math.Round(math.Abs(hi-lo)*1e4)/1e4, step)
}
