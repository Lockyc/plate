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

// snapTiles steps each tiling pattern in master by exactly its tile where
// the step overshoots the tile by a sliver, as tileSteps judges. Chrome
// writes a CSS gradient with a transparent stop as a soft-masked tiling
// pattern whose steps are two units wider than its BBox, and pdfwrite keeps
// them: the step is wider than the tile, so a renderer may draw a gap
// between tiles. It returns how many patterns it stepped and how many it
// left alone because their BBox or a step is indirect, and leaves master
// as it is when it steps none. master is replaced by a rename from its own
// directory, so it is never half-written.
func snapTiles(ctx context.Context, master string) (snapped, skipped int, err error) {
	res, err := engine.Run(ctx, engine.Cmd{Engine: "qpdf", Args: []string{"--warning-exit-0", "--json=2", "--json-key=qpdf", "--json-stream-data=none", master, "-"}, Inputs: []string{master}})
	if err != nil {
		return 0, 0, err
	}
	var doc struct {
		QPDF []json.RawMessage `json:"qpdf"`
	}
	var objs map[string]json.RawMessage
	if err := json.Unmarshal(res.Stdout, &doc); err != nil || len(doc.QPDF) != 2 {
		return 0, 0, fmt.Errorf("%s: qpdf's JSON is not what press reads: %v", master, err)
	}
	if err := json.Unmarshal(doc.QPDF[1], &objs); err != nil {
		return 0, 0, fmt.Errorf("%s: qpdf's JSON objects: %v", master, err)
	}
	patch, skipped := tileSteps(objs)
	if len(patch) == 0 {
		return 0, skipped, nil
	}
	b, err := json.Marshal(map[string]any{"qpdf": []any{doc.QPDF[0], patch}})
	if err != nil {
		return 0, 0, err
	}
	tmp, err := os.MkdirTemp("", "press-tiles-")
	if err != nil {
		return 0, 0, err
	}
	defer os.RemoveAll(tmp)
	patchFile := filepath.Join(tmp, "update.json")
	if err := os.WriteFile(patchFile, b, 0o600); err != nil {
		return 0, 0, err
	}
	f, err := os.CreateTemp(filepath.Dir(master), ".press-tiles-*.pdf")
	if err != nil {
		return 0, 0, err
	}
	out := f.Name()
	f.Close()
	defer os.Remove(out)
	// The update leaves every stream's data as it is, so the images keep
	// their JPEG and Flate bytes.
	if _, err := engine.Run(ctx, engine.Cmd{Engine: "qpdf", Args: []string{"--warning-exit-0", "--deterministic-id", "--update-from-json=" + patchFile, master, out}, Inputs: []string{master, patchFile}, Outputs: []string{out}}); err != nil {
		return 0, 0, err
	}
	if err := os.Rename(out, master); err != nil {
		return 0, 0, err
	}
	return len(patch), skipped, nil
}

// maxSliver is the most, in pattern space, a step may overshoot its tile
// and still be stepped to it: Chrome's overshoot is 2 units.
const maxSliver = 2

// tileSteps is a qpdf JSON update, by "obj:N 0 R" key, for each tiling
// pattern among objs with an axis whose step overshoots the tile by more
// than nothing and at most min(maxSliver, 1% of the tile); that axis's step
// becomes the tile's size, sign kept. A wider or a negative gap is the
// design (a spaced or an overlapping pattern) and stays. Each update carries
// the pattern's whole dictionary, since an update replaces it, and no data,
// so the stream's own stays. skipped counts the tiling patterns whose BBox
// or a step is indirect, which are left alone.
func tileSteps(objs map[string]json.RawMessage) (patch map[string]any, skipped int) {
	patch = map[string]any{}
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
			skipped++
			continue
		}
		nx, ny := snapStep(box[0], box[2], x), snapStep(box[1], box[3], y)
		if nx == x && ny == y {
			continue
		}
		dict := map[string]any{}
		for k, v := range d {
			dict[k] = v
		}
		dict["/XStep"], dict["/YStep"] = nx, ny
		patch[ref] = map[string]any{"stream": map[string]any{"dict": dict}}
	}
	return patch, skipped
}

// snapStep is step, or the tile's size from lo to hi with step's sign when
// step overshoots it by a sliver. Sizes round to the 1e-4 a PDF real
// carries, so a BBox of 12.7 to 53.1 is a tile of 40.4.
func snapStep(lo, hi, step float64) float64 {
	tile := math.Round(math.Abs(hi-lo)*1e4) / 1e4
	gap := math.Round((math.Abs(step)-tile)*1e4) / 1e4
	if gap <= 0 || gap > min(maxSliver, tile/100) {
		return step
	}
	return math.Copysign(tile, step)
}
