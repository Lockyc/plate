package clip

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
)

// Follow-camera timing, ms, and the padding a goal keeps around its target, pt.
const (
	followPad = 16.0
	lead      = 250.0 // a move arrives this long before its action
	move      = 700.0 // a move's full length
	minMove   = 300.0 // the shortest move; closer goals merge
	// loopMS is the loop close's length (closeLoop); the return move to R0
	// lands as it starts.
	loopMS = 300.0
)

// FollowHead is the trim head a follow clip needs: a full move and its
// lead before the first action, after the settle.
func FollowHead() float64 { return Settle + move + lead }

// PinSlot is one camera pin; System marks a system target (the keyboard).
type PinSlot struct {
	Name   string
	System bool
}

// Layout is the clip's output frame in px: W×H, fixed for the clip, with
// the region's slot height at the first rects sample.
type Layout struct {
	W, H, RegionH int
	Pins          []PinSlot
}

// PinView is one pin strip at one frame: Src in points, Y and H in output
// px. H = W·Src.H/Src.W, so a strip is never stretched.
type PinView struct {
	Name        string
	Src         Rect
	Y, H, Shown float64
}

// View is one output frame: the framed region in points, its slot in
// output px, and the strips stacked above and below it in screen order.
type View struct {
	Region           Rect
	RegionY, RegionH float64
	Pins             []PinView
}

// camState is where the camera points: a centre and a width, in points.
// The height follows the region's slot at each frame.
type camState struct{ cx, cy, w float64 }

type camera struct {
	tr     *Track
	lay    Layout
	r0     Rect
	screen Rect
	zoom   float64
	minW   float64 // the narrowest region max_zoom allows, pt
	// stack is the pins in output order, top to bottom, fixed for the clip;
	// above says which side of the region each sits on.
	stack []PinSlot
	above map[string]bool
	// restA and restB are the px the strips above and below take at the
	// first rects sample, unrounded: with strips that never change, the
	// state's centre is the region's centre on every frame.
	restA, restB float64
}

func newCamera(s *Script, l *Log, tr *Track, lay Layout) (*camera, error) {
	r0, err := startRect(s, l, tr)
	if err != nil {
		return nil, err
	}
	c := &camera{tr: tr, lay: lay, r0: r0,
		screen: Rect{0, 0, l.Meta.Screen[0], l.Meta.Screen[1]},
		zoom:   *s.Camera.MaxZoom,
		minW:   float64(s.Output.Width) / (*s.Camera.MaxZoom * l.Meta.Scale)}
	c.order()
	for _, p := range c.stack {
		h := c.pin(p, tr.first).H
		if c.above[p.Name] {
			c.restA += h
		} else {
			c.restB += h
		}
	}
	return c, nil
}

// order fixes each pin's side and the strips' order once for the clip, so
// a strip never swaps places mid-clip. A pin sits above the region when
// its centre, at its first up sample, lies above R0's centre. Two pins on
// one side stack in screen order at the first sample both are up (or, if
// never together, at each one's first up sample).
func (c *camera) order() {
	firstUp := func(name string) (Rect, float64) {
		for _, s := range c.tr.series[name] {
			if s.r != nil {
				return *s.r, s.t
			}
		}
		return Rect{}, 0
	}
	centre := func(r Rect) float64 { return r.Y + r.H/2 }
	cy := centre(c.r0)
	c.above = map[string]bool{}
	for _, p := range c.lay.Pins {
		r, _ := firstUp(p.Name)
		c.above[p.Name] = centre(r) < cy
	}
	together := func(a, b string) (float64, float64) {
		var ts []float64
		for _, s := range append(append([]sample{}, c.tr.series[a]...), c.tr.series[b]...) {
			ts = append(ts, s.t)
		}
		sort.Float64s(ts)
		for _, t := range ts {
			ra, sa := c.tr.PinAt(a, t)
			rb, sb := c.tr.PinAt(b, t)
			if sa == 1 && sb == 1 {
				return centre(ra), centre(rb)
			}
		}
		ra, _ := firstUp(a)
		rb, _ := firstUp(b)
		return centre(ra), centre(rb)
	}
	c.stack = append([]PinSlot{}, c.lay.Pins...)
	sort.SliceStable(c.stack, func(i, j int) bool {
		a, b := c.stack[i].Name, c.stack[j].Name
		if c.above[a] != c.above[b] {
			return c.above[a]
		}
		ya, yb := together(a, b)
		return ya < yb
	})
}

