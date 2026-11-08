// An external test package: video, which encodes the test clips, imports
// diff.
package diff_test

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/color"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strings"
	"testing"

	. "github.com/lockyc/plate/internal/diff"
	"github.com/lockyc/plate/internal/engine"
	"github.com/lockyc/plate/internal/raster"
	"github.com/lockyc/plate/internal/video"
)

// noise is a w×h image of random opaque pixels from seed.
func noise(w, h int, seed uint64) *image.RGBA64 {
	r := rand.New(rand.NewPCG(seed, 1))
	img := image.NewRGBA64(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{uint8(r.IntN(256)), uint8(r.IntN(256)), uint8(r.IntN(256)), 255})
		}
	}
	return img
}

// clip encodes frames as an mp4 at 30 fps in its own directory and returns
// its path. It skips t without ffmpeg and libx264.
func clip(t *testing.T, name string, frames []*image.RGBA64) string {
	t.Helper()
	if _, err := engine.Resolve("ffmpeg"); err != nil {
		t.Skip("no ffmpeg")
	}
	if err := video.RequireEncoders(context.Background(), []string{"mp4"}); err != nil {
		t.Skip("no libx264")
	}
	dir := filepath.Join(t.TempDir(), name)
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for i, f := range frames {
		if err := video.SavePNG(filepath.Join(dir, fmt.Sprintf("r%05d.png", i)), f); err != nil {
			t.Fatal(err)
		}
	}
	b := frames[0].Bounds()
	out := filepath.Join(dir, name+".mp4")
	if err := video.Encode(context.Background(), "mp4", dir, 30, b.Dx(), b.Dy(), out); err != nil {
		t.Fatal(err)
	}
	return out
}

func frames(n int, seed uint64) []*image.RGBA64 {
	fs := make([]*image.RGBA64, n)
	for i := range fs {
		fs[i] = noise(64, 48, seed+uint64(i))
	}
	return fs
}

func TestDiffClips(t *testing.T) {
	base := frames(6, 10)
	pa := clip(t, "a", base)
	same := clip(t, "same", base)
	changed := append([]*image.RGBA64{}, base...)
	changed[3] = noise(64, 48, 99)
	pc := clip(t, "changed", changed)
	short := clip(t, "short", base[:5])

	var out, errb bytes.Buffer
	run := func(args ...string) int {
		out.Reset()
		errb.Reset()
		return Main(context.Background(), args, &out, &errb)
	}
	if code := run("--threshold", "0", "--max-shift", "0", pa, same); code != 0 {
		t.Fatalf("same clip: code %d: %s %s", code, out.String(), errb.String())
	}
	if !strings.HasPrefix(out.String(), "ok 0.0% differ at worst (frame 0 of 6, threshold 0.0%") {
		t.Errorf("same clip: stdout %q", out.String())
	}
	pd := filepath.Join(t.TempDir(), "d.png")
	if code := run("--threshold", "1", "--max-shift", "0", "--out", pd, pa, pc); code != 1 {
		t.Fatalf("changed frame: code %d: %s %s", code, out.String(), errb.String())
	}
	if !strings.HasPrefix(out.String(), "OVER ") || !strings.Contains(out.String(), "(frame 3 of 6,") {
		t.Errorf("changed frame: stdout %q", out.String())
	}
	if _, err := raster.Load(pd); err != nil {
		t.Errorf("no picture of the worst frame: %v", err)
	}
	if code := run(pa, short); code != 1 || !strings.Contains(out.String(), "6 frames and 5") {
		t.Errorf("frame counts: code %d, stdout %q", code, out.String())
	}
	img := filepath.Join(t.TempDir(), "a.png")
	raster.SavePNG(img, base[0])
	if code := run(pa, img); code != 2 || !strings.Contains(errb.String(), "two clips or two images") {
		t.Errorf("clip and image: code %d, stderr %q", code, errb.String())
	}
}
