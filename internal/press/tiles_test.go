package press

import (
	"encoding/json"
	"os"
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
		"obj:5 0 R":  json.RawMessage(tiling("[0, 0, 840, 594]", "842", "596")),
		"obj:6 0 R":  json.RawMessage(tiling("[12.7, 0, 53.1, 40]", "40.4", "40")),
		"obj:7 0 R":  json.RawMessage(tiling("[0, 0, 40, 40]", "-42", "40")),
		"obj:8 0 R":  json.RawMessage(`{"value": {"/PatternType": 2, "/Shading": "9 0 R"}}`),
		"obj:10 0 R": json.RawMessage(`{"stream": {"dict": {"/Subtype": "/Image", "/Width": 4}}}`),
	}
	patch, err := tileSteps(objs)
	if err != nil {
		t.Fatal(err)
	}
	if len(patch) != 2 {
		t.Fatalf("patched %d objects, want 5 and 7: %v", len(patch), patch)
	}
	for ref, want := range map[string][2]float64{"obj:5 0 R": {840, 594}, "obj:7 0 R": {-40, 40}} {
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
	for _, bad := range []string{tiling(`"12 0 R"`, "842", "596"), tiling("[0, 0, 840]", "842", "596"), tiling("[0, 0, 840, 594]", `"13 0 R"`, "596")} {
		if _, err := tileSteps(map[string]json.RawMessage{"obj:5 0 R": json.RawMessage(bad)}); err == nil {
			t.Errorf("a pattern press cannot read was accepted: %s", bad)
		}
	}
}

func TestPressSnapsTileSteps(t *testing.T) {
	e := setup(t)
	qpdf := enginetest.Stub(t, "qpdf", `case "$2" in
  --json=2) printf '{"qpdf": [{"jsonversion": 2}, {"obj:5 0 R": %s}]}\n' "$TILING"; exit 0 ;;
  --deterministic-id) cp "${3#--update-from-json=}" "$PATCH_COPY"; cp "$4" "$5"; exit 0 ;;
esac
printf '%s\n' "${QDF:-/DeviceCMYK k}" > "$5"`)
	patch := e.dir + "/patch.json"
	t.Setenv("PATCH_COPY", patch)
	t.Setenv("TILING", tiling("[0, 0, 840, 594]", "842", "596"))
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
	if !strings.Contains(e.stdout.String(), "1 tiling pattern(s) stepped to their tile") {
		t.Errorf("stdout %q", e.stdout.String())
	}

	e = setup(t)
	enginetest.Stub(t, "qpdf", `case "$2" in
  --json=2) printf '{"qpdf": [{"jsonversion": 2}, {"obj:5 0 R": %s}]}\n' "$TILING"; exit 0 ;;
  --deterministic-id) echo "an update with nothing to change" >&2; exit 1 ;;
esac
printf '%s\n' "${QDF:-/DeviceCMYK k}" > "$5"`)
	t.Setenv("TILING", tiling("[0, 0, 840, 594]", "840", "594"))
	if code := e.run(); code != 0 {
		t.Fatalf("matched steps: code %d: %s", code, e.stderr.String())
	}
	if strings.Contains(e.stdout.String(), "tiling") {
		t.Errorf("stdout %q", e.stdout.String())
	}
}
