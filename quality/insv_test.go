package quality

import (
	"context"
	"math"
	"os"
	"path/filepath"
	"testing"

	"github.com/lockyc/plate/internal/insv"
)

func TestInsvTrailerStep(t *testing.T) {
	tmp := t.TempDir()
	clip := filepath.Join(tmp, "VID_20260101_120000_00_001.insv")
	if err := os.WriteFile(clip, []byte("video"), 0o644); err != nil {
		t.Fatal(err)
	}
	env := Env{Tmp: tmp}
	step := []string{"insv-trailer", "--size", "1024", "--seconds", "2", "--roll", "10", "--pitch", "20", "{tmp:VID_20260101_120000_00_001.insv}"}
	if err := env.step(context.Background(), step); err != nil {
		t.Fatal(err)
	}
	m, err := insv.Read(clip)
	if err != nil {
		t.Fatal(err)
	}
	if m.Width != 1024 || m.Lenses[1].CX != 512 || m.Lenses[0].R != 512 || math.Abs(m.Lenses[1].Roll-math.Pi/2) > 1e-9 {
		t.Errorf("meta %+v", m)
	}
	if n := len(m.IMU); n < 200 {
		t.Fatalf("%d IMU samples for 2 s", n)
	}
	g := insv.StandardGravity
	p, r := 20*math.Pi/180, 10*math.Pi/180
	want := [3]float64{g * math.Sin(p), -g * math.Sin(r) * math.Cos(p), -g * math.Cos(r) * math.Cos(p)}
	for k, v := range m.IMU[0].Accel {
		if math.Abs(v-want[k]) > 1e-6 {
			t.Errorf("accel %v, want %v", m.IMU[0].Accel, want)
			break
		}
	}
}
