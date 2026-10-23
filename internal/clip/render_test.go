package clip

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/lockyc/plate/internal/enginetest"
	"github.com/lockyc/plate/internal/video"
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
	if n := named(t, out, "adding-nodes"); !slices.Equal(n, []string{"adding-nodes.mp4"}) {
		t.Fatalf("a refused log touched the last good clip: --out holds %q", n)
	}
}

func TestRenderBadEventsPathKeepsLastClip(t *testing.T) {
	dir := t.TempDir()
	script := writeFile(t, filepath.Join(dir, "clip.toml"), contractExample)
	out := filepath.Join(dir, "out")
	if err := os.MkdirAll(out, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(out, "adding-nodes.mp4"), "the last good clip")
	writeFile(t, filepath.Join(out, "adding-nodes.json"), "{}")
	var stdout, stderr bytes.Buffer
	code := Main(context.Background(), []string{"render", "--script", script, "--master", filepath.Join(dir, "raw.mov"),
		"--events", filepath.Join(dir, "missing.jsonl"), "--out", out}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("code %d, stderr %q", code, stderr.String())
	}
	if n := named(t, out, "adding-nodes"); !slices.Equal(n, []string{"adding-nodes.json", "adding-nodes.mp4"}) {
		t.Fatalf("--out holds %q, want the last good clip intact", n)
	}
}

func TestRenderUsage(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Main(context.Background(), []string{"render", "--script", "clip.toml", "--events", "e.jsonl", "--out", "out"}, &stdout, &stderr)
	if code != 2 || !strings.Contains(stderr.String(), "usage: plate clip render") || !strings.Contains(stderr.String(), "--master") {
		t.Fatalf("code %d, stderr %q", code, stderr.String())
	}
}

func TestMeasurementJSONShape(t *testing.T) {
	b, err := json.MarshalIndent(Measurement{Trim: Window{Start: 1, End: 2}, RemovedMS: 5, Measurement: video.Measurement{
		Name: "n", Width: 2, Height: 4, FPS: 60, Frames: 1, DurationMS: 16.7,
		Formats:   map[string]video.FormatResult{"mp4": {File: "n.mp4", Bytes: 3, Sharpness: 0.9}},
		Sharpness: 0.9, LoopSeam: 0.1, LoopHeadFrames: 9, LoopTailFrames: 8}}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	want := `{
  "name": "n",
  "width": 2,
  "height": 4,
  "fps": 60,
  "frames": 1,
  "duration_ms": 16.7,
  "trim": {
    "start_ms": 1,
    "end_ms": 2
  },
  "removed_ms": 5,
  "formats": {
    "mp4": {
      "file": "n.mp4",
      "bytes": 3,
      "sharpness": 0.9
    }
  },
  "sharpness": 0.9,
  "loop_seam": 0.1,
  "loop_head_frames": 9,
  "loop_tail_frames": 8
}`
	if string(b) != want {
		t.Fatalf("sidecar:\n%s\nwant:\n%s", b, want)
	}
}

// TestMeasurementJSONKeepsVideoFields guards MarshalJSON's field list: a
// field video.Measurement gains must reach clip's sidecar too.
func TestMeasurementJSONKeepsVideoFields(t *testing.T) {
	var m Measurement
	keys := func(v any) map[string]json.RawMessage {
		t.Helper()
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		var out map[string]json.RawMessage
		if err := json.Unmarshal(b, &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	clip := keys(m)
	for k := range keys(m.Measurement) {
		if _, ok := clip[k]; !ok {
			t.Errorf("video.Measurement's %q is missing from clip's sidecar", k)
		}
	}
}

func TestRenderRefusesTruncatedMaster(t *testing.T) {
	dir := t.TempDir()
	script := writeFile(t, filepath.Join(dir, "clip.toml"), contractExample)
	events := writeFile(t, filepath.Join(dir, "events.jsonl"), strings.Join(contractLog, "\n")+"\n")
	enginetest.Stub(t, "ffprobe", `echo '{"streams":[{"width":1206,"height":2622}],"frames":[{"pts_time":"0"}],"format":{"duration":"1.0"}}'`)
	ffmpeg := enginetest.Stub(t, "ffmpeg", `exit 99`)
	var stdout, stderr bytes.Buffer
	code := Main(context.Background(), []string{"render", "--script", script, "--master", filepath.Join(dir, "raw.mov"),
		"--events", events, "--out", filepath.Join(dir, "out")}, &stdout, &stderr)
	if code != 1 || !strings.Contains(stderr.String(), "before the first action at 1520 ms") {
		t.Fatalf("code %d, stderr %q", code, stderr.String())
	}
	if c := enginetest.Calls(t, ffmpeg); len(c) != 0 {
		t.Fatalf("ffmpeg ran: %q", c)
	}
}

func TestRenderWarnsMasterEndsBeforeLastAction(t *testing.T) {
	dir := t.TempDir()
	script := writeFile(t, filepath.Join(dir, "clip.toml"), contractExample)
	events := writeFile(t, filepath.Join(dir, "events.jsonl"), strings.Join(contractLog, "\n")+"\n")
	enginetest.Stub(t, "ffprobe", `echo '{"streams":[{"width":1206,"height":2622}],"frames":[{"pts_time":"0"}],"format":{"duration":"3.0"}}'`)
	enginetest.Stub(t, "ffmpeg", `exit 99`)
	var stdout, stderr bytes.Buffer
	Main(context.Background(), []string{"render", "--script", script, "--master", filepath.Join(dir, "raw.mov"),
		"--events", events, "--out", filepath.Join(dir, "out")}, &stdout, &stderr)
	want := "plate clip render: warning: the master's last frame is at 3000 ms, before the last action at 6400 ms; the screen holds still from there\n"
	if !strings.Contains(stderr.String(), want) {
		t.Fatalf("stderr %q lacks %q", stderr.String(), want)
	}
}
