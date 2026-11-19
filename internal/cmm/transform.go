package cmm

import (
	"image"
	"math"
	"runtime"
	"sync"

	"github.com/lockyc/plate/internal/raster"
)

// d50 is the PCS illuminant, ICC.1 7.2.16.
var d50 = [3]float64{0.9642, 1, 0.8249}

type mat3 [3][3]float64

func (m mat3) mul(n mat3) mat3 {
	var r mat3
	for i := range 3 {
		for j := range 3 {
			for k := range 3 {
				r[i][j] += m[i][k] * n[k][j]
			}
		}
	}
	return r
}

func (m mat3) apply(v [3]float64) [3]float64 {
	return [3]float64{
		m[0][0]*v[0] + m[0][1]*v[1] + m[0][2]*v[2],
		m[1][0]*v[0] + m[1][1]*v[1] + m[1][2]*v[2],
		m[2][0]*v[0] + m[2][1]*v[1] + m[2][2]*v[2],
	}
}

func (m mat3) inv() mat3 {
	a := m
	det := a[0][0]*(a[1][1]*a[2][2]-a[1][2]*a[2][1]) -
		a[0][1]*(a[1][0]*a[2][2]-a[1][2]*a[2][0]) +
		a[0][2]*(a[1][0]*a[2][1]-a[1][1]*a[2][0])
	return mat3{
		{(a[1][1]*a[2][2] - a[1][2]*a[2][1]) / det, (a[0][2]*a[2][1] - a[0][1]*a[2][2]) / det, (a[0][1]*a[1][2] - a[0][2]*a[1][1]) / det},
		{(a[1][2]*a[2][0] - a[1][0]*a[2][2]) / det, (a[0][0]*a[2][2] - a[0][2]*a[2][0]) / det, (a[0][2]*a[1][0] - a[0][0]*a[1][2]) / det},
		{(a[1][0]*a[2][1] - a[1][1]*a[2][0]) / det, (a[0][1]*a[2][0] - a[0][0]*a[2][1]) / det, (a[0][0]*a[1][1] - a[0][1]*a[1][0]) / det},
	}
}

// xyz2rgb takes XYZ D50 (Y of white 1) to linear sRGB: Bradford D50→D65,
// then the matrix of IEC 61966-2-1's primaries and D65 white.
var xyz2rgb, rgb2xyz = func() (mat3, mat3) {
	fromXY := func(x, y float64) [3]float64 { return [3]float64{x / y, 1, (1 - x - y) / y} }
	d65 := fromXY(0.3127, 0.3290)
	r, g, b := fromXY(0.64, 0.33), fromXY(0.30, 0.60), fromXY(0.15, 0.06)
	prim := mat3{{r[0], g[0], b[0]}, {r[1], g[1], b[1]}, {r[2], g[2], b[2]}}
	s := prim.inv().apply(d65)
	for i := range 3 {
		for j := range 3 {
			prim[i][j] *= s[j]
		}
	}
	brad := mat3{{0.8951, 0.2664, -0.1614}, {-0.7502, 1.7135, 0.0367}, {0.0389, -0.0685, 1.0296}}
	src, dst := brad.apply(d50), brad.apply(d65)
	gain := mat3{{dst[0] / src[0], 0, 0}, {0, dst[1] / src[1], 0}, {0, 0, dst[2] / src[2]}}
	adapt := brad.inv().mul(gain).mul(brad)
	toRGB := prim.inv().mul(adapt)
	return toRGB, toRGB.inv()
}()

const (
	labE = 216.0 / 24389
	labK = 24389.0 / 27
)

func labToXYZ(lab [3]float64) [3]float64 {
	fy := (lab[0] + 16) / 116
	fx := fy + lab[1]/500
	fz := fy - lab[2]/200
	inv := func(f float64) float64 {
		if f*f*f > labE {
			return f * f * f
		}
		return (116*f - 16) / labK
	}
	return [3]float64{inv(fx) * d50[0], inv(fy) * d50[1], inv(fz) * d50[2]}
}

func xyzToLab(xyz [3]float64) [3]float64 {
	f := func(t float64) float64 {
		if t > labE {
			return math.Cbrt(t)
		}
		return (labK*t + 16) / 116
	}
	fx, fy, fz := f(xyz[0]/d50[0]), f(xyz[1]/d50[1]), f(xyz[2]/d50[2])
	return [3]float64{116*fy - 16, 500 * (fx - fy), 200 * (fy - fz)}
}

// encodeSRGB is the sRGB transfer function, odd-symmetric so a colour
// outside the gamut keeps its sign.
func encodeSRGB(x float64) float64 {
	if math.Abs(x) <= 0.0031308 {
		return 12.92 * x
	}
	return math.Copysign(1.055*math.Pow(math.Abs(x), 1/2.4)-0.055, x)
}

func decodeSRGB(x float64) float64 {
	if math.Abs(x) <= 0.04045 {
		return x / 12.92
	}
	return math.Copysign(math.Pow((math.Abs(x)+0.055)/1.055, 2.4), x)
}

