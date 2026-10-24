// Package icon makes a macOS app icon from a tile designed edge to edge: an
// SVG or PNG of the rounded tile and its art, filling its square. The tile
// is placed in Apple's icon grid, an 824 px box centred on the 1024 px
// canvas, because an icon drawn to the canvas edge shows about a quarter
// larger than every other app in the Dock and the app switcher. Placing it
// here rather than in the artwork means the margin cannot regress.
//
// Each size is rendered natively by Chrome (render.PNGAlpha) rather than
// resampled from the largest, so a 16 px icon is the vector drawn at 16 px.
// The .icns is written here in Go, in the entry codes and formats iconutil
// writes, so it needs no iconutil and builds on Linux too.
package icon

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"html/template"
	"image"
	"image/color"
	"image/png"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/lockyc/plate/internal/atomicfile"
	"github.com/lockyc/plate/internal/cli"
	"github.com/lockyc/plate/internal/render"
)

const usage = "icon [--icns out.icns] [--png PX:out.png]... <tile.svg|tile.png>\n\n" +
	"Places an edge-to-edge tile in Apple's icon grid (824 of 1024 px, centred) and writes\n" +
	"the macOS .icns, and any PNG at a size of its own, each size rendered natively."

// Canvas and Box are Apple's macOS icon grid: the tile's box, centred on
// the canvas, in px at the 1024 px size.
const (
	Canvas = 1024
	Box    = 824
)

// slot is one .icns entry: its type code, its side in px, and whether it
// holds packed ARGB rather than a PNG. The order is iconutil's, and so are
// the 16 and 32 px 1x entries' codes and format: macOS reads a PNG in the
// icp4 and icp5 slots meant for them as noise.
type slot struct {
	code string
	px   int
	argb bool
}

var slots = []slot{
	{"ic12", 64, false}, {"ic07", 128, false}, {"ic13", 256, false}, {"ic08", 256, false},
	{"ic04", 16, true}, {"ic14", 512, false}, {"ic09", 512, false}, {"ic05", 32, true},
	{"ic10", 1024, false}, {"ic11", 32, false},
}

var mimes = map[string]string{".svg": "image/svg+xml", ".png": "image/png"}

var page = template.Must(template.New("icon").Parse(`<!doctype html>
<html><head><meta charset="utf-8"><style>
html, body { margin: 0; background: transparent; }
img { position: absolute; left: {{.Inset}}px; top: {{.Inset}}px; width: {{.Side}}px; height: {{.Side}}px; }
</style></head><body><img src="{{.Src}}" alt=""></body></html>
`))

type pngOut struct {
	px   int
	path string
}

type pngFlags []pngOut

func (p *pngFlags) String() string { return "" }

func (p *pngFlags) Set(s string) error {
	px, path, ok := strings.Cut(s, ":")
	n, err := strconv.Atoi(px)
	if !ok || err != nil || n < 1 || n > Canvas || path == "" {
		return fmt.Errorf("want PX:out.png with PX from 1 to %d", Canvas)
	}
	*p = append(*p, pngOut{n, path})
	return nil
}

// Main runs `plate icon`.
func Main(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := cli.Flags("icon", usage, stderr)
	icns := fs.String("icns", "", "write the macOS .icns here")
	var pngs pngFlags
	fs.Var(&pngs, "png", "write the icon as a `PX:out.png` PNG, PX px square (repeatable)")
	rest, code, ok := cli.Parse(fs, args, 1)
	if !ok {
		return code
	}
	usageErr := func(format string, a ...any) int { return cli.Usage(stderr, "icon", format, a...) }
	if *icns == "" && len(pngs) == 0 {
		return usageErr("name an output: --icns, --png or both")
	}
	tile := rest[0]
	mime, ok := mimes[strings.ToLower(filepath.Ext(tile))]
	if !ok {
		return usageErr("%s: the tile must be an .svg or a .png", tile)
	}
	if err := run(ctx, tile, mime, *icns, pngs); err != nil {
		return cli.Fail(stderr, "icon", err)
	}
	if *icns != "" {
		fmt.Fprintf(stdout, "wrote %s\n", *icns)
	}
	for _, p := range pngs {
		fmt.Fprintf(stdout, "wrote %s\n", p.path)
	}
	return 0
}

