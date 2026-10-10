// Package shrink makes a small web asset from a large image: resampled
// with ImageMagick's Catrom filter in sRGB, then encoded as a PNG, a WebP or
// both. The filter is the one that came closest to the same artwork
// rendered natively at the small size (quality/cases.toml, shrink-*). The
// source is read upright and in sRGB, as every plate op reads it, then
// stripped of metadata; alpha is kept, and an opaque source is written
// without an alpha channel.
package shrink

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/lockyc/plate/internal/cli"
	"github.com/lockyc/plate/internal/engine"
	"github.com/lockyc/plate/internal/frame"
	"github.com/lockyc/plate/internal/icc"
)

const usage = "shrink (--width PX | --height PX | --width PX --height PX | --fit PX) [--quality Q] <in> <out.png|out.webp>...\n\n" +
	"Resizes to fit the box, aspect kept, never enlarging. Each output's extension picks its format:\n" +
	".png is lossless and compressed as small as plate's zlib settings make it; .webp is lossy at --quality."

// Filter is the resampling filter; quality/cases.toml's shrink-* cases
// hold the measurement that chose it.
const Filter = "Catrom"

// pngStrategies are the zlib settings a PNG is written with; the smallest
// result is kept. Adaptive row filters with the default strategy suit flat
// artwork, and with the filtered strategy suit photographs; neither wins on
// both.
var pngStrategies = [][]string{
	{"-define", "png:compression-level=9", "-define", "png:compression-filter=5", "-define", "png:compression-strategy=0"},
	{"-define", "png:compression-level=9", "-define", "png:compression-filter=5", "-define", "png:compression-strategy=1"},
}

// webpArgs encode a lossy WebP. sharp-yuv keeps colour edges that 4:2:0
// chroma would smear, and alpha stays lossless.
func webpArgs(quality int) []string {
	return []string{"-quality", strconv.Itoa(quality), "-define", "webp:method=6", "-define", "webp:use-sharp-yuv=true", "-define", "webp:alpha-quality=100"}
}

// Main runs `plate shrink`.
func Main(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := cli.Flags("shrink", usage, stderr)
	width := cli.Int(fs, "width", 0, "fit within this width in px")
	height := cli.Int(fs, "height", 0, "fit within this height in px")
	fit := cli.Int(fs, "fit", 0, "fit the longest side to this many px (instead of --width and --height)")
	quality := cli.Int(fs, "quality", 90, "WebP quality, 1 to 100")
	rest, code, ok := cli.Parse(fs, args, -1)
	if !ok {
		return code
	}
	if len(rest) < 2 {
		fs.Usage()
		return 2
	}
	if *fit != 0 {
		if *width != 0 || *height != 0 {
			return cli.Usage(stderr, "shrink", "give --fit, or --width and/or --height, not both")
		}
		*width, *height = *fit, *fit
	}
	if *width < 0 || *height < 0 || *width == 0 && *height == 0 {
		return cli.Usage(stderr, "shrink", "give a positive --width, --height or --fit")
	}
	if *quality < 1 || *quality > 100 {
		return cli.Usage(stderr, "shrink", "--quality must be 1 to 100")
	}
	in, outs := rest[0], rest[1:]
	for _, o := range outs {
		if ext := strings.ToLower(filepath.Ext(o)); ext != ".png" && ext != ".webp" {
			return cli.Usage(stderr, "shrink", "%s: an output must end .png or .webp", o)
		}
	}
	w, h, err := run(ctx, in, outs, *width, *height, *quality)
	if err != nil {
		return cli.Fail(stderr, "shrink", err)
	}
	for _, o := range outs {
		size := int64(0)
		if fi, err := os.Stat(o); err == nil {
			size = fi.Size()
		}
		fmt.Fprintf(stdout, "wrote %s (%dx%d, %d bytes)\n", o, w, h, size)
	}
	return 0
}

// Target is the size a w×h source shrinks to so it fits within boxW×boxH
// (0 leaves that side free), aspect kept and each side at least 1 px.
func Target(w, h, boxW, boxH int) (int, int) {
	s := math.Inf(1)
	if boxW > 0 {
		s = float64(boxW) / float64(w)
	}
	if boxH > 0 {
		s = math.Min(s, float64(boxH)/float64(h))
	}
	return max(1, int(math.Round(float64(w)*s))), max(1, int(math.Round(float64(h)*s)))
}