// pin is pin slot p at t. An element pin is always fully shown. A system
// pin's Src is the Shown share of its rect nearest the screen edge it
// slides in from (the bottom for a pin below the region), where it
// physically is mid-slide, so Src keeps the strip's aspect.
func (c *camera) pin(p PinSlot, t float64) PinView {
	r, shown := c.tr.PinAt(p.Name, t)
	if !p.System {
		shown = 1
	}
	if shown <= 0 {
		return PinView{Name: p.Name}
	}
	src := Rect{r.X, r.Y, r.W, r.H * shown}
	if !c.above[p.Name] {
		src.Y = r.Y + r.H*(1-shown)
	}
	return PinView{Name: p.Name, Src: src, Shown: shown, H: float64(c.lay.W) * src.H / src.W}
}

// unshared crops the shown strips at t, in stack order, so no two show the
// same master rows. Strips' rects come from separate samples, and between
// samples an element strip riding the keyboard (a touchbar, a filter bar)
// moves on its own interpolation, not flush with the keyboard's eased
// share, so their Srcs overlap mid-ease and the overlap would be drawn
// twice. Contested rows go to the strip nearer the screen edge its side is
// anchored to (the outer one in the stack), the inner one losing them off
// its outer edge; its H follows its Src at the strip's own scale. A strip
// cropped to nothing is dropped.
func (c *camera) unshared(t float64) []PinView {
	var above, below []PinView
	for _, p := range c.stack {
		if pv := c.pin(p, t); pv.Shown > 0 {
			if c.above[p.Name] {
				above = append(above, pv)
			} else {
				below = append(below, pv)
			}
		}
	}
	scale := func(pv *PinView) bool {
		pv.H = float64(c.lay.W) * pv.Src.H / pv.Src.W
		return pv.Src.H > 1e-9
	}
	var out []PinView
	limit := c.screen.Y
	for _, pv := range above { // outermost first
		if pv.Src.Y < limit {
			pv.Src.H -= limit - pv.Src.Y
			pv.Src.Y = limit
		}
		if scale(&pv) {
			out = append(out, pv)
			limit = math.Max(limit, pv.Src.Y+pv.Src.H)
		}
	}
	var kept []PinView
	limit = c.screen.Y + c.screen.H
	for i := len(below) - 1; i >= 0; i-- { // outermost first
		pv := below[i]
		if pv.Src.Y+pv.Src.H > limit {
			pv.Src.H = limit - pv.Src.Y
		}
		if scale(&pv) {
			kept = append(kept, pv)
			limit = math.Min(limit, pv.Src.Y)
		}
	}
	for i := len(kept) - 1; i >= 0; i-- {
		out = append(out, kept[i])
	}
	return out
}

// strips stacks the shown pins at t in their fixed order, cropped so none
// shares rows with another (unshared), and gives the region's slot, the
// screen area the region may occupy (between the strips' Src), and how
// many px the strips above and below take beyond their first-sample share.
func (c *camera) strips(t float64) (v View, bounds Rect, growA, growB float64) {
	top, bottom := c.screen.Y, c.screen.Y+c.screen.H
	var sumA, sumB float64
	y := 0.0
	for _, pv := range c.unshared(t) {
		if c.above[pv.Name] {
			sumA += pv.H
			top = math.Max(top, pv.Src.Y+pv.Src.H)
			pv.Y = y
			y += pv.H
		} else {
			sumB += pv.H
			bottom = math.Min(bottom, pv.Src.Y)
		}
		v.Pins = append(v.Pins, pv)
	}
	v.RegionY = sumA
	v.RegionH = float64(c.lay.H) - sumA - sumB
	y = v.RegionY + v.RegionH
	for i := range v.Pins {
		if !c.above[v.Pins[i].Name] {
			v.Pins[i].Y = y
			y += v.Pins[i].H
		}
	}
	return v, Rect{c.screen.X, top, c.screen.W, bottom - top}, sumA - c.restA, sumB - c.restB
}

