package render

import (
	"context"
	"image"
	"image/color"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lockyc/plate/internal/engine"
)

var red = color.NRGBA{255, 0, 0, 255}

// realRender renders page, served at / in the pinned Chrome, to a 100×100
// PNG within budget ms, and returns the colour at its centre. The server
// also answers /late.png with a red square 300 ms late and /never not at
// all.
func realRender(t *testing.T, page string, budget int) (color.NRGBA, error) {
	t.Helper()
	if _, err := engine.Resolve(chrome); err != nil {
		t.Skip(err)
	}
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/late.png":
			time.Sleep(300 * time.Millisecond)
			img := image.NewRGBA(image.Rect(0, 0, 100, 100))
			for i := range img.Pix {
				img.Pix[i] = []uint8{255, 0, 0, 255}[i%4]
			}
			w.Header().Set("Content-Type", "image/png")
			png.Encode(w, img)
		case "/never":
			<-r.Context().Done()
		default:
			w.Header().Set("Content-Type", "text/html")
			io.WriteString(w, page)
		}
	}))
	t.Cleanup(srv.Close)
	out := filepath.Join(t.TempDir(), "p.png")
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if _, err := Run(ctx, Options{Page: srv.URL + "/page.html", PNG: out, W: 100, H: 100, Scale: 1, Budget: budget}, io.Discard); err != nil {
		return color.NRGBA{}, err
	}
	f, err := os.Open(out)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	img, err := png.Decode(f)
	if err != nil {
		t.Fatal(err)
	}
	return color.NRGBAModel.Convert(img.At(50, 50)).(color.NRGBA), nil
}

// TestRenderWaitsForLateImage: an image the page starts only once it has
// loaded, answered late, is in the screenshot.
func TestRenderWaitsForLateImage(t *testing.T) {
	got, err := realRender(t, `<!doctype html><body style="margin:0"><img id="i" width="100" height="100">
<script>addEventListener("load", () => { document.getElementById("i").src = "/late.png"; });</script>`, DefaultBudget)
	if err != nil {
		t.Fatal(err)
	}
	if got != red {
		t.Errorf("centre %v, want the late image's red", got)
	}
}

// TestRenderWaitsForLazyImageInView: a lazy image in the viewport loads,
// so it is waited for.
func TestRenderWaitsForLazyImageInView(t *testing.T) {
	got, err := realRender(t, `<!doctype html><body style="margin:0"><img loading="lazy" src="/late.png" width="100" height="100">`, DefaultBudget)
	if err != nil {
		t.Fatal(err)
	}
	if got != red {
		t.Errorf("centre %v, want the lazy image's red", got)
	}
}

// TestRenderSkipsDeferredLoads: a lazy image far outside the viewport and a
// preload="none" video never load in a capture, by the page's design, so
// they are not waited for.
func TestRenderSkipsDeferredLoads(t *testing.T) {
	_, err := realRender(t, `<!doctype html><body style="margin:0">
<img loading="lazy" src="/never" width="100" height="100" style="margin-top:20000px">
<video preload="none" src="/never" width="100" height="100"></video>`, 1000)
	if err != nil {
		t.Fatal(err)
	}
}
