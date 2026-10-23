package video

import (
	"context"
	"image"
	"image/color"
	"math"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/lockyc/plate/internal/engine"
	"github.com/lockyc/plate/internal/enginetest"
	"github.com/lockyc/plate/internal/raster"
)

// subsample is img through a chroma-subsampled video format: converted to
// BT.709 YCbCr, Cb and Cr averaged over each n×n block, Y kept, and
// converted back. n = 2 is 4:2:0; n = 4 smears colour twice as far.
func subsample(img *image.RGBA64, n int) *image.RGBA64 {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	y, cb, cr := make([]float64, w*h), make([]float64, w*h), make([]float64, w*h)
	for j := 0; j < h; j++ {
		for i := 0; i < w; i++ {
			c := img.RGBA64At(i, j)
			r, g, bl := float64(c.R)/65535, float64(c.G)/65535, float64(c.B)/65535
			l := 0.2126*r + 0.7152*g + 0.0722*bl
			y[j*w+i], cb[j*w+i], cr[j*w+i] = l, (bl-l)/1.8556, (r-l)/1.5748
		}
	}
	out := image.NewRGBA64(image.Rect(0, 0, w, h))
	for j := 0; j < h; j += n {
		for i := 0; i < w; i += n {
			var k []int
			for dy := range n {
				for dx := range n {
					if i+dx < w && j+dy < h {
						k = append(k, (j+dy)*w+i+dx)
					}
				}
			}
			var mb, mr float64
			for _, p := range k {
				mb += cb[p] / float64(len(k))
				mr += cr[p] / float64(len(k))
			}
			for _, p := range k {
				r := y[p] + 1.5748*mr
				bl := y[p] + 1.8556*mb
				g := (y[p] - 0.2126*r - 0.0722*bl) / 0.7152
				out.SetRGBA64(p%w, p/w, color.RGBA64{clamp16(r * 65535), clamp16(g * 65535), clamp16(bl * 65535), 0xffff})
			}
		}
	}
	return out
}

// TestSharpnessCatchesChromaSmear: an encode that smears colour past what
// 4:2:0 itself does keeps a magenta line's luma and loses its colour, so
// luma Detail passes it and Sharpness, against the 4:2:0 reference, does
// not. The ground is mid grey rather than black: chroma spread onto black
// decodes to a negative green, whose clipping would change the luma too.
func TestSharpnessCatchesChromaSmear(t *testing.T) {
	ref := solid(32, 32, color.RGBA64{0x8000, 0x8000, 0x8000, 0xffff})
	for y := 0; y < 32; y++ {
		ref.SetRGBA64(9, y, color.RGBA64{0xffff, 0, 0xffff, 0xffff})
	}
	ref420, got := subsample(ref, 2), subsample(ref, 4)
	luma, err := raster.Detail(got, ref420, 1, raster.Luma)
	if err != nil || luma <= 0.95 {
		t.Fatalf("luma Detail = %v, %v; want > 0.95", luma, err)
	}
	if s, err := Sharpness([]*image.RGBA64{ref420}, []*image.RGBA64{got}); err != nil || s >= MinSharpness {
		t.Fatalf("Sharpness = %v, %v; want < MinSharpness", s, err)
	}
	if s, err := Sharpness([]*image.RGBA64{ref420, ref420}, []*image.RGBA64{ref420, got}); err != nil || s >= MinSharpness {
		t.Fatalf("Sharpness over two frames = %v, %v; want the worse frame's", s, err)
	}
}

func TestSharpnessSkipsFlatFrames(t *testing.T) {
	flat := solid(8, 8, color.RGBA64{0, 0, 0, 0xffff})
	if s, err := Sharpness([]*image.RGBA64{flat}, []*image.RGBA64{flat}); err != nil || s != 1 {
		t.Fatalf("Sharpness of flat frames = %v, %v; want 1", s, err)
	}
}

