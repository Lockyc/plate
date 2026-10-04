package slides

import (
	"fmt"
	"html"
	"math"
	"strings"

	"github.com/lockyc/plate/internal/fonts"
)

// textInset is the 0.1 in Slides keeps between a shape's edge and its text.
const textInset = 7.2

// level is what a placeholder passes down for one nesting level: the
// paragraph style and the text style of its first paragraph at that level.
type level struct {
	para ParaStyle
	run  TextStyle
}

// inherited is what a shape takes from the placeholders above it.
type inherited struct {
	levels    map[int]level
	alignment string
}

// builder turns one deck into pages. It indexes the layouts and masters,
// whose placeholders a slide's text inherits from, and collects the font
// variants and images the drawn pages use.
type builder struct {
	deck     *Deck
	elements map[string]*Element
	pages    map[string]*Page
	variants map[fonts.Variant]bool
	images   map[string]string // contentUrl -> local name, set by the caller
	undrawn  map[string]int
	theme    map[string]RGB
}

func newBuilder(d *Deck) *builder {
	b := &builder{deck: d, elements: map[string]*Element{}, pages: map[string]*Page{}, variants: map[fonts.Variant]bool{}, images: map[string]string{}}
	var index func(es []Element)
	index = func(es []Element) {
		for i := range es {
			b.elements[es[i].ObjectID] = &es[i]
			if g := es[i].ElementGroup; g != nil {
				index(g.Children)
			}
		}
	}
	for _, ps := range [][]Page{d.Layouts, d.Masters} {
		for i := range ps {
			b.pages[ps[i].ObjectID] = &ps[i]
			index(ps[i].PageElements)
		}
	}
	return b
}

// inherit walks a placeholder's parents, master first, so each nearer
// placeholder overrides what it sets.
func (b *builder) inherit(sh *Shape) inherited {
	var chain []*Shape
	seen := map[string]bool{}
	for p := sh.Placeholder; p != nil && p.ParentObjectID != "" && !seen[p.ParentObjectID]; {
		seen[p.ParentObjectID] = true
		e := b.elements[p.ParentObjectID]
		if e == nil || e.Shape == nil {
			break
		}
		chain = append(chain, e.Shape)
		p = e.Shape.Placeholder
	}
	in := inherited{levels: map[int]level{}}
	for i := len(chain) - 1; i >= 0; i-- {
		s := chain[i]
		if a := s.ShapeProperties.ContentAlignment; a != "" {
			in.alignment = a
		}
		for n, l := range levels(s.Text) {
			base := in.levels[n]
			in.levels[n] = level{para: l.para.over(base.para), run: l.run.over(base.run)}
		}
	}
	return in
}

// levels reads the first paragraph at each nesting level of t.
func levels(t *Text) map[int]level {
	out := map[int]level{}
	if t == nil {
		return out
	}
	cur, have := 0, false
	for _, e := range t.TextElements {
		if m := e.ParagraphMarker; m != nil {
			n := 0
			if m.Bullet != nil {
				n = m.Bullet.NestingLevel
			}
			cur = n
			_, have = out[n]
			if !have {
				out[n] = level{para: m.Style}
			}
			continue
		}
		if e.TextRun != nil && !have {
			l := out[cur]
			l.run = e.TextRun.Style
			out[cur] = l
			have = true
		}
	}
	return out
}

func (b *builder) color(c *OpaqueColor, alpha float64) string {
	if c == nil {
		return ""
	}
	if c.RGBColor != nil {
		return c.RGBColor.css(alpha)
	}
	if rgb, ok := b.theme[c.ThemeColor]; ok && c.ThemeColor != "" {
		return rgb.css(alpha)
	}
	return ""
}

func (b *builder) solid(s *Solid) string {
	if s == nil {
		return ""
	}
	a := 1.0
	if s.Alpha != nil {
		a = *s.Alpha
	}
	return b.color(s.Color, a)
}

// chain is a slide, its layout and its master, nearest first.
func (b *builder) chain(s *Page) []*Page {
	out := []*Page{s}
	if l := b.pages[s.SlideProperties.LayoutObjectID]; l != nil {
		out = append(out, l)
	}
	m := b.pages[s.SlideProperties.MasterObjectID]
	if m == nil && len(out) > 1 {
		m = b.pages[out[1].LayoutProperties.MasterObjectID]
	}
	if m != nil {
		out = append(out, m)
	}
	return out
}

