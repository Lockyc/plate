package clip

import (
	"context"
	"errors"
	"fmt"
	"image"
	"image/draw"
	"math"
	"strconv"

	"github.com/lockyc/plate/internal/video"
)

// errEmptyWindow refuses a trim window with no frame in it.
var errEmptyWindow = errors.New("the trim window is empty: nothing to render")

// toRGB is the tail of every master decode: BT.709 YCbCr in its tagged
// range to 8-bit RGB.
const toRGB = "scale=in_color_matrix=bt709:in_range=auto,format=rgb24"

// seconds formats a master time in ms as ffmpeg seconds, to the µs.
func seconds(ms float64) string {
	return strconv.FormatFloat(math.Round(ms*1000)/1e6, 'f', -1, 64)
}

// ExtractActivity decodes the whole master small and grey, a%05d.png in
// dir, for motion detection: frame i sits at i·1000/fps ms from the
// master's first frame, where ffmpeg's output timeline starts.
func ExtractActivity(ctx context.Context, master string, fps int, dir string) ([]*image.Gray, error) {
	paths, err := video.Decode(ctx, master, fmt.Sprintf("fps=%d:round=down,scale=200:-2,format=gray", fps),
		[]string{"-fps_mode", "cfr"}, 0, master, "a", dir)
	if err != nil {
		return nil, err
	}
	out := make([]*image.Gray, len(paths))
	for i, p := range paths {
		img, err := video.LoadImage(p)
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
// none to decode. The hold pads by excess, w.End past the master's last
// frame rather than past its probed end, and -frames:v cuts it, so a short
// master is never refused here; CheckMaster refuses one cut short. The
// master is read as BT.709 YCbCr in its tagged range, its primaries
// converted to BT.709's when c tags others.
func ExtractFrames(ctx context.Context, master string, c Colour, w Window, fps int, dir string) ([]string, error) {
	n := w.Frames(fps)
	if n <= 0 {
		return nil, errEmptyWindow
	}
	vf := fmt.Sprintf("%stpad=stop_mode=clone:stop_duration=%s,fps=%d:round=down,%s", toBT709(c), seconds(w.End), fps, toRGB)
	return video.Decode(ctx, master, vf, []string{"-fps_mode", "cfr", "-ss", seconds(w.Start), "-frames:v", strconv.Itoa(n)},
		n, master, "m", dir)
}
