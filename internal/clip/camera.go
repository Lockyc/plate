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
	// crossfadeMS is the loop crossfade's length; the return move to R0
	// lands as it starts.
	crossfadeMS = 300.0
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
// px. H = Shown·W·Src.H/Src.W, so a strip is never stretched.
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
	screen Rect
	minW   float64
}

func newCamera(s *Script, l *Log, tr *Track, lay Layout) *camera {
	return &camera{tr: tr, lay: lay,
		screen: Rect{0, 0, l.Meta.Screen[0], l.Meta.Screen[1]},
		minW:   float64(lay.W) / (*s.Camera.MaxZoom * l.Meta.Scale)}
}

// pin is pin slot p at t; an element pin is always fully shown.
func (c *camera) pin(p PinSlot, t float64) PinView {
	r, shown := c.tr.PinAt(p.Name, t)
	if !p.System {
		shown = 1
	}
	return PinView{Name: p.Name, Src: r, Shown: shown, H: shown * float64(c.lay.W) * r.H / r.W}
}

// strips stacks the shown pins at t about a region centred at cy, and gives
// the region's slot and the screen area the region may occupy. A strip
// whose centre is above cy sits above the region. A partly shown pin bounds
// the region at its edge eased in from the screen edge, as it slides in.
func (c *camera) strips(t, cy float64) (v View, bounds Rect) {
	var above, below []PinView
	edge := c.screen.Y + c.screen.H
	top, bottom := c.screen.Y, edge
	total := 0.0
	for _, p := range c.lay.Pins {
		pv := c.pin(p, t)
		if pv.Shown <= 0 {
			continue
		}
		total += pv.H
		if pv.Src.Y+pv.Src.H/2 < cy {
			above = append(above, pv)
			top = math.Max(top, c.screen.Y+pv.Shown*(pv.Src.Y+pv.Src.H-c.screen.Y))
		} else {
			below = append(below, pv)
			bottom = math.Min(bottom, edge+pv.Shown*(pv.Src.Y-edge))
		}
	}
	byY := func(ps []PinView) {
		sort.SliceStable(ps, func(i, j int) bool { return ps[i].Src.Y < ps[j].Src.Y })
	}
	byY(above)
	byY(below)
	y := 0.0
	for i := range above {
		above[i].Y = y
		y += above[i].H
	}
	v.RegionY = y
	v.RegionH = float64(c.lay.H) - total
	y += v.RegionH
	for i := range below {
		below[i].Y = y
		y += below[i].H
	}
	v.Pins = append(above, below...)
	return v, Rect{c.screen.X, top, c.screen.W, bottom - top}
}

// view is the frame at t with the camera at st: the region's height follows
// its slot about the centre, then the region is fitted into its bounds.
func (c *camera) view(st camState, t float64) View {
	v, b := c.strips(t, st.cy)
	a := float64(c.lay.W) / v.RegionH
	v.Region = bound(Rect{st.cx - st.w/2, st.cy - st.w/a/2, st.w, st.w / a}, a, b)
	return v
}

// fit is the camera state framing r at t: the smallest rect of the slot's
// aspect containing r, grown to the zoom clamp, bounded.
func (c *camera) fit(r Rect, t float64) camState {
	cx, cy := r.X+r.W/2, r.Y+r.H/2
	v, b := c.strips(t, cy)
	a := float64(c.lay.W) / v.RegionH
	w := math.Max(math.Max(r.W, r.H*a), c.minW)
	f := bound(Rect{cx - w/2, cy - w/a/2, w, w / a}, a, b)
	return camState{f.X + f.W/2, f.Y + f.H/2, f.W}
}

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
	zoom := *s.Camera.MaxZoom
	minW := float64(w) / (zoom * l.Meta.Scale)
	if l.Meta.Screen[0] < minW {
		return Layout{}, fmt.Errorf("output.width %d px would sample below capture resolution: the master is %g px across, max_zoom %s",
			w, l.Meta.Screen[0]*l.Meta.Scale, zoomString(zoom))
	}
	lay := Layout{W: w}
	for _, p := range s.Camera.Pin {
		lay.Pins = append(lay.Pins, PinSlot{Name: p.Name, System: p.System != ""})
	}
	c := &camera{tr: tr, lay: lay}
	for _, e := range l.Events {
		if e.Type != "rects" {
			continue
		}
		for _, p := range lay.Pins {
			if r := e.Rects[p.Name]; r != nil && r.W < minW {
				return Layout{}, fmt.Errorf("output.width %d px would sample below capture resolution: pin %s is %g px across at t=%g ms, max_zoom %s",
					w, p.Name, r.W*l.Meta.Scale, e.T, zoomString(zoom))
			}
		}
	}

	r, err := startRect(s, l, tr)
	if err != nil {
		return Layout{}, err
	}
	lay.RegionH = int(math.Round(float64(w) * r.H / r.W))
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
	return lay, nil
}

// key is one camera move: depart from the previous key's state, arrive at g.
type key struct {
	depart, arrive float64
	g              camState
}

// Path is the camera's view at each output frame of w; frame k is at master
// time w.Start + k·1000/fps.
func Path(s *Script, l *Log, tr *Track, lay Layout, w Window) ([]View, error) {
	c := newCamera(s, l, tr, lay)
	r, err := startRect(s, l, tr)
	if err != nil {
		return nil, err
	}
	start := c.fit(r, tr.first)
	keys := []key{{depart: w.Start, arrive: w.Start, g: start}}
	switch s.Camera.Mode {
	case "full", "fixed":
	case "follow":
		keys = c.follow(l, w, keys)
	default:
		return nil, fmt.Errorf("camera.mode %q: want full, fixed or follow", s.Camera.Mode)
	}
	fps := float64(*s.FPS)
	vs := make([]View, w.Frames(*s.FPS))
	for k := range vs {
		t := w.Start + float64(k)*1000/fps
		vs[k] = c.view(stateAt(keys, t), t)
	}
	return vs, nil
}

// goalBox is what an action event must keep in shot: its target and any
// from/to points, or without a target its at or from/to points, padded.
// A key event (and a type with no target) has none.
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
	return Rect{x0 - followPad, y0 - followPad, x1 - x0 + 2*followPad, y1 - y0 + 2*followPad}, true
}

// follow appends a move per action whose padded target the camera does not
// already frame, arriving lead before it; goals too close to fit a minMove
// move merge, and a return to R0 lands as the loop crossfade starts.
func (c *camera) follow(l *Log, w Window, keys []key) []key {
	home := keys[0].g
	for _, e := range l.Events {
		if _, ok := actionOf[e.Type]; !ok {
			continue
		}
		box, ok := goalBox(e)
		if !ok || inside(c.view(keys[len(keys)-1].g, e.T).Region, box) {
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
		arrive := w.End - crossfadeMS
		if arrive-last.arrive >= minMove+400 {
			keys = append(keys, key{math.Max(last.arrive, arrive-move), arrive, home})
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
