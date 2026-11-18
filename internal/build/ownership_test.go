package build

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"image"
	"image/png"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/lockyc/plate/internal/enginetest"
	"pgregory.net/rapid"
)

// line is one manifest line the model holds.
type line struct {
	outputs           []string // of pdf, png, jpeg, html
	twoUp, marks      bool
	back              string
	listed            bool // on the manifest
	builtOK, backedOK bool // its render, and its duplex, succeeded in the last build
	built             bool // a build has run it since it last changed
}

func (l line) has(kind string) bool { return slices.Contains(l.outputs, kind) }

var lineNames = []string{"a", "b", "c"}

// foreignKinds are files a person or another tool may keep beside a line's
// outputs under its name. None is a file a still line writes, and no JSON
// among them is a capture sidecar of that line.
var foreignKinds = []struct{ suffix, body string }{
	{".webp", "a shrink pipeline's webp"},
	{".mp4", "a hand-made video"},
	{".poster.png", "someone's poster"},
	{".txt", "notes"},
	{"-notes.pdf", "%PDF-1.4 notes"},
	{".json", `{"tool":"shrink","bytes":3}`},
	{".json", `{"name":"NAME","icons":["a.png"]}`},
	{".json", `{"name":"other","formats":{"mp4":{"file":"NAME.mp4"}}}`},
}

type ownership struct {
	t       *testing.T
	dir     string
	lines   map[string]*line
	foreign map[string][]byte // path to its content
	failing map[string]bool
	pages   int  // page edits so far
	stale   bool // an edit or a failure has come since the last build
}

func (m *ownership) dist(n string) string { return filepath.Join(m.dir, "dist", n) }

// manifest writes the listed lines, dropping a back= whose target is not a
// listed pdf line, so the manifest always parses.
func (m *ownership) manifest() string {
	var b strings.Builder
	for _, n := range lineNames {
		l := m.lines[n]
		if !l.listed {
			continue
		}
		fmt.Fprintf(&b, "%s  %s.html  560x792  1  %s", n, n, strings.Join(l.outputs, ","))
		if l.twoUp {
			b.WriteString("  2up")
		}
		if l.marks {
			b.WriteString("  marks")
		}
		if bl := m.lines[l.back]; l.back != "" && bl.listed && bl.has("pdf") {
			b.WriteString("  back=" + l.back)
		} else {
			l.back = ""
		}
		b.WriteString("\n")
	}
	p := filepath.Join(m.dir, "m.conf")
	if err := os.WriteFile(p, []byte(b.String()), 0o644); err != nil {
		m.t.Fatal(err)
	}
	return p
}

func (m *ownership) edit(t *rapid.T) {
	n := rapid.SampledFrom(lineNames).Draw(t, "line")
	m.set(t, n)
}

// set gives line n a random spec; it stays listed three times in four.
func (m *ownership) set(t *rapid.T, n string) {
	l := &line{listed: rapid.IntRange(0, 3).Draw(t, "unlisted") < 3}
	for _, k := range []string{"pdf", "png", "jpeg", "html"} {
		if rapid.Bool().Draw(t, k) {
			l.outputs = append(l.outputs, k)
		}
	}
	if len(l.outputs) == 0 {
		l.outputs = []string{"pdf"}
	}
	if l.has("pdf") {
		l.twoUp = rapid.Bool().Draw(t, "2up")
		l.marks = rapid.Bool().Draw(t, "marks")
		if rapid.Bool().Draw(t, "has back") {
			l.back = rapid.SampledFrom(slices.DeleteFunc(slices.Clone(lineNames), func(o string) bool { return o == n })).Draw(t, "back")
		}
	}
	m.lines[n] = l
	for _, o := range m.lines {
		o.built = false // a back= may come or go with this edit
	}
}

func (m *ownership) plant(t *rapid.T) {
	m.plantAs(t, rapid.SampledFrom(lineNames).Draw(t, "name"))
}

// plantAs writes a random foreign file under line n's name.
func (m *ownership) plantAs(t *rapid.T, n string) {
	k := rapid.SampledFrom(foreignKinds).Draw(t, "kind")
	p := m.dist(n + k.suffix)
	body := []byte(strings.ReplaceAll(k.body, "NAME", n))
	os.MkdirAll(filepath.Dir(p), 0o755)
	if err := os.WriteFile(p, body, 0o644); err != nil {
		m.t.Fatal(err)
	}
	m.foreign[p] = body
}

func (m *ownership) fail(t *rapid.T) {
	n := rapid.SampledFrom(lineNames).Draw(t, "line")
	m.failing[n] = !m.failing[n]
	m.stale = true
	// The page changes with it, so the render cache cannot answer for it.
	m.pages++
	os.WriteFile(filepath.Join(m.dir, n+".html"), []byte(fmt.Sprintf("<html>%s %d</html>", n, m.pages)), 0o644)
	var names []string
	for _, o := range lineNames {
		if m.failing[o] {
			names = append(names, o)
		}
	}
	os.Setenv("FIXTURE_FAIL", strings.Join(names, " "))
}

