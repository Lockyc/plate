package clip

import (
	"image"
	"math"
	"strings"
	"testing"
)

// kbMaster is a master's Activity frames, 200 px across a 402 x 874 pt
// screen at 60 fps for 4 s: a bar rests at 804 pt and, from 2000 to
// 2300 ms on easeInOutCubic, rides a keyboard up from the screen's foot
// until the bar sits at 529 and the keyboard's top at 583; with moving
// false nothing moves at all, and with bar false there is no bar.
func kbMaster(moving bool, bar ...bool) []*image.Gray {
	const w, h = 200, 434
	frames := make([]*image.Gray, 240)
	for i := range frames {
		e := 0.0
		if moving {
			e = easeInOutCubic(clamp01((float64(i)*1000/60 - 2000) / 300))
		}
		g := image.NewGray(image.Rect(0, 0, w, h))
		row := func(pt float64) int { return int(math.Round(pt * h / 874)) }
		for y := range h {
			v := uint8(20)
			switch {
			case y >= row(874-291*e):
				v = 200 // the keyboard
			case (len(bar) == 0 || bar[0]) && y >= row(804-275*e) && y < row(840-275*e):
				v = 120 // the bar
			}
			for x := range w {
				g.Pix[y*w+x] = v
			}
		}
		frames[i] = g
	}
	return frames
}

// kbLog brackets the rise with samples 1.3 s apart, at 1500 and 2800 ms,
// the way a driver busy with its own step logs it.
func kbLog() (*Script, *Log) {
	s := camScript(1080, "fixed", &regionT, touchbarT, kbT)
	r0, r1 := Rect{0, 151, 402, 600}, Rect{0, 151, 402, 330}
	b0, b1 := Rect{0, 804, 402, 36}, Rect{0, 529, 402, 36}
	kb := Rect{0, 583, 402, 233}
	return s, camLog(
		rs(0, map[string]*Rect{"region": &r0, "touchbar": &b0, "kb": nil}),
		rs(1500, map[string]*Rect{"region": &r0, "touchbar": &b0, "kb": nil}),
		rs(2800, map[string]*Rect{"region": &r1, "touchbar": &b1, "kb": &kb}),
	)
}

func TestTimeKeyboardEasesOverTheMeasuredMotion(t *testing.T) {
	s, l := kbLog()
	got, warns := TimeKeyboard(s, l, kbMaster(true), 60)
	if len(warns) != 0 {
		t.Fatalf("warned %q", warns)
	}
	_, vs := path(t, s, got, Window{1000, 3400})
	at := func(k int) float64 { return 1000 + float64(k)*1000/60 }
	pin := func(v View, name string) (PinView, bool) {
		for _, p := range v.Pins {
			if p.Name == name {
				return p, true
			}
		}
		return PinView{}, false
	}
	before, after := vs[0], vs[len(vs)-1]
	for k, v := range vs {
		ts := at(k)
		switch {
		case ts <= 2000:
			if !viewsEqual(v, before) {
				t.Fatalf("%.0f ms, before the keyboard moves: view changed from %+v to %+v", ts, before, v)
			}
		case ts >= 2300:
			if !viewsEqual(v, after) {
				t.Fatalf("%.0f ms, after the keyboard settles: view %+v, want %+v", ts, v, after)
			}
		default:
			// The bar's strip shows the master's bar where it is on this
			// frame, to within a row of Activity's frames (2 pt). Easing
			// over the motion's span instead, one frame off at either end
			// of a 300 ms move leaves the strip up to 23 pt (half a row of
			// text) from the master's bar mid-move.
			e := easeInOutCubic((ts - 2000) / 300)
			bar, ok := pin(v, "touchbar")
			if !ok || math.Abs(bar.Src.Y-(804-275*e)) > 874.0/434*1.5 {
				t.Fatalf("%.0f ms: touchbar Src %+v, the master's bar is at %.2f", ts, bar.Src, 804-275*e)
			}
		}
	}
	if len(l.Events) != 3 || len(l.Motions) != 0 {
		t.Error("TimeKeyboard changed its input log")
	}
}