// TestSharpnessRefusesMismatchedFrames: a decode of the wrong size or
// count is an error, never a score that blames the encode.
func TestSharpnessRefusesMismatchedFrames(t *testing.T) {
	a, b := solid(8, 8, red), solid(4, 4, red)
	if s, err := Sharpness([]*image.RGBA64{a}, []*image.RGBA64{b}); err == nil {
		t.Fatalf("mismatched sizes scored %v", s)
	}
	if s, err := Sharpness([]*image.RGBA64{a, a}, []*image.RGBA64{a}); err == nil {
		t.Fatalf("mismatched counts scored %v", s)
	}
}

func TestSampleFrames(t *testing.T) {
	for n, want := range map[int][]int{1: {0}, 2: {0, 1}, 30: {0, 29}, 31: {0, 30}, 61: {0, 30, 60}, 75: {0, 30, 60, 74}} {
		if got := SampleFrames(n, LoopClose{}); !reflect.DeepEqual(got, want) {
			t.Errorf("SampleFrames(%d) = %v, want %v", n, got, want)
		}
	}
}

func TestSampleFramesSkipTheLoopClose(t *testing.T) {
	// The adding-nodes render with a dip and idle cuts: 512 frames, the
	// dip on 9 at each end. Frame 510, every 30th from 0, is 99.5% ground,
	// its detail faded by design, and scored the mp4 -0.012. Sharpness
	// samples only the frames the loop close left as composed.
	got := SampleFrames(512, LoopClose{Head: 9, Tail: 9})
	if got[0] != 9 || got[len(got)-1] != 502 {
		t.Fatalf("SampleFrames = %v, want 9 to 502", got)
	}
	for _, k := range got {
		if k < 9 || k > 502 {
			t.Fatalf("SampleFrames = %v samples loop-close frame %d", got, k)
		}
	}
	if got := SampleFrames(512, LoopClose{Tail: 18}); got[len(got)-1] != 493 {
		t.Fatalf("crossfade: SampleFrames = %v, want it to end at 493", got)
	}
}

// neonText is glyph-like strokes, about 39 px text's (3 px stems, rings
// 20 px across), in c on #0a0a12, anti-aliased by 4×4 supersampling.
func neonText(c color.RGBA64) *image.RGBA64 {
	const w, h = 240, 60
	ground := color.RGBA64{0x0a0a, 0x0a0a, 0x1212, 0xffff}
	ink := func(x, y float64) bool {
		for k := 0.0; k < 6; k++ {
			ox := 14 + k*38
			if d := math.Hypot(x-ox, y-30); d <= 10 && d >= 7 {
				return true
			}
			if x >= ox+12 && x < ox+15 && y >= 12 && y < 41 {
				return true
			}
			if y >= 44+math.Mod(k, 2) && y < 47+math.Mod(k, 2) && x >= ox-8 && x < ox+20 {
				return true
			}
		}
		return false
	}
	img := image.NewRGBA64(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			n := 0.0
			for sy := range 4 {
				for sx := range 4 {
					if ink(float64(x)+(float64(sx)+0.5)/4, float64(y)+(float64(sy)+0.5)/4) {
						n++
					}
				}
			}
			f := n / 16
			mix := func(a, b uint16) uint16 { return uint16(math.Round(float64(a)*(1-f) + float64(b)*f)) }
			img.SetRGBA64(x, y, color.RGBA64{mix(ground.R, c.R), mix(ground.G, c.G), mix(ground.B, c.B), 0xffff})
		}
	}
	return img
}

// realMP4 skips t unless ffmpeg with libx264 is installed.
func realMP4(t *testing.T) {
	t.Helper()
	if _, err := engine.Resolve("ffmpeg"); err != nil {
		t.Skip("no ffmpeg")
	}
	if err := RequireEncoders(context.Background(), []string{"mp4"}); err != nil {
		t.Skip("no libx264")
	}
}

// mp4Encoded is img through the mp4 encode and the decode Sharpness reads
// it back with.
func mp4Encoded(t *testing.T, img *image.RGBA64) *image.RGBA64 {
	t.Helper()
	dir := t.TempDir()
	SavePNG(filepath.Join(dir, "r00000.png"), img)
	b := img.Bounds()
	mp4 := filepath.Join(dir, "c.mp4")
	if err := Encode(context.Background(), "mp4", dir, 60, b.Dx(), b.Dy(), mp4); err != nil {
		t.Fatal(err)
	}
	got, err := ExtractEncoded(context.Background(), mp4, []int{0}, dir)
	if err != nil {
		t.Fatal(err)
	}
	return got[0]
}

