package clip

import (
	"math"
	"strings"
	"testing"
)

var (
	regionT   = Target{Name: "region", Label: "Outline"}
	touchbarT = Target{Name: "touchbar", Label: "Touchbar"}
	kbT       = Target{Name: "kb", System: "keyboard"}
)

func camScript(w int, mode string, region *Target, pins ...Target) *Script {
	s := &Script{FPS: ptr(60), Output: Output{Width: w}, Targets: map[string]Target{},
		Camera: Camera{Mode: mode, Region: region, Pin: pins, MaxZoom: ptr(1.0)}}
	for _, p := range pins {
		s.Targets[p.Name] = p
	}
	return s
}

func camLog(evs ...Event) *Log {
	return &Log{Meta: Meta{Scale: 3, Screen: [2]float64{402, 874}, FPS: 60}, Events: evs, End: 10000}
}

func tap(t float64, at Point, target *Rect) Event {
	return Event{Type: "tap", T: t, Step: ptr(0), At: &at, Target: target}
}

func path(t *testing.T, s *Script, l *Log, w Window) (Layout, []View) {
	t.Helper()
	tr := NewTrack(l)
	lay, err := NewLayout(s, l, tr)
	if err != nil {
		t.Fatal(err)
	}
	vs, err := Path(s, l, tr, lay, w)
	if err != nil {
		t.Fatal(err)
	}
	if len(vs) != w.Frames(*s.FPS) {
		t.Fatalf("len(Path) = %d, want %d", len(vs), w.Frames(*s.FPS))
	}
	return lay, vs
}

func near(a, b, eps float64) bool { return math.Abs(a-b) <= eps }

func rectNear(a, b Rect, eps float64) bool {
	return near(a.X, b.X, eps) && near(a.Y, b.Y, eps) && near(a.W, b.W, eps) && near(a.H, b.H, eps)
}

func pad(r Rect) Rect { return Rect{r.X - 16, r.Y - 16, r.W + 32, r.H + 32} }

// moves counts the runs of frames over which the region changes.
func moves(vs []View) int {
	n, moving := 0, false
	for k := 1; k < len(vs); k++ {
		m := !rectNear(vs[k].Region, vs[k-1].Region, 1e-9)
		if m && !moving {
			n++
		}
		moving = m
	}
	return n
}

func stripsH(v View) float64 {
	h := 0.0
	for _, p := range v.Pins {
		h += p.H
	}
	return h
}

func TestLayoutFull(t *testing.T) {
	s := camScript(1080, "full", nil)
	l := camLog(rs(0, map[string]*Rect{}))
	lay, err := NewLayout(s, l, NewTrack(l))
	if err != nil {
		t.Fatal(err)
	}
	if lay.W != 1080 || lay.H != 2348 || lay.RegionH != 2348 || len(lay.Pins) != 0 {
		t.Errorf("Layout = %+v", lay)
	}
}

func pinnedLayout(tbH float64) (*Script, *Log) {
	return camScript(600, "fixed", &regionT, touchbarT),
		camLog(rs(0, map[string]*Rect{"region": {0, 120, 402, 640}, "touchbar": {0, 874 - tbH, 402, tbH}}))
}

func TestLayoutPinsAndEvenHeight(t *testing.T) {
	s, l := pinnedLayout(62)
	lay, vs := path(t, s, l, Window{0, 500})
	if lay.RegionH != 955 || lay.H != 1048 {
		t.Fatalf("Layout = %+v", lay)
	}
	if lay.Pins[0] != (PinSlot{Name: "touchbar"}) {
		t.Errorf("Pins = %+v", lay.Pins)
	}
	v := vs[0]
	p := v.Pins[0]
	if !near(p.H, 92.54, 0.01) || !near(p.Y, 955.46, 0.01) || p.Shown != 1 {
		t.Errorf("pin = %+v", p)
	}
	if p.Y < v.RegionY+v.RegionH-1e-9 {
		t.Errorf("pin at %g is not below the region (%g + %g)", p.Y, v.RegionY, v.RegionH)
	}

	s, l = pinnedLayout(63)
	lay, vs = path(t, s, l, Window{0, 500})
	if lay.RegionH != 956 || lay.H != 1050 {
		t.Fatalf("Layout (63 pt) = %+v", lay)
	}
	if p := vs[0].Pins[0]; !near(p.H, 94.03, 0.01) {
		t.Errorf("pin (63 pt) H = %g", p.H)
	}
}

