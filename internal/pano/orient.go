// Package pano turns an Insta360 clip into a level 360 picture. This file
// turns the clip's IMU into the rotation that undoes the camera's motion at
// each frame.
package pano

import (
	"errors"
	"fmt"
	"math"
	"sort"

	"github.com/lockyc/plate/internal/insv"
)

// Mode says how much of the camera's motion Rotations undoes.
type Mode int

const (
	// Level removes pitch and roll and keeps the heading, low-passed through
	// HeadingTau without lag (smooth), so walking turns the view and only the
	// heading's shake is undone.
	Level Mode = iota
	// Lock removes all rotation: the picture is level and keeps the heading
	// the camera had at the first frame time.
	Lock
)

// Rot is the rotation, in radians, that turns one frame's equirect into the
// stabilised one. On the camera's axes (+x out of the front lens, +y right,
// +z down, as insv.IMU states them) Rz(Yaw)·Ry(Pitch)·Rx(Roll), each a
// right-handed turn about that axis, takes a direction in the stabilised
// picture to the camera direction it samples. With the equirect centred on
// +x, right along +y and up along −z, that is ffmpeg v360's yaw, pitch and
// roll (in degrees) in its default rotation order "ypr": v360 turns each
// output direction by its yaw about the down axis, then pitch about the right
// axis, then roll about the forward axis, outermost first, before sampling
// the input.
type Rot struct{ Yaw, Pitch, Roll float64 }

// Gain is the fraction of the angle between the predicted and the measured
// gravity direction that each IMU sample's correction turns away: on the
// X3's 1 kHz IMU a time constant of about 2 s, slow enough that footsteps
// and sway barely move the horizon between frames.
const Gain = 0.0005

// HeadingTau is the time constant, in seconds, of the low-pass that Level
// keeps the heading through.
const HeadingTau = 2.0

// BiasWindow is the span, in seconds, over which gyroBias integrates the
// gyro and compares the result with the accelerometer.
const BiasWindow = 1.0

// gravityHalf is the half-width, in seconds, of the accelerometer average
// that gyroBias takes as gravity's direction at a window's ends.
const gravityHalf = 0.1

// Rotations returns, for each time in times (seconds from the first frame,
// as insv.IMU.T), the Rot that undoes the camera's motion under m. It
// removes a constant gyro bias (gyroBias), tracks the camera's orientation
// by integrating the gyro and turning each step a Gain fraction towards the
// accelerometer's gravity, and reads it at each time. A time outside the IMU
// takes the nearest sample's orientation.
func Rotations(imu []insv.IMU, times []float64, m Mode) ([]Rot, error) {
	if len(imu) == 0 {
		return nil, errors.New("no IMU")
	}
	att := attitude(imu, gyroBias(imu))
	ts := make([]float64, len(imu))
	heading := make([]float64, len(imu))
	for i, s := range imu {
		ts[i] = s.T
		heading[i], _, _ = toZYX(att[i])
	}
	unwrap(heading)
	var view func(t float64) float64
	switch m {
	case Level:
		smoothed := smooth(ts, heading, HeadingTau)
		view = func(t float64) float64 { return interp(ts, smoothed, t) }
	case Lock:
		var held float64
		if len(times) > 0 {
			held = interp(ts, heading, times[0])
		}
		view = func(float64) float64 { return held }
	default:
		return nil, fmt.Errorf("unknown mode %d", m)
	}
	out := make([]Rot, len(times))
	for k, t := range times {
		h := view(t)
		q := slerpAt(ts, att, t)
		out[k].Yaw, out[k].Pitch, out[k].Roll = toZYX(q.conj().mul(axisAngle([3]float64{0, 0, 1}, h)))
	}
	return out, nil
}

// up is world up, on world axes that point the camera's way when it is level
// (z down): a body at rest reads up times g on its accelerometer.
var up = [3]float64{0, 0, -1}

