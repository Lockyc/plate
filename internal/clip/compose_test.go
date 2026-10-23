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

	"github.com/lockyc/plate/internal/video"
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
	img, err := video.LoadRGBA64(path)
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
	refs, _, err := Compose(context.Background(), sources(t, src, src, src), 60, 1, lay, []View{up, down, up}, make([][]Op, 3), nil, "crossfade", t.TempDir())
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
	refs, _, err := Compose(context.Background(), sources(t, src), 60, 2, lay, views, make([][]Op, 1), nil, "crossfade", t.TempDir())
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
	refs, _, err := Compose(context.Background(), sources(t, solid(8, 8, grey(0))), 60, 1, lay, views, ops, sprites, "crossfade", t.TempDir())
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
		map[string]*image.RGBA64{"caption-0": caption}, "crossfade", t.TempDir())
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
	refs, _, err := Compose(context.Background(), sources(t, black, black, black), 60, 1, lay, []View{v, v, v}, ops, sprites, "crossfade", t.TempDir())
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
	refs, n, err := Compose(context.Background(), frames, 60, 1, lay, views, make([][]Op, 60), nil, "crossfade", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if n != (video.LoopClose{Tail: 18}) {
		t.Fatalf("loop close = %+v, want an 18-frame tail", n)
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
	after, err := video.Seam(last, first)
	if err != nil {
		t.Fatal(err)
	}
	before, err := video.Seam(white, first)
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
	for _, loop := range video.Loops {
		refs, n, err := Compose(context.Background(), frames, 60, 1, lay, views, make([][]Op, 30), nil, loop, t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		if n != (video.LoopClose{}) {
			t.Fatalf("%s: loop close = %+v, want none", loop, n)
		}
		for i, r := range refs {
			want := readRGBA64(t, frames[i])
			got := readRGBA64(t, r)
			for j := range got.Pix {
				if got.Pix[j] != want.Pix[j] {
					t.Fatalf("%s: frame %d changed", loop, i)
				}
			}
		}
	}
}

func TestComposeDipsWhenSeamDirty(t *testing.T) {
	// The start shows a pale block on the left, the end the block on the
	// right: a row that moved. 8-bit levels, so the output can match exactly.
	g, pale := grey(26.0/255), grey(230.0/255)
	left, right := image.Rect(0, 0, 3, 8), image.Rect(5, 0, 8, 8)
	start, end := solid(8, 8, g), solid(8, 8, g)
	fill(start, left, pale)
	fill(end, right, pale)
	frames, lay, views := loopFixture(t, start, end, 60)
	for k := 30; k < 60; k++ {
		frames[k] = frames[59]
	}
	refs, lc, err := Compose(context.Background(), frames, 60, 1, lay, views, make([][]Op, 60), nil, "dip", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if lc != (video.LoopClose{Head: 9, Tail: 9}) {
		t.Fatalf("loop close = %+v, want 9 head and 9 tail frames: 300 ms at 60 fps", lc)
	}
	for _, k := range []int{0, 59} {
		img := readRGBA64(t, refs[k])
		for y := range 8 {
			for x := range 8 {
				if c := img.RGBA64At(x, y); !nearRGB(c, g, 0) {
					t.Fatalf("seam frame %d (%d,%d) = %v, want the ground %v", k, x, y, c, g)
				}
			}
		}
	}
	full := (230.0 - 26) / 255
	shows := func(img *image.RGBA64, r image.Rectangle) float64 { return level(img, r.Min.X+1, 3) - 26.0/255 }
	for k, r := range refs {
		img := readRGBA64(t, r)
		if a, b := shows(img, left), shows(img, right); a > 1.0/255 && b > 1.0/255 {
			t.Fatalf("frame %d shows the start's block (%.3f) and the end's (%.3f) at once", k, a, b)
		}
	}
	// Eased: the dip leaves the content slowly, not at a linear 1/9 a frame.
	if v := shows(readRGBA64(t, refs[51]), right); v < full*(1-1.0/18) {
		t.Errorf("frame 51, the dip's first, keeps %.3f of %.3f: a linear ramp, not eased", v, full)
	}
	if v := shows(readRGBA64(t, refs[9]), left); math.Abs(v-full) > 0.5/255 {
		t.Errorf("frame 9, after the dip, = %.3f, want the start untouched", v)
	}
	if v := shows(readRGBA64(t, refs[50]), right); math.Abs(v-full) > 0.5/255 {
		t.Errorf("frame 50, before the dip, = %.3f, want the end untouched", v)
	}
}

func TestComposeDipLeavesAFrameOnAShortClip(t *testing.T) {
	// 10 frames at 60 fps is shorter than the 18-frame dip: the dip must
	// still leave frame Head, the poster, as composed.
	g, pale := grey(26.0/255), grey(230.0/255)
	start, end := solid(8, 8, g), solid(8, 8, g)
	fill(start, image.Rect(0, 0, 3, 8), pale)
	fill(end, image.Rect(5, 0, 8, 8), pale)
	frames, lay, views := loopFixture(t, start, end, 10)
	refs, lc, err := Compose(context.Background(), frames, 60, 1, lay, views, make([][]Op, 10), nil, "dip", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if lc.Head+lc.Tail >= len(refs) {
		t.Fatalf("loop close = %+v over %d frames: no frame left as composed", lc, len(refs))
	}
	if v := level(readRGBA64(t, refs[lc.Head]), 1, 3); math.Abs(v-230.0/255) > 0.5/255 {
		t.Errorf("frame %d (Head) = %.3f, want the start untouched", lc.Head, v)
	}
}

func TestComposeRefusesMismatchedInputs(t *testing.T) {
	_, _, err := Compose(context.Background(), sources(t, solid(8, 8, grey(0))), 60, 1, Layout{W: 8, H: 8, RegionH: 8}, nil, nil, nil, "crossfade", t.TempDir())
	if err == nil {
		t.Fatal("no error for 1 frame and 0 views")
	}
}
