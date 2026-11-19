// Package cmm is plate's own colour engine: it reads a CMYK output profile
// and converts CMYK pixels to sRGB, relative colorimetric with black point
// compensation. It is written from ICC.1:2010 (v4.3), ICC.1:2001-04 (v2)
// and Adobe's "Adobe Black Point Compensation" paper.
package cmm

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"os"
)

// Profile is a loaded CMYK output profile: its device-to-PCS and PCS-to-
// device transforms for the relative colorimetric intent, and the source
// black point that black point compensation maps to sRGB's black.
type Profile struct {
	a2b, b2a *lut
	xyzPCS   bool
	// black is the profile's black point, XYZ D50 relative to the media
	// white; darkest is the CMYK that prints it.
	black   [3]float64
	darkest [4]float64
}

// Load reads a CMYK output profile. It converts with A2B1 (relative
// colorimetric) and finds the black point with B2A1; a profile without
// A2B1 is read through A2B0 and B2A0 instead. The pair is never mixed, so
// the black point is the darkest colour of the tables that convert.
func Load(path string) (*Profile, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if len(b) < 132 {
		return nil, fmt.Errorf("%s is not an ICC profile", path)
	}
	if string(b[12:16]) != "prtr" || string(b[16:20]) != "CMYK" {
		return nil, fmt.Errorf("%s is not a CMYK output profile", path)
	}
	p := &Profile{}
	switch string(b[20:24]) {
	case "Lab ":
	case "XYZ ":
		p.xyzPCS = true
	default:
		return nil, fmt.Errorf("%s: PCS %q is neither Lab nor XYZ", path, b[20:24])
	}
	tags := map[string][]byte{}
	n := int(binary.BigEndian.Uint32(b[128:]))
	if 132+12*n > len(b) {
		return nil, fmt.Errorf("%s: the tag table runs past the end of the file", path)
	}
	for i := range n {
		e := b[132+12*i:]
		off, size := int(binary.BigEndian.Uint32(e[4:])), int(binary.BigEndian.Uint32(e[8:]))
		if off < 0 || size < 0 || off+size > len(b) || off+size < off {
			return nil, fmt.Errorf("%s: tag %s runs past the end of the file", path, e[:4])
		}
		tags[string(e[:4])] = b[off : off+size]
	}
	read := func(name string) (*lut, error) {
		data, ok := tags[name]
		if !ok {
			return nil, fmt.Errorf("%s has no %s tag", path, name)
		}
		l, err := parseLut(data, p.xyzPCS)
		var u unknownType
		if errors.As(err, &u) {
			return nil, fmt.Errorf("%s: tag %s has type %q, which plate does not read", path, name, string(u))
		}
		if err != nil {
			return nil, fmt.Errorf("%s: tag %s: %w", path, name, err)
		}
		return l, nil
	}
	a2b, b2a := "A2B1", "B2A1"
	if _, ok := tags[a2b]; !ok {
		a2b, b2a = "A2B0", "B2A0"
	}
	if p.a2b, err = read(a2b); err != nil {
		return nil, err
	}
	if p.b2a, err = read(b2a); err != nil {
		return nil, err
	}
	if p.a2b.in != 4 || p.a2b.out != 3 || p.b2a.in != 3 || p.b2a.out != 4 {
		return nil, fmt.Errorf("%s: want a 4-to-3 A2B and a 3-to-4 B2A, got %d-to-%d and %d-to-%d", path, p.a2b.in, p.a2b.out, p.b2a.in, p.b2a.out)
	}
	p.findBlack()
	return p, nil
}

// unknownType is a tag type signature the engine does not read.
type unknownType string

func (u unknownType) Error() string {
	return fmt.Sprintf("type %q, which plate does not read", string(u))
}

// lut is one A2B or B2A tag: stages applied in order to values in 0..1,
// and the PCS encoding its tag type uses.
type lut struct {
	in, out int
	stages  []stage
	enc     pcsEncoding
}