// attitude integrates the gyro, less bias, turning each sample Gain of the
// way from its predicted gravity to the measured one, and returns each
// sample's body-to-world orientation. The same filter first runs backwards
// from the last sample to the first, and the forward run starts from the
// tilt it arrives at (heading zero): one sample's accelerometer is a poor
// start for a handheld camera, and at Gain's slow rate its error would last
// seconds into the clip.
func attitude(imu []insv.IMU, bias [3]float64) []quat {
	n := len(imu)
	q := levelled(imu[n-1].Accel)
	for i := n - 1; i > 0; i-- {
		q = correct(q.mul(step(imu[i-1], imu[i], bias).conj()), imu[i-1].Accel)
	}
	q = levelled(q.conj().rotate(up))
	out := make([]quat, n)
	out[0] = q
	for i := 1; i < n; i++ {
		q = correct(q.mul(step(imu[i-1], imu[i], bias)), imu[i].Accel)
		out[i] = q
	}
	return out
}

// correct turns q Gain of the way from its predicted gravity direction to
// the accelerometer's.
func correct(q quat, accel [3]float64) quat {
	if a, ok := unit(accel); ok {
		p := q.conj().rotate(up)
		k := cross(p, a)
		if s := norm(k); s > 1e-12 {
			q = q.mul(axisAngle(scale(k, 1/s), -Gain*math.Atan2(s, dot(p, a))))
		}
	}
	return q.normalized()
}

// step is the body's turn from sample a to sample b: the mean of their gyro
// readings, less bias, over the time between them. A step with no forward
// time or a reading that is not a number does not turn: one bad sample
// costs a millisecond of rotation instead of every frame after it.
func step(a, b insv.IMU, bias [3]float64) quat {
	dt := b.T - a.T
	if !(dt > 0) || math.IsInf(dt, 0) {
		return quat{1, 0, 0, 0}
	}
	var w [3]float64
	for k := range w {
		w[k] = ((a.Gyro[k]+b.Gyro[k])/2 - bias[k]) * dt
		if math.IsNaN(w[k]) || math.IsInf(w[k], 0) {
			return quat{1, 0, 0, 0}
		}
	}
	return expq(w)
}

// levelled is the orientation, heading zero, whose accelerometer at rest
// reads along a.
func levelled(a [3]float64) quat {
	u, ok := unit(a)
	if !ok {
		return quat{1, 0, 0, 0}
	}
	pitch := math.Asin(clamp(u[0]))
	roll := math.Atan2(-u[1], -u[2])
	return axisAngle([3]float64{0, 1, 0}, pitch).mul(axisAngle([3]float64{1, 0, 0}, roll))
}

// gyroBias estimates the gyro's constant bias as the one that makes the
// integrated gyro carry gravity's direction, as the accelerometer reads it
// at the start of each BiasWindow, onto the direction it reads at the end.
// Windows start every quarter window; gravity at each end is the
// accelerometer averaged over ±gravityHalf, so footsteps and sway mostly
// cancel, and Cauchy weights on each window's miss discount the windows
// they do not. Only the part of the bias across gravity shows in a window: a
// clip whose gravity sweeps across the body pins all three axes, and a ridge
// keeps the part no window sees at zero, where a still clip's turn about
// gravity stays a turn.
func gyroBias(imu []insv.IMU) [3]float64 {
	var b [3]float64
	wins := windows(imu)
	if len(wins) == 0 {
		return b
	}
	const h = 1e-4 // rad/s, the finite-difference step
	for iter := 0; iter < 8; iter++ {
		var ata [3][3]float64
		var atr [3]float64
		var res [][3]float64
		var jac [][3][3]float64
		for _, w := range wins {
			r := w.miss(imu, b)
			var j [3][3]float64 // j[k] = d(predicted)/d(b[k])
			for k := range 3 {
				bk := b
				bk[k] += h
				rk := w.miss(imu, bk)
				for c := range 3 {
					j[k][c] = (r[c] - rk[c]) / h
				}
			}
			res = append(res, r)
			jac = append(jac, j)
		}
		c := cauchyScale(res)
		for i, r := range res {
			wt := 1 / (1 + dot(r, r)/(c*c))
			for p := range 3 {
				atr[p] += wt * dot(jac[i][p], r)
				for q := range 3 {
					ata[p][q] += wt * dot(jac[i][p], jac[i][q])
				}
			}
		}
		lambda := 1e-3 * (ata[0][0] + ata[1][1] + ata[2][2]) / 3
		for p := range 3 {
			ata[p][p] += lambda
			atr[p] -= lambda * b[p]
		}
		d, ok := solve3(ata, atr)
		if !ok {
			break
		}
		for k := range b {
			b[k] += d[k]
		}
		if norm(d) < 1e-7 {
			break
		}
	}
	return b
}

