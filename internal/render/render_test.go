package render

import (
	"bytes"
	"context"
	"image"
	"image/png"
	"maps"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/lockyc/plate/internal/enginetest"
	"github.com/lockyc/plate/internal/raster"
)

const chromeStub = `for a in "$@"; do
  case "$a" in
    --screenshot=*) [ -n "$FIXTURE_PNG" ] && cp "$FIXTURE_PNG" "${a#--screenshot=}" ;;
    --print-to-pdf=*) printf '%%PDF-1.4 stub' > "${a#--print-to-pdf=}" ;;
    --dump-dom) printf '%s' "$FIXTURE_DOM" ;;
    --proxy-server=*) proxy="${a#--proxy-server=}" ;;
  esac
done
page=
for a in "$@"; do page=$a; done
[ -n "$proxy" ] && curl -s -o /dev/null -x "$proxy" "$page"
load() {
  for p in $FIXTURE_LOADS; do
    [ -n "$proxy" ] && curl -s -o /dev/null -x "$proxy" "http://plate.invalid$p"
  done
}
load
for u in $FIXTURE_URLS; do
  [ -n "$proxy" ] && curl -s -o /dev/null -x "$proxy" "$u"
done
[ -n "$FIXTURE_STDERR" ] && printf '%s\n' "$FIXTURE_STDERR" >&2
[ -n "$FIXTURE_AFTER" ] && sh -c "$FIXTURE_AFTER"
[ -n "$FIXTURE_RELOAD" ] && load
exit ${CHROME_EXIT:-0}`

func fixturePNG(t *testing.T, w, h int) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "fixture.png")
	f, _ := os.Create(p)
	png.Encode(f, image.NewRGBA(image.Rect(0, 0, w, h)))
	f.Close()
	return p
}

type env struct {
	chrome, dir, page string
	stdout, stderr    bytes.Buffer
}

func setup(t *testing.T, fixW, fixH int) *env {
	t.Helper()
	e := &env{chrome: enginetest.Stub(t, "chrome-headless-shell", chromeStub), dir: t.TempDir()}
	t.Setenv("FIXTURE_PNG", fixturePNG(t, fixW, fixH))
	t.Setenv("FIXTURE_DOM", "<html><body>fine</body></html>")
	t.Setenv("CHROME_EXIT", "0")
	e.page = filepath.Join(e.dir, "page.html")
	os.WriteFile(e.page, []byte("<html></html>"), 0o644)
	enginetest.Stub(t, "pdfinfo", `printf 'Pages: 1\nPage size: %s\n' "${PDF_BOX:-900 x 675 pts}"`)
	enginetest.Stub(t, "pdftotext", `printf '%s' "$PDF_TEXT"`)
	return e
}

func (e *env) run(args ...string) int {
	return Main(context.Background(), args, &e.stdout, &e.stderr)
}

func (e *env) out(name string) string { return filepath.Join(e.dir, name) }

func exists(p string) bool { _, err := os.Stat(p); return err == nil }

func TestRenderPNG(t *testing.T) {
	e := setup(t, 200, 100)
	if code := e.run("--size", "100x50", "--scale", "2", "--png", e.out("p.png"), e.page); code != 0 {
		t.Fatalf("code %d: %s", code, e.stderr.String())
	}
	if d, _ := raster.DPI(e.out("p.png")); d < 191.99 || d > 192.01 {
		t.Errorf("DPI %v, want 192", d)
	}
	calls := enginetest.Calls(t, e.chrome)
	if len(calls) != 1 || slices.Contains(calls[0], "--dump-dom") {
		t.Fatalf("chrome calls %q, want 1 without --dump-dom (no --fail-if)", calls)
	}
	for _, want := range []string{"--window-size=100,50", "--force-device-scale-factor=2", "--virtual-time-budget=5000", "--hide-scrollbars", "--proxy-bypass-list=<-loopback>"} {
		if !slices.Contains(calls[0], want) {
			t.Errorf("chrome args lack %s: %q", want, calls[0])
		}
	}
	for _, a := range calls[0] {
		if strings.HasPrefix(a, "--user-data-dir") {
			t.Error("--user-data-dir passed; headless Chrome hangs after the shot with it")
		}
	}
}

