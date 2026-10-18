package clip

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const contractExample = `version  = 1
name     = "adding-nodes"
platform = "ios"
form     = "mobile"
app      = "com.example.notes"
device   = "iPhone 17 Pro"
state    = "adding-nodes"
fps      = 60

[output]
width     = 1080
formats   = ["mp4", "webm", "poster"]
budget_kb = 600

[camera]
mode     = "follow"
region   = { target = "outline" }
pin      = [{ target = "touchbar" }]
max_zoom = 1.0

[overlays]
captions = true
taps     = true
keys     = false
cursor   = false

[[step]]
action  = "tap"
target  = { label = "New node" }
caption = "Tap to add a node"
hold_ms = 600

[[step]]
action = "type"
text   = "Plan the garden"
cps    = 9

[[step]]
action    = "swipe"
target    = { label = "Plan the garden" }
direction = "right"
distance  = 0.6
duration_ms = 350

[[step]]
action     = "drag"
target     = { label = "Plan the garden" }
to         = { label = "Projects" }
duration_ms = 600

[[step]]
action     = "wait"
target     = { label = "Search" }
timeout_ms = 5000

[[step]]
action  = "key"
key     = "return"

[[step]]
action  = "pause"
hold_ms = 1200

[targets]
outline  = { id = "outline" }
touchbar = { id = "touchbar" }
`

const minimal = `version = 1
name = "a"
platform = "ios"
form = "mobile"
app = "com.example.notes"
device = "iPhone"
state = "s"
[output]
width = 600
[[step]]
action = "tap"
target = {label="A"}
`

