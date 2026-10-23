package video

import (
	"image"
	"testing"
)

func TestGroundIsTheDominantColour(t *testing.T) {
	// 45% ground with a few pixels a level off, under two paler greys: a
	// per-channel median lands on the 200 grey, not the ground.
	img := solid(20, 20, grey(250.0/255))
	fill(img, image.Rect(0, 0, 20, 9), grey(10.0/255))
	for x := 0; x < 20; x += 2 {
		img.SetRGBA64(x, 0, grey(11.0/255))
	}
	fill(img, image.Rect(0, 9, 20, 15), grey(200.0/255))
	if c := ground(img); !nearRGB(c, grey(10.0/255), 1) {
		t.Fatalf("ground = %v, want the 10 grey", c)
	}
}

func TestGroundStraddlingABinEdge(t *testing.T) {
	// 60% ground alternating either side of a multiple of 8, a flat card at
	// 35% ten levels paler, 5% white: each half of the ground is outnumbered
	// by the card, and the card is near enough to sit in the ground's
	// neighbourhood but must not be averaged into it.
	for _, c := range []struct{ lo, hi, card float64 }{{15, 16, 26}, {31, 32, 42}} {
		img := solid(20, 20, grey(c.card/255))
		for y := range 12 {
			for x := range 20 {
				v := c.lo
				if (x+y)%2 == 1 {
					v = c.hi
				}
				img.SetRGBA64(x, y, grey(v/255))
			}
		}
		fill(img, image.Rect(0, 19, 20, 20), grey(1))
		if g := ground(img); !nearRGB(g, grey((c.lo+c.hi)/2/255), 0.6) {
			t.Errorf("%g/%g ground beside a %g card = %v, want %g", c.lo, c.hi, c.card, g, (c.lo+c.hi)/2)
		}
	}
}
