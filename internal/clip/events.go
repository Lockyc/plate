package clip

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"slices"
	"sort"
)

// Point is a position in points; JSON [x, y].
type Point struct{ X, Y float64 }

// Rect is a frame in points; JSON [x, y, w, h].
type Rect struct{ X, Y, W, H float64 }

func (p *Point) UnmarshalJSON(b []byte) error {
	var a []float64
	if err := json.Unmarshal(b, &a); err != nil || len(a) != 2 {
		return errors.New("want [x, y]")
	}
	*p = Point{a[0], a[1]}
	return nil
}

func (r *Rect) UnmarshalJSON(b []byte) error {
	var a []float64
	if err := json.Unmarshal(b, &a); err != nil || len(a) != 4 {
		return errors.New("want [x, y, w, h]")
	}
	*r = Rect{a[0], a[1], a[2], a[3]}
	return nil
}

// Meta is the log's header line.
type Meta struct {
	Version  int        `json:"version"`
	Platform string     `json:"platform"`
	Scale    float64    `json:"scale"`
	Screen   [2]float64 `json:"screen"`
	FPS      int        `json:"fps"`
	Sync     string     `json:"sync"`
	// SyncErrorMS is a pointer so that a missing key and a null both read as
	// nil, and nil is refused.
	SyncErrorMS *float64 `json:"sync_error_ms"`
}

// Event is one line after the header. Which fields are set depends on Type;
// a nil Rects value is the null a system target carries while it is down.
type Event struct {
	Type     string
	T        float64
	Step     *int
	At       *Point
	From, To *Point
	Target   *Rect
	Duration float64
	Text     string
	End      float64
	Key      string
	Rects    map[string]*Rect
}

// Log is a parsed event log; Events are stably sorted by T. Motions are
// keyboard transitions measured from the master (TimeKeyboard), never read
// from the log file.
type Log struct {
	Meta    Meta
	Events  []Event
	End     float64
	Motions []Motion
}

// Motion is a keyboard transition's progress as the master shows it: X,
// 0 to 1, at master times T, both ascending; T spans exactly the gap
// between two adjacent rects samples.
type Motion struct{ T, X []float64 }

// at is m's progress at t, linear between its measured frames.
func (m Motion) at(t float64) float64 {
	i := sort.SearchFloat64s(m.T, t)
	switch {
	case i == 0:
		return m.X[0]
	case i == len(m.T):
		return m.X[len(m.X)-1]
	}
	f := (t - m.T[i-1]) / (m.T[i] - m.T[i-1])
	return m.X[i-1] + (m.X[i]-m.X[i-1])*f
}

// wire is every field any line may carry. Pointers tell an absent key from
// zero; the per-type key sets in eventFields say which a line may carry.
type wire struct {
	Type     string           `json:"type"`
	T        *float64         `json:"t"`
	Step     *int             `json:"step"`
	At       *Point           `json:"at"`
	From     *Point           `json:"from"`
	To       *Point           `json:"to"`
	Target   *Rect            `json:"target"`
	Duration *float64         `json:"duration"`
	Text     *string          `json:"text"`
	End      *float64         `json:"end"`
	Key      *string          `json:"key"`
	Rects    map[string]*Rect `json:"rects"`
}

// eventFields lists the keys each event type may carry, beyond type and t.
var eventFields = map[string][]string{
	"rects":      {"rects"},
	"tap":        {"step", "at", "target"},
	"long_press": {"step", "at", "target", "duration"},
	"swipe":      {"step", "from", "to", "target", "duration"},
	"drag":       {"step", "from", "to", "target", "duration"},
	"type":       {"step", "text", "end", "target"},
	"key":        {"step", "key"},
	"caption":    {"text"},
	"end":        {},
}

var metaFields = []string{"type", "version", "platform", "scale", "screen", "fps", "sync", "sync_error_ms"}

type lineEvent struct {
	line int
	Event
}

