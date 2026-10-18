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
