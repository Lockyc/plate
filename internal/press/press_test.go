package press

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/lockyc/plate/internal/enginetest"
	"github.com/lockyc/plate/internal/raster"
)

const gsStub = `dev=; out=
for a in "$@"; do
  case "$a" in
    -sDEVICE=*) dev=${a#-sDEVICE=} ;;
    -sOutputFile=*) out=${a#-sOutputFile=} ;;
  esac
done
case "$dev" in
  pdfwrite) printf '%%PDF-1.7 master' > "$(printf '%s' "$out" | sed 's/%%/%/g')"; [ -n "$GS_STDERR" ] && echo "$GS_STDERR" >&2 ;;
  png16m)
    f=$FIXTURE_SRC_PNG
    case "$*" in *DefaultCMYKProfile*) f=$FIXTURE_OUT_PNG ;; esac
    cp "$f" "$(printf '%s' "$out" | sed 's/%d/1/')" ;;
esac
exit 0`

const pdfinfoStub = `case "$*" in
  -f*) if [ -n "$PAGE_LINES" ]; then printf '%s\n' "$PAGE_LINES"; else printf 'Page    1 size: 900 x 675 pts\n'; fi ;;
  *out.pdf*) printf 'Pages:          %s\nPage size:      %s\n' "${PAGES:-1}" "${OUT_SIZE:-900 x 675 pts}" ;;
  *) printf 'Pages:          %s\nPage size:      900 x 675 pts\n' "${PAGES:-1}" ;;
esac`

func grey(t *testing.T, band color.Color) string {
	t.Helper()
	img := image.NewRGBA64(image.Rect(0, 0, 300, 225))
	for y := 0; y < 225; y++ {
		for x := 0; x < 300; x++ {
			c := color.Color(color.Gray{128})
			if band != nil && y >= 198 {
				c = band
			}
			img.Set(x, y, c)
		}
	}
	p := filepath.Join(t.TempDir(), "f.png")
	if err := raster.SavePNG(p, img); err != nil {
		t.Fatal(err)
	}
	return p
}

type env struct {
	gs, dir, in, out, icc string
	stdout, stderr        bytes.Buffer
}

func setup(t *testing.T) *env {
	t.Helper()
	e := &env{gs: enginetest.Stub(t, "gs", gsStub), dir: t.TempDir()}
	enginetest.Stub(t, "pdfinfo", pdfinfoStub)
	enginetest.Stub(t, "pdffonts", `printf 'name type encoding emb sub uni object ID\n---- ---- ---- --- --- --- ---\n%s' "$FONTS"`)
	enginetest.Stub(t, "pdfimages", `printf 'page num type width height color comp bpc enc interp object ID x-ppi y-ppi size ratio\n----\n   1   0 %s 1200 900 %s 4 8 image no 12 0 300 300 100K 2.5%%\n' "${IMG_TYPE:-image}" "${IMG_COLOR:-cmyk}"`)
	enginetest.Stub(t, "qpdf", `printf '%s\n' "${QDF:-/DeviceCMYK k}" > "$4"`)
	for _, k := range []string{"GS_STDERR", "FONTS", "IMG_TYPE", "IMG_COLOR", "QDF", "OUT_SIZE", "PAGES", "PAGE_LINES"} {
		t.Setenv(k, "")
	}
	src := grey(t, nil)
	t.Setenv("FIXTURE_SRC_PNG", src)
	t.Setenv("FIXTURE_OUT_PNG", src)
	e.in = filepath.Join(e.dir, "in.pdf")
	e.out = filepath.Join(e.dir, "out.pdf")
	e.icc = filepath.Join(e.dir, "cmyk.icc")
	os.WriteFile(e.in, []byte("%PDF"), 0o644)
	os.WriteFile(e.icc, []byte("icc"), 0o644)
	return e
}

func (e *env) run(extra ...string) int {
	args := append([]string{"--icc", e.icc}, extra...)
	return Main(context.Background(), append(args, e.in, e.out), &e.stdout, &e.stderr)
}

