package clip

import (
	"image"
	"math"
	"testing"
)

func rectsLog(evs ...Event) *Log { return &Log{Events: evs, End: 6000} }

func rs(t float64, m map[string]*Rect) Event { return Event{Type: "rects", T: t, Rects: m} }

func TestRectAtHoldsAndClamps(t *testing.T) {
	r := &Rect{10, 20, 30, 40}
	tr := NewTrack(rectsLog(rs(0, map[string]*Rect{"a": r}), rs(1000, map[string]*Rect{"a": r})))
	for _, at := range []float64{-50, 0, 400, 1000, 5000} {
		got, ok := tr.RectAt("a", at)
		if !ok || got != *r {
			t.Errorf("RectAt(%g) = %v, %v", at, got, ok)
		}
	}
}

func TestRectAtInterpolatesLinearly(t *testing.T) {
	tr := NewTrack(rectsLog(
		rs(0, map[string]*Rect{"bar": {0, 812, 400, 40}}),
		Event{Type: "tap", T: 100},
		rs(250, map[string]*Rect{"bar": {0, 500, 400, 40}}),
	))
	for at, want := range map[float64]float64{125: 656, 250: 500, 400: 500} {
		if got, _ := tr.RectAt("bar", at); got.Y != want {
			t.Errorf("RectAt(%g).Y = %g, want %g", at, got.Y, want)
		}
	}
}

func TestRectAtMissingName(t *testing.T) {
	tr := NewTrack(rectsLog(
		rs(0, map[string]*Rect{"a": {1, 1, 1, 1}}),
		rs(500, map[string]*Rect{"a": {1, 1, 1, 1}, "b": {5, 5, 5, 5}}),
		rs(900, map[string]*Rect{"a": {1, 1, 1, 1}}),
	))
	if _, ok := tr.RectAt("b", 400); ok {
		t.Error("b ok before first appearance")
	}
	for _, at := range []float64{500, 700, 2000} {
		if r, ok := tr.RectAt("b", at); !ok || r.X != 5 {
			t.Errorf("RectAt(b, %g) = %v, %v", at, r, ok)
		}
	}
}

func TestPinAtKeyboardEases(t *testing.T) {
	kb := &Rect{0, 560, 402, 314}
	tr := NewTrack(rectsLog(
		rs(0, map[string]*Rect{"kb": nil}),
		rs(250, map[string]*Rect{"kb": kb}),
		rs(1000, map[string]*Rect{"kb": nil}),
	))
	if _, s := tr.PinAt("kb", 0); s != 0 {
		t.Errorf("shown(0) = %g", s)
	}
	if r, s := tr.PinAt("kb", 125); s != 0.5 || r != *kb {
		t.Errorf("PinAt(125) = %v, %g", r, s)
	}
	if r, s := tr.PinAt("kb", 250); s != 1 || r != *kb {
		t.Errorf("PinAt(250) = %v, %g", r, s)
	}
	if _, s := tr.PinAt("kb", 500); math.Abs(s-(1-easeInOutCubic(1.0/3))) > 1e-9 {
		t.Errorf("shown(500) = %g", s)
	}
	if _, s := tr.PinAt("kb", 1000); s != 0 {
		t.Errorf("shown(1000) = %g", s)
	}
	prev := 0.0
	for at := 0.0; at <= 250; at += 10 {
		_, s := tr.PinAt("kb", at)
		if s < prev {
			t.Fatalf("not monotonic rising at %g", at)
		}
		prev = s
	}
	for at := 250.0; at <= 1000; at += 10 {
		_, s := tr.PinAt("kb", at)
		if s > prev {
			t.Fatalf("not monotonic falling at %g", at)
		}
		prev = s
	}
}

func TestPinAtNeverUp(t *testing.T) {
	tr := NewTrack(rectsLog(rs(0, map[string]*Rect{"kb": nil})))
	if r, s := tr.PinAt("kb", 100); r != (Rect{}) || s != 0 {
		t.Errorf("PinAt = %v, %g", r, s)
	}
}

func TestEaseInOutCubic(t *testing.T) {
	if easeInOutCubic(0) != 0 || easeInOutCubic(0.5) != 0.5 || easeInOutCubic(1) != 1 {
		t.Error("endpoints")
	}
	prev := 0.0
	for x := 0.0; x <= 1; x += 0.01 {
		if v := easeInOutCubic(x); v < prev {
			t.Fatalf("not monotonic at %g", x)
		} else {
			prev = v
		}
	}
}

func TestActivity(t *testing.T) {
	mk := func() *image.Gray {
		g := image.NewGray(image.Rect(0, 0, 10, 10))
		for i := range g.Pix {
			g.Pix[i] = 100
		}
		return g
	}
	a, b, c := mk(), mk(), mk()
	for i := 0; i < 5; i++ {
		b.Pix[i] = 108 // 8 is unchanged
		c.Pix[i] = 128
	}
	got := Activity([]*image.Gray{a, b, c})
	want := []float64{0, 0, 0.05}
	for i := range want {
		if math.Abs(got[i]-want[i]) > 1e-12 {
			t.Fatalf("Activity = %v, want %v", got, want)
		}
	}
}
