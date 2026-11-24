package pano

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"math"
	"testing"

	"github.com/lockyc/plate/internal/insv"
)

// idealX3 is an X3 as its trailer states one with perfect lenses: both at
// yaw 0 and roll 90°, centred in a 2880² frame.
func idealX3() insv.Meta {
	l := insv.Lens{CX: 1440, CY: 1440, R: 1440, Roll: math.Pi / 2}
	return insv.Meta{Model: "Insta360 X3", Width: 2880, Height: 2880, FPS: 30, Lenses: [2]insv.Lens{l, l}}
}

// at returns the equirect pixel nearest (lon, lat) in degrees.
func at(w, h int, lon, lat float64) int {
	i := int(math.Floor((lon/360 + 0.5) * float64(w)))
	j := int(math.Floor((0.5 - lat/180) * float64(h)))
	return min(max(j, 0), h-1)*w + min(max(i, 0), w-1)
}

func TestMapsIdentityLens(t *testing.T) {
	xm, ym, mask, w, h, err := Maps(idealX3(), 2048)
	if err != nil {
		t.Fatal(err)
	}
	if w != 2048 || h != 1024 || len(xm) != 2*w*h || len(ym) != 2*w*h || len(mask) != w*h {
		t.Fatalf("w %d h %d: %d %d %d values", w, h, len(xm), len(ym), len(mask))
	}
	near := func(name string, got uint16, want float64) {
		t.Helper()
		// One equirect pixel spans 360/w degrees, R/(FOV/2) source pixels a degree.
		if math.Abs(float64(got)-want) > 0.5+360/float64(w)*1440/(lensFOV["Insta360 X3"]/2) {
			t.Errorf("%s = %d, want %.1f", name, got, want)
		}
	}
	front := at(w, h, 0, 0)
	near("front x at lon 0", xm[front], 1440)
	near("front y at lon 0", ym[front], 1440)
	back := w*h + at(w, h, 180-0.01, 0)
	near("back x at lon 180", xm[back], 1440)
	near("back y at lon 180", ym[back], 1440)

	m := func(lon float64) float64 { return float64(mask[at(w, h, lon, 0)]) / math.MaxUint16 }
	if v := m(0); v != 1 {
		t.Errorf("mask at lon 0 = %.3f, want 1", v)
	}
	if v := m(179.9); v != 0 {
		t.Errorf("mask at lon 180 = %.3f, want 0", v)
	}
	if v := m(90); math.Abs(v-0.5) > 0.02 {
		t.Errorf("mask at lon 90 = %.3f, want 0.5", v)
	}
}

// The equirect's right is the front lens's right and its up the lens
// picture's up; looking out of the back lens, its right is the equirect's
// further right.
func TestMapsOrientation(t *testing.T) {
	xm, ym, _, w, h, err := Maps(idealX3(), 2048)
	if err != nil {
		t.Fatal(err)
	}
	if p := at(w, h, 30, 0); xm[p] <= 1440 || math.Abs(float64(ym[p])-1440) > 2 {
		t.Errorf("lon 30: front pixel (%d, %d), want right of centre", xm[p], ym[p])
	}
	if p := at(w, h, 0, 30); ym[p] >= 1440 || math.Abs(float64(xm[p])-1440) > 2 {
		t.Errorf("lat 30: front pixel (%d, %d), want above centre", xm[p], ym[p])
	}
	if p := w*h + at(w, h, -150, 0); xm[p] <= 1440 {
		t.Errorf("lon -150 (30 right of the back axis): back x %d, want right of centre", xm[p])
	}
}

// Equidistant: the image circle's edge is FOV/2 off the axis, and a pixel
// beyond FOV/2 is outside every source frame.
func TestMapsEquidistant(t *testing.T) {
	m := idealX3()
	fov := lensFOV[m.Model]
	xm, _, _, w, h, err := Maps(m, 4096)
	if err != nil {
		t.Fatal(err)
	}
	half := fov / 2
	got := float64(xm[at(w, h, half/2, 0)]) - 1440
	if want := 1440 * 0.5; math.Abs(got-want) > 2 {
		t.Errorf("x at FOV/4 = %.0f from centre, want %.0f", got, want)
	}
	if v := xm[at(w, h, half+1, 0)]; int(v) < m.Width {
		t.Errorf("x beyond the FOV = %d, want outside the frame", v)
	}
}

func TestMapsUnknownModel(t *testing.T) {
	m := idealX3()
	m.Model = "Insta360 X9"
	if _, _, _, _, _, err := Maps(m, 1024); err == nil {
		t.Error("an unknown model's lenses were mapped")
	}
}

func TestPGM(t *testing.T) {
	var b bytes.Buffer
	if err := writePGM(&b, []uint16{1, 0x0203, 65535, 0}, 2, 2); err != nil {
		t.Fatal(err)
	}
	want := "P5\n2 2\n65535\n\x00\x01\x02\x03\xff\xff\x00\x00"
	if b.String() != want {
		t.Errorf("pgm = %q, want %q", b.String(), want)
	}
}

// Where one lens has no pixel the other takes the whole blend, overlap or
// not: a lens centred far off its frame's middle loses its far side.
func TestMaskWhereALensHasNoPixel(t *testing.T) {
	for k, c := range []struct {
		lon  float64
		want uint16
	}{{-88, 0}, {92, math.MaxUint16}} {
		m := idealX3()
		m.Lenses[k].CX = 400
		xm, _, mask, w, h, err := Maps(m, 2048)
		if err != nil {
			t.Fatal(err)
		}
		p := at(w, h, c.lon, 0)
		if xm[k*w*h+p] != outside {
			t.Fatalf("lens %d has a pixel at lon %v", k, c.lon)
		}
		if mask[p] != c.want {
			t.Errorf("lens %d off its frame: mask at lon %v = %d, want %d", k, c.lon, mask[p], c.want)
		}
	}
}

// mapsSum pins Maps' output for a small ideal X3. A change to Maps' output
// changes it; when it does, bump mapsFormat (the map cache would otherwise
// serve maps the old code built) and set mapsSum to the new sum.
const mapsSum = "021a54d8608600b414616d18f94e45136b857c50b5b88b179b9b22b35727807e"

func TestMapsFormatPinned(t *testing.T) {
	// An off-centre lens and small residual turns, so every part of Maps counts.
	m := idealX3()
	m.Lenses[0].CX, m.Lenses[0].Yaw = 1000, 0.01
	m.Lenses[1].Pitch, m.Lenses[1].Roll = -0.01, m.Lenses[1].Roll+0.02
	xm, ym, mask, _, _, err := Maps(m, 64)
	if err != nil {
		t.Fatal(err)
	}
	h := sha256.New()
	for _, v := range [][]uint16{xm, ym, mask} {
		binary.Write(h, binary.LittleEndian, v)
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != mapsSum {
		t.Errorf("Maps' output changed (sum %s, pinned %s, mapsFormat %d): bump mapsFormat so cached maps are rebuilt, and set mapsSum to the new sum", got, mapsSum, mapsFormat)
	}
}
