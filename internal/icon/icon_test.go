package icon

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/lockyc/plate/internal/enginetest"
)

// stubChrome answers each screenshot with a mostly transparent PNG of the
// window's size, as render.PNGAlpha requires.
func stubChrome(t *testing.T) string {
	t.Helper()
	fix := t.TempDir()
	for _, px := range []int{16, 32, 64, 128, 256, 512, 1024, 100} {
		img := image.NewNRGBA(image.Rect(0, 0, px, px))
		img.Set(px/2, px/2, color.NRGBA{200, 40, 60, 255})
		f, _ := os.Create(filepath.Join(fix, fmt.Sprintf("%dx%d.png", px, px)))
		png.Encode(f, img)
		f.Close()
	}
	t.Setenv("FIXTURE_DIR", fix)
	return enginetest.Stub(t, "chrome-headless-shell", `for a in "$@"; do
  case "$a" in
    --window-size=*) size=$(echo "${a#--window-size=}" | tr , x) ;;
    --screenshot=*) out="${a#--screenshot=}" ;;
  esac
done
cp "$FIXTURE_DIR/$size.png" "$out"`)
}

func tile(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "tile.svg")
	os.WriteFile(p, []byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 1024 1024"><rect width="1024" height="1024" rx="229"/></svg>`), 0o644)
	return p
}

type entry struct {
	code string
	px   int
}

func parse(t *testing.T, b []byte) []entry {
	t.Helper()
	if string(b[:4]) != "icns" || int(binary.BigEndian.Uint32(b[4:8])) != len(b) {
		t.Fatalf("bad icns header % x, %d bytes", b[:8], len(b))
	}
	var es []entry
	for i := 8; i < len(b); {
		code, n := string(b[i:i+4]), int(binary.BigEndian.Uint32(b[i+4:i+8]))
		data := b[i+8 : i+n]
		i += n
		if px, ok := map[string]int{"ic04": 16, "ic05": 32}[code]; ok {
			if string(data[:4]) != "ARGB" {
				t.Fatalf("%s: not ARGB: % x", code, data[:4])
			}
			es = append(es, entry{code, px})
			continue
		}
		cfg, err := png.DecodeConfig(bytes.NewReader(data))
		if err != nil || cfg.Width != cfg.Height {
			t.Fatalf("%s: %v %dx%d", code, err, cfg.Width, cfg.Height)
		}
		es = append(es, entry{code, cfg.Width})
	}
	return es
}

func TestPackBits(t *testing.T) {
	run := bytes.Repeat([]byte{7}, 131)
	for _, c := range []struct {
		in, want []byte
	}{
		{[]byte{1}, []byte{0, 1}},
		{[]byte{1, 2, 2}, []byte{2, 1, 2, 2}},
		{[]byte{5, 5, 5}, []byte{0x80, 5}},
		{[]byte{1, 2, 9, 9, 9, 9}, []byte{1, 1, 2, 0x81, 9}},
		{run, []byte{0xff, 7, 0, 7}},
		{bytes.Repeat([]byte{1, 2}, 65), append([]byte{127}, append(bytes.Repeat([]byte{1, 2}, 64), 1, 1, 2)...)},
	} {
		if got := packBits(nil, c.in); !bytes.Equal(got, c.want) {
			t.Errorf("packBits(% x) = % x, want % x", c.in, got, c.want)
		}
	}
}

func TestIconWritesIcnsAndPNG(t *testing.T) {
	log := stubChrome(t)
	dir := t.TempDir()
	icns, hero := filepath.Join(dir, "app.icns"), filepath.Join(dir, "hero.png")
	var o, e bytes.Buffer
	if code := Main(context.Background(), []string{"--icns", icns, "--png", "256:" + hero, tile(t)}, &o, &e); code != 0 {
		t.Fatalf("code %d: %s", code, e.String())
	}
	b, _ := os.ReadFile(icns)
	var got []entry
	for _, s := range slots {
		got = append(got, entry{s.code, s.px})
	}
	if es := parse(t, b); !slices.Equal(es, got) {
		t.Errorf("entries %v\nwant %v", es, got)
	}
	f, _ := os.Open(hero)
	cfg, err := png.DecodeConfig(f)
	f.Close()
	if err != nil || cfg.Width != 256 {
		t.Errorf("hero: %v, %d px", err, cfg.Width)
	}
	// One render per distinct size: the PNG's 256 shares the .icns's.
	calls := enginetest.Calls(t, log)
	if len(calls) != 7 {
		t.Errorf("%d chrome calls, want 7", len(calls))
	}
}

func TestPagePlacesTileInGrid(t *testing.T) {
	for _, c := range []struct {
		px          int
		inset, side string
	}{{1024, "100px", "824px"}, {128, "12.5px", "103px"}} {
		b, err := pageFor("data:image/svg+xml;base64,eA==", c.px)
		if err != nil {
			t.Fatal(err)
		}
		s := string(b)
		if !strings.Contains(s, "left: "+c.inset) || !strings.Contains(s, "top: "+c.inset) || !strings.Contains(s, "width: "+c.side) {
			t.Errorf("%d px page does not place the tile at %s, %s wide:\n%s", c.px, c.inset, c.side, s)
		}
		if !strings.Contains(s, `src="data:image/svg&#43;xml;base64,eA=="`) {
			t.Errorf("%d px page lost the data URI:\n%s", c.px, s)
		}
	}
}

