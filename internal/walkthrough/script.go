// Package walkthrough checks a walkthrough script: a scripted tour of a web
// app that is recorded in headless Chrome and narrated.
package walkthrough

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/BurntSushi/toml"

	"github.com/lockyc/plate/internal/clip"
)

// Step is one scripted action; stepFields says which fields each action takes.
type Step struct {
	Action    string `toml:"action" json:"action"`
	Selector  string `toml:"selector" json:"selector,omitempty"`
	URL       string `toml:"url" json:"url,omitempty"`
	Text      string `toml:"text" json:"text,omitempty"`
	Key       string `toml:"key" json:"key,omitempty"`
	Caption   string `toml:"caption" json:"caption,omitempty"`
	Audio     string `toml:"audio" json:"audio,omitempty"` // relative to the script
	TimeoutMS *int   `toml:"timeout_ms" json:"timeout_ms"`
}

// Script is a walkthrough script, version 1.
type Script struct {
	Version int    `toml:"version" json:"version"`
	Name    string `toml:"name" json:"name"`
	Width   int    `toml:"width" json:"width"`
	Height  int    `toml:"height" json:"height"`
	FPS     *int   `toml:"fps" json:"fps"`
	Steps   []Step `toml:"step" json:"step"`
}

// Defaults for omitted keys.
const (
	DefaultWidth     = 1440
	DefaultHeight    = 900
	DefaultFPS       = 30
	DefaultTimeoutMS = 10000
)

// Problems is every fault a script has, each led by its key path.
type Problems []string

func (p Problems) Error() string {
	noun := "problems"
	if len(p) == 1 {
		noun = "problem"
	}
	return fmt.Sprintf("%d %s:\n  %s", len(p), noun, strings.Join(p, "\n  "))
}

var actions = []string{"goto", "click", "type", "press", "wait_for"}

// stepFields is the content fields each action requires.
var stepFields = map[string][]string{
	"goto":     {"url"},
	"click":    {"selector"},
	"type":     {"selector", "text"},
	"press":    {"key"},
	"wait_for": {"selector"},
}

// Load reads, validates and defaults the script at path.
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
	p = append(p, s.validate(filepath.Dir(path))...)
	if len(p) > 0 {
		return nil, p
	}
	s.fillDefaults()
	return &s, nil
}

// JSON is the normalised script.
func (s *Script) JSON() ([]byte, error) {
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

func (s *Script) validate(dir string) Problems {
	var p Problems
	add := func(format string, a ...any) { p = append(p, fmt.Sprintf(format, a...)) }
	if s.Version != 1 {
		add("version: want 1")
	}
	if s.Name == "" {
		add("name: required")
	}
	if s.Width < 0 {
		add("width: must be > 0")
	}
	if s.Height < 0 {
		add("height: must be > 0")
	}
	if s.FPS != nil && (*s.FPS < 1 || *s.FPS > 120) {
		add("fps: want an integer 1-120")
	}
	if len(s.Steps) == 0 {
		add("step: at least one step is required")
	}
	for i, st := range s.Steps {
		at := func(field, format string, a ...any) {
			p = append(p, fmt.Sprintf("step[%d].%s: %s", i, field, fmt.Sprintf(format, a...)))
		}
		req, ok := stepFields[st.Action]
		if !ok {
			at("action", "want one of %s", strings.Join(actions, ", "))
		} else {
			vals := map[string]string{"url": st.URL, "selector": st.Selector, "text": st.Text, "key": st.Key}
			for _, f := range req {
				if vals[f] == "" {
					at(f, "required for %s", st.Action)
				}
			}
			if st.Key != "" && !slices.Contains(clip.Keys, st.Key) {
				at("key", "want one of %s", strings.Join(clip.Keys, ", "))
			}
		}
		if st.TimeoutMS != nil && *st.TimeoutMS <= 0 {
			at("timeout_ms", "must be > 0")
		}
		if st.Audio != "" {
			if fi, err := os.Stat(filepath.Join(dir, st.Audio)); err != nil || fi.IsDir() {
				at("audio", "no such file %s", st.Audio)
			}
		}
	}
	return p
}

func (s *Script) fillDefaults() {
	if s.Width == 0 {
		s.Width = DefaultWidth
	}
	if s.Height == 0 {
		s.Height = DefaultHeight
	}
	if s.FPS == nil {
		f := DefaultFPS
		s.FPS = &f
	}
	for i := range s.Steps {
		if s.Steps[i].TimeoutMS == nil {
			t := DefaultTimeoutMS
			s.Steps[i].TimeoutMS = &t
		}
	}
}
