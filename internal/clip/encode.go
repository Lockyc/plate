package clip

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"github.com/lockyc/plate/internal/atomicfile"
	"github.com/lockyc/plate/internal/engine"
	"github.com/lockyc/plate/internal/pins"
)

// The lossy encoders' quality, measured by the clip-text-sharp-* and
// clip-text-scroll-* quality cases: thin text in four saturated accent
// colours on a dark UI ground, still and scrolling. mp4CRF, with
// mp4Params' chroma QP offset, writes the bytes a plain crf 18 did on both
// pages and keeps more of the text's colour at them (scrolling: 0.907
// rgb-min against 0.903). The webm is AV1: at webmCRF it keeps more detail
// than VP9 at crf 32 on both pages, at under half VP9's bytes scrolling.
const (
	mp4CRF    = 22
	mp4Params = "chroma-qp-offset=-6"
	webmCRF   = 32
)

// bt709 tags an encode BT.709 matrix and primaries in limited range with
// the sRGB transfer, and toYUV writes the composed RGB frames as 4:2:0
// under that matrix and range, its chroma downsampled by Lanczos, which of
// swscale's filters keeps the most of a coloured stroke as Chromium
// upsamples it (clip-text-sharp-mp4: 0.910 rgb-min against bicubic's 0.907
// and area's 0.905). The frames are sRGB, and so is the tag: WebKit applies
// a BT.709 transfer tag, drawing a #0a0a12 ground as #080812 beside the
// same CSS colour, and draws the sRGB tag as #0a0a13; Chromium draws both
// alike. toYUV tags the frames too: an encoder takes the filtered frames'
// tags over its own options, and frames from RGB PNGs carry none.
var (
	bt709 = []string{"-colorspace", "bt709", "-color_primaries", "bt709", "-color_trc", "iec61966-2-1", "-color_range", "tv"}
	toYUV = "scale=out_color_matrix=bt709:out_range=tv:flags=accurate_rnd+full_chroma_int+lanczos,format=yuv420p," +
		"setparams=color_primaries=bt709:color_trc=iec61966-2-1:colorspace=bt709:range=tv"
)

// Encode writes the composed frames r%05d.png in refsDir, w×h at fps, as
// format to out. mp4 is the compatibility floor every web view and
// Electron autoplays muted and inline: H.264 4:2:0, BT.709, faststart, no
// audio, constant fps, even sides. GIF is written at 50 fps because its
// delays are centiseconds. The poster is the first frame's bytes, so it
// equals the video's start.
func Encode(ctx context.Context, format, refsDir string, fps, w, h int, out string) error {
	if (format == "mp4" || format == "webm") && (w%2 != 0 || h%2 != 0) {
		return fmt.Errorf("%s needs even sides, not %dx%d", format, w, h)
	}
	first := filepath.Join(refsDir, "r00000.png")
	if format == "poster" {
		b, err := os.ReadFile(first)
		if err != nil {
			return err
		}
		return atomicfile.WriteFile(out, b, 0o644)
	}
	if format == "webp" {
		frames, err := filepath.Glob(filepath.Join(refsDir, "r*.png"))
		if err != nil {
			return err
		}
		args := append([]string{"-delay", "1x" + strconv.Itoa(fps), "-loop", "0"}, frames...)
		args = append(args, "-define", "webp:lossless=false", "-quality", "90", "WEBP:"+out)
		_, err = engine.Run(ctx, engine.Cmd{Engine: "magick", Args: args, Inputs: frames, Outputs: []string{out}})
		return err
	}
	r := strconv.Itoa(fps)
	in := []string{"-hide_banner", "-loglevel", "error", "-nostdin", "-framerate", r, "-start_number", "0",
		"-i", filepath.Join(refsDir, "r%05d.png")}
	var enc []string
	switch format {
	case "mp4":
		enc = append([]string{"-vf", toYUV, "-c:v", "libx264", "-preset", "slow", "-crf", strconv.Itoa(mp4CRF),
			"-x264-params", mp4Params, "-profile:v", "high", "-pix_fmt", "yuv420p"}, bt709...)
		enc = append(enc, "-movflags", "+faststart", "-an", "-fps_mode", "cfr", "-r", r)
	case "webm":
		enc = append([]string{"-vf", toYUV, "-c:v", "libsvtav1", "-preset", "4", "-crf", strconv.Itoa(webmCRF),
			"-pix_fmt", "yuv420p"}, bt709...)
		enc = append(enc, "-an")
	case "gif":
		enc = []string{"-vf", "fps=50,split[a][b];[a]palettegen=stats_mode=diff[p];[b][p]paletteuse=dither=bayer:bayer_scale=4",
			"-loop", "0"}
	default:
		return fmt.Errorf("unknown clip format %q", format)
	}
	args := append(append(in, enc...), out)
	_, err := engine.Run(ctx, engine.Cmd{Engine: "ffmpeg", Args: args, Inputs: []string{first}, Outputs: []string{out}})
	return err
}

// requireEncoders refuses, before any decoding, a render whose formats need
// an ffmpeg encoder this build lacks: each pinned ffmpeg capability whose
// For names a requested format.
func requireEncoders(ctx context.Context, formats []string) error {
	e, ok := pins.Lookup("ffmpeg")
	if !ok {
		return fmt.Errorf("ffmpeg is not in the pin table")
	}
	for _, c := range e.Capabilities {
		for _, f := range formats {
			if c.For != "clip "+f {
				continue
			}
			has, err := engine.Capable(ctx, e, c)
			if err != nil {
				return err
			}
			if !has {
				return fmt.Errorf("ffmpeg lacks encoder %s (%s)", c.Token, c.For)
			}
		}
	}
	return nil
}