// view is the frame at t with the camera at st. The state is the region
// at the first sample's strips; strips growing above take their height
// off the region's top, those below off its bottom, so the content stays
// still while a strip eases in. The region is then fitted into its bounds.
func (c *camera) view(st camState, t float64) View {
	v, b, growA, _ := c.strips(t)
	w := float64(c.lay.W)
	a := w / v.RegionH
	top := st.cy - st.w*c.restSlot()/w/2 + growA*st.w/w
	v.Region = bound(Rect{st.cx - st.w/2, top, st.w, st.w / a}, a, b)
	return v
}

// fit is the camera state framing r at t: the smallest rect of the slot's
// aspect containing r, grown to the zoom clamp, bounded.
func (c *camera) fit(r Rect, t float64) camState {
	cx, cy := r.X+r.W/2, r.Y+r.H/2
	v, b, growA, _ := c.strips(t)
	w := float64(c.lay.W)
	a := w / v.RegionH
	rw := math.Max(math.Max(r.W, r.H*a), c.minW)
	f := bound(Rect{cx - rw/2, cy - rw/a/2, rw, rw / a}, a, b)
	return camState{f.X + f.W/2, f.Y - growA*f.W/w + f.W*c.restSlot()/w/2, f.W}
}

// restSlot is the region's slot, px, with the strips at their first-sample
// heights: the height the camera state describes.
func (c *camera) restSlot() float64 { return float64(c.lay.H) - c.restA - c.restB }

// bound shifts r of aspect a inside b; a rect larger than b becomes the
// largest a-rect inside it, about r's centre, then shifted inside.
func bound(r Rect, a float64, b Rect) Rect {
	if r.W > b.W || r.H > b.H {
		cx, cy := r.X+r.W/2, r.Y+r.H/2
		w := math.Min(b.W, b.H*a)
		r = Rect{cx - w/2, cy - w/a/2, w, w / a}
	}
	r.X = math.Max(b.X, math.Min(r.X, b.X+b.W-r.W))
	r.Y = math.Max(b.Y, math.Min(r.Y, b.Y+b.H-r.H))
	return r
}

func union(a, b Rect) Rect {
	x, y := math.Min(a.X, b.X), math.Min(a.Y, b.Y)
	return Rect{x, y, math.Max(a.X+a.W, b.X+b.W) - x, math.Max(a.Y+a.H, b.Y+b.H) - y}
}

func inside(r, b Rect) bool {
	const eps = 1e-9
	return b.X >= r.X-eps && b.Y >= r.Y-eps && b.X+b.W <= r.X+r.W+eps && b.Y+b.H <= r.Y+r.H+eps
}

// startRect is R0, the rect the camera starts on: the screen in full mode,
// else the region at the first rects sample (the screen if follow names none).
func startRect(s *Script, l *Log, tr *Track) (Rect, error) {
	screen := Rect{0, 0, l.Meta.Screen[0], l.Meta.Screen[1]}
	if s.Camera.Mode == "full" || s.Camera.Region == nil {
		return screen, nil
	}
	r, ok := tr.RectAt(s.Camera.Region.Name, tr.first)
	if !ok {
		return Rect{}, fmt.Errorf("rects lacks %s", s.Camera.Region.Name)
	}
	return r, nil
}

func zoomString(z float64) string {
	s := strconv.FormatFloat(z, 'f', -1, 64)
	if !strings.Contains(s, ".") {
		s += ".0"
	}
	return s
}

