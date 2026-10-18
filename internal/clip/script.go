package clip

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"
)

// Target names one thing on screen: exactly one of Label (accessibility
// label), ID (accessibility id), XY (points) or Name (a [targets] entry),
// or in [targets] alone System. Load expands a Name inline and keeps it.
type Target struct {
	Label  string    `toml:"label" json:"label,omitempty"`
	ID     string    `toml:"id" json:"id,omitempty"`
	XY     []float64 `toml:"xy" json:"xy,omitempty"`
	System string    `toml:"system" json:"system,omitempty"`
	Name   string    `toml:"target" json:"name,omitempty"`
}

// Step is one scripted action. Pointer fields tell an absent key from zero;
// stepFields says which fields each action takes.
type Step struct {
	Action     string   `toml:"action" json:"action"`
	Target     *Target  `toml:"target" json:"target,omitempty"`
	To         *Target  `toml:"to" json:"to,omitempty"`
	Caption    *string  `toml:"caption" json:"caption,omitempty"`
	HoldMS     *int     `toml:"hold_ms" json:"hold_ms,omitempty"`
	Text       string   `toml:"text" json:"text,omitempty"`
	CPS        *float64 `toml:"cps" json:"cps,omitempty"`
	Direction  string   `toml:"direction" json:"direction,omitempty"`
	Distance   *float64 `toml:"distance" json:"distance,omitempty"`
	DurationMS *int     `toml:"duration_ms" json:"duration_ms,omitempty"`
	TimeoutMS  *int     `toml:"timeout_ms" json:"timeout_ms,omitempty"`
	Key        string   `toml:"key" json:"key,omitempty"`
}

// Keys are the keys a desktop script may send; MobileKeys the ones a
// phone's soft keyboard has.
var (
	Keys       = []string{"return", "delete", "tab", "escape", "space", "up", "down", "left", "right"}
	MobileKeys = []string{"return", "delete", "space"}
)

// Adapters are the platforms a driver exists for; Formats the outputs a
// render can write.
var (
	Adapters = []string{"ios"}
	Formats  = []string{"mp4", "webm", "poster", "webp", "gif"}
)

// Ext is the file extension a format is written with.
func Ext(format string) string {
	if format == "poster" {
		return "poster.png"
	}
	return format
}

type Output struct {
	Width    int      `toml:"width" json:"width"`
	Formats  []string `toml:"formats" json:"formats"`
	BudgetKB int      `toml:"budget_kb" json:"budget_kb,omitempty"`
}

type Camera struct {
	Mode    string   `toml:"mode" json:"mode"`
	Region  *Target  `toml:"region" json:"region,omitempty"`
	Pin     []Target `toml:"pin" json:"pin,omitempty"`
	MaxZoom *float64 `toml:"max_zoom" json:"max_zoom"`
}

type Overlays struct {
	Captions bool `toml:"captions" json:"captions"`
	Taps     bool `toml:"taps" json:"taps"`
	Keys     bool `toml:"keys" json:"keys"`
	Cursor   bool `toml:"cursor" json:"cursor"`
}

// Script is a demo-clip script, version 1.
type Script struct {
	Version  int               `toml:"version" json:"version"`
	Name     string            `toml:"name" json:"name"`
	Platform string            `toml:"platform" json:"platform"`
	Form     string            `toml:"form" json:"form"`
	App      string            `toml:"app" json:"app"`
	Device   string            `toml:"device" json:"device,omitempty"`
	State    string            `toml:"state" json:"state"`
	FPS      *int              `toml:"fps" json:"fps"`
	Output   Output            `toml:"output" json:"output"`
	Camera   Camera            `toml:"camera" json:"camera"`
	Overlays Overlays          `toml:"overlays" json:"overlays"`
	Steps    []Step            `toml:"step" json:"step"`
	Targets  map[string]Target `toml:"targets" json:"targets"`
}

// Problems is every fault a script has, each led by its key path.
type Problems []string

func (p Problems) Error() string {
	return fmt.Sprintf("%d problems:\n  %s", len(p), strings.Join(p, "\n  "))
}

