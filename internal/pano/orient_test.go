package pano

import (
	"math"
	"testing"

	"github.com/lockyc/plate/internal/insv"
)

const (
	testRate = 1000.0 // the X3's IMU rate, near enough
	testFPS  = 30.0
	deg      = math.Pi / 180
)

// synth samples an IMU riding the body-to-world orientation at(t) for dur
// seconds: the gyro is the body rate (plus bias), the accelerometer the
// specific force of a body at rest, R(t)ᵀ·(0, 0, −g).
func synth(dur float64, at func(t float64) quat, bias [3]float64) []insv.IMU {
	n := int(dur*testRate) + 1
	out := make([]insv.IMU, n)
	const h = 1e-4
	for i := range out {
		t := float64(i) / testRate
		q := at(t)
		w := logq(at(t - h/2).conj().mul(at(t + h/2)))
		a := q.conj().rotate([3]float64{0, 0, -insv.StandardGravity})
		out[i] = insv.IMU{T: t, Accel: a}
		for k := range w {
			out[i].Gyro[k] = w[k]/h + bias[k]
		}
	}
	return out
}

// frames is every frame time of a dur-second clip.
func frames(dur float64) []float64 {
	var ts []float64
	for t := 0.0; t <= dur+1e-9; t += 1 / testFPS {
		ts = append(ts, t)
	}
	return ts
}

// zyx is the body-to-world orientation Rz(yaw)·Ry(pitch)·Rx(roll).
func zyx(yaw, pitch, roll float64) quat {
	return axisAngle([3]float64{0, 0, 1}, yaw).
		mul(axisAngle([3]float64{0, 1, 0}, pitch)).
		mul(axisAngle([3]float64{1, 0, 0}, roll))
}

func rolled(float64) quat { return zyx(0, 0, 12*deg) }

func turning(t float64) quat { return zyx(30*deg*t, 0, 0) }

func rotations(t *testing.T, imu []insv.IMU, ts []float64, m Mode) []Rot {
	t.Helper()
	rs, err := Rotations(imu, ts, m)
	if err != nil {
		t.Fatal(err)
	}
	if len(rs) != len(ts) {
		t.Fatalf("got %d rotations for %d frames", len(rs), len(ts))
	}
	return rs
}

func near(t *testing.T, what string, at, got, want, tol float64) {
	t.Helper()
	if math.Abs(got-want) > tol {
		t.Fatalf("%s at %.3fs = %.3f°, want %.3f° ± %.2f°", what, at, got/deg, want/deg, tol/deg)
	}
}

// TestStaticTilt holds from the first frame: the attitude starts from the
// first sample's accelerometer, so there is nothing for the slow Gain to
// settle.
func TestStaticTilt(t *testing.T) {
	imu := synth(2, rolled, [3]float64{})
	ts := frames(2)
	for i, r := range rotations(t, imu, ts, Level) {
		near(t, "Roll", ts[i], r.Roll, -12*deg, 0.5*deg)
		near(t, "Pitch", ts[i], r.Pitch, 0, 0.5*deg)
	}
}

// TestJoltedStart: a sideways jolt on the first 50 ms of a still, tilted
// clip leaves the picture within 1° of level from the first frame (the
// jolt's own pull is what remains); the attitude starts where a backward run
// of the filter settles, not on that first reading, which is 17° off.
func TestJoltedStart(t *testing.T) {
	imu := synth(4, rolled, [3]float64{})
	for i := range imu[:50] {
		imu[i].Accel[1] += 3
	}
	ts := frames(4)
	for i, r := range rotations(t, imu, ts, Level) {
		near(t, "Roll", ts[i], r.Roll, -12*deg, 1*deg)
		near(t, "Pitch", ts[i], r.Pitch, 0, 1*deg)
	}
}

