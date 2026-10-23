package animate

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lockyc/plate/internal/engine"
	"github.com/lockyc/plate/internal/enginetest"
	"github.com/lockyc/plate/internal/render"
	"github.com/lockyc/plate/internal/video"
)

func opts(t *testing.T, page string) Options {
	return Options{Page: page, W: 600, H: 300, Scale: 1, DurationS: 1, FPS: 30, Formats: []string{"mp4"},
		Loop: "none", Budget: render.DefaultBudget, Out: t.TempDir()}
}

func TestValidate(t *testing.T) {
	for _, c := range []struct {
		name string
		edit func(*Options)
		want string // "" is valid
	}{
		{"defaults", func(*Options) {}, ""},
		{"odd mp4", func(o *Options) { o.W = 541 }, "even"},
		{"odd webm", func(o *Options) { o.H, o.Formats = 961, []string{"webm"} }, "even"},
		{"odd gif", func(o *Options) { o.W, o.Formats = 541, []string{"gif"} }, ""},
		{"odd at scale 2", func(o *Options) { o.H, o.Scale = 961, 2 }, ""},
		{"fps 0", func(o *Options) { o.FPS = 0 }, "--fps"},
		{"fps 121", func(o *Options) { o.FPS = 121 }, "--fps"},
		{"fps 120", func(o *Options) { o.FPS = 120 }, ""},
		{"one frame", func(o *Options) { o.DurationS = 0.04 }, "--duration"},
		{"no duration", func(o *Options) { o.DurationS = 0 }, "--duration"},
		{"loop bounce", func(o *Options) { o.Loop = "bounce" }, "--loop"},
		{"loop dip", func(o *Options) { o.Loop = "dip" }, ""},
		{"format mov", func(o *Options) { o.Formats = []string{"mp4", "mov"} }, "--format"},
		{"no format", func(o *Options) { o.Formats = nil }, "--format"},
		{"twice", func(o *Options) { o.Formats = []string{"gif", "gif"} }, "--format"},
		{"no out", func(o *Options) { o.Out = "" }, "--out"},
		{"too big", func(o *Options) { o.W, o.H = 20000, 20000 }, "verified whole"},
		{"name with slash", func(o *Options) { o.Name = "a/b" }, "--name"},
		{"page without a name", func(o *Options) { o.Page = "https://example.com/" }, "the page gives no file name; pass --name"},
		{"named page URL", func(o *Options) { o.Page, o.Name = "https://example.com/", "ad" }, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			o := opts(t, "testdata/css.html")
			c.edit(&o)
			err := o.Validate()
			switch {
			case c.want == "" && err != nil:
				t.Errorf("Validate() = %v, want nil", err)
			case c.want != "" && (err == nil || !strings.Contains(err.Error(), c.want)):
				t.Errorf("Validate() = %v, want it to mention %q", err, c.want)
			}
		})
	}
}

func TestOutputs(t *testing.T) {
	o := opts(t, "testdata/css.html")
	o.Formats = []string{"poster", "mp4"}
	o.PNG = "still.png"
	got := o.Outputs()
	abs, _ := filepath.Abs("still.png")
	want := []string{filepath.Join(o.Out, "css.mp4"), filepath.Join(o.Out, "css.poster.png"), filepath.Join(o.Out, "css.json"), abs}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("Outputs() = %q, want %q", got, want)
	}
}

func TestRunRefusesOddSidesBeforeCapture(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "chrome-ran")
	enginetest.Stub(t, "chrome-headless-shell", "touch '"+marker+"'")
	o := opts(t, "testdata/css.html")
	o.W, o.H = 541, 960
	if _, _, err := Run(context.Background(), o, io.Discard); err == nil || !strings.Contains(err.Error(), "even") {
		t.Fatalf("Run = %v, want an even-sides refusal", err)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Error("Chrome started before the odd sides were refused")
	}
}

// needEngines skips a test without Chrome or ffmpeg and gives it a private
// render cache.
func needEngines(t *testing.T) {
	t.Helper()
	for _, e := range []string{"chrome-headless-shell", "ffmpeg"} {
		if _, err := engine.Resolve(e); err != nil {
			t.Skip(err)
		}
	}
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
}

func run(t *testing.T, o Options) (render.Result, video.Measurement, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	return Run(ctx, o, io.Discard)
}

func TestRunWritesSet(t *testing.T) {
	needEngines(t)
	o := opts(t, "testdata/css.html")
	o.Formats = []string{"mp4", "poster"}
	o.PNG = filepath.Join(t.TempDir(), "still.png")
	res, m, err := run(t, o)
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != render.Rendered {
		t.Errorf("outcome %v, want Rendered", res.Outcome)
	}
	for _, f := range []string{"css.mp4", "css.poster.png", "css.json"} {
		if _, err := os.Stat(filepath.Join(o.Out, f)); err != nil {
			t.Error(err)
		}
	}
	b, err := os.ReadFile(filepath.Join(o.Out, "css.json"))
	if err != nil {
		t.Fatal(err)
	}
	var side video.Measurement
	if err := json.Unmarshal(b, &side); err != nil {
		t.Fatal(err)
	}
	if side.Frames != 30 || side.FPS != 30 || side.Width != 600 || side.Height != 300 {
		t.Errorf("sidecar %d frames at %d fps, %dx%d; want 30 at 30, 600x300", side.Frames, side.FPS, side.Width, side.Height)
	}
	if side.Sharpness < video.MinSharpness || side.Sharpness != m.Sharpness {
		t.Errorf("sharpness %v (returned %v), want at least %v", side.Sharpness, m.Sharpness, video.MinSharpness)
	}

	// The poster is the last frame, as a capture of that frame alone shows it.
	last := filepath.Join(t.TempDir(), "last.png")
	if err := withCapture(t, "testdata/css.html", 600, 300, testBudget, func(ctx context.Context, c *Capture) error {
		if err := c.Seek(ctx, at(29)); err != nil {
			return err
		}
		return c.Frame(ctx, last)
	}); err != nil {
		t.Fatal(err)
	}
	want, _ := os.ReadFile(last)
	poster, _ := os.ReadFile(filepath.Join(o.Out, "css.poster.png"))
	still, _ := os.ReadFile(o.PNG)
	if len(want) == 0 || !bytes.Equal(poster, want) {
		t.Error("the poster is not the last frame")
	}
	if !bytes.Equal(still, poster) {
		t.Error("the PNG is not the poster's bytes")
	}
}

