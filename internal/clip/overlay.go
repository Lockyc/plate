package clip

import (
	"fmt"
	"math"

	"github.com/lockyc/plate/internal/video"
)

// Overlay timing, ms, and sizes, pt. The touch values approximate iOS's own
// touch indicator.
const (
	captionFade = 150.0

	tapDiameter = 44.0
	tapIn       = 80.0
	tapHold     = 100.0
	tapOut      = 200.0
	tapFromS    = 0.8  // scale as the disc appears
	tapToS      = 1.15 // scale as it releases

	clickMS       = 250.0
	clickDiameter = 40.0
	clickFromS    = 0.3

	keyHold = 900.0
	keyFade = 150.0

	cursorLead   = 150.0
	cursorHeight = 20.0

	// spritePad is the room a point sprite's page leaves around what it
	// draws, for a shadow or stroke.
	spritePad = 4.0
)

// keyGlyphs draws the contract's named keys.
var keyGlyphs = map[string]string{
	"return": "↩", "delete": "⌫", "tab": "⇥", "escape": "⎋", "space": "␣",
	"up": "↑", "down": "↓", "left": "←", "right": "→",
}

// Op is one sprite placed on one frame, in output px. A point sprite is
// centred on X,Y (the cursor on its hotspot); a full-frame sprite sits at
// X = 0 and Y its vertical offset: 0 unless a strip took space below the region.
type Op struct {
	Key                  string
	X, Y, Scale, Opacity float64
}

// pointKinds are the point sprites, each keyed by its kind; every other
// sprite is full-frame.
var pointKinds = []string{"tap", "click", "cursor"}

func clamp01(x float64) float64 { return math.Max(0, math.Min(1, x)) }

// toOutput maps p through the pin strip or the region it falls in, and
// gives the px per pt there. ok is false outside both.
func toOutput(p Point, v View, lay Layout) (x, y, k float64, ok bool) {
	src, ok := sourceAt(p, v)
	if !ok {
		return 0, 0, 0, false
	}
	return toOutputVia(p, v, lay, src)
}

func contains(r Rect, p Point) bool {
	return p.X >= r.X && p.X < r.X+r.W && p.Y >= r.Y && p.Y < r.Y+r.H
}

// sourceAt is what shows p in v: a shown pin strip, by name, or the
// region, "".
func sourceAt(p Point, v View) (string, bool) {
	for _, pv := range v.Pins {
		if pv.Shown > 0 && contains(pv.Src, p) {
			return pv.Name, true
		}
	}
	return "", contains(v.Region, p)
}

// toOutputVia maps p through src alone, the pin strip of that name or the
// region for "": ok is false while src does not show p. An action's
// overlay maps through the source it landed in (landing), so it never
// moves onto content that slid in over the same screen point, such as a
// keyboard rising over a tapped row.
func toOutputVia(p Point, v View, lay Layout, src string) (x, y, k float64, ok bool) {
	if src == "" {
		r := v.Region
		if !contains(r, p) {
			return 0, 0, 0, false
		}
		k = float64(lay.W) / r.W
		return (p.X - r.X) * k, v.RegionY + (p.Y-r.Y)*k, k, true
	}
	for _, pv := range v.Pins {
		if pv.Name == src && pv.Shown > 0 && contains(pv.Src, p) {
			k = float64(lay.W) / pv.Src.W
			return (p.X - pv.Src.X) * k, pv.Y + (p.Y-pv.Src.Y)*k, k, true
		}
	}
	return 0, 0, 0, false
}

// landing tracks an action overlay's source: the first frame that shows
// its point fixes the strip or the region, and once that source stops
// showing it the overlay is over.
type landing struct {
	src           string
	landed, ended bool
}

// at is p on v through the landed source; ok is false before the point is
// shown anywhere and after the overlay has ended.
func (ln *landing) at(p Point, v View, lay Layout) (x, y, k float64, ok bool) {
	if ln.ended {
		return 0, 0, 0, false
	}
	if !ln.landed {
		if ln.src, ln.landed = sourceAt(p, v); !ln.landed {
			return 0, 0, 0, false
		}
	}
	x, y, k, ok = toOutputVia(p, v, lay, ln.src)
	ln.ended = !ok
	return x, y, k, ok
}

// pointOp is a point-sprite op before its scale is normalised: the sprite
// kind, where it sits in ops, and px per pt times the envelope scale.
type pointOp struct {
	kind   string
	frame  int
	index  int
	cam, s float64
}

type timeline struct {
	s     *Script
	lay   Layout
	views []View
	w     Window
	fps   float64
	ops   [][]Op
	specs []SpriteSpec
	warns []string
	pts   []pointOp
	seen  map[string]bool
}

func (tl *timeline) at(k int) float64 { return tl.w.Start + float64(k)*1000/tl.fps }