// cauchyScale is the Cauchy weights' scale: the median miss, floored so a
// clip that fits exactly keeps equal weights.
func cauchyScale(res [][3]float64) float64 {
	m := make([]float64, len(res))
	for i, r := range res {
		m[i] = norm(r)
	}
	sort.Float64s(m)
	return math.Max(m[len(m)/2], 1e-3)
}

// window is one span gyroBias integrates over: sample indices and gravity's
// measured direction at each end.
type window struct {
	from, to int
	start    [3]float64
	end      [3]float64
}

// miss is the measured end direction less the start direction carried
// through the window by the gyro less b.
func (w window) miss(imu []insv.IMU, b [3]float64) [3]float64 {
	q := quat{1, 0, 0, 0}
	for i := w.from + 1; i <= w.to; i++ {
		q = q.mul(step(imu[i-1], imu[i], b))
	}
	p := q.conj().rotate(w.start)
	return sub(w.end, p)
}

// windows lays BiasWindow-long windows a quarter window apart across the
// clip, each end far enough inside it for the ±gravityHalf average; a clip
// shorter than one window gets one as long as it allows.
func windows(imu []insv.IMU) []window {
	n := len(imu)
	first, last := imu[0].T, imu[n-1].T
	span := last - first - 2*gravityHalf
	if n < 3 || span <= 0 {
		return nil
	}
	length := math.Min(BiasWindow, span)
	sum := make([][3]float64, n+1) // running sums of the accelerometer
	for i, s := range imu {
		for k := range 3 {
			sum[i+1][k] = sum[i][k] + s.Accel[k]
		}
	}
	// index is the first sample at or after t.
	index := func(t float64) int { return sort.Search(n, func(i int) bool { return imu[i].T >= t }) }
	gravity := func(t float64) ([3]float64, bool) {
		lo, hi := index(t-gravityHalf), index(t+gravityHalf+1e-9)
		var m [3]float64
		for k := range m {
			m[k] = sum[hi][k] - sum[lo][k]
		}
		return unit(m)
	}
	var out []window
	for t0 := first + gravityHalf; t0+length <= last-gravityHalf+1e-9; t0 += length / 4 {
		w := window{from: index(t0), to: min(index(t0+length), n-1)}
		var ok1, ok2 bool
		w.start, ok1 = gravity(imu[w.from].T)
		w.end, ok2 = gravity(imu[w.to].T)
		if ok1 && ok2 && w.to > w.from {
			out = append(out, w)
		}
	}
	return out
}

