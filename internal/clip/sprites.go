package clip

import (
	"bytes"
	"context"
	"embed"
	"fmt"
	"html/template"
	"image"
	"image/draw"
	_ "image/png"
	"os"
	"path/filepath"

	"github.com/lockyc/plate/internal/render"
)

//go:embed sprites/*.html
var spriteFS embed.FS

var spriteTmpl = template.Must(template.ParseFS(spriteFS, "sprites/*.html"))

// SpriteSpec is one overlay image. A text sprite (caption, keys) is a
// full-frame W×H page; a point sprite (tap, click, cursor) is a square
// rendered at the largest on-screen size any frame needs. Scale is the
// largest Op.Scale among the sprite's ops, which the image is rendered at:
// a compositor draws it at W×H·Op.Scale/Scale. RegionH is the layout's
// region height, where a text sprite sits above.
type SpriteSpec struct {
	Key, Kind, Text string
	W, H, RegionH   int
	Scale           float64
}

// spriteData is what the sprite pages are filled from. K is the sprite's
// px per pt, D the pt size of the thing drawn, Box the pt side of the page.
type spriteData struct {
	Text    string
	RegionH int
	CSS     template.CSS
	K, D    float64
	Box     float64
	Half    float64
}

// boxPt is the pt side of a point sprite's page, and D the pt size of what
// it draws: the disc, the ring, or the cursor's height (its hotspot is the
// page's centre, so the page spans twice the cursor each way).
func boxPt(kind string) (box, d float64) {
	switch kind {
	case "tap":
		return tapDiameter + 2*spritePad, tapDiameter
	case "click":
		return clickDiameter + 2*spritePad, clickDiameter
	default:
		return 2 * (cursorHeight + spritePad), cursorHeight
	}
}

func spritePage(sp SpriteSpec, captionCSS string) ([]byte, error) {
	d := spriteData{Text: sp.Text, RegionH: sp.RegionH, CSS: template.CSS(captionCSS)}
	if sp.Kind != "caption" && sp.Kind != "keys" {
		d.Box, d.D = boxPt(sp.Kind)
		d.K = float64(sp.W) / d.Box
		d.Half = d.Box / 2
	}
	var b bytes.Buffer
	if err := spriteTmpl.ExecuteTemplate(&b, sp.Kind+".html", d); err != nil {
		return nil, fmt.Errorf("sprite %s: %w", sp.Key, err)
	}
	return b.Bytes(), nil
}

// RenderSprites renders each spec to a transparent PNG through Chrome, with
// its page and PNG kept in dir as <key>.html and <key>.png.
func RenderSprites(ctx context.Context, specs []SpriteSpec, captionCSS, dir string) (map[string]*image.RGBA64, error) {
	out := map[string]*image.RGBA64{}
	for _, sp := range specs {
		page, err := spritePage(sp, captionCSS)
		if err != nil {
			return nil, err
		}
		html, png := filepath.Join(dir, sp.Key+".html"), filepath.Join(dir, sp.Key+".png")
		if err := os.WriteFile(html, page, 0o644); err != nil {
			return nil, err
		}
		if err := render.PNGAlpha(ctx, html, png, sp.W, sp.H, 1); err != nil {
			return nil, fmt.Errorf("sprite %s: %w", sp.Key, err)
		}
		f, err := os.Open(png)
		if err != nil {
			return nil, err
		}
		img, _, err := image.Decode(f)
		f.Close()
		if err != nil {
			return nil, fmt.Errorf("sprite %s: %w", sp.Key, err)
		}
		rgba := image.NewRGBA64(img.Bounds())
		draw.Draw(rgba, rgba.Bounds(), img, img.Bounds().Min, draw.Src)
		out[sp.Key] = rgba
	}
	return out, nil
}