func TestRenderFractionalScale(t *testing.T) {
	e := setup(t, 1200, 150)
	if code := e.run("--size", "800x100", "--scale", "1.5", "--png", e.out("p.png"), e.page); code != 0 {
		t.Fatalf("code %d: %s", code, e.stderr.String())
	}
	if d, _ := raster.DPI(e.out("p.png")); d < 143.99 || d > 144.01 {
		t.Errorf("DPI %v, want 144", d)
	}
}

func TestRenderRefusesBeyondVerified(t *testing.T) {
	e := setup(t, 1, 1)
	stale := e.out("p.png")
	os.WriteFile(stale, []byte("stale"), 0o644)
	if code := e.run("--size", "8200x8200", "--scale", "2", "--png", stale, e.page); code != 1 {
		t.Fatalf("code %d", code)
	}
	if !strings.Contains(e.stderr.String(), "MP") {
		t.Errorf("stderr %q", e.stderr.String())
	}
	if exists(stale) {
		t.Error("stale output survived a refused render")
	}
	if len(enginetest.Calls(t, e.chrome)) != 0 {
		t.Error("chrome ran for a refused render")
	}
}

func TestRenderWrongSizeFails(t *testing.T) {
	e := setup(t, 100, 100)
	if code := e.run("--size", "100x50", "--scale", "2", "--png", e.out("p.png"), e.page); code != 1 {
		t.Fatalf("code %d", code)
	}
	if !strings.Contains(e.stderr.String(), "rendered 100x100, expected 200x100") || exists(e.out("p.png")) {
		t.Errorf("stderr %q, file left: %v", e.stderr.String(), exists(e.out("p.png")))
	}
	// The PNG is checked under a temporary name; the error names the file
	// asked for.
	if s := e.stderr.String(); !strings.Contains(s, e.out("p.png")+": ") || strings.Contains(s, ".plate-") {
		t.Errorf("stderr %q does not name %s, or names the temporary file", s, e.out("p.png"))
	}
}

func TestRenderKilledChromeLeavesNothing(t *testing.T) {
	e := setup(t, 200, 100)
	t.Setenv("CHROME_EXIT", "144")
	if code := e.run("--size", "100x50", "--scale", "2", "--png", e.out("p.png"), e.page); code != 1 {
		t.Fatalf("code %d", code)
	}
	if exists(e.out("p.png")) {
		t.Error("output of a killed Chrome survived")
	}
}

func TestRenderNoOutput(t *testing.T) {
	e := setup(t, 200, 100)
	t.Setenv("FIXTURE_PNG", "")
	if code := e.run("--size", "100x50", "--scale", "2", "--png", e.out("p.png"), e.page); code != 1 {
		t.Fatalf("code %d", code)
	}
	if s := e.stderr.String(); !strings.Contains(s, "produced no output at "+e.out("p.png")) || strings.Contains(s, ".plate-") {
		t.Errorf("stderr %q", s)
	}
}

func TestRenderFailIfDOM(t *testing.T) {
	e := setup(t, 200, 100)
	t.Setenv("FIXTURE_DOM", `<p data-source-error>could not be read</p>`)
	if code := e.run("--size", "100x50", "--scale", "2", "--fail-if", "could not be read", "--png", e.out("p.png"), e.page); code != 1 {
		t.Fatalf("code %d", code)
	}
	if !strings.Contains(e.stderr.String(), `"could not be read"`) || exists(e.out("p.png")) {
		t.Errorf("stderr %q", e.stderr.String())
	}
	if calls := enginetest.Calls(t, e.chrome); len(calls) != 1 || !slices.Contains(calls[0], "--dump-dom") {
		t.Errorf("chrome calls %q, want one screenshot that also dumps the DOM", calls)
	}
}

func TestRenderPDF(t *testing.T) {
	e := setup(t, 1, 1)
	if code := e.run("--size", "1200x900", "--pdf", e.out("p.pdf"), e.page); code != 0 {
		t.Fatalf("code %d: %s", code, e.stderr.String())
	}
	if !slices.Contains(enginetest.Calls(t, e.chrome)[0], "--no-pdf-header-footer") {
		t.Error("PDF printed with header and footer")
	}
}

