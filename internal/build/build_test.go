package build

import (
	"bytes"
	"context"
	"image"
	"image/png"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lockyc/plate/internal/engine"
	"github.com/lockyc/plate/internal/enginetest"
	"github.com/lockyc/plate/internal/pdf"
	"github.com/lockyc/plate/internal/raster"
	"github.com/lockyc/plate/internal/render"
)

// chromeStub writes the fixture as the screenshot and fetches the page
// through plate's proxy, as Chrome does, so the render cache can record it.
const chromeStub = `for a in "$@"; do
  case "$a" in
    --screenshot=*) cp "$FIXTURE_PNG" "${a#--screenshot=}" ;;
    --print-to-pdf=*) cp "$FIXTURE_PDF" "${a#--print-to-pdf=}" ;;
    --proxy-server=*) proxy="${a#--proxy-server=}" ;;
  esac
done
page=
for a in "$@"; do page=$a; done
[ -n "$proxy" ] && curl -s -o /dev/null -x "$proxy" "$page"
origin=$(printf '%s' "$page" | sed -E 's|^(http://[^/]+).*|\1|')
for p in $FIXTURE_LOADS; do
  [ -n "$proxy" ] && curl -s -o /dev/null -x "$proxy" "$origin$p"
done
for u in $FIXTURE_URLS; do
  [ -n "$proxy" ] && curl -s -o /dev/null -x "$proxy" "$u"
done
if [ -n "$CANCEL_URL" ]; then
  curl -s -o /dev/null "$CANCEL_URL"
  sleep 30
fi
exit 0`

type env struct {
	chrome, dir, manifest string
	stdout, stderr        bytes.Buffer
}

// setup writes a manifest of PNG targets, one per name, each with its own
// page, under a stub Chrome and a private render cache.
func setup(t *testing.T, names ...string) *env {
	t.Helper()
	e := &env{chrome: enginetest.Stub(t, "chrome-headless-shell", chromeStub), dir: t.TempDir()}
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("SOURCE_DATE_EPOCH", "")
	t.Setenv("FIXTURE_URLS", "")
	t.Setenv("CANCEL_URL", "")
	fix := filepath.Join(t.TempDir(), "fixture.png")
	f, err := os.Create(fix)
	if err != nil {
		t.Fatal(err)
	}
	png.Encode(f, image.NewRGBA(image.Rect(0, 0, 100, 50)))
	f.Close()
	t.Setenv("FIXTURE_PNG", fix)
	var m strings.Builder
	for _, n := range names {
		os.WriteFile(filepath.Join(e.dir, n+".html"), []byte("<html>"+n+"</html>"), 0o644)
		m.WriteString(n + "  " + n + ".html  100x50  1  png\n")
	}
	e.manifest = filepath.Join(e.dir, "m.conf")
	os.WriteFile(e.manifest, []byte(m.String()), 0o644)
	return e
}

func (e *env) run(args ...string) int { return e.runCtx(context.Background(), args...) }

func (e *env) runCtx(ctx context.Context, args ...string) int {
	e.stdout.Reset()
	e.stderr.Reset()
	return Main(ctx, args, &e.stdout, &e.stderr)
}

func (e *env) built(name string) bool {
	_, err := os.Stat(filepath.Join(e.dir, "dist", name+".png"))
	return err == nil
}

func TestBuildRendersInManifestOrder(t *testing.T) {
	e := setup(t, "c", "a", "b")
	if code := e.run("--jobs", "3", e.manifest); code != 0 {
		t.Fatalf("code %d: %s", code, e.stderr.String())
	}
	want := "rendered c\nrendered a\nrendered b\n3 rendered, 0 unchanged\n"
	if got := e.stderr.String(); got != want {
		t.Errorf("stderr %q, want %q", got, want)
	}
	for _, n := range []string{"a", "b", "c"} {
		if !e.built(n) {
			t.Errorf("%s not built", n)
		}
	}
}

func TestBuildSelectsNames(t *testing.T) {
	e := setup(t, "a", "b")
	if code := e.run(e.manifest, "b"); code != 0 {
		t.Fatalf("code %d: %s", code, e.stderr.String())
	}
	if e.built("a") || !e.built("b") {
		t.Errorf("a built %v, b built %v; want only b", e.built("a"), e.built("b"))
	}
	if code := e.run(e.manifest, "zz"); code != 2 || !strings.Contains(e.stderr.String(), `no target "zz"`) {
		t.Errorf("code %d, stderr %q", code, e.stderr.String())
	}
}

