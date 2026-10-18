package clip

import (
	"fmt"
	"image"

	"github.com/lockyc/plate/internal/diff"
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