// page returns the HTML for slide s, sized w×h CSS px (1 pt = 1 px), with
// the stylesheet fontsCSS linked.
func (b *builder) page(s *Page, w, h float64, fontsCSS string) string {
	chain := b.chain(s)
	b.theme = map[string]RGB{}
	for i := len(chain) - 1; i >= 0; i-- {
		for _, c := range chain[i].PageProperties.ColorScheme.Colors {
			b.theme[c.Type] = c.Color
		}
	}
	bg := "background:#fff;"
	for _, p := range chain {
		f := p.PageProperties.PageBackgroundFill
		if f.PropertyState == "INHERIT" {
			continue
		}
		if f.StretchedPictureFill != nil && f.StretchedPictureFill.ContentURL != "" {
			bg = fmt.Sprintf("background:url('%s') 0 0/100%% 100%% no-repeat;", b.image(f.StretchedPictureFill.ContentURL))
		} else if c := b.solid(f.SolidFill); c != "" {
			bg = "background:" + c + ";"
		} else {
			continue
		}
		break
	}
	var body strings.Builder
	for _, e := range s.PageElements {
		b.element(&body, &e, identity)
	}
	return fmt.Sprintf("<!doctype html><meta charset=utf-8><link rel=stylesheet href=\"%s\">"+
		"<style>html,body{margin:0}body{width:%gpx;height:%gpx;position:relative;overflow:hidden;%sfont-family:Arial}p{margin:0}</style>\n%s",
		html.EscapeString(fontsCSS), w, h, html.EscapeString(bg), body.String())
}

// image returns the local name for url, registering it to be fetched.
func (b *builder) image(url string) string {
	if n, ok := b.images[url]; ok {
		return n
	}
	n := imageName(url)
	b.images[url] = n
	return n
}

func (b *builder) element(out *strings.Builder, e *Element, m affine) {
	t := m.times(e.Transform.affine())
	if g := e.ElementGroup; g != nil {
		for i := range g.Children {
			b.element(out, &g.Children[i], t)
		}
		return
	}
	if k := e.undrawn(); k != "" {
		if b.undrawn == nil {
			b.undrawn = map[string]int{}
		}
		b.undrawn[k]++
		return
	}
	w, h := e.Size.Width.pt(0), e.Size.Height.pt(0)
	nu, nv := math.Hypot(t[0], t[1]), math.Hypot(t[2], t[3])
	if nu == 0 {
		nu = 1
	}
	if nv == 0 {
		nv = 1
	}
	W, H := w*nu, h*nv
	box := fmt.Sprintf("position:absolute;left:0;top:0;width:%gpx;height:%gpx;transform-origin:0 0;transform:matrix(%g,%g,%g,%g,%g,%g);",
		W, H, t[0]/nu, t[1]/nu, t[2]/nv, t[3]/nv, t[4], t[5])
	switch {
	case e.Image != nil:
		if ol := e.Image.ImageProperties.Outline; ol.PropertyState != "NOT_RENDERED" && ol.PropertyState != "INHERIT" {
			if c := b.solid(ol.OutlineFill.SolidFill); c != "" {
				box += fmt.Sprintf("outline:%gpx solid %s;", ol.Weight.pt(0), c)
			}
		}
		fmt.Fprintf(out, "<div style=\"%soverflow:hidden\" title=\"%s\"><img src=\"%s\" style=\"width:100%%;height:100%%;object-fit:fill\"></div>\n",
			html.EscapeString(box), html.EscapeString(e.ObjectID), html.EscapeString(b.image(e.Image.ContentURL)))
	case e.Shape != nil:
		sh := e.Shape
		sp := sh.ShapeProperties
		in := b.inherit(sh)
		css := box
		if f := sp.ShapeBackgroundFill; f.PropertyState != "NOT_RENDERED" && f.PropertyState != "INHERIT" {
			if c := b.solid(f.SolidFill); c != "" {
				css += "background:" + c + ";"
			}
		}
		if ol := sp.Outline; ol.PropertyState != "NOT_RENDERED" && ol.PropertyState != "INHERIT" {
			if c := b.solid(ol.OutlineFill.SolidFill); c != "" {
				css += fmt.Sprintf("outline:%gpx solid %s;", ol.Weight.pt(0.75), c)
			}
		}
		// A rounded rectangle's corner radius is 16% of its shorter side,
		// and Slides keeps its text clear of the corners too.
		pad := textInset
		switch sh.ShapeType {
		case "ROUND_RECTANGLE":
			css += fmt.Sprintf("border-radius:%gpx;", math.Min(W, H)*0.16)
			pad += (1 - math.Sqrt2/2) * 0.16 * math.Min(W, H)
		case "ELLIPSE":
			css += "border-radius:50%;"
		}
		align := sp.ContentAlignment
		if align == "" {
			align = in.alignment
		}
		justify := map[string]string{"MIDDLE": "center", "BOTTOM": "flex-end"}[align]
		if justify == "" {
			justify = "flex-start"
		}
		css += fmt.Sprintf("display:flex;flex-direction:column;justify-content:%s;padding:%gpx;box-sizing:border-box;overflow:visible;", justify, pad)
		fmt.Fprintf(out, "<div style=\"%s\" title=\"%s\"><div>%s</div></div>\n", html.EscapeString(css), html.EscapeString(e.ObjectID), b.text(sh.Text, in))
	}
}

