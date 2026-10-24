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
		n := int(binary.BigEndian.Uint32(b[i+4 : i+8]))
		cfg, err := png.DecodeConfig(bytes.NewReader(b[i+8 : i+n]))
		if err != nil || cfg.Width != cfg.Height {
			t.Fatalf("%s: %v %dx%d", b[i:i+4], err, cfg.Width, cfg.Height)
		}
		es = append(es, entry{string(b[i : i+4]), cfg.Width})
		i += n
	}
	return es
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
// where iconutil is there to ask.
func TestEncodeReadsBackWithIconutil(t *testing.T) {
	if _, err := exec.LookPath("iconutil"); err != nil {
		t.Skip("no iconutil")
	}
	pngs := map[int][]byte{}
	for _, s := range slots {
		var b bytes.Buffer
		img := image.NewNRGBA(image.Rect(0, 0, s.px, s.px))
		img.Set(0, 0, color.NRGBA{1, 2, 3, 255})
		png.Encode(&b, img)
		pngs[s.px] = b.Bytes()
	}
	dir := t.TempDir()
	icns := filepath.Join(dir, "a.icns")
	os.WriteFile(icns, Encode(pngs), 0o644)
	set := filepath.Join(dir, "a.iconset")
	if out, err := exec.Command("iconutil", "-c", "iconset", "-o", set, icns).CombinedOutput(); err != nil {
		t.Fatalf("iconutil: %v: %s", err, out)
	}
	want := []string{"icon_128x128.png", "icon_128x128@2x.png", "icon_16x16.png", "icon_16x16@2x.png",
		"icon_256x256.png", "icon_256x256@2x.png", "icon_32x32.png", "icon_32x32@2x.png",
		"icon_512x512.png", "icon_512x512@2x.png"}
	ents, _ := os.ReadDir(set)
	var got []string
	for _, e := range ents {
		got = append(got, e.Name())
	}
	slices.Sort(got)
	if !slices.Equal(got, want) {
		t.Errorf("iconset %q\nwant %q", got, want)
	}
}