func TestKeyboardStripTakesRegionSpace(t *testing.T) {
	down := map[string]*Rect{"touchbar": {0, 812, 402, 62}, "kb": nil}
	up := map[string]*Rect{"touchbar": {0, 498, 402, 62}, "kb": {0, 560, 402, 314}}
	s := camScript(600, "fixed", &regionT, touchbarT, kbT)
	l := camLog(
		rs(0, map[string]*Rect{"region": {0, 120, 402, 640}, "touchbar": {0, 812, 402, 62}, "kb": nil}),
		rs(250, down), rs(500, down), rs(750, down),
		rs(1000, up), rs(1250, up), rs(1500, up),
	)
	lay, vs := path(t, s, l, Window{0, 1500})
	if lay.H != 1048 || len(lay.Pins) != 2 || lay.Pins[1] != (PinSlot{Name: "kb", System: true}) {
		t.Fatalf("Layout = %+v", lay)
	}
	kbH := 600 * 314 / 402.0
	for k, v := range vs {
		at := float64(k) * 1000 / 60
		if !near(v.RegionH+stripsH(v), 1048, 1e-9) {
			t.Fatalf("frame %d: region %g + strips %g != 1048", k, v.RegionH, stripsH(v))
		}
		for _, p := range v.Pins {
			if !near(p.Src.H/p.H, p.Src.W/600, 1e-12) {
				t.Fatalf("frame %d: %s strip stretched, src %gx%g into 600x%g", k, p.Name, p.Src.W, p.Src.H, p.H)
			}
		}
		switch {
		case at <= 750:
			if !near(v.RegionH, 955.46, 0.01) || len(v.Pins) != 1 || v.Pins[0].Name != "touchbar" {
				t.Fatalf("frame %d (down): %+v", k, v)
			}
		case at >= 1000:
			if len(v.Pins) != 2 {
				t.Fatalf("frame %d (up): %+v", k, v)
			}
			tb, kb := v.Pins[0], v.Pins[1]
			if tb.Name != "touchbar" || kb.Name != "kb" || kb.Shown != 1 ||
				!near(kb.H, kbH, 1e-9) || !near(kb.Y+kb.H, 1048, 1e-9) || !near(tb.Y+tb.H, kb.Y, 1e-9) ||
				!near(v.RegionH, 486.8, 0.01) {
				t.Fatalf("frame %d (up): %+v", k, v)
			}
		}
	}
	peak := 3 * kbH / 15
	for k := 46; k <= 60; k++ {
		step := vs[k-1].RegionH - vs[k].RegionH
		if step < 0 {
			t.Fatalf("frame %d: region grew by %g while the keyboard rose", k, -step)
		}
		if step > 1.1*peak {
			t.Fatalf("frame %d: region stepped %g px, peak allowed %g", k, step, 1.1*peak)
		}
	}
}

func TestElementPinResizesPerFrame(t *testing.T) {
	s := camScript(600, "fixed", &regionT, touchbarT)
	l := camLog(
		rs(0, map[string]*Rect{"region": {0, 120, 402, 640}, "touchbar": {0, 812, 402, 62}}),
		rs(250, map[string]*Rect{"touchbar": {0, 750, 402, 124}}),
		rs(500, map[string]*Rect{"touchbar": {0, 750, 402, 124}}),
	)
	lay, at125 := path(t, s, l, Window{125, 225})
	if lay.H != 1048 {
		t.Fatalf("Layout = %+v", lay)
	}
	if p := at125[0].Pins[0]; !near(p.H, 600*93/402.0, 1e-9) || p.Src.H != 93 {
		t.Errorf("at 125 ms: %+v", p)
	}
	_, vs := path(t, s, l, Window{0, 600})
	for k, v := range vs {
		p := v.Pins[0]
		if !near(p.Src.H/p.H, p.Src.W/600, 1e-12) {
			t.Fatalf("frame %d: strip stretched, src %gx%g into 600x%g", k, p.Src.W, p.Src.H, p.H)
		}
		if !near(v.RegionH+p.H, 1048, 1e-9) {
			t.Fatalf("frame %d: region %g + strip %g != 1048", k, v.RegionH, p.H)
		}
		if k >= 15 && (!near(p.H, 185.07, 0.01) || !near(v.RegionH, 862.93, 0.01)) {
			t.Fatalf("frame %d: strip %g, region %g", k, p.H, v.RegionH)
		}
	}
}

