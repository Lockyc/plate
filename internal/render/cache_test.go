package render

import (
	"context"
	"encoding/json"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lockyc/plate/internal/enginetest"
)

// cacheEnv is setup with a private cache dir, so records never leak between
// tests or into the user's cache.
func cacheEnv(t *testing.T) *env {
	t.Helper()
	e := setup(t, 200, 100)
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("FIXTURE_LOADS", "")
	t.Setenv("FIXTURE_URLS", "")
	t.Setenv("FIXTURE_AFTER", "")
	t.Setenv("FIXTURE_RELOAD", "")
	// The key reads these; the caller's environment must not leak in. The
	// chrome override is already the test's own stub.
	t.Setenv("SOURCE_DATE_EPOCH", "")
	return e
}

// Another writer lands its bytes on the output path while this render runs,
// as a parallel target with the same --png would. The render's own bytes end
// up at the path, and the record names their hash, never the other
// writer's: otherwise the next run would call the other render "unchanged".
func TestCacheRecordsOwnOutput(t *testing.T) {
	e := cacheEnv(t)
	o := e.pngOpts()
	other := filepath.Join(t.TempDir(), "other.png")
	f, _ := os.Create(other)
	img := image.NewRGBA(image.Rect(0, 0, 200, 100))
	for i := range img.Pix {
		img.Pix[i] = 255
	}
	png.Encode(f, img)
	f.Close()
	t.Setenv("FIXTURE_AFTER", "cp '"+other+"' '"+o.PNG+"'")
	e.render(t, o)
	got, err := os.Open(o.PNG)
	if err != nil {
		t.Fatal(err)
	}
	m, err := png.Decode(got)
	got.Close()
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, a := m.At(0, 0).RGBA(); a != 0 {
		t.Fatal("the output holds the other writer's image, not this render's")
	}
	recs := records(t)
	if len(recs) != 1 {
		t.Fatalf("records %q", recs)
	}
	b, _ := os.ReadFile(recs[0])
	var r record
	json.Unmarshal(b, &r)
	if len(r.Outputs) != 1 || r.Outputs[0].SHA256 != hashFile(o.PNG) {
		t.Errorf("record outputs %+v, want this render's hash %s", r.Outputs, hashFile(o.PNG))
	}
	if left, _ := filepath.Glob(filepath.Join(e.dir, ".plate-*")); len(left) != 0 {
		t.Errorf("temporary files left: %q", left)
	}
}

// A failed render leaves neither its output nor a temporary file.
func TestCacheFailureLeavesNoTemp(t *testing.T) {
	e := cacheEnv(t)
	t.Setenv("CHROME_EXIT", "1")
	if _, err := Run(context.Background(), e.pngOpts(), &e.stderr); err == nil {
		t.Fatal("render succeeded")
	}
	if left, _ := filepath.Glob(filepath.Join(e.dir, "*.png")); len(left) != 0 {
		t.Errorf("left %q", left)
	}
	if left, _ := filepath.Glob(filepath.Join(e.dir, ".plate-*")); len(left) != 0 {
		t.Errorf("temporary files left: %q", left)
	}
}

func (e *env) pngOpts() Options {
	return Options{Page: e.page, PNG: e.out("p.png"), W: 100, H: 50, Scale: 2, Budget: defaultBudget}
}

// render runs o through Run, fails the test on an error, and reports
// whether the render was skipped as unchanged.
func (e *env) render(t *testing.T, o Options) bool {
	t.Helper()
	return e.result(t, o).Outcome == Unchanged
}

func (e *env) result(t *testing.T, o Options) Result {
	t.Helper()
	res, err := Run(context.Background(), o, &e.stderr)
	if err != nil {
		t.Fatalf("Run: %v (stderr %q)", err, e.stderr.String())
	}
	return res
}

func (e *env) calls(t *testing.T) int {
	t.Helper()
	return len(enginetest.Calls(t, e.chrome))
}

// records lists the record files in the test's cache dir.
func records(t *testing.T) []string {
	t.Helper()
	recs, err := filepath.Glob(filepath.Join(os.Getenv("XDG_CACHE_HOME"), "plate", "render", "*.json"))
	if err != nil {
		t.Fatal(err)
	}
	return recs
}

