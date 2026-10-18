package clip

import (
	"context"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"testing"
)

func writePNG(t *testing.T, path string, img image.Image) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := png.Encode(f, img); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

func readRGBA64(t *testing.T, path string) *image.RGBA64 {
	t.Helper()
	img, err := loadRGBA64(path)
	if err != nil {
		t.Fatal(err)
	}
	return img
}

// solid is a w×h image of one opaque colour.
func solid(w, h int, c color.RGBA64) *image.RGBA64 {
	img := image.NewRGBA64(image.Rect(0, 0, w, h))
	fill(img, img.Bounds(), c)
	return img
}

// sources writes imgs as master frames and returns their paths.
func sources(t *testing.T, imgs ...*image.RGBA64) []string {
	dir := t.TempDir()
	var paths []string
	for i, img := range imgs {
		p := filepath.Join(dir, fmt.Sprintf("m%05d.png", i))
		writePNG(t, p, img)
		paths = append(paths, p)
	}
	return paths
}

// nearRGB reports whether c is opaque and within tol/255 of want on every colour channel.
func nearRGB(c color.RGBA64, want color.RGBA64, tol float64) bool {
	d := func(a, b uint16) bool { return math.Abs(float64(a)-float64(b))/0xffff <= tol/255 }
	return d(c.R, want.R) && d(c.G, want.G) && d(c.B, want.B) && c.A == 0xffff
}

func checkRows(t *testing.T, img *image.RGBA64, y0, y1 int, want color.RGBA64, what string) {
	t.Helper()
	for y := y0; y < y1; y++ {
		for x := 0; x < img.Bounds().Dx(); x++ {
			if c := img.RGBA64At(x, y); !nearRGB(c, want, 2) {
				t.Fatalf("%s: (%d,%d) = %v, want %v", what, x, y, c, want)
			}
		}
	}
}

var red = color.RGBA64{0xffff, 0, 0, 0xffff}

