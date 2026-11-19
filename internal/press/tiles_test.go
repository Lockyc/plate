package press

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lockyc/plate/internal/enginetest"
)

// tiling is a tiling pattern stream object as qpdf --json writes it.
func tiling(bbox, xstep, ystep string) string {
	return `{"stream": {"dict": {"/BBox": ` + bbox + `, "/Filter": "/FlateDecode", "/Length": 81, "/PaintType": 1, "/PatternType": 1, "/Resources": {"/XObject": {"/R11": "11 0 R"}}, "/TilingType": 1, "/Type": "/Pattern", "/XStep": ` + xstep + `, "/YStep": ` + ystep + `}}}`
}

func TestTileSteps(t *testing.T) {
	objs := map[string]json.RawMessage{
		"obj:1 0 R":  json.RawMessage(`{"value": {"/Type": "/Catalog"}}`),
		"obj:5 0 R":  json.RawMessage(tiling("[0, 0, 840, 594]", "842", "596")),    // Chrome's gradient
		"obj:6 0 R":  json.RawMessage(tiling("[12.7, 0, 53.1, 40]", "40.4", "40")), // exact, through float error
		"obj:7 0 R":  json.RawMessage(tiling("[0, 0, 840, 594]", "-842", "596")),   // sign kept
		"obj:8 0 R":  json.RawMessage(`{"value": {"/PatternType": 2, "/Shading": "9 0 R"}}`),
		"obj:10 0 R": json.RawMessage(`{"stream": {"dict": {"/Subtype": "/Image", "/Width": 4}}}`),
		"obj:11 0 R": json.RawMessage(tiling("[0, 0, 10, 10]", "20", "20")),          // spaced on purpose
		"obj:12 0 R": json.RawMessage(tiling("[0, 0, 20, 20]", "10", "10")),          // overlapping on purpose
		"obj:13 0 R": json.RawMessage(tiling("[0, 0, 40, 40]", "40", "40")),          // exact
		"obj:14 0 R": json.RawMessage(tiling("[0, 0, 40, 40]", "42", "40")),          // 2 units is 5% of a 40 tile
		"obj:15 0 R": json.RawMessage(tiling("[0, 0, 840, 40]", "842", "50")),        // one axis a sliver, one spaced
		"obj:16 0 R": json.RawMessage(tiling(`"12 0 R"`, "842", "596")),              // indirect BBox
		"obj:17 0 R": json.RawMessage(tiling("[0, 0, 840]", "842", "596")),           // short BBox
		"obj:18 0 R": json.RawMessage(tiling("[0, 0, 840, 594]", `"13 0 R"`, "596")), // indirect step
	}
	patch, skipped := tileSteps(objs)
	if skipped != 3 {
		t.Errorf("skipped %d, want 16, 17 and 18", skipped)
	}
	want := map[string][2]float64{"obj:5 0 R": {840, 594}, "obj:7 0 R": {-840, 594}, "obj:15 0 R": {840, 50}}
	if len(patch) != len(want) {
		t.Fatalf("patched %d objects, want 5, 7 and 15: %v", len(patch), patch)
	}
	for ref, want := range want {
		var o struct {
			Stream struct {
				Dict map[string]any `json:"dict"`
			} `json:"stream"`
		}
		b, _ := json.Marshal(patch[ref])
		if err := json.Unmarshal(b, &o); err != nil {
			t.Fatalf("%s: %v", ref, err)
		}
		d := o.Stream.Dict
		if d["/XStep"] != want[0] || d["/YStep"] != want[1] {
			t.Errorf("%s steps %v %v, want %v", ref, d["/XStep"], d["/YStep"], want)
		}
		// The stream's data stays as it is, so its filter must too.
		if d["/Filter"] != "/FlateDecode" || d["/Resources"] == nil {
			t.Errorf("%s lost the rest of its dictionary: %v", ref, d)
		}
	}
}

const tilesQPDF = `case "$2" in
  --json=2) printf '{"qpdf": [{"jsonversion": 2}, {"obj:5 0 R": %s, "obj:6 0 R": %s}]}\n' "$TILING" "${OTHER:-null}"; exit 0 ;;
  --deterministic-id) [ -n "$UPDATE_FAILS" ] && { echo "update failed" >&2; exit 1; }
    cp "${3#--update-from-json=}" "$PATCH_COPY"; cp "$4" "$5"; exit 0 ;;
esac
printf '%s\n' "${QDF:-/DeviceCMYK k}" > "$5"`

func TestPressSnapsTileSteps(t *testing.T) {
	e := setup(t)
	qpdf := enginetest.Stub(t, "qpdf", tilesQPDF)
	patch := filepath.Join(t.TempDir(), "patch.json")
	t.Setenv("PATCH_COPY", patch)
	t.Setenv("UPDATE_FAILS", "")
	t.Setenv("TILING", tiling("[0, 0, 840, 594]", "842", "596"))
	t.Setenv("OTHER", tiling(`"12 0 R"`, "842", "596"))
	if code := e.run(); code != 0 {
		t.Fatalf("code %d: %s", code, e.stderr.String())
	}
	b, err := os.ReadFile(patch)
	if err != nil {
		t.Fatalf("no update: %v; qpdf calls %q", err, enginetest.Calls(t, qpdf))
	}
	if !strings.Contains(string(b), `"/XStep":840`) || !strings.Contains(string(b), `"/YStep":594`) {
		t.Errorf("patch %s", b)
	}
	for _, want := range []string{"1 tiling pattern(s) stepped to their tile", "1 tiling pattern(s) left alone: indirect BBox or step"} {
		if !strings.Contains(e.stdout.String(), want) {
			t.Errorf("stdout %q lacks %q", e.stdout.String(), want)
		}
	}
	// The update is written beside the master, so the rename that replaces
	// it never crosses a volume, and nothing of it is left there.
	for _, call := range enginetest.Calls(t, qpdf) {
		if call[1] == "--deterministic-id" && filepath.Dir(call[4]) != e.dir {
			t.Errorf("update written to %s, not beside the master in %s", call[4], e.dir)
		}
	}
	if left, _ := filepath.Glob(filepath.Join(e.dir, ".press-tiles-*")); len(left) > 0 {
		t.Errorf("left behind %v", left)
	}

	e = setup(t)
	enginetest.Stub(t, "qpdf", tilesQPDF)
	t.Setenv("OTHER", "")
	t.Setenv("UPDATE_FAILS", "1")
	t.Setenv("TILING", tiling("[0, 0, 840, 594]", "840", "594"))
	if code := e.run(); code != 0 {
		t.Fatalf("matched steps: code %d: %s", code, e.stderr.String())
	}
	if strings.Contains(e.stdout.String(), "tiling") {
		t.Errorf("stdout %q", e.stdout.String())
	}

	e = setup(t)
	enginetest.Stub(t, "qpdf", tilesQPDF)
	t.Setenv("TILING", tiling("[0, 0, 840, 594]", "842", "596"))
	if code := e.run(); code != 1 || e.outExists() {
		t.Errorf("a failed update: code %d, master left %v", code, e.outExists())
	}
	if left, _ := filepath.Glob(filepath.Join(e.dir, ".press-tiles-*")); len(left) > 0 {
		t.Errorf("a failed update left %v", left)
	}
}