func TestRunRefusesStill(t *testing.T) {
	needEngines(t)
	o := opts(t, "testdata/still.html")
	_, _, err := run(t, o)
	if err == nil || !strings.Contains(err.Error(), "never ran on plate's clock") {
		t.Fatalf("Run = %v, want a still-page refusal", err)
	}
	if left, _ := os.ReadDir(o.Out); len(left) > 0 {
		t.Errorf("a refused capture left %v", left)
	}
}

func TestRunRefusesBlank(t *testing.T) {
	needEngines(t)
	_, _, err := run(t, opts(t, "testdata/blank.html"))
	if err == nil || !strings.Contains(err.Error(), "every frame is one colour") {
		t.Fatalf("Run = %v, want a blank-page refusal", err)
	}
}

func TestRunFailIf(t *testing.T) {
	needEngines(t)
	o := opts(t, "testdata/css.html")
	o.FailIf = []string{`id="b"`}
	if _, _, err := run(t, o); err == nil || !strings.Contains(err.Error(), `the rendered page contains "id=\"b\""`) {
		t.Fatalf("Run = %v, want a --fail-if refusal", err)
	}
}

func TestRunUnchanged(t *testing.T) {
	needEngines(t)
	dir := t.TempDir()
	page, css := filepath.Join(dir, "ad.html"), filepath.Join(dir, "ad.css")
	os.WriteFile(page, []byte(`<!doctype html><link rel="stylesheet" href="ad.css"><div id="b"></div>`), 0o644)
	style := "body { margin: 0; background: #fff; }\n" +
		"#b { width: 40px; height: 40px; background: #c33; animation: slide 1s linear forwards; }\n" +
		"@keyframes slide { to { transform: translateX(500px); } }\n"
	os.WriteFile(css, []byte(style), 0o644)
	o := opts(t, page)
	o.Formats = []string{"poster", "gif"}
	o.PNG, o.JPEG = filepath.Join(dir, "still.png"), filepath.Join(dir, "still.jpg")
	for i, want := range []render.Outcome{render.Rendered, render.Unchanged} {
		res, _, err := run(t, o)
		if err != nil {
			t.Fatal(err)
		}
		if res.Outcome != want {
			t.Fatalf("run %d: outcome %v (%s), want %v", i+1, res.Outcome, res.Reason, want)
		}
	}
	os.WriteFile(css, []byte(style+"#b { background: #33c; }\n"), 0o644)
	if res, _, err := run(t, o); err != nil || res.Outcome != render.Rendered {
		t.Fatalf("after the CSS changed: outcome %v, err %v; want Rendered", res.Outcome, err)
	}
}

func TestRunRefusesFrameThatNeverFinishes(t *testing.T) {
	needEngines(t)
	o := opts(t, "testdata/spin.html")
	o.Budget = 500
	start := time.Now()
	_, _, err := run(t, o)
	if err == nil || !strings.Contains(err.Error(), "frame 15 did not finish within") {
		t.Fatalf("Run = %v, want frame 15 refused", err)
	}
	if d := time.Since(start); d > time.Minute {
		t.Errorf("the refusal took %v", d)
	}
}

func TestRunRefusesPageThatNeverOpens(t *testing.T) {
	needEngines(t)
	o := opts(t, "testdata/busy.html")
	o.Budget = 500
	start := time.Now()
	_, _, err := run(t, o)
	if err == nil || !strings.Contains(err.Error(), "page did not open within 5500 ms") {
		t.Fatalf("Run = %v, want the open refused", err)
	}
	if d := time.Since(start); d > time.Minute {
		t.Errorf("the refusal took %v", d)
	}
}

func TestMainUnknownFlagIsUsage(t *testing.T) {
	var stderr bytes.Buffer
	if code := Main(context.Background(), []string{"--bogus"}, io.Discard, &stderr); code != 2 {
		t.Errorf("exit %d, want 2", code)
	}
}

func TestMainRequiresSize(t *testing.T) {
	var stderr bytes.Buffer
	code := Main(context.Background(), []string{"--duration", "1", "--out", t.TempDir(), "testdata/css.html"}, io.Discard, &stderr)
	if code != 2 || !strings.Contains(stderr.String(), "--size") {
		t.Errorf("exit %d, stderr %q; want 2 naming --size", code, stderr.String())
	}
}