// lossless420 is img through Lossless420: the Sharpness reference.
func lossless420(t *testing.T, img *image.RGBA64) *image.RGBA64 {
	t.Helper()
	dir := t.TempDir()
	SavePNG(filepath.Join(dir, "r00000.png"), img)
	out, err := Lossless420(context.Background(), dir, []int{0}, dir)
	if err != nil {
		t.Fatal(err)
	}
	return out[0]
}

// TestLossless420IsALosslessEncode: the reference is pixel for pixel what
// a lossless (qp 0) x264 encode through the mp4's own conversion decodes
// to, read back the way ExtractEncoded reads the real encode.
func TestLossless420IsALosslessEncode(t *testing.T) {
	realMP4(t)
	ref := neonText(color.RGBA64{0x0000, 0xe5e5, 0xffff, 0xffff})
	dir := t.TempDir()
	SavePNG(filepath.Join(dir, "r00000.png"), ref)
	mp4 := filepath.Join(dir, "qp0.mp4")
	args := append([]string{"-hide_banner", "-loglevel", "error", "-nostdin", "-framerate", "60", "-i", filepath.Join(dir, "r%05d.png"),
		"-vf", toYUV, "-c:v", "libx264", "-qp", "0", "-pix_fmt", "yuv420p"}, bt709...)
	if _, err := engine.Run(context.Background(), engine.Cmd{Engine: "ffmpeg", Args: append(args, mp4)}); err != nil {
		t.Fatal(err)
	}
	enc, err := ExtractEncoded(context.Background(), mp4, []int{0}, dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := lossless420(t, ref); !reflect.DeepEqual(got.Pix, enc[0].Pix) {
		t.Fatal("Lossless420 differs from a qp 0 encode's decode")
	}
}

// TestSharpnessPassesNeonText: neon text on near black keeps its detail
// through the mp4 encode, though one of its channels barely moves.
func TestSharpnessPassesNeonText(t *testing.T) {
	realMP4(t)
	for _, hex := range []uint32{0x00e5ff, 0xff2bd6, 0x39ff14, 0xffd400} {
		c := color.RGBA64{uint16(hex>>16) * 0x101, uint16(hex>>8&0xff) * 0x101, uint16(hex&0xff) * 0x101, 0xffff}
		ref := neonText(c)
		s, err := Sharpness([]*image.RGBA64{lossless420(t, ref)}, []*image.RGBA64{mp4Encoded(t, ref)})
		if err != nil || s < MinSharpness {
			t.Errorf("#%06x: Sharpness = %.3f, %v; want ≥ %v", hex, s, err, MinSharpness)
		}
	}
}

// neutralBesideCyan is a white block in a black strip above a cyan box on
// #0a0a12, as the clip-sharp-neutral-beside-cyan case frames them: neutral detail
// gives the red channel its energy while 4:2:0 adds red at the cyan edge.
func neutralBesideCyan() *image.RGBA64 {
	img := solid(600, 400, color.RGBA64{0x0a0a, 0x0a0a, 0x1212, 0xffff})
	for x := range 600 {
		v := uint16(0)
		if x >= 150 && x < 350 {
			v = 0xffff
		}
		for y := range 40 {
			img.SetRGBA64(x, y, color.RGBA64{v, v, v, 0xffff})
		}
	}
	for y := 151; y < 305; y++ {
		for x := 101; x < 255; x++ {
			img.SetRGBA64(x, y, color.RGBA64{0, 0xe5e5, 0xffff, 0xffff})
		}
	}
	return img
}

// TestSharpnessScoresTheEncoderNotTheFormat: neutral detail beside a
// saturated edge loses red to 4:2:0 itself, so a lossless 4:2:0 copy scored
// against the composed frame falls below MinSharpness. Scored against the
// 4:2:0 reference, the mp4 keeps its detail.
func TestSharpnessScoresTheEncoderNotTheFormat(t *testing.T) {
	realMP4(t)
	ref := neutralBesideCyan()
	ref420 := lossless420(t, ref)
	if s, err := Sharpness([]*image.RGBA64{ref}, []*image.RGBA64{ref420}); err != nil || s >= MinSharpness {
		t.Fatalf("lossless 4:2:0 against the composed frame = %.3f, %v; want < MinSharpness", s, err)
	} else {
		t.Logf("lossless 4:2:0 against the composed frame = %.3f", s)
	}
	if s, err := Sharpness([]*image.RGBA64{ref420}, []*image.RGBA64{mp4Encoded(t, ref)}); err != nil || s < MinSharpness {
		t.Fatalf("mp4 against the 4:2:0 reference = %.3f, %v; want ≥ MinSharpness", s, err)
	} else {
		t.Logf("mp4 against the 4:2:0 reference = %.3f", s)
	}
}

func TestSeamRefusesSizeMismatch(t *testing.T) {
	if _, err := Seam(solid(8, 8, grey(0)), solid(8, 6, grey(0))); err == nil {
		t.Fatal("no error for 8x8 against 8x6")
	}
}

func TestSharpnessScoresEveryVideoEncode(t *testing.T) {
	meas := Measurement{Formats: map[string]FormatResult{
		"mp4": {File: "n.mp4"}, "webm": {File: "n.webm"}, "gif": {File: "n.gif"}}}
	var scored []string
	err := scoreSharpness(&meas, func(file string) (float64, error) {
		scored = append(scored, file)
		return map[string]float64{"n.mp4": 0.9, "n.webm": 0.5}[file], nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(scored, []string{"n.mp4", "n.webm"}) {
		t.Fatalf("scored %q, want both video encodes", scored)
	}
	if meas.Sharpness != 0.5 || meas.Formats["mp4"].Sharpness != 0.9 || meas.Formats["webm"].Sharpness != 0.5 {
		t.Fatalf("measurement %+v", meas)
	}
	want := "webm sharpness 0.500 is below 0.85: the encode loses the frames' fine detail"
	if err := checkSharpness(meas); err == nil || err.Error() != want {
		t.Fatalf("checkSharpness = %v, want %q", err, want)
	}
	none := Measurement{Formats: map[string]FormatResult{"gif": {File: "n.gif"}}}
	if err := scoreSharpness(&none, nil); err != nil || none.Sharpness != 1 || checkSharpness(none) != nil {
		t.Fatalf("no video encode: %+v, %v", none, err)
	}
}

func TestExtractEncodedArgs(t *testing.T) {
	log := enginetest.Stub(t, "ffmpeg", `for a; do pat=$a; done
for i in 0 1 2; do cp "$RGB" "$(printf "$pat" $i)"; done`)
	rgb := filepath.Join(t.TempDir(), "c.png")
	writePNG(t, rgb, solid(4, 6, red))
	t.Setenv("RGB", rgb)
	frames, err := ExtractEncoded(context.Background(), "clip.mp4", []int{0, 30, 59}, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if len(frames) != 3 || frames[2].RGBA64At(1, 1) != red {
		t.Fatalf("frames = %d", len(frames))
	}
	got := enginetest.Calls(t, log)[0]
	want := []string{"-hide_banner", "-loglevel", "error", "-nostdin",
		"-i", "clip.mp4",
		"-vf", `select=eq(n\,0)+eq(n\,30)+eq(n\,59),scale=in_color_matrix=bt709:in_range=auto:flags=accurate_rnd+full_chroma_int+bilinear,format=rgb24`,
		"-fps_mode", "passthrough", "-start_number", "0", got[len(got)-1]}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("argv = %q\nwant   %q", got, want)
	}
	enginetest.Stub(t, "ffmpeg", `for a; do pat=$a; done
cp "$RGB" "$(printf "$pat" 0)"`)
	if _, err := ExtractEncoded(context.Background(), "clip.mp4", []int{0, 30}, t.TempDir()); err == nil || !strings.Contains(err.Error(), "decoded 1 of 2 frames") {
		t.Fatalf("short decode: err = %v", err)
	}
}