// text renders a shape's paragraphs, each run over the style its
// placeholder passes down for the paragraph's nesting level.
func (b *builder) text(t *Text, in inherited) string {
	if t == nil {
		return ""
	}
	type para struct {
		style ParaStyle
		base  TextStyle
		runs  []TextStyle
		texts []string
		end   *TextStyle
	}
	var paras []*para
	var cur *para
	for _, e := range t.TextElements {
		if m := e.ParagraphMarker; m != nil {
			n := 0
			if m.Bullet != nil {
				n = m.Bullet.NestingLevel
			}
			l := in.levels[n]
			cur = &para{style: m.Style.over(l.para), base: l.run}
			paras = append(paras, cur)
			continue
		}
		if e.TextRun == nil || cur == nil {
			continue
		}
		st := e.TextRun.Style.over(cur.base)
		c := e.TextRun.Content
		if strings.HasSuffix(c, "\n") {
			end := st
			cur.end = &end
		}
		c = strings.ReplaceAll(strings.ReplaceAll(c, "\n", ""), "\v", "\n")
		if c == "" {
			continue
		}
		cur.runs = append(cur.runs, st)
		cur.texts = append(cur.texts, c)
	}
	var out strings.Builder
	for _, p := range paras {
		align := map[string]string{"CENTER": "center", "END": "right", "JUSTIFIED": "justify"}[p.style.Alignment]
		if align == "" {
			align = "left"
		}
		ls := 100.0
		if p.style.LineSpacing != nil {
			ls = *p.style.LineSpacing
		}
		// The paragraph takes its end-of-paragraph style, as Slides sizes an
		// empty line and the line strut from it.
		end := p.base
		if p.end != nil {
			end = *p.end
		} else if len(p.runs) > 0 {
			end = p.runs[len(p.runs)-1]
		}
		css := fmt.Sprintf("margin:%gpx 0 %gpx %gpx;text-align:%s;line-height:%g;white-space:pre-wrap;%s",
			p.style.SpaceAbove.pt(0), p.style.SpaceBelow.pt(0), p.style.IndentStart.pt(0), align, ls/100*1.2, b.font(end))
		fmt.Fprintf(&out, "<p style=\"%s\">", html.EscapeString(css))
		if len(p.runs) == 0 {
			out.WriteString("&#8203;")
		}
		for i, st := range p.runs {
			fmt.Fprintf(&out, "<span style=\"%s\">%s</span>", html.EscapeString(b.font(st)+b.decoration(st)), html.EscapeString(p.texts[i]))
		}
		out.WriteString("</p>")
	}
	return out.String()
}

// font is a run's face and size as CSS, registering the face to fetch.
func (b *builder) font(st TextStyle) string {
	v := fonts.Variant{Family: st.family(), Weight: st.weight(), Italic: st.italic()}
	b.variants[v] = true
	css := fmt.Sprintf("font-family:%s;font-size:%gpx;font-weight:%d;", cssString(v.Family), st.FontSize.pt(18), v.Weight)
	if v.Italic {
		css += "font-style:italic;"
	}
	return css
}

func (b *builder) decoration(st TextStyle) string {
	css := ""
	if st.ForegroundColor != nil {
		if c := b.color(st.ForegroundColor.OpaqueColor, 1); c != "" {
			css += "color:" + c + ";"
		}
	}
	var d []string
	if st.Underline != nil && *st.Underline {
		d = append(d, "underline")
	}
	if st.Strikethrough != nil && *st.Strikethrough {
		d = append(d, "line-through")
	}
	if len(d) > 0 {
		css += "text-decoration:" + strings.Join(d, " ") + ";"
	}
	return css
}

// cssString quotes s as a CSS string.
func cssString(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\a `).Replace(s) + `"`
}