func TestBuildReportsUnchanged(t *testing.T) {
	e := setup(t, "a", "b")
	e.run(e.manifest)
	if code := e.run(e.manifest); code != 0 {
		t.Fatalf("code %d: %s", code, e.stderr.String())
	}
	if want := "unchanged a\nunchanged b\n0 rendered, 2 unchanged\n"; e.stderr.String() != want {
		t.Errorf("stderr %q, want %q", e.stderr.String(), want)
	}
	if code := e.run("--force", e.manifest); code != 0 || !strings.Contains(e.stderr.String(), "2 rendered, 0 unchanged") {
		t.Errorf("--force: code %d, stderr %q", code, e.stderr.String())
	}
	if n := len(enginetest.Calls(t, e.chrome)); n != 4 {
		t.Errorf("chrome called %d times, want 4", n)
	}
}

func TestBuildContinuesPastFailure(t *testing.T) {
	e := setup(t, "a", "b")
	os.Remove(filepath.Join(e.dir, "a.html"))
	if code := e.run(e.manifest); code != 1 {
		t.Fatalf("code %d: %s", code, e.stderr.String())
	}
	s := e.stderr.String()
	if !strings.Contains(s, "failed a: ") || !strings.Contains(s, "rendered b\n") || !strings.Contains(s, "1 rendered, 0 unchanged, 1 failed") {
		t.Errorf("stderr %q", s)
	}
	if !e.built("b") {
		t.Error("b not built after a failed")
	}
}

func TestBuildBadManifestRendersNothing(t *testing.T) {
	e := setup(t, "a")
	for _, bad := range []string{"b b.html 100x50 1 gif", "b b.html 0x0 1 png", "A b.html 100x50 1 png"} {
		os.WriteFile(e.manifest, []byte("a a.html 100x50 1 png\n"+bad+"\n"), 0o644)
		if code := e.run(e.manifest); code != 2 || !strings.Contains(e.stderr.String(), e.manifest+":2: ") {
			t.Errorf("%q: code %d, stderr %q", bad, code, e.stderr.String())
		}
	}
	if len(enginetest.Calls(t, e.chrome)) != 0 {
		t.Error("chrome ran for a bad manifest")
	}
}

func TestBuildUnreadableManifestFails(t *testing.T) {
	e := setup(t)
	for _, path := range []string{filepath.Join(e.dir, "missing.conf"), e.dir} {
		if code := e.run(path); code != 1 || !strings.HasPrefix(e.stderr.String(), "plate build: ") {
			t.Errorf("%s: code %d, stderr %q", path, code, e.stderr.String())
		}
	}
}

func TestBuildReportsNotCached(t *testing.T) {
	e := setup(t, "a")
	t.Setenv("FIXTURE_URLS", "http://example.invalid/")
	if code := e.run(e.manifest); code != 0 {
		t.Fatalf("code %d: %s", code, e.stderr.String())
	}
	if s := e.stderr.String(); !strings.Contains(s, "rendered a (not cached: ") || !strings.HasSuffix(s, "1 rendered, 0 unchanged\n") {
		t.Errorf("stderr %q", s)
	}
}

func TestBuildQueryPageInSpacedDir(t *testing.T) {
	e := setup(t)
	dir := filepath.Join(e.dir, "with space")
	os.Mkdir(dir, 0o755)
	os.WriteFile(filepath.Join(dir, "q.html"), []byte("<html>q</html>"), 0o644)
	m := filepath.Join(dir, "m.conf")
	os.WriteFile(m, []byte("q q.html?m=date 100x50 1 png\n"), 0o644)
	if code := e.run(m); code != 0 {
		t.Fatalf("code %d: %s", code, e.stderr.String())
	}
	if _, err := os.Stat(filepath.Join(dir, "dist", "q.png")); err != nil {
		t.Error(err)
	}
	calls := enginetest.Calls(t, e.chrome)
	if len(calls) != 1 || !strings.HasSuffix(calls[0][len(calls[0])-1], "?m=date") {
		t.Errorf("chrome calls %q", calls)
	}
}

