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

// TestRenderWaitsForLateImage renders, in the pinned Chrome, a page that
// starts loading an image only once it has loaded, from a server that
// answers late; the screenshot shows the image.
func TestRenderWaitsForLateImage(t *testing.T) {
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
		default:
			w.Header().Set("Content-Type", "text/html")
			io.WriteString(w, `<!doctype html><body style="margin:0"><img id="i" width="100" height="100">
<script>addEventListener("load", () => { document.getElementById("i").src = "/late.png"; });</script>`)
		}
	}))
	t.Cleanup(srv.Close)
	out := filepath.Join(t.TempDir(), "p.png")
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if _, err := Run(ctx, Options{Page: srv.URL + "/page.html", PNG: out, W: 100, H: 100, Scale: 1, Budget: DefaultBudget}, io.Discard); err != nil {
		t.Fatal(err)
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
	if got := color.NRGBAModel.Convert(img.At(50, 50)).(color.NRGBA); got != (color.NRGBA{255, 0, 0, 255}) {
		t.Errorf("centre %v, want the late image's red", got)
	}
}
