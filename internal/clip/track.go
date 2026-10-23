package clip

import (
	"image"
	"math"
	"sort"

	"github.com/lockyc/plate/internal/video"
)

// idleLuma is the luma step a pixel must exceed to count as moved;
// idleShare is the share of moved pixels above which a frame counts as
// active. At Activity's 200 px across a phone screen a blinking iOS caret
// moves 0.00053 a blink and a 22 pt checkbox ticking 0.0014; idleShare sits
// between them, about twice the caret, so a caret alone is idle
// (TestIdleCutsCountACaretBlinkIdle) and the smallest control change is not.
const (
	idleLuma  = 8
	idleShare = 0.001
)

type sample struct {
	t   float64
	idx int   // ordinal of the rects event; adjacent idx means no event between
	r   *Rect // nil: the target was down
}

// Track answers where each named rect is at any master time.
type Track struct {
	series map[string][]sample
	first  float64 // time of the log's first rects sample
	// eased marks the gaps (by the ordinal of the sample opening them) in
	// which a system target comes up or goes down. Every rect moves on the
	// system target's eased curve there, as the layout riding the keyboard
	// does, so a touchbar and the keyboard under it stay flush.
	eased map[int]bool
	// motions are the eased gaps the master was measured over (Log.Motions),
	// by the ordinal of the sample opening them: they move on the measured
	// progress instead of the cubic.
	motions map[int]Motion
}

// NewTrack indexes every rects sample in the log by name. A name absent from
// a sample keeps its previous value, so only the samples naming it are kept.
func NewTrack(l *Log) *Track {
	tr := &Track{series: map[string][]sample{}, first: math.Inf(1), eased: map[int]bool{}, motions: map[int]Motion{}}
	n := -1
	for _, e := range l.Events {
		if e.Type != "rects" {
			continue
		}
		n++
		for _, m := range l.Motions {
			if m.T[0] == e.T {
				tr.motions[n] = m
			}
		}
		tr.first = math.Min(tr.first, e.T)
		for name, r := range e.Rects {
			tr.series[name] = append(tr.series[name], sample{e.T, n, r})
		}
	}
	for _, ss := range tr.series {
		for i := 1; i < len(ss); i++ {
			if a, b := ss[i-1], ss[i]; b.idx == a.idx+1 && (a.r == nil) != (b.r == nil) {
				tr.eased[a.idx] = true
			}
		}
	}
	return tr
}

func lerp(a, b Rect, f float64) Rect {
	return Rect{a.X + (b.X-a.X)*f, a.Y + (b.Y-a.Y)*f, a.W + (b.W-a.W)*f, a.H + (b.H-a.H)*f}
}

// between is the rect at t between two adjacent up samples: linear, or
// eased in a gap where a system target comes or goes.
func (tr *Track) between(a, b sample, t float64) Rect {
	x := (t - a.t) / (b.t - a.t)
	if tr.eased[a.idx] {
		x = tr.ease(a, b, t)
	}
	return lerp(*a.r, *b.r, x)
}

// ease is the progress at t through the eased gap a-b: as measured from the
// master where TimeKeyboard measured it, else the cubic over the gap.
func (tr *Track) ease(a, b sample, t float64) float64 {
	if m, ok := tr.motions[a.idx]; ok {
		return m.at(t)
	}
	return video.EaseInOutCubic((t - a.t) / (b.t - a.t))
}

// RectAt is the rect of name at t, interpolated between two consecutive
// samples that both carry it and held otherwise, so an omitted (off-screen) name
// keeps its last value. ok is false before the name's first appearance.
func (tr *Track) RectAt(name string, t float64) (Rect, bool) {
	ss := tr.series[name]
	i := sort.Search(len(ss), func(i int) bool { return ss[i].t > t }) - 1
	if i < 0 {
		// Held back to the first sample, but only for a name the log's first
		// sample already carried; a later arrival does not exist before it.
		for _, s := range ss {
			if s.r != nil {
				return *s.r, s.t == tr.first
			}
		}
		return Rect{}, false
	}
	if ss[i].r == nil {
		r := tr.lastUp(ss, i)
		if r == (Rect{}) {
			for _, s := range ss[i:] {
				if s.r != nil {
					return *s.r, false
				}
			}
			return Rect{}, false
		}
		return r, true
	}
	if i+1 < len(ss) && ss[i+1].r != nil && ss[i+1].idx == ss[i].idx+1 {
		return tr.between(ss[i], ss[i+1], t), true
	}
	return *ss[i].r, true
}

// PinAt is the rect of a pinned target and how visible it is: 1 while up, 0
// while down, eased (ease) over the gap between a down sample and an up one.
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
	if b.idx != a.idx+1 { // a sample omitted the name: hold
		if a.r != nil {
			return *a.r, 1
		}
		return tr.lastUp(ss, i), 0
	}
	switch {
	case a.r != nil && b.r != nil:
		return tr.between(a, b, t), 1
	case a.r != nil:
		return *a.r, 1 - tr.ease(a, b, t)
	case b.r != nil:
		return *b.r, tr.ease(a, b, t)
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
