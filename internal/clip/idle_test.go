package clip

import (
	"image"
	"math"
	"path/filepath"
	"strings"
	"testing"
)

const fms = 1000.0 / 60

// idleScript is two taps around stepsBetween, every step at its defaults.
func idleScript(t *testing.T, between string) *Script {
	body := minimal + between + "[[step]]\naction = \"tap\"\ntarget = {label=\"B\"}\n"
	return load(t, body)
}

// idleLog taps step 0 at 500 and step last at 3500, with a rects sample
// and a caption in the gap between.
func idleLog(last int) *Log {
	r := Rect{0, 0, 10, 10}
	return &Log{End: 4990, Events: []Event{
		{Type: "rects", T: 0, Rects: map[string]*Rect{"a": &r}},
		{Type: "tap", T: 500, Step: ptr(0)},
		{Type: "rects", T: 2000, Rects: map[string]*Rect{"a": &r}},
		{Type: "tap", T: 3500, Step: ptr(last)},
		{Type: "end", T: 4990},
	}}
}

// idleActivity is 300 frames at 60 fps, active over frames 30-60 and
// 210-240: motion from 0.5 to 1.0 s and from 3.5 to 4.0 s.
func idleActivity() []float64 {
	a := make([]float64, 300)
	for i := 30; i <= 60; i++ {
		a[i], a[i+180] = 0.01, 0.01
	}
	return a
}

func TestIdleCutsShortenAStaticGap(t *testing.T) {
	s := idleScript(t, "")
	l := idleLog(1)
	w := Window{0, 4500}
	cuts := IdleCuts(s, l, idleActivity(), w, 60, nil)
	if len(cuts) != 1 {
		t.Fatalf("cuts = %+v, want one", cuts)
	}
	// The gap runs from the motion's end, 1000, to its restart, frame 210's
	// change from 3483; a whole number of frames comes out of its middle.
	gap := 3500 - fms - 1000
	removed := Removed(cuts)
	if kept := gap - removed; kept < 500-1e-6 || kept >= 500+fms {
		t.Fatalf("removed %.2f ms of the %.2f ms gap: kept %.2f, want 500 to a frame", removed, gap, kept)
	}
	c := cuts[0]
	if mid, cm := 1000+gap/2, (c.Start+c.End)/2; math.Abs(mid-cm) > fms {
		t.Errorf("cut %+v is centred on %.1f, want the gap's middle %.1f", c, cm, mid)
	}
	if k := (c.Start - w.Start) / fms; math.Abs(k-math.Round(k)) > 1e-6 {
		t.Errorf("cut starts %.3f frames in: not on a frame", k)
	}

	got, gw := CompressLog(l, cuts), Compress(w, cuts)
	if got.Events[3].T != 3500-removed || got.End != 4990-removed || gw.End != 4500-removed || gw.Start != 0 {
		t.Errorf("after the cut: tap %.2f, end %.2f, window %+v; removed %.2f", got.Events[3].T, got.End, gw, removed)
	}
	if got.Events[1].T != 500 {
		t.Errorf("the tap before the cut moved to %.2f", got.Events[1].T)
	}
	if got.Events[2].T != c.Start {
		t.Errorf("the rects sample inside the cut is at %.2f, want the cut's start %.2f", got.Events[2].T, c.Start)
	}
	if l.Events[3].T != 3500 {
		t.Error("CompressLog changed its input")
	}
}

func TestIdleCutsKeepAPause(t *testing.T) {
	s := idleScript(t, "[[step]]\naction = \"pause\"\nhold_ms = 1200\n")
	if cuts := IdleCuts(s, idleLog(2), idleActivity(), Window{0, 4500}, 60, nil); len(cuts) != 0 {
		t.Fatalf("cuts = %+v: a pause's hold is never cut", cuts)
	}
}

func TestIdleCutsKeepTheHeadAndTail(t *testing.T) {
	// Idle before the first activity and after the last is the trim's
	// head and settle, not interior.
	s := idleScript(t, "")
	l := &Log{End: 9000, Events: []Event{{Type: "tap", T: 3000, Step: ptr(0)}, {Type: "tap", T: 3100, Step: ptr(1)}}}
	if cuts := IdleCuts(s, l, make([]float64, 540), Window{0, 9000}, 60, nil); len(cuts) != 0 {
		t.Fatalf("cuts = %+v", cuts)
	}
	// A caption at t 0, from a leading wait step, ends before a follow
	// clip's trim starts: the head after it is still the head.
	l = &Log{End: 9000, Events: []Event{{Type: "caption", T: 0}, {Type: "tap", T: 3000, Step: ptr(0)}, {Type: "tap", T: 3800, Step: ptr(1)}}}
	if cuts := IdleCuts(s, l, make([]float64, 540), Window{3000 - FollowHead(), 6000}, 60, nil); len(cuts) != 0 {
		t.Fatalf("cuts = %+v: the head after a caption before the window was cut", cuts)
	}
}

func TestIdleCutsKeepAFollowMove(t *testing.T) {
	// The second tap's target is out of R0, so the camera pans to it: the
	// cut must leave the pan's move and lead before the tap, never start
	// it while the first tap's ripple plays.
	s, l := followFixture(tap(1000, Point{200, 500}, &Rect{150, 480, 100, 40}), tap(4000, Point{200, 200}, &Rect{150, 180, 100, 40}))
	s.Steps = []Step{{Action: "tap", HoldMS: ptr(400)}}
	s.Output.MaxIdleMS = ptr(500)
	w := Window{0, 6000}
	tr := NewTrack(l)
	lay, err := NewLayout(s, l, tr)
	if err != nil {
		t.Fatal(err)
	}
	moves, err := FollowMoves(s, l, tr, lay, w)
	if err != nil {
		t.Fatal(err)
	}
	cuts := IdleCuts(s, l, make([]float64, 360), w, 60, moves)
	if len(cuts) == 0 {
		t.Fatal("no cut: the gap before the move is still idle")
	}
	if before := shift(4000, cuts) - shift(1400, cuts); before < move+lead-1e-6 {
		t.Fatalf("%.1f ms kept between the first tap's hold and the second tap: the %g ms pan cannot fit", before, move+lead)
	}
}