func (e *env) outExists() bool { _, err := os.Stat(e.out); return err == nil }

func TestPressClean(t *testing.T) {
	e := setup(t)
	if code := e.run(); code != 0 {
		t.Fatalf("code %d: %s", code, e.stderr.String())
	}
	write := enginetest.Calls(t, e.gs)[0]
	for _, want := range []string{"-dNoOutputFonts", "-sColorConversionStrategy=CMYK", "-sOutputICCProfile=" + e.icc,
		"--permit-file-read=" + e.icc, "-dFIXEDMEDIA", "-dDEVICEWIDTHPOINTS=900", "-dDEVICEHEIGHTPOINTS=675",
		"-dColorImageFilter=/FlateEncode", "-dColorImageResolution=450", "-dColorImageDownsampleThreshold=1.5", "-dSAFER"} {
		if !slices.Contains(write, want) {
			t.Errorf("pdfwrite args lack %s", want)
		}
	}
	if !strings.Contains(e.stdout.String(), "RMSE") {
		t.Errorf("stdout %q", e.stdout.String())
	}
}

func TestPressFailures(t *testing.T) {
	cases := []struct {
		name, env, val, want string
	}{
		{"gs stderr", "GS_STDERR", "   **** Error: rangecheck", "stderr"},
		{"font survived", "FONTS", "ABCDEF+Inter-Bold Type 1C WinAnsi yes yes no 12 0\n", "font"},
		{"rgb image", "IMG_COLOR", "rgb", "non-CMYK image"},
		{"rgb operator", "QDF", "0.2 0.3 0.4 rg", "non-CMYK colour"},
		{"rgb space", "QDF", "/ColorSpace /DeviceRGB", "non-CMYK colour"},
		{"box changed", "OUT_SIZE", "612 x 792 pts (letter)", "page box"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := setup(t)
			t.Setenv(c.env, c.val)
			if code := e.run(); code != 1 {
				t.Fatalf("code %d", code)
			}
			if !strings.Contains(e.stderr.String(), c.want) {
				t.Errorf("stderr %q lacks %q", e.stderr.String(), c.want)
			}
			if e.outExists() {
				t.Error("a failed master was left behind")
			}
		})
	}
}

func TestPressAcceptsStencilMasks(t *testing.T) {
	e := setup(t)
	t.Setenv("IMG_TYPE", "stencil")
	t.Setenv("IMG_COLOR", "-")
	if code := e.run(); code != 0 {
		t.Fatalf("code %d: %s", code, e.stderr.String())
	}
}

func TestPressSkipsImageData(t *testing.T) {
	e := setup(t)
	t.Setenv("QDF", "1 0 obj\n<< /Type /XObject /Subtype /Image /ColorSpace /DeviceCMYK >>\nstream\n rg\n RG\nendstream\nendobj\n2 0 obj\n<< /Length 3 >>\nstream\n0 0 0 1 k\nendstream\nendobj")
	if code := e.run(); code != 0 {
		t.Fatalf("code %d: %s", code, e.stderr.String())
	}
	e = setup(t)
	t.Setenv("QDF", "2 0 obj\n<< /Length 3 >>\nstream\n0.2 0.3 0.4 rg\nendstream\nendobj")
	if code := e.run(); code != 1 || !strings.Contains(e.stderr.String(), "non-CMYK colour") {
		t.Fatalf("content-stream rg passed: code %d: %s", code, e.stderr.String())
	}
}

func TestPressEscapesTheOutputName(t *testing.T) {
	e := setup(t)
	e.out = filepath.Join(e.dir, "out%d.pdf")
	if code := e.run(); code != 0 {
		t.Fatalf("code %d: %s", code, e.stderr.String())
	}
	if !slices.Contains(enginetest.Calls(t, e.gs)[0], "-sOutputFile="+filepath.Join(e.dir, "out%%d.pdf")) {
		t.Errorf("args %q", enginetest.Calls(t, e.gs)[0])
	}
}

