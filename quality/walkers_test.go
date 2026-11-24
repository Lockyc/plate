package quality

import (
	"context"
	"image"
	"math"
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
	// walker 9 stands at lon -90, 5 m away: her middle differs from the ground
	x, y := int((-90.0/360+0.5)*720), 360/2+10
	r1, g1, b1, _ := img.At(x, y).RGBA()
	r2, g2, b2, _ := ground.At(x, y).RGBA()
	if absDiff(r1, r2)+absDiff(g1, g2)+absDiff(b1, b2) < 0x3000 {
		t.Errorf("no walker drawn at lon -90")
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

// TestWalkerPaths pins what the scene is built to test: walkers turning at
// up to 5-15 degrees a second, no one lapping the sphere, two returners
// coming back within a minute and far from where they left, and only the
// crossing pair ever overlapping.
func TestWalkerPaths(t *testing.T) {
	wrap := func(d float64) float64 { return math.Mod(d+540, 360) - 180 }
	size := func(d float64) float64 {
		return (math.Atan(camHeight/d) + math.Atan((personHeight-camHeight)/d)) * 180 / math.Pi
	}
	fastest, tracks := 0.0, 0
	for i, segs := range walkerPaths {
		for _, s := range segs {
			tracks++
			if l, d := s.at(s.start); math.Abs(wrap(l-s.lon0)) > 1e-9 || math.Abs(d-s.dist0) > 1e-9 {
				t.Errorf("walker %d starts at %v, %v", i, l, d)
			}
			turned := 0.0
			for tt := s.start; tt+0.5 < s.end; tt += 0.5 {
				a, _ := s.at(tt)
				b, _ := s.at(tt + 0.5)
				fastest = math.Max(fastest, math.Abs(wrap(b-a))/0.5)
				turned += math.Abs(wrap(b - a))
			}
			if turned >= 180 {
				t.Errorf("walker %d turns %v degrees", i, turned)
			}
		}
		if len(segs) == 2 {
			l0, _ := segs[0].at(segs[0].end)
			l1, _ := segs[1].at(segs[1].start)
			if gap := segs[1].start - segs[0].end; gap <= 4 || gap > 60 || math.Abs(wrap(l1-l0)) < 90 {
				t.Errorf("walker %d returns after %v s, %v degrees round", i, gap, wrap(l1-l0))
			}
		}
	}
	if fastest < 5 || fastest > 15 {
		t.Errorf("fastest walker turns %v degrees a second", fastest)
	}
	if tracks != 12 {
		t.Errorf("%d tracks", tracks)
	}
	for tt := 0.0; tt < 40; tt += 0.5 {
		type at struct {
			i         int
			lon, dist float64
		}
		var in []at
		for i, segs := range walkerPaths {
			for _, s := range segs {
				if tt >= s.start && tt < s.end {
					l, d := s.at(tt)
					in = append(in, at{i, l, d})
				}
			}
		}
		for a := range in {
			for b := a + 1; b < len(in); b++ {
				p, q := in[a], in[b]
				if math.Abs(wrap(p.lon-q.lon)) < 0.2*(size(p.dist)+size(q.dist)) && !(p.i == 3 && q.i == 4) {
					t.Errorf("walkers %d and %d overlap at %v s", p.i, q.i, tt)
				}
			}
		}
	}
}