// TestRotConvention pins what a Rot means: Rz(Yaw)·Ry(Pitch)·Rx(Roll) takes
// a direction in the levelled picture to the camera direction it samples, so
// the levelled picture's straight down lands on the camera's measured down.
func TestRotConvention(t *testing.T) {
	at := func(float64) quat { return zyx(0, 20*deg, -35*deg) }
	imu := synth(1, at, [3]float64{})
	r := rotations(t, imu, []float64{0.5}, Level)[0]
	got := zyx(r.Yaw, r.Pitch, r.Roll).rotate([3]float64{0, 0, 1})
	a := imu[500].Accel
	g := math.Sqrt(a[0]*a[0] + a[1]*a[1] + a[2]*a[2])
	for k := range got {
		if math.Abs(got[k]-(-a[k]/g)) > 1e-3 {
			t.Fatalf("levelled down lands on camera %v, want the camera's down %v", got, [3]float64{-a[0] / g, -a[1] / g, -a[2] / g})
		}
	}
}

func TestStaticTiltBias(t *testing.T) {
	imu := synth(2, rolled, [3]float64{0.26, 0, 0})
	ts := frames(2)
	for i, r := range rotations(t, imu, ts, Level) {
		near(t, "Roll", ts[i], r.Roll, -12*deg, 0.5*deg)
		near(t, "Pitch", ts[i], r.Pitch, 0, 0.5*deg)
	}
	lock := rotations(t, imu, ts, Lock)
	for i, r := range lock {
		near(t, "Lock Yaw drift", ts[i], r.Yaw, lock[0].Yaw, 2*deg)
	}
}

func TestSteadyTurnLevel(t *testing.T) {
	imu := synth(6, turning, [3]float64{})
	ts := frames(6)
	for i, r := range rotations(t, imu, ts, Level) {
		near(t, "Pitch", ts[i], r.Pitch, 0, 0.5*deg)
		near(t, "Roll", ts[i], r.Roll, 0, 0.5*deg)
		if ts[i] >= 2 {
			near(t, "Yaw", ts[i], r.Yaw, 0, 5*deg)
		}
	}
}

func TestSteadyTurnLock(t *testing.T) {
	imu := synth(2, turning, [3]float64{})
	r := rotations(t, imu, []float64{0, 1}, Lock)
	near(t, "Yaw", 0, r[0].Yaw, 0, 1*deg)
	near(t, "Yaw", 1, r[1].Yaw, -30*deg, 1*deg)
}

// rocking is a handheld pole: swaying through ±25° of pitch and roll at
// unrelated rates while turning, so gravity sweeps across the body and every
// gyro axis's bias shows against it.
func rocking(t float64) quat {
	return zyx(0.4*math.Sin(0.3*t), 25*deg*math.Sin(0.9*t), 25*deg*math.Sin(1.3*t+1))
}

func TestBiasFromMotion(t *testing.T) {
	bias := [3]float64{0.26, -0.1, 0.2}
	imu := synth(20, rocking, bias)
	got := gyroBias(imu)
	for k := range bias {
		if math.Abs(got[k]-bias[k]) > 0.005 {
			t.Fatalf("bias = %v, want %v", got, bias)
		}
	}
	ts := frames(20)
	for i, r := range rotations(t, imu, ts, Level) {
		q := rocking(ts[i])
		_, p, ro := toZYX(q)
		// Undoing a pure tilt Ry(p)·Rx(ro) gives Rx(−ro)·Ry(−p), whose ZYX
		// angles are not simply −p and −ro; compare the undone down instead.
		got := zyx(r.Yaw, r.Pitch, r.Roll).rotate([3]float64{0, 0, 1})
		down := q.conj().rotate([3]float64{0, 0, 1})
		var e float64
		for k := range got {
			e += (got[k] - down[k]) * (got[k] - down[k])
		}
		if math.Sqrt(e) > 1*deg {
			t.Fatalf("at %.2fs (pitch %.1f°, roll %.1f°) the levelled down is %.2f° off", ts[i], p/deg, ro/deg, math.Sqrt(e)/deg)
		}
	}
}

func TestNoIMU(t *testing.T) {
	if _, err := Rotations(nil, []float64{0}, Level); err == nil || err.Error() != "no IMU" {
		t.Fatalf("err = %v, want no IMU", err)
	}
}
