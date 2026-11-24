package pano

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"

	"github.com/lockyc/plate/internal/engine"
	"github.com/lockyc/plate/internal/insv"
)

// lensFOV is each model's lens field of view in degrees: the angle its
// calibrated image circle (insv.Lens.R) spans edge to edge, the lens taken
// as equidistant (radius proportional to the angle off its axis). The
// trailer states the circle but not the angle it spans, so each is fitted
// to the least colour disagreement between the two lenses across their
// overlap on real footage.
var lensFOV = map[string]float64{
	"Insta360 X3": 189.5,
}

// lens is one lens's projection: its centre and focal length in pixels of
// its own frame, the widest angle off its axis it sees, and its axes (out
// of the lens, its picture's right and its picture's down) on the camera's
// body axes.
type lens struct {
	cx, cy, f, maxTheta float64
	axes                [3][3]float64
}

// lenses returns the clip's two lenses under fov (degrees). Lens 0 looks
// along +x with its picture upright and right along +y. Lens 1 looks the
// other way, turned half a turn about the body's down: the trailer states
// it at yaw ≈ 0 like lens 0. Each stated angle is a residual from that
// nominal pose, roll from the 90° an X3 states for an upright picture,
// turned right-handed about the lens's own down, right and forward axes in
// that order. On X3 footage the roll's sign is plain in the seam (the
// opposite sign leaves about a quarter more disagreement); the yaw and
// pitch residuals move it by less than the parallax between the lenses.
func lenses(m insv.Meta, fov float64) [2]lens {
	half := fov * math.Pi / 360
	var out [2]lens
	for i, c := range m.Lenses {
		q := axisAngle([3]float64{0, 0, 1}, float64(i)*math.Pi).
			mul(axisAngle([3]float64{0, 0, 1}, c.Yaw)).
			mul(axisAngle([3]float64{0, 1, 0}, c.Pitch)).
			mul(axisAngle([3]float64{1, 0, 0}, c.Roll-math.Pi/2))
		out[i] = lens{cx: c.CX, cy: c.CY, f: c.R / half, maxTheta: half}
		for k, e := range [3][3]float64{{1, 0, 0}, {0, 1, 0}, {0, 0, 1}} {
			out[i].axes[k] = q.rotate(e)
		}
	}
	return out
}

// pixel is where direction d (camera axes, unit length) lands in the lens's
// frame, in continuous pixel coordinates (pixel i spans [i, i+1)); ok is
// false beyond the lens's field of view.
func (l lens) pixel(d [3]float64) (x, y float64, ok bool) {
	a, b, c := dot(l.axes[0], d), dot(l.axes[1], d), dot(l.axes[2], d)
	rho := math.Hypot(b, c)
	theta := math.Atan2(rho, a)
	if theta > l.maxTheta {
		return 0, 0, false
	}
	if rho < 1e-12 {
		return l.cx, l.cy, true
	}
	r := l.f * theta / rho
	return l.cx + r*b, l.cy + r*c, true
}

// direction is the camera-axes direction of equirect pixel (i, j) in a w×h
// equirect centred on lens 0 (+x), right along +y and up along −z.
func direction(i, j, w, h int) [3]float64 {
	lon := ((float64(i)+0.5)/float64(w) - 0.5) * 2 * math.Pi
	lat := (0.5 - (float64(j)+0.5)/float64(h)) * math.Pi
	return [3]float64{math.Cos(lat) * math.Cos(lon), math.Cos(lat) * math.Sin(lon), -math.Sin(lat)}
}

// outside is the map value ffmpeg's remap reads as no source pixel: past
// every frame, so it fills the pixel.
const outside = math.MaxUint16

