package video

import (
	"fmt"
	"image"
	"image/color"
	"math"
)

// Loops are the ways a render may close a loop (CloseLoop), the first the
// default: crossfade blends the end into the start; dip eases both toward
// the ground and back, for a video whose end state differs from its start,
// where a blend shows both at once.
var Loops = []string{"crossfade", "dip"}

// LoopMS is the loop close's length, ms.
const LoopMS = 300.0

// EaseInOutCubic maps 0..1 to 0..1, slow at both ends.
func EaseInOutCubic(x float64) float64 {
	if x < 0.5 {
		return 4 * x * x * x
	}
	return 1 - math.Pow(-2*x+2, 3)/2
}

func clamp16(v float64) uint16 { return uint16(math.Max(0, math.Min(0xffff, math.Round(v)))) }

// LoopClose is what CloseLoop altered: how many of the clip's first (Head)
// and last (Tail) frames. Both are 0 for a clip that loops as it is.
type LoopClose struct{ Head, Tail int }

// CloseLoop measures the seam from the last frame back to the first and,
// when it is above MaxSeam, the most a loop may show, closes it over
// LoopMS; at or under it the clip loops as it is.
//
// crossfade blends the last n frames toward the first, frame i of n at
// weight i/(n+1). dip eases the last n/2 frames toward the first frame's
// ground and the first n-n/2 back from it, both by EaseInOutCubic, so the
// two seam frames are the ground itself and no frame shows the end and the
// start at once.
func CloseLoop(refs []string, fps int, loop string) (LoopClose, error) {
	first, err := LoadRGBA64(refs[0])
	if err != nil {
		return LoopClose{}, err
	}
	last, err := LoadRGBA64(refs[len(refs)-1])
	if err != nil {
		return LoopClose{}, err
	}
	seam, err := Seam(first, last)
	if err != nil {
		return LoopClose{}, err
	}
	if seam <= MaxSeam {
		return LoopClose{}, nil
	}
	n := int(math.Round(LoopMS * float64(fps) / 1000))
	switch loop {
	case "crossfade":
		n = min(n, len(refs)-1)
		for i := 1; i <= n; i++ {
			err := mixInto(refs[len(refs)-n-1+i], float64(i)/float64(n+1), func(j int) uint16 {
				return uint16(first.Pix[j])<<8 | uint16(first.Pix[j+1])
			})
			if err != nil {
				return LoopClose{}, err
			}
		}
		return LoopClose{Tail: n}, nil
	case "dip":
		g := ground(first)
		at := [4]uint16{g.R, g.G, g.B, g.A}
		to := func(j int) uint16 { return at[j/2%4] }
		// One frame short of the clip, so frame Head, the poster, is as composed.
		n = max(2, min(n, len(refs)-1))
		lc := LoopClose{Head: n - n/2, Tail: n / 2}
		for i := 1; i <= lc.Tail; i++ {
			if err := mixInto(refs[len(refs)-lc.Tail-1+i], EaseInOutCubic(float64(i)/float64(lc.Tail)), to); err != nil {
				return LoopClose{}, err
			}
		}
		for i := range lc.Head {
			if err := mixInto(refs[i], EaseInOutCubic(1-float64(i)/float64(lc.Head)), to); err != nil {
				return LoopClose{}, err
			}
		}
		return lc, nil
	}
	return LoopClose{}, fmt.Errorf("compose: unknown loop %q (want one of %v)", loop, Loops)
}

// mixInto rewrites the frame at path as itself at weight 1-w over to's
// sample at w, to(j) giving the 16-bit sample for Pix[j:j+2].
func mixInto(path string, w float64, to func(j int) uint16) error {
	img, err := LoadRGBA64(path)
	if err != nil {
		return err
	}
	for j := 0; j < len(img.Pix); j += 2 {
		a := float64(uint16(img.Pix[j])<<8 | uint16(img.Pix[j+1]))
		v := clamp16(a*(1-w) + float64(to(j))*w)
		img.Pix[j], img.Pix[j+1] = uint8(v>>8), uint8(v)
	}
	return SavePNG(path, img)
}

// groundReach is how far, in 8-bit levels per channel, a pixel may sit from
// ground's first estimate and still count toward the ground.
const groundReach = 8

// ground is img's dominant colour, the one a UI's background gives most of
// its pixels. Each channel is cut to 8-level bins; the peak is the bin
// whose 3×3×3 neighbourhood holds the most pixels (then its own count), so
// a ground split across a bin edge still counts whole. The colour is the
// mean of the pixels within groundReach of that bin's mean, so a card in a
// neighbouring bin but further off is not averaged in. Unlike a per-channel
// median, it is a colour the frame shows.
func ground(img *image.RGBA64) color.RGBA64 {
	bin := func(j int) int {
		return int(img.Pix[j])>>3<<10 | int(img.Pix[j+2])>>3<<5 | int(img.Pix[j+4])>>3
	}
	var count [1 << 15]int
	var used []int
	for j := 0; j < len(img.Pix); j += 8 {
		b := bin(j)
		if count[b] == 0 {
			used = append(used, b)
		}
		count[b]++
	}
	near := func(b int) int {
		r, g, bl := b>>10, b>>5&31, b&31
		n := 0
		for dr := -1; dr <= 1; dr++ {
			for dg := -1; dg <= 1; dg++ {
				for db := -1; db <= 1; db++ {
					x, y, z := r+dr, g+dg, bl+db
					if x >= 0 && x < 32 && y >= 0 && y < 32 && z >= 0 && z < 32 {
						n += count[x<<10|y<<5|z]
					}
				}
			}
		}
		return n
	}
	top, topNear := -1, -1
	for _, b := range used {
		if n := near(b); n > topNear || n == topNear && count[b] > count[top] {
			top, topNear = b, n
		}
	}
	sample := func(j, c int) float64 { return float64(uint16(img.Pix[j+2*c])<<8 | uint16(img.Pix[j+2*c+1])) }
	mean := func(keep func(j int) bool) [3]float64 {
		var sum [3]float64
		n := 0.0
		for j := 0; j < len(img.Pix); j += 8 {
			if keep(j) {
				for c := range 3 {
					sum[c] += sample(j, c)
				}
				n++
			}
		}
		return [3]float64{sum[0] / n, sum[1] / n, sum[2] / n}
	}
	est := mean(func(j int) bool { return bin(j) == top })
	g := mean(func(j int) bool {
		for c := range 3 {
			if math.Abs(sample(j, c)-est[c]) > groundReach*257 {
				return false
			}
		}
		return true
	})
	return color.RGBA64{clamp16(g[0]), clamp16(g[1]), clamp16(g[2]), 0xffff}
}
