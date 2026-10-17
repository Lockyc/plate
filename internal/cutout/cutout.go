// Package cutout lifts the subject out of an image as RGBA at the source's
// framing: a coarse mask (Apple Vision or BiRefNet) as the prior, ViTMatte
// re-solving a wide band of it over the whole frame at up to contextSide²
// px and then the edge at full resolution in tiles, foreground estimation
// to lift the background's tint out of soft edges, and an optional despill.
// Every step computes alpha or un-mixes captured colour, so cutout is not
// a synthesising command. The output is PNG: a soft matte edge is exactly
// the artefact print cannot hide, and lossy alpha damages it.
package cutout

import (
	"context"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/lockyc/plate/internal/cli"
	"github.com/lockyc/plate/internal/engine"
	imgframe "github.com/lockyc/plate/internal/frame"
	"github.com/lockyc/plate/internal/imgsize"
	"github.com/lockyc/plate/internal/ml"
	"github.com/lockyc/plate/internal/pins"
	"github.com/lockyc/plate/internal/raster"
	"github.com/lockyc/plate/internal/vision"
)

const usage = "cutout [--coarse vision|birefnet] [--height PX] [--tile PX] [--band-in D] [--band-out D] [--despill PRESET] [--despill-hue A:B] [--despill-clean A:B] [--despill-chroma A:B] [--despill-lmax L] [--despill-hue-end H] <in> <out.png>"

// overlap is how far adjacent ViTMatte tiles overlap, in px. A tile must be
// larger than it, or the tiles never advance across the frame.
const overlap = 128

// contextSide bounds the whole-frame ViTMatte pass that re-solves the coarse
// mask before the full-resolution pass: its frame holds at most contextSide²
// px. ViTMatte's memory grows with the square of the pixel count, whatever
// the shape, so an area bound costs one 1024 px tile's memory at any aspect
// ratio and keeps all the resolution that budget allows. A height or long-side
// bound would not: a height bound lets a panorama grow without limit, and a
// long-side bound wastes resolution on every frame that is not square.
const contextSide = 1024

// contextSize scales a w×h frame to at most contextSide² px, keeping its
// aspect ratio. It never enlarges.
func contextSize(w, h int) (int, int) {
	if w*h <= contextSide*contextSide {
		return w, h
	}
	s := contextSide / math.Sqrt(float64(w)*float64(h))
	return max(1, int(float64(w)*s)), max(1, int(float64(h)*s))
}

// despill is one full set of despill values: matte.py despills only with
// all five, and has no defaults of its own.
type despill struct{ hue, clean, chroma, lmax, hueEnd string }

// presets name despill settings measured on a real subject. warm-on-green
// is warm hair and skin (Lab hue 15–65°) over foliage: contamination ramps
// in from 70° to 100°, stops at 200° (blue eyes and sky are not spill), at
// chroma 6–40 and lightness below 88.
var presets = map[string]despill{
	"warm-on-green": {hue: "70:100", clean: "15:65", chroma: "6:40", lmax: "88", hueEnd: "200"},
}

func defaultCoarse() string {
	if runtime.GOOS == "darwin" {
		return "vision"
	}
	return "birefnet"
}

// isPair reports whether s is A:B with finite numbers A < B.
func isPair(s string) bool {
	a, b, ok := strings.Cut(s, ":")
	x, e1 := cli.Finite(a)
	y, e2 := cli.Finite(b)
	return ok && e1 && e2 && x < y
}

func isNumber(s string) bool {
	_, ok := cli.Finite(s)
	return ok
}

type job struct {
	in, out, coarse         string
	height, tile, bin, bout int
	despill                 despill
}