// smooth low-passes x(t) with time constant tau, forwards then backwards so
// the result does not lag. The straight line through the ends is taken out
// first and put back after, and each end is padded with the residual's
// point reflection, so a steady turn passes through unchanged and the ends
// carry no start-up transient.
func smooth(t, x []float64, tau float64) []float64 {
	n := len(x)
	out := make([]float64, n)
	if n < 3 {
		copy(out, x)
		return out
	}
	t0, t1 := t[0], t[n-1]
	line := func(s float64) float64 {
		if t1 == t0 {
			return x[0]
		}
		return x[0] + (x[n-1]-x[0])*(s-t0)/(t1-t0)
	}
	pad := n - 1
	if per := (t1 - t0) / float64(n-1); per > 0 {
		pad = min(pad, int(math.Ceil(5*tau/per)))
	}
	pt := make([]float64, 0, n+2*pad)
	px := make([]float64, 0, n+2*pad)
	for i := pad; i >= 1; i-- {
		pt = append(pt, 2*t0-t[i])
		px = append(px, -(x[i] - line(t[i])))
	}
	for i := range x {
		pt = append(pt, t[i])
		px = append(px, x[i]-line(t[i]))
	}
	for i := n - 2; i >= n-1-pad; i-- {
		pt = append(pt, 2*t1-t[i])
		px = append(px, -(x[i] - line(t[i])))
	}
	pass := func(order []int) {
		y := px[order[0]]
		prev := pt[order[0]]
		for _, i := range order {
			a := 1 - math.Exp(-math.Abs(pt[i]-prev)/tau)
			y += a * (px[i] - y)
			px[i] = y
			prev = pt[i]
		}
	}
	idx := make([]int, len(px))
	for i := range idx {
		idx[i] = i
	}
	pass(idx)
	for i, j := 0, len(idx)-1; i < j; i, j = i+1, j-1 {
		idx[i], idx[j] = idx[j], idx[i]
	}
	pass(idx)
	for i := range out {
		out[i] = px[pad+i] + line(t[i])
	}
	return out
}

// unwrap removes the 2π jumps from a run of angles.
func unwrap(a []float64) {
	for i := 1; i < len(a); i++ {
		d := a[i] - a[i-1]
		a[i] -= 2 * math.Pi * math.Round(d/(2*math.Pi))
	}
}

// interp reads x(t) linearly between samples, clamped at the ends.
func interp(ts, x []float64, t float64) float64 {
	i, u := locate(ts, t)
	if u == 0 {
		return x[i]
	}
	return x[i] + u*(x[i+1]-x[i])
}

// slerpAt reads the orientation at t between samples, clamped at the ends.
func slerpAt(ts []float64, qs []quat, t float64) quat {
	i, u := locate(ts, t)
	if u == 0 {
		return qs[i]
	}
	return slerp(qs[i], qs[i+1], u)
}

// locate finds the sample at or before t and how far t is towards the next.
func locate(ts []float64, t float64) (int, float64) {
	n := len(ts)
	if t <= ts[0] || n == 1 {
		return 0, 0
	}
	if t >= ts[n-1] {
		return n - 1, 0
	}
	i := sort.SearchFloat64s(ts, t)
	if ts[i] == t {
		return i, 0
	}
	i--
	if ts[i+1] == ts[i] {
		return i, 0
	}
	return i, (t - ts[i]) / (ts[i+1] - ts[i])
}

// quat is a unit quaternion (w, x, y, z) turning body vectors into world
// ones: v' = q·v·q*.
type quat [4]float64

func (a quat) mul(b quat) quat {
	return quat{
		a[0]*b[0] - a[1]*b[1] - a[2]*b[2] - a[3]*b[3],
		a[0]*b[1] + a[1]*b[0] + a[2]*b[3] - a[3]*b[2],
		a[0]*b[2] - a[1]*b[3] + a[2]*b[0] + a[3]*b[1],
		a[0]*b[3] + a[1]*b[2] - a[2]*b[1] + a[3]*b[0],
	}
}

func (q quat) conj() quat { return quat{q[0], -q[1], -q[2], -q[3]} }

func (q quat) normalized() quat {
	n := math.Sqrt(q[0]*q[0] + q[1]*q[1] + q[2]*q[2] + q[3]*q[3])
	return quat{q[0] / n, q[1] / n, q[2] / n, q[3] / n}
}

