// Package infill fills holes in a flat ground (paper, a painted wall, sky)
// from their surroundings: each level blurs the image with its holes
// transparent, keeps what the blur reached, and the levels stack from
// coarse to fine, so a hole takes the colour of what is nearest on its side
// of any strong edge (edgeStops). The blur carries no grain, so texture then
// lays the ground's own fine detail, in patches taken from around the hole,
// over the fill. It synthesises pixels, so plate.toml can forbid it.
package infill

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/lockyc/plate/internal/cli"
	"github.com/lockyc/plate/internal/engine"
	"github.com/lockyc/plate/internal/frame"
	"github.com/lockyc/plate/internal/imgsize"
)

const usage = "infill --mask mask.png [--levels 150,60,20,6] [--grain 1] [--seed 1] <in> <out.png>\n\nThe output keeps the input's bit depth and colour profile, turned upright by its EXIF rotation."

// Main runs `plate infill`.
func Main(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := cli.Flags("infill", usage, stderr)
	mask := fs.String("mask", "", "white where the holes are, same size as the image (required)")
	levels := fs.String("levels", "150,60,20,6", "blur radii in px, coarse to fine; the grain is the detail finer than the last")
	grain := cli.Float(fs, "grain", 1, "strength of the ground's grain laid over the fill: 1 matches the ground, 0 leaves it smooth")
	seed := cli.Int(fs, "seed", 1, "seed for which patches of the ground make the grain")
	rest, code, ok := cli.Parse(fs, args, 2)
	if !ok {
		return code
	}
	var radii []int
	for _, s := range strings.Split(*levels, ",") {
		r, err := strconv.Atoi(strings.TrimSpace(s))
		if err != nil || r <= 0 || (len(radii) > 0 && r >= radii[len(radii)-1]) {
			return cli.Usage(stderr, "infill", "--levels must be positive radii, strictly coarse to fine")
		}
		radii = append(radii, r)
	}
	if *mask == "" || strings.ToLower(filepath.Ext(rest[1])) != ".png" || *grain < 0 {
		return cli.Usage(stderr, "infill", "give --mask, a --grain of 0 or more, and an output ending .png")
	}
	if err := run(ctx, rest[0], *mask, rest[1], radii, *grain, *seed); err != nil {
		return cli.Fail(stderr, "infill", err)
	}
	fmt.Fprintf(stdout, "wrote %s\n", rest[1])
	return 0
}

