package clip

import (
	"reflect"
	"strings"
	"testing"
)

var contractLog = []string{
	`{"type":"meta","version":1,"platform":"ios","scale":3,"screen":[402,874],"fps":60,"sync":"simctl-start","sync_error_ms":8}`,
	`{"type":"rects","t":0,"rects":{"outline":[0,120,402,640],"touchbar":[0,812,402,62]}}`,
	`{"type":"tap","t":1520,"step":0,"at":[201,300],"target":[16,282,370,36]}`,
	`{"type":"type","t":2200,"step":1,"text":"Plan the garden","end":3870,"target":[16,282,370,36]}`,
	`{"type":"swipe","t":4300,"step":2,"from":[90,300],"to":[312,300],"target":[16,282,370,36],"duration":350}`,
	`{"type":"drag","t":5100,"step":3,"from":[201,300],"to":[201,180],"target":[16,282,370,36],"duration":600}`,
	`{"type":"key","t":6400,"step":5,"key":"return"}`,
	`{"type":"caption","t":1400,"text":"Tap to add a node"}`,
	`{"type":"caption","t":8000,"text":""}`,
	`{"type":"end","t":8200}`,
}

func parse(t *testing.T, script *Script, lines ...string) (*Log, error) {
	t.Helper()
	return ParseEvents(strings.NewReader(strings.Join(lines, "\n")+"\n"), script)
}

func with(lines []string, n int, repl string) []string {
	out := append([]string(nil), lines...)
	out[n-1] = repl
	return out
}

func TestParseContractExample(t *testing.T) {
	l, err := parse(t, load(t, contractExample), contractLog...)
	if err != nil {
		t.Fatal(err)
	}
	if len(l.Events) != 9 {
		t.Fatalf("events = %d, want 9", len(l.Events))
	}
	if l.Events[0].Type != "rects" || l.Events[0].T != 0 || l.Events[1].Type != "caption" || l.Events[1].T != 1400 {
		t.Fatalf("not sorted: %+v %+v", l.Events[0], l.Events[1])
	}
	for i := 1; i < len(l.Events); i++ {
		if l.Events[i].T < l.Events[i-1].T {
			t.Fatalf("event %d out of order", i)
		}
	}
	var drag, key, typ Event
	for _, e := range l.Events {
		switch e.Type {
		case "drag":
			drag = e
		case "key":
			key = e
		case "type":
			typ = e
		}
	}
	if drag.To == nil || *drag.To != (Point{201, 180}) || drag.From == nil || *drag.From != (Point{201, 300}) || drag.Duration != 600 {
		t.Fatalf("drag: %+v", drag)
	}
	if key.Key != "return" || key.Step == nil || *key.Step != 5 {
		t.Fatalf("key: %+v", key)
	}
	if typ.Text != "Plan the garden" || typ.End != 3870 || typ.Target == nil || *typ.Target != (Rect{16, 282, 370, 36}) {
		t.Fatalf("type: %+v", typ)
	}
	if l.End != 8200 || l.Meta.Scale != 3 || l.Meta.Screen != [2]float64{402, 874} || l.Meta.FPS != 60 || *l.Meta.SyncErrorMS != 8 {
		t.Fatalf("log: %+v", l)
	}
	if r := l.Events[0].Rects["touchbar"]; r == nil || *r != (Rect{0, 812, 402, 62}) {
		t.Fatalf("rects: %+v", l.Events[0].Rects)
	}
}

// minimal puts [[step]] last, so camera and targets must precede it.
func kbScriptBody() string {
	return strings.Replace(minimal, "[output]", "[camera]\nmode=\"follow\"\nregion={target=\"outline\"}\npin=[{target=\"kb\"}]\n[targets]\noutline={id=\"outline\"}\nkb={system=\"keyboard\"}\n[output]", 1)
}

const metaLine = `{"type":"meta","version":1,"platform":"ios","scale":3,"screen":[402,874],"fps":60,"sync":"x","sync_error_ms":8}`

func TestParseKeyboardNull(t *testing.T) {
	s := load(t, kbScriptBody())
	l, err := parse(t, s, metaLine,
		`{"type":"rects","t":0,"rects":{"outline":[0,0,402,600],"kb":null}}`,
		`{"type":"tap","t":100,"step":0,"at":[1,1]}`,
		`{"type":"rects","t":250,"rects":{"outline":[0,0,402,300],"kb":[0,560,402,314]}}`,
		`{"type":"end","t":500}`)
	if err != nil {
		t.Fatal(err)
	}
	if v, ok := l.Events[0].Rects["kb"]; !ok || v != nil {
		t.Fatalf("first kb = %v, %v", v, ok)
	}
	if r := l.Events[2].Rects["kb"]; r == nil || *r != (Rect{0, 560, 402, 314}) {
		t.Fatalf("second kb = %v", r)
	}
	_, err = parse(t, s, metaLine,
		`{"type":"rects","t":0,"rects":{"outline":null,"kb":null}}`,
		`{"type":"end","t":500}`)
	if err == nil || !strings.Contains(err.Error(), "rects: outline is null; only a system target may be") {
		t.Fatalf("err = %v", err)
	}
}

func TestParseXYActionWithoutTarget(t *testing.T) {
	l, err := parse(t, load(t, kbScriptBody()), metaLine,
		`{"type":"rects","t":0,"rects":{"outline":[0,0,402,600],"kb":null}}`,
		`{"type":"tap","t":100,"step":0,"at":[1,1]}`,
		`{"type":"end","t":500}`)
	if err != nil {
		t.Fatal(err)
	}
	if e := l.Events[1]; e.Target != nil || e.At == nil || *e.At != (Point{1, 1}) {
		t.Fatalf("tap: %+v", e)
	}
}

