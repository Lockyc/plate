package clip

import (
	"image"
	"image/color"
	"reflect"
	"testing"

	"github.com/lockyc/plate/internal/raster"
)

// subsample420 is img through 4:2:0 video: converted to BT.709 YCbCr, Cb
// and Cr averaged over each 2×2 block, Y kept, and converted back.
func subsample420(img *image.RGBA64) *image.RGBA64 {
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
	for j := 0; j < h; j += 2 {
		for i := 0; i < w; i += 2 {
			k := []int{j*w + i, j*w + i + 1, (j+1)*w + i, (j+1)*w + i + 1}
			var mb, mr float64
			for _, n := range k {
				mb += cb[n] / 4
				mr += cr[n] / 4
			}
			for _, n := range k {
				r := y[n] + 1.5748*mr
				bl := y[n] + 1.8556*mb
				g := (y[n] - 0.2126*r - 0.0722*bl) / 0.7152
				out.SetRGBA64(n%w, n/w, color.RGBA64{clamp16(r * 65535), clamp16(g * 65535), clamp16(bl * 65535), 0xffff})
			}
		}
	}
	return out
}

// TestSharpnessCatchesChromaSmear: 4:2:0 keeps a magenta line's luma and
// smears its colour, so luma Detail passes it and Sharpness does not. The
// ground is mid grey rather than black: chroma spread onto black decodes to
// a negative green, whose clipping would change the luma too.
func TestSharpnessCatchesChromaSmear(t *testing.T) {
	ref := solid(32, 32, color.RGBA64{0x8000, 0x8000, 0x8000, 0xffff})
	for y := 0; y < 32; y++ {
		ref.SetRGBA64(9, y, color.RGBA64{0xffff, 0, 0xffff, 0xffff})
	}
	got := subsample420(ref)
	luma, err := raster.Detail(got, ref, 1, raster.Luma)
	if err != nil || luma <= 0.95 {
		t.Fatalf("luma Detail = %v, %v; want > 0.95", luma, err)
	}
	if s, err := Sharpness([]*image.RGBA64{ref}, []*image.RGBA64{got}); err != nil || s >= 0.8 {
		t.Fatalf("Sharpness = %v, %v; want < 0.8", s, err)
	}
	if s, err := Sharpness([]*image.RGBA64{ref, ref}, []*image.RGBA64{ref, got}); err != nil || s >= 0.8 {
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
		if got := SampleFrames(n); !reflect.DeepEqual(got, want) {
			t.Errorf("SampleFrames(%d) = %v, want %v", n, got, want)
		}
	}
}