func TestKeyboardStripRefusedWhenTooTall(t *testing.T) {
	s := camScript(600, "fixed", &regionT, touchbarT, kbT)
	l := camLog(
		rs(0, map[string]*Rect{"region": {0, 120, 402, 640}, "touchbar": {0, 812, 402, 62}, "kb": nil}),
		rs(750, map[string]*Rect{"touchbar": {0, 812, 402, 62}, "kb": nil}),
		rs(1000, map[string]*Rect{"touchbar": {0, 12, 402, 62}, "kb": {0, 74, 402, 800}}),
	)
	_, err := NewLayout(s, l, NewTrack(l))
	want := "pins touchbar, kb would leave the framed region under a quarter of the frame at t=1000 ms"
	if err == nil || err.Error() != want {
		t.Errorf("err = %v, want %q", err, want)
	}
}

func TestLayoutRefusesUpsampling(t *testing.T) {
	s := camScript(1440, "full", nil)
	l := camLog(rs(0, map[string]*Rect{}))
	_, err := NewLayout(s, l, NewTrack(l))
	want := "output.width 1440 px would sample below capture resolution: the master is 1206 px across, max_zoom 1.0"
	if err == nil || err.Error() != want {
		t.Errorf("err = %v, want %q", err, want)
	}
}

func TestLayoutRefusesNarrowPin(t *testing.T) {
	s := camScript(600, "fixed", &regionT, touchbarT)
	l := camLog(rs(0, map[string]*Rect{"region": {0, 120, 402, 640}, "touchbar": {100, 812, 150, 62}}))
	_, err := NewLayout(s, l, NewTrack(l))
	want := "output.width 600 px would sample below capture resolution: pin touchbar is 450 px across at t=0 ms, max_zoom 1.0"
	if err == nil || err.Error() != want {
		t.Errorf("err = %v, want %q", err, want)
	}
}

func TestZoomClampRefusesSqueezedRegion(t *testing.T) {
	// No pins: clamped to 200 pt wide, the region is 1600 pt tall, and the
	// screen's 874 would squeeze it to 109.25 pt.
	s := camScript(600, "fixed", &regionT)
	l := camLog(rs(0, map[string]*Rect{"region": {150, 30, 100, 800}}))
	tr := NewTrack(l)
	_, err := NewLayout(s, l, tr)
	want := "camera region would sample below capture resolution at t=0 ms: 109.25 pt across, minimum 200 pt at max_zoom 1.0"
	if err == nil || err.Error() != want {
		t.Errorf("NewLayout err = %v, want %q", err, want)
	}
	if _, err := Path(s, l, tr, Layout{W: 600, H: 4800, RegionH: 4800}, Window{0, 100}); err == nil || err.Error() != want {
		t.Errorf("Path err = %v, want %q", err, want)
	}

	// The keyboard rising pushes the touchbar to 498 and squeezes the region.
	s = camScript(600, "fixed", &regionT, touchbarT, kbT)
	down := map[string]*Rect{"touchbar": {0, 812, 402, 62}, "kb": nil}
	l = camLog(
		rs(0, map[string]*Rect{"region": {100, 20, 200, 750}, "touchbar": {0, 812, 402, 62}, "kb": nil}),
		rs(750, down),
		rs(1000, map[string]*Rect{"touchbar": {0, 498, 402, 62}, "kb": {0, 560, 402, 314}}),
	)
	_, err = NewLayout(s, l, NewTrack(l))
	if prefix := "camera region would sample below capture resolution at t=1000 ms: "; err == nil || !strings.HasPrefix(err.Error(), prefix) {
		t.Errorf("err = %v, want %q…", err, prefix)
	}
}

