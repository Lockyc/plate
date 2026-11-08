// Package frames decodes a clip to PNG frames with ffmpeg: the one decoder
// the video measures, clip extraction and diff's clip comparison share.
package frames

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/lockyc/plate/internal/engine"
)

// ViewerRGB decodes an encode the way Chromium draws it: 4:2:0 chroma
// interpolated bilinearly at every pixel. Chromium's frame matches it to
// RMSE 0.0001, and swscale's default bicubic chroma to only 0.03, so a
// measure on the default decode scores colour fringes no viewer sees.
const ViewerRGB = "scale=in_color_matrix=bt709:in_range=auto:flags=accurate_rnd+full_chroma_int+bilinear,format=rgb24"

// Decode has ffmpeg write src through vf and then outArgs as
// prefix%05d.png. It decodes into a temporary directory and moves the
// frames into dir only once there are want of them (any number when want
// is 0), so dir never holds a partial set; short names what a short decode
// means.
func Decode(ctx context.Context, src, vf string, outArgs []string, want int, short, prefix, dir string) ([]string, error) {
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

// All is every frame of src as a viewer sees it (ViewerRGB), one per
// encoded frame, prefix%05d.png in dir.
func All(ctx context.Context, src, prefix, dir string) ([]string, error) {
	return Decode(ctx, src, ViewerRGB, []string{"-fps_mode", "passthrough"}, 0, src, prefix, dir)
}
