package video

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/lockyc/plate/internal/diff"
	"github.com/lockyc/plate/internal/engine"
	"github.com/lockyc/plate/internal/raster"
)

// The render's checks: the least Sharpness an encode may score, and the
// most LoopSeam, percent, a clip may show where it loops. MinSharpness is
// the least Sharpness the mp4's settings score on the neon text of
// TestSharpnessPassesNeonText (cyan, 0.857) rounded down; an encode that
// smears colour past 4:2:0 (chroma-qp-offset +12, or 4:1:0) scores under
// 0.68 on three of its four colours. MaxSeam is 1.5 times the median Seam
// (0.1339) between consecutive frames of the clip-loop-crossfade quality
// case's body, a square sliding 300 px/s, crossfade excluded, rounded up:
// a loop's cut may jump no more than a frame and a half of steady motion.
// It is also where closeLoop starts to close the loop, so a loop is either
// clean enough to pass or closed: a crossfade leaves a smaller cut, and a
// dip none, its two seam frames both the ground.
const (
	MinSharpness = 0.85
	MaxSeam      = 0.201
)

// sharpSigma is the detail scale Sharpness scores, px. Of 1, 2 and 3 on the
// neon text, 3 scores the real mp4 and webm encodes highest (least 0.857,
// against 0.826 at 1 and 0.850 at 2) and sets them furthest above the
// smears it catches (0.185 over the worst of them, against 0.072 and
// 0.147). At none does a magenta smear (4:1:0 0.864, chroma-qp-offset +12
// 0.899) fall below every real encode: magenta keeps its strokes through
// either smear.
const sharpSigma = 3

// Seam is the percent of pixels that differ between a and b, plate diff's
// measure at tolerance 28 with no shift search: how visible the cut is
// where the clip loops from b back to a.
func Seam(a, b *image.RGBA64) (float64, error) {
	if a.Bounds().Size() != b.Bounds().Size() {
		return 0, fmt.Errorf("seam: frames differ in size: %v and %v", a.Bounds().Size(), b.Bounds().Size())
	}
	res, _, _, err := diff.Compare(a, b, 28, 0, 1)
	if err != nil {
		return 0, err
	}
	return res.Percent, nil
}

// SampleFrames are the frames of an n-frame clip that Sharpness is
// measured on: the first, every 30th after it, and the last of the frames
// the loop close lc left as composed. A crossfaded or dipped frame's detail
// is faded by design, and an encoder keeps faint detail worst, so scoring
// one measures the fade, not the encode.
func SampleFrames(n int, lc LoopClose) []int {
	lo, hi := lc.Head, n-1-lc.Tail
	if hi < lo {
		lo, hi = 0, n-1
	}
	var out []int
	for k := lo; k <= hi; k += 30 {
		out = append(out, k)
	}
	if len(out) > 0 && out[len(out)-1] != hi {
		out = append(out, hi)
	}
	return out
}

// Sharpness is how much of the reference frames' fine detail the encoded
// frames keep: the least raster.DetailMin(got[i], refs[i], sharpSigma) over
// the SampleFrames pairs given. The render's reference is Lossless420 of
// its composed frames, so the score is the encoder's loss alone, never
// what 4:2:0, the format the clip must ship in, loses before it (red beside
// a saturated cyan edge, say). A pair with no fine detail is skipped, so 1
// means none had any to lose. Pairs that differ in size or count are an
// error: a bad decode, not a soft encode.
func Sharpness(refs, got []*image.RGBA64) (float64, error) {
	if len(refs) != len(got) {
		return 0, fmt.Errorf("sharpness: %d reference frames, %d encoded", len(refs), len(got))
	}
	least := 1.0
	for i := range refs {
		v, err := raster.DetailMin(got[i], refs[i], sharpSigma)
		if errors.Is(err, raster.ErrNoDetail) {
			continue
		}
		if err != nil {
			return 0, fmt.Errorf("sharpness: frame pair %d: %w", i, err)
		}
		least = math.Min(least, v)
	}
	return least, nil
}

