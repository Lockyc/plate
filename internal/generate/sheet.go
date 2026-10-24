package generate

import (
	"bytes"
	"context"
	"fmt"
	"html/template"
	"net/url"
	"os"
	"path/filepath"
	"sort"
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
// and nothing is copied. sheet.html holds every round; sheet.png the newest
// rounds that fit under sheetTall, and WriteSheet returns how many older
// rounds it left out. On an error no sheet.png is left, so a stale one
// cannot pass for the current pose.
func WriteSheet(ctx context.Context, dir string, refs []string) (int, error) {
	omitted, err := writeSheetFiles(ctx, dir, refs)
	if err != nil {
		os.Remove(filepath.Join(dir, "sheet.png"))
	}
	return omitted, err
}

func writeSheetFiles(ctx context.Context, dir string, refs []string) (int, error) {
	d, err := sheetPage(dir, refs)
	if err != nil {
		return 0, err
	}
	if err := writeSheetPage(filepath.Join(dir, "sheet.html"), d); err != nil {
		return 0, err
	}
	return renderSheet(ctx, dir, d)
}

type sheetRef struct{ Src, Name string }

type sheetAttempt struct{ Src, Label string }

type sheetRound struct {
	Number        int
	Model, Prompt string
	Cost          string
	Failed        string // "2 failed, $0.13 charged[, 1 cost not reported]", or "" when no failure was or may have been charged
	Attempts      []sheetAttempt
}

type sheetData struct {
	Width   int
	Name    string
	Refs    []sheetRef
	Rounds  []sheetRound
	Omitted int // older rounds left out of this page, shown only in sheet.html
	Total   string
}

// OmittedNote says, on a page that left rounds out, where they are.
func (d sheetData) OmittedNote() string {
	switch d.Omitted {
	case 0:
		return ""
	case 1:
		return "1 older round is only in sheet.html"
	}
	return fmt.Sprintf("%d older rounds are only in sheet.html", d.Omitted)
}

func money(v float64) string { return fmt.Sprintf("$%.2f", v) }

// writeSheetHTML writes DIR/sheet.html.
func writeSheetHTML(dir string, refs []string) error {
	d, err := sheetPage(dir, refs)
	if err != nil {
		return err
	}
	return writeSheetPage(filepath.Join(dir, "sheet.html"), d)
}

func writeSheetPage(path string, d sheetData) error {
	var b bytes.Buffer
	if err := sheetTmpl.Execute(&b, d); err != nil {
		return err
	}
	return atomicfile.WriteFile(path, b.Bytes(), 0o644)
}

// sheetPage is the sheet of the pose in dir, every round in it.
func sheetPage(dir string, refs []string) (sheetData, error) {
	all, err := LoadPose(dir)
	if err != nil {
		return sheetData{}, err
	}
	fails, err := LoadFailures(dir)
	if err != nil {
		return sheetData{}, err
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return sheetData{}, err
	}
	d := sheetData{Width: sheetWidth, Name: filepath.Base(abs), Total: money(PoseTotal(all) + ChargedTotal(fails))}
	for _, ref := range refs {
		r, err := filepath.Abs(ref)
		if err != nil {
			return sheetData{}, err
		}
		rel, err := filepath.Rel(abs, r)
		if err != nil {
			return sheetData{}, err
		}
		d.Refs = append(d.Refs, sheetRef{Src: urlPath(rel), Name: filepath.Base(ref)})
	}
	// A round has records, a failures.json, or both; newest round first.
	byRound := map[int][]Record{}
	failsBy := map[int]RoundFailures{}
	var nums []int
	for _, r := range all {
		if _, ok := byRound[r.Round]; !ok {
			nums = append(nums, r.Round)
		}
		byRound[r.Round] = append(byRound[r.Round], r)
	}
	for _, rf := range fails {
		if _, ok := byRound[rf.Round]; !ok {
			nums = append(nums, rf.Round)
		}
		failsBy[rf.Round] = rf
	}
	sort.Sort(sort.Reverse(sort.IntSlice(nums)))
	for _, round := range nums {
		rs, rf := byRound[round], failsBy[round]
		sr := sheetRound{Number: round, Model: rf.Model, Prompt: rf.Prompt, Cost: money(PoseTotal(rs) + rf.Charged())}
		if len(rs) > 0 {
			sr.Model, sr.Prompt = rs[0].Model, rs[0].Prompt
		}
		if c, u := rf.Charged(), rf.Unreported(); c > 0 || u > 0 {
			sr.Failed = fmt.Sprintf("%d failed, %s charged", len(rf.Failures), money(c))
			if u > 0 {
				sr.Failed += fmt.Sprintf(", %d cost not reported", u)
			}
		}
		for _, a := range rs {
			label := fmt.Sprintf("r%02d a%02d · ", a.Round, a.Attempt)
			if a.Width > 0 && a.Height > 0 {
				label += fmt.Sprintf("%d×%d · ", a.Width, a.Height)
			}
			sr.Attempts = append(sr.Attempts, sheetAttempt{
				Src:   urlPath(filepath.Join("rounds", roundName(a.Round), attemptBase(a.Attempt)+".png")),
				Label: label + money(a.CostUSD),
			})
		}
		d.Rounds = append(d.Rounds, sr)
	}
	return d, nil
}

// urlPath is a relative file path as a URL path, each segment escaped.
func urlPath(p string) string {
	segs := strings.Split(filepath.ToSlash(p), "/")
	for i, s := range segs {
		segs[i] = url.PathEscape(s)
	}
	return strings.Join(segs, "/")
}

// renderSheet writes DIR/sheet.png from d, the page sheet.html holds. When
// the whole page is too tall for one picture, it keeps the newest rounds
// that fit, found by halving (each try is a render), drawn from a page of
// its own beside sheet.html so the images resolve the same; it returns how
// many older rounds it left out.
func renderSheet(ctx context.Context, dir string, d sheetData) (int, error) {
	tmp, err := os.MkdirTemp(dir, ".sheet-")
	if err != nil {
		return 0, err
	}
	defer os.RemoveAll(tmp)
	try := func(k int) (string, bool, error) {
		page := filepath.Join(dir, "sheet.html")
		if k < len(d.Rounds) {
			f, err := os.CreateTemp(dir, ".sheet-*.html")
			if err != nil {
				return "", false, err
			}
			page = f.Name()
			f.Close()
			defer os.Remove(page)
			v := d
			v.Rounds, v.Omitted = d.Rounds[:k], len(d.Rounds)-k
			if err := writeSheetPage(page, v); err != nil {
				return "", false, err
			}
		}
		out := filepath.Join(tmp, fmt.Sprintf("cut-%d.png", k))
		h, err := shootSheet(ctx, page, tmp, out)
		if err != nil {
			return "", false, err
		}
		return out, h < sheetTall, nil
	}
	best, fits, err := try(len(d.Rounds))
	if err != nil {
		return 0, err
	}
	kept := len(d.Rounds)
	if !fits {
		// lo fits (or is -1, nothing tried fits); hi does not.
		lo, hi := -1, len(d.Rounds)
		for hi-lo > 1 {
			mid := (lo + hi) / 2
			out, ok, err := try(mid)
			if err != nil {
				return 0, err
			}
			if ok {
				lo, best = mid, out
			} else {
				hi = mid
			}
		}
		if lo < 0 {
			return 0, fmt.Errorf("sheet: even the references alone are taller than %d px", sheetTall)
		}
		kept = lo
	}
	b, err := os.ReadFile(best)
	if err != nil {
		return 0, err
	}
	return len(d.Rounds) - kept, atomicfile.WriteFile(filepath.Join(dir, "sheet.png"), b, 0o644)
}

// shootSheet renders page on a sheetTall canvas into out, cut to its
// content, and returns out's height; a var so tests need no Chrome. The page
// is cream edge to edge down to its footer, over a white floor. A white
// border makes white the colour -trim takes away whatever the corners hold,
// so a page cut off by the canvas, with no floor, keeps the canvas's height
// and reads as too tall.
var shootSheet = func(ctx context.Context, page, tmp, out string) (int, error) {
	abs, err := filepath.Abs(page)
	if err != nil {
		return 0, err
	}
	tall := filepath.Join(tmp, "tall.png")
	if err := render.PNG(ctx, abs, tall, sheetWidth, sheetTall, 1); err != nil {
		return 0, fmt.Errorf("sheet: %w", err)
	}
	if _, err := engine.Run(ctx, engine.Cmd{Engine: "magick",
		Args:   []string{tall, "-bordercolor", "white", "-border", "1", "-trim", "+repage", "PNG24:" + out},
		Inputs: []string{tall}, Outputs: []string{out}}); err != nil {
		return 0, fmt.Errorf("sheet: %w", err)
	}
	return pngHeight(out)
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
<p class="meta">{{.Model}} · {{.Cost}}{{if .Failed}} · {{.Failed}}{{end}}</p>
<p class="prompt">{{.Prompt}}</p>
<div class="grid">
{{range .Attempts}}<figure><img src="{{.Src}}" alt="{{.Label}}"><figcaption>{{.Label}}</figcaption></figure>
{{end}}</div>
</section>
{{end}}</div>
</main>
<footer>Pose total {{.Total}}{{with .OmittedNote}} · {{.}}{{end}}</footer>
</body></html>
`))

func pngHeight(path string) (int, error) {
	_, h, err := imgsize.Dims(path)
	return h, err
}