// The stub's first call cancels the build and then hangs; the hung render is
// killed and the queued one never starts.
func TestBuildCancelled(t *testing.T) {
	e := setup(t, "a", "b")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { cancel() }))
	defer srv.Close()
	t.Setenv("CANCEL_URL", srv.URL)
	start := time.Now()
	if code := e.runCtx(ctx, "--jobs", "1", e.manifest); code != 1 {
		t.Fatalf("code %d: %s", code, e.stderr.String())
	}
	if d := time.Since(start); d > 20*time.Second {
		t.Errorf("took %v: the hung render was not killed", d)
	}
	if s := e.stderr.String(); !strings.Contains(s, "failed a: ") || !strings.Contains(s, "failed b: ") || !strings.Contains(s, "0 rendered, 0 unchanged, 2 failed") {
		t.Errorf("stderr %q", s)
	}
	if n := len(enginetest.Calls(t, e.chrome)); n != 1 {
		t.Errorf("chrome called %d times, want 1", n)
	}
}

func TestBuildOutRelativeToCwd(t *testing.T) {
	e := setup(t, "a")
	t.Chdir(t.TempDir())
	if code := e.run("--out", "here", e.manifest); code != 0 {
		t.Fatalf("code %d: %s", code, e.stderr.String())
	}
	if _, err := os.Stat(filepath.Join("here", "a.png")); err != nil {
		t.Error(err)
	}
}

// render deletes a target's outputs before it starts and build runs targets
// in parallel, so a target that loads another's output renders against a
// file that may be missing. Each such target fails, rendered or unchanged.
func TestBuildRefusesLoadingAnotherOutput(t *testing.T) {
	e := setup(t, "b")
	aOut := filepath.Join(e.dir, "dist", "a.png")
	t.Setenv("FIXTURE_LOADS", filepath.ToSlash(aOut))
	if code := e.run(e.manifest); code != 0 {
		t.Fatalf("b alone: code %d: %s", code, e.stderr.String())
	}
	os.WriteFile(filepath.Join(e.dir, "a.html"), []byte("<html>a</html>"), 0o644)
	m, _ := os.ReadFile(e.manifest)
	os.WriteFile(e.manifest, append([]byte("a  a.html  100x50  1  png\n"), m...), 0o644)
	want := "failed b: loads a's output " + aOut + "; render a in a step before plate build, or split the manifest\n"
	// b's record still holds: the cache hit is refused all the same.
	if code := e.run(e.manifest, "b"); code != 1 || !strings.Contains(e.stderr.String(), want) {
		t.Errorf("cache hit: code %d, stderr %q", code, e.stderr.String())
	}
	if code := e.run("--force", e.manifest); code != 1 || !strings.Contains(e.stderr.String(), want) || !strings.Contains(e.stderr.String(), "1 rendered, 0 unchanged, 1 failed") {
		t.Errorf("render: code %d, stderr %q", code, e.stderr.String())
	}
	if left, _ := filepath.Glob(filepath.Join(e.dir, "dist", "b*")); len(left) > 0 {
		t.Errorf("the refused target left %q behind", left)
	}
}

// Go's flag package stops at the first positional argument, so a flag
// after the manifest arrives as a target name.
func TestBuildFlagAfterManifest(t *testing.T) {
	e := setup(t, "a")
	if code := e.run(e.manifest, "--force"); code != 2 || !strings.Contains(e.stderr.String(), "--force: flags go before the manifest") {
		t.Errorf("code %d, stderr %q", code, e.stderr.String())
	}
	if len(enginetest.Calls(t, e.chrome)) != 0 {
		t.Error("chrome ran")
	}
}

// setupTwoUp is setup with one A5 PDF target carrying 2up, under a stub
// Chrome that prints the PDF at FIXTURE_PDF. Imposing runs the real qpdf.
func setupTwoUp(t *testing.T, fixture string) *env {
	t.Helper()
	needPDFEngines(t)
	e := setup(t)
	t.Setenv("FIXTURE_PDF", fixture)
	os.WriteFile(filepath.Join(e.dir, "f.html"), []byte("<html>f</html>"), 0o644)
	os.WriteFile(e.manifest, []byte("f  f.html  560x792  1  pdf  2up\n"), 0o644)
	return e
}

// twoA5 is the impose quality case's two-page A5 flyer.
var twoA5, _ = filepath.Abs(filepath.Join("..", "..", "quality", "assets", "two-a5.pdf"))

