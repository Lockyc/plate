package cmm

import (
	"context"
	"math"
	"math/rand/v2"
	"sync"
	"testing"

	"github.com/lockyc/plate/internal/cmm/gsref"
	"github.com/lockyc/plate/internal/engine"
	"github.com/lockyc/plate/internal/raster"
)

var loadFogra = sync.OnceValues(func() (*Profile, error) { return Load(fogra) })

func fogra39(t *testing.T) *Profile {
	t.Helper()
	p, err := loadFogra()
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// pixel converts one CMYK pixel and returns its sRGB as fractions of full scale.
func pixel(p *Profile, c [4]byte) [3]float64 {
	img := p.ToSRGB(raster.PAM{W: 1, H: 1, Pix: c[:]})
	px := img.RGBA64At(0, 0)
	return [3]float64{float64(px.R) / 65535, float64(px.G) / 65535, float64(px.B) / 65535}
}

func TestPaperWhite(t *testing.T) {
	for i, v := range pixel(fogra39(t), [4]byte{}) {
		if v < 0.97 {
			t.Errorf("channel %d of paper white = %.4f, want >= 0.97", i, v)
		}
	}
}

func TestBlackWithBPC(t *testing.T) {
	p := fogra39(t)
	var c [4]byte
	for i, v := range p.darkest {
		c[i] = byte(math.Round(v * 255))
	}
	if sum := int(c[0]) + int(c[1]) + int(c[2]) + int(c[3]); sum >= 4*255 {
		t.Fatalf("darkest colour %v is not ink-limited", c)
	}
	for i, v := range pixel(p, c) {
		if v > 0.02 {
			t.Errorf("channel %d of the darkest colour %v = %.4f, want <= 0.02", i, c, v)
		}
	}
}

// TestLabAgainstGS compares through sRGB, because Ghostscript has no Lab
// output device: the same patches through ToSRGB and through Ghostscript's
// own transform of the profile, both read back as Lab D50.
func TestLabAgainstGS(t *testing.T) {
	if _, err := engine.Resolve("gs"); err != nil {
		t.Skip(err)
	}
	p := fogra39(t)
	r := rand.New(rand.NewPCG(1, 2))
	patches := make([][4]byte, 50)
	for i := range patches {
		for k := range patches[i] {
			patches[i][k] = byte(r.IntN(256))
		}
	}
	ref, err := gsref.SRGB(context.Background(), fogra, patches)
	if err != nil {
		t.Fatal(err)
	}
	var sum float64
	for i, c := range patches {
		sum += DE2000(SRGBToLab(pixel(p, c)), SRGBToLab(ref[i]))
	}
	if mean := sum / float64(len(patches)); mean > 1.0 {
		t.Errorf("mean ΔE2000 against Ghostscript = %.3f, want <= 1.0", mean)
	}
}