// pcsEncoding is how a tag type maps 0..1 to Lab.
type pcsEncoding int

const (
	// legacyLab is lut16's: L* 100 at 0xFF00, a* and b* 0 at 0x8000.
	legacyLab pcsEncoding = iota
	// fullLab is lut8's and v4's: L* 100 at full scale, a* and b* 0 at 128/255.
	fullLab
)

// stage transforms a pixel in place; it reads and writes as many channels
// as its tag says.
type stage interface{ apply(v *[16]float64) }

type curves []curve

func (c curves) apply(v *[16]float64) {
	for i, f := range c {
		v[i] = f.eval(clamp01(v[i]))
	}
}

// matrix is a 3×3 matrix and an offset.
type matrix [12]float64

func (m *matrix) apply(v *[16]float64) {
	x, y, z := v[0], v[1], v[2]
	v[0] = m[0]*x + m[1]*y + m[2]*z + m[9]
	v[1] = m[3]*x + m[4]*y + m[5]*z + m[10]
	v[2] = m[6]*x + m[7]*y + m[8]*z + m[11]
}

// curve is a one-channel transfer function on 0..1.
type curve interface{ eval(x float64) float64 }

type identity struct{}

func (identity) eval(x float64) float64 { return x }

type gamma float64

func (g gamma) eval(x float64) float64 { return math.Pow(x, float64(g)) }

// table is a sampled curve, linearly interpolated.
type table []float64

func (t table) eval(x float64) float64 {
	pos := x * float64(len(t)-1)
	i := int(pos)
	if i >= len(t)-1 {
		return t[len(t)-1]
	}
	f := pos - float64(i)
	return t[i] + f*(t[i+1]-t[i])
}

// parametric is a parametricCurveType, function types 0 to 4.
type parametric struct {
	kind int
	g    float64
	p    [6]float64 // a, b, c, d, e, f
}

func (c parametric) eval(x float64) float64 {
	a, b, cc, d, e, f := c.p[0], c.p[1], c.p[2], c.p[3], c.p[4], c.p[5]
	pow := func(v float64) float64 {
		if v <= 0 {
			return 0
		}
		return math.Pow(v, c.g)
	}
	switch c.kind {
	case 0:
		return pow(x)
	case 1:
		if x >= -b/a {
			return pow(a*x + b)
		}
		return 0
	case 2:
		if x >= -b/a {
			return pow(a*x+b) + cc
		}
		return cc
	case 3:
		if x >= d {
			return pow(a*x + b)
		}
		return cc * x
	default:
		if x >= d {
			return pow(a*x+b) + e
		}
		return cc*x + f
	}
}

// clut is a multidimensional lookup table, the first input varying
// slowest, interpolated multilinearly: exact at the grid nodes.
type clut struct {
	in, out int
	grid    []int
	stride  []int // per input, in samples
	data    []float64
}

func (c *clut) apply(v *[16]float64) {
	var base [8]int
	var frac [8]float64
	for i := range c.in {
		pos := clamp01(v[i]) * float64(c.grid[i]-1)
		k := int(pos)
		if k >= c.grid[i]-1 {
			k = max(c.grid[i]-2, 0)
		}
		base[i] = k
		frac[i] = pos - float64(k)
	}
	var acc [16]float64
	for corner := range 1 << c.in {
		w := 1.0
		off := 0
		for i := range c.in {
			if corner>>(c.in-1-i)&1 == 1 {
				w *= frac[i]
				off += (base[i] + 1) * c.stride[i]
			} else {
				w *= 1 - frac[i]
				off += base[i] * c.stride[i]
			}
		}
		if w == 0 {
			continue
		}
		for o := range c.out {
			acc[o] += w * c.data[off+o]
		}
	}
	copy(v[:c.out], acc[:c.out])
}

func clamp01(x float64) float64 {
	if x < 0 || x != x {
		return 0
	}
	if x > 1 {
		return 1
	}
	return x
}