func TestBuildTwoUpWritesTheSheet(t *testing.T) {
	e := setupTwoUp(t, twoA5)
	if code := e.run(e.manifest); code != 0 {
		t.Fatalf("code %d: %s", code, e.stderr.String())
	}
	if want := "rendered f\nimposed f-2up-a4.pdf\n1 rendered, 0 unchanged\n"; e.stderr.String() != want {
		t.Errorf("stderr %q, want %q", e.stderr.String(), want)
	}
	sheet := filepath.Join(e.dir, "dist", "f-2up-a4.pdf")
	pages, err := pdf.Pages(context.Background(), sheet)
	if err != nil {
		t.Fatal(err)
	}
	if len(pages) != 2 {
		t.Fatalf("%d pages, want 2", len(pages))
	}
	for i, p := range pages {
		if w, h := p.Size(); math.Abs(w-841.89) > 1 || math.Abs(h-595.28) > 1 {
			t.Errorf("page %d is %gx%g pt, want A4 landscape", i+1, w, h)
		}
	}
}

func TestBuildTwoUpUnchangedSkipsBoth(t *testing.T) {
	e := setupTwoUp(t, twoA5)
	if code := e.run(e.manifest); code != 0 {
		t.Fatalf("code %d: %s", code, e.stderr.String())
	}
	sheet := filepath.Join(e.dir, "dist", "f-2up-a4.pdf")
	later := time.Now().Add(time.Hour).Truncate(time.Second)
	os.Chtimes(sheet, later, later)
	if code := e.run(e.manifest); code != 0 {
		t.Fatalf("code %d: %s", code, e.stderr.String())
	}
	if want := "unchanged f\n0 rendered, 1 unchanged\n"; e.stderr.String() != want {
		t.Errorf("stderr %q, want %q", e.stderr.String(), want)
	}
	if fi, err := os.Stat(sheet); err != nil || !fi.ModTime().Equal(later) {
		t.Errorf("the sheet was rewritten: %v", err)
	}
	// A sheet that is missing, or older than its PDF, is imposed again
	// though the target is unchanged.
	os.Remove(sheet)
	if code := e.run(e.manifest); code != 0 || e.stderr.String() != "unchanged f\nimposed f-2up-a4.pdf\n0 rendered, 1 unchanged\n" {
		t.Errorf("missing: code %d, stderr %q", code, e.stderr.String())
	}
	earlier := time.Now().Add(-time.Hour)
	os.Chtimes(sheet, earlier, earlier)
	if code := e.run(e.manifest); code != 0 || e.stderr.String() != "unchanged f\nimposed f-2up-a4.pdf\n0 rendered, 1 unchanged\n" {
		t.Errorf("stale: code %d, stderr %q", code, e.stderr.String())
	}
	if code := e.run("--force", e.manifest); code != 0 || e.stderr.String() != "rendered f\nimposed f-2up-a4.pdf\n1 rendered, 0 unchanged\n" {
		t.Errorf("--force: code %d, stderr %q", code, e.stderr.String())
	}
}

func TestBuildTwoUpRefusesNotTwoA5(t *testing.T) {
	needPDFEngines(t)
	one := filepath.Join(t.TempDir(), "one.pdf")
	if _, err := engine.Run(context.Background(), engine.Cmd{Engine: "qpdf", Args: []string{"--empty", "--pages", twoA5, "1", "--", one}, Inputs: []string{twoA5}, Outputs: []string{one}}); err != nil {
		t.Fatal(err)
	}
	e := setupTwoUp(t, one)
	if code := e.run(e.manifest); code != 1 {
		t.Fatalf("code %d: %s", code, e.stderr.String())
	}
	if s := e.stderr.String(); !strings.Contains(s, "failed f: 2up: want 2 A5 pages, found 1 pages") || !strings.HasSuffix(s, "0 rendered, 0 unchanged, 1 failed\n") {
		t.Errorf("stderr %q", s)
	}
	if _, err := os.Stat(filepath.Join(e.dir, "dist", "f-2up-a4.pdf")); err == nil {
		t.Error("a sheet was written")
	}
}