func (m *ownership) build(t *rapid.T) {
	path := m.manifest()
	var stderr bytes.Buffer
	code := Main(context.Background(), []string{path}, io.Discard, &stderr)
	m.stale = false
	failed := false
	for _, n := range lineNames {
		l := m.lines[n]
		if !l.listed {
			continue
		}
		l.built, l.builtOK = true, !m.failing[n]
		l.backedOK = l.builtOK && l.back != "" && !m.failing[l.back]
		failed = failed || !l.builtOK || (l.back != "" && !l.backedOK)
	}
	if (code != 0) != failed {
		t.Fatalf("build exited %d, want failure %v:\n%s\nmanifest:\n%s", code, failed, stderr.String(), readFile(path))
	}
}

func readFile(p string) string {
	b, _ := os.ReadFile(p)
	return string(b)
}

// check holds after every action: no foreign file changed, and each listed
// line a build has run has its outputs exactly when they succeeded.
func (m *ownership) check(t *rapid.T) {
	for p, want := range m.foreign {
		if got, err := os.ReadFile(p); err != nil || !bytes.Equal(got, want) {
			t.Fatalf("%s, which plate did not write, is gone or changed (%v)", p, err)
		}
	}
	exts := map[string]string{"pdf": ".pdf", "png": ".png", "jpeg": ".jpg", "html": ".html"}
	for _, n := range lineNames {
		l := m.lines[n]
		if !l.listed || !l.built {
			continue
		}
		want := map[string]bool{}
		for _, k := range l.outputs {
			want[n+exts[k]] = l.builtOK
		}
		if l.twoUp {
			want[n+twoUpSuffix] = l.builtOK
		}
		if l.marks {
			want[n+marksSuffix] = l.builtOK
		}
		if l.back != "" {
			want[n+duplexSuffix] = l.backedOK
			if l.marks {
				want[n+duplexMarksSuffix] = l.backedOK
			}
		}
		for f, ok := range want {
			if _, err := os.Stat(m.dist(f)); (err == nil) != ok {
				t.Fatalf("%s exists %v, want %v (line %s %+v)\nmanifest:\n%s", f, err == nil, ok, n, *l, readFile(filepath.Join(m.dir, "m.conf")))
			}
		}
	}
}

// failStub is chromeStub that fails, writing nothing, for a page whose name
// FIXTURE_FAIL lists, and prints a page for --dump-dom.
const failStub = `for a in "$@"; do page=$a; done
n=${page##*/}; n=${n%%.html*}
for f in $FIXTURE_FAIL; do [ "$f" = "$n" ] && exit 1; done
for a in "$@"; do [ "$a" = --dump-dom ] && printf '<html>%s</html>' "$n"; done
` + chromeStub

// TestBuildNeverRemovesForeignFiles builds a random manifest (lines with
// any of pdf, png, jpeg and html, 2up, marks and back=) over a file under
// each line's name that plate never writes, then edits it, plants more such
// files, makes lines' renders fail and builds at random, and checks after every step that every planted file is as it was
// and every listed line's outputs exist exactly when its last build of
// them succeeded.
func TestBuildNeverRemovesForeignFiles(t *testing.T) {
	needPDFEngines(t)
	setup(t)
	enginetest.Stub(t, "chrome-headless-shell", failStub)
	t.Setenv("FIXTURE_PDF", twoA5)
	t.Setenv("FIXTURE_FAIL", "")
	fix := filepath.Join(t.TempDir(), "page.png")
	f, err := os.Create(fix)
	if err != nil {
		t.Fatal(err)
	}
	png.Encode(f, image.NewRGBA(image.Rect(0, 0, 560, 792)))
	f.Close()
	t.Setenv("FIXTURE_PNG", fix)
	rapid.Check(t, func(rt *rapid.T) {
		os.Setenv("FIXTURE_FAIL", "")
		m := &ownership{t: t, dir: t.TempDir(), lines: map[string]*line{}, foreign: map[string][]byte{}, failing: map[string]bool{}}
		for _, n := range lineNames {
			os.WriteFile(filepath.Join(m.dir, n+".html"), []byte("<html>"+n+"</html>"), 0o644)
			m.set(rt, n)
			m.plantAs(rt, n)
		}
		// Every case starts built over a foreign file per line, and ends
		// built when a render's failure has changed since, so a build that
		// takes a foreign file, or a failure that leaves a stale output
		// behind, is met within the gate's few checks.
		m.build(rt)
		m.check(rt)
		actions := map[string]func(*rapid.T){"edit": m.edit, "plant": m.plant, "fail": m.fail, "build": m.build}
		for range rapid.IntRange(1, 8).Draw(rt, "steps") {
			actions[rapid.SampledFrom([]string{"build", "edit", "fail", "plant"}).Draw(rt, "action")](rt)
			m.check(rt)
		}
		if m.stale {
			m.build(rt)
			m.check(rt)
		}
	})
}

// TestMain runs rapid's tests here at 30 checks unless -rapid.checks or
// RAPID_CHECKS asks for more: every build runs qpdf for 2up, marks and
// duplex, so rapid's default 100 would add twenty seconds to the gate.
func TestMain(m *testing.M) {
	flag.Parse()
	set := os.Getenv("RAPID_CHECKS") != ""
	flag.Visit(func(f *flag.Flag) { set = set || f.Name == "rapid.checks" })
	if !set {
		flag.Set("rapid.checks", "30")
	}
	os.Exit(m.Run())
}
