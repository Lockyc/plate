package animate

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lockyc/plate/internal/engine"
	"github.com/lockyc/plate/internal/raster"
	"github.com/lockyc/plate/internal/render"
	"github.com/lockyc/plate/internal/video"
)

const testBudget = 5 * time.Second

// at is frame n's time at 30 fps, in ms.
func at(n int) float64 { return float64(n) * 1000 / 30 }

// withCapture opens page at w×h through render's proxy and runs fn on it.
func withCapture(t *testing.T, page string, w, h int, budget time.Duration, fn func(ctx context.Context, c *Capture) error) error {
	t.Helper()
	if _, err := engine.Resolve("chrome-headless-shell"); err != nil {
		t.Skip(err)
	}
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	_, err := render.Cached(ctx, "animate-test", page, page, nil, true, io.Discard,
		func(ctx context.Context, l render.Load) ([]render.FileHash, error) {
			c, err := Open(ctx, l, w, h, 1, budget, 0)
			if err != nil {
				return nil, err
			}
			defer c.Close()
			return nil, fn(ctx, c)
		})
	return err
}

// sample seeks frames 0..last in order and calls probe at each frame in want.
func sample(ctx context.Context, c *Capture, last int, want map[int]bool, probe func(n int) error) error {
	for n := 0; n <= last; n++ {
		if err := c.Seek(ctx, at(n)); err != nil {
			return err
		}
		if want[n] {
			if err := probe(n); err != nil {
				return err
			}
		}
	}
	return nil
}

func near(a, b float64) bool { return math.Abs(a-b) < 1e-6 }