// LoopSeam is Seam between the composed clip's last frame and its first,
// where it loops.
func LoopSeam(refs []string) (float64, error) {
	first, err := LoadRGBA64(refs[0])
	if err != nil {
		return 0, err
	}
	last, err := LoadRGBA64(refs[len(refs)-1])
	if err != nil {
		return 0, err
	}
	return Seam(first, last)
}

// sharpness is Sharpness of the encoded file against the composed frames'
// lossless 4:2:0 copy (Lossless420) at SampleFrames.
func sharpness(ctx context.Context, file string, refs []string, lc LoopClose, dir string) (float64, error) {
	idx := SampleFrames(len(refs), lc)
	got, err := ExtractEncoded(ctx, file, idx, dir)
	if err != nil {
		return 0, err
	}
	want, err := Lossless420(ctx, filepath.Dir(refs[0]), idx, dir)
	if err != nil {
		return 0, err
	}
	return Sharpness(want, got)
}

// sharpFormats are the encodes Sharpness scores: the video ones, whose
// encoders lose fine detail. GIF and WebP are palette and still formats
// with their own losses, unscored.
var sharpFormats = []string{"mp4", "webm"}

// ScoreSharpness scores every video encode in meas, found in tmp, against
// the composed frames refs, and sets meas.Sharpness to the least, or 1 with
// none. work makes the named scratch directory a score decodes into.
func ScoreSharpness(ctx context.Context, meas *Measurement, tmp string, refs []string, lc LoopClose, work func(string) (string, error)) error {
	return scoreSharpness(meas, func(file string) (float64, error) {
		decDir, err := work("decoded-" + file)
		if err != nil {
			return 0, err
		}
		return sharpness(ctx, filepath.Join(tmp, file), refs, lc, decDir)
	})
}

// scoreSharpness scores every video encode in meas with score (given its
// file name), and sets meas.Sharpness to the least, or 1 with none.
func scoreSharpness(meas *Measurement, score func(file string) (float64, error)) error {
	meas.Sharpness = 1
	for _, f := range sharpFormats {
		r, ok := meas.Formats[f]
		if !ok {
			continue
		}
		v, err := score(r.File)
		if err != nil {
			return fmt.Errorf("%s: %w", f, err)
		}
		r.Sharpness = v
		meas.Formats[f] = r
		meas.Sharpness = math.Min(meas.Sharpness, v)
	}
	return nil
}

// checkSharpness holds each video encode to MinSharpness.
func checkSharpness(meas Measurement) error {
	for _, f := range sharpFormats {
		if r, ok := meas.Formats[f]; ok && r.Sharpness < MinSharpness {
			return fmt.Errorf("%s sharpness %.3f is below %.2f: the encode loses the frames' fine detail", f, r.Sharpness, MinSharpness)
		}
	}
	return nil
}

// Checks are the limits Check holds a render to: BudgetKB the mp4's size
// in KB (0 for none), Seam whether MaxSeam applies, for a video that loops.
type Checks struct {
	BudgetKB int
	Seam     bool
}

// Check holds the encodes to the floor and the measures to their limits:
// the mp4 as ffprobe reads it, c.BudgetKB, MinSharpness and, with c.Seam,
// MaxSeam. tmp is where the encodes are.
func Check(ctx context.Context, meas Measurement, tmp string, c Checks) error {
	if r, ok := meas.Formats["mp4"]; ok {
		if err := checkMP4(ctx, filepath.Join(tmp, r.File), meas.Frames, meas.FPS); err != nil {
			return err
		}
		if kb := (r.Bytes + 1023) / 1024; c.BudgetKB > 0 && kb > int64(c.BudgetKB) {
			return fmt.Errorf("mp4 is %d KB, above budget_kb %d", kb, c.BudgetKB)
		}
	}
	if err := checkSharpness(meas); err != nil {
		return err
	}
	if c.Seam && meas.LoopSeam > MaxSeam {
		return fmt.Errorf("loop seam %.3g%% is above %.3g%%: the loop shows a cut", meas.LoopSeam, MaxSeam)
	}
	return nil
}