func TestBuildMarksWritesTheCopy(t *testing.T) {
	e := setupTwoUp(t, twoA5)
	os.WriteFile(e.manifest, []byte("f  f.html  560x792  1  pdf  2up marks\n"), 0o644)
	if code := e.run(e.manifest); code != 0 {
		t.Fatalf("code %d: %s", code, e.stderr.String())
	}
	if want := "rendered f\nimposed f-2up-a4.pdf\nmarked f-marks.pdf\n1 rendered, 0 unchanged\n"; e.stderr.String() != want {
		t.Errorf("stderr %q, want %q", e.stderr.String(), want)
	}
	pages, err := pdf.Pages(context.Background(), filepath.Join(e.dir, "dist", "f-marks.pdf"))
	if err != nil {
		t.Fatal(err)
	}
	if len(pages) != 2 {
		t.Fatalf("%d pages, want 2", len(pages))
	}
	if code := e.run(e.manifest); code != 0 || e.stderr.String() != "unchanged f\n0 rendered, 1 unchanged\n" {
		t.Errorf("unchanged: code %d, stderr %q", code, e.stderr.String())
	}
}

// TestBuildMarksGoesWhenTwoUpFails: a target whose 2up fails has failed, so
// its marked copy is not left behind either.
func TestBuildMarksGoesWhenTwoUpFails(t *testing.T) {
	needPDFEngines(t)
	one := filepath.Join(t.TempDir(), "one.pdf")
	if _, err := engine.Run(context.Background(), engine.Cmd{Engine: "qpdf", Args: []string{"--empty", "--pages", twoA5, "1", "--", one}, Inputs: []string{twoA5}, Outputs: []string{one}}); err != nil {
		t.Fatal(err)
	}
	e := setupTwoUp(t, one)
	os.WriteFile(e.manifest, []byte("f  f.html  560x792  1  pdf  2up marks\n"), 0o644)
	if code := e.run(e.manifest); code != 1 || !strings.Contains(e.stderr.String(), "failed f: 2up:") {
		t.Fatalf("code %d: %s", code, e.stderr.String())
	}
	if _, err := os.Stat(filepath.Join(e.dir, "dist", "f-marks.pdf")); err == nil {
		t.Error("a marked copy was written")
	}
}

// needPDFEngines skips a test that imposes when qpdf or pdfinfo is missing.
func needPDFEngines(t *testing.T) {
	t.Helper()
	for _, n := range []string{"qpdf", "pdfinfo"} {
		if _, err := engine.Resolve(n); err != nil {
			t.Skip(err)
		}
	}
}

func TestBuildTwoUpWithoutPDFIsRefused(t *testing.T) {
	e := setup(t, "a")
	os.WriteFile(e.manifest, []byte("a  a.html  100x50  1  png  2up\n"), 0o644)
	if code := e.run(e.manifest); code != 2 || !strings.Contains(e.stderr.String(), e.manifest+":1: 2up needs the pdf output") {
		t.Errorf("code %d, stderr %q", code, e.stderr.String())
	}
	if len(enginetest.Calls(t, e.chrome)) != 0 {
		t.Error("chrome ran")
	}
}

const slidePage = `<!doctype html>
<style>
  body { margin: 0; background: #fff; }
  #b { width: 40px; height: 40px; background: #c33; animation: slide 2s linear forwards; }
  @keyframes slide { from { transform: translateX(0); } to { transform: translateX(500px); } }
</style>
<div id="b"></div>`

