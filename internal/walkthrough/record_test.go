package walkthrough

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/lockyc/plate/internal/clip"
	"github.com/lockyc/plate/internal/engine"
)

// app serves the fixture app, or skips when Chrome is not installed.
func app(t *testing.T) string {
	t.Helper()
	if _, err := engine.Resolve("chrome-headless-shell"); err != nil {
		t.Skip(err)
	}
	srv := httptest.NewServer(http.FileServer(http.Dir("testdata/app")))
	t.Cleanup(srv.Close)
	return srv.URL
}

// script is a small-viewport script over steps, every default filled.
func script(steps ...Step) *Script {
	s := &Script{Version: 1, Name: "test", Width: 640, Height: 400, Steps: steps}
	s.fillDefaults()
	return s
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
	s := script(Step{Action: "click", Selector: ".dup", TimeoutMS: timeout(500)})
	_, err := record(t, s, []string{"/index.html"}, []time.Duration{0})
	if err == nil || !strings.Contains(err.Error(), "step 1 (click .dup): ") || !strings.Contains(err.Error(), "2 elements") {
		t.Fatalf("err = %v, want step 1 refused for matching 2 elements", err)
	}
}

func TestRecordRefusesHiddenTarget(t *testing.T) {
	s := script(Step{Action: "click", Selector: "#hidden", TimeoutMS: timeout(500)})
	_, err := record(t, s, []string{"/index.html"}, []time.Duration{0})
	if err == nil || !strings.Contains(err.Error(), "step 1 (click #hidden): ") || !strings.Contains(err.Error(), "hidden") {
		t.Fatalf("err = %v, want step 1 refused for a hidden target", err)
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
	for _, f := range rec.Frames {
		if f.MS < rec.Steps[0].StartMS {
			t.Fatalf("frame %s at %.1f ms precedes step 1 at %.1f", f.File, f.MS, rec.Steps[0].StartMS)
		}
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
