package insv_test

import (
	"encoding/binary"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lockyc/plate/internal/insv"
	"github.com/lockyc/plate/internal/insv/insvtest"
)

func rad(deg float64) float64 { return deg * math.Pi / 180 }

func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

// clip writes a file of placeholder video bytes with m's trailer appended.
func clip(t *testing.T, m insv.Meta, offset string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "VID_20260101_000000_00_001.insv")
	if err := os.WriteFile(path, []byte("not really video"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := insvtest.Write(path, m, offset); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestReadOffset(t *testing.T) {
	m := insv.Meta{Model: "Insta360 X3", Serial: "TESTSERIAL", Width: 2880, Height: 2880, FPS: 30}
	// The calibration frame (5760×2880, both lenses side by side) is the
	// clip's, so lens 1's centre sits one lens width along.
	path := clip(t, m, "2_1435.0_1440.2_1441.7_0.1_-0.2_90.0_1434.6_4319.8_1440.5_179.8_0.3_-90.1_5760_2880_2887")
	got, err := insv.Read(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.Model != m.Model || got.Serial != m.Serial || got.Width != 2880 || got.Height != 2880 || got.FPS != 30 {
		t.Fatalf("header = %q %q %dx%d %v fps", got.Model, got.Serial, got.Width, got.Height, got.FPS)
	}
	want := insv.Lens{CX: 1440.2, CY: 1441.7, R: 1435.0, Yaw: rad(0.1), Pitch: rad(-0.2), Roll: rad(90.0)}
	l := got.Lenses[0]
	if !near(l.CX, want.CX) || !near(l.CY, want.CY) || !near(l.R, want.R) ||
		!near(l.Yaw, want.Yaw) || !near(l.Pitch, want.Pitch) || !near(l.Roll, want.Roll) {
		t.Errorf("Lenses[0] = %+v, want %+v", l, want)
	}
	if l1 := got.Lenses[1]; !near(l1.Yaw, rad(179.8)) || !near(l1.CX, 1439.8) || !near(l1.Roll, rad(-90.1)) {
		t.Errorf("Lenses[1] = %+v", l1)
	}
}

func TestReadOffsetScaled(t *testing.T) {
	// The calibration frame is the sensor's (11904×5952); the clip's frame is
	// 2880², so every length scales by 2880/5952.
	m := insv.Meta{Model: "Insta360 X3", Width: 2880, Height: 2880, FPS: 30}
	path := clip(t, m, "2_2893.880_2968.260_3008.860_0.244_0.303_90.124_2908.880_8927.530_2996.270_0.145_0.091_90.549_11904_5952_2887")
	got, err := insv.Read(path)
	if err != nil {
		t.Fatal(err)
	}
	s := 2880.0 / 5952
	if l := got.Lenses[0]; !near(l.R, 2893.880*s) || !near(l.CX, 2968.260*s) || !near(l.CY, 3008.860*s) {
		t.Errorf("Lenses[0] = %+v", l)
	}
	if l := got.Lenses[1]; !near(l.CX, (8927.530-5952)*s) || !near(l.CY, 2996.270*s) {
		t.Errorf("Lenses[1] = %+v", l)
	}
}

const testOffset = "2_1435_1440_1440_0_0_90_1435_4320_1440_180_0_-90_5760_2880_2887"

func TestReadIMU(t *testing.T) {
	samples := []insv.IMU{
		{T: 0, Gyro: [3]float64{0.1, -0.2, 0.3}, Accel: [3]float64{0, 0, 9.80665}},
		{T: 0.005, Gyro: [3]float64{0.4, 0.5, -0.6}, Accel: [3]float64{1, -2, 9}},
		{T: 0.010, Gyro: [3]float64{-1, 0, 1}, Accel: [3]float64{-9.80665, 0.5, 0}},
	}
	m := insv.Meta{Model: "Insta360 X3", Width: 2880, Height: 2880, FPS: 30, IMU: samples}
	got, err := insv.Read(clip(t, m, testOffset))
	if err != nil {
		t.Fatal(err)
	}
	if len(got.IMU) != 3 {
		t.Fatalf("len(IMU) = %d, want 3", len(got.IMU))
	}
	if got.IMU[2].T != 0.010 {
		t.Errorf("IMU[2].T = %v, want 0.010", got.IMU[2].T)
	}
	for i, s := range samples {
		g := got.IMU[i]
		for k := range 3 {
			if !near(g.Gyro[k], s.Gyro[k]) || !near(g.Accel[k], s.Accel[k]) {
				t.Errorf("IMU[%d] = %+v, want %+v", i, g, s)
				break
			}
		}
	}
}

func TestNoTrailer(t *testing.T) {
	path := filepath.Join(t.TempDir(), "plain.insv")
	if err := os.WriteFile(path, []byte(strings.Repeat("plain video bytes ", 20)), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := insv.Read(path)
	if err == nil || !strings.Contains(err.Error(), "no Insta360 trailer") {
		t.Fatalf("err = %v, want one containing %q", err, "no Insta360 trailer")
	}
}

func TestTruncatedRecord(t *testing.T) {
	m := insv.Meta{Model: "Insta360 X3", Width: 2880, Height: 2880, FPS: 30}
	path := clip(t, m, testOffset)
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// The last record's descriptor ends where the 72-byte footer starts; its
	// size field is its last four bytes.
	binary.LittleEndian.PutUint32(b[len(b)-72-4:], 0x7fffffff)
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := insv.Read(path); err == nil {
		t.Fatal("Read of a record running past the file's start: no error")
	}
}

func touch(t *testing.T, path string) {
	t.Helper()
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestPair(t *testing.T) {
	dir := t.TempDir()
	front := filepath.Join(dir, "VID_20261122_123616_00_002.insv")
	back := filepath.Join(dir, "VID_20261122_123616_10_002.insv")
	touch(t, front)
	touch(t, back)
	for _, given := range []string{front, back} {
		f, b, err := insv.Pair(given)
		if err != nil || f != front || b != back {
			t.Errorf("Pair(%s) = %s, %s, %v; want %s, %s", filepath.Base(given), f, b, err, front, back)
		}
	}
}

func TestPairMissing(t *testing.T) {
	dir := t.TempDir()
	back := filepath.Join(dir, "VID_20261122_123616_10_002.insv")
	touch(t, back)
	_, _, err := insv.Pair(back)
	if err == nil || !strings.Contains(err.Error(), "VID_20261122_123616_00_002.insv") {
		t.Fatalf("err = %v, want one naming the _00_ file", err)
	}
}

func TestPairNotInsta360Name(t *testing.T) {
	if _, _, err := insv.Pair("clip.mp4"); err == nil {
		t.Fatal("Pair of a name with no _00_/_10_ lens field: no error")
	}
}

// TestRealFile reads a real X3 clip's front (_00_) file, named by PLATE_INSV.
func TestRealFile(t *testing.T) {
	path := os.Getenv("PLATE_INSV")
	if path == "" {
		t.Skip("PLATE_INSV names no real .insv file")
	}
	m, err := insv.Read(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(m.Model, "X3") {
		t.Errorf("Model = %q, want an X3", m.Model)
	}
	for i, l := range m.Lenses {
		if l.R <= 1000 {
			t.Errorf("Lenses[%d].R = %v, want > 1000", i, l.R)
		}
	}
	if len(m.IMU) < 2 {
		t.Fatalf("len(IMU) = %d", len(m.IMU))
	}
	rate := float64(len(m.IMU)-1) / (m.IMU[len(m.IMU)-1].T - m.IMU[0].T)
	if rate < 500 || rate > 2000 {
		t.Errorf("IMU rate = %.1f Hz, want 500-2000", rate)
	}
	t.Logf("%s %dx%d %v fps; lenses %+v; %d IMU samples at %.1f Hz from T=%.3f s", m.Model, m.Width, m.Height, m.FPS, m.Lenses, len(m.IMU), rate, m.IMU[0].T)
}