func TestRenderPDFPageMismatch(t *testing.T) {
	e := setup(t, 1, 1)
	t.Setenv("PDF_BOX", "612 x 792 pts (letter)")
	if code := e.run("--size", "1200x900", "--pdf", e.out("p.pdf"), e.page); code != 1 {
		t.Fatalf("code %d", code)
	}
	if !strings.Contains(e.stderr.String(), "@page") || exists(e.out("p.pdf")) {
		t.Errorf("stderr %q", e.stderr.String())
	}
}

// A named @page sizes one page apart from the first.
func TestRenderPDFLaterPageMismatch(t *testing.T) {
	e := setup(t, 1, 1)
	enginetest.Stub(t, "pdfinfo", `printf 'Pages: 2\nPage size: 900 x 675 pts\nPage    1 size: 900 x 675 pts\nPage    2 size: 612 x 792 pts\n'`)
	if code := e.run("--size", "1200x900", "--pdf", e.out("p.pdf"), e.page); code != 1 {
		t.Fatalf("code %d", code)
	}
	if !strings.Contains(e.stderr.String(), "PDF page 2 is 612x792 pt") || exists(e.out("p.pdf")) {
		t.Errorf("stderr %q", e.stderr.String())
	}
}

func TestRenderPDFFailIf(t *testing.T) {
	e := setup(t, 1, 1)
	t.Setenv("PDF_TEXT", "Lot 4 nothing matches")
	if code := e.run("--fail-if", "nothing matches", "--pdf", e.out("p.pdf"), e.page); code != 1 {
		t.Fatalf("code %d", code)
	}
	if exists(e.out("p.pdf")) {
		t.Error("sentinel PDF survived")
	}
}

func TestRenderPDFWarnsOffGrid(t *testing.T) {
	e := setup(t, 1, 1)
	t.Setenv("PDF_BOX", "903 x 675 pts")
	if code := e.run("--size", "1204x900", "--pdf", e.out("p.pdf"), e.page); code != 0 {
		t.Fatalf("code %d: %s", code, e.stderr.String())
	}
	if !strings.Contains(e.stderr.String(), "multiples of 8") {
		t.Errorf("no warning: %q", e.stderr.String())
	}
}

func TestRenderUsage(t *testing.T) {
	e := setup(t, 1, 1)
	for _, args := range [][]string{
		{e.page},
		{"--png", e.out("p.png"), e.page},
		{"--size", "0x5", "--png", e.out("p.png"), e.page},
		{"--size", "100x50", "--scale", "0", "--png", e.out("p.png"), e.page},
	} {
		if code := e.run(args...); code != 2 {
			t.Errorf("%q: code %d, want 2", args, code)
		}
	}
}

func TestRenderRefusesPageAsOutput(t *testing.T) {
	for _, flag := range []string{"--png", "--pdf"} {
		e := setup(t, 1, 1)
		if code := e.run("--size", "100x50", flag, e.page, e.page); code != 1 {
			t.Fatalf("%s: code %d", flag, code)
		}
		if b, err := os.ReadFile(e.page); err != nil || string(b) != "<html></html>" {
			t.Errorf("%s: page destroyed: %q %v", flag, b, err)
		}
		if len(enginetest.Calls(t, e.chrome)) != 0 {
			t.Errorf("%s: chrome ran", flag)
		}
	}
}

func TestRenderBadScale(t *testing.T) {
	e := setup(t, 1, 1)
	for _, sc := range []string{"NaN", "Inf", "1e30"} {
		code := e.run("--size", "100x50", "--scale", sc, "--png", e.out("p.png"), e.page)
		want := 2
		if sc == "1e30" {
			want = 1
		}
		if code != want {
			t.Errorf("--scale %s: code %d, want %d", sc, code, want)
		}
	}
	if len(enginetest.Calls(t, e.chrome)) != 0 {
		t.Error("chrome ran")
	}
}

func TestRenderSameOutputPath(t *testing.T) {
	e := setup(t, 1, 1)
	if code := e.run("--size", "100x50", "--png", e.out("x"), "--pdf", e.out("x"), e.page); code != 2 {
		t.Errorf("code %d, want 2", code)
	}
}