func (q quat) rotate(v [3]float64) [3]float64 {
	r := q.mul(quat{0, v[0], v[1], v[2]}).mul(q.conj())
	return [3]float64{r[1], r[2], r[3]}
}

// axisAngle is the right-handed turn by angle about the unit axis.
func axisAngle(axis [3]float64, angle float64) quat {
	s := math.Sin(angle / 2)
	return quat{math.Cos(angle / 2), axis[0] * s, axis[1] * s, axis[2] * s}
}

// expq is the turn by the rotation vector v.
func expq(v [3]float64) quat {
	a := norm(v)
	if a < 1e-12 {
		return quat{1, v[0] / 2, v[1] / 2, v[2] / 2}.normalized()
	}
	return axisAngle(scale(v, 1/a), a)
}

// logq is expq's inverse.
func logq(q quat) [3]float64 {
	if q[0] < 0 {
		q = quat{-q[0], -q[1], -q[2], -q[3]}
	}
	v := [3]float64{q[1], q[2], q[3]}
	s := norm(v)
	if s < 1e-12 {
		return scale(v, 2)
	}
	return scale(v, 2*math.Atan2(s, q[0])/s)
}

func slerp(a, b quat, u float64) quat {
	if a[0]*b[0]+a[1]*b[1]+a[2]*b[2]+a[3]*b[3] < 0 {
		b = quat{-b[0], -b[1], -b[2], -b[3]}
	}
	return a.mul(expq(scale(logq(a.conj().mul(b)), u))).normalized()
}

// toZYX splits q into yaw, pitch and roll with q = Rz(yaw)·Ry(pitch)·Rx(roll).
func toZYX(q quat) (yaw, pitch, roll float64) {
	w, x, y, z := q[0], q[1], q[2], q[3]
	yaw = math.Atan2(2*(w*z+x*y), 1-2*(y*y+z*z))
	pitch = math.Asin(clamp(2 * (w*y - z*x)))
	roll = math.Atan2(2*(w*x+y*z), 1-2*(x*x+y*y))
	return
}

func clamp(v float64) float64 { return math.Max(-1, math.Min(1, v)) }

func dot(a, b [3]float64) float64 { return a[0]*b[0] + a[1]*b[1] + a[2]*b[2] }

func norm(a [3]float64) float64 { return math.Sqrt(dot(a, a)) }

func scale(a [3]float64, s float64) [3]float64 { return [3]float64{a[0] * s, a[1] * s, a[2] * s} }

func sub(a, b [3]float64) [3]float64 { return [3]float64{a[0] - b[0], a[1] - b[1], a[2] - b[2]} }

func cross(a, b [3]float64) [3]float64 {
	return [3]float64{a[1]*b[2] - a[2]*b[1], a[2]*b[0] - a[0]*b[2], a[0]*b[1] - a[1]*b[0]}
}

func unit(a [3]float64) ([3]float64, bool) {
	n := norm(a)
	if n == 0 || math.IsNaN(n) || math.IsInf(n, 0) {
		return a, false
	}
	return scale(a, 1/n), true
}

// solve3 solves m·x = r by Cramer's rule.
func solve3(m [3][3]float64, r [3]float64) ([3]float64, bool) {
	det := func(a [3][3]float64) float64 {
		return a[0][0]*(a[1][1]*a[2][2]-a[1][2]*a[2][1]) -
			a[0][1]*(a[1][0]*a[2][2]-a[1][2]*a[2][0]) +
			a[0][2]*(a[1][0]*a[2][1]-a[1][1]*a[2][0])
	}
	d := det(m)
	if d == 0 || math.IsNaN(d) {
		return [3]float64{}, false
	}
	var x [3]float64
	for c := range 3 {
		mc := m
		for row := range 3 {
			mc[row][c] = r[row]
		}
		x[c] = det(mc) / d
	}
	return x, true
}
