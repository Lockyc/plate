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
// most LoopSeam a clip may show where it loops.
const (
	MinSharpness = 0.90
	MaxSeam      = 1.0
)

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
// frames keep: the least raster.DetailMin(got[i], refs[i], 1) over the
// SampleFrames pairs given. A pair with no fine detail is skipped, so 1
// means none had any to lose; a pair that differs in size scores 0.
func Sharpness(refs, got []*image.RGBA64) float64 {
	least := 1.0
	for i := range min(len(refs), len(got)) {
		v, err := raster.DetailMin(got[i], refs[i], 1)
		if errors.Is(err, raster.ErrNoDetail) {
			continue
		}
		if err != nil {
			return 0
		}
		least = math.Min(least, v)
	}
	return least
}