func TestParseActionFields(t *testing.T) {
	s := load(t, minimal+`
[[step]]
action = "long_press"
target = {label="A"}
[[step]]
action = "type"
text = "hi"
`)
	rects := `{"type":"rects","t":0,"rects":{}}`
	l, err := parse(t, s, metaLine, rects,
		`{"type":"tap","t":10,"step":0,"at":[5,6],"target":[1,2,3,4]}`,
		`{"type":"long_press","t":20,"step":1,"at":[5,6],"target":[1,2,3,4],"duration":800}`,
		`{"type":"type","t":30,"step":2,"text":"hi","end":90,"target":[1,2,3,4]}`,
		`{"type":"end","t":100}`)
	if err != nil {
		t.Fatal(err)
	}
	lp := l.Events[2]
	if lp.Type != "long_press" || *lp.At != (Point{5, 6}) || *lp.Target != (Rect{1, 2, 3, 4}) || lp.Duration != 800 {
		t.Fatalf("long_press: %+v", lp)
	}
	if e := l.Events[3]; e.Text != "hi" || e.End != 90 || *e.Target != (Rect{1, 2, 3, 4}) {
		t.Fatalf("type: %+v", e)
	}
	_, err = parse(t, s, metaLine, rects,
		`{"type":"long_press","t":20,"step":1,"at":[5,6],"from":[1,1],"duration":800}`,
		`{"type":"end","t":100}`)
	if err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("err = %v", err)
	}
}

func TestEventsSortedStably(t *testing.T) {
	l, err := parse(t, load(t, minimal), metaLine,
		`{"type":"rects","t":0,"rects":{}}`,
		`{"type":"caption","t":50,"text":"A"}`,
		`{"type":"caption","t":10,"text":"early"}`,
		`{"type":"caption","t":50,"text":"B"}`,
		`{"type":"end","t":100}`)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, e := range l.Events {
		if e.Type == "caption" {
			got = append(got, e.Text)
		}
	}
	if !reflect.DeepEqual(got, []string{"early", "A", "B"}) {
		t.Fatalf("captions = %v", got)
	}
}

func TestParseRefuses(t *testing.T) {
	sub := func(old, new string) []string {
		out := make([]string, len(contractLog))
		for i, l := range contractLog {
			out[i] = strings.Replace(l, old, new, 1)
		}
		return out
	}
	drop := func(n int) []string {
		return append(append([]string(nil), contractLog[:n-1]...), contractLog[n:]...)
	}
	rows := []struct {
		name  string
		lines []string
		want  string // "" means accepted
	}{
		{"sync 17", sub(`"sync_error_ms":8`, `"sync_error_ms":17`), "sync_error_ms 17 exceeds one frame at 60 fps (16.7 ms)"},
		{"sync 16.6", sub(`"sync_error_ms":8`, `"sync_error_ms":16.6`), ""},
		{"sync missing", sub(`,"sync_error_ms":8`, ``), "meta.sync_error_ms is missing: an uncalibrated log cannot be rendered"},
		{"sync null", sub(`"sync_error_ms":8`, `"sync_error_ms":null`), "meta.sync_error_ms is null: an uncalibrated log cannot be rendered"},
		{"sync raw", sub(`"sync":"simctl-start","sync_error_ms":8`, `"sync":"raw"`), "meta.sync_error_ms is missing"},
		{"no meta", drop(1), "line 1: want the meta header"},
		{"fps", sub(`"fps":60`, `"fps":30`), "meta.fps 30 differs from the script's 60"},
		{"platform", sub(`"platform":"ios"`, `"platform":"android"`), "meta.platform"},
		{"wrong step", with(contractLog, 4, `{"type":"tap","t":2200,"step":2,"at":[1,1]}`), "line 4: step 2 is a swipe, not a tap"},
		{"step range", with(contractLog, 4, `{"type":"tap","t":2200,"step":9,"at":[1,1]}`), "line 4: tap: step 9 is out of range"},
		{"wave", with(contractLog, 3, `{"type":"wave","t":1520}`), "line 3: unknown event type wave"},
		{"wait", with(contractLog, 3, `{"type":"wait","t":1520,"step":4}`), "line 3: unknown event type wait"},
		{"extra", sub(`"key":"return"`, `"key":"return","foo":1`), "unknown field"},
		{"no end", drop(10), "no end event"},
		{"two ends", append(append([]string(nil), contractLog...), `{"type":"end","t":8300}`), "end given twice"},
		{"after end", with(contractLog, 9, `{"type":"caption","t":9000,"text":""}`), "after end"},
		{"empty target", with(contractLog, 3, `{"type":"tap","t":1520,"step":0,"at":[1,1],"target":[16,282,0,36]}`), "empty target rect"},
		{"no rects", drop(2), "no rects sample before the first action"},
		{"rects late", with(contractLog, 2, `{"type":"rects","t":3000,"rects":{"outline":[0,120,402,640],"touchbar":[0,812,402,62]}}`), "no rects sample before the first action"},
		{"rects lacks", with(contractLog, 2, `{"type":"rects","t":0,"rects":{"touchbar":[0,812,402,62]}}`), "rects lacks outline"},
	}
	s := load(t, contractExample)
	for _, r := range rows {
		t.Run(r.name, func(t *testing.T) {
			_, err := parse(t, s, r.lines...)
			switch {
			case r.want == "" && err != nil:
				t.Fatalf("want accepted, got %v", err)
			case r.want != "" && (err == nil || !strings.Contains(err.Error(), r.want)):
				t.Fatalf("err = %v, want %q", err, r.want)
			}
		})
	}
}
