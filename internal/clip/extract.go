package clip

import (
	"context"
	"errors"
	"fmt"
	"image"
	"image/draw"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/lockyc/plate/internal/engine"
)

// errEmptyWindow refuses a trim window with no frame in it.
var errEmptyWindow = errors.New("the trim window is empty: nothing to render")

// toRGB is the tail of every colour decode: BT.709 YCbCr in its tagged
// range to 8-bit RGB.
const toRGB = "scale=in_color_matrix=bt709:in_range=auto,format=rgb24"

// seconds formats a master time in ms as ffmpeg seconds, to the µs.
func seconds(ms float64) string {
	return strconv.FormatFloat(math.Round(ms*1000)/1e6, 'f', -1, 64)
}

// decode has ffmpeg write src through vf and then outArgs as
// prefix%05d.png. It decodes into a temporary directory and moves the
// frames into dir only once there are want of them (any number when want
// is 0), so dir never holds a partial set; short names what a short decode
// means.
func decode(ctx context.Context, src, vf string, outArgs []string, want int, short, prefix, dir string) ([]string, error) {
	tmp, err := os.MkdirTemp(dir, ".decode-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmp)
	args := append([]string{"-hide_banner", "-loglevel", "error", "-nostdin", "-i", src, "-vf", vf}, outArgs...)
	args = append(args, "-start_number", "0", filepath.Join(tmp, prefix+"%05d.png"))
	if _, err := engine.Run(ctx, engine.Cmd{Engine: "ffmpeg", Args: args, Inputs: []string{src}}); err != nil {
		return nil, err
	}
	var names []string
	for i := 0; ; i++ {
		n := fmt.Sprintf("%s%05d.png", prefix, i)
		if _, err := os.Stat(filepath.Join(tmp, n)); err != nil {
			break
		}
		names = append(names, n)
	}
	if want > 0 && len(names) < want {
		return nil, fmt.Errorf("%s: decoded %d of %d frames", short, len(names), want)
	}
	if len(names) == 0 {
		return nil, fmt.Errorf("%s: no frame decoded", src)
	}
	paths := make([]string, len(names))
	for i, n := range names {
		paths[i] = filepath.Join(dir, n)
		if err := os.Rename(filepath.Join(tmp, n), paths[i]); err != nil {
			for _, p := range paths[:i] {
				os.Remove(p)
			}
			return nil, err
		}
	}
	return paths, nil
}

// ExtractActivity decodes the whole master small and grey, a%05d.png in
// dir, for motion detection: frame i sits at i·1000/fps ms from the
// master's first frame, where ffmpeg's output timeline starts.
func ExtractActivity(ctx context.Context, master string, fps int, dir string) ([]*image.Gray, error) {
	paths, err := decode(ctx, master, fmt.Sprintf("fps=%d:round=down,scale=200:-2,format=gray", fps),
		[]string{"-fps_mode", "cfr"}, 0, master, "a", dir)
	if err != nil {
		return nil, err
	}
	out := make([]*image.Gray, len(paths))
	for i, p := range paths {
		img, err := loadImage(p)
		if err != nil {
			return nil, err
		}
		g, ok := img.(*image.Gray)
		if !ok {
			g = image.NewGray(img.Bounds())
			draw.Draw(g, g.Bounds(), img, img.Bounds().Min, draw.Src)
		}
		out[i] = g
	}
	return out, nil
}

// toBT709 is the filter that brings a master tagged with other primaries
// to BT.709's, keeping its transfer, so the frames read as sRGB; "" for a
// master tagged BT.709 or untagged. simctl tags its recordings Display P3
// (smpte432) with the sRGB transfer, and a P3 pixel read as BT.709 is
// desaturated. The colorspace filter does it, not swscale's out_primaries,
// which maps sRGB green written in P3 to 3,217,0.
func toBT709(c Colour) string {
	if c.Primaries == "" || c.Primaries == "bt709" {
		return ""
	}
	trc, rng := c.Transfer, c.Range
	if trc == "" {
		trc = "iec61966-2-1"
	}
	if rng == "" {
		rng = "tv"
	}
	return fmt.Sprintf("colorspace=ispace=bt709:irange=%s:iprimaries=%s:itrc=%s:space=bt709:range=pc:primaries=bt709:trc=%s:format=yuv444p12,",
		rng, c.Primaries, trc, trc)
}

// ExtractFrames decodes the window [w.Start, w.End) of the master at
// native size to RGB, m%05d.png in dir; window times count from the
// master's first frame, where ffmpeg's output timeline starts. Output frame
// k shows the latest master frame before the next output tick, w.Start +
// (k+1)·1000/fps, so a variable frame rate master is resampled to constant
// fps by its timestamps. The last master frame is held to w.End: simctl
// writes no frame while the screen is still, so a clip's static tail has
// none to decode. The master is read as BT.709 YCbCr in its tagged range,
// its primaries converted to BT.709's when c tags others.
func ExtractFrames(ctx context.Context, master string, c Colour, w Window, fps int, dir string) ([]string, error) {
	n := w.Frames(fps)
	if n <= 0 {
		return nil, errEmptyWindow
	}
	vf := fmt.Sprintf("%stpad=stop_mode=clone:stop_duration=%s,fps=%d:round=down,%s", toBT709(c), seconds(w.End), fps, toRGB)
	return decode(ctx, master, vf, []string{"-fps_mode", "cfr", "-ss", seconds(w.Start), "-frames:v", strconv.Itoa(n)},
		n, "master ends before the trim window", "m", dir)
}

// ExtractEncoded decodes frames indices (ascending) of an encoded clip to
// RGB, e%05d.png in dir, in the order given.
func ExtractEncoded(ctx context.Context, file string, indices []int, dir string) ([]*image.RGBA64, error) {
	terms := make([]string, len(indices))
	for i, k := range indices {
		terms[i] = fmt.Sprintf(`eq(n\,%d)`, k)
	}
	vf := "select=" + strings.Join(terms, "+") + "," + toRGB
	paths, err := decode(ctx, file, vf, []string{"-fps_mode", "passthrough"}, len(indices), file, "e", dir)
	if err != nil {
		return nil, err
	}
	out := make([]*image.RGBA64, len(indices))
	for i := range out {
		if out[i], err = loadRGBA64(paths[i]); err != nil {
			return nil, err
		}
	}
	return out, nil
}
