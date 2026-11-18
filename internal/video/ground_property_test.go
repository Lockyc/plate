package video

import (
	"image"
	"image/color"
	"math"
	"math/rand/v2"
	"testing"

	"pgregory.net/rapid"
)

// TestGroundIsTheMajorityForAnyFrame: a frame whose majority is one colour
// jittered a few levels per channel, wherever that colour sits against the
// 8-level bin edges, beside a smaller card at least three bins off on some
// channel, has that colour for its ground, within groundReach.
func TestGroundIsTheMajorityForAnyFrame(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		jitter := rapid.IntRange(0, 3).Draw(rt, "jitter")
		var base, card [3]int
		far := false
		for c := range 3 {
			base[c] = rapid.IntRange(jitter, 255-jitter).Draw(rt, "base")
			card[c] = base[c]
			off := rapid.IntRange(0, 100).Draw(rt, "card offset")
			if off >= 24+jitter {
				far = true
			}
			if base[c]+off <= 255 {
				card[c] += off
			} else {
				card[c] -= off
			}
		}
		if !far {
			rt.Skip("the card is within the ground's neighbourhood")
		}
		const w, h = 40, 40
		share := rapid.Float64Range(0.55, 0.9).Draw(rt, "ground share")
		rnd := rand.New(rand.NewPCG(rapid.Uint64().Draw(rt, "seed"), 0))
		img := image.NewRGBA64(image.Rect(0, 0, w, h))
		level := func(v int) uint16 { return uint16(v) * 257 }
		for i, p := range rnd.Perm(w * h) {
			c := color.RGBA64{level(card[0]), level(card[1]), level(card[2]), 0xffff}
			if float64(i) < share*w*h {
				var v [3]int
				for ch := range 3 {
					v[ch] = base[ch] + rnd.IntN(2*jitter+1) - jitter
				}
				c = color.RGBA64{level(v[0]), level(v[1]), level(v[2]), 0xffff}
			}
			img.SetRGBA64(p%w, p/w, c)
		}
		g := ground(img)
		for ch, got := range []uint16{g.R, g.G, g.B} {
			if math.Abs(float64(got)/257-float64(base[ch])) > groundReach {
				rt.Fatalf("ground %v, want within %d levels of %v (card %v, jitter %d, share %.2f)", g, groundReach, base, card, jitter, share)
			}
		}
	})
}
