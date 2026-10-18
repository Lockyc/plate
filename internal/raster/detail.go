package raster

import (
	"errors"
	"fmt"
	"image"
	"math"
)

// Channel is what Detail measures: BT.709 luma, or one colour channel.
type Channel int

const (
	Luma Channel = iota
	Red
	Green
	Blue
)

// ErrNoDetail is Detail's answer when b has no fine detail in the channel,
// so there is nothing for a to reproduce.
var ErrNoDetail = errors.New("b has no fine detail to recover")

// Detail scores how much of b's fine detail a reproduces, where they line
// up: 1 - Σ(hf(a)-hf(b))² / Σhf(b)², over channel ch, with hf the image
// less its Gaussian blur at sigma px. 1 is every fine stroke back in place;
// 0 is a blur that lost them all; below 0 is invented detail that misses
// the original's. RMSE alone is dominated by tone, so a soft result and a
// sharp one with the same tone error read alike there. Luma alone misses
// chroma smear: 4:2:0 video keeps the luma of coloured text and blurs its
// colour, which only a colour channel shows, hence DetailMin.
func Detail(a, b *image.RGBA64, sigma float64, ch Channel) (float64, error) {
	miss, energy, err := detailParts(a, b, sigma, ch)
	if err != nil {
		return 0, err
	}
	if energy < 1e-12 {
		return 0, ErrNoDetail
	}
	return 1 - miss/energy, nil
}

// detailParts is Detail's Σ(hf(a)-hf(b))² and Σhf(b)².
func detailParts(a, b *image.RGBA64, sigma float64, ch Channel) (miss, energy float64, err error) {
	if a.Bounds().Size() != b.Bounds().Size() {
		return 0, 0, fmt.Errorf("a and b sizes differ")
	}
	if !(sigma > 0) {
		return 0, 0, fmt.Errorf("sigma: want a positive number")
	}
	ha, hb := highPass(a, sigma, ch), highPass(b, sigma, ch)
	for i := range hb {
		d := ha[i] - hb[i]
		miss += d * d
		energy += hb[i] * hb[i]
	}
	return miss, energy, nil
}

// weakChannel is the share of the strongest channel's fine-detail energy
// under which DetailMin leaves a channel out. Neon text on near black
// barely moves one channel (#39ff14's blue holds 0.01% of its green's
// energy), and that channel, normalised by its own tiny energy, scores
// codec noise thousands below zero.
const weakChannel = 0.05

// DetailMin is the least Detail of Red, Green and Blue, leaving out a
// channel in which b has under weakChannel of its strongest channel's fine
// detail; ErrNoDetail when b has none in any.
func DetailMin(a, b *image.RGBA64, sigma float64) (float64, error) {
	var miss, energy [3]float64
	strongest := 0.0
	for i, ch := range []Channel{Red, Green, Blue} {
		var err error
		if miss[i], energy[i], err = detailParts(a, b, sigma, ch); err != nil {
			return 0, err
		}
		strongest = math.Max(strongest, energy[i])
	}
	if strongest < 1e-12 {
		return 0, ErrNoDetail
	}
	least := math.Inf(1)
	for i := range energy {
		if energy[i] >= weakChannel*strongest {
			least = math.Min(least, 1-miss[i]/energy[i])
		}
	}
	return least, nil
}

// value is channel ch of img at (x, y), 0..1.
func value(img *image.RGBA64, x, y int, ch Channel) float64 {
	c := img.RGBA64At(x, y)
	switch ch {
	case Red:
		return float64(c.R) / 65535
	case Green:
		return float64(c.G) / 65535
	case Blue:
		return float64(c.B) / 65535
	}
	return (0.2126*float64(c.R) + 0.7152*float64(c.G) + 0.0722*float64(c.B)) / 65535
}

// highPass is channel ch of img less its separable Gaussian blur, edges
// clamped.
func highPass(img *image.RGBA64, sigma float64, ch Channel) []float64 {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	l := make([]float64, w*h)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			l[y*w+x] = value(img, b.Min.X+x, b.Min.Y+y, ch)
		}
	}
	r := int(math.Ceil(3 * sigma))
	k := make([]float64, 2*r+1)
	var ks float64
	for i := range k {
		d := float64(i - r)
		k[i] = math.Exp(-d * d / (2 * sigma * sigma))
		ks += k[i]
	}
	clamp := func(v, n int) int { return min(max(v, 0), n-1) }
	tmp, blur := make([]float64, w*h), make([]float64, w*h)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			var s float64
			for i, kv := range k {
				s += kv * l[y*w+clamp(x+i-r, w)]
			}
			tmp[y*w+x] = s / ks
		}
	}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			var s float64
			for i, kv := range k {
				s += kv * tmp[clamp(y+i-r, h)*w+x]
			}
			blur[y*w+x] = s / ks
		}
	}
	for i := range l {
		l[i] -= blur[i]
	}
	return l
}
