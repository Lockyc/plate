package generate

import (
	"bytes"
	"context"
	"fmt"
	"html/template"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/lockyc/plate/internal/atomicfile"
	"github.com/lockyc/plate/internal/engine"
	"github.com/lockyc/plate/internal/imgsize"
	"github.com/lockyc/plate/internal/render"
)

// The sheet is sheetWidth CSS px wide and rendered on a sheetTall canvas,
// then cut to its content.
const (
	sheetWidth = 1600
	sheetTall  = 16384 // render's tallest verified capture
)

// writeSheet is WriteSheet; a var so the round's tests need no Chrome.
var writeSheet = WriteSheet

// WriteSheet writes DIR/sheet.html and DIR/sheet.png: the references, then
// every attempt of every complete round, newest round first, so a person
// can judge them and an agent can look at the same picture. refs are the
// current run's references. Images are linked by their paths from DIR, so a
// reference outside DIR is reached through the render's local file serving
// and nothing is copied.
func WriteSheet(ctx context.Context, dir string, refs []string) error {
	if err := writeSheetHTML(dir, refs); err != nil {
		return err
	}
	return renderSheet(ctx, dir)
}

type sheetRef struct{ Src, Name string }

type sheetAttempt struct{ Src, Label string }

type sheetRound struct {
	Number        int
	Model, Prompt string
	Cost          string
	Attempts      []sheetAttempt
}

type sheetData struct {
	Width  int
	Name   string
	Refs   []sheetRef
	Rounds []sheetRound
	Total  string
}

func money(v float64) string { return fmt.Sprintf("$%.2f", v) }

func writeSheetHTML(dir string, refs []string) error {
	all, err := LoadPose(dir)
	if err != nil {
		return err
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	d := sheetData{Width: sheetWidth, Name: filepath.Base(abs), Total: money(PoseTotal(all))}
	for _, ref := range refs {
		r, err := filepath.Abs(ref)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(abs, r)
		if err != nil {
			return err
		}
		d.Refs = append(d.Refs, sheetRef{Src: urlPath(rel), Name: filepath.Base(ref)})
	}
	// Newest round first.
	for i := len(all) - 1; i >= 0; {
		round := all[i].Round
		j := i
		for j >= 0 && all[j].Round == round {
			j--
		}
		rs := all[j+1 : i+1]
		sr := sheetRound{Number: round, Model: rs[0].Model, Prompt: rs[0].Prompt, Cost: money(PoseTotal(rs))}
		for _, a := range rs {
			sr.Attempts = append(sr.Attempts, sheetAttempt{
				Src:   urlPath(filepath.Join("rounds", roundName(a.Round), attemptBase(a.Attempt)+".png")),
				Label: fmt.Sprintf("r%02d a%02d · %s", a.Round, a.Attempt, money(a.CostUSD)),
			})
		}
		d.Rounds = append(d.Rounds, sr)
		i = j
	}
	var b bytes.Buffer
	if err := sheetTmpl.Execute(&b, d); err != nil {
		return err
	}
	return atomicfile.WriteFile(filepath.Join(dir, "sheet.html"), b.Bytes(), 0o644)
}

// urlPath is a relative file path as a URL path, each segment escaped.
func urlPath(p string) string {
	segs := strings.Split(filepath.ToSlash(p), "/")
	for i, s := range segs {
		segs[i] = url.PathEscape(s)
	}
	return strings.Join(segs, "/")
}

// renderSheet renders sheet.html on a tall canvas and cuts the white below
// the content away. The page is cream edge to edge down to its footer, so
// flipping the image makes the white floor its corner, the one colour
// -trim takes away.
func renderSheet(ctx context.Context, dir string) error {
	tmp, err := os.MkdirTemp(dir, ".sheet-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	page, err := filepath.Abs(filepath.Join(dir, "sheet.html"))
	if err != nil {
		return err
	}
	tall := filepath.Join(tmp, "tall.png")
	if err := render.PNG(ctx, page, tall, sheetWidth, sheetTall, 1); err != nil {
		return fmt.Errorf("sheet: %w", err)
	}
	cut := filepath.Join(tmp, "cut.png")
	if _, err := engine.Run(ctx, engine.Cmd{Engine: "magick",
		Args:   []string{tall, "-flip", "-trim", "+repage", "-flip", "PNG24:" + cut},
		Inputs: []string{tall}, Outputs: []string{cut}}); err != nil {
		return fmt.Errorf("sheet: %w", err)
	}
	h, err := pngHeight(cut)
	if err != nil {
		return err
	}
	if h >= sheetTall {
		return fmt.Errorf("sheet: the page is taller than %d px; it has too many rounds for one picture", sheetTall)
	}
	b, err := os.ReadFile(cut)
	if err != nil {
		return err
	}
	return atomicfile.WriteFile(filepath.Join(dir, "sheet.png"), b, 0o644)
}

var sheetTmpl = template.Must(template.New("sheet").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8">
<title>{{.Name}} · contact sheet</title>
<style>
html{background:#fff}
body{margin:0;width:{{.Width}}px;background:#f4f1ea;color:#1a1a1a;font:16px/1.4 -apple-system,"Helvetica Neue",Arial,sans-serif}
header,footer{background:#14213d;color:#fff;padding:20px 24px}
header h1{margin:0;font-size:28px}
main{display:grid;grid-template-columns:360px 1fr;gap:24px;padding:24px;align-items:start}
h2{margin:0 0 4px;font-size:22px}
section{margin-bottom:32px}
.meta{margin:0 0 12px;font-size:16px}
.prompt{margin:0 0 12px;font-size:16px;max-width:75ch}
.grid{display:grid;grid-template-columns:repeat(3,360px);gap:24px}
figure{margin:0}
img{display:block;width:360px;height:auto;background:#fff;border:1px solid #c9c2b2}
figcaption{margin-top:4px;font-size:16px}
footer{font-size:18px}
</style></head><body>
<header><h1>{{.Name}}</h1></header>
<main>
<aside><h2>References</h2>
{{range .Refs}}<figure><img src="{{.Src}}" alt="reference {{.Name}}"><figcaption>{{.Name}}</figcaption></figure>
{{end}}</aside>
<div>
{{range .Rounds}}<section>
<h2>Round {{.Number}}</h2>
<p class="meta">{{.Model}} · {{.Cost}}</p>
<p class="prompt">{{.Prompt}}</p>
<div class="grid">
{{range .Attempts}}<figure><img src="{{.Src}}" alt="{{.Label}}"><figcaption>{{.Label}}</figcaption></figure>
{{end}}</div>
</section>
{{end}}</div>
</main>
<footer>Pose total {{.Total}}</footer>
</body></html>
`))

func pngHeight(path string) (int, error) {
	_, h, err := imgsize.Dims(path)
	return h, err
}