// textOp places a full-frame sprite on frame k; like a caption it rides up
// with the region as a strip takes space below it.
func (tl *timeline) textOp(k int, key string, opacity float64) {
	v := tl.views[k]
	tl.ops[k] = append(tl.ops[k], Op{Key: key, Y: v.RegionY + v.RegionH - float64(tl.lay.RegionH), Scale: 1, Opacity: opacity})
}

func (tl *timeline) text(key, kind, text string) {
	if tl.seen[key] {
		return
	}
	tl.seen[key] = true
	tl.specs = append(tl.specs, SpriteSpec{Key: key, Kind: kind, Text: text, W: tl.lay.W, H: tl.lay.H, RegionH: tl.lay.RegionH, Scale: 1})
}

// fade is the opacity of a span [from, to] at t, ramping in and out over f.
func fade(t, from, to, f float64) float64 {
	return math.Min(clamp01((t-from)/f), clamp01(1-(t-to)/f))
}

func (tl *timeline) point(k int, kind string, x, y, cam, s, opacity float64) {
	tl.pts = append(tl.pts, pointOp{kind, k, len(tl.ops[k]), cam, s})
	tl.ops[k] = append(tl.ops[k], Op{Key: kind, X: x, Y: y, Opacity: opacity})
}

// Timeline places every overlay on every frame of the window, from the log
// and the camera's views. Point sprites are normalised so each kind's
// largest Op.Scale is its spec's Scale.
func Timeline(s *Script, l *Log, lay Layout, views []View, w Window) (ops [][]Op, specs []SpriteSpec, warnings []string) {
	tl := &timeline{s: s, lay: lay, views: views, w: w, fps: float64(*s.FPS),
		ops: make([][]Op, len(views)), seen: map[string]bool{}}
	if s.Overlays.Captions {
		tl.captions(l)
	}
	if s.Overlays.Keys {
		tl.keys(l)
	}
	if s.Overlays.Taps {
		tl.taps(l)
	}
	if s.Overlays.Cursor {
		tl.cursor(l)
	}
	tl.normalise()
	return tl.ops, tl.specs, tl.warns
}

func (tl *timeline) captions(l *Log) {
	var caps []Event
	for _, e := range l.Events {
		if e.Type == "caption" {
			caps = append(caps, e)
		}
	}
	for i, e := range caps {
		if e.Text == "" {
			continue
		}
		end := tl.w.End
		if i+1 < len(caps) {
			end = caps[i+1].T
		}
		key := fmt.Sprintf("caption-%d", i)
		for k := range tl.views {
			if t := tl.at(k); t >= e.T && t <= end+captionFade {
				tl.textOp(k, key, fade(t, e.T, end, captionFade))
				tl.text(key, "caption", e.Text)
			}
		}
	}
}

func (tl *timeline) keys(l *Log) {
	for _, e := range l.Events {
		if e.Type != "key" {
			continue
		}
		glyph, ok := keyGlyphs[e.Key]
		if !ok {
			glyph = e.Key
		}
		key := "key-" + e.Key
		for k := range tl.views {
			if t := tl.at(k); t >= e.T && t <= e.T+keyHold {
				tl.textOp(k, key, fade(t, e.T, e.T+keyHold-keyFade, keyFade))
				tl.text(key, "keys", glyph)
			}
		}
	}
}

func stepOf(e Event) int {
	if e.Step != nil {
		return *e.Step
	}
	return -1
}

func (tl *timeline) warnOutside(e Event, p Point) {
	tl.warns = append(tl.warns, fmt.Sprintf("step %d %s at (%g,%g) is outside the framed area", stepOf(e), e.Type, p.X, p.Y))
}

// touch is one touch gesture: the disc moves from..to over dur after
// appearing, holds, and releases.
type touch struct {
	from, to  Point
	dur, hold float64
}

func (tl *timeline) taps(l *Log) {
	for _, e := range l.Events {
		if tl.s.Form == "desktop" {
			if (e.Type == "tap" || e.Type == "long_press") && e.At != nil {
				tl.click(e)
			}
			continue
		}
		if g, ok := touchOf(e); ok {
			tl.touch(e, g)
		}
	}
}

// touchOf is e's touch disc on a phone, if it draws one.
func touchOf(e Event) (touch, bool) {
	switch {
	case (e.Type == "tap") && e.At != nil:
		return touch{*e.At, *e.At, 0, tapHold}, true
	case e.Type == "long_press" && e.At != nil:
		return touch{*e.At, *e.At, 0, e.Duration}, true
	case (e.Type == "swipe" || e.Type == "drag") && e.From != nil && e.To != nil:
		return touch{*e.From, *e.To, e.Duration, math.Max(0, e.Duration-tapIn)}, true
	}
	return touch{}, false
}

// life is how long g's disc shows from its event's t, ms.
func (g touch) life() float64 { return tapIn + g.hold + tapOut }