func TestPageURLEscapes(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "a b#c%d")
	os.MkdirAll(dir, 0o755)
	p := filepath.Join(dir, "page.html")
	os.WriteFile(p, nil, 0o644)
	got, _, _, err := pageURL(p)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(got, "file:///") || !strings.Contains(got, "a%20b%23c%25d/page.html") {
		t.Errorf("pageURL = %q", got)
	}
	if u, l, _, _ := pageURL("https://example.org/x"); l != "" || u != "https://example.org/x" {
		t.Errorf("https URL rewritten to %q", u)
	}
	if _, _, _, err := pageURL(filepath.Join(t.TempDir(), "missing.html")); err == nil {
		t.Error("missing page accepted")
	}
	if u, l, _, err := pageURL(got); err != nil || l != p {
		t.Errorf("file URL to an existing page: %q, %q, %v", u, l, err)
	}
	// The proxy records the cleaned path it served; the cache looks the
	// page up by this one.
	dotted := (&url.URL{Scheme: "file", Path: filepath.ToSlash(dir) + "/./x/../page.html"}).String()
	if _, l, _, err := pageURL(dotted); err != nil || l != p {
		t.Errorf("non-canonical file URL: local %q, %v; want %q", l, err, p)
	}
	missing := (&url.URL{Scheme: "file", Path: filepath.Join(t.TempDir(), "typo.html")}).String()
	if _, _, _, err := pageURL(missing); err == nil {
		t.Error("file URL to a missing page accepted")
	}
}

func TestRenderSameOutputPathSpelledDifferently(t *testing.T) {
	e := setup(t, 1, 1)
	t.Chdir(e.dir)
	if code := e.run("--size", "100x50", "--png", "./x", "--pdf", "x", e.page); code != 2 {
		t.Errorf("code %d, want 2", code)
	}
}

func TestHTMLWritesSettledDOM(t *testing.T) {
	e := setup(t, 100, 50)
	t.Setenv("FIXTURE_DOM", "<html><body>quoted</body></html>")
	if code := e.run("--html", e.out("p.html"), e.page); code != 0 {
		t.Fatalf("exit %d: %s", code, e.stderr.String())
	}
	got, _ := os.ReadFile(e.out("p.html"))
	if string(got) != "<html><body>quoted</body></html>" {
		t.Errorf("html = %q", got)
	}
}

func TestHTMLIgnoresChromeExitWhenPageComplete(t *testing.T) {
	e := setup(t, 100, 50)
	t.Setenv("CHROME_EXIT", "2")
	if code := e.run("--html", e.out("p.html"), e.page); code != 0 {
		t.Fatalf("exit %d, want 0 for a complete page: %s", code, e.stderr.String())
	}
}

func TestHTMLRefusesTruncatedPage(t *testing.T) {
	e := setup(t, 100, 50)
	t.Setenv("FIXTURE_DOM", "<html><body>cut sh")
	if code := e.run("--html", e.out("p.html"), e.page); code != 1 {
		t.Fatalf("exit %d, want 1", code)
	}
	if exists(e.out("p.html")) {
		t.Error("truncated page left on disk")
	}
}

func TestHTMLFailIf(t *testing.T) {
	e := setup(t, 100, 50)
	t.Setenv("FIXTURE_DOM", "<html><body>QUOTE FAILED: x</body></html>")
	if code := e.run("--fail-if", "QUOTE FAILED", "--html", e.out("p.html"), e.page); code != 1 {
		t.Fatalf("exit %d, want 1", code)
	}
	if exists(e.out("p.html")) {
		t.Error("page with a sentinel left on disk")
	}
}

func TestRenderTransparent(t *testing.T) {
	e := setup(t, 200, 100)
	if code := e.run("--size", "100x50", "--scale", "2", "--transparent", "--png", e.out("p.png"), e.page); code != 0 {
		t.Fatalf("code %d: %s", code, e.stderr.String())
	}
	calls := enginetest.Calls(t, e.chrome)
	if len(calls) != 1 || !slices.Contains(calls[0], "--default-background-color=00000000") {
		t.Fatalf("chrome calls %q, want one with a transparent default background", calls)
	}
}