func run(ctx context.Context, tile, mime, icns string, pngs []pngOut) error {
	src, err := os.ReadFile(tile)
	if err != nil {
		return err
	}
	tmp, err := os.MkdirTemp("", "plate-icon-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	var sizes []int
	if icns != "" {
		for _, s := range slots {
			sizes = append(sizes, s.px)
		}
	}
	for _, p := range pngs {
		sizes = append(sizes, p.px)
	}
	slices.Sort(sizes)
	uri := template.URL("data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(src))
	rendered := map[int][]byte{}
	for _, px := range slices.Compact(sizes) {
		if rendered[px], err = renderAt(ctx, tmp, uri, px); err != nil {
			return fmt.Errorf("%d px: %w", px, err)
		}
	}
	// Every size renders before any output is written, so a failure leaves
	// every output as it was.
	if icns != "" {
		b, err := Encode(rendered)
		if err != nil {
			return err
		}
		if err := atomicfile.WriteFile(icns, b, 0o644); err != nil {
			return err
		}
	}
	for _, p := range pngs {
		if err := atomicfile.WriteFile(p.path, rendered[p.px], 0o644); err != nil {
			return err
		}
	}
	return nil
}

// pageFor is the px px page that draws src in the grid's box.
func pageFor(src template.URL, px int) ([]byte, error) {
	scale := float64(px) / Canvas
	var b bytes.Buffer
	err := page.Execute(&b, struct {
		Inset, Side float64
		Src         template.URL
	}{float64(Canvas-Box) / 2 * scale, Box * scale, src})
	return b.Bytes(), err
}

func renderAt(ctx context.Context, dir string, src template.URL, px int) ([]byte, error) {
	b, err := pageFor(src, px)
	if err != nil {
		return nil, err
	}
	html := filepath.Join(dir, fmt.Sprintf("%d.html", px))
	out := filepath.Join(dir, fmt.Sprintf("%d.png", px))
	if err := os.WriteFile(html, b, 0o644); err != nil {
		return nil, err
	}
	if err := render.PNGAlpha(ctx, html, out, px, px, 1); err != nil {
		return nil, err
	}
	return os.ReadFile(out)
}

// Encode writes an .icns holding one entry per slot, from PNGs keyed by
// side in px; a slot whose size is missing is left out.
func Encode(pngs map[int][]byte) ([]byte, error) {
	var body bytes.Buffer
	for _, s := range slots {
		p, ok := pngs[s.px]
		if !ok {
			continue
		}
		if s.argb {
			img, err := png.Decode(bytes.NewReader(p))
			if err != nil {
				return nil, fmt.Errorf("%d px: %w", s.px, err)
			}
			p = packARGB(img)
		}
		body.WriteString(s.code)
		binary.Write(&body, binary.BigEndian, uint32(8+len(p)))
		body.Write(p)
	}
	var b bytes.Buffer
	b.WriteString("icns")
	binary.Write(&b, binary.BigEndian, uint32(8+body.Len()))
	b.Write(body.Bytes())
	return b.Bytes(), nil
}

// packARGB is img as an ARGB entry: "ARGB", then the alpha, red, green and
// blue planes, colour premultiplied by alpha, each run-length packed on its
// own.
func packARGB(img image.Image) []byte {
	r := img.Bounds()
	planes := make([][]byte, 4)
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			c := color.RGBAModel.Convert(img.At(x, y)).(color.RGBA)
			for i, v := range []byte{c.A, c.R, c.G, c.B} {
				planes[i] = append(planes[i], v)
			}
		}
	}
	out := []byte("ARGB")
	for _, p := range planes {
		out = packBits(out, p)
	}
	return out
}

// packBits appends p in the icns run-length code: 0x80+n-3 then one byte
// for a run of n from 3 to 130, or n-1 then n bytes for up to 128 literals.
func packBits(out, p []byte) []byte {
	for i := 0; i < len(p); {
		run := 1
		for i+run < len(p) && run < 130 && p[i+run] == p[i] {
			run++
		}
		if run >= 3 {
			out = append(out, byte(0x80+run-3), p[i])
			i += run
			continue
		}
		// Literals stop before the next run of 3, which packs smaller.
		j := i
		for j < len(p) && j-i < 128 && !(j+2 < len(p) && p[j] == p[j+1] && p[j] == p[j+2]) {
			j++
		}
		out = append(out, byte(j-i-1))
		out = append(out, p[i:j]...)
		i = j
	}
	return out
}
