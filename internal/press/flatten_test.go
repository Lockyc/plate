package press

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/lockyc/plate/internal/enginetest"
)

func objects(t *testing.T, s string) map[string]json.RawMessage {
	t.Helper()
	var m map[string]json.RawMessage
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		t.Fatal(err)
	}
	return m
}

const chromeGradient = `{
 "obj:6 0 R": {"stream": {"dict": {"/BBox": [0, 0, 840, 594], "/PatternType": 1, "/Resources": {"/ExtGState": {"/G3": "3 0 R"}, "/XObject": {"/X4": "4 0 R"}}, "/TilingType": 1, "/Type": "/Pattern", "/XStep": 842, "/YStep": 596}}},
 "obj:3 0 R": {"value": {"/BM": "/Normal", "/ca": 1}},
 "obj:4 0 R": {"stream": {"dict": {"/SMask": "5 0 R", "/Subtype": "/Image", "/Width": 840}}}
}`

func TestTransparentTiles(t *testing.T) {
	for _, c := range []struct {
		name, objs string
		want       bool
	}{
		{"chrome's gradient: an image with a soft mask", chromeGradient, true},
		{"no pattern, a soft-masked cut-out", `{"obj:4 0 R": {"stream": {"dict": {"/SMask": "5 0 R", "/Subtype": "/Image"}}}}`, false},
		{"an opaque tile", `{"obj:6 0 R": {"stream": {"dict": {"/PatternType": 1, "/Resources": {"/ExtGState": {"/G3": {"/ca": 1, "/SMask": "/None"}}, "/XObject": {"/X4": "4 0 R"}}}}},
		  "obj:4 0 R": {"stream": {"dict": {"/Subtype": "/Image", "/SMask": "/None"}}}}`, false},
		{"a shading pattern", `{"obj:6 0 R": {"value": {"/PatternType": 2, "/ExtGState": {"/ca": 0.5}}}}`, false},
		{"fill alpha", `{"obj:6 0 R": {"stream": {"dict": {"/PatternType": 1, "/Resources": {"/ExtGState": {"/G": "3 0 R"}}}}}, "obj:3 0 R": {"value": {"/ca": 0.4}}}`, true},
		{"a soft mask in the graphics state", `{"obj:6 0 R": {"stream": {"dict": {"/PatternType": 1, "/Resources": {"/ExtGState": {"/G": {"/SMask": "9 0 R"}}}}}}}`, true},
		{"a blend mode", `{"obj:6 0 R": {"stream": {"dict": {"/PatternType": 1, "/Resources": {"/ExtGState": {"/G": {"/BM": "/Multiply"}}}}}}}`, true},
		{"a transparency group, two forms down, with a cycle", `{
		  "obj:6 0 R": {"stream": {"dict": {"/PatternType": 1, "/Resources": {"/XObject": {"/F": "7 0 R"}}}}},
		  "obj:7 0 R": {"stream": {"dict": {"/Subtype": "/Form", "/Resources": {"/XObject": {"/F": "8 0 R", "/Me": "7 0 R"}}}}},
		  "obj:8 0 R": {"stream": {"dict": {"/Subtype": "/Form", "/Group": {"/S": "/Transparency"}}}}}`, true},
		{"resources by reference", `{"obj:6 0 R": {"stream": {"dict": {"/PatternType": 1, "/Resources": "9 0 R"}}}, "obj:9 0 R": {"value": {"/XObject": {"/X": "4 0 R"}}}, "obj:4 0 R": {"stream": {"dict": {"/SMask": "5 0 R"}}}}`, true},
	} {
		if got := transparentTiles(objects(t, c.objs)); got != c.want {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
}

func TestPressFlattensOnlyTransparentTiles(t *testing.T) {
	for _, c := range []struct {
		objs, level, log string
	}{
		{"{}", "-dCompatibilityLevel=1.4", "transparency kept live (PDF 1.4)"},
		{chromeGradient, "-dCompatibilityLevel=1.3", "transparency flattened (PDF 1.3)"},
	} {
		e := setup(t)
		enginetest.Stub(t, "qpdf", `[ "$2" = --json=2 ] && { printf '{"qpdf": [{"jsonversion": 2}, %s]}\n' "$OBJS"; exit 0; }
printf '%s\n' "${QDF:-/DeviceCMYK k}" > "$5"`)
		t.Setenv("OBJS", c.objs)
		if code := e.run(); code != 0 {
			t.Fatalf("code %d: %s", code, e.stderr.String())
		}
		if write := enginetest.Calls(t, e.gs)[0]; !slices.Contains(write, c.level) {
			t.Errorf("pdfwrite args lack %s: %q", c.level, write)
		}
		if !strings.Contains(e.stdout.String(), c.log) {
			t.Errorf("stdout %q lacks %q", e.stdout.String(), c.log)
		}
	}
}