func run(ctx context.Context, in, mask, out string, radii []int, grain float64, seed int) error {
	// The step that writes <out> reads temp files, so no engine call names
	// both <in> or <mask> and <out>; this is the one place that can refuse
	// before the temp work runs.
	if engine.SameFile(in, out) || engine.SameFile(mask, out) {
		return fmt.Errorf("%s is also an input; write the result elsewhere", out)
	}
	tmp, err := os.MkdirTemp("", "plate-infill-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	// Oriented as displayed, so a mask drawn on the displayed image lines
	// up, and otherwise as stored: the result keeps the source's bit depth
	// and profile.
	info, err := frame.RequireOpaque(ctx, in)
	if err != nil {
		return err
	}
	// A PNG holds no CMYK, so writing one would convert without the
	// profile and shift every colour.
	if info.Colorspace == "CMYK" {
		return fmt.Errorf("%s is CMYK, which the PNG result cannot keep; convert it to RGB with its profile first", in)
	}
	img, m := filepath.Join(tmp, "in.png"), filepath.Join(tmp, "mask.png")
	if err := frame.Orient(ctx, in, img); err != nil {
		return err
	}
	if err := frame.Orient(ctx, mask, m); err != nil {
		return err
	}
	iw, ih, err := imgsize.Dims(img)
	if err != nil {
		return err
	}
	mw, mh, err := imgsize.Dims(m)
	if err != nil {
		return err
	}
	if iw != mw || ih != mh {
		return fmt.Errorf("the mask is %dx%d and the image %dx%d as displayed", mw, mh, iw, ih)
	}
	magick := func(inputs []string, args ...string) error {
		_, err := engine.Run(ctx, engine.Cmd{Engine: "magick", Args: args, Inputs: inputs, Outputs: []string{args[len(args)-1]}})
		return err
	}
	holes := filepath.Join(tmp, "holes.png")
	// The mask is flattened onto black first, so its transparent pixels are
	// no hole whatever colour they store.
	if err := magick([]string{img, m}, img, "(", m, "-background", "black", "-alpha", "remove", "-negate", ")", "-alpha", "off", "-compose", "CopyOpacity", "-composite", holes); err != nil {
		return err
	}
	// The levels see only the ground each hole reaches without crossing a
	// strong edge; the finest level sets how lightly that edge test blurs,
	// and the coarsest how hard a ground across an edge pulls on a hole.
	src := holes
	stops, err := edgeStops(holes, tmp, float64(radii[len(radii)-1])/2, float64(radii[0]))
	if err != nil {
		return err
	}
	if len(stops) > 0 {
		src = filepath.Join(tmp, "reached.png")
		inputs := []string{holes}
		keep := []string{"-size", fmt.Sprintf("%dx%d", iw, ih), "xc:white"}
		for _, p := range stops {
			inputs = append(inputs, p.path)
			keep = append(keep, p.path, "-geometry", fmt.Sprintf("+%d+%d", p.at.X, p.at.Y), "-compose", "over", "-composite")
		}
		args := append([]string{holes, "(", "+clone", "-alpha", "extract", "("}, keep...)
		args = append(args, ")", "-geometry", "+0+0", "-compose", "multiply", "-composite", ")", "-alpha", "off", "-compose", "CopyOpacity", "-composite", src)
		if err := magick(inputs, args...); err != nil {
			return err
		}
	}
	var layers []string
	for _, r := range radii {
		l := filepath.Join(tmp, fmt.Sprintf("b%d.png", r))
		if err := magick([]string{src}, src, "-channel", "RGBA", "-blur", fmt.Sprintf("0x%d", r), "-channel", "A", "-threshold", "0.1%", "+channel", l); err != nil {
			return err
		}
		layers = append(layers, l)
	}
	fill := filepath.Join(tmp, "fill.png")
	args := []string{layers[0]}
	for _, l := range layers[1:] {
		args = append(args, l, "-compose", "over", "-composite")
	}
	// Each level is opaque only as far as its blur reaches, so a hole wider
	// than the coarsest reach keeps a middle no level covers, which -alpha
	// off would show as black.
	reach := filepath.Join(tmp, "reach.png")
	if err := magick(layers, append(args, "-write", reach, "-alpha", "off", fill)...); err != nil {
		return err
	}
	res, err := engine.Run(ctx, engine.Cmd{Engine: "magick", Args: []string{reach, "-alpha", "extract", "(", m, "-negate", ")", "-compose", "lighten", "-composite", "-format", "%[fx:minima]", "info:"}, Inputs: []string{reach, m}})
	if err != nil {
		return err
	}
	if v, err := strconv.ParseFloat(strings.TrimSpace(string(res.Stdout)), 64); err != nil || v < 0.5 {
		return fmt.Errorf("the fill does not reach the middle of a hole: start --levels with a radius coarser than %d", radii[0])
	}
	var pieces []piece
	if grain > 0 {
		// The finest level is the scale the fill already follows, so the
		// grain is the detail finer than it.
		if pieces, err = texture(holes, fill, tmp, float64(radii[len(radii)-1]), grain, seed); err != nil {
			return err
		}
	}
	// holes first, so the result keeps the source's bit depth; the grained
	// pieces go over the fill at their offsets, then the fill under holes.
	inputs := []string{holes, fill}
	args = []string{holes, "(", fill}
	for _, p := range pieces {
		inputs = append(inputs, p.path)
		args = append(args, p.path, "-geometry", fmt.Sprintf("+%d+%d", p.at.X, p.at.Y), "-compose", "over", "-composite")
	}
	args = append(args, ")", "-geometry", "+0+0", "-compose", "dst-over", "-composite", "-alpha", "off", out)
	return magick(inputs, args...)
}
