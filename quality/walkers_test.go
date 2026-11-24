package quality

import (
	"context"
	"image"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestTrackWalkersStep: the helper writes a pano-like run, frame names and
// times in pano.tsv, every walker drawn at the start.
func TestTrackWalkersStep(t *testing.T) {
	tmp := t.TempDir()
	env := Env{Tmp: tmp, Assets: "assets"}
	step := []string{"track-walkers", "--every", "0.5", "--seconds", "1.5", "--width", "720", "{asset:crowd-equirect-holder.jpg}", "{asset:track-walkers.png}", "{tmp:run}"}
	if err := env.step(context.Background(), step); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(tmp, "run", "pano.tsv"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	if len(lines) != 4 || lines[0] != "file\tt\tclock\tyaw\tpitch\troll" || !strings.HasPrefix(lines[2], "000000_500.jpg\t0.500\t") {
		t.Fatalf("pano.tsv %q", lines)
	}
	f, err := os.Open(filepath.Join(tmp, "run", "000001_000.jpg"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	img, _, err := image.Decode(f)
	if err != nil {
		t.Fatal(err)
	}
	if img.Bounds().Dx() != 720 || img.Bounds().Dy() != 360 {
		t.Errorf("frame %v", img.Bounds())
	}
	bg, _ := decode(filepath.Join("assets", "crowd-equirect-holder.jpg"))
	ground := resample(bg, bg.Bounds(), 720, 360)
	// walker 3 stands at lon -70, 4.5 m away: his middle differs from the ground
	x, y := int((-70.0/360+0.5)*720), 360/2+10
	r1, g1, b1, _ := img.At(x, y).RGBA()
	r2, g2, b2, _ := ground.At(x, y).RGBA()
	if absDiff(r1, r2)+absDiff(g1, g2)+absDiff(b1, b2) < 0x3000 {
		t.Errorf("no walker drawn at lon -70")
	}
	if err := env.step(context.Background(), []string{"track-walkers", "{tmp:x}", "{tmp:run2}"}); err == nil {
		t.Error("two arguments accepted")
	}
}

func absDiff(a, b uint32) uint32 {
	if a > b {
		return a - b
	}
	return b - a
}