func TestIdleCutsKeepAGesturesOverlay(t *testing.T) {
	// A long press with no hold: its disc stays until tapIn + duration +
	// tapOut after it starts, and no cut may start inside that.
	body := strings.Replace(minimal, "action = \"tap\"", "action = \"long_press\"\nhold_ms = 0", 1) +
		"[[step]]\naction = \"tap\"\ntarget = {label=\"B\"}\n"
	s := load(t, body)
	l := &Log{End: 5000, Events: []Event{{Type: "long_press", T: 500, Step: ptr(0), At: &Point{10, 10}, Duration: 1500}, {Type: "tap", T: 3500, Step: ptr(1)}}}
	cuts := IdleCuts(s, l, make([]float64, 300), Window{0, 4500}, 60, nil)
	if end := 500 + tapIn + 1500 + tapOut; len(cuts) != 1 || cuts[0].Start < end {
		t.Fatalf("cuts = %+v: a cut starts before the press's disc is gone at %g", cuts, end)
	}
}

func TestIdleCutsCountACaretBlinkIdle(t *testing.T) {
	// Activity's own frames, 200 px across a 402 pt screen: an iOS caret,
	// 2 by about 22 pt, downscales to a blur that moves 46 px a blink (the
	// clip-idle-compress master's caret, 0.00053); the fixture's moves 48,
	// on for half a second and off for half, through the whole gap. A
	// square moves 0.5-1.0 s and 3.5-4.0 s.
	frames := make([]*image.Gray, 300)
	for i := range frames {
		g := image.NewGray(image.Rect(0, 0, 200, 436))
		for j := range g.Pix {
			g.Pix[j] = 20
		}
		x := 20
		switch {
		case i >= 30 && i <= 60:
			x = 20 + 2*(i-30)
		case i > 60 && i < 210:
			x = 80
		case i >= 210 && i <= 240:
			x = 80 - 2*(i-210)
		}
		for y := 300; y < 340; y++ {
			for xx := x; xx < x+40; xx++ {
				g.Pix[y*200+xx] = 230
			}
		}
		if i > 60 && i < 210 && (i/30)%2 == 0 {
			for y := 100; y < 112; y++ {
				for x := 100; x < 104; x++ {
					g.Pix[y*200+x] = 230
				}
			}
		}
		frames[i] = g
	}
	act := Activity(frames)
	for i := 62; i < 209; i++ {
		if act[i] > idleShare {
			t.Fatalf("frame %d: the caret alone is activity %.5f, above idleShare %g", i, act[i], idleShare)
		}
	}
	cuts := IdleCuts(idleScript(t, ""), idleLog(1), act, Window{0, 4500}, 60, nil)
	if len(cuts) != 1 || Removed(cuts) < 1900 {
		t.Fatalf("cuts = %+v: the blinking caret kept the gap", cuts)
	}
	// A 22 pt checkbox ticking, 11 by 11 px here, is activity.
	box := image.NewGray(image.Rect(0, 0, 200, 436))
	copy(box.Pix, frames[100].Pix)
	for y := 200; y < 211; y++ {
		for x := 50; x < 61; x++ {
			box.Pix[y*200+x] = 230
		}
	}
	if a := Activity([]*image.Gray{frames[100], box})[1]; a <= idleShare {
		t.Errorf("a checkbox tick is activity %.5f, not above idleShare %g", a, idleShare)
	}
}

func TestApplyCutsDropsTheFramesAndChecksTheSeam(t *testing.T) {
	dir := t.TempDir()
	var frames []string
	for k := range 10 {
		img := solid(4, 4, grey(0.1))
		if k >= 7 {
			img = solid(4, 4, grey(0.9))
		}
		p := filepath.Join(dir, "m"+string(rune('0'+k))+".png")
		writePNG(t, p, img)
		frames = append(frames, p)
	}
	w := Window{100, 100 + 10*fms}
	static := Cut{100 + 2*fms, 100 + 5*fms}
	var warned []string
	warn := func(msg string) { warned = append(warned, msg) }
	kept, cuts, err := ApplyCuts(frames, w, 60, []Cut{static}, warn)
	if err != nil {
		t.Fatal(err)
	}
	if len(cuts) != 1 || len(kept) != 7 || kept[2] != frames[5] {
		t.Fatalf("kept %d frames, cuts %+v", len(kept), cuts)
	}
	// Cutting frames 4-6 joins frame 3 to frame 7, which differ: the cut
	// would jump, so it is not made.
	jump := Cut{100 + 4*fms, 100 + 7*fms}
	if len(warned) != 0 {
		t.Fatalf("warned %q for a cut that was made", warned)
	}
	kept, cuts, err = ApplyCuts(frames, w, 60, []Cut{jump}, warn)
	if err != nil {
		t.Fatal(err)
	}
	if len(cuts) != 0 || len(kept) != 10 {
		t.Fatalf("a cut whose seam frames differ was made: kept %d, cuts %+v", len(kept), cuts)
	}
	if len(warned) != 1 || !strings.Contains(warned[0], "166.7-216.7 ms") {
		t.Fatalf("warned %q, want one naming the span 166.7-216.7 ms", warned)
	}
}
