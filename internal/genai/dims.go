package genai

import (
	"math"
	"strconv"
	"strings"
)

// tierPixels is the pixel budget of each Request.Size for a provider that is
// told an explicit width and height: 1K is one megapixel, 2K four and 4K
// sixteen, before any cap of the model's own.
var tierPixels = map[string]float64{"1K": 1e6, "2K": 4e6, "4K": 16e6}

// parseAspect reads "W:H" into its two positive, finite sides.
func parseAspect(aspect string) (float64, float64, bool) {
	ws, hs, found := strings.Cut(aspect, ":")
	w, err1 := strconv.ParseFloat(ws, 64)
	h, err2 := strconv.ParseFloat(hs, 64)
	if !found || err1 != nil || err2 != nil || !finite(w) || !finite(h) || w <= 0 || h <= 0 {
		return 0, 0, false
	}
	return w, h, true
}

func finite(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }

// fitDims is the largest width and height at aspect aw:ah within px pixels
// and, when maxEdge is above 0, maxEdge on either side, each rounded down to
// a multiple of 16 so the area never passes the budget it was priced at.
func fitDims(px, aw, ah float64, maxEdge int) (int, int) {
	w := math.Sqrt(px * aw / ah)
	h := w * ah / aw
	if e := float64(maxEdge); e > 0 && math.Max(w, h) > e {
		s := e / math.Max(w, h)
		w, h = w*s, h*s
	}
	// The 1e-9 keeps a side scaled to exactly maxEdge from flooring a whole
	// step below it.
	return int(w+1e-9) / 16 * 16, int(h+1e-9) / 16 * 16
}
