package clip

import "math"

// Settle is how long the picture holds after the last thing that moves, ms.
const Settle = 500.0

// Window is a span of the master timeline, ms.
type Window struct {
	Start float64 `json:"start_ms"`
	End   float64 `json:"end_ms"`
}

// Frames is the window's length in output frames.
func (w Window) Frames(fps int) int {
	return int(math.Round((w.End - w.Start) * float64(fps) / 1000))
}

// TrimWindow is the stretch of the master that holds the interaction: from
// head before the first action or movement to Settle after the last.
func TrimWindow(l *Log, activity []float64, fps int, head float64) Window {
	first, lastAct := math.Inf(1), math.Inf(-1)
	for _, e := range l.Events {
		if _, ok := actionOf[e.Type]; !ok {
			continue
		}
		end := e.T
		switch e.Type {
		case "swipe", "drag", "long_press":
			end = e.T + e.Duration
		case "type":
			end = e.End
		}
		first = math.Min(first, e.T)
		lastAct = math.Max(lastAct, end)
	}
	actFirst, actLast := math.Inf(1), math.Inf(-1)
	for i, a := range activity {
		if a > idleShare {
			ft := float64(i) * 1000 / float64(fps)
			actFirst = math.Min(actFirst, ft)
			actLast = math.Max(actLast, ft)
		}
	}
	return Window{
		Start: math.Max(0, math.Min(first, actFirst)-head),
		End:   math.Min(l.End, math.Max(lastAct, actLast)+Settle),
	}
}