// ParseEvents reads an event log against the script it was recorded for.
func ParseEvents(r io.Reader, s *Script) (*Log, error) {
	raw, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	var p Problems
	var meta Meta
	var evs []lineEvent
	haveMeta := false
	ends := 0
	var endT float64
	for i, text := range bytes.Split(raw, []byte("\n")) {
		n := i + 1
		if len(bytes.TrimSpace(text)) == 0 {
			continue
		}
		bad := func(format string, a ...any) { p = append(p, fmt.Sprintf("line %d: %s", n, fmt.Sprintf(format, a...))) }
		var keys map[string]json.RawMessage
		if err := json.Unmarshal(text, &keys); err != nil {
			bad("%v", err)
			continue
		}
		var typ string
		json.Unmarshal(keys["type"], &typ)
		if !haveMeta {
			if typ != "meta" {
				return nil, Problems{fmt.Sprintf("line %d: want the meta header", n)}
			}
			haveMeta = true
			var mp Problems
			meta, mp = parseMeta(n, text, keys, s)
			p = append(p, mp...)
			continue
		}
		if typ == "meta" {
			bad("meta header repeated")
			continue
		}
		allowed, known := eventFields[typ]
		if !known {
			bad("unknown event type %s", typ)
			continue
		}
		okKeys := true
		for k := range keys {
			if k != "type" && k != "t" && !slices.Contains(allowed, k) {
				bad("%s: unknown field %q", typ, k)
				okKeys = false
			}
		}
		if !okKeys {
			continue
		}
		var w wire
		if err := json.Unmarshal(text, &w); err != nil {
			bad("%s: %v", typ, err)
			continue
		}
		if w.T == nil {
			bad("%s: t is missing", typ)
			continue
		}
		e := Event{Type: typ, T: *w.T, Step: w.Step, At: w.At, From: w.From, To: w.To,
			Target: w.Target, Rects: w.Rects}
		need := func(field string, present bool) bool {
			if !present {
				bad("%s: %s is missing", typ, field)
			}
			return present
		}
		valid := true
		switch typ {
		case "tap", "long_press":
			valid = need("at", w.At != nil)
		case "swipe", "drag":
			valid = need("from", w.From != nil) && need("to", w.To != nil)
		case "type":
			valid = need("text", w.Text != nil) && need("end", w.End != nil)
		case "key":
			valid = need("key", w.Key != nil)
		case "caption":
			valid = need("text", w.Text != nil)
		case "rects":
			valid = need("rects", w.Rects != nil)
		}
		if _, isAction := actionOf[typ]; valid && isAction {
			valid = need("step", w.Step != nil) && s != nil && checkStep(bad, s, typ, *w.Step)
		}
		if valid && (typ == "long_press" || typ == "swipe" || typ == "drag") {
			if valid = need("duration", w.Duration != nil); valid && *w.Duration <= 0 {
				bad("%s: duration must be > 0", typ)
				valid = false
			}
		}
		if !valid {
			continue
		}
		if w.Duration != nil {
			e.Duration = *w.Duration
		}
		if w.Text != nil {
			e.Text = *w.Text
		}
		if w.End != nil {
			e.End = *w.End
			if e.End < e.T {
				bad("type: end %g precedes t %g", e.End, e.T)
			}
		}
		if w.Key != nil {
			e.Key = *w.Key
		}
		if e.Target != nil && (e.Target.W <= 0 || e.Target.H <= 0) {
			bad("%s: empty target rect", typ)
		}
		for name, rc := range e.Rects {
			switch {
			case rc == nil && !isSystem(s, name):
				bad("rects: %s is null; only a system target may be", name)
			case rc != nil && (rc.W <= 0 || rc.H <= 0):
				bad("rects: %s is an empty rect", name)
			}
		}
		if typ == "end" {
			ends++
			endT = e.T
		}
		evs = append(evs, lineEvent{n, e})
	}
	if !haveMeta {
		return nil, Problems{"line 1: want the meta header"}
	}
	switch {
	case ends == 0:
		p = append(p, "no end event")
	case ends > 1:
		p = append(p, "end given twice")
	}
	if ends == 1 {
		for _, e := range evs {
			if e.T > endT {
				p = append(p, fmt.Sprintf("line %d: %s at %g ms is after end at %g ms", e.line, e.Type, e.T, endT))
			}
		}
	}
	sort.SliceStable(evs, func(i, j int) bool { return evs[i].T < evs[j].T })
	p = append(p, checkRects(evs, s)...)
	if len(p) > 0 {
		return nil, p
	}
	l := &Log{Meta: meta, End: endT}
	for _, e := range evs {
		l.Events = append(l.Events, e.Event)
	}
	return l, nil
}

// actionOf maps an action event type to the script action it records.
var actionOf = map[string]string{"tap": "tap", "long_press": "long_press", "swipe": "swipe", "drag": "drag", "type": "type", "key": "key"}

func checkStep(bad func(string, ...any), s *Script, typ string, step int) bool {
	if step < 0 || step >= len(s.Steps) {
		bad("%s: step %d is out of range (script has %d)", typ, step, len(s.Steps))
		return false
	}
	if a := s.Steps[step].Action; a != actionOf[typ] {
		bad("step %d is a %s, not a %s", step, a, typ)
		return false
	}
	return true
}