func TestClockCSSAnimation(t *testing.T) {
	want := map[int]float64{0: 0, 15: 125, 30: 250, 60: 500}
	frames := map[int]bool{0: true, 15: true, 30: true, 60: true}
	err := withCapture(t, "testdata/css.html", 600, 100, testBudget, func(ctx context.Context, c *Capture) error {
		return sample(ctx, c, 60, frames, func(n int) error {
			var x float64
			if err := c.Eval(ctx, `new DOMMatrix(getComputedStyle(document.getElementById("b")).transform).m41`, &x); err != nil {
				return err
			}
			if !near(x, want[n]) {
				t.Errorf("frame %d: x = %v, want %v", n, x, want[n])
			}
			return nil
		})
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestClockRAF(t *testing.T) {
	want := map[int]float64{0: 0, 15: 125, 30: 250, 60: 500}
	frames := map[int]bool{0: true, 15: true, 30: true, 60: true}
	err := withCapture(t, "testdata/raf.html", 600, 100, testBudget, func(ctx context.Context, c *Capture) error {
		return sample(ctx, c, 60, frames, func(n int) error {
			var left float64
			if err := c.Eval(ctx, `parseFloat(document.getElementById("j").style.left)`, &left); err != nil {
				return err
			}
			if !near(left, want[n]) {
				t.Errorf("frame %d: left = %v, want %v", n, left, want[n])
			}
			return nil
		})
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestClockTimers(t *testing.T) {
	err := withCapture(t, "testdata/timers.html", 100, 100, testBudget, func(ctx context.Context, c *Capture) error {
		return sample(ctx, c, 30, map[int]bool{30: true}, func(int) error {
			var got struct {
				Flag  bool
				Count int
			}
			if err := c.Eval(ctx, `({Flag: window.flag, Count: window.count})`, &got); err != nil {
				return err
			}
			if !got.Flag || got.Count != 10 {
				t.Errorf("at 1000 ms: flag %v, count %d; want true, 10", got.Flag, got.Count)
			}
			return nil
		})
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestClockLateTransition(t *testing.T) {
	want := map[int]float64{15: 0, 30: 0.5, 45: 1}
	frames := map[int]bool{15: true, 30: true, 45: true}
	err := withCapture(t, "testdata/transition.html", 100, 100, testBudget, func(ctx context.Context, c *Capture) error {
		return sample(ctx, c, 45, frames, func(n int) error {
			var o float64
			if err := c.Eval(ctx, `parseFloat(getComputedStyle(document.getElementById("t")).opacity)`, &o); err != nil {
				return err
			}
			if math.Abs(o-want[n]) > 1e-3 {
				t.Errorf("at %.0f ms: opacity = %v, want %v", at(n), o, want[n])
			}
			return nil
		})
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestClockSVG(t *testing.T) {
	err := withCapture(t, "testdata/svg.html", 300, 100, testBudget, func(ctx context.Context, c *Capture) error {
		return sample(ctx, c, 30, map[int]bool{30: true}, func(int) error {
			var x float64
			if err := c.Eval(ctx, `document.getElementById("r").x.animVal.value`, &x); err != nil {
				return err
			}
			if math.Abs(x-100) > 1e-3 {
				t.Errorf("at 1000 ms: x = %v, want 100", x)
			}
			return nil
		})
	})
	if err != nil {
		t.Fatal(err)
	}
}

// sourceVideo writes testdata's page and a 3 s 30 fps 320×240 testsrc H.264
// source.mp4 beside it, returning the copied page.
func sourceVideo(t *testing.T, name string) string {
	t.Helper()
	if _, err := engine.Resolve("ffmpeg"); err != nil {
		t.Skip(err)
	}
	if err := video.RequireEncoders(context.Background(), []string{"mp4"}); err != nil {
		t.Skip(err)
	}
	dir := t.TempDir()
	page, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), page, 0o644); err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(dir, "source.mp4")
	if _, err := engine.Run(context.Background(), engine.Cmd{Engine: "ffmpeg", Args: []string{"-v", "error",
		"-f", "lavfi", "-i", "testsrc=size=320x240:rate=30", "-t", "3",
		"-c:v", "libx264", "-pix_fmt", "yuv420p", src}, Outputs: []string{src}}); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(dir, name)
}

// sourceFrame is frame n of src, decoded by ffmpeg.
func sourceFrame(t *testing.T, src string, n int) *image.RGBA64 {
	t.Helper()
	out := filepath.Join(t.TempDir(), fmt.Sprintf("f%d.png", n))
	if _, err := engine.Run(context.Background(), engine.Cmd{Engine: "ffmpeg", Args: []string{"-v", "error",
		"-i", src, "-vf", fmt.Sprintf(`select=eq(n\,%d)`, n), "-frames:v", "1", out}, Outputs: []string{out}}); err != nil {
		t.Fatal(err)
	}
	img, err := raster.Load(out)
	if err != nil {
		t.Fatal(err)
	}
	return img
}

func TestClockVideoLateOffsetLoop(t *testing.T) {
	page := sourceVideo(t, "video.html")
	want := map[int]float64{30: 0.5, 60: 1.5, 120: 0.5}
	frames := map[int]bool{30: true, 60: true, 120: true}
	shot := filepath.Join(t.TempDir(), "f60.png")
	err := withCapture(t, page, 320, 240, testBudget, func(ctx context.Context, c *Capture) error {
		return sample(ctx, c, 120, frames, func(n int) error {
			var ct float64
			if err := c.Eval(ctx, `document.getElementById("v").currentTime`, &ct); err != nil {
				return err
			}
			if math.Abs(ct-want[n]) > 1e-6 {
				t.Errorf("at %.0f ms: currentTime = %v, want %v", at(n), ct, want[n])
			}
			if n == 60 {
				return c.Frame(ctx, shot)
			}
			return nil
		})
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := raster.Load(shot)
	if err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(filepath.Dir(page), "source.mp4")
	best, bestN := math.Inf(1), -1
	for _, n := range []int{44, 45, 46} {
		ref := sourceFrame(t, src, n)
		d, err := raster.RMSE(got, ref, ref.Bounds(), raster.RGB)
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("source frame %d: RMSE %.4f", n, d)
		if d < best {
			best, bestN = d, n
		}
	}
	if bestN != 45 {
		t.Errorf("page frame at 2000 ms matches source frame %d, want 45", bestN)
	}
}

func TestCaptureDeterministic(t *testing.T) {
	run := func() [][]byte {
		dir := t.TempDir()
		var out [][]byte
		err := withCapture(t, "testdata/css.html", 600, 100, testBudget, func(ctx context.Context, c *Capture) error {
			for n := 0; n <= 30; n++ {
				if err := c.Seek(ctx, at(n)); err != nil {
					return err
				}
				p := filepath.Join(dir, fmt.Sprintf("f%02d.png", n))
				if err := c.Frame(ctx, p); err != nil {
					return err
				}
				b, err := os.ReadFile(p)
				if err != nil {
					return err
				}
				out = append(out, b)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	a, b := run(), run()
	for n := range a {
		if !bytes.Equal(a[n], b[n]) {
			t.Errorf("frame %d differs between runs", n)
		}
	}
	if bytes.Equal(a[0], a[30]) {
		t.Error("frames 0 and 30 are identical: the animation never moved")
	}
}

func TestCaptureRefusesThrownError(t *testing.T) {
	err := withCapture(t, "testdata/throws.html", 100, 100, testBudget, func(ctx context.Context, c *Capture) error {
		for _, ms := range []float64{0, 250} {
			if err := c.Seek(ctx, ms); err != nil {
				t.Fatalf("seek %v: %v", ms, err)
			}
		}
		return c.Seek(ctx, 500)
	})
	if err == nil || !strings.Contains(err.Error(), "page threw at 500 ms") || !strings.Contains(err.Error(), "scene broke") {
		t.Fatalf("err = %v", err)
	}
}

func TestCaptureRefusesUnplayableVideo(t *testing.T) {
	err := withCapture(t, "testdata/badvideo.html", 100, 100, testBudget, func(ctx context.Context, c *Capture) error {
		return c.Seek(ctx, 0)
	})
	if err == nil || !strings.Contains(err.Error(), "cannot play") || !strings.Contains(err.Error(), "missing.mp4") {
		t.Fatalf("err = %v", err)
	}
}

func TestCaptureRefusesUnsettledPage(t *testing.T) {
	hang := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	t.Cleanup(hang.Close)
	abs, err := filepath.Abs("testdata/unsettled.html")
	if err != nil {
		t.Fatal(err)
	}
	page := (&url.URL{Scheme: "file", Path: abs, RawQuery: url.Values{"img": {hang.URL + "/never"}}.Encode()}).String()
	err = withCapture(t, page, 100, 100, 300*time.Millisecond, func(context.Context, *Capture) error {
		t.Fatal("Open settled a page whose image never loads")
		return nil
	})
	if err == nil || !strings.Contains(err.Error(), "not settled after 300 ms") || !strings.Contains(err.Error(), "/never") {
		t.Fatalf("err = %v", err)
	}
}

// hangServer answers nothing until the client goes away.
func hangServer(t *testing.T) string {
	t.Helper()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	t.Cleanup(s.Close)
	return s.URL
}

// withQuery is the file URL of page with query.
func withQuery(t *testing.T, page string, query url.Values) string {
	t.Helper()
	abs, err := filepath.Abs(page)
	if err != nil {
		t.Fatal(err)
	}
	return (&url.URL{Scheme: "file", Path: abs, RawQuery: query.Encode()}).String()
}

// evalAt seeks frames 0..last and evaluates expr at each frame in want.
func evalAt[T any](t *testing.T, c *Capture, ctx context.Context, last int, expr string, check map[int]func(T)) error {
	t.Helper()
	for n := 0; n <= last; n++ {
		if err := c.Seek(ctx, at(n)); err != nil {
			return err
		}
		if f, ok := check[n]; ok {
			var v T
			if err := c.Eval(ctx, expr, &v); err != nil {
				return err
			}
			f(v)
		}
	}
	return nil
}

func wantNear(t *testing.T, what string, got, want, tol float64) {
	t.Helper()
	if math.Abs(got-want) > tol {
		t.Errorf("%s = %v, want %v", what, got, want)
	}
}

func TestClockCSSPlayState(t *testing.T) {
	err := withCapture(t, "testdata/csspaused.html", 600, 100, testBudget, func(ctx context.Context, c *Capture) error {
		x := func(want float64) func(float64) {
			return func(v float64) { wantNear(t, "x", v, want, 1e-6) }
		}
		return evalAt(t, c, ctx, 45,
			`new DOMMatrix(getComputedStyle(document.getElementById("b")).transform).m41`,
			map[int]func(float64){15: x(0), 30: x(0), 45: x(125)})
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestClockAnimationFinishes(t *testing.T) {
	type state struct {
		Done   bool
		Events int
		X      float64
	}
	err := withCapture(t, "testdata/waapi.html", 200, 100, testBudget, func(ctx context.Context, c *Capture) error {
		return evalAt(t, c, ctx, 30,
			`({Done: window.done, Events: window.finishEvents, X: new DOMMatrix(getComputedStyle(document.getElementById("b")).transform).m41})`,
			map[int]func(state){
				14: func(s state) {
					if s.Done || s.Events != 0 {
						t.Errorf("at %.0f ms: %+v, want not finished", at(14), s)
					}
				},
				15: func(s state) {
					if !s.Done || s.Events != 1 {
						t.Errorf("at 500 ms: %+v, want finished once", s)
					}
					wantNear(t, "chained x at 500 ms", s.X, 0, 1e-6)
				},
				30: func(s state) { wantNear(t, "chained x at 1000 ms", s.X, 50, 1e-6) },
			})
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestClockVideoEndedStartsHeldAnimation(t *testing.T) {
	page := sourceVideo(t, "videoended.html")
	type anim struct {
		N     int
		Time  float64
		State string
	}
	err := withCapture(t, page, 320, 300, testBudget, func(ctx context.Context, c *Capture) error {
		return evalAt(t, c, ctx, 100,
			`(() => { const a = document.getAnimations(); return {N: a.length, Time: a[0]?.currentTime ?? -1, State: a[0]?.playState ?? ""}; })()`,
			map[int]func(anim){
				89: func(a anim) {
					if a.N != 0 {
						t.Errorf("before the end: %+v, want no animation", a)
					}
				},
				90: func(a anim) {
					if a.N != 1 || a.Time != 0 || a.State != "paused" {
						t.Errorf("at 3000 ms: %+v, want one animation held at 0", a)
					}
				},
				100: func(a anim) { wantNear(t, "animation time at 3333 ms", a.Time, at(100)-3000, 1e-6) },
			})
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestClockAutoplayKeepsPageTime(t *testing.T) {
	page := sourceVideo(t, "videoautoplay.html")
	err := withCapture(t, page, 320, 240, testBudget, func(ctx context.Context, c *Capture) error {
		ct := func(want float64) func(float64) {
			return func(v float64) { wantNear(t, "currentTime", v, want, 1e-6) }
		}
		return evalAt(t, c, ctx, 30, `document.getElementById("v").currentTime`,
			map[int]func(float64){0: ct(1), 30: ct(2)})
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestClockVideoNewSourceResets(t *testing.T) {
	page := sourceVideo(t, "videosrc.html")
	type st struct {
		Time   float64
		Paused bool
	}
	err := withCapture(t, page, 320, 240, testBudget, func(ctx context.Context, c *Capture) error {
		check := func(want st) func(st) {
			return func(s st) {
				if math.Abs(s.Time-want.Time) > 1e-6 || s.Paused != want.Paused {
					t.Errorf("got %+v, want %+v", s, want)
				}
			}
		}
		return evalAt(t, c, ctx, 45,
			`(() => { const v = document.getElementById("v"); return {Time: v.currentTime, Paused: v.paused}; })()`,
			map[int]func(st){29: check(st{at(29) / 1000, false}), 30: check(st{0, true}), 45: check(st{0, true})})
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestClockVideoAddedWaitsForFrame(t *testing.T) {
	page := sourceVideo(t, "videoadd.html")
	shot := filepath.Join(t.TempDir(), "f.png")
	err := withCapture(t, page, 320, 240, testBudget, func(ctx context.Context, c *Capture) error {
		for n := 0; n <= 30; n++ {
			if err := c.Seek(ctx, at(n)); err != nil {
				return err
			}
		}
		var ready int
		if err := c.Eval(ctx, `document.getElementById("v").readyState`, &ready); err != nil {
			return err
		}
		if ready < 2 {
			t.Errorf("readyState %d after the seek that added the video", ready)
		}
		return c.Frame(ctx, shot)
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := raster.Load(shot)
	if err != nil {
		t.Fatal(err)
	}
	ref := sourceFrame(t, filepath.Join(filepath.Dir(page), "source.mp4"), 0)
	if d, _ := raster.RMSE(got, ref, ref.Bounds(), raster.RGB); d > 0.01 {
		t.Errorf("frame after the video was added: RMSE %.4f against source frame 0", d)
	}
}

func TestCaptureRefusesStalledVideo(t *testing.T) {
	if _, err := engine.Resolve("ffmpeg"); err != nil {
		t.Skip(err)
	}
	page := withQuery(t, "testdata/videoadd.html", url.Values{"src": {hangServer(t) + "/never.mp4"}})
	err := withCapture(t, page, 320, 240, 300*time.Millisecond, func(ctx context.Context, c *Capture) error {
		for n := 0; n <= 30; n++ {
			if err := c.Seek(ctx, at(n)); err != nil {
				return err
			}
		}
		return nil
	})
	if err == nil || !strings.Contains(err.Error(), "not ready after 300 ms") || !strings.Contains(err.Error(), "/never.mp4") {
		t.Fatalf("err = %v", err)
	}
}

func TestClockLateSVG(t *testing.T) {
	err := withCapture(t, "testdata/svglate.html", 300, 100, testBudget, func(ctx context.Context, c *Capture) error {
		x := func(want float64) func(float64) {
			return func(v float64) { wantNear(t, "x", v, want, 1e-3) }
		}
		return evalAt(t, c, ctx, 60, `document.getElementById("r").x.animVal.value`,
			map[int]func(float64){30: x(0), 60: x(100)})
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestClockDrivesIframe(t *testing.T) {
	err := withCapture(t, "testdata/iframe.html", 600, 100, testBudget, func(ctx context.Context, c *Capture) error {
		x := func(want float64) func(float64) {
			return func(v float64) { wantNear(t, "iframe x", v, want, 1e-6) }
		}
		return evalAt(t, c, ctx, 30,
			`(() => { const d = document.getElementById("f").contentDocument; return new DOMMatrix(d.defaultView.getComputedStyle(d.getElementById("b")).transform).m41; })()`,
			map[int]func(float64){0: x(0), 15: x(125), 30: x(250)})
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestCaptureRefusesCrossOriginIframe(t *testing.T) {
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		io.WriteString(w, "<!doctype html><p>elsewhere</p>")
	}))
	t.Cleanup(other.Close)
	page := withQuery(t, "testdata/iframexo.html", url.Values{"src": {other.URL + "/elsewhere.html"}})
	err := withCapture(t, page, 200, 100, testBudget, func(ctx context.Context, c *Capture) error {
		return c.Seek(ctx, 0)
	})
	if err == nil || !strings.Contains(err.Error(), "cross-origin") || !strings.Contains(err.Error(), "/elsewhere.html") {
		t.Fatalf("err = %v", err)
	}
}

func TestCaptureWaitsForLateFont(t *testing.T) {
	page := withQuery(t, "testdata/fontlate.html", url.Values{"font": {hangServer(t) + "/late.woff2"}})
	err := withCapture(t, page, 200, 100, 300*time.Millisecond, func(context.Context, *Capture) error {
		t.Fatal("Open settled a page whose font never loads")
		return nil
	})
	if err == nil || !strings.Contains(err.Error(), "not settled after 300 ms: fonts") {
		t.Fatalf("err = %v", err)
	}
}

func TestCaptureRefusesThrowBeforeFrame(t *testing.T) {
	err := withCapture(t, "testdata/css.html", 100, 100, testBudget, func(ctx context.Context, c *Capture) error {
		if err := c.Seek(ctx, 0); err != nil {
			t.Fatal(err)
		}
		if err := c.Eval(ctx, `queueMicrotask(() => { throw new Error("late throw"); })`, nil); err != nil {
			t.Fatal(err)
		}
		return c.Frame(ctx, filepath.Join(t.TempDir(), "f.png"))
	})
	if err == nil || !strings.Contains(err.Error(), "page threw at 0 ms") || !strings.Contains(err.Error(), "late throw") {
		t.Fatalf("err = %v", err)
	}
}

// boxLeft is the left edge, in px with subpixel coverage, of css.html's red
// box on white along row y.
func boxLeft(img *image.RGBA64, y int) float64 {
	b := img.Bounds()
	for x := b.Min.X; x < b.Max.X; x++ {
		g := float64(img.RGBA64At(x, y).G) / 0xffff
		cov := (1 - g) / (1 - 51.0/255)
		if cov > 0.02 {
			return float64(x) + 1 - math.Min(cov, 1)
		}
	}
	return math.Inf(1)
}

// TestCapturePixelsMatchClock reads positions from the screenshots, which
// the compositor paints, not from getComputedStyle.
func TestCapturePixelsMatchClock(t *testing.T) {
	dir := t.TempDir()
	frames := []int{0, 1, 15, 30, 45, 60}
	err := withCapture(t, "testdata/css.html", 600, 100, testBudget, func(ctx context.Context, c *Capture) error {
		return sample(ctx, c, 60, map[int]bool{0: true, 1: true, 15: true, 30: true, 45: true, 60: true}, func(n int) error {
			return c.Frame(ctx, filepath.Join(dir, fmt.Sprintf("css%d.png", n)))
		})
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range frames {
		img, err := raster.Load(filepath.Join(dir, fmt.Sprintf("css%d.png", n)))
		if err != nil {
			t.Fatal(err)
		}
		wantNear(t, fmt.Sprintf("frame %d box edge", n), boxLeft(img, 20), at(n)*0.25, 1)
	}

	err = withCapture(t, "testdata/transition.html", 100, 100, testBudget, func(ctx context.Context, c *Capture) error {
		return sample(ctx, c, 45, map[int]bool{15: true, 30: true, 45: true}, func(n int) error {
			return c.Frame(ctx, filepath.Join(dir, fmt.Sprintf("tr%d.png", n)))
		})
	})
	if err != nil {
		t.Fatal(err)
	}
	// #3a3 over white at opacity o has red 255 − 204·o.
	for n, o := range map[int]float64{15: 0, 30: 0.5, 45: 1} {
		img, err := raster.Load(filepath.Join(dir, fmt.Sprintf("tr%d.png", n)))
		if err != nil {
			t.Fatal(err)
		}
		r := float64(img.RGBA64At(20, 20).R) / 0xffff * 255
		wantNear(t, fmt.Sprintf("transition red at %.0f ms", at(n)), r, 255-204*o, 3)
	}
}

func TestClockReversedAnimation(t *testing.T) {
	type st struct {
		Time   float64
		Events int
	}
	err := withCapture(t, "testdata/reverse.html", 100, 100, testBudget, func(ctx context.Context, c *Capture) error {
		check := func(time float64, events int) func(st) {
			return func(s st) {
				if math.Abs(s.Time-time) > 1e-6 || s.Events != events {
					t.Errorf("got %+v, want time %v, %d finish events", s, time, events)
				}
			}
		}
		return evalAt(t, c, ctx, 30, `({Time: window.anim.currentTime ?? -1, Events: window.finishEvents})`,
			map[int]func(st){0: check(1000, 0), 15: check(500, 0), 30: check(0, 1)})
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestClockPageSetsAnimationTime(t *testing.T) {
	type st struct{ Running, Held float64 }
	err := withCapture(t, "testdata/scrub.html", 100, 100, testBudget, func(ctx context.Context, c *Capture) error {
		check := func(want st) func(st) {
			return func(s st) {
				if math.Abs(s.Running-want.Running) > 1e-6 || math.Abs(s.Held-want.Held) > 1e-6 {
					t.Errorf("got %+v, want %+v", s, want)
				}
			}
		}
		return evalAt(t, c, ctx, 30, `({Running: window.running.currentTime, Held: window.held.currentTime})`,
			map[int]func(st){14: check(st{at(14), 0}), 15: check(st{600, 600}), 30: check(st{1100, 600})})
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestCaptureRefusesRestlessPage(t *testing.T) {
	err := withCapture(t, sourceVideo(t, "restless.html"), 100, 100, testBudget, func(ctx context.Context, c *Capture) error {
		return c.Seek(ctx, 0)
	})
	if err == nil || !strings.Contains(err.Error(), "still starting animations, media seeks or timers after 20 passes at 0 ms") {
		t.Fatalf("err = %v", err)
	}
}

func TestClockTimerFromFinishHandler(t *testing.T) {
	type st struct {
		N    int
		Time float64
	}
	err := withCapture(t, "testdata/finishtimer.html", 200, 100, testBudget, func(ctx context.Context, c *Capture) error {
		return evalAt(t, c, ctx, 30,
			`(() => { const a = document.getElementById("b").getAnimations(); return {N: a.length, Time: a[0]?.currentTime ?? -1}; })()`,
			map[int]func(st){
				15: func(s st) {
					if s.N != 1 || s.Time != 0 {
						t.Errorf("at 500 ms: %+v, want the chained animation held at 0", s)
					}
				},
				30: func(s st) { wantNear(t, "chained time at 1000 ms", s.Time, 500, 1e-6) },
			})
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestClockCancelledAnimationRestarts(t *testing.T) {
	err := withCapture(t, "testdata/cancel.html", 100, 100, testBudget, func(ctx context.Context, c *Capture) error {
		ct := func(want float64) func(float64) {
			return func(v float64) { wantNear(t, "currentTime", v, want, 1e-6) }
		}
		return evalAt(t, c, ctx, 24, `window.anim.currentTime ?? -1`,
			map[int]func(float64){9: ct(-1), 18: ct(0), 24: ct(200)})
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestClockRejectedTimeWriteKeepsTime(t *testing.T) {
	err := withCapture(t, "testdata/scrub.html", 100, 100, testBudget, func(ctx context.Context, c *Capture) error {
		ct := func(want float64) func(float64) {
			return func(v float64) { wantNear(t, "currentTime", v, want, 1e-6) }
		}
		return evalAt(t, c, ctx, 15,
			`(() => { try { window.running.currentTime = null; } catch {} return window.running.currentTime; })()`,
			map[int]func(float64){3: ct(at(3)), 12: ct(at(12))})
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestClockVideoEvents(t *testing.T) {
	page := sourceVideo(t, "videoevents.html")
	type st struct {
		P, A   []string
		Held   bool
		HeldAt float64
	}
	expr := `(() => { const h = document.getElementById("h"); return {P: window.events.p, A: window.events.a, Held: h.paused, HeldAt: h.currentTime}; })()`
	err := withCapture(t, page, 320, 240, testBudget, func(ctx context.Context, c *Capture) error {
		check := func(p, a []string) func(st) {
			return func(s st) {
				if fmt.Sprint(s.P) != fmt.Sprint(p) || fmt.Sprint(s.A) != fmt.Sprint(a) {
					t.Errorf("events: play() %v, autoplay %v; want %v, %v", s.P, s.A, p, a)
				}
				if !s.Held || s.HeldAt != 0 {
					t.Errorf("an autoplay video paused before it was seen: paused %v at %v, want paused at 0", s.Held, s.HeldAt)
				}
			}
		}
		started := []string{"play", "playing"}
		ended := []string{"play", "playing", "pause", "ended"}
		return evalAt(t, c, ctx, 100, expr, map[int]func(st){
			30:  check(started, started),
			91:  check(started, ended),
			100: check(ended, ended),
		})
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestClockCancelsQueuedFrame(t *testing.T) {
	err := withCapture(t, "testdata/rafcancel.html", 100, 100, testBudget, func(ctx context.Context, c *Capture) error {
		return evalAt(t, c, ctx, 2, `window.ran`, map[int]func(bool){2: func(ran bool) {
			if ran {
				t.Error("a frame callback cancelled by an earlier one in the same batch ran")
			}
		}})
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestCaptureRefusesStringTimer(t *testing.T) {
	err := withCapture(t, "testdata/stringtimer.html", 100, 100, testBudget, func(ctx context.Context, c *Capture) error {
		return c.Seek(ctx, 0)
	})
	if err == nil || !strings.Contains(err.Error(), "page threw at 0 ms") || !strings.Contains(err.Error(), "setTimeout with a code string") {
		t.Fatalf("err = %v", err)
	}
}

func TestClockClampsNestedTimers(t *testing.T) {
	err := withCapture(t, "testdata/nestedtimer.html", 100, 100, testBudget, func(ctx context.Context, c *Capture) error {
		// The script's call and four nested runs at 0 ms, then one every 4 ms.
		want := map[int]int{0: 5, 30: 255}
		check := func(f int) func(int) {
			return func(n int) {
				if n != want[f] {
					t.Errorf("frame %d: the self-rescheduling timer ran %d times, want %d", f, n, want[f])
				}
			}
		}
		return evalAt(t, c, ctx, 30, `window.n`, map[int]func(int){0: check(0), 30: check(30)})
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestClockClampsRepeatingInterval(t *testing.T) {
	err := withCapture(t, "testdata/interval.html", 100, 100, testBudget, func(ctx context.Context, c *Capture) error {
		// Runs at 0, 1, 2 and 3 ms, then every 4 ms: 3 + 4k <= 1000.
		return evalAt(t, c, ctx, 30, `window.n`, map[int]func(int){30: func(n int) {
			if n != 253 {
				t.Errorf("at 1000 ms setInterval(f, 0) ran %d times, want 253", n)
			}
		}})
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestClockTimerRunsAtItsDueTime(t *testing.T) {
	err := withCapture(t, "testdata/typewriter.html", 100, 100, testBudget, func(ctx context.Context, c *Capture) error {
		return evalAt(t, c, ctx, 30, `window.log`, map[int]func([]float64){30: func(log []float64) {
			if len(log) != 20 {
				t.Errorf("a 50 ms chained timer ran %d times by 1000 ms, want 20", len(log))
			}
			for i, ms := range log {
				if want := float64(i+1) * 50; ms != want {
					t.Errorf("run %d read %v ms, want %v", i, ms, want)
				}
			}
		}})
	})
	if err != nil {
		t.Fatal(err)
	}
}

// TestCaptureRefusesTimerOverrun seeks once across 1000 s of a clamped
// setInterval(f, 0): about 250000 runs, past the cap.
func TestCaptureRefusesTimerOverrun(t *testing.T) {
	err := withCapture(t, "testdata/interval.html", 100, 100, testBudget, func(ctx context.Context, c *Capture) error {
		return c.Seek(ctx, 1e6)
	})
	if err == nil || !strings.Contains(err.Error(), "timers still due after 100000 runs at 1000000 ms") || strings.Contains(err.Error(), "__plate") {
		t.Fatalf("err = %v", err)
	}
}

func TestCaptureRefusalNamesPreloadNone(t *testing.T) {
	err := withCapture(t, "testdata/videopreload.html", 100, 100, 300*time.Millisecond, func(ctx context.Context, c *Capture) error {
		return c.Seek(ctx, 0)
	})
	if err == nil || !strings.Contains(err.Error(), "source.mp4") || !strings.Contains(err.Error(), `preload="none"`) {
		t.Fatalf("err = %v", err)
	}
}
