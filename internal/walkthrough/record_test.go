package walkthrough

import (
	"bytes"
	"context"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lockyc/plate/internal/clip"
	"github.com/lockyc/plate/internal/engine"
)

// slowPage is how long the fixture app takes to answer /slow, past
// settleCap, so the step after a click to it starts mid-navigation.
const slowPage = settleCap + 700*time.Millisecond

// app serves the fixture app, or skips when Chrome is not installed.
func app(t *testing.T) string {
	t.Helper()
	if _, err := engine.Resolve("chrome-headless-shell"); err != nil {
		t.Skip(err)
	}
	files := http.FileServer(http.Dir("testdata/app"))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/slow" {
			time.Sleep(slowPage)
			r.URL.Path = "/detail.html"
		}
		files.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// script is a small-viewport script over steps, its defaults left for
// Record to fill.
func script(steps ...Step) *Script {
	return &Script{Version: 1, Name: "test", Width: 640, Height: 400, Steps: steps}
}

func timeout(ms int) *int { return &ms }

func record(t *testing.T, s *Script, warmup []string, durations []time.Duration) (*Recording, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	return Record(ctx, s, app(t), warmup, durations, t.TempDir())
}

func TestRecordStepSpans(t *testing.T) {
	s := script(
		Step{Action: "goto", URL: "/index.html", Caption: "The jobs list"},
		Step{Action: "click", Selector: "#first", Caption: "Open a job"},
		Step{Action: "goto", URL: "/form.html"},
		Step{Action: "type", Selector: "#name", Text: "Ada", Caption: "Name it"},
		Step{Action: "press", Key: "return"},
		Step{Action: "wait_for", Selector: "#saved", Caption: "Saved"},
	)
	durations := []time.Duration{300 * time.Millisecond, 0, 1200 * time.Millisecond, 0, 0, 200 * time.Millisecond}
	rec, err := record(t, s, nil, durations)
	if err != nil {
		t.Fatal(err)
	}
	if len(rec.Steps) != len(s.Steps) {
		t.Fatalf("%d spans for %d steps", len(rec.Steps), len(s.Steps))
	}
	for i, sp := range rec.Steps {
		want := max(float64((durations[i] + 400*time.Millisecond).Milliseconds()), 800)
		if got := sp.EndMS - sp.StartMS; got < want {
			t.Errorf("step %d lasts %.0f ms, want at least %.0f", i+1, got, want)
		}
		if i > 0 && sp.StartMS != rec.Steps[i-1].EndMS {
			t.Errorf("step %d starts at %.1f ms, step %d ended at %.1f", i+1, sp.StartMS, i, rec.Steps[i-1].EndMS)
		}
	}
	if rec.Steps[0].StartMS != 0 {
		t.Errorf("step 1 starts at %.1f ms, want 0", rec.Steps[0].StartMS)
	}
	if len(rec.Frames) == 0 {
		t.Fatal("no frames")
	}
	for i := 1; i < len(rec.Frames); i++ {
		if rec.Frames[i].MS < rec.Frames[i-1].MS {
			t.Fatalf("frame %d at %.1f ms precedes frame %d at %.1f", i, rec.Frames[i].MS, i-1, rec.Frames[i-1].MS)
		}
	}
}

func TestRecordLayersSurviveNavigation(t *testing.T) {
	s := script(
		Step{Action: "goto", URL: "/index.html", Caption: "The jobs list"},
		Step{Action: "click", Selector: "#first", Caption: "Open a job"},
	)
	var caption string
	var url string
	afterStep = func(ctx context.Context, r *recorder, i int) error {
		if i != 1 {
			return nil
		}
		if err := r.eval(ctx, `location.pathname`, &url); err != nil {
			return err
		}
		return r.eval(ctx, `(document.querySelector("[data-plate-caption]") || {}).textContent ?? null`, &caption)
	}
	t.Cleanup(func() { afterStep = nil })
	if _, err := record(t, s, nil, []time.Duration{0, 0}); err != nil {
		t.Fatal(err)
	}
	if url != "/detail.html" {
		t.Fatalf("after the click the page is %q, want /detail.html", url)
	}
	if caption != "Open a job" {
		t.Errorf("caption after navigating = %q, want %q", caption, "Open a job")
	}
}

func TestRecordRefusesAmbiguousSelector(t *testing.T) {
	s := script(
		Step{Action: "goto", URL: "/index.html"},
		Step{Action: "click", Selector: ".dup", TimeoutMS: timeout(500)},
	)
	_, err := record(t, s, nil, []time.Duration{0, 0})
	if err == nil || !strings.Contains(err.Error(), "step 2 (click .dup): 2 elements match") {
		t.Fatalf("err = %v, want step 2 refused for matching 2 elements", err)
	}
}

func TestRecordRefusesHiddenTarget(t *testing.T) {
	s := script(
		Step{Action: "goto", URL: "/index.html"},
		Step{Action: "click", Selector: "#hidden", TimeoutMS: timeout(500)},
	)
	_, err := record(t, s, nil, []time.Duration{0, 0})
	if err == nil || !strings.Contains(err.Error(), "step 2 (click #hidden): the element it matches is still hidden") {
		t.Fatalf("err = %v, want step 2 refused for a hidden target", err)
	}
}

func TestRecordRefusesMissingTarget(t *testing.T) {
	s := script(
		Step{Action: "goto", URL: "/index.html"},
		Step{Action: "wait_for", Selector: "#nope", TimeoutMS: timeout(500)},
	)
	_, err := record(t, s, nil, []time.Duration{0, 0})
	if err == nil || !strings.Contains(err.Error(), "step 2 (wait_for #nope): ") {
		t.Fatalf("err = %v, want step 2 refused", err)
	}
}

