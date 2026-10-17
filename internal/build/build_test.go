package build

import (
	"bytes"
	"context"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lockyc/plate/internal/enginetest"
)

// chromeStub writes the fixture as the screenshot and fetches the page
// through plate's proxy, as Chrome does, so the render cache can record it.
const chromeStub = `for a in "$@"; do
  case "$a" in
    --screenshot=*) cp "$FIXTURE_PNG" "${a#--screenshot=}" ;;
    --proxy-server=*) proxy="${a#--proxy-server=}" ;;
  esac
done
page=
for a in "$@"; do page=$a; done
[ -n "$proxy" ] && curl -s -o /dev/null -x "$proxy" "$page"
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

func (e *env) run(args ...string) int {
	e.stdout.Reset()
	e.stderr.Reset()
	return Main(context.Background(), args, &e.stdout, &e.stderr)
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
	os.WriteFile(e.manifest, []byte("a a.html 100x50 1 png\nb b.html 100x50 1 gif\n"), 0o644)
	if code := e.run(e.manifest); code != 2 || !strings.Contains(e.stderr.String(), e.manifest+":2: ") {
		t.Errorf("code %d, stderr %q", code, e.stderr.String())
	}
	if len(enginetest.Calls(t, e.chrome)) != 0 {
		t.Error("chrome ran for a bad manifest")
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