func write(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "clip.toml")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func load(t *testing.T, body string) *Script {
	t.Helper()
	s, err := Load(write(t, body))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestLoadContractExample(t *testing.T) {
	s := load(t, contractExample)
	if *s.FPS != 60 || *s.Steps[0].HoldMS != 600 || *s.Steps[1].CPS != 9 || *s.Steps[2].DurationMS != 350 {
		t.Fatalf("scalars: %+v", s.Steps)
	}
	if !reflect.DeepEqual(*s.Steps[3].To, Target{Label: "Projects"}) || *s.Steps[3].DurationMS != 600 {
		t.Fatalf("drag: %+v", s.Steps[3])
	}
	if *s.Steps[4].TimeoutMS != 5000 || s.Steps[5].Key != "return" || *s.Steps[6].HoldMS != 1200 {
		t.Fatalf("steps: %+v", s.Steps)
	}
	if !reflect.DeepEqual(s.Camera.Region, &Target{ID: "outline", Name: "outline"}) {
		t.Fatalf("region: %+v", s.Camera.Region)
	}
	if !reflect.DeepEqual(s.Camera.Pin[0], Target{ID: "touchbar", Name: "touchbar"}) {
		t.Fatalf("pin: %+v", s.Camera.Pin[0])
	}
}

func TestLoadDefaults(t *testing.T) {
	s := load(t, minimal+`
[[step]]
action = "long_press"
target = {label="A"}
[[step]]
action = "swipe"
target = {label="A"}
direction = "left"
[[step]]
action = "drag"
target = {label="A"}
to = {label="B"}
[[step]]
action = "type"
text = "x"
[[step]]
action = "wait"
target = {label="A"}
`)
	if *s.FPS != 60 || s.Camera.Mode != "full" || *s.Camera.MaxZoom != 1.0 ||
		!reflect.DeepEqual(s.Output.Formats, []string{"mp4"}) || s.Overlays != (Overlays{}) || s.Output.BudgetKB != 0 {
		t.Fatalf("script defaults: %+v", s)
	}
	for i, st := range s.Steps {
		if *st.HoldMS != 400 {
			t.Errorf("step %d hold %d", i, *st.HoldMS)
		}
	}
	if *s.Steps[1].DurationMS != 800 || *s.Steps[2].DurationMS != 300 || *s.Steps[3].DurationMS != 600 ||
		*s.Steps[2].Distance != 0.5 || *s.Steps[4].CPS != 9 || *s.Steps[5].TimeoutMS != 5000 {
		t.Fatalf("step defaults: %+v", s.Steps)
	}
}

func TestLoadRefuses(t *testing.T) {
	step := func(body string) string {
		return strings.Replace(minimal, "[[step]]\naction = \"tap\"\ntarget = {label=\"A\"}\n", body, 1)
	}
	sub := func(old, new string) string { return strings.Replace(minimal, old, new, 1) }
	steps := func(s string) string { return step("[[step]]\n" + s + "\n") }
	mobileKey := func(k string) string { return steps("action = \"key\"\nkey = \"" + k + "\"") }
	desktop := func(b string) string {
		return strings.Replace(strings.Replace(b, `platform = "ios"`, `platform = "macos"`, 1), `form = "mobile"`, `form = "desktop"`, 1)
	}
	tgt := "\n[targets]\nkb = {system=\"keyboard\"}\n"
	rows := []struct{ name, toml, want string }{
		{"unknown", "extra = 1\n" + minimal, "unknown key extra"},
		{"name", sub(`name = "a"`, `name = "Adding_Nodes"`), "name: want [a-z0-9-]+"},
		{"version", sub("version = 1", "version = 2"), "version: want 1"},
		{"adapter", desktop(minimal), "platform macos: no v1 adapter (v1: ios)"},
		{"ios desktop", sub(`form = "mobile"`, `form = "desktop"`), "form desktop: platform ios requires form mobile"},
		{"macos mobile", sub(`platform = "ios"`, `platform = "macos"`), "form mobile: platform macos requires form desktop"},
		{"zoom hi", sub("[output]", "[camera]\nmax_zoom = 1.5\n[output]"), "camera.max_zoom: must be > 0 and <= 1.0"},
		{"zoom 0", sub("[output]", "[camera]\nmax_zoom = 0\n[output]"), "camera.max_zoom: must be > 0 and <= 1.0"},
		{"fps 0", sub("version = 1", "version = 1\nfps = 0"), "fps: want an integer 1-120"},
		{"fps 121", sub("version = 1", "version = 1\nfps = 121"), "fps: want an integer 1-120"},
		{"pin full", sub("[output]", "[camera]\nmode=\"full\"\npin=[{target=\"touchbar\"}]\n[output]") + "[targets]\ntouchbar={id=\"t\"}\n", "camera.pin: only with fixed or follow"},
		{"two kinds", steps("action = \"tap\"\ntarget = {label=\"A\", id=\"b\"}"), "step[0].target: give exactly one of label, id, xy, target"},
		{"missing named", steps("action = \"tap\"\ntarget = {target=\"nope\"}"), "step[0].target: no [targets] entry nope"},
		{"region label", sub("[output]", "[camera]\nmode=\"fixed\"\nregion={label=\"X\"}\n[output]"), "camera.region: must name a [targets] entry"},
		{"fixed no region", sub("[output]", "[camera]\nmode=\"fixed\"\n[output]"), "camera.region: required for fixed"},
		{"width odd", sub("width = 600", "width = 601"), "output.width: must be an even number of px, at least 16"},
		{"format unknown", sub("width = 600", "width = 600\nformats=[\"mp4\",\"mov\"]"), "output.formats: unknown format mov"},
		{"format dup", sub("width = 600", "width = 600\nformats=[\"mp4\",\"mp4\"]"), "listed twice"},
		{"budget", sub("width = 600", "width = 600\nbudget_kb=600\nformats=[\"webm\"]"), "output.budget_kb: applies to mp4"},
		{"cursor mobile", sub("[output]", "[overlays]\ncursor=true\n[output]"), "overlays.cursor: refused for form mobile"},
		{"keys mobile", sub("[output]", "[overlays]\nkeys=true\n[output]"), "overlays.keys: refused for form mobile"},
		{"tap no target", steps("action = \"tap\""), "step[0].target: required for tap"},
		{"long_press no target", steps("action = \"long_press\""), "step[0].target: required for long_press"},
		{"swipe no target", steps("action = \"swipe\"\ndirection=\"up\""), "step[0].target: required for swipe"},
		{"drag no target", steps("action = \"drag\"\nto={label=\"B\"}"), "step[0].target: required for drag"},
		{"tap direction", steps("action = \"tap\"\ntarget={label=\"A\"}\ndirection=\"left\""), "step[0].direction: not used by tap"},
		{"swipe no dir", steps("action = \"swipe\"\ntarget={label=\"A\"}"), "step[0].direction: required for swipe"},
		{"swipe distance", steps("action = \"swipe\"\ntarget={label=\"A\"}\ndirection=\"up\"\ndistance=1.5"), "step[0].distance: want a fraction in (0, 1]"},
		{"drag no to", steps("action = \"drag\"\ntarget={label=\"A\"}"), "step[0].to: required for drag"},
		{"drag direction", steps("action = \"drag\"\ntarget={label=\"A\"}\nto={label=\"B\"}\ndirection=\"up\""), "step[0].direction: not used by drag"},
		{"drag to two", steps("action = \"drag\"\ntarget={label=\"A\"}\nto={label=\"A\", id=\"b\"}"), "step[0].to: give exactly one of label, id, xy, target"},
		{"type target", steps("action = \"type\"\ntext=\"x\"\ntarget={label=\"A\"}"), "step[0].target: not used by type"},
		{"wait no target", steps("action = \"wait\""), "step[0].target: required for wait"},
		{"wait timeout", steps("action = \"wait\"\ntarget={label=\"A\"}\ntimeout_ms=0"), "step[0].timeout_ms: must be > 0"},
		{"pause target", steps("action = \"pause\"\ntarget={label=\"A\"}"), "step[0].target: not used by pause"},
		{"key none", steps("action = \"key\""), "step[0].key: required for key"},
		{"key cmd", mobileKey("cmd+z"), "step[0].key: want one of return, delete, tab, escape, space, up, down, left, right"},
		{"key tab", mobileKey("tab"), "step[0].key: tab is not on a phone's soft keyboard (form mobile: return, delete, space)"},
		{"key escape", mobileKey("escape"), "step[0].key: escape is not on a phone's soft keyboard"},
		{"key up", mobileKey("up"), "step[0].key: up is not on a phone's soft keyboard"},
		{"key down", mobileKey("down"), "step[0].key: down is not on a phone's soft keyboard"},
		{"key left", mobileKey("left"), "step[0].key: left is not on a phone's soft keyboard"},
		{"key right", mobileKey("right"), "step[0].key: right is not on a phone's soft keyboard"},
		{"type empty", steps("action = \"type\"\ntext=\"\""), "step[0].text: required for type"},
		{"xy", steps("action = \"tap\"\ntarget={xy=[1]}"), "xy: want [x, y]"},
		{"target names target", sub("[output]", "[targets]\na={target=\"b\"}\n[output]"), "targets.a: may not name another target"},
		{"system mouse", minimal + "[targets]\nkb={system=\"mouse\"}\n", "targets.kb: system must be keyboard"},
		{"system in step", steps("action = \"tap\"\ntarget={target=\"kb\"}") + tgt, "step[0].target: kb is a system target, usable only as a camera pin"},
		{"system in region", sub("[output]", "[camera]\nmode=\"fixed\"\nregion={target=\"kb\"}\n[output]") + tgt, "camera.region: kb is a system target, usable only as a camera pin"},
		{"xy region", sub("[output]", "[camera]\nmode=\"fixed\"\nregion={target=\"p\"}\n[output]") + "\n[targets]\np = {xy=[10, 20]}\n", "camera.region: p is an xy target: a point has no rect to sample"},
		{"xy pin", sub("[output]", "[camera]\nmode=\"follow\"\npin=[{target=\"p\"}]\n[output]") + "\n[targets]\np = {xy=[10, 20]}\n", "camera.pin[0]: p is an xy target: a point has no rect to sample"},
		{"system inline", steps("action = \"tap\"\ntarget={system=\"keyboard\"}"), "step[0].target: system targets belong in [targets]"},
	}
	for _, r := range rows {
		t.Run(r.name, func(t *testing.T) {
			_, err := Load(write(t, r.toml))
			var p Problems
			if !errors.As(err, &p) {
				t.Fatalf("err = %v, want Problems containing %q", err, r.want)
			}
			if !strings.Contains(err.Error(), r.want) {
				t.Fatalf("got %v\nwant %q", err, r.want)
			}
		})
	}

	t.Run("desktop overlays accepted", func(t *testing.T) {
		_, err := Load(write(t, desktop(sub("[output]", "[overlays]\ncursor=true\nkeys=true\n[output]"))))
		if err == nil || strings.Contains(err.Error(), "overlays") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("desktop tab accepted", func(t *testing.T) {
		_, err := Load(write(t, desktop(steps("action = \"key\"\nkey = \"tab\""))))
		if err == nil || strings.Contains(err.Error(), "step[0].key") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("keyboard pin accepted", func(t *testing.T) {
		load(t, sub("[output]", "[camera]\nmode=\"follow\"\npin=[{target=\"kb\"}]\n[output]")+tgt)
	})
	t.Run("mobile keys accepted", func(t *testing.T) {
		for _, k := range []string{"return", "delete", "space"} {
			load(t, mobileKey(k))
		}
	})
}

func TestLoadCollectsEveryProblem(t *testing.T) {
	body := strings.Replace(strings.Replace(minimal, `name = "a"`, `name = "A"`, 1), "width = 600", "width = 601\nformats=[\"mov\"]", 1)
	_, err := Load(write(t, body))
	var p Problems
	if !errors.As(err, &p) || len(p) != 3 {
		t.Fatalf("got %v", err)
	}
}

func TestJSONIsNormalised(t *testing.T) {
	body := strings.Replace(contractExample, "pin      = [{ target = \"touchbar\" }]", "pin      = [{ target = \"touchbar\" }, { target = \"kb\" }]", 1) + "kb = { system = \"keyboard\" }\n"
	b, err := load(t, body).JSON()
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	steps := m["step"].([]any)
	s0 := steps[0].(map[string]any)
	if !reflect.DeepEqual(s0["target"], map[string]any{"label": "New node"}) || s0["hold_ms"] != 600.0 {
		t.Fatalf("step0 %v", s0)
	}
	if _, ok := s0["duration_ms"]; ok {
		t.Fatal("step0 has duration_ms")
	}
	if !reflect.DeepEqual(steps[3].(map[string]any)["to"], map[string]any{"label": "Projects"}) || steps[4].(map[string]any)["timeout_ms"] != 5000.0 {
		t.Fatalf("steps %v", steps)
	}
	cam := m["camera"].(map[string]any)
	if !reflect.DeepEqual(cam["region"], map[string]any{"id": "outline", "name": "outline"}) {
		t.Fatalf("region %v", cam["region"])
	}
	if !reflect.DeepEqual(cam["pin"].([]any)[1], map[string]any{"system": "keyboard", "name": "kb"}) {
		t.Fatalf("pin %v", cam["pin"])
	}
	tg := m["targets"].(map[string]any)
	if !reflect.DeepEqual(tg["outline"], map[string]any{"id": "outline"}) || !reflect.DeepEqual(tg["kb"], map[string]any{"system": "keyboard"}) {
		t.Fatalf("targets %v", tg)
	}
	if !reflect.DeepEqual(m["output"].(map[string]any)["formats"], []any{"mp4", "webm", "poster"}) {
		t.Fatalf("formats %v", m["output"])
	}
}

func TestCheckMain(t *testing.T) {
	run := func(args ...string) (int, string, string) {
		var o, e bytes.Buffer
		c := Main(context.Background(), args, &o, &e)
		return c, o.String(), e.String()
	}
	good := write(t, contractExample)
	if c, o, _ := run("check", good); c != 0 || o != "ok adding-nodes\n" {
		t.Fatalf("ok: %d %q", c, o)
	}
	if c, o, _ := run("check", "--json", good); c != 0 || !json.Valid([]byte(o)) {
		t.Fatalf("json: %d %q", c, o)
	}
	if c, _, e := run("check", write(t, "extra = 1\n"+minimal)); c != 1 || !strings.HasPrefix(e, "plate clip check: ") {
		t.Fatalf("bad: %d %q", c, e)
	}
	if c, _, _ := run("check"); c != 2 {
		t.Fatalf("no file: %d", c)
	}
	if c, _, _ := run("-h"); c != 0 {
		t.Fatalf("-h: %d", c)
	}
}
