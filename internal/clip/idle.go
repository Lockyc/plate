package clip

import (
	"fmt"
	"math"
	"slices"
	"sort"
)

// Cut is a span of master time a render drops, ms: [Start, End), on the
// output frame grid.
type Cut struct{ Start, End float64 }

// span is a busy stretch of master time, ms.
type span struct{ a, b float64 }

// IdleCuts finds every interior stretch of w longer than the script's
// max_idle_ms in which nothing happens, and cuts it to that length, to the
// frame, from its middle. Something happens wherever activity (Activity's,
// per master frame at fps) is above idleShare, which a blinking caret is
// not; wherever an event acts: an action from its t to the later of its end
// (a gesture's duration, a type's end) plus its step's hold_ms and its own
// overlay's end (overlayEnd); a caption's fade; and the whole stretch
// between the actions either side of a pause step, whose hold is explicit;
// and across each of holds (FollowMoves). Interior means between the first
// busy moment inside w and the last: the trim's head and settle are never
// cut.
func IdleCuts(s *Script, l *Log, activity []float64, w Window, fps int, holds []Window) []Cut {
	fr := 1000 / float64(fps)
	var busy []span
	for _, h := range holds {
		busy = append(busy, span{h.Start, h.End})
	}
	for i := 1; i < len(activity); i++ {
		if activity[i] > idleShare {
			busy = append(busy, span{float64(i-1) * fr, float64(i) * fr})
		}
	}
	type act struct {
		step   int
		t, end float64
	}
	var acts []act
	for _, e := range l.Events {
		switch {
		case e.Type == "caption":
			busy = append(busy, span{e.T, e.T + captionFade})
		case e.Step != nil:
			end := e.T
			switch e.Type {
			case "long_press", "swipe", "drag":
				end += e.Duration
			case "type":
				end = e.End
			}
			hold := 0.0
			if st := s.Steps[*e.Step]; st.HoldMS != nil {
				hold = float64(*st.HoldMS)
			}
			busy = append(busy, span{e.T, math.Max(end+hold, overlayEnd(e, s.Form))})
			acts = append(acts, act{*e.Step, e.T, end})
		}
	}
	for p, st := range s.Steps {
		if st.Action != "pause" {
			continue
		}
		from, to := w.Start, w.End
		for _, a := range acts {
			if a.step < p {
				from = math.Max(from, a.end)
			} else if a.step > p {
				to = math.Min(to, a.t)
			}
		}
		busy = append(busy, span{from, to})
	}
	// A span over by w.Start (a caption at a leading wait step) is the
	// head's, never the start of the interior.
	busy = slices.DeleteFunc(busy, func(b span) bool { return b.b <= w.Start })
	if len(busy) == 0 {
		return nil
	}
	sort.Slice(busy, func(i, j int) bool { return busy[i].a < busy[j].a })
	limit := float64(*s.Output.MaxIdleMS)
	var cuts []Cut
	reach := busy[0].b
	for _, b := range busy[1:] {
		if gap := b.a - math.Max(reach, w.Start); gap > limit && b.a <= w.End {
			g0 := math.Max(reach, w.Start)
			if n := math.Floor((gap-limit)/fr + 1e-9); n > 0 {
				k := math.Round((g0 + (gap-n*fr)/2 - w.Start) / fr)
				c0 := w.Start + k*fr
				cuts = append(cuts, Cut{c0, c0 + n*fr})
			}
		}
		reach = math.Max(reach, b.b)
	}
	return cuts
}

// Removed is the master time cuts drop, ms.
func Removed(cuts []Cut) float64 {
	var r float64
	for _, c := range cuts {
		r += c.End - c.Start
	}
	return r
}

// shift is master time t on the timeline with cuts dropped: earlier by
// every cut before it, and a time inside a cut lands on its start.
func shift(t float64, cuts []Cut) float64 {
	d := 0.0
	for _, c := range cuts {
		switch {
		case t >= c.End:
			d += c.End - c.Start
		case t > c.Start:
			d += t - c.Start
		}
	}
	return t - d
}

// CompressLog is a copy of l on the timeline with cuts dropped: every
// event's t and end, and the log's end, shifted, so the overlays and the
// camera, which read the log, stay locked to the frames ApplyCuts keeps.
func CompressLog(l *Log, cuts []Cut) *Log {
	out := &Log{Meta: l.Meta, Events: slices.Clone(l.Events), End: shift(l.End, cuts)}
	for i := range out.Events {
		e := &out.Events[i]
		if e.Type == "type" {
			e.End = shift(e.End, cuts)
		}
		e.T = shift(e.T, cuts)
	}
	return out
}

// Compress is w with cuts dropped from its end.
func Compress(w Window, cuts []Cut) Window {
	return Window{w.Start, w.End - Removed(cuts)}
}

// ApplyCuts drops each cut's frames from frames, the window w decoded at
// fps (frame k at w.Start + k·1000/fps), and returns the frames kept and
// the cuts made. A cut is made only if the frames either side of it match
// within MaxSeam, the jump a loop may show: the dropped span is idle by
// Activity, but a slow drift below idleShare a frame can add up across it.
// Each cut not made is reported to warn, naming its span.
func ApplyCuts(frames []string, w Window, fps int, cuts []Cut, warn func(string)) ([]string, []Cut, error) {
	fr := 1000 / float64(fps)
	drop := make([]bool, len(frames))
	var made []Cut
	for _, c := range cuts {
		a := int(math.Round((c.Start - w.Start) / fr))
		b := int(math.Round((c.End - w.Start) / fr))
		if a < 1 || b >= len(frames) {
			continue
		}
		before, err := loadRGBA64(frames[a-1])
		if err != nil {
			return nil, nil, err
		}
		after, err := loadRGBA64(frames[b])
		if err != nil {
			return nil, nil, err
		}
		seam, err := Seam(before, after)
		if err != nil {
			return nil, nil, err
		}
		if seam > MaxSeam {
			warn(fmt.Sprintf("idle %.1f-%.1f ms kept: the frames either side differ by %.3g%%, above %.3g%%", c.Start, c.End, seam, MaxSeam))
			continue
		}
		for k := a; k < b; k++ {
			drop[k] = true
		}
		made = append(made, c)
	}
	var kept []string
	for k, f := range frames {
		if !drop[k] {
			kept = append(kept, f)
		}
	}
	return kept, made, nil
}