func TestComposePlacesPins(t *testing.T) {
	src := solid(300, 600, grey(0.5))
	fill(src, image.Rect(0, 500, 300, 600), red)
	band := Rect{0, 500, 300, 100}
	lay := Layout{W: 150, H: 300, RegionH: 250, Pins: []PinSlot{{Name: "kb", System: true}}}
	up := View{Region: Rect{0, 0, 300, 500}, RegionY: 50, RegionH: 250,
		Pins: []PinView{{Name: "kb", Src: band, Y: 0, H: 50, Shown: 1}}}
	down := View{Region: Rect{0, 0, 250, 500}, RegionY: 0, RegionH: 300,
		Pins: []PinView{{Name: "kb", Src: band, Y: 0, H: 50, Shown: 0}}}
	// Ending as it starts keeps the loop crossfade out of the frames under test.
	refs, _, err := Compose(context.Background(), sources(t, src, src, src), 60, 1, lay, []View{up, down, up}, make([][]Op, 3), nil, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	shown := readRGBA64(t, refs[0])
	if shown.Bounds() != image.Rect(0, 0, 150, 300) {
		t.Fatalf("bounds = %v", shown.Bounds())
	}
	checkRows(t, shown, 0, 50, red, "pin strip")
	checkRows(t, shown, 50, 300, grey(0.5), "region")
	checkRows(t, readRGBA64(t, refs[1]), 0, 300, grey(0.5), "hidden pin's frame")
}

func TestComposeScalesFromPoints(t *testing.T) {
	src := solid(40, 40, grey(0))
	fill(src, image.Rect(20, 0, 40, 40), grey(1))
	lay := Layout{W: 20, H: 20, RegionH: 20}
	views := []View{{Region: Rect{10, 0, 10, 10}, RegionH: 20}}
	refs, _, err := Compose(context.Background(), sources(t, src), 60, 2, lay, views, make([][]Op, 1), nil, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	img := readRGBA64(t, refs[0])
	checkRows(t, img, 0, 20, grey(1), "region x 10..20 pt at 2 px/pt is the white right half")
}

func TestComposeOverlayOpacity(t *testing.T) {
	lay := Layout{W: 8, H: 8, RegionH: 8}
	views := []View{{Region: Rect{0, 0, 8, 8}, RegionH: 8}}
	ops := [][]Op{{{Key: "caption-0", Scale: 1, Opacity: 0.5}}}
	sprites := map[string]*image.RGBA64{"caption-0": solid(8, 8, grey(1))}
	refs, _, err := Compose(context.Background(), sources(t, solid(8, 8, grey(0))), 60, 1, lay, views, ops, sprites, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	checkRows(t, readRGBA64(t, refs[0]), 0, 8, grey(0.5), "white at 0.5 over black")
}

func TestComposeFullFrameSpriteLandsOnWholePixels(t *testing.T) {
	lay := Layout{W: 8, H: 12, RegionH: 12}
	views := []View{{Region: Rect{0, 0, 8, 12}, RegionH: 12}}
	caption := solid(8, 12, color.RGBA64{})
	fill(caption, image.Rect(2, 3, 6, 5), grey(1))
	ops := [][]Op{{{Key: "caption-0", Y: -1.5, Scale: 1, Opacity: 1}}}
	refs, _, err := Compose(context.Background(), sources(t, solid(8, 12, grey(0))), 60, 1, lay, views, ops,
		map[string]*image.RGBA64{"caption-0": caption}, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	img := readRGBA64(t, refs[0])
	// Y -1.5 rounds to -2 (half away from zero), as the region's rows round.
	for y := range 12 {
		for x := range 8 {
			want := 0.0
			if x >= 2 && x < 6 && y >= 1 && y < 3 {
				want = 1
			}
			if v := level(img, x, y); v != want {
				t.Fatalf("(%d,%d) = %.4f, want %g: the caption was resampled, not placed", x, y, v, want)
			}
		}
	}
}

func TestComposePointSpriteCentredAndScaled(t *testing.T) {
	lay := Layout{W: 40, H: 40, RegionH: 40}
	v := View{Region: Rect{0, 0, 40, 40}, RegionH: 40}
	// The sprite is rendered at the largest Op.Scale, 2: it is drawn 10 px
	// across there and 5 px at Op.Scale 1. The clip ends as it starts.
	full := []Op{{Key: "tap", X: 20, Y: 20, Scale: 2, Opacity: 1}}
	ops := [][]Op{full, {{Key: "tap", X: 20, Y: 20, Scale: 1, Opacity: 1}}, full}
	sprites := map[string]*image.RGBA64{"tap": solid(10, 10, grey(1))}
	black := solid(40, 40, grey(0))
	refs, _, err := Compose(context.Background(), sources(t, black, black, black), 60, 1, lay, []View{v, v, v}, ops, sprites, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	big, small := readRGBA64(t, refs[0]), readRGBA64(t, refs[1])
	for _, c := range []struct {
		img  *image.RGBA64
		x, y int
		want float64
	}{
		{big, 15, 20, 1}, {big, 24, 24, 1}, {big, 14, 20, 0}, {big, 25, 20, 0},
		{small, 20, 20, 1}, {small, 16, 20, 0}, {small, 23, 20, 0}, {small, 20, 23, 0},
	} {
		if v := level(c.img, c.x, c.y); math.Abs(v-c.want) > 2.0/255 {
			t.Errorf("(%d,%d) = %.3f, want %g", c.x, c.y, v, c.want)
		}
	}
}

func loopFixture(t *testing.T, first, last *image.RGBA64, n int) (frames []string, lay Layout, views []View) {
	imgs := make([]*image.RGBA64, n)
	for i := range imgs {
		imgs[i] = first
	}
	imgs[n-1] = last
	lay = Layout{W: 8, H: 8, RegionH: 8}
	views = make([]View, n)
	for i := range views {
		views[i] = View{Region: Rect{0, 0, 8, 8}, RegionH: 8}
	}
	return sources(t, imgs...), lay, views
}

func TestComposeCrossfadesWhenSeamDirty(t *testing.T) {
	black, white := solid(8, 8, grey(0)), solid(8, 8, grey(1))
	frames, lay, views := loopFixture(t, black, white, 60)
	refs, n, err := Compose(context.Background(), frames, 60, 1, lay, views, make([][]Op, 60), nil, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if n != 18 {
		t.Fatalf("crossfade = %d, want 18", n)
	}
	first, last := readRGBA64(t, refs[0]), readRGBA64(t, refs[59])
	if v, want := level(last, 3, 3), 1.0/19; math.Abs(v-want) > 1.0/255 {
		t.Errorf("last frame = %.4f, want white at weight 1/19 = %.4f", v, want)
	}
	if first.RGBA64At(3, 3) == last.RGBA64At(3, 3) {
		t.Error("the last frame equals the first")
	}
	if v := level(readRGBA64(t, refs[41]), 3, 3); v != 0 {
		t.Errorf("frame 41, before the crossfade, = %.4f", v)
	}
	after, err := Seam(last, first)
	if err != nil {
		t.Fatal(err)
	}
	before, err := Seam(white, first)
	if err != nil {
		t.Fatal(err)
	}
	if after >= before {
		t.Errorf("seam after crossfade %.2f%%, before %.2f%%", after, before)
	}
}

func TestComposeCleanLoopUntouched(t *testing.T) {
	// 8-bit levels, so the 8-bit output can match the source exactly.
	a := solid(8, 8, grey(51.0/255))
	fill(a, image.Rect(0, 0, 4, 8), grey(230.0/255))
	b := solid(8, 8, grey(153.0/255))
	frames, lay, views := loopFixture(t, a, a, 30)
	frames[10] = sources(t, b)[0]
	refs, n, err := Compose(context.Background(), frames, 60, 1, lay, views, make([][]Op, 30), nil, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("crossfade = %d, want 0", n)
	}
	for i, r := range refs {
		want := readRGBA64(t, frames[i])
		got := readRGBA64(t, r)
		for j := range got.Pix {
			if got.Pix[j] != want.Pix[j] {
				t.Fatalf("frame %d changed", i)
			}
		}
	}
}

func TestComposeRefusesMismatchedInputs(t *testing.T) {
	_, _, err := Compose(context.Background(), sources(t, solid(8, 8, grey(0))), 60, 1, Layout{W: 8, H: 8, RegionH: 8}, nil, nil, nil, t.TempDir())
	if err == nil {
		t.Fatal("no error for 1 frame and 0 views")
	}
}

func TestSeamRefusesSizeMismatch(t *testing.T) {
	if _, err := Seam(solid(8, 8, grey(0)), solid(8, 6, grey(0))); err == nil {
		t.Fatal("no error for 8x8 against 8x6")
	}
}
