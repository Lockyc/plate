package clip

import (
	"fmt"
	"math"
	"testing"

	"pgregory.net/rapid"
)

// geometry is a random capture: a region over a keyboard that rises and
// drops, with up to two bars riding it from the screen's foot to its top,
// flush (give or take a gap) only at the rects samples, and taps for a
// follow camera to chase.
type geometry struct {
	s    *Script
	l    *Log
	w    Window
	desc string
}

func drawGeometry(t *rapid.T, keyboard bool) geometry {
	const sw, sh = 402.0, 874.0
	width := rapid.SampledFrom([]int{600, 1080}).Draw(t, "width")
	mode := rapid.SampledFrom([]string{"follow", "fixed"}).Draw(t, "mode")
	minW := float64(width) / 3
	rw := rapid.Float64Range(minW, sw).Draw(t, "region w")
	region := Rect{rapid.Float64Range(0, sw-rw).Draw(t, "region x"), rapid.Float64Range(0, 300).Draw(t, "region y"), rw, 0}
	region.H = rapid.Float64Range(200, 760-region.Y).Draw(t, "region h")

	home := rapid.Float64Range(0, 60).Draw(t, "home indicator")
	kh := rapid.Float64Range(150, 340).Draw(t, "keyboard h")
	kb := Rect{0, sh - home - kh, sw, kh}
	nBars := rapid.IntRange(0, 2).Draw(t, "bars")
	if !keyboard {
		nBars = rapid.IntRange(1, 2).Draw(t, "bars")
	}
	var rest, up []Rect
	restY, upY := sh-rapid.Float64Range(0, 40).Draw(t, "bar foot"), kb.Y+rapid.Float64Range(-12, 12).Draw(t, "bar gap")
	for i := range nBars {
		h := rapid.Float64Range(28, 62).Draw(t, fmt.Sprintf("bar %d h", i))
		restY -= h
		upY -= h
		rest = append(rest, Rect{0, restY, sw, h})
		up = append(up, Rect{0, upY, sw, h})
	}
	// The bars sit below the region: rest is listed foot first, up flush
	// on the keyboard; the stack is filter (top) then touchbar.
	names := []string{"touchbar", "filter"}
	var pins []Target
	for i := nBars - 1; i >= 0; i-- {
		pins = append(pins, Target{Name: names[i], Label: names[i]})
	}
	if keyboard {
		pins = append(pins, kbT)
	}
	s := camScript(width, mode, &regionT, pins...)

	sample := func(t float64, isUp bool) Event {
		m := map[string]*Rect{"region": &region}
		for i := range nBars {
			r := rest[i]
			if isUp {
				r = up[i]
			}
			m[names[i]] = &r
		}
		if keyboard {
			m["kb"] = nil
			if isUp {
				k := kb
				m["kb"] = &k
			}
		}
		return rs(t, m)
	}
	rise := rapid.Float64Range(200, 1500).Draw(t, "rise at")
	riseFor := rapid.Float64Range(80, 600).Draw(t, "rise for")
	drop := rise + riseFor + rapid.Float64Range(100, 1500).Draw(t, "held for")
	dropFor := rapid.Float64Range(80, 600).Draw(t, "drop for")
	evs := []Event{sample(0, false)}
	if keyboard {
		evs = append(evs, sample(rise, false), sample(rise+riseFor, true), sample(drop, true), sample(drop+dropFor, false))
	}
	end := drop + dropFor + 400
	for i := range rapid.IntRange(0, 3).Draw(t, "taps") {
		at := Point{rapid.Float64Range(20, sw-20).Draw(t, fmt.Sprintf("tap %d x", i)), rapid.Float64Range(20, sh-20).Draw(t, fmt.Sprintf("tap %d y", i))}
		tw, th := rapid.Float64Range(10, 200).Draw(t, fmt.Sprintf("tap %d w", i)), rapid.Float64Range(10, 120).Draw(t, fmt.Sprintf("tap %d h", i))
		target := Rect{math.Max(0, at.X-tw/2), math.Max(0, at.Y-th/2), tw, th}
		target.W = math.Min(target.W, sw-target.X)
		target.H = math.Min(target.H, sh-target.Y)
		evs = append(evs, tap(rapid.Float64Range(300, end-300).Draw(t, fmt.Sprintf("tap %d at", i)), at, &target))
	}
	l := camLog(evs...)
	l.End = end
	return geometry{s, l, Window{0, end}, fmt.Sprintf("%s %d px, region %+v, keyboard %+v, bars rest %v up %v", mode, width, region, kb, rest, up)}
}