func TestRecordWarmupNotRecorded(t *testing.T) {
	s := script(Step{Action: "goto", URL: "/index.html", Caption: "The jobs list"})
	var cookie string
	afterStep = func(ctx context.Context, r *recorder, i int) error {
		return r.eval(ctx, `document.cookie`, &cookie)
	}
	t.Cleanup(func() { afterStep = nil })
	rec, err := record(t, s, []string{"/login.html"}, []time.Duration{0})
	if err != nil {
		t.Fatal(err)
	}
	if cookie != "session=1" {
		t.Errorf("cookie = %q: the warmup did not run in the recorded session", cookie)
	}
	if len(rec.Frames) == 0 {
		t.Fatal("no frames")
	}
	// login.html, the warmup, is the only page painted #ff00ff.
	for _, f := range rec.Frames {
		if showsColour(t, filepath.Join(rec.FramesDir, f.File), color.RGBA{0xff, 0x00, 0xff, 0xff}) {
			t.Fatalf("frame %s at %.1f ms shows the warmup page", f.File, f.MS)
		}
	}
}

// showsColour is whether any pixel on a 16 px grid of the PNG at path is c.
func showsColour(t *testing.T, path string, c color.RGBA) bool {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	img, err := png.Decode(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	bounds := img.Bounds()
	for y := bounds.Min.Y; y < bounds.Max.Y; y += 16 {
		for x := bounds.Min.X; x < bounds.Max.X; x += 16 {
			if color.RGBAModel.Convert(img.At(x, y)) == c {
				return true
			}
		}
	}
	return false
}

// The frames' clock (Chrome's) and the steps' clock (plate's) are one: a
// step's caption change is drawn within moments of the step's start, and
// no frame falls outside the steps.
func TestRecordFramesShareStepsClock(t *testing.T) {
	s := script(
		Step{Action: "goto", URL: "/index.html", Caption: "The jobs list"},
		Step{Action: "wait_for", Selector: "#first", Caption: "The first job"},
	)
	durations := []time.Duration{0, time.Second}
	rec, err := record(t, s, nil, durations)
	if err != nil {
		t.Fatal(err)
	}
	first, last := rec.Steps[0], rec.Steps[len(rec.Steps)-1]
	if len(rec.Frames) == 0 {
		t.Fatal("no frames")
	}
	if f := rec.Frames[0]; f.MS < first.StartMS {
		t.Errorf("first frame at %.1f ms precedes step 1 at %.1f", f.MS, first.StartMS)
	}
	if f := rec.Frames[len(rec.Frames)-1]; f.MS < last.StartMS || f.MS > last.EndMS {
		t.Errorf("last frame at %.1f ms is outside the last step, %.1f-%.1f", f.MS, last.StartMS, last.EndMS)
	}
	drawn := false
	for _, f := range rec.Frames {
		if f.MS >= last.StartMS && f.MS <= last.StartMS+500 {
			drawn = true
		}
	}
	if !drawn {
		t.Errorf("no frame within 500 ms of step 2's start at %.1f ms, when its caption was drawn", last.StartMS)
	}
	for i, sp := range rec.Steps {
		want := max(durations[i]+audioPad, minHold)
		if got := sp.EndMS - sp.StartMS; got > float64((want + 3*time.Second).Milliseconds()) {
			t.Errorf("step %d lasts %.0f ms against a %d ms hold: the clocks disagree", i+1, got, want.Milliseconds())
		}
	}
}

func TestRecordRefusesCoveredTarget(t *testing.T) {
	s := script(
		Step{Action: "goto", URL: "/modal.html"},
		Step{Action: "click", Selector: "#under", TimeoutMS: timeout(500)},
	)
	_, err := record(t, s, nil, []time.Duration{0, 0})
	if err == nil || !strings.Contains(err.Error(), "step 2 (click #under): the element it matches is still covered") {
		t.Fatalf("err = %v, want step 2 refused for a covered target", err)
	}
}

func TestRecordSameDocumentGoto(t *testing.T) {
	s := script(
		Step{Action: "goto", URL: "/index.html"},
		Step{Action: "goto", URL: "/index.html#first", TimeoutMS: timeout(1000)},
	)
	if _, err := record(t, s, nil, []time.Duration{0, 0}); err != nil {
		t.Fatal(err)
	}
}

// A step that starts while the last one's navigation is still loading
// captions the page that loads.
func TestRecordStepDuringSlowNavigation(t *testing.T) {
	s := script(
		Step{Action: "goto", URL: "/index.html"},
		Step{Action: "click", Selector: "#slow"},
		Step{Action: "wait_for", Selector: "#title", Caption: "The slow job"},
	)
	var caption string
	afterStep = func(ctx context.Context, r *recorder, i int) error {
		if i != 2 {
			return nil
		}
		return r.eval(ctx, `document.querySelector("[data-plate-caption]").textContent`, &caption)
	}
	t.Cleanup(func() { afterStep = nil })
	if _, err := record(t, s, nil, []time.Duration{0, 0, 0}); err != nil {
		t.Fatal(err)
	}
	if caption != "The slow job" {
		t.Errorf("caption = %q, want %q", caption, "The slow job")
	}
}

func TestKeyEventsCoverScriptKeys(t *testing.T) {
	for _, k := range clip.Keys {
		if _, ok := keyEvents[k]; !ok {
			t.Errorf("no key event for %q", k)
		}
	}
}

func TestRecordRefusesDurationCount(t *testing.T) {
	s := script(Step{Action: "goto", URL: "/index.html"})
	_, err := Record(context.Background(), s, "http://127.0.0.1:1", nil, nil, t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "durations") {
		t.Fatalf("err = %v, want a durations count refusal", err)
	}
}
