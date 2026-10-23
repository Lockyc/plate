package clip

import (
	"image"
	"math"
	"testing"

	"github.com/lockyc/plate/internal/video"
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
	if _, s := tr.PinAt("kb", 500); math.Abs(s-(1-video.EaseInOutCubic(1.0/3))) > 1e-9 {
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
	if video.EaseInOutCubic(0) != 0 || video.EaseInOutCubic(0.5) != 0.5 || video.EaseInOutCubic(1) != 1 {
		t.Error("endpoints")
	}
	prev := 0.0
	for x := 0.0; x <= 1; x += 0.01 {
		if v := video.EaseInOutCubic(x); v < prev {
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

func TestRectAtHoldsAcrossOmittedSample(t *testing.T) {
	tr := NewTrack(rectsLog(
		rs(0, map[string]*Rect{"a": {1, 1, 1, 1}}),
		rs(500, map[string]*Rect{"a": {1, 1, 1, 1}, "b": {5, 5, 5, 5}}),
		rs(900, map[string]*Rect{"a": {1, 1, 1, 1}}),
		rs(1500, map[string]*Rect{"a": {1, 1, 1, 1}, "b": {9, 9, 9, 9}}),
	))
	for _, at := range []float64{900, 1200, 1499} {
		if r, ok := tr.RectAt("b", at); !ok || r != (Rect{5, 5, 5, 5}) {
			t.Errorf("RectAt(b, %g) = %v, %v", at, r, ok)
		}
	}
	if r, _ := tr.RectAt("b", 1500); r.X != 9 {
		t.Errorf("RectAt(b, 1500) = %v", r)
	}
}

func TestElementEasesWithKeyboard(t *testing.T) {
	// The touchbar rides the keyboard: in the gap where kb comes up, it
	// moves on the keyboard's eased curve, not linearly.
	tr := NewTrack(rectsLog(
		rs(0, map[string]*Rect{"tb": {0, 812, 402, 62}, "kb": nil}),
		rs(250, map[string]*Rect{"tb": {0, 498, 402, 62}, "kb": {0, 560, 402, 314}}),
		rs(500, map[string]*Rect{"tb": {0, 400, 402, 62}, "kb": {0, 560, 402, 314}}),
	))
	want := 812 + (498-812)*video.EaseInOutCubic(0.25)
	if r, _ := tr.RectAt("tb", 62.5); math.Abs(r.Y-want) > 1e-9 {
		t.Errorf("RectAt(tb, 62.5).Y = %g, want %g", r.Y, want)
	}
	if r, _ := tr.PinAt("tb", 62.5); math.Abs(r.Y-want) > 1e-9 {
		t.Errorf("PinAt(tb, 62.5).Y = %g, want %g", r.Y, want)
	}
	if r, _ := tr.RectAt("tb", 312.5); r.Y != 498+(400-498)*0.25 {
		t.Errorf("RectAt(tb, 312.5).Y = %g: a gap with no system transition is linear", r.Y)
	}
}
