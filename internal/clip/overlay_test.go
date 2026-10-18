package clip

import (
	"maps"
	"slices"
	"testing"
)

// ovScript is a full-camera mobile script with the given overlays.
func ovScript(fps int, o Overlays) *Script {
	s := camScript(600, "full", nil)
	s.Form = "mobile"
	s.FPS = &fps
	s.Overlays = o
	return s
}

func ovLog(evs ...Event) *Log {
	return camLog(append([]Event{rs(0, map[string]*Rect{})}, evs...)...)
}

// ovRun builds the layout and views for s and l over w, and times them.
func ovRun(t *testing.T, s *Script, l *Log, w Window) (Layout, []View, [][]Op, []SpriteSpec, []string) {
	t.Helper()
	lay, vs := path(t, s, l, w)
	ops, specs, warns := Timeline(s, l, lay, vs, w)
	if len(ops) != len(vs) {
		t.Fatalf("len(ops) = %d, want %d", len(ops), len(vs))
	}
	return lay, vs, ops, specs, warns
}

func opFor(ops []Op, key string) (Op, bool) {
	for _, o := range ops {
		if o.Key == key {
			return o, true
		}
	}
	return Op{}, false
}

func capEv(t float64, text string) Event { return Event{Type: "caption", T: t, Text: text} }

func TestCaptionSpan(t *testing.T) {
	s := ovScript(60, Overlays{Captions: true})
	l := ovLog(capEv(1400, "Tap"), capEv(7000, ""))
	_, _, ops, specs, _ := ovRun(t, s, l, Window{1000, 9000})
	if len(specs) != 1 || specs[0].Kind != "caption" || specs[0].Text != "Tap" {
		t.Fatalf("specs = %+v", specs)
	}
	key := specs[0].Key
	for k := range ops {
		o, has := opFor(ops[k], key)
		if want := k >= 24 && k <= 369; has != want {
			t.Fatalf("frame %d: caption present = %v, want %v", k, has, want)
		}
		switch k {
		case 24:
			if o.Opacity != 0 {
				t.Errorf("opacity at 24 = %g, want 0", o.Opacity)
			}
		case 33, 200, 360:
			if !near(o.Opacity, 1, 1e-9) {
				t.Errorf("opacity at %d = %g, want 1", k, o.Opacity)
			}
		}
	}
}

func TestCaptionsOffDrawsNothing(t *testing.T) {
	s := ovScript(60, Overlays{})
	l := ovLog(capEv(1400, "Tap"), capEv(7000, ""))
	_, _, ops, specs, _ := ovRun(t, s, l, Window{1000, 3000})
	for k, f := range ops {
		if len(f) != 0 {
			t.Fatalf("frame %d has ops %+v", k, f)
		}
	}
	if len(specs) != 0 {
		t.Errorf("specs = %+v", specs)
	}
}

func TestCaptionRidesWithKeyboard(t *testing.T) {
	s := ovScript(60, Overlays{Captions: true})
	l := ovLog(capEv(0, "Tap"))
	lay, vs := path(t, s, l, Window{0, 200})
	vs[3].RegionY, vs[3].RegionH = 0, float64(lay.RegionH)-469
	ops, _, _ := Timeline(s, l, lay, vs, Window{0, 200})
	o, ok := opFor(ops[3], "caption-0")
	if !ok || o.Y != -469 || o.X != 0 || o.Scale != 1 {
		t.Errorf("caption op = %+v, %v", o, ok)
	}
	if o, _ := opFor(ops[2], "caption-0"); o.Y != 0 {
		t.Errorf("caption Y with no strip = %g", o.Y)
	}
}

func tapEv(step int, t float64, at Point) Event {
	return Event{Type: "tap", T: t, Step: &step, At: &at}
}

