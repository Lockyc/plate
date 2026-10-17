// Package frame normalises a source image before an operation reads its
// pixels: EXIF rotation applied, colour converted to sRGB, 8 bits per
// channel, RGB(A) whatever the source's PNG colour type. A 16-bit grey,
// palette or wide-gamut source then reads the same to every model. Orient
// applies the rotation alone, for an operation whose output keeps the
// source's depth and profile.
package frame

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/lockyc/plate/internal/engine"
	"github.com/lockyc/plate/internal/icc"
)

// Options shape the frame.
type Options struct {
	Height int  // resize to this height; 0 keeps the source's
	Opaque bool // flatten onto black and write RGB, so a mask's transparent pixels are no hole; otherwise RGBA, opaque where the source has no alpha
}

// Args is the magick command line that writes in's frame to out. It orients,
// then converts to sRGB, then strips: stripping first loses the rotation and
// leaves a wide-gamut photo plausibly duller. The PNG24/PNG32 prefix forces a
// colour type, since magick otherwise keeps a grey or palette source as grey
// or palette.
func Args(in, out, srgb string, o Options) []string {
	a := append(SRGBArgs(in, srgb), "-strip")
	if o.Height > 0 {
		a = append(a, "-resize", "x"+strconv.Itoa(o.Height))
	}
	format := "PNG32:"
	if o.Opaque {
		a = append(a, "-background", "black", "-alpha", "remove", "-alpha", "off")
		format = "PNG24:"
	}
	return append(a, "-depth", "8", format+out)
}

// SRGBArgs reads in upright and converted to the sRGB profile at srgb, its
// depth, alpha and colour type kept: the prefix Args builds on, for an
// operation that must see the colours a frame sees but keep the rest. A grey
// source with an embedded grey profile converts correctly.
func SRGBArgs(in, srgb string) []string { return append(oriented(in), "-profile", srgb) }

// Write writes in's frame to out, a PNG.
func Write(ctx context.Context, in, out string, o Options) error {
	srgb, err := icc.SRGB()
	if err != nil {
		return err
	}
	_, err = engine.Run(ctx, engine.Cmd{Engine: "magick", Args: Args(in, out, srgb, o), Inputs: []string{in}, Outputs: []string{out}})
	return err
}

// oriented reads in as it is displayed: its EXIF rotation applied.
func oriented(in string) []string { return []string{in, "-auto-orient"} }

// OrientArgs is the magick command line that writes in to out rotated as
// displayed and otherwise as stored: bit depth, colour profile and colour
// type stay the source's.
func OrientArgs(in, out string) []string { return append(oriented(in), out) }

// Orient writes in to out, a PNG, with only its EXIF rotation applied, so a
// mask drawn on the displayed image lines up with it.
func Orient(ctx context.Context, in, out string) error {
	_, err := engine.Run(ctx, engine.Cmd{Engine: "magick", Args: OrientArgs(in, out), Inputs: []string{in}, Outputs: []string{out}})
	return err
}

// Info is what Probe reads of a source as displayed.
type Info struct {
	Opaque     bool   // no pixel is transparent
	W, H       int    // EXIF rotation applied
	Colorspace string // ImageMagick's name: sRGB, Gray, CMYK, …
}

// ProbeArgs is the magick command line Probe runs.
func ProbeArgs(in string) []string {
	return append(oriented(in), "-format", "%[opaque] %w %h %[colorspace]\n", "info:")
}

// Probe reads in's Info. A multi-frame input is refused: every operation
// reads one picture, and magick would otherwise write one output per frame.
func Probe(ctx context.Context, in string) (Info, error) {
	res, err := engine.Run(ctx, engine.Cmd{Engine: "magick", Args: ProbeArgs(in), Inputs: []string{in}})
	if err != nil {
		return Info{}, err
	}
	lines := strings.Split(strings.TrimSpace(string(res.Stdout)), "\n")
	if len(lines) != 1 {
		return Info{}, fmt.Errorf("could not read %s: expected one frame (a multi-frame input is not supported)", in)
	}
	f := strings.Fields(lines[0])
	if len(f) != 4 {
		return Info{}, fmt.Errorf("could not read %s", in)
	}
	w, errW := strconv.Atoi(f[1])
	h, errH := strconv.Atoi(f[2])
	if errW != nil || errH != nil || w < 1 || h < 1 {
		return Info{}, fmt.Errorf("could not read the size of %s", in)
	}
	return Info{Opaque: f[0] == "True", W: w, H: h, Colorspace: f[3]}, nil
}

// RequireOpaque refuses a source with any transparent pixel, for an
// operation that reads every pixel as picture: it would fill from, or cut
// out, the colour stored under the transparency and show it.
func RequireOpaque(ctx context.Context, in string) (Info, error) {
	info, err := Probe(ctx, in)
	if err == nil && !info.Opaque {
		err = fmt.Errorf("%s has transparent pixels; flatten it onto its background first", in)
	}
	return info, err
}
