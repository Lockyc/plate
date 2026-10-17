package build

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func manifest(t *testing.T, body string) (path, dir string) {
	t.Helper()
	dir = t.TempDir()
	path = filepath.Join(dir, "formats.conf")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path, dir
}

func TestManifestParses(t *testing.T) {
	path, dir := manifest(t, "# formats\n\npromo-a5  promo-a5.html  560x792  3.125  pdf,png  blank-margin=23\n"+
		"date-square social.html?m=date 1080x1080 1 jpeg   # the date card\n")
	out := filepath.Join(dir, "dist")
	ts, err := ParseManifest(path, out, []string{"QUOTE FAILED"})
	if err != nil {
		t.Fatal(err)
	}
	if len(ts) != 2 {
		t.Fatalf("%d targets, want 2", len(ts))
	}
	a := ts[0]
	if a.Name != "promo-a5" || a.Line != 3 {
		t.Errorf("target %q on line %d", a.Name, a.Line)
	}
	o := a.Opts
	if o.Page != filepath.Join(dir, "promo-a5.html") || o.PDF != filepath.Join(out, "promo-a5.pdf") || o.PNG != filepath.Join(out, "promo-a5.png") {
		t.Errorf("page %q, pdf %q, png %q", o.Page, o.PDF, o.PNG)
	}
	if o.W != 560 || o.H != 792 || o.Scale != 3.125 || o.BlankMargin != 23 || o.JPEG != "" || o.HTML != "" {
		t.Errorf("opts %+v", o)
	}
	if o.Budget != 5000 || o.Quality != nil || len(o.FailIf) != 1 {
		t.Errorf("budget %d, quality %v, fail-if %q", o.Budget, o.Quality, o.FailIf)
	}
	b := ts[1].Opts
	if b.JPEG != filepath.Join(out, "date-square.jpg") || !strings.HasSuffix(b.Page, "social.html?m=date") || b.PNG != "" {
		t.Errorf("jpeg %q, page %q, png %q", b.JPEG, b.Page, b.PNG)
	}
}

func TestManifestOptions(t *testing.T) {
	path, _ := manifest(t, "a a.html 100x50 2 png,html transparent budget=800\nb b.html 100x50 1 jpeg quality=70\n")
	ts, err := ParseManifest(path, t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if o := ts[0].Opts; !o.Transparent || o.Budget != 800 || o.HTML == "" {
		t.Errorf("a: %+v", o)
	}
	if q := ts[1].Opts.Quality; q == nil || *q != 70 {
		t.Errorf("b quality %v", q)
	}
}

func TestManifestRejectsCollisions(t *testing.T) {
	path, _ := manifest(t, "a a.html 100x50 1 png\nb b.html 100x50 1 png\na c.html 100x50 1 pdf\n")
	_, err := ParseManifest(path, t.TempDir(), nil)
	if err == nil || !strings.Contains(err.Error(), "formats.conf:3:") || !strings.Contains(err.Error(), "line 1") {
		t.Errorf("duplicate name: %v", err)
	}
	path, _ = manifest(t, "a a.html 100x50 1 pdf,pdf\n")
	if _, err := ParseManifest(path, t.TempDir(), nil); err == nil || !strings.Contains(err.Error(), "formats.conf:1:") {
		t.Errorf("repeated output: %v", err)
	}
}

func TestManifestBadLines(t *testing.T) {
	for _, line := range []string{
		"a a.html 100x50 1 gif",
		"a a.html 100x50 1 png sparkle",
		"a a.html 100x50 1 png sparkle=1",
		"a a.html 100 1 png",
		"a a.html 100x0 1 png",
		"a a.html 100x50 1",
		"a a.html 100x50 x png",
		"a a.html 100x50 1 png budget=0x10",
		"a a.html 100x50 1 png budget=1 budget=2",
		"a a.html 100x50 1 png quality=80",
		"a a.html 100x50 1 jpeg quality=101",
		"a a.html 100x50 1 jpeg transparent",
		"../a a.html 100x50 1 png",
	} {
		path, _ := manifest(t, "# header\nok ok.html 100x50 1 png\n"+line+"\n")
		_, err := ParseManifest(path, t.TempDir(), nil)
		if err == nil || !strings.HasPrefix(err.Error(), path+":3: ") {
			t.Errorf("%q: %v", line, err)
		}
	}
}