// reader reads big-endian fields of one tag, recording the first overrun.
type reader struct {
	b   []byte
	err error
}

func (r *reader) need(off, n int) bool {
	if r.err == nil && (off < 0 || n < 0 || off+n > len(r.b)) {
		r.err = fmt.Errorf("the tag is shorter than its contents")
	}
	return r.err == nil
}

func (r *reader) u8(off int) int {
	if !r.need(off, 1) {
		return 0
	}
	return int(r.b[off])
}

func (r *reader) u16(off int) int {
	if !r.need(off, 2) {
		return 0
	}
	return int(binary.BigEndian.Uint16(r.b[off:]))
}

func (r *reader) u32(off int) int {
	if !r.need(off, 4) {
		return 0
	}
	return int(binary.BigEndian.Uint32(r.b[off:]))
}

// s15f16 is an s15Fixed16Number.
func (r *reader) s15f16(off int) float64 {
	if !r.need(off, 4) {
		return 0
	}
	return float64(int32(binary.BigEndian.Uint32(r.b[off:]))) / 65536
}

// samples reads n unsigned samples of width bytes each, as 0..1.
func (r *reader) samples(off, n, width int) []float64 {
	if n > 1<<26 || !r.need(off, n*width) {
		r.need(-1, 0)
		return nil
	}
	out := make([]float64, n)
	for i := range out {
		if width == 1 {
			out[i] = float64(r.b[off+i]) / 255
		} else {
			out[i] = float64(binary.BigEndian.Uint16(r.b[off+2*i:])) / 65535
		}
	}
	return out
}

// parseLut reads an A2B or B2A tag; xyzPCS says the profile's PCS is XYZ.
func parseLut(b []byte, xyzPCS bool) (*lut, error) {
	r := &reader{b: b}
	if len(b) < 12 {
		return nil, fmt.Errorf("the tag is shorter than its header")
	}
	sig := string(b[:4])
	l := &lut{in: r.u8(8), out: r.u8(9)}
	if l.in < 1 || l.in > 8 || l.out < 1 || l.out > 15 {
		return nil, fmt.Errorf("%d inputs and %d outputs are out of range", l.in, l.out)
	}
	switch sig {
	case "mft1", "mft2":
		parseLut816(r, l, sig == "mft2", xyzPCS)
	case "mAB ", "mBA ":
		parseLutAB(r, l, sig == "mAB ")
	default:
		return nil, unknownType(sig)
	}
	if r.err != nil {
		return nil, r.err
	}
	return l, nil
}

// parseLut816 reads lut8Type and lut16Type: matrix, input tables, CLUT,
// output tables. The matrix applies only to XYZ input, so only to a B2A
// tag of an XYZ-PCS profile.
func parseLut816(r *reader, l *lut, wide, xyzPCS bool) {
	grid := r.u8(10)
	width, nIn, nOut, off := 1, 256, 256, 48
	l.enc = fullLab
	if wide {
		width, nIn, nOut, off = 2, r.u16(48), r.u16(50), 52
		l.enc = legacyLab
	}
	if grid < 2 || nIn < 2 || nOut < 2 {
		r.need(-1, 0)
		return
	}
	var m matrix
	for i := range 9 {
		m[i] = r.s15f16(12 + 4*i)
	}
	if xyzPCS && l.in == 3 {
		l.stages = append(l.stages, &m)
	}
	in := make(curves, l.in)
	for i := range in {
		in[i] = table(r.samples(off, nIn, width))
		off += nIn * width
	}
	gridPts := make([]int, l.in)
	for i := range gridPts {
		gridPts[i] = grid
	}
	c := newClut(l.in, l.out, gridPts)
	c.data = r.samples(off, len(c.data), width)
	off += len(c.data) * width
	out := make(curves, l.out)
	for i := range out {
		out[i] = table(r.samples(off, nOut, width))
		off += nOut * width
	}
	l.stages = append(l.stages, in, c, out)
}

