package clip

import (
	"image"
	"math"
	"sort"
)

// idleLuma is the luma step a pixel must exceed to count as moved;
// idleShare is the share of moved pixels above which a frame counts as active.
const (
	idleLuma  = 8
	idleShare = 0.0005
)

// easeInOutCubic maps 0..1 to 0..1, slow at both ends.
func easeInOutCubic(x float64) float64 {
	if x < 0.5 {
		return 4 * x * x * x
	}
	return 1 - math.Pow(-2*x+2, 3)/2
}

type sample struct {
	t float64
	r *Rect // nil: the target was down
}

// Track answers where each named rect is at any master time.
type Track struct {
	series map[string][]sample
	first  float64 // time of the log's first rects sample
}

// NewTrack indexes every rects sample in the log by name. A name absent from
// a sample keeps its previous value, so only the samples naming it are kept.
func NewTrack(l *Log) *Track {
	tr := &Track{series: map[string][]sample{}, first: math.Inf(1)}
	for _, e := range l.Events {
		if e.Type != "rects" {
			continue
		}
		tr.first = math.Min(tr.first, e.T)
		for name, r := range e.Rects {
			tr.series[name] = append(tr.series[name], sample{e.T, r})
		}
	}
	return tr
}

func lerp(a, b Rect, f float64) Rect {
	return Rect{a.X + (b.X-a.X)*f, a.Y + (b.Y-a.Y)*f, a.W + (b.W-a.W)*f, a.H + (b.H-a.H)*f}
}

// RectAt is the rect of name at t, linear between samples and held outside
// them. ok is false before the name's first appearance.
func (tr *Track) RectAt(name string, t float64) (Rect, bool) {
	var up []sample
	for _, s := range tr.series[name] {
		if s.r != nil {
			up = append(up, s)
		}
	}
	if len(up) == 0 {
		return Rect{}, false
	}
	if t < up[0].t {
		// Held back to the first sample, but only for a name the log's first
		// sample already carried; a later arrival does not exist before it.
		return *up[0].r, up[0].t == tr.first
	}
	i := sort.Search(len(up), func(i int) bool { return up[i].t > t }) - 1
	if i == len(up)-1 {
		return *up[i].r, true
	}
	a, b := up[i], up[i+1]
	return lerp(*a.r, *b.r, (t-a.t)/(b.t-a.t)), true
}

// PinAt is the rect of a pinned target and how visible it is: 1 while up, 0
// while down, eased over the whole gap between a down sample and an up one.
func (tr *Track) PinAt(name string, t float64) (r Rect, shown float64) {
	ss := tr.series[name]
	if len(ss) == 0 {
		return Rect{}, 0
	}
	i := sort.Search(len(ss), func(i int) bool { return ss[i].t > t }) - 1
	if i < 0 {
		if ss[0].r != nil {
			return *ss[0].r, 1
		}
		return Rect{}, 0
	}
	a := ss[i]
	if i == len(ss)-1 {
		if a.r != nil {
			return *a.r, 1
		}
		return tr.lastUp(ss, i), 0
	}
	b := ss[i+1]
	x := (t - a.t) / (b.t - a.t)
	switch {
	case a.r != nil && b.r != nil:
		return lerp(*a.r, *b.r, x), 1
	case a.r != nil:
		return *a.r, 1 - easeInOutCubic(x)
	case b.r != nil:
		return *b.r, easeInOutCubic(x)
	}
	return tr.lastUp(ss, i), 0
}

// lastUp is the rect of the latest up sample at or before index i.
func (tr *Track) lastUp(ss []sample, i int) Rect {
	for ; i >= 0; i-- {
		if ss[i].r != nil {
			return *ss[i].r
		}
	}
	return Rect{}
}

// Activity is, per frame, the share of pixels whose luma moved by more than
// idleLuma since the previous frame; the first frame is 0.
func Activity(frames []*image.Gray) []float64 {
	a := make([]float64, len(frames))
	for i := 1; i < len(frames); i++ {
		p, c := frames[i-1].Pix, frames[i].Pix
		n := min(len(p), len(c))
		moved := 0
		for j := 0; j < n; j++ {
			d := int(c[j]) - int(p[j])
			if d > idleLuma || -d > idleLuma {
				moved++
			}
		}
		if n > 0 {
			a[i] = float64(moved) / float64(n)
		}
	}
	return a
}
