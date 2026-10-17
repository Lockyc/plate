package doc

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/lockyc/plate/internal/enginetest"
	"github.com/lockyc/plate/internal/render"
)

// chromeStub writes the PDF and fetches the page it was given (the last
// argument, served by plate's proxy) to $CAPTURE.
const chromeStub = `for a in "$@"; do
  case "$a" in --print-to-pdf=*) printf '%%PDF-1.4 stub' > "${a#--print-to-pdf=}" ;; esac
  case "$a" in --proxy-server=*) proxy=${a#--proxy-server=} ;; esac
  page=$a
done
curl -s -x "$proxy" -o "$CAPTURE" "$page"`

// pandocStub writes a standalone page to the path after -o.
const pandocStub = `while [ "$1" != "-o" ]; do shift; done
printf '<!DOCTYPE html>\n<html>\n<head>\n<title>t</title>\n</head>\n<body><h1>From markdown</h1></body>\n</html>\n' > "$2"`

type env struct {
	dir, capture, chrome, pandoc string
	stdout, stderr               bytes.Buffer
}

func setup(t *testing.T) *env {
	t.Helper()
	e := &env{dir: t.TempDir()}
	e.capture = filepath.Join(e.dir, "captured.html")
	t.Setenv("CAPTURE", e.capture)
	e.chrome = enginetest.Stub(t, "chrome-headless-shell", chromeStub)
	e.pandoc = enginetest.Stub(t, "pandoc", pandocStub)
	return e
}

func (e *env) write(name, body string) string {
	p := filepath.Join(e.dir, name)
	os.MkdirAll(filepath.Dir(p), 0o755)
	os.WriteFile(p, []byte(body), 0o644)
	return p
}

func (e *env) run(args ...string) int {
	return Main(context.Background(), args, &e.stdout, &e.stderr)
}