// NewLayout sizes the output frame from the first rects sample, and refuses
// a clip that would upsample the master or squeeze the region under a
// quarter of the frame at any sample.
func NewLayout(s *Script, l *Log, tr *Track) (Layout, error) {
	if math.IsInf(tr.first, 1) {
		return Layout{}, errors.New("no rects sample")
	}
	w := s.Output.Width
	lay := Layout{W: w}
	for _, p := range s.Camera.Pin {
		lay.Pins = append(lay.Pins, PinSlot{Name: p.Name, System: p.System != ""})
	}
	c, err := newCamera(s, l, tr, lay)
	if err != nil {
		return Layout{}, err
	}
	if c.screen.W < c.minW {
		return Layout{}, fmt.Errorf("output.width %d px would sample below capture resolution: the master is %g px across, max_zoom %s",
			w, c.screen.W*l.Meta.Scale, zoomString(c.zoom))
	}
	for _, e := range l.Events {
		if e.Type != "rects" {
			continue
		}
		for _, p := range lay.Pins {
			if r := e.Rects[p.Name]; r != nil && r.W < c.minW {
				return Layout{}, fmt.Errorf("output.width %d px would sample below capture resolution: pin %s is %g px across at t=%g ms, max_zoom %s",
					w, p.Name, r.W*l.Meta.Scale, e.T, zoomString(c.zoom))
			}
		}
	}

	lay.RegionH = int(math.Round(float64(w) * c.r0.H / c.r0.W))
	lay.H = lay.RegionH
	for _, p := range lay.Pins {
		lay.H += int(math.Round(c.pin(p, tr.first).H))
	}
	if lay.H%2 != 0 {
		lay.RegionH++
		lay.H++
	}

	c.lay = lay
	for _, e := range l.Events {
		if e.Type != "rects" {
			continue
		}
		var names []string
		total := 0.0
		for _, p := range lay.Pins {
			if pv := c.pin(p, e.T); pv.Shown > 0 {
				names = append(names, p.Name)
				total += pv.H
			}
		}
		if float64(lay.H)-total < float64(lay.H)/4 {
			word := "pins"
			if len(names) == 1 {
				word = "pin"
			}
			return Layout{}, fmt.Errorf("%s %s would leave the framed region under a quarter of the frame at t=%g ms",
				word, strings.Join(names, ", "), e.T)
		}
	}

	// The start state frames every sample in full and fixed mode; in follow
	// it holds at least at the first, and Path checks every frame.
	start := c.fit(c.r0, tr.first)
	for _, e := range l.Events {
		if e.Type != "rects" || (s.Camera.Mode == "follow" && e.T != tr.first) {
			continue
		}
		if err := c.sampled(c.view(start, e.T), e.T); err != nil {
			return Layout{}, err
		}
	}
	return lay, nil
}

// sampled refuses a frame whose region, squeezed by its bounds, is narrower
// than the zoom clamp allows.
func (c *camera) sampled(v View, t float64) error {
	if v.Region.W < c.minW-1e-9 {
		return fmt.Errorf("camera region would sample below capture resolution at t=%g ms: %g pt across, minimum %g pt at max_zoom %s",
			t, v.Region.W, c.minW, zoomString(c.zoom))
	}
	return nil
}

// key is one camera move: depart from the previous key's state, arrive at g.
type key struct {
	depart, arrive float64
	g              camState
}

// Path is the camera's view at each output frame of w; frame k is at master
// time w.Start + k·1000/fps.
func Path(s *Script, l *Log, tr *Track, lay Layout, w Window) ([]View, error) {
	c, keys, err := cameraKeys(s, l, tr, lay, w)
	if err != nil {
		return nil, err
	}
	fps := float64(*s.FPS)
	vs := make([]View, w.Frames(*s.FPS))
	for k := range vs {
		t := w.Start + float64(k)*1000/fps
		vs[k] = c.view(stateAt(keys, t), t)
		if err := c.sampled(vs[k], t); err != nil {
			return nil, err
		}
	}
	return vs, nil
}

// cameraKeys is the camera over w and its moves: R0 alone for full and
// fixed, and a move per goal for follow.
func cameraKeys(s *Script, l *Log, tr *Track, lay Layout, w Window) (*camera, []key, error) {
	c, err := newCamera(s, l, tr, lay)
	if err != nil {
		return nil, nil, err
	}
	keys := []key{{depart: w.Start, arrive: w.Start, g: c.fit(c.r0, tr.first)}}
	switch s.Camera.Mode {
	case "full", "fixed":
	case "follow":
		keys = c.follow(l, w, keys)
	default:
		return nil, nil, fmt.Errorf("camera.mode %q: want full, fixed or follow", s.Camera.Mode)
	}
	return c, keys, nil
}