// stepFields is the fields each action takes, beyond the caption and
// hold_ms every action takes.
var stepFields = map[string][]string{
	"tap":        {"target"},
	"long_press": {"target", "duration_ms"},
	"swipe":      {"target", "direction", "distance", "duration_ms"},
	"drag":       {"target", "to", "duration_ms"},
	"type":       {"text", "cps"},
	"key":        {"key"},
	"wait":       {"target", "timeout_ms"},
	"pause":      {},
}

var actions = []string{"tap", "long_press", "swipe", "drag", "type", "key", "wait", "pause"}

var optionalFields = []string{"target", "to", "direction", "distance", "duration_ms", "text", "cps", "key", "timeout_ms"}

var (
	nameRE     = regexp.MustCompile(`^[a-z0-9-]+$`)
	directions = []string{"left", "right", "up", "down"}
)

// Load reads, validates, defaults and expands the script at path.
func Load(path string) (*Script, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var s Script
	md, err := toml.Decode(string(raw), &s)
	if err != nil {
		return nil, Problems{err.Error()}
	}
	var p Problems
	for _, k := range md.Undecoded() {
		p = append(p, "unknown key "+k.String())
	}
	p = append(p, s.validate()...)
	if len(p) > 0 {
		return nil, p
	}
	s.fillDefaults()
	s.expand()
	return &s, nil
}