func (e *env) staged(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(e.capture)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestMarkdownGoesThroughPandocWithTheDefaultLook(t *testing.T) {
	e := setup(t)
	in := e.write("notes/report.md", "# Report\n")
	out := filepath.Join(e.dir, "report.pdf")
	if code := e.run(in, out); code != 0 {
		t.Fatalf("code %d: %s", code, e.stderr.String())
	}
	if e.stdout.String() != "wrote "+out+"\n" {
		t.Errorf("stdout %q", e.stdout.String())
	}
	call := enginetest.Calls(t, e.pandoc)[0]
	if !slices.Equal(call[:3], []string{"--standalone", "-M", "document-css=false"}) || call[len(call)-1] != in {
		t.Errorf("pandoc args %q", call)
	}
	// pandoc titles an untitled page after its output file.
	if body := call[len(call)-2]; filepath.Base(body) != "report.html" {
		t.Errorf("pandoc writes %s, want one named after the input", body)
	}
	page := e.staged(t)
	if !strings.Contains(page, `<base href="`+render.PlateURL(filepath.Join(e.dir, "notes"), "")+`/">`) {
		t.Errorf("no <base> at the input's directory:\n%s", page)
	}
	if !strings.Contains(page, "--doc-title") || !strings.Contains(page, "From markdown") {
		t.Errorf("staged page lacks the title script or the body:\n%s", page)
	}
}

// The default stylesheet the staged page links is the embedded one.
func TestDefaultStylesheetIsLinked(t *testing.T) {
	e := setup(t)
	enginetest.Stub(t, "chrome-headless-shell", chromeStub+`
css=$(sed -n 's|.*<link rel="stylesheet" href="\(http://plate.invalid[^"]*\)">.*|\1|p' "$CAPTURE")
curl -s -x "$proxy" -o "$CAPTURE.css" "$css"`)
	in := e.write("a.html", "<html><head></head><body></body></html>")
	if code := e.run(in, filepath.Join(e.dir, "a.pdf")); code != 0 {
		t.Fatalf("code %d: %s", code, e.stderr.String())
	}
	got, err := os.ReadFile(e.capture + ".css")
	if err != nil || !bytes.Equal(got, defaultCSS) {
		t.Errorf("linked stylesheet is not the default (err %v)", err)
	}
}

func TestHTMLSkipsPandoc(t *testing.T) {
	e := setup(t)
	in := e.write("a.HTM", "<html><head><style>own</style></head><body></body></html>")
	if code := e.run(in, filepath.Join(e.dir, "a.pdf")); code != 0 {
		t.Fatalf("code %d: %s", code, e.stderr.String())
	}
	if len(enginetest.Calls(t, e.pandoc)) != 0 {
		t.Error("HTML went through pandoc")
	}
	page := e.staged(t)
	if strings.Index(page, "<link rel=\"stylesheet\"") > strings.Index(page, "<style>own") {
		t.Errorf("the stylesheet must come before the document's own styles:\n%s", page)
	}
}

func TestCSSReplacesTheDefault(t *testing.T) {
	e := setup(t)
	in := e.write("a.html", "<html><head></head></html>")
	css := e.write("my style.css", "body{}")
	if code := e.run("--css", css, in, filepath.Join(e.dir, "a.pdf")); code != 0 {
		t.Fatalf("code %d: %s", code, e.stderr.String())
	}
	if want := `href="http://plate.invalid` + strings.ReplaceAll(filepath.ToSlash(css), " ", "%20") + `"`; !strings.Contains(e.staged(t), want) {
		t.Errorf("staged page does not link %s:\n%s", want, e.staged(t))
	}
}

func TestRefusesBeforeAnyEngineRuns(t *testing.T) {
	e := setup(t)
	md := e.write("a.md", "x")
	cases := []struct {
		args []string
		code int
		want string
	}{
		{[]string{e.write("a.txt", "x"), filepath.Join(e.dir, "a.pdf")}, 2, "don't know how to render"},
		{[]string{"--css", filepath.Join(e.dir, "absent.css"), md, filepath.Join(e.dir, "a.pdf")}, 1, "--css"},
		{[]string{md, filepath.Join(e.dir, "absent", "a.pdf")}, 1, "does not exist"},
		{[]string{md, md}, 1, "is the document being rendered"},
		{[]string{filepath.Join(e.dir, "absent.md"), filepath.Join(e.dir, "a.pdf")}, 1, "no such file"},
		{[]string{md}, 2, "usage"},
	}
	for _, c := range cases {
		e.stderr.Reset()
		if code := e.run(c.args...); code != c.code || !strings.Contains(e.stderr.String(), c.want) {
			t.Errorf("%q: code %d stderr %q", c.args, code, e.stderr.String())
		}
	}
	if len(enginetest.Calls(t, e.pandoc))+len(enginetest.Calls(t, e.chrome)) != 0 {
		t.Error("an engine ran")
	}
}

func TestStageInsertsAtTheTopOfHead(t *testing.T) {
	cases := map[string]string{
		`<html><HEAD lang="en"><title>x</title></HEAD></html>`: `<HEAD lang="en">` + "\n<base",
		`<!doctype html><html><header>h</header></html>`:       `<!doctype html>` + "\n<base",
		`<p>bare</p>`: "\n<base",
	}
	for page, want := range cases {
		got := string(Stage([]byte(page), "/in", "/s.css"))
		if !strings.Contains(got, want) || (want == "\n<base" && !strings.HasPrefix(got, want)) {
			t.Errorf("Stage(%q) =\n%s", page, got)
		}
		if strings.Count(got, "<base") != 1 {
			t.Errorf("Stage(%q) inserted the block more than once", page)
		}
	}
}

func TestStageUsesPlateOriginURLs(t *testing.T) {
	got := string(Stage([]byte("<html><head></head></html>"), "/in dir", "/s.css"))
	for _, want := range []string{`<base href="http://plate.invalid/in%20dir/">`, `href="http://plate.invalid/s.css"`} {
		if !strings.Contains(got, want) {
			t.Errorf("staged block lacks %s:\n%s", want, got)
		}
	}
}
