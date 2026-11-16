package walkthrough

import (
	"context"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/lockyc/plate/internal/engine"
)

// renderFixture writes walk.toml (with body after its header) and a 1 s
// 440 Hz tone, tone.wav, into a temp dir, renders it against the fixture app
// into out, and returns the measurement, the out dir and the error.
func renderFixture(t *testing.T, body string) (Measurement, string, error) {
	t.Helper()
	base := app(t)
	if _, err := engine.Resolve("ffmpeg"); err != nil {
		t.Skip(err)
	}
	dir := t.TempDir()
	tone := filepath.Join(dir, "tone.wav")
	if _, err := engine.Run(context.Background(), engine.Cmd{Engine: "ffmpeg", Outputs: []string{tone},
		Args: []string{"-hide_banner", "-loglevel", "error", "-nostdin", "-f", "lavfi", "-i", "sine=frequency=440:duration=1", tone}}); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(dir, "walk.toml")
	head := "version = 1\nname = \"walk\"\nwidth = 640\nheight = 400\n\n"
	if err := os.WriteFile(script, []byte(head+body), 0o644); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "out")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	m, err := Render(ctx, RenderOptions{Script: script, BaseURL: base, Out: out})
	return m, out, err
}

func probeStreams(t *testing.T, file string) []map[string]string {
	t.Helper()
	res, err := engine.Run(context.Background(), engine.Cmd{Engine: "ffprobe", Inputs: []string{file}, Args: []string{"-v", "error", "-count_frames",
		"-show_entries", "stream=codec_type,codec_name,nb_read_frames", "-of", "json", file}})
	if err != nil {
		t.Fatal(err)
	}
	var out struct{ Streams []map[string]string }
	if err := json.Unmarshal(res.Stdout, &out); err != nil {
		t.Fatal(err)
	}
	return out.Streams
}

func stream(streams []map[string]string, typ string) map[string]string {
	for _, s := range streams {
		if s["codec_type"] == typ {
			return s
		}
	}
	return nil
}

func TestRenderAudioAligned(t *testing.T) {
	m, out, err := renderFixture(t, `
[[step]]
action = "goto"
url    = "/index.html"
audio  = "tone.wav"

[[step]]
action   = "wait_for"
selector = "#first"
caption  = "The first job"
audio    = "tone.wav"
`)
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(out, "walk.mp4")
	res, err := engine.Run(context.Background(), engine.Cmd{Engine: "ffmpeg", Inputs: []string{file},
		Args: []string{"-hide_banner", "-nostats", "-nostdin", "-i", file, "-af", "silencedetect=noise=-40dB:d=0.1", "-f", "null", "-"}})
	if err != nil {
		t.Fatal(err)
	}
	var onsets []float64
	for _, mm := range regexp.MustCompile(`silence_end: ([0-9.]+)`).FindAllStringSubmatch(string(res.Stderr), -1) {
		v, _ := strconv.ParseFloat(mm[1], 64)
		onsets = append(onsets, v*1000)
	}
	if len(onsets) < len(m.Steps) {
		t.Fatalf("found %d tone onsets for %d steps:\n%s", len(onsets), len(m.Steps), res.Stderr)
	}
	frame := 1000 / float64(m.FPS)
	for i, sp := range m.Steps {
		want := sp.StartMS + float64(audioDelay.Milliseconds())
		if d := math.Abs(onsets[i] - want); d > frame {
			t.Errorf("step %d's audio starts at %.1f ms, want %.1f (off by %.1f, more than a frame)", i+1, onsets[i], want, d)
		}
	}
}