// Main runs `plate cutout`.
func Main(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := cli.Flags("cutout", usage, stderr)
	coarse := fs.String("coarse", defaultCoarse(), "coarse mask: vision (macOS) or birefnet")
	height := cli.Int(fs, "height", 0, "working height in px, never above the source; at least the largest size the cut-out will be drawn at (0 = source height)")
	tile := cli.Int(fs, "tile", 1024, fmt.Sprintf("ViTMatte tile size in px, above the %d px overlap; 0 runs the whole frame (memory grows with the square of its pixel count)", overlap))
	bin := cli.Int(fs, "band-in", 12, "sure foreground of the context pass: the coarse mask eroded by height/D; lower it when the coarse mask swallows background deeper behind hair or fur, raise it when a part of the subject is thinner than about 2·height/D")
	bout := cli.Int(fs, "band-out", 40, "sure background: beyond the coarse mask dilated by height/D")
	preset := fs.String("despill", "", "despill preset: warm-on-green; the --despill-* flags override its values")
	hue := fs.String("despill-hue", "", "contamination ramp in Lab hue degrees, A:B; without a preset, despill needs all five --despill-* flags")
	clean := fs.String("despill-clean", "", "hue range of clean subject pixels, A:B")
	chroma := fs.String("despill-chroma", "", "chroma range treated as spill, A:B")
	lmax := fs.String("despill-lmax", "", "only pixels below this Lab lightness")
	hueEnd := fs.String("despill-hue-end", "", "hue above which a pixel is not spill")
	rest, code, ok := cli.Parse(fs, args, 2)
	if !ok {
		return code
	}
	usageErr := func(format string, a ...any) int { return cli.Usage(stderr, "cutout", format, a...) }
	j := job{in: rest[0], out: rest[1], coarse: *coarse, height: *height, tile: *tile, bin: *bin, bout: *bout}
	if strings.ToLower(filepath.Ext(j.out)) != ".png" {
		return usageErr("the output must be .png: lossy alpha damages the matte edge")
	}
	if j.coarse != "vision" && j.coarse != "birefnet" {
		return usageErr("--coarse %q: use vision or birefnet", j.coarse)
	}
	if j.height < 0 {
		return usageErr("--height %d: use 0 (the source height) or a positive height", j.height)
	}
	if j.tile != 0 && j.tile <= overlap {
		return usageErr("--tile %d: use 0 (the whole frame) or more than the %d px tile overlap", j.tile, overlap)
	}
	if j.bin < 1 || j.bout < 1 {
		return usageErr("--band-in and --band-out must be at least 1")
	}
	if *preset != "" {
		p, ok := presets[*preset]
		if !ok {
			return usageErr("--despill %q: known presets are warm-on-green", *preset)
		}
		j.despill = p
	}
	given := 0
	for _, o := range []struct {
		name, v string
		dst     *string
		pair    bool
	}{{"despill-hue", *hue, &j.despill.hue, true}, {"despill-clean", *clean, &j.despill.clean, true}, {"despill-chroma", *chroma, &j.despill.chroma, true}, {"despill-lmax", *lmax, &j.despill.lmax, false}, {"despill-hue-end", *hueEnd, &j.despill.hueEnd, false}} {
		if o.v == "" {
			continue
		}
		if o.pair && !isPair(o.v) {
			return usageErr("--%s %q: want A:B with A < B", o.name, o.v)
		}
		if !o.pair && !isNumber(o.v) {
			return usageErr("--%s %q: want a number", o.name, o.v)
		}
		*o.dst = o.v
		given++
	}
	if *preset == "" && given > 0 && given < 5 {
		return usageErr("despill without --despill needs all five of --despill-hue, --despill-clean, --despill-chroma, --despill-lmax and --despill-hue-end")
	}
	if j.coarse == "vision" {
		if err := vision.Available(); err != nil {
			return cli.Fail(stderr, "cutout", err)
		}
	}
	w, h, err := j.run(ctx)
	if err != nil {
		return cli.Fail(stderr, "cutout", err)
	}
	fmt.Fprintf(stdout, "wrote %s (%dx%d)\n", j.out, w, h)
	return 0
}

func (j job) run(ctx context.Context) (int, int, error) {
	// The matte step writes <out> after temp files and two engines have run,
	// and none of those steps names both <in> and <out>, so this is the one
	// place that can refuse them being the same file before any work.
	if engine.SameFile(j.in, j.out) {
		return 0, 0, fmt.Errorf("%s is both the input and the output", j.in)
	}
	// The frame drops alpha, so a transparent source's stored colour would
	// read as picture.
	src, err := imgframe.RequireOpaque(ctx, j.in)
	if err != nil {
		return 0, 0, err
	}
	sh := src.H
	h := j.height
	if h == 0 {
		h = sh
	}
	if h > sh {
		return 0, 0, fmt.Errorf("--height %d would upsample the %d px source; a cut-out is never upsampled", h, sh)
	}
	tmp, err := os.MkdirTemp("", "plate-cutout-")
	if err != nil {
		return 0, 0, err
	}
	defer os.RemoveAll(tmp)
	frame, mask := filepath.Join(tmp, "frame.png"), filepath.Join(tmp, "coarse.png")
	if err := imgframe.Write(ctx, j.in, frame, imgframe.Options{Height: h, Opaque: true}); err != nil {
		return 0, 0, err
	}
	fw, fh, err := imgsize.Dims(frame)
	if err != nil {
		return 0, 0, err
	}
	cw, ch := contextSize(fw, fh)
	if err := j.coarseMask(ctx, frame, mask, tmp); err != nil {
		return 0, 0, err
	}
	if err := hasForeground(mask); err != nil {
		other := map[string]string{"vision": "birefnet", "birefnet": "vision (macOS)"}[j.coarse]
		return 0, 0, fmt.Errorf("%s: %w; try --coarse %s", j.in, err, other)
	}
	args := []string{"--image", frame, "--coarse", mask, "--out", j.out,
		"--model", pins.ViTMatte.Repo, "--revision", pins.ViTMatte.Revision,
		"--context-width", strconv.Itoa(cw), "--context-height", strconv.Itoa(ch), "--band-in", strconv.Itoa(j.bin), "--band-out", strconv.Itoa(j.bout),
		"--tile", strconv.Itoa(j.tile), "--overlap", strconv.Itoa(overlap)}
	if d := j.despill; d.hue != "" {
		args = append(args, "--despill-hue", d.hue, "--despill-clean", d.clean, "--despill-chroma", d.chroma,
			"--despill-lmax", d.lmax, "--despill-hue-end", d.hueEnd)
	}
	if _, err := ml.RunScript(ctx, "matte.py", args, []string{frame, mask}, []string{j.out}); err != nil {
		return 0, 0, err
	}
	ow, oh, err := imgsize.Dims(j.out)
	if err != nil {
		os.Remove(j.out)
		return 0, 0, err
	}
	return ow, oh, nil
}