func TestFollowReturnsToOpening(t *testing.T) {
	// The tap at 400 leaves no room for a move from R0, so the clip opens
	// on the union; the loop's return must land there, not on R0.
	s, l := followFixture(
		tap(400, Point{200, 200}, &Rect{150, 180, 100, 40}),
		tap(3000, Point{340, 770}, &Rect{300, 750, 80, 40}),
	)
	_, vs := path(t, s, l, Window{0, 5000})
	first, last := vs[0].Region, vs[len(vs)-1].Region
	if rectNear(first, r0, 1e-9) {
		t.Fatalf("clip opens on R0 %+v, want the union", first)
	}
	if !rectNear(last, first, 1e-9) {
		t.Errorf("clip ends on %+v, opens on %+v", last, first)
	}
	if n := moves(vs); n != 2 {
		t.Errorf("moves = %d, want 2", n)
	}
}

func TestZoomClampGrowsRegion(t *testing.T) {
	s := camScript(600, "fixed", &regionT)
	l := camLog(rs(0, map[string]*Rect{"region": {150, 400, 100, 100}}))
	_, vs := path(t, s, l, Window{0, 100})
	r := vs[0].Region
	if !near(r.W, 200, 1e-9) || !near(r.X+r.W/2, 200, 1e-9) || !near(r.Y+r.H/2, 450, 1e-9) {
		t.Errorf("region = %+v", r)
	}
}

func TestFixedIsConstant(t *testing.T) {
	s := camScript(600, "fixed", &regionT, touchbarT)
	l := camLog(
		rs(0, map[string]*Rect{"region": {0, 120, 402, 640}, "touchbar": {0, 812, 402, 62}}),
		rs(250, map[string]*Rect{"touchbar": {0, 790, 402, 62}}),
		rs(500, map[string]*Rect{"touchbar": {0, 790, 402, 62}}),
	)
	_, vs := path(t, s, l, Window{0, 600})
	for k, v := range vs {
		if v.Region != vs[0].Region {
			t.Fatalf("frame %d: region %+v != %+v", k, v.Region, vs[0].Region)
		}
	}
	if y0, y7, y15 := vs[0].Pins[0].Src.Y, vs[7].Pins[0].Src.Y, vs[15].Pins[0].Src.Y; y0 != 812 || !(y7 < 812 && y7 > 790) || y15 != 790 {
		t.Errorf("pin Y = %g, %g, %g", y0, y7, y15)
	}
}

// followFixture frames R0 = [100,350,200,300]: RegionH 900, aspect 2:3, and
// a 200 pt minimum width at W 600, scale 3.
func followFixture(evs ...Event) (*Script, *Log) {
	return camScript(600, "follow", &regionT),
		camLog(append([]Event{rs(0, map[string]*Rect{"region": {100, 350, 200, 300}})}, evs...)...)
}

var r0 = Rect{100, 350, 200, 300}

func TestFollowArrivesBeforeAction(t *testing.T) {
	if FollowHead() != 1450 {
		t.Fatalf("FollowHead = %g", FollowHead())
	}
	s, l := followFixture(tap(3000, Point{200, 200}, &Rect{150, 180, 100, 40}))
	w := Window{3000 - FollowHead(), 5000}
	_, vs := path(t, s, l, w)
	g := Rect{100, 50, 200, 300}
	arrive := int((3000 - 250 - w.Start) * 60 / 1000)
	depart := int((3000 - 250 - 700 - w.Start) * 60 / 1000)
	back := int((5000 - 300 - 700 - w.Start) * 60 / 1000)
	for k, v := range vs {
		switch {
		case k <= depart && !rectNear(v.Region, r0, 1e-9):
			t.Fatalf("frame %d: %+v, want R0", k, v.Region)
		case k >= arrive && k <= back && !rectNear(v.Region, g, 1e-9):
			t.Fatalf("frame %d: %+v, want G %+v", k, v.Region, g)
		}
	}
	if rectNear(vs[depart+1].Region, r0, 1e-9) {
		t.Error("still at R0 after the departure frame")
	}
}

func TestFollowNoMoveWhenInside(t *testing.T) {
	s, l := followFixture(tap(3000, Point{200, 470}, &Rect{150, 450, 100, 40}))
	_, vs := path(t, s, l, Window{1550, 5000})
	for k, v := range vs {
		if !rectNear(v.Region, r0, 1e-9) {
			t.Fatalf("frame %d: %+v, want R0", k, v.Region)
		}
	}
}

