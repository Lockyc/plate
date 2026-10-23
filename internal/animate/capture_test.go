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
			c, err := Open(ctx, l, w, h, 1, budget)
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

// sourceVideo writes video.html and a 3 s 30 fps 320×240 testsrc H.264
// source.mp4 beside it, returning the page.
func sourceVideo(t *testing.T) string {
	t.Helper()
	if _, err := engine.Resolve("ffmpeg"); err != nil {
		t.Skip(err)
	}
	if err := video.RequireEncoders(context.Background(), []string{"mp4"}); err != nil {
		t.Skip(err)
	}
	dir := t.TempDir()
	page, err := os.ReadFile("testdata/video.html")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "video.html"), page, 0o644); err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(dir, "source.mp4")
	if _, err := engine.Run(context.Background(), engine.Cmd{Engine: "ffmpeg", Args: []string{"-v", "error",
		"-f", "lavfi", "-i", "testsrc=size=320x240:rate=30", "-t", "3",
		"-c:v", "libx264", "-pix_fmt", "yuv420p", src}, Outputs: []string{src}}); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(dir, "video.html")
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
	page := sourceVideo(t)
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
