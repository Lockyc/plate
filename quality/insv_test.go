package quality

import (
	"context"
	"fmt"
	"math"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lockyc/plate/internal/enginetest"
	"github.com/lockyc/plate/internal/insv"
	"github.com/lockyc/plate/internal/pano"
)

func TestInsvPairStep(t *testing.T) {
	tmp := t.TempDir()
	log := enginetest.Stub(t, "ffmpeg", `for a; do last=$a; done; printf video > "$last"`)
	env := Env{Tmp: tmp}
	step := []string{"insv-pair", "--size", "1024", "--seconds", "2", "--roll", "10", "--pitch", "20", "{tmp:src.jpg}", "{tmp:VID_20260101_120000_00_001.insv}"}
	if err := env.step(context.Background(), step); err != nil {
		t.Fatal(err)
	}
	calls := enginetest.Calls(t, log)
	if len(calls) != 2 {
		t.Fatalf("%d ffmpeg calls, want one a lens", len(calls))
	}
	fov, _ := pano.LensFOV("Insta360 X3")
	for i, c := range calls {
		args := strings.Join(c, " ")
		for _, w := range []string{fmt.Sprintf("h_fov=%g:", fov), []string{"yaw=0:", "yaw=180:"}[i], []string{"_00_", "_10_"}[i]} {
			if !strings.Contains(args, w) {
				t.Errorf("lens %d: ffmpeg lacks %q: %s", i, w, args)
			}
		}
	}
	m, err := insv.Read(filepath.Join(tmp, "VID_20260101_120000_00_001.insv"))
	if err != nil {
		t.Fatal(err)
	}
	if m.Width != 1024 || m.Lenses[1].CX != 512 || m.Lenses[0].R != 512 || math.Abs(m.Lenses[1].Roll-math.Pi/2) > 1e-9 {
		t.Errorf("meta %+v", m)
	}
	if n := len(m.IMU); n < 2000 {
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