func TestTimeKeyboardTimesADrop(t *testing.T) {
	// kbMaster played backwards: the keyboard drops from 1683 to 1983 ms,
	// bracketed by samples at 1100 and 2400.
	up := kbMaster(true)
	frames := make([]*image.Gray, len(up))
	for i := range up {
		frames[i] = up[len(up)-1-i]
	}
	s := camScript(1080, "fixed", &regionT, touchbarT, kbT)
	r0, r1 := Rect{0, 151, 402, 600}, Rect{0, 151, 402, 330}
	b0, b1 := Rect{0, 804, 402, 36}, Rect{0, 529, 402, 36}
	kb := Rect{0, 583, 402, 233}
	l := camLog(
		rs(0, map[string]*Rect{"region": &r1, "touchbar": &b1, "kb": &kb}),
		rs(1100, map[string]*Rect{"region": &r1, "touchbar": &b1, "kb": &kb}),
		rs(2400, map[string]*Rect{"region": &r0, "touchbar": &b0, "kb": nil}),
	)
	got, warns := TimeKeyboard(s, l, frames, 60)
	if len(warns) != 0 {
		t.Fatalf("warned %q", warns)
	}
	_, vs := path(t, s, got, Window{1000, 2800})
	for k, v := range vs {
		ts := 1000 + float64(k)*1000/60
		mt := float64(len(up)-1)*1000/60 - ts // the rising master's time
		e := easeInOutCubic(clamp01((mt - 2000) / 300))
		for _, p := range v.Pins {
			if p.Name == "touchbar" && p.Src.H > 0 && math.Abs(p.Src.Y-(804-275*e)) > 874.0/434*1.5 {
				t.Fatalf("%.0f ms: touchbar Src %+v, the master's bar is at %.2f", ts, p.Src, 804-275*e)
			}
		}
	}
}

func TestTimeKeyboardSeesTheSlideBelowTheRect(t *testing.T) {
	// The keyboard's rect ends at 816 pt, but it slides in from the screen's
	// foot: its first fifth of the rise (and a drop's last) happens below
	// the rect. The motion is the whole slide, 2000-2300 ms.
	s, l := kbLog()
	got, _ := TimeKeyboard(s, l, kbMaster(true, false), 60)
	if len(got.Motions) != 1 {
		t.Fatalf("motions = %+v", got.Motions)
	}
	m := got.Motions[0]
	if t0, t1 := m.T[0], m.T[len(m.T)-1]; math.Abs(t0-2000) > 1000.0/60+1e-6 || math.Abs(t1-2300) > 1000.0/60+1e-6 {
		t.Fatalf("motion %.1f-%.1f ms, want 2000-2300 to a frame", t0, t1)
	}
}

func TestTimeKeyboardFallsBackAndWarns(t *testing.T) {
	s, l := kbLog()
	got, warns := TimeKeyboard(s, l, kbMaster(false), 60)
	if len(warns) != 1 || !strings.Contains(warns[0], "1500-2800 ms") {
		t.Fatalf("warned %q, want one naming the 1500-2800 ms gap", warns)
	}
	if len(got.Events) != len(l.Events) {
		t.Fatalf("with no motion found the log gained %d events", len(got.Events)-len(l.Events))
	}
}

// viewsEqual reports whether two views frame the same rows the same way.
func viewsEqual(a, b View) bool {
	if !rectNear(a.Region, b.Region, 1e-6) || !near(a.RegionY, b.RegionY, 1e-6) || !near(a.RegionH, b.RegionH, 1e-6) || len(a.Pins) != len(b.Pins) {
		return false
	}
	for i := range a.Pins {
		p, q := a.Pins[i], b.Pins[i]
		if p.Name != q.Name || !rectNear(p.Src, q.Src, 1e-6) || !near(p.Y, q.Y, 1e-6) || !near(p.H, q.H, 1e-6) {
			return false
		}
	}
	return true
}
