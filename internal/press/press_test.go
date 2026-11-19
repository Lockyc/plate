package press

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/lockyc/plate/internal/cmm"
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
    cp "$FIXTURE_SRC_PNG" "$(printf '%s' "$out" | sed 's/%d/1/')" ;;
  pam)
    p=1
    while [ "$p" -le "${PAGES:-1}" ]; do
      eval "f=\${PAM_$p:-\$FIXTURE_PAM}"
      cp "$f" "$(printf '%s' "$out" | sed "s/%d/$p/")"
      p=$((p+1))
    done ;;
esac
exit 0`

const pdfinfoStub = `case "$*" in
  -f*) if [ -n "$PAGE_LINES" ]; then printf '%s\n' "$PAGE_LINES"; else printf 'Page    1 size: 900 x 675 pts\n'; fi ;;
  *out.pdf*) printf 'Pages:          %s\nPage size:      %s\n' "${PAGES:-1}" "${OUT_SIZE:-900 x 675 pts}" ;;
  *) printf 'Pages:          %s\nPage size:      900 x 675 pts\n' "${PAGES:-1}" ;;
esac`

const fogra = "../../quality/assets/Coated_Fogra39L_VIGC_300.icc"

// flat is a 300x225 CMYK page of K=128, its bottom 27 rows K=band when
// band is above 0.
func flat(band byte) raster.PAM {
	img := raster.PAM{W: 300, H: 225}
	for y := range img.H {
		for range img.W {
			k := byte(128)
			if band > 0 && y >= 198 {
				k = band
			}
			img.Pix = append(img.Pix, 0, 0, 0, k)
		}
	}
	return img
}

func pamFile(t *testing.T, img raster.PAM) string {
	t.Helper()
	b := []byte("P7\nWIDTH " + strconv.Itoa(img.W) + "\nHEIGHT " + strconv.Itoa(img.H) + "\nDEPTH 4\nMAXVAL 255\nTUPLTYPE CMYK\nENDHDR\n")
	p := filepath.Join(t.TempDir(), "m.pam")
	if err := os.WriteFile(p, append(b, img.Pix...), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// srcFor is the PNG a source that proofs as img would render to.
func srcFor(t *testing.T, img raster.PAM) string {
	t.Helper()
	prof, err := cmm.Load(fogra)
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "src.png")
	if err := raster.SavePNG(p, prof.ToSRGB(img)); err != nil {
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
	enginetest.Stub(t, "qpdf", `q=${QDF:-/DeviceCMYK k}