// Maps returns ffmpeg remap maps from each lens's frame onto a width ×
// width/2 equirect centred on lens 0, and the weight lens 0 has in the
// blend. xmap and ymap hold lens 0's w×h map then lens 1's; a pixel the lens
// cannot see is outside. mask is lens 0's weight, 0 to MaxUint16: whole up
// to the overlap the two lenses share (FOV − 180°), half at 90° off its
// axis, nothing beyond, linear across the overlap; nothing where lens 0 has
// no pixel and whole where lens 1 has none.
func Maps(m insv.Meta, width int) (xmap, ymap, mask []uint16, w, h int, err error) {
	fov, ok := lensFOV[m.Model]
	if !ok {
		return nil, nil, nil, 0, 0, fmt.Errorf("the lens field of view of a %q is not known", m.Model)
	}
	if width < 2 || width%2 != 0 {
		return nil, nil, nil, 0, 0, fmt.Errorf("width %d: want an even number of pixels", width)
	}
	w, h = width, width/2
	ls := lenses(m, fov)
	xmap = make([]uint16, 2*w*h)
	ymap = make([]uint16, 2*w*h)
	mask = make([]uint16, w*h)
	overlap := (fov - 180) * math.Pi / 180
	for j := range h {
		for i := range w {
			p := j*w + i
			d := direction(i, j, w, h)
			for k, l := range ls {
				x, y, ok := l.pixel(d)
				xi, yi := math.Floor(x), math.Floor(y)
				if !ok || xi < 0 || yi < 0 || xi >= float64(m.Width) || yi >= float64(m.Height) {
					xmap[k*w*h+p], ymap[k*w*h+p] = outside, outside
					continue
				}
				xmap[k*w*h+p], ymap[k*w*h+p] = uint16(xi), uint16(yi)
			}
			alpha := math.Acos(clamp(d[0]))
			wt := 0.5 + (math.Pi/2-alpha)/overlap
			switch {
			case xmap[p] == outside:
				wt = 0
			case xmap[w*h+p] == outside:
				wt = 1
			}
			mask[p] = uint16(math.Round(math.Max(0, math.Min(1, wt)) * math.MaxUint16))
		}
	}
	return xmap, ymap, mask, w, h, nil
}

// writePGM writes v, w×h, as a 16-bit binary PGM: the format ffmpeg reads a
// remap map from.
func writePGM(out io.Writer, v []uint16, w, h int) error {
	b := bufio.NewWriterSize(out, 1<<20)
	fmt.Fprintf(b, "P5\n%d %d\n65535\n", w, h)
	buf := make([]byte, 2*w)
	for j := range h {
		for i, x := range v[j*w : (j+1)*w] {
			binary.BigEndian.PutUint16(buf[2*i:], x)
		}
		if _, err := b.Write(buf); err != nil {
			return err
		}
	}
	return b.Flush()
}

// mapFiles are the remap inputs cachedMaps writes: each lens's x and y map
// and lens 0's blend weight.
var mapFiles = [5]string{"x0.pgm", "y0.pgm", "x1.pgm", "y1.pgm", "mask.pgm"}

// mapsFormat names the version of Maps' output in the map cache's key, so a
// plate whose Maps changed never reads maps an older one built.
// TestMapsFormatPinned fails when Maps' output changes and says to bump it.
const mapsFormat = 2

// cachedMaps returns the directory holding mapFiles for m's lenses at
// width, built once per camera calibration, width and mapsFormat.
func cachedMaps(m insv.Meta, width int) (string, error) {
	key := fmt.Sprintf("format %d model %q fov %v frame %dx%d width %d lenses %+v", mapsFormat, m.Model, lensFOV[m.Model], m.Width, m.Height, width, m.Lenses)
	return engine.Cached("pano-maps", map[string][]byte{"key": []byte(key)}, func(dir string) error {
		xm, ym, mask, w, h, err := Maps(m, width)
		if err != nil {
			return err
		}
		n := w * h
		for i, v := range [][]uint16{xm[:n], ym[:n], xm[n:], ym[n:], mask} {
			if err := writeFile(filepath.Join(dir, mapFiles[i]), v, w, h); err != nil {
				return err
			}
		}
		return nil
	})
}

func writeFile(path string, v []uint16, w, h int) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	if err := writePGM(f, v, w, h); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}