func TestRenderOpaqueByDefault(t *testing.T) {
	e := setup(t, 200, 100)
	if code := e.run("--size", "100x50", "--scale", "2", "--png", e.out("p.png"), e.page); code != 0 {
		t.Fatalf("code %d: %s", code, e.stderr.String())
	}
	for _, a := range enginetest.Calls(t, e.chrome)[0] {
		if strings.HasPrefix(a, "--default-background-color") {
			t.Errorf("chrome given %s without --transparent", a)
		}
	}
}

func TestRenderTransparentRefusesOpaqueResult(t *testing.T) {
	e := setup(t, 200, 100)
	p := filepath.Join(t.TempDir(), "opaque.png")
	f, _ := os.Create(p)
	img := image.NewRGBA(image.Rect(0, 0, 200, 100))
	for i := 3; i < len(img.Pix); i += 4 {
		img.Pix[i] = 255
	}
	png.Encode(f, img)
	f.Close()
	t.Setenv("FIXTURE_PNG", p)
	if code := e.run("--size", "100x50", "--scale", "2", "--transparent", "--png", e.out("p.png"), e.page); code != 1 {
		t.Fatalf("code %d, want 1", code)
	}
	if !strings.Contains(e.stderr.String(), "alpha") || exists(e.out("p.png")) {
		t.Errorf("stderr %q, file left: %v", e.stderr.String(), exists(e.out("p.png")))
	}
}

func TestRenderTransparentNeedsPNG(t *testing.T) {
	e := setup(t, 1, 1)
	for _, args := range [][]string{
		{"--transparent", "--pdf", e.out("p.pdf"), e.page},
		{"--transparent", "--html", e.out("p.html"), e.page},
	} {
		if code := e.run(args...); code != 2 {
			t.Errorf("%q: code %d, want 2", args, code)
		}
	}
	if !strings.Contains(e.stderr.String(), "--transparent") {
		t.Errorf("stderr %q", e.stderr.String())
	}
	if len(enginetest.Calls(t, e.chrome)) != 0 {
		t.Error("chrome ran")
	}
}

func chromeArgs(t *testing.T, e *env) []string {
	t.Helper()
	calls := enginetest.Calls(t, e.chrome)
	if len(calls) == 0 {
		t.Fatal("chrome never ran")
	}
	return calls[0]
}

func TestRenderLoadsRecorded(t *testing.T) {
	e := setup(t, 1, 1)
	css := filepath.Join(e.dir, "a.css")
	os.WriteFile(css, []byte("body{}"), 0o644)
	t.Setenv("FIXTURE_LOADS", css+" "+filepath.Join(e.dir, "nope.js"))
	u, local, query, err := pageURL(e.page)
	if err != nil {
		t.Fatal(err)
	}
	j := job{url: u, local: local, query: query, pdf: e.out("p.pdf"), scale: 1, budget: DefaultBudget, warn: &e.stderr}
	got, _, err := j.run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(slices.Sorted(maps.Keys(got.Files)), []string{css, e.page}) || !slices.Equal(got.Missing, []string{filepath.Join(e.dir, "nope.js")}) {
		t.Errorf("loads = %+v", got)
	}
}

func TestRenderLoadsRealPathOfAwkwardDir(t *testing.T) {
	e := setup(t, 1, 1)
	dir := filepath.Join(e.dir, "a b%c")
	os.MkdirAll(dir, 0o755)
	page := filepath.Join(dir, "page.html")
	css := filepath.Join(dir, "x.css")
	os.WriteFile(page, []byte("<html></html>"), 0o644)
	os.WriteFile(css, []byte("body{}"), 0o644)
	t.Setenv("FIXTURE_LOADS", "/"+strings.TrimPrefix((&url.URL{Path: css}).EscapedPath(), "/"))
	u, local, query, err := pageURL(page)
	if err != nil {
		t.Fatal(err)
	}
	j := job{url: u, local: local, query: query, pdf: e.out("p.pdf"), scale: 1, budget: DefaultBudget, warn: &e.stderr}
	got, _, err := j.run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(slices.Sorted(maps.Keys(got.Files)), []string{page, css}) || len(got.Missing) != 0 {
		t.Errorf("loads = %+v, want Files [%s %s]", got, page, css)
	}
}