case "$4" in */in.pdf) [ -n "$SRC_QDF" ] && q=$SRC_QDF ;; esac
printf '%s\n' "$q" > "$5"`)
	enginetest.Stub(t, "fix-qdf", `cp "$1" "$2"`)
	for _, k := range []string{"GS_STDERR", "FONTS", "IMG_TYPE", "IMG_COLOR", "QDF", "SRC_QDF", "OUT_SIZE", "PAGES", "PAGE_LINES", "PAM_1", "PAM_2"} {
		t.Setenv(k, "")
	}
	t.Setenv("FIXTURE_PAM", pamFile(t, flat(0)))
	t.Setenv("FIXTURE_SRC_PNG", srcFor(t, flat(0)))
	e.in = filepath.Join(e.dir, "in.pdf")
	e.out = filepath.Join(e.dir, "out.pdf")
	e.icc = filepath.Join(e.dir, "cmyk.icc")
	os.WriteFile(e.in, []byte("%PDF"), 0o644)
	b, err := os.ReadFile(fogra)
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(e.icc, b, 0o644)
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
		"-dAutoFilterColorImages=false", "-dColorImageFilter=/FlateEncode", "-dColorImageResolution=450", "-dColorImageDownsampleThreshold=1.5", "-dSAFER"} {
		if !slices.Contains(write, want) {
			t.Errorf("pdfwrite args lack %s", want)
		}
	}
	if !strings.Contains(e.stdout.String(), "RMSE") {
		t.Errorf("stdout %q", e.stdout.String())
	}
}

func TestPressJPEG(t *testing.T) {
	e := setup(t)
	if code := e.run("--jpeg", "90"); code != 0 {
		t.Fatalf("code %d: %s", code, e.stderr.String())
	}
	write := enginetest.Calls(t, e.gs)[0]
	params := "<< /ColorACSImageDict << /QFactor 0.2 /Blend 1 /HSamples [1 1 1 1] /VSamples [1 1 1 1] >> >> setdistillerparams"
	i := slices.Index(write, params)
	switch {
	case !slices.Contains(write, "-dAutoFilterColorImages=true"):
		t.Error("colour images not left to the auto filter")
	case !slices.Contains(write, "-dAutoFilterGrayImages=false") || !slices.Contains(write, "-dGrayImageFilter=/FlateEncode"):
		t.Error("grey images, soft masks among them, must stay lossless")
	case i < 1 || write[i-1] != "-c" || write[i+1] != "-f" || write[i+2] != e.in:
		t.Errorf("quality not set before the input: %q", write)
	}
	for q, want := range map[int]string{100: "/QFactor 0 ", 50: "/QFactor 1 ", 25: "/QFactor 2 "} {
		if !strings.Contains(jpegParams(q), want) {
			t.Errorf("q %d: %s, want %s", q, jpegParams(q), want)
		}
	}
	for _, bad := range []string{"-1", "101"} {
		if code := setup(t).run("--jpeg", bad); code != 2 {
			t.Errorf("--jpeg %s: code %d, want 2", bad, code)
		}
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

// banded makes the master's page carry band at its foot while the source
// stays flat.
func banded(t *testing.T, band byte) {
	t.Helper()
	t.Setenv("FIXTURE_PAM", pamFile(t, flat(band)))
}

func TestPressProof(t *testing.T) {
	e := setup(t)
	banded(t, 160)
	if code := e.run(); code != 0 {
		t.Fatalf("a band difference failed the page gate: %s", e.stderr.String())
	}
	e = setup(t)
	banded(t, 160)
	if code := e.run("--region", "0,0.88,1,0.12:0.06"); code != 1 {
		t.Fatalf("region gate passed a changed band")
	}
	if !strings.Contains(e.stderr.String(), "0,0.88,1,0.12") {
		t.Errorf("stderr %q does not name the region", e.stderr.String())
	}
	e = setup(t)
	allBlack := flat(0)
	for i := range allBlack.Pix {
		allBlack.Pix[i] = 255
	}
	t.Setenv("FIXTURE_PAM", pamFile(t, allBlack))
	if code := e.run("--max-ink", "400"); code != 1 || !strings.Contains(e.stderr.String(), "soft proof") {
		t.Fatalf("code %d, stderr %q", code, e.stderr.String())
	}
}

func TestProofNoGSReadback(t *testing.T) {
	e := setup(t)
	if code := e.run(); code != 0 {
		t.Fatalf("code %d: %s", code, e.stderr.String())
	}
	for _, c := range enginetest.Calls(t, e.gs) {
		if slices.Contains(c, "-sDEVICE=png16m") && slices.Contains(c, "--permit-file-read="+e.icc) {
			t.Errorf("gs read the master back through a profile: %q", c)
		}
		for _, a := range c {
			if strings.HasPrefix(a, "-sDefaultCMYKProfile") {
				t.Errorf("gs call with %q: %q", a, c)
			}
		}
	}
}

func TestProofSizeMismatch(t *testing.T) {
	e := setup(t)
	t.Setenv("FIXTURE_PAM", pamFile(t, raster.PAM{W: 300, H: 226, Pix: make([]byte, 300*226*4)}))
	if code := e.run(); code != 1 {
		t.Fatalf("code %d", code)
	}
	for _, want := range []string{"300x225", "300x226"} {
		if !strings.Contains(e.stderr.String(), want) {
			t.Errorf("stderr %q lacks %q", e.stderr.String(), want)
		}
	}
}

func TestRunRefusesRGBProfile(t *testing.T) {
	e := setup(t)
	e.icc = "../../quality/assets/sRGB-v4.icc"
	if code := e.run(); code != 1 || !strings.Contains(e.stderr.String(), "not a CMYK output profile") {
		t.Fatalf("code %d, stderr %q", code, e.stderr.String())
	}
	if len(enginetest.Calls(t, e.gs)) != 0 {
		t.Error("gs ran with an RGB profile")
	}
}

func TestVerifyFlag(t *testing.T) {
	e := setup(t)
	master := filepath.Join(e.dir, "master.pdf")
	os.WriteFile(master, []byte("%PDF master"), 0o644)
	var out, errb bytes.Buffer
	if code := Main(context.Background(), []string{"--icc", e.icc, "--verify", master, e.in, e.out}, &out, &errb); code != 2 || !strings.Contains(errb.String(), "--verify takes <in.pdf> only") {
		t.Errorf("--verify with an output: code %d, stderr %q", code, errb.String())
	}
	listing := func() []string {
		ents, err := os.ReadDir(e.dir)
		if err != nil {
			t.Fatal(err)
		}
		var names []string
		for _, f := range ents {
			names = append(names, f.Name())
		}
		return names
	}
	before := listing()
	if code := Main(context.Background(), []string{"--icc", e.icc, "--verify", master, e.in}, &out, &errb); code != 0 {
		t.Fatalf("code %d: %s", code, errb.String())
	}
	if after := listing(); !slices.Equal(before, after) {
		t.Errorf("--verify changed the directory: %q then %q", before, after)
	}
	if b, _ := os.ReadFile(master); string(b) != "%PDF master" {
		t.Errorf("--verify rewrote the master: %q", b)
	}
	for _, c := range enginetest.Calls(t, e.gs) {
		if slices.Contains(c, "-sDEVICE=pdfwrite") {
			t.Errorf("--verify converted: %q", c)
		}
	}
	if !strings.Contains(out.String(), "soft-proof RMSE") || !strings.Contains(out.String(), "checked "+master) || strings.Contains(out.String(), "wrote") {
		t.Errorf("stdout %q", out.String())
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
	want, _ := os.ReadFile(fogra)
	if b, err := os.ReadFile(e.icc); err != nil || !bytes.Equal(b, want) {
		t.Errorf("the profile did not survive: %v", err)
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

func TestParseSpot(t *testing.T) {
	sp, err := parseSpot("17458f=100,84,12,3")
	if err != nil || sp.rgb != [3]int{0x17, 0x45, 0x8f} || sp.cmyk != [4]float64{1, .84, .12, .03} {
		t.Errorf("17458f=100,84,12,3: %+v, %v", sp, err)
	}
	for _, bad := range []string{"17458f", "17458f=1,2,3", "1745=1,2,3,4", "zz458f=1,2,3,4", "17458f=101,0,0,0", "17458f=-1,0,0,0"} {
		if _, err := parseSpot(bad); err == nil {
			t.Errorf("%q: no error", bad)
		}
	}
}

// TestWithSpots rewrites a spot's rg and RG as k and K, black's longer than
// the rgb it replaces, leaves other colours and image data alone, and writes
// fractions as a content stream reads them.
func TestWithSpots(t *testing.T) {
	sp, _ := parseSpot("17458f=100,84,12,3")
	black, _ := parseSpot("000000=0,0,0,100")
	in := "1 0 obj\n<< /Length 2 0 R >>\nstream\nq .0902 .2706 .5608 rg 0 0 9 9 re f .0902 .2706 .5608 RG 1 1 1 rg 0 0 0 rg\nendstream\nendobj\n" +
		"3 0 obj\n<< /Subtype /Image /Length 4 0 R >>\nstream\n.0902 .2706 .5608 rg\nendstream\nendobj\n"
	out, n := withSpots([]byte(in), []spot{sp, black})
	if n != 3 {
		t.Fatalf("n %d, want 3", n)
	}
	for _, want := range []string{"q 1 .84 .12 .03 k 0", " 1 .84 .12 .03 K 1", " 1 1 1 rg", " 0 0 0 1 k\n", "stream\n.0902 .2706 .5608 rg\nendstream"} {
		if !strings.Contains(string(out), want) {
			t.Errorf("no %q in:\n%s", want, out)
		}
	}
}

// kText is a source page, in QDF, whose first text object is drawn in rgb
// black (K-only once --cmyk maps black to 0,0,0,100) and second in colour.
const kText = "3 0 obj\n<<\n  /Contents 4 0 R\n  /Resources <<\n    /Font <<\n      /F1 6 0 R\n    >>\n  >>\n  /Type /Page\n>>\nendobj\n" +
	"4 0 obj\n<<\n  /Length 5 0 R\n>>\nstream\n0 0 0 rg BT /F1 9 Tf (A) Tj ET .2 .3 .4 rg BT /F1 9 Tf (B) Tj ET\nendstream\nendobj\n"

// extGStateKey is an /ExtGState entry of a resource dictionary.
var extGStateKey = regexp.MustCompile(`/ExtGState\s*(<<|\d)`)

func TestWithOverprintKOnly(t *testing.T) {
	in := "3 0 obj\n<<\n  /Contents 4 0 R\n  /Resources <<\n    /Font <<\n      /F1 6 0 R\n    >>\n  >>\n  /Type /Page\n>>\nendobj\n" +
		"4 0 obj\n<<\n  /Length 5 0 R\n>>\nstream\nBT 0 0 0 1 k /F1 9 Tf (A) Tj ET\nBT .2 .3 .4 .5 k /F1 9 Tf (B) Tj ET\nq 0 0 0 1 k 0 0 9 9 re f Q\nendstream\nendobj\n" +
		"6 0 obj\n<<\n  /Length1 3\n>>\nstream\n0 0 0 1 k BT (x) Tj ET\nendstream\nendobj\n"
	out, n, err := withOverprint([]byte(in))
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("n %d, want 1:\n%s", n, out)
	}
	s := string(out)
	first, rest, _ := strings.Cut(s, "(A) Tj ET")
	if !strings.Contains(first, "BT\n/PlateOP gs") {
		t.Errorf("the K-only text does not overprint:\n%s", s)
	}
	if b, _, _ := strings.Cut(rest, "(B)"); strings.Contains(b, "/PlateOP gs") || strings.Count(s, "/PlateOP gs") != 1 {
		t.Errorf("/PlateOP gs outside the K-only text:\n%s", s)
	}
	if c := len(extGStateKey.FindAllString(s, -1)); c != 1 {
		t.Errorf("%d /ExtGState, want 1:\n%s", c, s)
	}
	if !strings.Contains(s, "(A) Tj ET\n/PlateKO gs") {
		t.Errorf("the overprint outlives its text object:\n%s", s)
	}
	if !strings.Contains(s, "stream\n0 0 0 1 k BT (x) Tj ET\nendstream") {
		t.Errorf("a stream no page draws was rewritten:\n%s", s)
	}
	if !strings.Contains(s, "/PlateOP << /Type /ExtGState /OP false /op true /OPM 1 >>") {
		t.Errorf("no /PlateOP state:\n%s", s)
	}
}

func TestWithOverprintMergesExtGState(t *testing.T) {
	stream := "stream\n0 0 0 1 k BT /F1 9 Tf (A) Tj ET\nendstream\nendobj\n"
	for name, in := range map[string]string{
		"inline": "3 0 obj\n<<\n  /Contents 4 0 R\n  /Resources <<\n    /ExtGState <<\n      /G3 5 0 R\n    >>\n  >>\n  /Type /Page\n>>\nendobj\n" +
			"4 0 obj\n<<\n  /Length 9 0 R\n>>\n" + stream,
		"indirect": "3 0 obj\n<<\n  /Contents 4 0 R\n  /Resources <<\n    /ExtGState 7 0 R\n  >>\n  /Type /Page\n>>\nendobj\n" +
			"4 0 obj\n<<\n  /Length 9 0 R\n>>\n" + stream + "7 0 obj\n<<\n  /G3 5 0 R\n>>\nendobj\n",
		"indirect resources": "3 0 obj\n<<\n  /Contents 4 0 R\n  /Resources 8 0 R\n  /Type /Page\n>>\nendobj\n" +
			"4 0 obj\n<<\n  /Length 9 0 R\n>>\n" + stream + "8 0 obj\n<<\n  /ExtGState <<\n    /G3 5 0 R\n  >>\n>>\nendobj\n",
	} {
		out, n, err := withOverprint([]byte(in))
		if err != nil || n != 1 {
			t.Fatalf("%s: n %d, %v", name, n, err)
		}
		s := string(out)
		if !strings.Contains(s, "/G3 5 0 R") || strings.Count(s, "/PlateOP << /Type /ExtGState /OP false /op true /OPM 1 >>") != 1 || len(extGStateKey.FindAllString(s, -1)) != 1 {
			t.Errorf("%s:\n%s", name, s)
		}
	}
}

func TestVerifyOverprintLost(t *testing.T) {
	e := setup(t)
	t.Setenv("SRC_QDF", kText)
	if code := e.run("--cmyk", "000000=0,0,0,100"); code != 1 || !strings.Contains(e.stderr.String(), "overprint") {
		t.Fatalf("code %d, stderr %q", code, e.stderr.String())
	}
	// /op true under OPM 0 knocks out the zero C, M and Y: no overprint.
	e = setup(t)
	t.Setenv("SRC_QDF", kText)
	t.Setenv("QDF", "<<\n  /Type /ExtGState\n  /op true\n>>\n<<\n  /OPM 1\n>>")
	if code := e.run("--cmyk", "000000=0,0,0,100"); code != 1 || !strings.Contains(e.stderr.String(), "overprint") {
		t.Fatalf("/op true without /OPM 1 passed: code %d, stderr %q", code, e.stderr.String())
	}
	e = setup(t)
	t.Setenv("SRC_QDF", kText)
	t.Setenv("QDF", "<<\n  /OPM 1\n  /Type /ExtGState\n  /op true\n>>")
	if code := e.run("--cmyk", "000000=0,0,0,100"); code != 0 {
		t.Fatalf("a master that kept its overprint failed: %s", e.stderr.String())
	}
	if !strings.Contains(e.stdout.String(), "1 text object(s) set to overprint") {
		t.Errorf("stdout %q", e.stdout.String())
	}
	e = setup(t)
	t.Setenv("SRC_QDF", kText)
	if code := e.run("--cmyk", "000000=0,0,0,100", "--knockout"); code != 0 {
		t.Fatalf("--knockout still required overprint: %s", e.stderr.String())
	}
}

func TestInkOverWithOverprintSuggestsKnockout(t *testing.T) {
	allBlack := flat(0)
	for i := range allBlack.Pix {
		allBlack.Pix[i] = 255
	}
	for _, knockout := range []bool{false, true} {
		e := setup(t)
		t.Setenv("FIXTURE_PAM", pamFile(t, allBlack))
		t.Setenv("SRC_QDF", kText)
		t.Setenv("QDF", "<< /OPM 1 /Type /ExtGState /op true >>")
		args := []string{"--cmyk", "000000=0,0,0,100"}
		if knockout {
			args = append(args, "--knockout")
		}
		if code := e.run(args...); code != 1 || !strings.Contains(e.stderr.String(), "ink") {
			t.Fatalf("code %d, stderr %q", code, e.stderr.String())
		}
		if got := strings.Contains(e.stderr.String(), "--knockout"); got == knockout {
			t.Errorf("knockout %v: stderr %q", knockout, e.stderr.String())
		}
	}
}