func (j job) coarseMask(ctx context.Context, frame, mask, tmp string) error {
	if j.coarse == "birefnet" {
		_, err := ml.RunScript(ctx, "birefnet.py", []string{frame, mask}, []string{frame}, []string{mask})
		return err
	}
	rgba := filepath.Join(tmp, "coarse-rgba.png")
	if err := vision.Mask(ctx, frame, rgba); err != nil {
		return err
	}
	_, err := engine.Run(ctx, engine.Cmd{Engine: "magick", Args: []string{rgba, "-alpha", "extract", mask}, Inputs: []string{rgba}, Outputs: []string{mask}})
	return err
}

// maxBand is the widest a coarse mask's uncertain band may be, as a share of
// the frame's long side. The band's width is its uncertain pixels (5–95%)
// over the perimeter of its confident region (95% and up). A mask that found
// a subject is near-binary with a soft edge a few px wide whatever the
// subject's size; the uncertain share of the mask is not, and reaches
// 15% on Vision for a subject filling 3% of the frame. A mask that found
// nothing is a haze with no confident region, or a ramp whose band runs
// across much of the frame: rembg stretches BiRefNet's output to the full
// 0–1 range, so a flat frame whose raw output never passes 0.0001 comes back
// as a 98%-"foreground" haze.
//
// Measured on 28 subjects under each coarse mask (the portrait, the fur, the
// synthetic figure and the embroidery detail; six tight crops filling 59–92%
// of the frame; and the first three composited onto plaster and foliage at
// 3%, 8% and 15% of the frame), the widest band is 0.97% on BiRefNet (the
// fur crop at 92%) and 0.50% on Vision (the portrait at 8%). Of 23 empty
// frames (10 flat colours; two linear, a radial and two plasma gradients;
// four kinds of noise; checkerboard; stripes; the plaster texture and a crop
// of it), Vision refuses all itself, and on BiRefNet the narrowest band is
// 7.95% (the radial gradient), then 13.9% (flat red); the other 21 have no
// confident region or a band of 92% and up. 2.5% holds for that
// population, 2.6× above the widest subject and 3.2× below the narrowest
// empty frame.
//
// Two blind spots. Confident speckles, scattered pixels at 95% and up in an
// uncertain haze, each add perimeter, so such a mask reads as a narrow band
// and passes. And every frame measured was about 600 px or more on its long
// side; below that the bound is unmeasured.
const maxBand = 0.025

// hasForeground refuses a coarse mask that found no subject: one that marks
// nothing, nothing clearly, or nothing with an edge narrower than maxBand.
func hasForeground(mask string) error {
	m, err := raster.Load(mask)
	if err != nil {
		return err
	}
	const lo, hi = 0.05 * 0xffff, 0.95 * 0xffff
	b := m.Bounds()
	w, h := b.Dx(), b.Dy()
	sure := make([]bool, w*h)
	var claimed, uncertain int
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			v := float64(m.RGBA64At(b.Min.X+x, b.Min.Y+y).R)
			sure[y*w+x] = v >= hi
			if v > lo {
				claimed++
				if v < hi {
					uncertain++
				}
			}
		}
	}
	if claimed == 0 {
		return fmt.Errorf("no foreground found: the coarse mask is empty")
	}
	perimeter := 0
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			if sure[y*w+x] && ((x > 0 && !sure[y*w+x-1]) || (x < w-1 && !sure[y*w+x+1]) ||
				(y > 0 && !sure[(y-1)*w+x]) || (y < h-1 && !sure[(y+1)*w+x])) {
				perimeter++
			}
		}
	}
	if perimeter == 0 && uncertain > 0 {
		return fmt.Errorf("no foreground found: no part of the coarse mask is clearly subject")
	}
	if perimeter == 0 {
		return nil // all subject, edge to edge
	}
	band := float64(uncertain) / float64(perimeter)
	if share := band / float64(max(w, h)); share > maxBand {
		width := "wider than the frame"
		if share < 1 {
			width = fmt.Sprintf("%.1f%% of the frame's long side", 100*share)
		}
		return fmt.Errorf("no foreground found: %.0f%% of the pixels the coarse mask marks at all are neither clearly subject nor background, a band around what it marks as subject averaging %s", 100*float64(uncertain)/float64(claimed), width)
	}
	return nil
}
