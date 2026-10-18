package clip

import (
	"context"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lockyc/plate/internal/enginetest"
)

func captionHTML(t *testing.T, text, css string) string {
	t.Helper()
	b, err := spritePage(SpriteSpec{Key: "caption-0", Kind: "caption", Text: text, W: 600, H: 1048, RegionH: 955}, css)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestCaptionPageEscapes(t *testing.T) {
	page := captionHTML(t, `<b>&"`, "")
	if !strings.Contains(page, "&lt;b&gt;&amp;&#34;") || strings.Contains(page, "<b>") {
		t.Errorf("caption text not escaped:\n%s", page)
	}
}

func TestCaptionPageMarkup(t *testing.T) {
	page := captionHTML(t, "Tap", "/*consumer*/")
	body := page[strings.Index(page, "<body>")+len("<body>") : strings.Index(page, "</body>")]
	if want := `<div class="clip-caption"><span class="clip-caption-text">Tap</span></div>`; body != want {
		t.Errorf("body = %q, want %q", body, want)
	}
	def, consumer := strings.Index(page, "clip-caption-text {"), strings.Index(page, "/*consumer*/")
	if def < 0 || consumer < 0 || def > consumer {
		t.Errorf("default style at %d, consumer css at %d", def, consumer)
	}
	if !strings.Contains(page, "html, body {") || strings.Count(page, "background: transparent") < 1 {
		t.Errorf("html and body are not transparent:\n%s", page)
	}
	if !strings.Contains(page, "--region-bottom: 955px") {
		t.Errorf("no --region-bottom:\n%s", page)
	}
}

func TestRenderSpritesUsesPNGAlpha(t *testing.T) {
	fix := t.TempDir()
	sizes := [][2]int{{60, 104}, {52, 52}}
	for _, sz := range sizes {
		img := image.NewNRGBA(image.Rect(0, 0, sz[0], sz[1]))
		img.Set(0, 0, color.NRGBA{255, 255, 255, 128})
		f, _ := os.Create(filepath.Join(fix, fmt.Sprintf("%dx%d.png", sz[0], sz[1])))
		png.Encode(f, img)
		f.Close()
	}
	t.Setenv("FIXTURE_DIR", fix)
	log := enginetest.Stub(t, "chrome-headless-shell", `for a in "$@"; do
  case "$a" in
    --window-size=*) size=$(echo "${a#--window-size=}" | tr , x) ;;
    --screenshot=*) out="${a#--screenshot=}" ;;
  esac
done
cp "$FIXTURE_DIR/$size.png" "$out"`)
	specs := []SpriteSpec{
		{Key: "caption-0", Kind: "caption", Text: "Tap", W: 60, H: 104, RegionH: 90, Scale: 1},
		{Key: "tap", Kind: "tap", W: 52, H: 52, Scale: 1.15},
	}
	dir := t.TempDir()
	imgs, err := RenderSprites(context.Background(), specs, "", dir)
	if err != nil {
		t.Fatal(err)
	}
	calls := enginetest.Calls(t, log)
	if len(calls) != len(specs) || len(imgs) != len(specs) {
		t.Fatalf("%d chrome calls, %d images, want %d", len(calls), len(imgs), len(specs))
	}
	for i, sp := range specs {
		args := strings.Join(calls[i], " ")
		for _, want := range []string{"--default-background-color=00000000", fmt.Sprintf("--window-size=%d,%d", sp.W, sp.H)} {
			if !strings.Contains(args, want) {
				t.Errorf("call %d lacks %s: %s", i, want, args)
			}
		}
		if b := imgs[sp.Key].Bounds(); b.Dx() != sp.W || b.Dy() != sp.H {
			t.Errorf("%s image is %v", sp.Key, b)
		}
		if _, err := os.Stat(filepath.Join(dir, sp.Key+".html")); err != nil {
			t.Error(err)
		}
	}
}

func TestPointPagesRender(t *testing.T) {
	for _, kind := range []string{"tap", "click", "cursor"} {
		b, err := spritePage(SpriteSpec{Key: kind, Kind: kind, W: 100, H: 100, Scale: 1}, "")
		if err != nil || strings.Contains(string(b), "ZgotmplZ") {
			t.Errorf("%s page: %v\n%s", kind, err, b)
		}
	}
}