func TestRenderConstantFPS(t *testing.T) {
	m, out, err := renderFixture(t, `
[[step]]
action = "goto"
url    = "/index.html"
audio  = "tone.wav"

[[step]]
action   = "click"
selector = "#first"
`)
	if err != nil {
		t.Fatal(err)
	}
	total := m.Steps[len(m.Steps)-1].EndMS
	want := int(math.Round(total * float64(m.FPS) / 1000))
	if m.Frames != want {
		t.Errorf("sidecar frames = %d, want round(%.1f ms × %d fps) = %d", m.Frames, total, m.FPS, want)
	}
	v := stream(probeStreams(t, filepath.Join(out, "walk.mp4")), "video")
	if v == nil || v["nb_read_frames"] != strconv.Itoa(want) {
		t.Errorf("mp4 video stream %v, want %d frames", v, want)
	}
	var side Measurement
	b, err := os.ReadFile(filepath.Join(out, "walk.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &side); err != nil {
		t.Fatal(err)
	}
	if len(side.Steps) != 2 || side.Frames != want || !strings.Contains(string(b), `"start_ms"`) {
		t.Errorf("sidecar = %s", b)
	}
	if _, err := os.Stat(filepath.Join(out, "walk.poster.png")); err != nil {
		t.Error(err)
	}
}

func TestRenderNoAudioSteps(t *testing.T) {
	_, out, err := renderFixture(t, `
[[step]]
action = "goto"
url    = "/index.html"
caption = "The jobs list"
`)
	if err != nil {
		t.Fatal(err)
	}
	streams := probeStreams(t, filepath.Join(out, "walk.mp4"))
	if a := stream(streams, "audio"); a == nil || a["codec_name"] != "aac" {
		t.Errorf("streams %v, want a silent AAC track", streams)
	}
}

// staleRun writes walk.toml (body after its header) into a temp dir, and an
// earlier run's walk outputs into its out dir, which it returns with the
// script's path.
func staleRun(t *testing.T, body string) (script, out string) {
	t.Helper()
	if _, err := engine.Resolve("ffmpeg"); err != nil {
		t.Skip(err)
	}
	dir := t.TempDir()
	out = filepath.Join(dir, "out")
	if err := os.MkdirAll(out, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"walk.mp4", "walk.poster.png", "walk.json"} {
		if err := os.WriteFile(filepath.Join(out, n), []byte("stale"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	script = filepath.Join(dir, "walk.toml")
	head := "version = 1\nname = \"walk\"\nwidth = 640\nheight = 400\n\n"
	if err := os.WriteFile(script, []byte(head+body), 0o644); err != nil {
		t.Fatal(err)
	}
	return script, out
}

// An earlier run's outputs must not survive a failed render to look current.
func assertEmpty(t *testing.T, out string) {
	t.Helper()
	entries, _ := os.ReadDir(out)
	for _, e := range entries {
		t.Errorf("%s left in --out after a failed render", e.Name())
	}
}

func TestRenderFailureLeavesNoOutputs(t *testing.T) {
	script, out := staleRun(t, "[[step]]\naction = \"goto\"\nurl = \"/index.html\"\n\n"+
		"[[step]]\naction = \"wait_for\"\nselector = \"#nope\"\ntimeout_ms = 500\n")
	_, err := Render(context.Background(), RenderOptions{Script: script, BaseURL: app(t), Out: out})
	if err == nil || !strings.Contains(err.Error(), "step 2 (wait_for #nope)") {
		t.Fatalf("err = %v, want step 2 refused", err)
	}
	assertEmpty(t, out)
}

func TestRenderUnprobeableAudioLeavesNoOutputs(t *testing.T) {
	script, out := staleRun(t, "[[step]]\naction = \"goto\"\nurl = \"/index.html\"\naudio = \"noise.wav\"\n")
	if err := os.WriteFile(filepath.Join(filepath.Dir(script), "noise.wav"), []byte("not audio"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Probing fails before Chrome starts, so no app is needed.
	_, err := Render(context.Background(), RenderOptions{Script: script, BaseURL: "http://127.0.0.1:1", Out: out})
	if err == nil || !strings.Contains(err.Error(), "step 1 audio") {
		t.Fatalf("err = %v, want step 1's audio refused", err)
	}
	assertEmpty(t, out)
}

func TestResampleHoldsLatestFrame(t *testing.T) {
	frames := []FrameStamp{{"a", 0}, {"b", 40}, {"c", 41}, {"d", 250}}
	got := resample(frames, 300, 10) // ticks at 0, 100, 200
	want := []string{"a", "c", "c"}
	if len(got) != len(want) {
		t.Fatalf("%d ticks, want %d", len(got), len(want))
	}
	for i, f := range got {
		if f != want[i] {
			t.Errorf("tick %d shows %s, want %s", i, f, want[i])
		}
	}
}
