package clip

import (
	"errors"
	"fmt"
	"image"
	"math"

	"github.com/lockyc/plate/internal/diff"
	"github.com/lockyc/plate/internal/raster"
)

// The render's checks: the least Sharpness an encode may score, and the
// most LoopSeam, percent, a clip may show where it loops. MaxSeam is 1.5
// times the median Seam between consecutive frames of the
// clip-loop-crossfade quality case's body, a square sliding 300 px/s: a
// loop's cut may jump no more than a frame and a half of steady motion.
const (
	MinSharpness = 0.80
	MaxSeam      = 0.201
)

// sharpSigma is the detail scale Sharpness scores, px: at 3, 4:2:0's loss
// of a stroke's 1-px colour fringe weighs less than at 1, where lossless
// 4:2:0 alone scored magenta text 0.48, while a 1-px coloured line's smear
// still scores far below MinSharpness.
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
// measured on: the first, every 30th, and the last.
func SampleFrames(n int) []int {
	var out []int
	for k := 0; k < n; k += 30 {
		out = append(out, k)
	}
	if n > 0 && out[len(out)-1] != n-1 {
		out = append(out, n-1)
	}
	return out
}

// Sharpness is how much of the reference frames' fine detail the encoded
// frames keep: the least raster.DetailMin(got[i], refs[i], sharpSigma) over
// the SampleFrames pairs given. A pair with no fine detail is skipped, so 1
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