func TestTapEnvelope(t *testing.T) {
	s := ovScript(1000, Overlays{Taps: true})
	l := ovLog(tapEv(0, 1100, Point{201, 400}))
	_, _, ops, specs, _ := ovRun(t, s, l, Window{1000, 1800})
	at := func(ms float64) Op {
		o, ok := opFor(ops[int(ms)], "tap")
		if !ok {
			t.Fatalf("no tap op at +%g ms", ms)
		}
		return o
	}
	t0 := 100.0
	for _, c := range []struct{ ms, op float64 }{
		{t0, 0}, {t0 + tapIn, 1}, {t0 + tapIn + tapHold, 1}, {t0 + tapIn + tapHold + tapOut, 0},
	} {
		if o := at(c.ms); !near(o.Opacity, c.op, 1e-9) {
			t.Errorf("opacity at +%g = %g, want %g", c.ms-t0, o.Opacity, c.op)
		}
	}
	// A fixed camera: Op.Scale is the envelope's own scale.
	if o := at(t0); !near(o.Scale, tapFromS, 1e-9) {
		t.Errorf("scale at start = %g, want %g", o.Scale, tapFromS)
	}
	if o := at(t0 + tapIn + tapHold + tapOut); !near(o.Scale, tapToS, 1e-9) {
		t.Errorf("scale at end = %g, want %g", o.Scale, tapToS)
	}
	if _, has := opFor(ops[int(t0+tapIn+tapHold+tapOut)+1], "tap"); has {
		t.Error("tap still drawn after its release")
	}
	if len(specs) != 1 || specs[0].Key != "tap" || specs[0].Kind != "tap" || !near(specs[0].Scale, tapToS, 1e-9) || specs[0].W != specs[0].H {
		t.Errorf("specs = %+v", specs)
	}
}

func TestSwipeMovesLinearly(t *testing.T) {
	s := ovScript(1000, Overlays{Taps: true})
	from, to := Point{60, 300}, Point{282, 300}
	step := 1
	l := ovLog(Event{Type: "swipe", T: 1100, Step: &step, From: &from, To: &to, Duration: 350})
	lay, vs, ops, _, _ := ovRun(t, s, l, Window{1000, 1800})
	k := 100 + 175
	o, ok := opFor(ops[k], "tap")
	if !ok {
		t.Fatal("no disc at the swipe's midpoint")
	}
	x, y, _, _ := toOutput(Point{171, 300}, vs[k], lay)
	if !near(o.X, x, 0.5) || !near(o.Y, y, 0.5) {
		t.Errorf("disc at (%g,%g), want (%g,%g)", o.X, o.Y, x, y)
	}
	e, _ := opFor(ops[100+350+int(tapOut)], "tap")
	xe, _, _, _ := toOutput(to, vs[0], lay)
	if !near(e.X, xe, 0.5) {
		t.Errorf("release at X %g, want %g", e.X, xe)
	}
}

func TestTapMapsThroughPin(t *testing.T) {
	down := map[string]*Rect{"touchbar": {0, 812, 402, 62}, "kb": nil}
	up := map[string]*Rect{"touchbar": {0, 498, 402, 62}, "kb": {0, 560, 402, 314}}
	s := camScript(600, "fixed", &regionT, touchbarT, kbT)
	s.Form = "mobile"
	s.Overlays = Overlays{Taps: true}
	l := camLog(
		rs(0, map[string]*Rect{"region": {0, 120, 402, 640}, "touchbar": {0, 812, 402, 62}, "kb": nil}),
		rs(250, down), rs(500, down), rs(750, down),
		rs(1000, up), rs(1250, up), rs(1500, up),
		tapEv(0, 100, Point{200, 840}),  // on the touchbar, keyboard down
		tapEv(1, 1300, Point{200, 700}), // on a key, keyboard up
	)
	lay, vs := path(t, s, l, Window{0, 1500})
	ops, _, warns := Timeline(s, l, lay, vs, Window{0, 1500})
	if len(warns) != 0 {
		t.Fatalf("warnings %q", warns)
	}
	in := func(k int, name string) {
		t.Helper()
		var pv *PinView
		for i := range vs[k].Pins {
			if vs[k].Pins[i].Name == name {
				pv = &vs[k].Pins[i]
			}
		}
		o, ok := opFor(ops[k], "tap")
		if pv == nil || !ok {
			t.Fatalf("frame %d: pin %s %v, tap %v", k, name, pv, ok)
		}
		if o.Y < pv.Y || o.Y >= pv.Y+pv.H {
			t.Errorf("frame %d: tap Y %g outside %s strip [%g,%g)", k, o.Y, name, pv.Y, pv.Y+pv.H)
		}
	}
	in(10, "touchbar")
	in(80, "kb")
}