func TestRenderNoFileAccessFlag(t *testing.T) {
	e := setup(t, 1, 1)
	if code := e.run("--pdf", e.out("p.pdf"), e.page); code != 0 {
		t.Fatalf("code %d: %s", code, e.stderr.String())
	}
	args := chromeArgs(t, e)
	if !slices.Contains(args, "--proxy-bypass-list=<-loopback>") {
		t.Errorf("no proxy bypass list: %q", args)
	}
	if !slices.ContainsFunc(args, func(a string) bool { return strings.HasPrefix(a, "--proxy-server=http://127.0.0.1:") }) {
		t.Errorf("no proxy server: %q", args)
	}
	if slices.Contains(args, "--allow-file-access-from-files") {
		t.Errorf("file access flag still passed: %q", args)
	}
}

func TestRenderPDFFailIfReadsDOM(t *testing.T) {
	e := setup(t, 1, 1)
	t.Setenv("FIXTURE_DOM", `<html><p data-source-error="yes">x</p></html>`)
	t.Setenv("PDF_TEXT", "fine")
	if code := e.run("--pdf", e.out("p.pdf"), "--fail-if", `data-source-error="yes"`, e.page); code != 1 {
		t.Fatalf("code %d", code)
	}
	if !strings.Contains(e.stderr.String(), `data-source-error=\"yes\"`) || exists(e.out("p.pdf")) {
		t.Errorf("stderr %q, pdf left: %v", e.stderr.String(), exists(e.out("p.pdf")))
	}
	if calls := enginetest.Calls(t, e.chrome); len(calls) != 1 || !slices.Contains(calls[0], "--dump-dom") {
		t.Errorf("chrome calls %q, want one print that also dumps the DOM", calls)
	}
}

func TestRenderPageURLIsPlateOrigin(t *testing.T) {
	e := setup(t, 1, 1)
	arg := (&url.URL{Scheme: "file", Path: e.page, RawQuery: "x=1"}).String()
	if code := e.run("--pdf", e.out("p.pdf"), arg); code != 0 {
		t.Fatalf("code %d: %s", code, e.stderr.String())
	}
	args := chromeArgs(t, e)
	last := args[len(args)-1]
	if !strings.HasPrefix(last, "http://plate.invalid/") || !strings.HasSuffix(last, "page.html?x=1") {
		t.Errorf("page URL = %q", last)
	}
}

func TestRenderRemotePageSkipsProxy(t *testing.T) {
	e := setup(t, 1, 1)
	j := job{url: "https://example.org/x", pdf: e.out("p.pdf"), scale: 1, budget: DefaultBudget, warn: &e.stderr}
	got, _, err := j.run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Files)+len(got.Missing)+len(got.External) != 0 {
		t.Errorf("loads = %+v", got)
	}
	for _, a := range chromeArgs(t, e) {
		if strings.HasPrefix(a, "--proxy") {
			t.Errorf("remote page given %s", a)
		}
	}
}

// magickStub is ImageMagick writing its last argument.
const magickStub = `for a in "$@"; do out=$a; done; printf jpeg > "$out"`

func TestRenderJPEGOnly(t *testing.T) {
	e := setup(t, 100, 50)
	magick := enginetest.Stub(t, "magick", magickStub)
	if code := e.run("--size", "100x50", "--jpeg", e.out("p.jpg"), "--quality", "88", e.page); code != 0 {
		t.Fatalf("code %d: %s", code, e.stderr.String())
	}
	calls := enginetest.Calls(t, magick)
	if len(calls) != 1 || len(calls[0]) != 5 || calls[0][1] != "-strip" || calls[0][2] != "-quality" || calls[0][3] != "88" {
		t.Fatalf("magick calls %q, want <png> -strip -quality 88 <jpg>", calls)
	}
	if got, _ := os.ReadFile(e.out("p.jpg")); string(got) != "jpeg" {
		t.Errorf("p.jpg = %q", got)
	}
	left, _ := filepath.Glob(filepath.Join(e.dir, "*.png"))
	hidden, _ := filepath.Glob(filepath.Join(e.dir, ".plate-*"))
	if len(left)+len(hidden) != 0 {
		t.Errorf("PNG or temp left beside the output: %q %q", left, hidden)
	}
}

