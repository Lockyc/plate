package clip

import "testing"

func trimFixture(tapAt float64) (*Log, []float64) {
	l := &Log{End: 5900, Events: []Event{{Type: "tap", T: tapAt}}}
	act := make([]float64, 360)
	for i := 120; i <= 180; i++ {
		act[i] = 0.01
	}
	return l, act
}

func TestTrimWindow(t *testing.T) {
	l, act := trimFixture(2000)
	w := TrimWindow(l, act, 60, Settle)
	if w != (Window{1500, 3500}) || w.Frames(60) != 120 {
		t.Errorf("window = %+v, frames %d", w, w.Frames(60))
	}
	if w := TrimWindow(l, act, 60, 1450); w.Start != 550 {
		t.Errorf("Start = %g", w.Start)
	}
	l, act = trimFixture(300)
	if w := TrimWindow(l, act, 60, Settle); w.Start != 0 {
		t.Errorf("Start = %g", w.Start)
	}
}

func TestTrimWindowActionEnds(t *testing.T) {
	l := &Log{End: 9000, Events: []Event{
		{Type: "swipe", T: 1000, Duration: 400},
		{Type: "type", T: 2000, End: 3000},
	}}
	if w := TrimWindow(l, make([]float64, 60), 60, Settle); w.Start != 500 || w.End != 3500 {
		t.Errorf("window = %+v", w)
	}
}