// JSON is the normalised script: the form a runner reads.
func (s *Script) JSON() ([]byte, error) {
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

type refCtx int

const (
	ctxStep refCtx = iota
	ctxRegion
	ctxPin
	ctxTargets
)

func (s *Script) validate() Problems {
	var p Problems
	add := func(format string, a ...any) { p = append(p, fmt.Sprintf(format, a...)) }

	if s.Version != 1 {
		add("version: want 1")
	}
	if !nameRE.MatchString(s.Name) {
		add("name: want [a-z0-9-]+")
	}
	if !slices.Contains([]string{"ios", "macos", "android"}, s.Platform) {
		add("platform: want ios, macos or android")
	} else if !slices.Contains(Adapters, s.Platform) {
		add("platform %s: no v1 adapter (v1: %s)", s.Platform, strings.Join(Adapters, ", "))
	}
	if !slices.Contains([]string{"mobile", "desktop"}, s.Form) {
		add("form: want mobile or desktop")
	} else if s.Platform == "macos" && s.Form != "desktop" {
		add("form %s: platform macos requires form desktop", s.Form)
	} else if (s.Platform == "ios" || s.Platform == "android") && s.Form != "mobile" {
		add("form %s: platform %s requires form mobile", s.Form, s.Platform)
	}
	if s.App == "" {
		add("app: required")
	}
	if s.State == "" {
		add("state: required")
	}
	if s.Device == "" && (s.Platform == "ios" || s.Platform == "android") {
		add("device: required for %s", s.Platform)
	}
	if s.FPS != nil && (*s.FPS < 1 || *s.FPS > 120) {
		add("fps: want an integer 1-120")
	}

	if s.Output.Width < 16 || s.Output.Width%2 != 0 {
		add("output.width: must be an even number of px, at least 16")
	}
	seen := map[string]bool{}
	for _, f := range s.Output.Formats {
		switch {
		case !slices.Contains(Formats, f):
			add("output.formats: unknown format %s", f)
		case seen[f]:
			add("output.formats: %s listed twice", f)
		}
		seen[f] = true
	}
	formats := s.Output.Formats
	if len(formats) == 0 {
		formats = []string{"mp4"}
	}
	if s.Output.BudgetKB < 0 {
		add("output.budget_kb: must be > 0")
	} else if s.Output.BudgetKB > 0 && !slices.Contains(formats, "mp4") {
		add("output.budget_kb: applies to mp4, which formats does not list")
	}

	mode := s.Camera.Mode
	if mode == "" {
		mode = "full"
	}
	if !slices.Contains([]string{"full", "fixed", "follow"}, mode) {
		add("camera.mode: want full, fixed or follow")
	}
	if z := s.Camera.MaxZoom; z != nil && !(*z > 0 && *z <= 1.0) {
		add("camera.max_zoom: must be > 0 and <= 1.0")
	}
	if mode == "fixed" && s.Camera.Region == nil {
		add("camera.region: required for fixed")
	}
	if s.Camera.Region != nil {
		p = append(p, s.validateRef("camera.region", s.Camera.Region, ctxRegion)...)
	}
	if len(s.Camera.Pin) > 0 && mode != "fixed" && mode != "follow" {
		add("camera.pin: only with fixed or follow")
	} else {
		for i := range s.Camera.Pin {
			p = append(p, s.validateRef(fmt.Sprintf("camera.pin[%d]", i), &s.Camera.Pin[i], ctxPin)...)
		}
	}

	if s.Form == "mobile" {
		if s.Overlays.Cursor {
			add("overlays.cursor: refused for form mobile")
		}
		if s.Overlays.Keys {
			add("overlays.keys: refused for form mobile")
		}
	}

	names := make([]string, 0, len(s.Targets))
	for n := range s.Targets {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		t := s.Targets[n]
		p = append(p, s.validateRef("targets."+n, &t, ctxTargets)...)
	}

	if len(s.Steps) == 0 {
		add("step: at least one step is required")
	}
	for i := range s.Steps {
		p = append(p, s.validateStep(i, &s.Steps[i])...)
	}
	return p
}

// validateRef refuses a target that names no [targets] entry, a system target
// outside a camera pin, and an xy target anywhere but a step: a point has no
// rect to sample as a camera region or pin.
func (s *Script) validateRef(path string, t *Target, ctx refCtx) Problems {
	var p Problems
	add := func(format string, a ...any) { p = append(p, path+": "+fmt.Sprintf(format, a...)) }
	if t.XY != nil && len(t.XY) != 2 {
		p = append(p, path+".xy: want [x, y]")
	}
	kinds := 0
	for _, set := range []bool{t.Label != "", t.ID != "", t.XY != nil} {
		if set {
			kinds++
		}
	}

	if ctx == ctxTargets {
		switch {
		case t.Name != "":
			add("may not name another target")
		case t.System != "":
			if t.System != "keyboard" {
				add("system must be keyboard")
			} else if kinds > 0 {
				add("give exactly one of label, id, xy, system")
			}
		case kinds != 1:
			add("give exactly one of label, id, xy")
		}
		return p
	}

	if t.System != "" {
		if ctx == ctxStep {
			add("system targets belong in [targets]")
		} else {
			add("must name a [targets] entry")
		}
		return p
	}
	if t.Name == "" {
		if ctx != ctxStep {
			add("must name a [targets] entry")
		} else if kinds != 1 {
			add("give exactly one of label, id, xy, target")
		}
		return p
	}
	if kinds > 0 {
		add("give exactly one of label, id, xy, target")
		return p
	}
	entry, ok := s.Targets[t.Name]
	switch {
	case !ok:
		add("no [targets] entry %s", t.Name)
	case entry.System != "" && ctx != ctxPin:
		add("%s is a system target, usable only as a camera pin", t.Name)
	case entry.XY != nil && ctx != ctxStep:
		add("%s is an xy target: a point has no rect to sample", t.Name)
	}
	return p
}

func (s *Script) validateStep(i int, st *Step) Problems {
	var p Problems
	at := func(field, format string, a ...any) {
		p = append(p, fmt.Sprintf("step[%d].%s: %s", i, field, fmt.Sprintf(format, a...)))
	}
	allowed, ok := stepFields[st.Action]
	if !ok {
		at("action", "want one of %s", strings.Join(actions, ", "))
		return p
	}
	present := map[string]bool{
		"target": st.Target != nil, "to": st.To != nil, "direction": st.Direction != "",
		"distance": st.Distance != nil, "duration_ms": st.DurationMS != nil, "text": st.Text != "",
		"cps": st.CPS != nil, "key": st.Key != "", "timeout_ms": st.TimeoutMS != nil,
	}
	for _, f := range optionalFields {
		if present[f] && !slices.Contains(allowed, f) {
			at(f, "not used by %s", st.Action)
		}
	}
	require := func(f string) bool {
		if !present[f] {
			at(f, "required for %s", st.Action)
			return false
		}
		return true
	}
	if st.HoldMS != nil && *st.HoldMS < 0 {
		at("hold_ms", "must be >= 0")
	}
	if slices.Contains(allowed, "target") {
		if require("target") {
			p = append(p, s.validateRef(fmt.Sprintf("step[%d].target", i), st.Target, ctxStep)...)
		}
	}
	if slices.Contains(allowed, "to") {
		if require("to") {
			p = append(p, s.validateRef(fmt.Sprintf("step[%d].to", i), st.To, ctxStep)...)
		}
	}
	if slices.Contains(allowed, "duration_ms") && st.DurationMS != nil && *st.DurationMS <= 0 {
		at("duration_ms", "must be > 0")
	}
	if slices.Contains(allowed, "timeout_ms") && st.TimeoutMS != nil && *st.TimeoutMS <= 0 {
		at("timeout_ms", "must be > 0")
	}
	if slices.Contains(allowed, "direction") && require("direction") && !slices.Contains(directions, st.Direction) {
		at("direction", "want one of %s", strings.Join(directions, ", "))
	}
	if slices.Contains(allowed, "distance") && st.Distance != nil && !(*st.Distance > 0 && *st.Distance <= 1) {
		at("distance", "want a fraction in (0, 1]")
	}
	if slices.Contains(allowed, "text") {
		require("text")
	}
	if slices.Contains(allowed, "cps") && st.CPS != nil && !(*st.CPS > 0 && !math.IsInf(*st.CPS, 0)) {
		at("cps", "must be > 0")
	}
	if slices.Contains(allowed, "key") && require("key") {
		switch {
		case !slices.Contains(Keys, st.Key):
			at("key", "want one of %s", strings.Join(Keys, ", "))
		case s.Form == "mobile" && !slices.Contains(MobileKeys, st.Key):
			at("key", "%s is not on a phone's soft keyboard (form mobile: %s)", st.Key, strings.Join(MobileKeys, ", "))
		}
	}
	return p
}

func (s *Script) fillDefaults() {
	if s.FPS == nil {
		s.FPS = ptr(60)
	}
	if s.Camera.Mode == "" {
		s.Camera.Mode = "full"
	}
	if s.Camera.MaxZoom == nil {
		s.Camera.MaxZoom = ptr(1.0)
	}
	if len(s.Output.Formats) == 0 {
		s.Output.Formats = []string{"mp4"}
	}
	for i := range s.Steps {
		st := &s.Steps[i]
		if st.HoldMS == nil {
			st.HoldMS = ptr(400)
		}
		switch st.Action {
		case "long_press":
			st.DurationMS = orInt(st.DurationMS, 800)
		case "swipe":
			st.DurationMS = orInt(st.DurationMS, 300)
			if st.Distance == nil {
				st.Distance = ptr(0.5)
			}
		case "drag":
			st.DurationMS = orInt(st.DurationMS, 600)
		case "type":
			if st.CPS == nil {
				st.CPS = ptr(9.0)
			}
		case "wait":
			st.TimeoutMS = orInt(st.TimeoutMS, 5000)
		}
	}
}

// expand inlines each named target, keeping its name.
func (s *Script) expand() {
	if s.Targets == nil {
		s.Targets = map[string]Target{}
	}
	named := func(t *Target) *Target {
		if t == nil || t.Name == "" {
			return t
		}
		e := s.Targets[t.Name]
		e.Name = t.Name
		return &e
	}
	s.Camera.Region = named(s.Camera.Region)
	for i := range s.Camera.Pin {
		s.Camera.Pin[i] = *named(&s.Camera.Pin[i])
	}
	for i := range s.Steps {
		s.Steps[i].Target = named(s.Steps[i].Target)
		s.Steps[i].To = named(s.Steps[i].To)
	}
}

func ptr[T any](v T) *T { return &v }

func orInt(v *int, d int) *int {
	if v != nil {
		return v
	}
	return &d
}