func TestTapOutsideIsDroppedWithWarning(t *testing.T) {
	s := ovScript(60, Overlays{Taps: true})
	l := ovLog(tapEv(2, 1100, Point{500, 2000}))
	_, _, ops, _, warns := ovRun(t, s, l, Window{1000, 1500})
	if len(warns) != 1 || warns[0] != "step 2 tap at (500,2000) is outside the framed area" {
		t.Errorf("warnings = %q", warns)
	}
	for k, f := range ops {
		if len(f) != 0 {
			t.Fatalf("frame %d has ops %+v", k, f)
		}
	}
}

func desktopScript(fps int, o Overlays) *Script {
	s := ovScript(fps, o)
	s.Form = "desktop"
	return s
}

func TestKeysAndCursorOps(t *testing.T) {
	s := desktopScript(1000, Overlays{Taps: true, Keys: true, Cursor: true})
	a, b := Point{50, 100}, Point{300, 400}
	l := ovLog(tapEv(0, 1300, a), Event{Type: "key", T: 1500, Step: ptr(1), Key: "return"}, tapEv(2, 2500, b))
	lay, vs, ops, specs, _ := ovRun(t, s, l, Window{1000, 3000})

	kinds := map[string]bool{}
	for _, sp := range specs {
		kinds[sp.Kind] = true
	}
	for _, k := range []string{"keys", "click", "cursor"} {
		if !kinds[k] {
			t.Errorf("no %s spec in %+v", k, specs)
		}
	}
	if kinds["tap"] {
		t.Error("a desktop click drew a touch disc")
	}
	for k := range ops {
		if _, ok := opFor(ops[k], "cursor"); !ok {
			t.Fatalf("frame %d has no cursor", k)
		}
	}
	// the key badge spans keyHold
	for k := range ops {
		_, has := opFor(ops[k], "key-return")
		if want := k >= 500 && k <= 500+int(keyHold); has != want {
			t.Fatalf("frame %d: key badge present = %v, want %v", k, has, want)
		}
	}
	if o, _ := opFor(ops[500+int(keyHold)/2], "key-return"); o.Opacity != 1 {
		t.Errorf("badge opacity mid-span = %g", o.Opacity)
	}
	var sp SpriteSpec
	for _, c := range specs {
		if c.Key == "key-return" {
			sp = c
		}
	}
	if sp.Text != "↩" {
		t.Errorf("badge text %q", sp.Text)
	}
	// cursor: at a by a's click - lead, monotonic on the way to b, at b by its click - lead
	cx := func(k int) float64 { o, _ := opFor(ops[k], "cursor"); return o.X }
	ax, _, _, _ := toOutput(a, vs[0], lay)
	bx, _, _, _ := toOutput(b, vs[0], lay)
	if !near(cx(300-int(cursorLead)), ax, 1e-6) || !near(cx(0), ax, 1e-6) {
		t.Errorf("cursor X before click A = %g / %g, want %g", cx(300-int(cursorLead)), cx(0), ax)
	}
	if !near(cx(1500-int(cursorLead)), bx, 1e-6) {
		t.Errorf("cursor X at B - lead = %g, want %g", cx(1500-int(cursorLead)), bx)
	}
	for k := 301; k <= 1350; k++ {
		if cx(k) < cx(k-1)-1e-9 {
			t.Fatalf("cursor X went back at frame %d", k)
		}
	}
	if cx(900) <= ax || cx(900) >= bx {
		t.Errorf("cursor X mid-travel = %g, want within (%g, %g)", cx(900), ax, bx)
	}
	// a click is a ring
	if _, ok := opFor(ops[300+100], "click"); !ok {
		t.Error("no click ring after the click")
	}
}

func TestKeyGlyphs(t *testing.T) {
	want := map[string]string{"return": "↩", "delete": "⌫", "tab": "⇥", "escape": "⎋", "space": "␣",
		"up": "↑", "down": "↓", "left": "←", "right": "→"}
	for _, k := range Keys {
		g, ok := keyGlyphs[k]
		if !ok || g != want[k] {
			t.Errorf("key %q glyph %q (%v), want %q", k, g, ok, want[k])
		}
	}
	if len(keyGlyphs) != len(Keys) || !slices.Equal(slices.Sorted(maps.Keys(want)), slices.Sorted(maps.Keys(keyGlyphs))) {
		t.Errorf("keyGlyphs keys differ from the contract's")
	}
}