func write(t *testing.T, p, s string) {
	t.Helper()
	if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestCacheSecondRunSkips(t *testing.T) {
	e := cacheEnv(t)
	if res := e.result(t, e.pngOpts()); res != (Result{Outcome: Rendered}) {
		t.Fatalf("first run %+v, want Rendered", res)
	}
	if !e.render(t, e.pngOpts()) {
		t.Error("second identical run rendered")
	}
	if n := e.calls(t); n != 1 {
		t.Errorf("chrome called %d times, want 1", n)
	}
}

func TestCacheInputChangeRerenders(t *testing.T) {
	e := cacheEnv(t)
	css := e.out("a.css")
	write(t, css, "body{}")
	t.Setenv("FIXTURE_LOADS", css)
	e.render(t, e.pngOpts())
	write(t, css, "body{color:red}")
	if e.render(t, e.pngOpts()) {
		t.Error("skipped after a loaded file changed")
	}
	if n := e.calls(t); n != 2 {
		t.Errorf("chrome called %d times, want 2", n)
	}
}

func TestCachePageChangeRerenders(t *testing.T) {
	e := cacheEnv(t)
	e.render(t, e.pngOpts())
	write(t, e.page, "<html><body>new</body></html>")
	if e.render(t, e.pngOpts()) {
		t.Error("skipped after the page itself changed")
	}
}

func TestCacheOutputChangedRerenders(t *testing.T) {
	e := cacheEnv(t)
	o := e.pngOpts()
	e.render(t, o)
	write(t, o.PNG, "not the render")
	if e.render(t, o) {
		t.Error("skipped with an overwritten output")
	}
	os.Remove(o.PNG)
	if e.render(t, o) {
		t.Error("skipped with a deleted output")
	}
	if n := e.calls(t); n != 3 {
		t.Errorf("chrome called %d times, want 3", n)
	}
}

func TestCacheAbsentInputAppears(t *testing.T) {
	e := cacheEnv(t)
	js := e.out("b.js")
	t.Setenv("FIXTURE_LOADS", js)
	e.render(t, e.pngOpts())
	if !e.render(t, e.pngOpts()) {
		t.Fatal("second run with the input still absent rendered")
	}
	write(t, js, "1")
	if e.render(t, e.pngOpts()) {
		t.Error("skipped after an absent input appeared")
	}
}

func TestCacheKeyIncludesQuery(t *testing.T) {
	e := cacheEnv(t)
	o := e.pngOpts()
	o.Page = "file://" + e.page + "?m=date"
	e.render(t, o)
	o.Page = "file://" + e.page + "?m=sell"
	if e.render(t, o) {
		t.Error("?m=sell skipped on ?m=date's record")
	}
	if n := e.calls(t); n != 2 {
		t.Errorf("chrome called %d times, want 2", n)
	}
}

func TestCacheKeyIncludesOptions(t *testing.T) {
	e := cacheEnv(t)
	o := e.pngOpts()
	e.render(t, o)
	o.FailIf = []string{"QUOTE FAILED"}
	if e.render(t, o) {
		t.Error("a new --fail-if skipped on the old record")
	}
}

func TestCacheKeyIncludesSourceDate(t *testing.T) {
	e := cacheEnv(t)
	o := Options{Page: e.page, PDF: e.out("p.pdf"), Scale: 1, Budget: defaultBudget}
	e.render(t, o)
	t.Setenv("SOURCE_DATE_EPOCH", "1700000000")
	if e.render(t, o) {
		t.Error("a new SOURCE_DATE_EPOCH skipped, leaving the PDF stamped with the old date")
	}
}

func TestCacheForce(t *testing.T) {
	e := cacheEnv(t)
	e.render(t, e.pngOpts())
	f := e.pngOpts()
	f.Force = true
	if e.render(t, f) {
		t.Error("--force skipped")
	}
	if !e.render(t, e.pngOpts()) {
		t.Error("the run after --force rendered; --force wrote no record")
	}
	if n := e.calls(t); n != 2 {
		t.Errorf("chrome called %d times, want 2", n)
	}
}

func TestCacheExternalNotCached(t *testing.T) {
	e := cacheEnv(t)
	t.Setenv("FIXTURE_URLS", "http://example.invalid/")
	if res := e.result(t, e.pngOpts()); res != (Result{Uncacheable, "loads example.invalid"}) {
		t.Errorf("result %+v", res)
	}
	if e.render(t, e.pngOpts()) {
		t.Error("a render with an external load skipped")
	}
}

func TestCacheRemotePageNotCached(t *testing.T) {
	e := cacheEnv(t)
	o := Options{Page: "https://example.org/x", PDF: e.out("p.pdf"), Scale: 1, Budget: defaultBudget}
	if res := e.result(t, o); res != (Result{Uncacheable, "remote page"}) {
		t.Errorf("result %+v", res)
	}
	if e.render(t, o) {
		t.Error("a remote page skipped")
	}
}

func TestCacheFailureDropsRecord(t *testing.T) {
	e := cacheEnv(t)
	css := e.out("a.css")
	write(t, css, "body{}")
	t.Setenv("FIXTURE_LOADS", css)
	o := e.pngOpts()
	o.FailIf = []string{"QUOTE FAILED"}
	e.render(t, o)
	t.Setenv("FIXTURE_DOM", "<html><body>QUOTE FAILED</body></html>")
	write(t, css, "body{color:red}")
	if _, err := Run(context.Background(), o, &e.stderr); err == nil {
		t.Fatal("the --fail-if render succeeded")
	}
	if recs := records(t); len(recs) != 0 {
		t.Errorf("records %q survived a failed render", recs)
	}
	t.Setenv("FIXTURE_DOM", "<html><body>fine</body></html>")
	write(t, css, "body{}")
	if e.render(t, o) {
		t.Error("skipped on the record from before the failure")
	}
	if n := e.calls(t); n != 3 {
		t.Errorf("chrome called %d times, want 3", n)
	}
}

// An input edited after Chrome loaded it, but before the render finished,
// is recorded with the bytes Chrome saw, so the next run sees the change.
func TestCacheEditDuringRenderRerenders(t *testing.T) {
	e := cacheEnv(t)
	css := e.out("a.css")
	write(t, css, "body{}")
	t.Setenv("FIXTURE_LOADS", css)
	t.Setenv("FIXTURE_AFTER", "printf 'body{color:red}' > '"+css+"'")
	e.render(t, e.pngOpts())
	t.Setenv("FIXTURE_AFTER", "")
	if e.render(t, e.pngOpts()) {
		t.Error("skipped although the render saw the old bytes")
	}
	if !e.render(t, e.pngOpts()) {
		t.Error("third run rendered")
	}
}

func TestCacheUnstableInputNotCached(t *testing.T) {
	e := cacheEnv(t)
	css := e.out("a.css")
	write(t, css, "body{}")
	t.Setenv("FIXTURE_LOADS", css)
	// The stub loads FIXTURE_LOADS, runs FIXTURE_AFTER, then loads them again.
	t.Setenv("FIXTURE_AFTER", "printf 'body{color:red}' > '"+css+"'")
	t.Setenv("FIXTURE_RELOAD", "1")
	if res := e.result(t, e.pngOpts()); res != (Result{Uncacheable, "a file changed while it rendered"}) {
		t.Errorf("result %+v", res)
	}
	t.Setenv("FIXTURE_AFTER", "")
	t.Setenv("FIXTURE_RELOAD", "")
	if e.render(t, e.pngOpts()) {
		t.Error("a run that served one file with two contents wrote a record")
	}
}

func TestCacheCorruptRecordIsMiss(t *testing.T) {
	e := cacheEnv(t)
	e.render(t, e.pngOpts())
	dir := records(t)
	if len(dir) != 1 {
		t.Fatalf("records %q, want 1", dir)
	}
	b, _ := os.ReadFile(dir[0])
	var r record
	if err := json.Unmarshal(b, &r); err != nil || len(r.Inputs) == 0 {
		t.Fatalf("record %s: %v", b, err)
	}
	r.Inputs = nil
	noInputs, _ := json.Marshal(r)
	for _, junk := range []string{"{not json", "{}", `{"Inputs":[],"Outputs":[]}`, string(noInputs)} {
		write(t, dir[0], junk)
		if e.render(t, e.pngOpts()) {
			t.Errorf("record %q gave a hit", junk)
		}
	}
}

func TestCacheLibraryUncached(t *testing.T) {
	e := cacheEnv(t)
	for range 2 {
		if err := PDF(context.Background(), e.page, e.out("p.pdf"), &e.stderr); err != nil {
			t.Fatal(err)
		}
	}
	if n := e.calls(t); n != 2 {
		t.Errorf("chrome called %d times, want 2: render.PDF is uncached", n)
	}
}

func TestMainReportsUnchanged(t *testing.T) {
	e := cacheEnv(t)
	args := []string{"--size", "100x50", "--scale", "2", "--png", e.out("p.png"), e.page}
	for range 2 {
		if code := e.run(args...); code != 0 {
			t.Fatalf("code %d: %s", code, e.stderr.String())
		}
	}
	if !strings.Contains(e.stderr.String(), "plate render: unchanged: "+e.out("p.png")) {
		t.Errorf("stderr %q", e.stderr.String())
	}
	if code := e.run(append([]string{"--force"}, args...)...); code != 0 {
		t.Fatalf("--force code %d", code)
	}
	if n := e.calls(t); n != 2 {
		t.Errorf("chrome called %d times, want 2", n)
	}
}