// checkMP4 reads the mp4 back with ffprobe: one H.264 4:2:0 stream of
// frames frames at a constant fps, and no audio.
func checkMP4(ctx context.Context, file string, frames, fps int) error {
	res, err := engine.Run(ctx, engine.Cmd{Engine: "ffprobe", Inputs: []string{file}, Args: []string{"-v", "error", "-count_frames",
		"-show_entries", "stream=codec_type,codec_name,pix_fmt,nb_read_frames,avg_frame_rate,r_frame_rate", "-of", "json", file}})
	if err != nil {
		return err
	}
	var out struct {
		Streams []struct {
			Type    string `json:"codec_type"`
			Codec   string `json:"codec_name"`
			PixFmt  string `json:"pix_fmt"`
			Frames  string `json:"nb_read_frames"`
			AvgRate string `json:"avg_frame_rate"`
			Rate    string `json:"r_frame_rate"`
		}
	}
	if err := json.Unmarshal(res.Stdout, &out); err != nil {
		return fmt.Errorf("ffprobe: unreadable output: %w", err)
	}
	var video int
	for _, st := range out.Streams {
		if st.Type == "audio" {
			return errors.New("mp4 has an audio stream")
		}
		if st.Type != "video" {
			continue
		}
		video++
		rate := strconv.Itoa(fps) + "/1"
		switch {
		case st.Codec != "h264":
			return fmt.Errorf("mp4 codec is %s, not h264", st.Codec)
		case st.PixFmt != "yuv420p":
			return fmt.Errorf("mp4 pix_fmt is %s, not yuv420p", st.PixFmt)
		case st.Frames != strconv.Itoa(frames):
			return fmt.Errorf("mp4 has %s frames, not %d", st.Frames, frames)
		case st.AvgRate != rate || st.Rate != rate:
			return fmt.Errorf("mp4 frame rate is %s (average %s), not %s", st.Rate, st.AvgRate, rate)
		}
	}
	if video != 1 {
		return fmt.Errorf("mp4 has %d video streams, not 1", video)
	}
	return nil
}

// toViewerRGB decodes an encode the way Chromium draws it: 4:2:0 chroma
// interpolated bilinearly at every pixel. Chromium's frame matches it to
// RMSE 0.0001, and swscale's default bicubic chroma to only 0.03, so a
// measure on the default decode scores colour fringes no viewer sees.
const toViewerRGB = "scale=in_color_matrix=bt709:in_range=auto:flags=accurate_rnd+full_chroma_int+bilinear,format=rgb24"

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

// ExtractEncoded decodes frames indices (ascending) of an encoded clip to
// RGB as a viewer sees them (toViewerRGB), e%05d.png in dir, in the order
// given.
func ExtractEncoded(ctx context.Context, file string, indices []int, dir string) ([]*image.RGBA64, error) {
	return extractSelected(ctx, file, "", indices, "e", dir)
}

// Lossless420 is frames indices (ascending) of the composed frames, the
// r%05d.png sequence in refDir, through the encodes' own 4:2:0 conversion
// (toYUV) and back as ExtractEncoded reads an encode, l%05d.png in dir:
// pixel for pixel what a lossless encode decodes to. It is Sharpness's
// reference, so Sharpness scores what the encoder loses and not what 4:2:0,
// the format the clip must ship in, loses before any encoder runs.
func Lossless420(ctx context.Context, refDir string, indices []int, dir string) ([]*image.RGBA64, error) {
	return extractSelected(ctx, filepath.Join(refDir, "r%05d.png"), toYUV+",", indices, "l", dir)
}

// extractSelected decodes frames indices of src through pre and then
// toViewerRGB, prefix%05d.png in dir.
func extractSelected(ctx context.Context, src, pre string, indices []int, prefix, dir string) ([]*image.RGBA64, error) {
	terms := make([]string, len(indices))
	for i, k := range indices {
		terms[i] = fmt.Sprintf(`eq(n\,%d)`, k)
	}
	vf := "select=" + strings.Join(terms, "+") + "," + pre + toViewerRGB
	paths, err := Decode(ctx, src, vf, []string{"-fps_mode", "passthrough"}, len(indices), src, prefix, dir)
	if err != nil {
		return nil, err
	}
	out := make([]*image.RGBA64, len(indices))
	for i := range out {
		if out[i], err = LoadRGBA64(paths[i]); err != nil {
			return nil, err
		}
	}
	return out, nil
}