// pathOf is Path over g, or a skip for a geometry the layout or camera
// refuses (a region squeezed under a quarter of the frame, say).
func pathOf(t *rapid.T, g geometry) (Layout, []View) {
	tr := NewTrack(g.l)
	lay, err := NewLayout(g.s, g.l, tr)
	if err != nil {
		t.Skip(err)
	}
	vs, err := Path(g.s, g.l, tr, lay, g.w)
	if err != nil {
		t.Skip(err)
	}
	return lay, vs
}

// TestPathNeverSharesRowsForAnyGeometry is TestPathNeverSharesRows over
// random keyboard and bar geometry and timing, follow and fixed: at every
// frame no two strips, nor the region, show the same master rows, and each
// strip is shown at its width's scale.
func TestPathNeverSharesRowsForAnyGeometry(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		g := drawGeometry(rt, true)
		lay, vs := pathOf(rt, g)
		for k, v := range vs {
			if err := sharedRows(v, float64(lay.W)); err != nil {
				rt.Fatalf("%s\nframe %d (t=%g ms): %v", g.desc, k, float64(k)*1000/60, err)
			}
		}
	})
}

// sharedRows is noSharedRows as an error.
func sharedRows(v View, w float64) error {
	srcs := []Rect{v.Region}
	names := []string{"region"}
	for _, p := range v.Pins {
		if !near(p.H, w*p.Src.H/p.Src.W, 1e-6) {
			return fmt.Errorf("%s shows %g pt in %g px: not at its width's scale", p.Name, p.Src.H, p.H)
		}
		srcs = append(srcs, p.Src)
		names = append(names, p.Name)
	}
	for i := range srcs {
		for j := i + 1; j < len(srcs); j++ {
			if overlaps(srcs[i], srcs[j]) {
				return fmt.Errorf("%s %+v and %s %+v show the same master rows", names[i], srcs[i], names[j], srcs[j])
			}
		}
	}
	return nil
}

// TestConstantStripsCentreTheRegionForAnyGeometry: with strips that never
// change, every frame whose region the bounds leave alone is centred on the
// camera's state, however Layout rounded the strips' heights.
func TestConstantStripsCentreTheRegionForAnyGeometry(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		g := drawGeometry(rt, false)
		tr := NewTrack(g.l)
		lay, err := NewLayout(g.s, g.l, tr)
		if err != nil {
			rt.Skip(err)
		}
		c, keys, err := cameraKeys(g.s, g.l, tr, lay, g.w)
		if err != nil {
			rt.Fatal(err)
		}
		for k := range g.w.Frames(*g.s.FPS) {
			tm := float64(k) * 1000 / 60
			st := stateAt(keys, tm)
			v := c.view(st, tm)
			_, b, _, _ := c.strips(tm)
			r := v.Region
			const m = 1e-6
			if r.X <= b.X+m || r.Y <= b.Y+m || r.X+r.W >= b.X+b.W-m || r.Y+r.H >= b.Y+b.H-m {
				continue // the bounds moved it
			}
			if !near(r.X+r.W/2, st.cx, 1e-9) || !near(r.Y+r.H/2, st.cy, 1e-9) {
				rt.Fatalf("%s\nframe %d: region %+v centred at (%g, %g), the camera at (%g, %g)", g.desc, k, r, r.X+r.W/2, r.Y+r.H/2, st.cx, st.cy)
			}
		}
	})
}