func TestIconRefuses(t *testing.T) {
	stubChrome(t)
	dir := t.TempDir()
	png := filepath.Join(dir, "x.png")
	cases := []struct {
		args []string
		want string
	}{
		{[]string{tile(t)}, "name an output"},
		{[]string{"--png", "0:" + png, tile(t)}, "PX:out.png"},
		{[]string{"--png", "2048:" + png, tile(t)}, "PX:out.png"},
		{[]string{"--png", png, tile(t)}, "PX:out.png"},
		{[]string{"--png", "64:" + png, filepath.Join(dir, "tile.jpg")}, ".svg or a .png"},
	}
	for _, c := range cases {
		var e bytes.Buffer
		if code := Main(context.Background(), c.args, &bytes.Buffer{}, &e); code != 2 || !strings.Contains(e.String(), c.want) {
			t.Errorf("%q: code %d, %q; want 2 and %q", c.args, code, e.String(), c.want)
		}
		if _, err := os.Stat(png); err == nil {
			t.Errorf("%q wrote %s", c.args, png)
		}
	}
}

func TestIconFailureLeavesOutputs(t *testing.T) {
	enginetest.Stub(t, "chrome-headless-shell", `exit 1`)
	dir := t.TempDir()
	icns := filepath.Join(dir, "app.icns")
	os.WriteFile(icns, []byte("old"), 0o644)
	if code := Main(context.Background(), []string{"--icns", icns, tile(t)}, &bytes.Buffer{}, &bytes.Buffer{}); code != 1 {
		t.Fatalf("code %d, want 1", code)
	}
	if b, _ := os.ReadFile(icns); string(b) != "old" {
		t.Errorf("a failed run changed %s", icns)
	}
}

// TestEncodeReadsBackWithIconutil holds Encode to what macOS itself reads,
// pixel for pixel, where iconutil is there to ask.
func TestEncodeReadsBackWithIconutil(t *testing.T) {
	if _, err := exec.LookPath("iconutil"); err != nil {
		t.Skip("no iconutil")
	}
	// Flat bands and per-pixel noise, with soft alpha, exercise both
	// run-length modes.
	src := map[int]*image.NRGBA{}
	pngs := map[int][]byte{}
	for _, s := range slots {
		img := image.NewNRGBA(image.Rect(0, 0, s.px, s.px))
		for y := 0; y < s.px; y++ {
			for x := 0; x < s.px; x++ {
				c := color.NRGBA{200, 40, 60, 255}
				if y >= s.px/2 {
					c = color.NRGBA{uint8(x * 37), uint8(y * 11), uint8(x ^ y), uint8(128 + x%128)}
				}
				img.SetNRGBA(x, y, c)
			}
		}
		var b bytes.Buffer
		png.Encode(&b, img)
		src[s.px], pngs[s.px] = img, b.Bytes()
	}
	dir := t.TempDir()
	icns := filepath.Join(dir, "a.icns")
	b, err := Encode(pngs)
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(icns, b, 0o644)
	set := filepath.Join(dir, "a.iconset")
	if out, err := exec.Command("iconutil", "-c", "iconset", "-o", set, icns).CombinedOutput(); err != nil {
		t.Fatalf("iconutil: %v: %s", err, out)
	}
	files := map[string]int{"icon_16x16.png": 16, "icon_16x16@2x.png": 32, "icon_32x32.png": 32, "icon_32x32@2x.png": 64,
		"icon_128x128.png": 128, "icon_128x128@2x.png": 256, "icon_256x256.png": 256, "icon_256x256@2x.png": 512,
		"icon_512x512.png": 512, "icon_512x512@2x.png": 1024}
	ents, _ := os.ReadDir(set)
	if len(ents) != len(files) {
		t.Errorf("iconset has %d files, want %d", len(ents), len(files))
	}
	for name, px := range files {
		f, err := os.Open(filepath.Join(set, name))
		if err != nil {
			t.Error(err)
			continue
		}
		got, err := png.Decode(f)
		f.Close()
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if bad := differing(src[px], got); bad > 0 {
			t.Errorf("%s: %d of %d pixels differ from the source", name, bad, px*px)
		}
	}
}

// differing counts the pixels of got off from want by more than a level
// in alpha, or in a colour channel by more than the 1 + 254/alpha levels
// that 8-bit premultiplied storage can round away (1 opaque, 2 near half
// alpha); alpha 0 pixels match whatever their colour.
func differing(want *image.NRGBA, got image.Image) int {
	n := 0
	r := want.Bounds()
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			w := want.NRGBAAt(x, y)
			g := color.NRGBAModel.Convert(got.At(x, y)).(color.NRGBA)
			if w.A == 0 && g.A == 0 {
				continue
			}
			tol := 1 + 254/max(int(w.A), 1)
			for i, d := range [][2]uint8{{w.R, g.R}, {w.G, g.G}, {w.B, g.B}, {w.A, g.A}} {
				if i == 3 {
					tol = 1
				}
				if int(d[0])-int(d[1]) > tol || int(d[1])-int(d[0]) > tol {
					n++
					break
				}
			}
		}
	}
	return n
}