func TestFollowGoalFromPointWhenNoTarget(t *testing.T) {
	s, l := followFixture(tap(3000, Point{350, 800}, nil), Event{Type: "key", T: 3300, Step: ptr(1), Key: "return"})
	w := Window{1550, 3600}
	_, vs := path(t, s, l, w)
	if r := vs[int((2750-w.Start)*60/1000)].Region; !inside(r, Rect{334, 784, 32, 32}) {
		t.Errorf("arrival region %+v misses the tap point", r)
	}
	if n := moves(vs); n != 1 {
		t.Errorf("moves = %d, want 1", n)
	}
}

func TestFollowMergesCloseActions(t *testing.T) {
	t1, t2 := Rect{20, 200, 60, 40}, Rect{320, 200, 60, 40}
	s, l := followFixture(tap(3000, Point{50, 220}, &t1), tap(3150, Point{350, 220}, &t2))
	w := Window{1550, 3700}
	_, vs := path(t, s, l, w)
	for k, v := range vs {
		at := w.Start + float64(k)*1000/60
		r := v.Region
		t1Out := r.X >= t1.X+t1.W || r.X+r.W <= t1.X || r.Y >= t1.Y+t1.H || r.Y+r.H <= t1.Y
		if at < 3000 && inside(r, pad(t2)) && t1Out {
			t.Fatalf("frame %d (%g ms) frames only the second target: %+v", k, at, v.Region)
		}
	}
	if r := vs[int((2750-w.Start)*60/1000)].Region; !inside(r, pad(t1)) || !inside(r, pad(t2)) {
		t.Errorf("arrival region %+v does not frame both targets", r)
	}
	if n := moves(vs); n != 1 {
		t.Errorf("moves = %d, want 1", n)
	}
}

func TestFollowNoHardCut(t *testing.T) {
	s, l := followFixture(
		tap(3000, Point{200, 370}, &Rect{150, 350, 100, 40}),
		tap(5000, Point{200, 350}, &Rect{40, 330, 320, 40}),
	)
	w := Window{1550, 7000}
	_, vs := path(t, s, l, w)
	if n := moves(vs); n != 3 {
		t.Fatalf("moves = %d, want 3 (two goals and the return)", n)
	}
	centre := func(r Rect) (float64, float64) { return r.X + r.W/2, r.Y + r.H/2 }
	for k := 1; k < len(vs); k++ {
		a, b := vs[k-1].Region, vs[k].Region
		ax, ay := centre(a)
		bx, by := centre(b)
		if d := math.Hypot(bx-ax, by-ay); d > 0.06*a.W {
			t.Fatalf("frame %d: centre jumped %g pt (width %g)", k, d, a.W)
		}
		if z := math.Abs(math.Log(b.W / a.W)); z > 0.06 {
			t.Fatalf("frame %d: zoom jumped %g", k, z)
		}
	}
	at := func(ms float64) int { return int((ms - w.Start) * 60 / 1000) }
	for _, k := range []int{at(2050), at(4050), at(6000)} { // departures
		if !rectNear(vs[k].Region, vs[k-1].Region, 1e-9) {
			t.Errorf("frame %d: moving at departure", k)
		}
	}
	for _, k := range []int{at(2750), at(4750), at(6700)} { // arrivals
		if !rectNear(vs[k+1].Region, vs[k].Region, 1e-9) {
			t.Errorf("frame %d: moving at arrival", k)
		}
	}
	if !rectNear(vs[len(vs)-1].Region, r0, 1e-9) {
		t.Errorf("last frame %+v, want R0", vs[len(vs)-1].Region)
	}
}

