package cmm

import "math"

// DE2000 is the CIEDE2000 colour difference between two Lab colours, with
// the parametric weights kL, kC and kH all 1.
func DE2000(a, b [3]float64) float64 {
	const pow25to7 = 6103515625 // 25^7
	deg := math.Pi / 180
	c1 := math.Hypot(a[1], a[2])
	c2 := math.Hypot(b[1], b[2])
	cBar7 := math.Pow((c1+c2)/2, 7)
	g := 0.5 * (1 - math.Sqrt(cBar7/(cBar7+pow25to7)))
	a1, a2 := (1+g)*a[1], (1+g)*b[1]
	c1p, c2p := math.Hypot(a1, a[2]), math.Hypot(a2, b[2])
	hue := func(ap, bp float64) float64 {
		if ap == 0 && bp == 0 {
			return 0
		}
		h := math.Atan2(bp, ap) / deg
		if h < 0 {
			h += 360
		}
		return h
	}
	h1, h2 := hue(a1, a[2]), hue(a2, b[2])

	dL := b[0] - a[0]
	dC := c2p - c1p
	var dh float64
	if c1p*c2p != 0 {
		dh = h2 - h1
		if dh > 180 {
			dh -= 360
		} else if dh < -180 {
			dh += 360
		}
	}
	dH := 2 * math.Sqrt(c1p*c2p) * math.Sin(dh/2*deg)

	lBar := (a[0] + b[0]) / 2
	cBar := (c1p + c2p) / 2
	hBar := h1 + h2
	if c1p*c2p != 0 {
		switch {
		case math.Abs(h1-h2) <= 180:
			hBar /= 2
		case h1+h2 < 360:
			hBar = (hBar + 360) / 2
		default:
			hBar = (hBar - 360) / 2
		}
	}
	t := 1 - 0.17*math.Cos((hBar-30)*deg) + 0.24*math.Cos(2*hBar*deg) +
		0.32*math.Cos((3*hBar+6)*deg) - 0.20*math.Cos((4*hBar-63)*deg)
	dTheta := 30 * math.Exp(-math.Pow((hBar-275)/25, 2))
	cBarP7 := math.Pow(cBar, 7)
	rc := 2 * math.Sqrt(cBarP7/(cBarP7+pow25to7))
	l50 := (lBar - 50) * (lBar - 50)
	sl := 1 + 0.015*l50/math.Sqrt(20+l50)
	sc := 1 + 0.045*cBar
	sh := 1 + 0.015*cBar*t
	rt := -math.Sin(2*dTheta*deg) * rc
	return math.Sqrt((dL/sl)*(dL/sl) + (dC/sc)*(dC/sc) + (dH/sh)*(dH/sh) + rt*(dC/sc)*(dH/sh))
}