func TestRenderJPEGAndPNG(t *testing.T) {
	e := setup(t, 100, 50)
	magick := enginetest.Stub(t, "magick", magickStub)
	if code := e.run("--size", "100x50", "--png", e.out("p.png"), "--jpeg", e.out("p.jpg"), e.page); code != 0 {
		t.Fatalf("code %d: %s", code, e.stderr.String())
	}
	if n := len(enginetest.Calls(t, e.chrome)); n != 1 {
		t.Errorf("chrome ran %d times, want 1", n)
	}
	if calls := enginetest.Calls(t, magick); len(calls) != 1 || calls[0][3] != "90" {
		t.Errorf("magick calls %q, want quality 90", calls)
	}
	if !exists(e.out("p.png")) || !exists(e.out("p.jpg")) {
		t.Error("an output is missing")
	}
}

func TestRenderQualityRange(t *testing.T) {
	e := setup(t, 1, 1)
	enginetest.Stub(t, "magick", magickStub)
	for _, args := range [][]string{
		{"--size", "100x50", "--jpeg", e.out("p.jpg"), "--quality", "0", e.page},
		{"--size", "100x50", "--jpeg", e.out("p.jpg"), "--quality", "101", e.page},
		{"--size", "100x50", "--png", e.out("p.png"), "--quality", "80", e.page},
		{"--jpeg", e.out("p.jpg"), e.page},
		{"--size", "100x50", "--transparent", "--jpeg", e.out("p.jpg"), e.page},
	} {
		if code := e.run(args...); code != 2 {
			t.Errorf("%q: code %d, want 2", args, code)
		}
	}
	if len(enginetest.Calls(t, e.chrome)) != 0 {
		t.Error("chrome ran")
	}
}

func TestRenderJPEGFailureLeavesNothing(t *testing.T) {
	e := setup(t, 100, 50)
	enginetest.Stub(t, "magick", `echo boom >&2; exit 3`)
	if code := e.run("--size", "100x50", "--jpeg", e.out("p.jpg"), e.page); code != 1 {
		t.Fatalf("code %d", code)
	}
	if strings.Contains(e.stderr.String(), ".plate-") {
		t.Errorf("error names a temp: %s", e.stderr.String())
	}
	if left, _ := filepath.Glob(filepath.Join(e.dir, ".plate-*")); len(left) != 0 || exists(e.out("p.jpg")) {
		t.Errorf("left behind: %q", left)
	}
}

func TestRenderTransparentRefusesJPEG(t *testing.T) {
	e := setup(t, 1, 1)
	enginetest.Stub(t, "magick", magickStub)
	if code := e.run("--size", "100x50", "--transparent", "--png", e.out("p.png"), "--jpeg", e.out("p.jpg"), e.page); code != 2 {
		t.Fatalf("code %d, want 2", code)
	}
	if !strings.Contains(e.stderr.String(), "--transparent can't be combined with --jpeg: a JPEG has no alpha channel") {
		t.Errorf("stderr %q", e.stderr.String())
	}
}

func TestRenderExplicitZeroQualityNeedsJPEG(t *testing.T) {
	e := setup(t, 1, 1)
	if code := e.run("--size", "100x50", "--png", e.out("p.png"), "--quality", "0", e.page); code != 2 {
		t.Errorf("code %d, want 2", code)
	}
}

func TestPNGAlphaIsTransparent(t *testing.T) {
	e := setup(t, 200, 100)
	if err := PNGAlpha(context.Background(), e.page, e.out("a.png"), 100, 50, 2); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(chromeArgs(t, e), "--default-background-color=00000000") {
		t.Errorf("chrome args %q lack the transparent background", chromeArgs(t, e))
	}
}

func TestPNGAlphaRefusesOpaqueResult(t *testing.T) {
	e := setup(t, 200, 100)
	p := filepath.Join(t.TempDir(), "opaque.png")
	f, _ := os.Create(p)
	img := image.NewRGBA(image.Rect(0, 0, 200, 100))
	for i := 3; i < len(img.Pix); i += 4 {
		img.Pix[i] = 255
	}
	png.Encode(f, img)
	f.Close()
	t.Setenv("FIXTURE_PNG", p)
	err := PNGAlpha(context.Background(), e.page, e.out("a.png"), 100, 50, 2)
	if err == nil || !strings.Contains(err.Error(), "alpha") || exists(e.out("a.png")) {
		t.Errorf("err %v, file left: %v", err, exists(e.out("a.png")))
	}
}