func TestRegionStaysBetweenPins(t *testing.T) {
	s := camScript(600, "follow", &regionT, touchbarT)
	l := camLog(
		rs(0, map[string]*Rect{"region": {0, 120, 402, 640}, "touchbar": {0, 812, 402, 62}}),
		tap(3000, Point{200, 795}, &Rect{16, 780, 370, 30}),
	)
	_, vs := path(t, s, l, Window{1550, 5000})
	if moves(vs) == 0 {
		t.Fatal("the camera never moved toward the target")
	}
	for k, v := range vs {
		r, tb := v.Region, v.Pins[0].Src
		if r.Y+r.H > tb.Y+1e-9 || r.Y < -1e-9 {
			t.Fatalf("frame %d: region %+v overlaps the touchbar %+v", k, r, tb)
		}
	}
}

// kbRise is the reviewer's keyboard probe: the touchbar rides the keyboard
// up from 812 to 498 pt while kb eases in over 1000-1300 ms.
func kbRise(evs ...Event) (*Script, *Log) {
	s := camScript(1080, "follow", &regionT, touchbarT, kbT)
	r0, bar, bar2, kb := Rect{0, 100, 402, 700}, Rect{0, 812, 402, 62}, Rect{0, 498, 402, 62}, Rect{0, 560, 402, 314}
	return s, camLog(append([]Event{
		rs(0, map[string]*Rect{"region": &r0, "touchbar": &bar, "kb": nil}),
		rs(1000, map[string]*Rect{"region": &r0, "touchbar": &bar, "kb": nil}),
		rs(1300, map[string]*Rect{"region": &r0, "touchbar": &bar2, "kb": &kb}),
	}, evs...)...)
}

func overlaps(a, b Rect) bool {
	const eps = 1e-9
	return a.X < b.X+b.W-eps && b.X < a.X+a.W-eps && a.Y < b.Y+b.H-eps && b.Y < a.Y+a.H-eps
}

func TestStripsNeverDuplicateContent(t *testing.T) {
	s, l := kbRise()
	_, vs := path(t, s, l, Window{0, 2000})
	var order []string
	for k, v := range vs {
		srcs := []Rect{v.Region}
		var names []string
		for _, p := range v.Pins {
			for _, o := range srcs {
				if overlaps(p.Src, o) {
					t.Fatalf("frame %d: %s src %+v overlaps %+v", k, p.Name, p.Src, o)
				}
			}
			srcs = append(srcs, p.Src)
			names = append(names, p.Name)
		}
		if len(names) == 2 {
			if order == nil {
				order = names
			} else if names[0] != order[0] {
				t.Fatalf("frame %d: strips reordered to %v from %v", k, names, order)
			}
		}
	}
	if order == nil {
		t.Fatal("the keyboard never came up")
	}
}

func TestRegionTopHeldWhileStripEases(t *testing.T) {
	s, l := kbRise()
	_, vs := path(t, s, l, Window{0, 2000})
	// The camera holds still, so the content does: the keyboard takes its
	// 314 pt off the region's bottom.
	for k, v := range vs {
		if !near(v.Region.Y, vs[0].Region.Y, 1e-9) {
			t.Fatalf("frame %d: region top %g, was %g", k, v.Region.Y, vs[0].Region.Y)
		}
	}
	first, last := vs[0].Region, vs[len(vs)-1].Region
	if d := (first.Y + first.H) - (last.Y + last.H); !near(d, 314, 1e-6) {
		t.Errorf("region bottom rose %g pt, want the keyboard's 314", d)
	}
}

func TestFollowIgnoresTapOnPin(t *testing.T) {
	s := camScript(1080, "follow", &regionT, touchbarT)
	l := camLog(
		rs(0, map[string]*Rect{"region": {20, 200, 362, 300}, "touchbar": {0, 812, 402, 62}}),
		tap(2000, Point{200, 840}, &Rect{180, 820, 40, 40}),
	)
	_, vs := path(t, s, l, Window{0, 4000})
	if n := moves(vs); n != 0 {
		t.Errorf("a tap inside the touchbar strip moved the camera %d times", n)
	}
}