// FollowMoves is the master time each follow-camera move needs kept, from
// a full move and its lead before the moment it serves (its arrival plus
// lead) to that moment, so IdleCuts never shortens a pan or starts one
// over the previous action's response. Nil for a camera that never moves.
func FollowMoves(s *Script, l *Log, tr *Track, lay Layout, w Window) ([]Window, error) {
	_, keys, err := cameraKeys(s, l, tr, lay, w)
	if err != nil {
		return nil, err
	}
	var out []Window
	for _, k := range keys[1:] {
		at := k.arrive + lead
		out = append(out, Window{at - move - lead, at})
	}
	return out, nil
}

// goalBox is what an action event must keep in shot: its target and any
// from/to points, or without a target its at or from/to points. A key
// event (and a type with no target) has none.
func goalBox(e Event) (Rect, bool) {
	var pts []Point
	if e.Target != nil {
		t := *e.Target
		pts = append(pts, Point{t.X, t.Y}, Point{t.X + t.W, t.Y + t.H})
	} else if e.At != nil {
		pts = append(pts, *e.At)
	}
	if e.Type == "swipe" || e.Type == "drag" || (e.Target == nil && e.At == nil) {
		for _, p := range []*Point{e.From, e.To} {
			if p != nil {
				pts = append(pts, *p)
			}
		}
	}
	if len(pts) == 0 {
		return Rect{}, false
	}
	x0, y0, x1, y1 := pts[0].X, pts[0].Y, pts[0].X, pts[0].Y
	for _, p := range pts[1:] {
		x0, y0 = math.Min(x0, p.X), math.Min(y0, p.Y)
		x1, y1 = math.Max(x1, p.X), math.Max(y1, p.Y)
	}
	return Rect{x0, y0, x1 - x0, y1 - y0}, true
}

// onPin reports whether box lies inside a pin strip shown at t: the strip
// already keeps it in shot.
func (c *camera) onPin(box Rect, t float64) bool {
	v, _, _, _ := c.strips(t)
	for _, p := range v.Pins {
		if inside(p.Src, box) {
			return true
		}
	}
	return false
}

// follow appends a move per action whose padded target the camera does not
// already frame, arriving lead before it; goals too close to fit a minMove
// move merge, and a return to the opening state lands as the loop close
// starts.
func (c *camera) follow(l *Log, w Window, keys []key) []key {
	for _, e := range l.Events {
		if _, ok := actionOf[e.Type]; !ok {
			continue
		}
		raw, ok := goalBox(e)
		if !ok || c.onPin(raw, e.T) {
			continue
		}
		box := Rect{raw.X - followPad, raw.Y - followPad, raw.W + 2*followPad, raw.H + 2*followPad}
		if inside(c.view(keys[len(keys)-1].g, e.T).Region, box) {
			continue
		}
		g, arrive := c.fit(box, e.T), e.T-lead
		for {
			prev := keys[len(keys)-1]
			depart := math.Max(prev.arrive, arrive-move)
			if arrive-depart >= minMove {
				keys = append(keys, key{depart, arrive, g})
				break
			}
			merged := c.fit(union(c.view(prev.g, e.T).Region, c.view(g, e.T).Region), e.T)
			if len(keys) == 1 { // no room to move from R0: start on the union
				keys[0].g = merged
				break
			}
			keys = keys[:len(keys)-1]
			g, arrive = merged, prev.arrive
		}
	}
	if last := keys[len(keys)-1]; len(keys) > 1 {
		arrive := w.End - loopMS
		if arrive-last.arrive >= minMove+400 {
			// Home is where the clip opens, so the loop seam matches.
			keys = append(keys, key{math.Max(last.arrive, arrive-move), arrive, keys[0].g})
		}
	}
	return keys
}

// stateAt is the camera at t: held between moves, and through a move the
// centre lerped and the width geometric, both by the eased parameter.
func stateAt(keys []key, t float64) camState {
	cur := keys[0].g
	for _, k := range keys[1:] {
		if t <= k.depart {
			return cur
		}
		if t < k.arrive {
			e := easeInOutCubic((t - k.depart) / (k.arrive - k.depart))
			return camState{
				cx: cur.cx + (k.g.cx-cur.cx)*e,
				cy: cur.cy + (k.g.cy-cur.cy)*e,
				w:  math.Exp(math.Log(cur.w) + (math.Log(k.g.w)-math.Log(cur.w))*e),
			}
		}
		cur = k.g
	}
	return cur
}
