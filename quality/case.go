// Package quality runs every plate operation over openly licensed or
// synthetic test assets and checks each result against a measured
// threshold. The real run is `just quality` (build tag quality): it needs
// the engines and models and runs outside `just gate`. Untagged tests check
// the registry and the case file.
package quality

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/lockyc/plate/internal/engine"
)

// Asset is one registered file in quality/assets/.
type Asset struct {
	File    string `toml:"file"`
	Source  string `toml:"source"` // URL, or "synthetic"
	Recipe  string `toml:"recipe"` // how a synthetic or derived file was made
	Author  string `toml:"author"`
	Licence string `toml:"licence"`
	SHA256  string `toml:"sha256"`
}

// Case is one quality check. String fields and params may use {asset:NAME}
// (a file in quality/assets/) and {tmp:NAME} (a file in the case's own
// directory, which is also the working directory of every step).
type Case struct {
	Name   string            `toml:"name"`
	Why    string            `toml:"why"`    // the shortfall this case pins
	Files  map[string]string `toml:"files"`  // written into the case directory first
	Setup  [][]string        `toml:"setup"`  // steps before run; argv[0] "plate" or a pinned engine
	Run    []string          `toml:"run"`    // plate arguments under test
	Post   [][]string        `toml:"post"`   // steps after run, before the metric
	Exit   int               `toml:"exit"`   // expected exit status of run
	Stderr string            `toml:"stderr"` // substring run's stderr must contain
	Stdout string            `toml:"stdout"` // substring run's stdout must contain
	Absent []string          `toml:"absent"` // files that must not exist after run
	Metric string            `toml:"metric"`
	Params Params            `toml:"params"`
	Max    *float64          `toml:"max"`
	Min    *float64          `toml:"min"`
}

// text is every string in the case, for reference checks.
func (c Case) text() string {
	var b strings.Builder
	for _, s := range c.Run {
		b.WriteString(s + "\n")
	}
	for _, step := range append(append([][]string{}, c.Setup...), c.Post...) {
		b.WriteString(strings.Join(step, "\n") + "\n")
	}
	fmt.Fprintf(&b, "%v", c.Params)
	return b.String()
}

func LoadAssets(path string) ([]Asset, error) {
	var f struct {
		Asset []Asset `toml:"asset"`
	}
	_, err := toml.DecodeFile(path, &f)
	return f.Asset, err
}

func LoadCases(path string) ([]Case, error) {
	var f struct {
		Case []Case `toml:"case"`
	}
	md, err := toml.DecodeFile(path, &f)
	if err != nil {
		return nil, err
	}
	if extra := md.Undecoded(); len(extra) > 0 {
		return nil, fmt.Errorf("%s: unknown fields %v", path, extra)
	}
	return f.Case, nil
}

// Env is where a case runs.
type Env struct{ Assets, Tmp, Plate string }

var placeholder = regexp.MustCompile(`\{(asset|tmp):([^}]+)\}`)

func (e Env) Expand(s string) string {
	return placeholder.ReplaceAllStringFunc(s, func(m string) string {
		p := placeholder.FindStringSubmatch(m)
		if p[1] == "asset" {
			return filepath.Join(e.Assets, p[2])
		}
		return filepath.Join(e.Tmp, p[2])
	})
}

func (e Env) ExpandParams(p Params) Params {
	out := Params{}
	for k, v := range p {
		out[k] = e.expandAny(v)
	}
	return out
}

func (e Env) expandAny(v any) any {
	switch t := v.(type) {
	case string:
		return e.Expand(t)
	case []any:
		out := make([]any, len(t))
		for i, x := range t {
			out[i] = e.expandAny(x)
		}
		return out
	}
	return v
}

func (e Env) argv(step []string) []string {
	out := make([]string, len(step))
	for i, s := range step {
		out[i] = e.Expand(s)
	}
	return out
}

func (e Env) step(ctx context.Context, step []string) error {
	argv := e.argv(step)
	path := e.Plate
	if argv[0] != "plate" {
		p, err := engine.Resolve(argv[0])
		if err != nil {
			return err
		}
		path = p
	}
	cmd := exec.CommandContext(ctx, path, argv[1:]...)
	cmd.Dir = e.Tmp
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("step %q: %v\n%s", argv, err, out)
	}
	return nil
}

// RunCase writes the case's files, runs setup, run and post, and checks the
// exit status, stderr, stdout and absent files. The metric is the caller's.
func (e Env) RunCase(ctx context.Context, c Case) error {
	for name, body := range c.Files {
		if err := os.WriteFile(filepath.Join(e.Tmp, name), []byte(body), 0o644); err != nil {
			return err
		}
	}
	for _, s := range c.Setup {
		if err := e.step(ctx, s); err != nil {
			return fmt.Errorf("setup: %w", err)
		}
	}
	cmd := exec.CommandContext(ctx, e.Plate, e.argv(c.Run)...)
	cmd.Dir = e.Tmp
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		return err
	}
	if code != c.Exit {
		return fmt.Errorf("plate %s exited %d, want %d\n%s", strings.Join(c.Run, " "), code, c.Exit, stderr.String())
	}
	if c.Stderr != "" && !strings.Contains(stderr.String(), c.Stderr) {
		return fmt.Errorf("stderr lacks %q:\n%s", c.Stderr, stderr.String())
	}
	if c.Stdout != "" && !strings.Contains(stdout.String(), c.Stdout) {
		return fmt.Errorf("stdout lacks %q:\n%s", c.Stdout, stdout.String())
	}
	for _, a := range c.Absent {
		if _, err := os.Stat(e.Expand(a)); err == nil {
			return fmt.Errorf("%s exists after a run that should leave nothing", e.Expand(a))
		}
	}
	for _, s := range c.Post {
		if err := e.step(ctx, s); err != nil {
			return fmt.Errorf("post: %w", err)
		}
	}
	return nil
}
