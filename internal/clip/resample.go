package clip

import (
	"image"
	"math"
)

// Kernel names the filter Resample applies, so a measurement can name the
// one it compares against. The clip-text-zoomed cases measure it against a
// native render at zoom 0.5 and 0.75, and the kernels split the zooms:
// Mitchell and linear-light Catmull-Rom win at 0.5 and fall behind at 0.75,
// Lanczos-3 beats Catmull-Rom at 0.75 (0.8018 against 0.7989), trails it
// at 0.5 and ties it at zoom 1, and Mitchell, which does not interpolate,
// softens a zoom-1 clip.
const Kernel = "catrom"

// catrom is the Catmull-Rom cubic (B = 0, C = 0.5): exact at integer
// offsets, so an unscaled, unshifted copy is lossless.
func catrom(x float64) float64 {
	x = math.Abs(x)
	switch {
	case x < 1:
		return (1.5*x-2.5)*x*x + 1
	case x < 2:
		return ((-0.5*x+2.5)*x-4)*x + 2
	}
	return 0
}

type kernelTap struct {
	j int
	w float64
}

// weights are the taps for n output samples spanning [start, start+length)
// of the source, whose pixel j covers [j, j+1). Downscaling widens the
// kernel by 1/zoom so every source pixel contributes; a tap past [lo, hi)
// reads the edge pixel.
func weights(n int, start, length float64, lo, hi int) [][]kernelTap {
	f := math.Min(1, float64(n)/length)
	support := 2 / f
	out := make([][]kernelTap, n)
	for i := range out {
		c := start + (float64(i)+0.5)*length/float64(n)
		var ts []kernelTap
		sum := 0.0
		for j := int(math.Floor(c - 0.5 - support)); j <= int(math.Ceil(c-0.5+support)); j++ {
			w := catrom((float64(j) + 0.5 - c) * f)
			if w == 0 {
				continue
			}
			k := max(lo, min(j, hi-1))
			if len(ts) > 0 && ts[len(ts)-1].j == k {
				ts[len(ts)-1].w += w
			} else {
				ts = append(ts, kernelTap{k, w})
			}
			sum += w
		}
		for t := range ts {
			ts[t].w /= sum
		}
		out[i] = ts
	}
	return out
}

func clamp16(v float64) uint16 { return uint16(math.Max(0, math.Min(0xffff, math.Round(v)))) }

// Resample draws the src rect sr (px, fractional) into dst's dr: separable
// Catmull-Rom on the stored (sRGB, premultiplied) values, horizontal pass
// first into 16-bit rows. Taps clamp to sr's pixels, so a strip cut from
// the source never takes in what lies outside it.
func Resample(dst *image.RGBA64, dr image.Rectangle, src *image.RGBA64, sr Rect) {
	cl := image.Rect(int(math.Floor(sr.X)), int(math.Floor(sr.Y)),
		int(math.Ceil(sr.X+sr.W)), int(math.Ceil(sr.Y+sr.H))).Intersect(src.Bounds())
	out := dr.Intersect(dst.Bounds())
	if cl.Empty() || out.Empty() || sr.W <= 0 || sr.H <= 0 {
		return
	}
	xw := weights(dr.Dx(), sr.X, sr.W, cl.Min.X, cl.Max.X)[out.Min.X-dr.Min.X : out.Max.X-dr.Min.X]
	yw := weights(dr.Dy(), sr.Y, sr.H, cl.Min.Y, cl.Max.Y)[out.Min.Y-dr.Min.Y : out.Max.Y-dr.Min.Y]
	r0, r1 := yw[0][0].j, yw[len(yw)-1][len(yw[len(yw)-1])-1].j
	dw := len(xw)
	rows := make([]uint16, (r1-r0+1)*dw*4)
	for r := r0; r <= r1; r++ {
		line := src.Pix[(r-src.Rect.Min.Y)*src.Stride:]
		row := rows[(r-r0)*dw*4:]
		for i, ts := range xw {
			var acc [4]float64
			for _, t := range ts {
				p := line[(t.j-src.Rect.Min.X)*8:]
				for c := range 4 {
					acc[c] += t.w * float64(uint16(p[2*c])<<8|uint16(p[2*c+1]))
				}
			}
			for c := range 4 {
				row[i*4+c] = clamp16(acc[c])
			}
		}
	}
	for y, ts := range yw {
		line := dst.Pix[(out.Min.Y+y-dst.Rect.Min.Y)*dst.Stride+(out.Min.X-dst.Rect.Min.X)*8:]
		for i := range dw {
			var acc [4]float64
			for _, t := range ts {
				row := rows[(t.j-r0)*dw*4+i*4:]
				for c := range 4 {
					acc[c] += t.w * float64(row[c])
				}
			}
			a := clamp16(acc[3])
			for c := range 4 {
				v := clamp16(acc[c])
				if c < 3 {
					v = min(v, a) // overshoot must not leave a colour past its alpha
				} else {
					v = a
				}
				line[i*8+2*c], line[i*8+2*c+1] = uint8(v>>8), uint8(v)
			}
		}
	}
}
