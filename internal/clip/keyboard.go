package clip

import (
	"fmt"
	"image"
	"math"
	"sort"
)

// TimeKeyboard times each keyboard rise and drop from the master itself. A
// driver logs rects only between its own steps, so the samples bracketing
// a keyboard coming up or going down can sit a second or more apart around
// an animation that takes about 300 ms, and easing every rect over the
// whole gap shows the strips and the region's bound where the master's
// content no longer is. For each pair of adjacent rects samples in which a
// system target goes from null to a rect or back, TimeKeyboard finds,
// between them, the first and last master frames with Activity above
// idleShare inside the keyboard's rect (the keyboard sliding in or out is
// a large change there; a caret blink is not), and returns a copy of l
// with the earlier sample repeated where that motion starts and the later
// one where it ends. The Track then holds every rect before the motion,
// eases the keyboard's share and every rect that moves with it (the bars
// riding it, the region) over exactly the motion, and holds after it.
// frames are Activity's (ExtractActivity), frame i at i·1000/fps ms. A gap
// with no motion found keeps the whole-gap ease, and is warned about.
func TimeKeyboard(s *Script, l *Log, frames []*image.Gray, fps int) (*Log, []string) {
	fr := 1000 / float64(fps)
	var rects []int // indices into l.Events of the rects samples, in order
	for i, e := range l.Events {
		if e.Type == "rects" {
			rects = append(rects, i)
		}
	}
	out := &Log{Meta: l.Meta, Events: append([]Event{}, l.Events...), End: l.End}
	var warns []string
	for j := 1; j < len(rects); j++ {
		a, b := l.Events[rects[j-1]], l.Events[rects[j]]
		kb, ok := keyboardIn(s, a, b)
		if !ok {
			continue
		}
		i0, i1 := moved(frames, kb, l.Meta.Screen, a.T, b.T, fr)
		if i0 < 0 {
			warns = append(warns, fmt.Sprintf("keyboard %.0f-%.0f ms: no motion found in the master inside the keyboard's rect; easing over the whole gap", a.T, b.T))
			continue
		}
		m0, m1 := math.Max(a.T, float64(i0-1)*fr), math.Min(b.T, float64(i1)*fr)
		if m0 > a.T {
			out.Events = append(out.Events, Event{Type: "rects", T: m0, Rects: a.Rects})
		}
		if m1 < b.T {
			out.Events = append(out.Events, Event{Type: "rects", T: m1, Rects: b.Rects})
		}
		out.Motions = append(out.Motions, progress(frames, kb, l.Meta.Screen, rising(s, a, b), i0, i1, m0, m1, fr))
	}
	sort.SliceStable(out.Events, func(i, j int) bool { return out.Events[i].T < out.Events[j].T })
	return out, warns
}

// rising reports whether the system target changing between a and b comes
// up (null to a rect) rather than going down.
func rising(s *Script, a, b Event) bool {
	for name, ra := range a.Rects {
		if rb, has := b.Rects[name]; has && isSystem(s, name) && ra == nil && rb != nil {
			return true
		}
	}
	return false
}

// progress is the transition's Motion over [m0, m1], frames i0 to i1 the
// ones that moved: at each, how far the keyboard is along its slide, from
// where its content sits. The keyboard slides in from the screen's foot,
// travelling from the foot to its rect's top. On each moving frame, the
// keyboard's rect at rest (the frame after the motion for a rise, the one
// before it for a drop) is matched against the frame shifted down by every
// whole row of that travel; the best match's shift d gives the progress,
// 1 - d/travel coming up and d/travel going down. Progress is held
// monotonic, and is 0 at m0 and 1 at m1.
func progress(frames []*image.Gray, kb Rect, screen [2]float64, up bool, i0, i1 int, m0, m1, fr float64) Motion {
	m := Motion{T: []float64{m0}, X: []float64{0}}
	ref := frames[min(i1+1, len(frames)-1)]
	if !up {
		ref = frames[max(i0-2, 0)]
	}
	b := ref.Bounds()
	ky := float64(b.Dy()) / screen[1]
	top := int(math.Round(kb.Y * ky))
	bottom := min(int(math.Round((kb.Y+kb.H)*ky)), b.Dy())
	travel := b.Dy() - top
	last := 0.0
	for i := i0; i < i1; i++ {
		f := frames[i]
		best, bestCost := 0, math.Inf(1)
		for d := 0; d <= travel; d++ {
			var cost float64
			n := 0
			for y := top; y < bottom && y+d < b.Dy(); y++ {
				for x := b.Min.X; x < b.Max.X; x += 2 {
					c := int(f.GrayAt(x, y+d).Y) - int(ref.GrayAt(x, y).Y)
					cost += math.Abs(float64(c))
					n++
				}
			}
			if n == 0 {
				continue
			}
			if cost /= float64(n); cost < bestCost {
				best, bestCost = d, cost
			}
		}
		x := float64(best) / float64(travel)
		if up {
			x = 1 - x
		}
		last = math.Max(last, math.Min(1, x))
		m.T = append(m.T, float64(i)*fr)
		m.X = append(m.X, last)
	}
	m.T = append(m.T, m1)
	m.X = append(m.X, 1)
	return m
}

// keyboardIn is the rect of a system target that comes up or goes down
// between adjacent samples a and b: the one carrying it.
func keyboardIn(s *Script, a, b Event) (Rect, bool) {
	for name, ra := range a.Rects {
		rb, has := b.Rects[name]
		if !has || !isSystem(s, name) || (ra == nil) == (rb == nil) {
			continue
		}
		if ra != nil {
			return *ra, true
		}
		return *rb, true
	}
	return Rect{}, false
}

// moved is the first and last of frames, strictly after t0 and up to t1,
// whose change from the frame before moves more than idleShare of the
// pixels inside r (pt, on a screen of size screen); -1, -1 for none.
func moved(frames []*image.Gray, r Rect, screen [2]float64, t0, t1, fr float64) (int, int) {
	i0, i1 := -1, -1
	for i := int(math.Floor(t0/fr)) + 1; i < len(frames) && float64(i)*fr <= t1; i++ {
		if i < 1 {
			continue
		}
		p, c := frames[i-1], frames[i]
		b := c.Bounds()
		kx, ky := float64(b.Dx())/screen[0], float64(b.Dy())/screen[1]
		box := image.Rect(int(r.X*kx), int(r.Y*ky), int(math.Ceil((r.X+r.W)*kx)), int(math.Ceil((r.Y+r.H)*ky))).Intersect(b)
		if box.Empty() {
			continue
		}
		n := 0
		for y := box.Min.Y; y < box.Max.Y; y++ {
			for x := box.Min.X; x < box.Max.X; x++ {
				d := int(c.GrayAt(x, y).Y) - int(p.GrayAt(x, y).Y)
				if d > idleLuma || -d > idleLuma {
					n++
				}
			}
		}
		if float64(n)/float64(box.Dx()*box.Dy()) > idleShare {
			if i0 < 0 {
				i0 = i
			}
			i1 = i
		}
	}
	return i0, i1
}