func isSystem(s *Script, name string) bool { return s != nil && s.Targets[name].System != "" }

func parseMeta(n int, text []byte, keys map[string]json.RawMessage, s *Script) (Meta, Problems) {
	var p Problems
	bad := func(format string, a ...any) { p = append(p, fmt.Sprintf("line %d: %s", n, fmt.Sprintf(format, a...))) }
	for k := range keys {
		if !slices.Contains(metaFields, k) {
			bad("meta: unknown field %q", k)
		}
	}
	var m Meta
	if err := json.Unmarshal(text, &m); err != nil {
		bad("meta: %v", err)
		return m, p
	}
	var screen []float64
	json.Unmarshal(keys["screen"], &screen)
	switch {
	case m.Version != 1:
		bad("meta.version: want 1")
	case m.Platform != s.Platform:
		bad("meta.platform %q differs from the script's %q", m.Platform, s.Platform)
	case m.FPS != *s.FPS:
		bad("meta.fps %d differs from the script's %d", m.FPS, *s.FPS)
	}
	if !(m.Scale > 0) || len(screen) != 2 || !(screen[0] > 0 && screen[1] > 0) {
		bad("meta: want scale > 0 and screen [w, h] in points")
	}
	sync, present := keys["sync_error_ms"]
	switch {
	case !present:
		bad("meta.sync_error_ms is missing: an uncalibrated log cannot be rendered")
	case m.SyncErrorMS == nil || string(bytes.TrimSpace(sync)) == "null":
		bad("meta.sync_error_ms is null: an uncalibrated log cannot be rendered")
	case *m.SyncErrorMS < 0:
		bad("meta.sync_error_ms %g is negative", *m.SyncErrorMS)
	case m.FPS > 0 && *m.SyncErrorMS > 1000/float64(m.FPS):
		bad("sync_error_ms %g exceeds one frame at %d fps (%.1f ms)", *m.SyncErrorMS, m.FPS, 1000/float64(m.FPS))
	}
	return m, p
}

// checkRects holds the earliest rects sample to the script: it must come at
// or before the first action and name the camera region and every pin.
func checkRects(evs []lineEvent, s *Script) Problems {
	firstAction := math.Inf(1)
	var first *lineEvent
	for i := range evs {
		if _, ok := actionOf[evs[i].Type]; ok && evs[i].T < firstAction {
			firstAction = evs[i].T
		}
		if evs[i].Type == "rects" && first == nil {
			first = &evs[i]
		}
	}
	if first == nil || first.T > firstAction {
		return Problems{"no rects sample before the first action"}
	}
	var p Problems
	need := []Target{}
	if s.Camera.Region != nil {
		need = append(need, *s.Camera.Region)
	}
	need = append(need, s.Camera.Pin...)
	seen := map[string]bool{}
	for _, t := range need {
		if _, ok := first.Rects[t.Name]; t.Name != "" && !ok && !seen[t.Name] {
			p = append(p, fmt.Sprintf("line %d: rects lacks %s", first.line, t.Name))
		}
		seen[t.Name] = true
	}
	return p
}

// CheckMaster holds the log to the master video it was recorded beside: the
// same screen size, and a last frame no earlier than the first action.
// simctl writes a frame only when the screen changes, so a master whose
// screen stood still probes as ending at its last change and the log's end
// is when recording stopped; but every action changes the screen, so a
// master over before the first one is cut short or another recording. One
// over before the last action passes with warn set: an action can change
// nothing on screen. A log with no action has no time to hold the master to.
func (l *Log) CheckMaster(m Master) (warn string, err error) {
	w := int(math.Round(l.Meta.Screen[0] * l.Meta.Scale))
	h := int(math.Round(l.Meta.Screen[1] * l.Meta.Scale))
	if abs(m.W-w) > 1 || abs(m.H-h) > 1 {
		return "", fmt.Errorf("master is %dx%d, events say %dx%d", m.W, m.H, w, h)
	}
	first, last := math.Inf(1), math.Inf(-1)
	for _, e := range l.Events {
		if _, ok := actionOf[e.Type]; ok {
			first, last = math.Min(first, e.T), math.Max(last, e.T)
		}
	}
	end := m.Duration - m.FirstPTS
	switch {
	case math.IsInf(first, 1):
	case end < first:
		return "", fmt.Errorf("the master's last frame is at %g ms, before the first action at %g ms: it is cut short or not this log's recording", end, first)
	case end < last:
		return fmt.Sprintf("the master's last frame is at %g ms, before the last action at %g ms; the screen holds still from there", end, last), nil
	}
	return "", nil
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}