// parseLutAB reads lutAtoBType (A curves, CLUT, M curves, matrix, B curves)
// and lutBtoAType (the same elements in reverse order). Each element is
// optional; its offset is 0 when absent.
func parseLutAB(r *reader, l *lut, aToB bool) {
	l.enc = fullLab
	offB, offMat, offM, offClut, offA := r.u32(12), r.u32(16), r.u32(20), r.u32(24), r.u32(28)
	// The A curves are on the device side of the CLUT and count device
	// channels; the B and M curves are on the PCS side and count PCS
	// channels: after the CLUT in lutAtoBType, before it in lutBtoAType.
	aN, bN := l.in, l.out
	if !aToB {
		aN, bN = l.out, l.in
	}
	var a, m, bc curves
	if offA != 0 {
		a = readCurves(r, offA, aN)
	}
	if offM != 0 {
		m = readCurves(r, offM, bN)
	}
	if offB != 0 {
		bc = readCurves(r, offB, bN)
	}
	var mat *matrix
	if offMat != 0 {
		mat = new(matrix)
		for i := range 12 {
			mat[i] = r.s15f16(offMat + 4*i)
		}
	}
	var c *clut
	if offClut != 0 {
		cin, cout := l.in, l.out
		gridPts := make([]int, cin)
		for i := range gridPts {
			if gridPts[i] = r.u8(offClut + i); gridPts[i] < 2 {
				r.need(-1, 0)
				return
			}
		}
		width := r.u8(offClut + 16)
		if width != 1 && width != 2 {
			r.need(-1, 0)
			return
		}
		c = newClut(cin, cout, gridPts)
		c.data = r.samples(offClut+20, len(c.data), width)
	}
	if r.err != nil {
		return
	}
	add := func(s stage, ok bool) {
		if ok {
			l.stages = append(l.stages, s)
		}
	}
	if aToB {
		add(a, a != nil)
		add(c, c != nil)
		add(m, m != nil)
		add(mat, mat != nil)
		add(bc, bc != nil)
	} else {
		add(bc, bc != nil)
		add(mat, mat != nil)
		add(m, m != nil)
		add(c, c != nil)
		add(a, a != nil)
	}
}

func newClut(in, out int, grid []int) *clut {
	c := &clut{in: in, out: out, grid: grid, stride: make([]int, in)}
	n := out
	for i := in - 1; i >= 0; i-- {
		c.stride[i] = n
		n *= grid[i]
	}
	c.data = make([]float64, n)
	return c
}

// readCurves reads n consecutive curveType or parametricCurveType elements,
// each padded to a 4-byte boundary.
func readCurves(r *reader, off, n int) curves {
	cs := make(curves, n)
	for i := range cs {
		if !r.need(off, 12) {
			return nil
		}
		var size int
		switch string(r.b[off : off+4]) {
		case "curv":
			cnt := r.u32(off + 8)
			size = 12 + 2*cnt
			switch cnt {
			case 0:
				cs[i] = identity{}
			case 1:
				cs[i] = gamma(float64(r.u16(off+12)) / 256)
			default:
				cs[i] = table(r.samples(off+12, cnt, 2))
			}
		case "para":
			kind := r.u16(off + 8)
			nParams := []int{1, 3, 4, 5, 7}
			if kind > 4 {
				r.err = fmt.Errorf("parametric curve function type %d, which plate does not read", kind)
				return nil
			}
			c := parametric{kind: kind, g: r.s15f16(off + 12)}
			for k := 1; k < nParams[kind]; k++ {
				c.p[k-1] = r.s15f16(off + 12 + 4*k)
			}
			cs[i] = c
			size = 12 + 4*nParams[kind]
		default:
			r.err = fmt.Errorf("curve type %q, which plate does not read", r.b[off:off+4])
			return nil
		}
		off += (size + 3) &^ 3
	}
	return cs
}