func TestPressMixedPageSizes(t *testing.T) {
	e := setup(t)
	t.Setenv("PAGES", "2")
	t.Setenv("PAGE_LINES", "Page    1 size: 900 x 675 pts\nPage    2 size: 612 x 792 pts")
	if code := e.run(); code != 1 {
		t.Fatalf("code %d", code)
	}
	if len(enginetest.Calls(t, e.gs)) != 0 {
		t.Error("gs ran on a mixed-size PDF")
	}
}

func TestPressProof(t *testing.T) {
	e := setup(t)
	t.Setenv("FIXTURE_OUT_PNG", grey(t, color.Gray{90}))
	if code := e.run(); code != 0 {
		t.Fatalf("a band difference failed the page gate: %s", e.stderr.String())
	}
	e = setup(t)
	t.Setenv("FIXTURE_OUT_PNG", grey(t, color.Gray{90}))
	if code := e.run("--region", "0,0.88,1,0.12:0.06"); code != 1 {
		t.Fatalf("region gate passed a changed band")
	}
	if !strings.Contains(e.stderr.String(), "0,0.88,1,0.12") {
		t.Errorf("stderr %q does not name the region", e.stderr.String())
	}
	e = setup(t)
	black := filepath.Join(t.TempDir(), "black.png")
	raster.SavePNG(black, image.NewRGBA64(image.Rect(0, 0, 300, 225)))
	t.Setenv("FIXTURE_OUT_PNG", black)
	if code := e.run(); code != 1 || !strings.Contains(e.stderr.String(), "soft proof") {
		t.Fatalf("code %d, stderr %q", code, e.stderr.String())
	}
}

func TestPressUsage(t *testing.T) {
	e := setup(t)
	var out, errb bytes.Buffer
	if code := Main(context.Background(), []string{e.in, e.out}, &out, &errb); code != 2 {
		t.Errorf("missing --icc: code %d", code)
	}
	if code := e.run("--region", "0,0,1"); code != 2 {
		t.Errorf("bad region: code %d", code)
	}
	for _, max := range []string{"nan", "inf", "+Inf", "-1", "0"} {
		if code := e.run("--region", "0,0,1,1:"+max); code != 2 {
			t.Errorf("--region max %q: code %d, want 2", max, code)
		}
	}
}

func TestPressRefusesInputAsOutput(t *testing.T) {
	e := setup(t)
	e.out = e.in
	if code := e.run(); code != 1 {
		t.Fatalf("code %d", code)
	}
	if b, err := os.ReadFile(e.in); err != nil || string(b) != "%PDF" {
		t.Errorf("the input did not survive: %q, %v", b, err)
	}
	if len(enginetest.Calls(t, e.gs)) != 0 {
		t.Error("gs ran")
	}
}

func TestPressRefusesProfileAsOutput(t *testing.T) {
	e := setup(t)
	e.out = e.icc
	if code := e.run(); code != 1 {
		t.Fatalf("code %d", code)
	}
	if b, err := os.ReadFile(e.icc); err != nil || string(b) != "icc" {
		t.Errorf("the profile did not survive: %q, %v", b, err)
	}
}

func TestPressFailureLeavesNoStaleMaster(t *testing.T) {
	e := setup(t)
	os.WriteFile(e.out, []byte("stale"), 0o644)
	t.Setenv("PAGES", "2")
	t.Setenv("PAGE_LINES", "Page    1 size: 900 x 675 pts\nPage    2 size: 612 x 792 pts")
	if code := e.run(); code != 1 {
		t.Fatalf("code %d", code)
	}
	if e.outExists() {
		t.Error("a stale master survived a failed run")
	}
}

func TestPressRejectsNonPositiveLimits(t *testing.T) {
	for _, f := range []string{"--max-ppi", "--max-rmse", "--proof-width"} {
		for _, v := range []string{"0", "-1"} {
			e := setup(t)
			if code := e.run(f, v); code != 2 {
				t.Errorf("%s %s: code %d", f, v, code)
			}
			if len(enginetest.Calls(t, e.gs)) != 0 {
				t.Errorf("%s %s: gs ran", f, v)
			}
		}
	}
}