func run(ctx context.Context, in string, outs []string, boxW, boxH, quality int) (w, h int, err error) {
	// Each output is copied from a temporary file, so no engine call names
	// both <in> and an output; this is the one place that can refuse it.
	for i, o := range outs {
		if engine.SameFile(in, o) {
			return 0, 0, fmt.Errorf("%s is both an input and an output", in)
		}
		for _, p := range outs[:i] {
			if engine.SameFile(p, o) {
				return 0, 0, fmt.Errorf("%s is named twice as an output", o)
			}
		}
	}
	// An earlier run's outputs go first, and a failed run removes its own,
	// so a failure leaves no output that looks current.
	for _, o := range outs {
		if err := os.Remove(o); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return 0, 0, err
		}
	}
	defer func() {
		if err != nil {
			for _, o := range outs {
				os.Remove(o)
			}
		}
	}()
	res, err := engine.Run(ctx, engine.Cmd{Engine: "magick", Args: []string{in, "-auto-orient", "-format", "%[opaque] %w %h\n", "info:"}, Inputs: []string{in}})
	if err != nil {
		return 0, 0, err
	}
	lines := strings.Split(strings.TrimSpace(string(res.Stdout)), "\n")
	f := strings.Fields(lines[0])
	if len(lines) != 1 || len(f) != 3 {
		return 0, 0, fmt.Errorf("could not read %s: expected one frame (a multi-frame input is not supported)", in)
	}
	opaque := f[0] == "True"
	sw, errW := strconv.Atoi(f[1])
	sh, errH := strconv.Atoi(f[2])
	if errW != nil || errH != nil || sw < 1 || sh < 1 {
		return 0, 0, fmt.Errorf("could not read the size of %s", in)
	}
	w, h = Target(sw, sh, boxW, boxH)
	if w > sw || h > sh {
		return 0, 0, fmt.Errorf("%s is %dx%d, smaller than %dx%d; shrink never enlarges", in, sw, sh, w, h)
	}
	tmp, err := os.MkdirTemp("", "plate-shrink-")
	if err != nil {
		return 0, 0, err
	}
	defer os.RemoveAll(tmp)
	srgb, err := icc.SRGB()
	if err != nil {
		return 0, 0, err
	}
	// The resampled frame stays 16-bit until each encoder rounds it once.
	small := filepath.Join(tmp, "small.miff")
	args := append(frame.SRGBArgs(in, srgb), "-strip", "-filter", Filter, "-resize", fmt.Sprintf("%dx%d!", w, h))
	if opaque {
		args = append(args, "-alpha", "off")
	}
	args = append(args, "-depth", "16", "MIFF:"+small)
	if _, err := engine.Run(ctx, engine.Cmd{Engine: "magick", Args: args, Inputs: []string{in}, Outputs: []string{small}}); err != nil {
		return 0, 0, err
	}
	for i, o := range outs {
		var best string
		if strings.ToLower(filepath.Ext(o)) == ".webp" {
			best = filepath.Join(tmp, fmt.Sprintf("o%d.webp", i))
			a := append(append([]string{small}, webpArgs(quality)...), "WEBP:"+best)
			if _, err := engine.Run(ctx, engine.Cmd{Engine: "magick", Args: a, Inputs: []string{small}, Outputs: []string{best}}); err != nil {
				return 0, 0, err
			}
		} else if best, err = smallestPNG(ctx, small, tmp, i); err != nil {
			return 0, 0, err
		}
		if err := place(best, o); err != nil {
			return 0, 0, err
		}
	}
	return w, h, nil
}

// place copies src to dst through a temporary file in dst's directory and a
// rename, so an interrupted copy never leaves a partial dst.
func place(src, dst string) error {
	b, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(dst), ".plate-shrink-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	_, err = f.Write(b)
	if err == nil {
		err = f.Chmod(0o644)
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	return os.Rename(f.Name(), dst)
}

// smallestPNG writes small as an 8-bit PNG under each of pngStrategies and
// returns the smallest file.
func smallestPNG(ctx context.Context, small, tmp string, i int) (string, error) {
	var best string
	var bestSize int64
	for j, s := range pngStrategies {
		p := filepath.Join(tmp, fmt.Sprintf("o%d-%d.png", i, j))
		a := append(append([]string{small, "-depth", "8"}, s...), "PNG:"+p)
		if _, err := engine.Run(ctx, engine.Cmd{Engine: "magick", Args: a, Inputs: []string{small}, Outputs: []string{p}}); err != nil {
			return "", err
		}
		fi, err := os.Stat(p)
		if err != nil {
			return "", err
		}
		if best == "" || fi.Size() < bestSize {
			best, bestSize = p, fi.Size()
		}
	}
	return best, nil
}