// SRGBToLab takes sRGB in 0..1 to Lab D50.
func SRGBToLab(rgb [3]float64) [3]float64 {
	lin := [3]float64{decodeSRGB(rgb[0]), decodeSRGB(rgb[1]), decodeSRGB(rgb[2])}
	return xyzToLab(rgb2xyz.apply(lin))
}

// LabToSRGB takes Lab D50 to sRGB, unclipped: a channel outside 0..1 means
// the colour is outside sRGB.
func LabToSRGB(lab [3]float64) [3]float64 {
	lin := xyz2rgb.apply(labToXYZ(lab))
	return [3]float64{encodeSRGB(lin[0]), encodeSRGB(lin[1]), encodeSRGB(lin[2])}
}

// decode reads the PCS value a tag wrote in v[:3] as XYZ D50 or Lab.
func (p *Profile) decode(enc pcsEncoding, v *[16]float64) (xyz, lab [3]float64) {
	if p.xyzPCS {
		// u1Fixed15Number: 1.0 is 0x8000.
		for i := range 3 {
			xyz[i] = v[i] * 65535 / 32768
		}
		return xyz, xyzToLab(xyz)
	}
	if enc == legacyLab {
		lab = [3]float64{v[0] * 65535 / 65280 * 100, v[1]*65535/256 - 128, v[2]*65535/256 - 128}
	} else {
		lab = [3]float64{v[0] * 100, v[1]*255 - 128, v[2]*255 - 128}
	}
	return labToXYZ(lab), lab
}

// encode writes Lab in v[:3] as a tag's PCS input.
func (p *Profile) encode(enc pcsEncoding, lab [3]float64, v *[16]float64) {
	if p.xyzPCS {
		xyz := labToXYZ(lab)
		for i := range 3 {
			v[i] = xyz[i] * 32768 / 65535
		}
		return
	}
	if enc == legacyLab {
		v[0] = lab[0] / 100 * 65280 / 65535
		v[1] = (lab[1] + 128) * 256 / 65535
		v[2] = (lab[2] + 128) * 256 / 65535
		return
	}
	v[0], v[1], v[2] = lab[0]/100, (lab[1]+128)/255, (lab[2]+128)/255
}

func (l *lut) eval(v *[16]float64) {
	for _, s := range l.stages {
		s.apply(v)
	}
}

// findBlack sets the black point as Adobe's black point compensation finds
// it for an output profile under the relative colorimetric intent: Lab 0
// through B2A gives the darkest colour the profile prints, ink limit
// included; that colour back through A2B, made neutral and held at or
// below L* 50, is the black point.
func (p *Profile) findBlack() {
	var v [16]float64
	p.encode(p.b2a.enc, [3]float64{}, &v)
	p.b2a.eval(&v)
	for i := range 4 {
		p.darkest[i] = clamp01(v[i])
	}
	lab := p.Lab(p.darkest)
	p.black = labToXYZ([3]float64{min(lab[0], 50), 0, 0})
}

// Lab is the PCS Lab D50 of a CMYK colour (0..1) under the relative
// colorimetric intent, without black point compensation.
func (p *Profile) Lab(cmyk [4]float64) [3]float64 {
	var v [16]float64
	copy(v[:4], cmyk[:])
	p.a2b.eval(&v)
	_, lab := p.decode(p.a2b.enc, &v)
	return lab
}

// ToSRGB converts a CMYK image to sRGB: relative colorimetric, the
// profile's black point scaled to sRGB's black (XYZ 0) with the white
// held, adapted D50 to D65 by Bradford, clipped to the gamut. Rows are
// split across GOMAXPROCS workers.
func (p *Profile) ToSRGB(img raster.PAM) *image.RGBA64 {
	out := image.NewRGBA64(image.Rect(0, 0, img.W, img.H))
	// Black point compensation: X' = (X - black) · white / (white - black),
	// per component, which keeps D50 white and sends the black point to 0.
	var scale, offset [3]float64
	for i := range 3 {
		scale[i] = d50[i] / (d50[i] - p.black[i])
		offset[i] = -p.black[i] * scale[i]
	}
	workers := max(min(runtime.GOMAXPROCS(0), img.H), 1)
	var wg sync.WaitGroup
	for w := range workers {
		wg.Go(func() {
			var v [16]float64
			for y := w; y < img.H; y += workers {
				src := img.Pix[y*img.W*4 : (y+1)*img.W*4]
				dst := out.Pix[y*out.Stride : y*out.Stride+img.W*8]
				for x := range img.W {
					for i := range 4 {
						v[i] = float64(src[4*x+i]) / 255
					}
					p.a2b.eval(&v)
					xyz, _ := p.decode(p.a2b.enc, &v)
					for i := range 3 {
						xyz[i] = xyz[i]*scale[i] + offset[i]
					}
					rgb := xyz2rgb.apply(xyz)
					d := dst[8*x:]
					for i := range 3 {
						c := uint16(math.Round(encodeSRGB(clamp01(rgb[i])) * 65535))
						d[2*i], d[2*i+1] = byte(c>>8), byte(c)
					}
					d[6], d[7] = 0xff, 0xff
				}
			}
		})
	}
	wg.Wait()
	return out
}
