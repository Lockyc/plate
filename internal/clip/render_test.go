package clip

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/lockyc/plate/internal/enginetest"
)

func writeFile(t *testing.T, path, body string) string {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// named lists the entries of dir whose names start with name + ".".
func named(t *testing.T, dir, name string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), name+".") {
			out = append(out, e.Name())
		}
	}
	return out
}

func TestRenderRefusesSyncErrorBeforeDecoding(t *testing.T) {
	dir := t.TempDir()
	script := writeFile(t, filepath.Join(dir, "clip.toml"), contractExample)
	events := writeFile(t, filepath.Join(dir, "events.jsonl"),
		strings.Join(with(contractLog, 1, strings.Replace(contractLog[0], `"sync_error_ms":8`, `"sync_error_ms":20`, 1)), "\n")+"\n")
	enginetest.Stub(t, "ffprobe", `echo '{"streams":[{"width":1206,"height":2622}],"frames":[{"pts_time":"0"}],"format":{"duration":"8.3"}}'`)
	ffmpeg := enginetest.Stub(t, "ffmpeg", `exit 99`)
	out := filepath.Join(dir, "out")
	writeFile(t, filepath.Join(dir, "raw.mov"), "x")
	if err := os.MkdirAll(out, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(out, "adding-nodes.mp4"), "an earlier run")
	var stdout, stderr bytes.Buffer
	code := Main(context.Background(), []string{"render", "--script", script, "--master", filepath.Join(dir, "raw.mov"),
		"--events", events, "--out", out}, &stdout, &stderr)
	if code != 1 || !strings.Contains(stderr.String(), "exceeds one frame") {
		t.Fatalf("code %d, stderr %q", code, stderr.String())
	}
	if c := enginetest.Calls(t, ffmpeg); len(c) != 0 {
		t.Fatalf("ffmpeg ran: %q", c)
	}
	if n := named(t, out, "adding-nodes"); len(n) != 0 {
		t.Fatalf("--out holds %q after a refusal", n)
	}
}

func TestRenderUsage(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Main(context.Background(), []string{"render", "--script", "clip.toml", "--events", "e.jsonl", "--out", "out"}, &stdout, &stderr)
	if code != 2 || !strings.Contains(stderr.String(), "usage: plate clip render") || !strings.Contains(stderr.String(), "--master") {
		t.Fatalf("code %d, stderr %q", code, stderr.String())
	}
}

func TestPublishRemovesStaleSiblings(t *testing.T) {
	tmp, out := t.TempDir(), t.TempDir()
	files := []string{"adding-nodes.mp4", "adding-nodes.poster.png", "adding-nodes.json"}
	for _, f := range files {
		writeFile(t, filepath.Join(tmp, f), "new "+f)
	}
	for _, f := range []string{"adding-nodes.webp", "adding-nodes.json", "other.mp4", "adding-nodes.png"} {
		writeFile(t, filepath.Join(out, f), "old")
	}
	if err := publish(tmp, out, "adding-nodes", files); err != nil {
		t.Fatal(err)
	}
	want := []string{"adding-nodes.json", "adding-nodes.mp4", "adding-nodes.png", "adding-nodes.poster.png"}
	if got := named(t, out, "adding-nodes"); !reflect.DeepEqual(got, want) {
		t.Fatalf("out holds %q, want %q", got, want)
	}
	if b, _ := os.ReadFile(filepath.Join(out, "adding-nodes.json")); string(b) != "new adding-nodes.json" {
		t.Fatalf("json = %q", b)
	}
	for _, f := range []string{"other.mp4", "adding-nodes.png"} {
		if b, _ := os.ReadFile(filepath.Join(out, f)); string(b) != "old" {
			t.Fatalf("%s was touched", f)
		}
	}
}

func TestPublishFailureLeavesNothing(t *testing.T) {
	tmp, out := filepath.Join(t.TempDir(), "tmp"), t.TempDir()
	if err := os.Mkdir(tmp, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(tmp, "adding-nodes.mp4"), "new")
	writeFile(t, filepath.Join(tmp, "adding-nodes.json"), "new")
	writeFile(t, filepath.Join(out, "adding-nodes.webm"), "old")
	err := publish(tmp, out, "adding-nodes", []string{"adding-nodes.mp4", "adding-nodes.poster.png", "adding-nodes.json"})
	if err == nil {
		t.Fatal("publish with a missing file succeeded")
	}
	if n := named(t, out, "adding-nodes"); len(n) != 0 {
		t.Fatalf("out holds %q", n)
	}
	if _, err := os.Stat(tmp); !os.IsNotExist(err) {
		t.Fatalf("the temp dir survived: %v", err)
	}
}

func TestMeasurementJSONShape(t *testing.T) {
	b, err := json.Marshal(Measurement{Name: "n", Width: 2, Height: 4, FPS: 60, Frames: 1, DurationMS: 16.7,
		Trim: Window{Start: 1, End: 2}, Formats: map[string]FormatResult{"mp4": {File: "n.mp4", Bytes: 3}},
		Sharpness: 0.9, LoopSeam: 0.1, CrossfadeFrames: 18})
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	var keys []string
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	want := []string{"crossfade_frames", "duration_ms", "formats", "fps", "frames", "height", "loop_seam", "name", "sharpness", "trim", "width"}
	if !reflect.DeepEqual(keys, want) {
		t.Fatalf("keys = %q, want %q", keys, want)
	}
	if string(m["trim"]) != `{"start_ms":1,"end_ms":2}` || string(m["formats"]) != `{"mp4":{"file":"n.mp4","bytes":3}}` {
		t.Fatalf("trim %s, formats %s", m["trim"], m["formats"])
	}
}