// overlayEnd is when e's own overlay is gone, ms of master time: a touch
// disc's life on a phone, a click's on a desktop, a key badge's; e.T for an
// event that draws none.
func overlayEnd(e Event, form string) float64 {
	switch {
	case e.Type == "key":
		return e.T + keyHold
	case form == "desktop":
		if (e.Type == "tap" || e.Type == "long_press") && e.At != nil {
			return e.T + clickMS
		}
	default:
		if g, ok := touchOf(e); ok {
			return e.T + g.life()
		}
	}
	return e.T
}

func (tl *timeline) touch(e Event, g touch) {
	total := g.life()
	warned := false
	var ln landing
	for k := range tl.views {
		u := tl.at(k) - e.T
		if u < 0 || u > total {
			continue
		}
		p := g.from
		if g.dur > 0 {
			f := clamp01(u / g.dur)
			p = Point{g.from.X + (g.to.X-g.from.X)*f, g.from.Y + (g.to.Y-g.from.Y)*f}
		}
		x, y, cam, ok := ln.at(p, tl.views[k], tl.lay)
		if !ok {
			if !ln.landed && !warned {
				tl.warnOutside(e, p)
				warned = true
			}
			continue
		}
		var sc, op float64
		switch {
		case u < tapIn:
			sc, op = tapFromS+(1-tapFromS)*u/tapIn, u/tapIn
		case u < tapIn+g.hold:
			sc, op = 1, 1
		default:
			f := (u - tapIn - g.hold) / tapOut
			sc, op = 1+(tapToS-1)*f, 1-f
		}
		tl.point(k, "tap", x, y, cam, sc, op)
	}
}

func (tl *timeline) click(e Event) {
	warned := false
	var ln landing
	for k := range tl.views {
		u := tl.at(k) - e.T
		if u < 0 || u > clickMS {
			continue
		}
		x, y, cam, ok := ln.at(*e.At, tl.views[k], tl.lay)
		if !ok {
			if !ln.landed && !warned {
				tl.warnOutside(e, *e.At)
				warned = true
			}
			continue
		}
		f := u / clickMS
		tl.point(k, "click", x, y, cam, clickFromS+(1-clickFromS)*(1-math.Pow(1-f, 3)), 1-f)
	}
}

// stop is where the cursor goes for one action: it arrives at p0 by arrive,
// and from start to leave moves on to p1.
type stop struct {
	arrive, start, leave float64
	p0, p1               Point
}

func (tl *timeline) cursor(l *Log) {
	var stops []stop
	for _, e := range l.Events {
		switch {
		case (e.Type == "tap" || e.Type == "long_press") && e.At != nil:
			stops = append(stops, stop{e.T - cursorLead, e.T, e.T + e.Duration, *e.At, *e.At})
		case (e.Type == "swipe" || e.Type == "drag") && e.From != nil && e.To != nil:
			stops = append(stops, stop{e.T - cursorLead, e.T, e.T + e.Duration, *e.From, *e.To})
		}
	}
	if len(stops) == 0 {
		return
	}
	for k := range tl.views {
		x, y, cam, ok := toOutput(cursorAt(stops, tl.at(k)), tl.views[k], tl.lay)
		if ok {
			tl.point(k, "cursor", x, y, cam, 1, 1)
		}
	}
}

// cursorAt is the cursor's point at t: eased between actions, holding on
// each, and starting at the first.
func cursorAt(stops []stop, t float64) Point {
	prev := stop{leave: math.Inf(-1), p1: stops[0].p0}
	for _, st := range stops {
		if t < st.arrive {
			if t <= prev.leave || math.IsInf(prev.leave, -1) {
				return prev.p1
			}
			return lerpPt(prev.p1, st.p0, video.EaseInOutCubic((t-prev.leave)/(st.arrive-prev.leave)))
		}
		if t < st.leave {
			if t < st.start || st.leave == st.start {
				return st.p0
			}
			return lerpPt(st.p0, st.p1, (t-st.start)/(st.leave-st.start))
		}
		prev = st
	}
	return prev.p1
}

func lerpPt(a, b Point, f float64) Point { return Point{a.X + (b.X-a.X)*f, a.Y + (b.Y-a.Y)*f} }

// normalise makes each point sprite kind's Op.Scale relative to the
// largest px per pt it is drawn at, and sizes its spec to the largest
// on-screen Op.Scale.
func (tl *timeline) normalise() {
	for _, kind := range pointKinds {
		camMax := 0.0
		for _, p := range tl.pts {
			if p.kind == kind {
				camMax = math.Max(camMax, p.cam)
			}
		}
		if camMax == 0 {
			continue
		}
		top := 0.0
		for _, p := range tl.pts {
			if p.kind == kind {
				sc := p.cam * p.s / camMax
				tl.ops[p.frame][p.index].Scale = sc
				top = math.Max(top, sc)
			}
		}
		box, _ := boxPt(kind)
		side := int(math.Ceil(box * camMax * top))
		tl.specs = append(tl.specs, SpriteSpec{Key: kind, Kind: kind, W: side, H: side, Scale: top})
	}
}
