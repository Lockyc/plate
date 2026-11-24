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
	return clipWith(t, m, insvtest.Options{Offset: offset})
}

func clipWith(t *testing.T, m insv.Meta, o insvtest.Options) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "VID_20260101_000000_00_001.insv")
	if err := os.WriteFile(path, []byte("not really video"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := insvtest.Write(path, m, o); err != nil {
		t.Fatal(err)
	}
	return path
}

// patch rewrites the file at path through fn.
func patch(t *testing.T, path string, fn func(b []byte)) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	fn(b)
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestAxes(t *testing.T) {
	x3 := insv.Axes["Insta360 X3"]
	// yzX: camera x = −sensor y, camera y = −sensor z, camera z = sensor x.
	if got := x3.ToCamera([3]float64{1, 2, 3}); got != [3]float64{-2, -3, 1} {
		t.Errorf("ToCamera(1,2,3) = %v, want [-2 -3 1]", got)
	}
	if got := x3.ToSensor(x3.ToCamera([3]float64{1, 2, 3})); got != [3]float64{1, 2, 3} {
		t.Errorf("ToSensor(ToCamera(1,2,3)) = %v", got)
	}
}

// TestReadRawIMU writes the X3's raw layout and checks both the counts on
// disk (sensor axes) and what Read makes of them (camera axes).
func TestReadRawIMU(t *testing.T) {
	acc, gyro := insv.RawScales(32, 2000)
	if !near(acc, insv.StandardGravity/1024) || !near(gyro, 2000.0/32768*math.Pi/180) {
		t.Fatalf("RawScales(32, 2000) = %v, %v", acc, gyro)
	}
	// Upright and turning about the front lens's axis: sensor gyro -y alone,
	// 1000 counts; gravity's reaction on sensor -x, 1024 counts (1 g at 32 g).
	sample := insv.IMU{T: 0.002, Gyro: [3]float64{1000 * gyro, 0, 0}, Accel: [3]float64{0, 0, -insv.StandardGravity}}
	m := insv.Meta{Model: "Insta360 X3", Width: 2880, Height: 2880, FPS: 30, IMU: []insv.IMU{sample}}
	path := clipWith(t, m, insvtest.Options{Offset: testOffset, Raw: true, AccRange: 32, GyroRange: 2000})

	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	trailer := int(binary.LittleEndian.Uint32(b[len(b)-insv.FooterSize+32:]))
	rec := b[len(b)-trailer:] // the IMU is the trailer's first record
	if ts := binary.LittleEndian.Uint64(rec); ts != 2000 {
		t.Errorf("timestamp = %d µs, want 2000", ts)
	}
	wantCounts := []int{-1024, 0, 0, 0, -1000, 0} // accel xyz, gyro xyz
	for k, want := range wantCounts {
		if c := int(binary.LittleEndian.Uint16(rec[8+2*k:])) - insv.RawZero; c != want {
			t.Errorf("count %d = %d, want %d", k, c, want)
		}
	}

	got, err := insv.Read(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.IMU) != 1 {
		t.Fatalf("len(IMU) = %d", len(got.IMU))
	}
	g := got.IMU[0]
	if !near(g.T, 0.002) || !near(g.Gyro[0], sample.Gyro[0]) || g.Gyro[1] != 0 || g.Gyro[2] != 0 ||
		!near(g.Accel[2], -insv.StandardGravity) || g.Accel[0] != 0 || g.Accel[1] != 0 {
		t.Errorf("IMU[0] = %+v, want %+v", g, sample)
	}
}

// x3UpCounts are raw IMU counts (accel xyz, gyro xyz, as the sensor wrote
// them) from a real X3 clip, 40 ms apart, held still with lens 0 (_00_)
// pointing up about 32° off straight up: its picture is sky with the horizon
// near its edge.
var x3UpCounts = [][6]uint16{
	{32115, 31715, 32806, 32659, 32347, 32633},
	{32180, 31515, 33093, 32738, 32390, 32739},
	{32208, 31816, 33007, 32727, 32737, 32884},
	{32201, 31930, 32862, 32731, 32898, 32823},
	{32239, 32024, 32853, 32534, 32844, 32786},
}

// Spliced into a trailer by bytes, so Axes is tested only against what the
// camera wrote: up must read along +x.
func TestAxesRealCounts(t *testing.T) {
	m := insv.Meta{Model: "Insta360 X3", Width: 2880, Height: 2880, FPS: 30}
	for i := range x3UpCounts {
		m.IMU = append(m.IMU, insv.IMU{T: float64(i) * 0.04})
	}
	path := clipWith(t, m, insvtest.Options{Offset: testOffset, Raw: true, AccRange: 32, GyroRange: 2000})
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	trailer := int(binary.LittleEndian.Uint32(b[len(b)-insv.FooterSize+32:]))
	rec := b[len(b)-trailer:] // the IMU is the trailer's first record
	for i, s := range x3UpCounts {
		for k, c := range s {
			binary.LittleEndian.PutUint16(rec[i*insv.RawSampleSize+8+2*k:], c)
		}
	}
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := insv.Read(path)
	if err != nil {
		t.Fatal(err)
	}
	var a [3]float64
	for _, s := range got.IMU {
		for k := range a {
			a[k] += s.Accel[k] / float64(len(got.IMU))
		}
	}
	n := math.Sqrt(a[0]*a[0] + a[1]*a[1] + a[2]*a[2])
	if math.Abs(n-insv.StandardGravity) > 0.2*insv.StandardGravity || a[0]/n < 0.8 {
		t.Errorf("mean accel %.2f (|a| %.2f): want up, about g, along +x", a, n)
	}
}

func TestReadFPS(t *testing.T) {
	m := insv.Meta{Model: "Insta360 X3", Width: 2880, Height: 2880, FPS: 30000.0 / 1001}
	got, err := insv.Read(clipWith(t, m, insvtest.Options{Offset: testOffset, Raw: true, AccRange: 32, GyroRange: 2000, Frames: 300}))
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(got.FPS-m.FPS) > 1e-3 {
		t.Errorf("FPS = %v, want %v", got.FPS, m.FPS)
	}
}

func TestMalformedProtobuf(t *testing.T) {
	path := clip(t, insv.Meta{Model: "Insta360 X3", Width: 2880, Height: 2880, FPS: 30}, testOffset)
	patch(t, path, func(b []byte) {
		// The metadata is the last record: its descriptor sits before the footer.
		desc := len(b) - insv.FooterSize - insv.DescriptorSize
		n := int(binary.LittleEndian.Uint32(b[desc+2:]))
		for i := desc - n; i < desc; i++ {
			b[i] = 0xff // a varint key that never ends
		}
	})
	if _, err := insv.Read(path); err == nil || !strings.Contains(err.Error(), "metadata") {
		t.Fatalf("err = %v, want a metadata error", err)
	}
}

func TestFooterLargerThanFile(t *testing.T) {
	path := clip(t, insv.Meta{Model: "Insta360 X3", Width: 2880, Height: 2880, FPS: 30}, testOffset)
	patch(t, path, func(b []byte) {
		binary.LittleEndian.PutUint32(b[len(b)-insv.FooterSize+32:], uint32(len(b)+1))
	})
	if _, err := insv.Read(path); err == nil || !strings.Contains(err.Error(), "trailer") {
		t.Fatalf("err = %v, want a trailer-size error", err)
	}
}

func TestRecordOverLimit(t *testing.T) {
	// A metadata record of 17 MiB of zeros: structurally whole, too big to take in.
	n := 17 << 20
	b := make([]byte, n, n+insv.DescriptorSize+insv.FooterSize)
	b = append(b, insv.FormatProtobuf, insv.RecordMetadata)
	b = binary.LittleEndian.AppendUint32(b, uint32(n))
	b = append(b, make([]byte, 32)...)
	b = binary.LittleEndian.AppendUint32(b, uint32(len(b)+insv.FooterSize-32))
	b = binary.LittleEndian.AppendUint32(b, 3)
	b = append(b, insv.Magic...)
	path := filepath.Join(t.TempDir(), "big.insv")
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := insv.Read(path); err == nil || !strings.Contains(err.Error(), "over the") {
		t.Fatalf("err = %v, want the record refused for its size", err)
	}
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
	var mean [3]float64
	for _, s := range m.IMU {
		for k := range mean {
			mean[k] += s.Accel[k] / float64(len(m.IMU))
		}
	}
	t.Logf("%s %dx%d %.4f fps; lenses %+v; %d IMU samples at %.1f Hz from T=%.3f s; mean accel %.2f m/s²",
		m.Model, m.Width, m.Height, m.FPS, m.Lenses, len(m.IMU), rate, m.IMU[0].T, mean)
}