func TestConstantStripsCentreTheRegion(t *testing.T) {
	// The touchbar's 92.54 px rounds to 93 in Layout; with strips that
	// never change, the region's centre is still the camera's, so a move
	// that zooms lerps the centre exactly.
	s := camScript(600, "follow", &regionT, touchbarT)
	l := camLog(
		rs(0, map[string]*Rect{"region": {100, 350, 200, 300}, "touchbar": {0, 812, 402, 62}}),
		tap(3000, Point{200, 200}, &Rect{20, 180, 360, 40}),
	)
	w := Window{1550, 3600}
	_, vs := path(t, s, l, w)
	at := func(ms float64) Rect { return vs[int((ms-w.Start)*60/1000)].Region }
	cy := func(r Rect) float64 { return r.Y + r.H/2 }
	from, to, mid := at(2050), at(2750), at(2400)
	if from.W == to.W {
		t.Fatal("the fixture's move does not zoom")
	}
	if want := (cy(from) + cy(to)) / 2; !near(cy(mid), want, 1e-9) {
		t.Errorf("mid-move centre %g, want %g", cy(mid), want)
	}
}

// lagRise is a keyboard rising at 1000-1300 ms and dropping at 3000-3300
// under a filter bar and a touchbar riding it, with a real capture's
// geometry: the keyboard's rect stops at 816 pt, above the home indicator,
// while the bars rest at the screen's foot, so a bar travels 275 pt to the
// keyboard's 233. Flush only at the samples, the bars lag the keyboard's
// eased share between them and overlap its top rows by up to 24 pt.
func lagRise() (*Script, *Log) {
	filterT := Target{Name: "filter", Label: "Filter"}
	s := camScript(1080, "follow", &regionT, filterT, touchbarT, kbT)
	r0, kb := Rect{0, 151, 402, 560}, Rect{0, 583, 402, 233}
	f0, b0 := Rect{0, 760, 402, 44}, Rect{0, 804, 402, 36}
	f1, b1 := Rect{0, 485, 402, 44}, Rect{0, 529, 402, 37}
	return s, camLog(
		rs(0, map[string]*Rect{"region": &r0, "filter": &f0, "touchbar": &b0, "kb": nil}),
		rs(1000, map[string]*Rect{"region": &r0, "filter": &f0, "touchbar": &b0, "kb": nil}),
		rs(1300, map[string]*Rect{"region": &r0, "filter": &f1, "touchbar": &b1, "kb": &kb}),
		rs(3000, map[string]*Rect{"region": &r0, "filter": &f1, "touchbar": &b1, "kb": &kb}),
		rs(3300, map[string]*Rect{"region": &r0, "filter": &f0, "touchbar": &b0, "kb": nil}),
	)
}

// noSharedRows fails if any two of a view's region and strips show the same
// master rows, or a strip is scaled unlike its width.
func noSharedRows(t *testing.T, k int, v View, w float64) {
	t.Helper()
	srcs := []Rect{v.Region}
	names := []string{"region"}
	for _, p := range v.Pins {
		if !near(p.H, w*p.Src.H/p.Src.W, 1e-6) {
			t.Fatalf("frame %d: %s shows %g pt in %g px: not at its width's scale", k, p.Name, p.Src.H, p.H)
		}
		srcs = append(srcs, p.Src)
		names = append(names, p.Name)
	}
	for i := range srcs {
		for j := i + 1; j < len(srcs); j++ {
			if overlaps(srcs[i], srcs[j]) {
				t.Fatalf("frame %d: %s %+v and %s %+v show the same master rows", k, names[i], srcs[i], names[j], srcs[j])
			}
		}
	}
}

func TestStripsNeverShareRowsWhileBarsLag(t *testing.T) {
	// Mid-rise the touchbar would overlap the keyboard: the keyboard keeps
	// the contested rows, the touchbar loses them off its bottom.
	s, l := lagRise()
	_, vs := path(t, s, l, Window{0, 1600})
	cropped := false
	for _, v := range vs {
		for _, p := range v.Pins {
			if p.Name == "touchbar" && p.Src.H < 36-1e-6 {
				cropped = true
			}
		}
	}
	if !cropped {
		t.Fatal("the touchbar was never cropped: the fixture no longer overlaps")
	}
}

func TestPathNeverSharesRows(t *testing.T) {
	// The invariant over every frame of a whole Path, following and fixed,
	// through the rise and the drop.
	for _, mode := range []string{"follow", "fixed"} {
		s, l := lagRise()
		s.Camera.Mode = mode
		_, vs := path(t, s, l, Window{0, 4000})
		for k, v := range vs {
			noSharedRows(t, k, v, 1080)
		}
	}
}