func TestBuildRunsVideoLine(t *testing.T) {
	for _, e := range []string{"chrome-headless-shell", "ffmpeg"} {
		if _, err := engine.Resolve(e); err != nil {
			t.Skip(err)
		}
	}
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("SOURCE_DATE_EPOCH", "")
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "css.html"), []byte(slidePage), 0o644); err != nil {
		t.Fatal(err)
	}
	m := filepath.Join(dir, "m.conf")
	write := func(body string) {
		if err := os.WriteFile(m, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("still css.html 600x100 2 png\nclip css.html 600x100 2 mp4,png dur=1s\n")
	e := &env{dir: dir, manifest: m}
	if code := e.run(m); code != 0 {
		t.Fatalf("code %d: %s", code, e.stderr.String())
	}
	if want := "rendered still\nrendered clip\n2 rendered, 0 unchanged\n"; e.stderr.String() != want {
		t.Errorf("stderr %q, want %q", e.stderr.String(), want)
	}
	dist := filepath.Join(dir, "dist")
	for _, f := range []string{"clip.mp4", "clip.json", "clip.png"} {
		if _, err := os.Stat(filepath.Join(dist, f)); err != nil {
			t.Error(err)
		}
	}
	want, _ := raster.DPI(filepath.Join(dist, "still.png"))
	if got, _ := raster.DPI(filepath.Join(dist, "clip.png")); want < 191.99 || math.Abs(got-want) > 0.01 {
		t.Errorf("clip.png is %v dpi, a still line's is %v (want 192)", got, want)
	}
	e.run(m)
	if want := "unchanged still\nunchanged clip\n0 rendered, 2 unchanged\n"; e.stderr.String() != want {
		t.Errorf("second run stderr %q, want %q", e.stderr.String(), want)
	}
	// A run that drops the still removes the earlier one.
	write("still css.html 600x100 2 png\nclip css.html 600x100 2 mp4 dur=1s\n")
	if code := e.run(m); code != 0 {
		t.Fatalf("code %d: %s", code, e.stderr.String())
	}
	if _, err := os.Stat(filepath.Join(dist, "clip.png")); err == nil {
		t.Error("clip.png survived a run that no longer lists png")
	}
	// A line switched back to a still removes the video set.
	write("still css.html 600x100 2 png\nclip css.html 600x100 2 png\n")
	if code := e.run(m); code != 0 {
		t.Fatalf("code %d: %s", code, e.stderr.String())
	}
	for _, f := range []string{"clip.mp4", "clip.json"} {
		if _, err := os.Stat(filepath.Join(dist, f)); err == nil {
			t.Errorf("%s survived the line becoming a still", f)
		}
	}
	if _, err := os.Stat(filepath.Join(dist, "clip.png")); err != nil {
		t.Error(err)
	}
}

func TestRemoveVideoSetRemovesOnlyACaptureOfTheLine(t *testing.T) {
	write := func(t *testing.T, dir string, files map[string]string) {
		t.Helper()
		for n, body := range files {
			if err := os.WriteFile(filepath.Join(dir, n), []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	exists := func(dir, n string) bool {
		_, err := os.Stat(filepath.Join(dir, n))
		return err == nil
	}
	still := func(dir string) Target {
		return Target{Name: "social", Opts: render.Options{PNG: filepath.Join(dir, "social.png")}}
	}

	t.Run("files no capture of the line wrote", func(t *testing.T) {
		dir := t.TempDir()
		write(t, dir, map[string]string{"social.png": "p", "social.webp": "w", "social.json": `{"tool":"shrink","bytes":3}`})
		if err := removeVideoSet(still(dir), dir, []Target{still(dir)}); err != nil {
			t.Fatal(err)
		}
		for _, n := range []string{"social.png", "social.webp", "social.json"} {
			if !exists(dir, n) {
				t.Errorf("%s removed", n)
			}
		}
	})

	t.Run("a json naming the line but no formats", func(t *testing.T) {
		dir := t.TempDir()
		write(t, dir, map[string]string{"social.webp": "w", "social.json": `{"name":"social","icons":["a.png"]}`})
		if err := removeVideoSet(still(dir), dir, []Target{still(dir)}); err != nil {
			t.Fatal(err)
		}
		for _, n := range []string{"social.webp", "social.json"} {
			if !exists(dir, n) {
				t.Errorf("%s removed", n)
			}
		}
	})

	t.Run("an earlier capture's set", func(t *testing.T) {
		dir := t.TempDir()
		write(t, dir, map[string]string{"social.mp4": "m", "social.poster.png": "p", "social.webp": "w",
			"social.json": `{"name":"social","formats":{"mp4":{"file":"social.mp4"},"poster":{"file":"social.poster.png"}}}`})
		if err := removeVideoSet(still(dir), dir, []Target{still(dir)}); err != nil {
			t.Fatal(err)
		}
		for _, n := range []string{"social.mp4", "social.poster.png", "social.json"} {
			if exists(dir, n) {
				t.Errorf("%s survived", n)
			}
		}
		if !exists(dir, "social.webp") {
			t.Error("social.webp, which the sidecar does not name, removed")
		}
	})

	t.Run("a file another target writes", func(t *testing.T) {
		dir := t.TempDir()
		write(t, dir, map[string]string{"social.mp4": "m", "social.gif": "g",
			"social.json": `{"name":"social","formats":{"mp4":{"file":"social.mp4"},"gif":{"file":"social.gif"}}}`})
		other := Target{Name: "other", Opts: render.Options{PNG: filepath.Join(dir, "social.gif")}}
		if err := removeVideoSet(still(dir), dir, []Target{still(dir), other}); err != nil {
			t.Fatal(err)
		}
		if exists(dir, "social.mp4") {
			t.Error("social.mp4 survived")
		}
		if !exists(dir, "social.gif") {
			t.Error("social.gif, which another target writes, removed")
		}
	})
}
