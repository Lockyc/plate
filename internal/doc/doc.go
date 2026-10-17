// Package doc renders a markdown or HTML document to a PDF: markdown becomes
// HTML through pandoc, then both take the same path, a staged copy of the
// page with a document stylesheet linked first, printed by render.PDF.
//
// HTML never goes through pandoc, whose reader drops markup it does not
// model. The staged copy lives in a temporary directory, so it carries a
// <base> at the input's directory, or every relative image would vanish
// without an error. Chrome has no string-set, so a script sets --doc-title
// from the first h1 (else <title>) for the stylesheet's running head.
package doc

import (
	"context"
	_ "embed"
	"fmt"
	"html"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/lockyc/plate/internal/cli"
	"github.com/lockyc/plate/internal/engine"
	"github.com/lockyc/plate/internal/render"
)

const usage = "doc [--css FILE] <in.md|in.html> <out.pdf>"

//go:embed default.css
var defaultCSS []byte

// titleJS sets --doc-title. JSON string syntax is valid CSS string syntax
// once whitespace is collapsed, so the value goes straight into the
// property.
const titleJS = `addEventListener("DOMContentLoaded",function(){var h=document.querySelector("h1");var t=(h?h.textContent:document.title).replace(/\s+/g," ").trim();document.documentElement.style.setProperty("--doc-title",JSON.stringify(t))})`

// Main runs `plate doc`.
func Main(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := cli.Flags("doc", usage, stderr)
	css := fs.String("css", "", "stylesheet to use in place of plate's default document look")
	rest, code, ok := cli.Parse(fs, args, 2)
	if !ok {
		return code
	}
	in, out := rest[0], rest[1]
	markdown, err := isMarkdown(in)
	if err != nil {
		return cli.Usage(stderr, "doc", "%v", err)
	}
	if err := build(ctx, in, out, *css, markdown, stderr); err != nil {
		return cli.Fail(stderr, "doc", err)
	}
	fmt.Fprintf(stdout, "wrote %s\n", out)
	return 0
}

// isMarkdown picks the route by extension; an unknown one is refused, never
// guessed.
func isMarkdown(in string) (bool, error) {
	switch strings.ToLower(filepath.Ext(in)) {
	case ".md", ".markdown":
		return true, nil
	case ".html", ".htm":
		return false, nil
	}
	return false, fmt.Errorf("don't know how to render %s (want .md, .markdown, .html or .htm)", in)
}

func build(ctx context.Context, in, out, css string, markdown bool, warn io.Writer) error {
	if _, err := os.Stat(in); err != nil {
		return err
	}
	if engine.SameFile(in, out) {
		return fmt.Errorf("%s is the document being rendered; write the PDF elsewhere", out)
	}
	if fi, err := os.Stat(filepath.Dir(out)); err != nil || !fi.IsDir() {
		return fmt.Errorf("output directory %s does not exist", filepath.Dir(out))
	}
	if css != "" {
		if _, err := os.Stat(css); err != nil {
			return fmt.Errorf("--css: %w", err)
		}
	}
	stage, err := os.MkdirTemp("", "plate-doc-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	if css == "" {
		css = filepath.Join(stage, "plate.css")
		if err := os.WriteFile(css, defaultCSS, 0o644); err != nil {
			return err
		}
	}
	src := in
	if markdown {
		if src, err = pandoc(ctx, in, stage); err != nil {
			return err
		}
	}
	page, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	dir, err := filepath.Abs(filepath.Dir(in))
	if err != nil {
		return err
	}
	if css, err = filepath.Abs(css); err != nil {
		return err
	}
	staged := filepath.Join(stage, "page.html")
	if err := os.WriteFile(staged, Stage(page, dir, css), 0o644); err != nil {
		return err
	}
	return render.PDF(ctx, staged, out, warn)
}

// pandoc converts in to a standalone HTML page in stage, with pandoc's own
// look switched off so the stylesheet is the whole look. The page is named
// after the input because pandoc titles a page with no title metadata after
// its output file, and any fallback passed as metadata would outrank the
// document's own title.
func pandoc(ctx context.Context, in, stage string) (string, error) {
	dir := filepath.Join(stage, "md")
	if err := os.Mkdir(dir, 0o755); err != nil {
		return "", err
	}
	body := filepath.Join(dir, strings.TrimSuffix(filepath.Base(in), filepath.Ext(in))+".html")
	_, err := engine.Run(ctx, engine.Cmd{
		Engine:  "pandoc",
		Args:    []string{"--standalone", "-M", "document-css=false", "-o", body, in},
		Inputs:  []string{in},
		Outputs: []string{body},
	})
	return body, err
}

var (
	headRe    = regexp.MustCompile(`(?i)<head(?:\s[^>]*)?>`)
	doctypeRe = regexp.MustCompile(`(?i)<!doctype[^>]*>`)
)

// Stage returns page with the staging block — <base> at dir, the stylesheet
// css, the title script — inserted at the top of its <head>, else after its
// doctype, else at the start. First in <head> puts the stylesheet before the
// document's own styles, so those win.
func Stage(page []byte, dir, css string) []byte {
	at := 0
	if loc := headRe.FindIndex(page); loc != nil {
		at = loc[1]
	} else if loc := doctypeRe.FindIndex(page); loc != nil {
		at = loc[1]
	}
	block := "\n<base href=\"" + html.EscapeString(render.PlateURL(dir, "")+"/") + "\">\n" +
		"<link rel=\"stylesheet\" href=\"" + html.EscapeString(render.PlateURL(css, "")) + "\">\n" +
		"<script>" + titleJS + "</script>\n"
	out := make([]byte, 0, len(page)+len(block))
	out = append(out, page[:at]...)
	out = append(out, block...)
	return append(out, page[at:]...)
}
