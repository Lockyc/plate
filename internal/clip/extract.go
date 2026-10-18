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

	"github.com/lockyc/plate/internal/engine"
)

// seconds formats a master time in ms as ffmpeg seconds, to the µs.
func seconds(ms float64) string {
	return strconv.FormatFloat(math.Round(ms*1000)/1e6, 'f', -1, 64)
}

// decode has ffmpeg write the master through vf as prefix%05d.png, CFR.
// When frames > 0 it skips the first start ms of the output timeline and
// writes that many. The seek is on the output, after vf: an input seek
// drops the frame still on screen at start, and its time already counts
// from the master's first frame. It decodes into a temporary directory and
// moves the frames into dir only once there are as many as asked for, so
// dir never holds a partial set.
func decode(ctx context.Context, master string, start float64, vf string, frames int, prefix, dir string) ([]string, error) {
	tmp, err := os.MkdirTemp(dir, ".decode-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmp)
	args := []string{"-hide_banner", "-loglevel", "error", "-nostdin",
		"-i", master, "-vf", vf, "-fps_mode", "cfr"}
	if frames > 0 {
		args = append(args, "-ss", seconds(start), "-frames:v", strconv.Itoa(frames))
	}
	args = append(args, "-start_number", "0", filepath.Join(tmp, prefix+"%05d.png"))
	if _, err := engine.Run(ctx, engine.Cmd{Engine: "ffmpeg", Args: args, Inputs: []string{master}}); err != nil {
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
	if frames > 0 && len(names) < frames {
		return nil, fmt.Errorf("master ends before the trim window: decoded %d of %d frames", len(names), frames)
	}
	if len(names) == 0 {
		return nil, fmt.Errorf("%s: no frame decoded", master)
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
	paths, err := decode(ctx, master, 0, fmt.Sprintf("fps=%d:round=down,scale=200:-2,format=gray", fps), 0, "a", dir)
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

// ExtractFrames decodes the window [w.Start, w.End) of the master at
// native size to RGB, m%05d.png in dir; window times count from the
// master's first frame, where ffmpeg's output timeline starts. Output frame
// k shows the latest master frame before the next output tick, w.Start +
// (k+1)·1000/fps, so a variable frame rate master is resampled to constant
// fps by its timestamps. The master is read as BT.709 in its tagged range.
func ExtractFrames(ctx context.Context, master string, w Window, fps int, dir string) ([]string, error) {
	n := w.Frames(fps)
	if n <= 0 {
		return nil, errors.New("the trim window is empty: nothing to render")
	}
	vf := fmt.Sprintf("fps=%d:round=down,scale=in_color_matrix=bt709:in_range=auto,format=rgb24", fps)
	return decode(ctx, master, w.Start, vf, n, "m", dir)
}
