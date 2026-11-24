// Package ml runs plate's Python: embedded single-file scripts whose
// dependencies are fixed by a committed `uv lock --script` lockfile and run
// with --locked, and pinned upstream CLIs through `uv tool run`. uv is the
// only Python tool a user installs. Python is here only because the matting,
// inpainting and super-resolution models have no Go, Rust or shell
// equivalent of comparable quality.
package ml

import (
	"context"
	"embed"
	"fmt"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/lockyc/plate/internal/engine"
	"github.com/lockyc/plate/internal/pins"
)

//go:embed scripts
var scripts embed.FS

// Timeout covers a first run, which downloads the dependencies and model weights.
const Timeout = 2 * time.Hour

// deviceModule is the device rule's file name, which the torch scripts
// import from beside them.
const deviceModule = "plate_device.py"

//go:embed plate_device.py
var deviceRule []byte

// Script writes an embedded script, its lockfile and the device rule into
// the cache and returns the script's path.
func Script(name string) (string, error) {
	py, err := scripts.ReadFile("scripts/" + name)
	if err != nil {
		return "", fmt.Errorf("no embedded script %s", name)
	}
	lock, err := scripts.ReadFile("scripts/" + name + ".lock")
	if err != nil {
		return "", fmt.Errorf("%s has no lockfile", name)
	}
	dir, err := engine.Cached("ml", map[string][]byte{name: py, name + ".lock": lock, deviceModule: deviceRule}, nil)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, name), nil
}

// ToolDevice is the device a pinned tool's torch model runs on, by the same
// rule the scripts import, asked of the tool's own torch.
func ToolDevice(ctx context.Context, t pins.PyTool) (string, error) {
	dir, err := engine.Cached("ml", map[string][]byte{deviceModule: deviceRule}, nil)
	if err != nil {
		return "", err
	}
	res, err := RunTool(ctx, t, "python", []string{filepath.Join(dir, deviceModule)}, nil, nil)
	if err != nil {
		return "", err
	}
	d := strings.TrimSpace(string(res.Stdout))
	if d == "" {
		return "", fmt.Errorf("%s's torch named no device", t.Package)
	}
	return d, nil
}

// platform is GOOS/GOARCH, and procTranslated reports Rosetta; variables
// so tests can stand in another machine.
var (
	platform       = runtime.GOOS + "/" + runtime.GOARCH
	procTranslated = translated
)

// intelMac reports an Intel Mac: an amd64 build on macOS that Rosetta is not
// translating. Under Rosetta the machine is Apple Silicon, and uv installs
// the arm64 wheels.
func intelMac(platform, translated string) bool {
	return platform == "darwin/amd64" && translated != "1"
}

// intelMacBlockers names the packages in a script's lockfile that uv cannot
// install on an Intel Mac: built for macOS, but only for Apple Silicon, and
// with no source distribution to build from. PyTorch and ONNX Runtime
// publish no macOS x86_64 wheels.
func intelMacBlockers(lock []byte) ([]string, error) {
	var l struct {
		Package []struct {
			Name   string
			Sdist  map[string]any
			Wheels []struct{ URL string }
		}
	}
	if err := toml.Unmarshal(lock, &l); err != nil {
		return nil, err
	}
	var blocked []string
	for _, p := range l.Package {
		mac, intel := false, false
		for _, w := range p.Wheels {
			mac = mac || strings.Contains(w.URL, "-macosx_")
			intel = intel || strings.HasSuffix(w.URL, "-none-any.whl") || intelWheel.MatchString(w.URL)
		}
		if mac && !intel && p.Sdist == nil {
			blocked = append(blocked, p.Name)
		}
	}
	return blocked, nil
}

var intelWheel = regexp.MustCompile(`-macosx_[0-9_]+_(x86_64|universal2|intel)\.whl$`)

// Option changes how RunScript runs a script.
type Option func(*engine.Cmd)

// WithTimeout gives the script d instead of Timeout: for a run whose work
// grows with its input, such as a long run of frames.
func WithTimeout(d time.Duration) Option {
	return func(c *engine.Cmd) { c.Timeout = d }
}

// RunScript runs an embedded script with uv, locked to its lockfile. On an
// Intel Mac it first refuses a script whose lockfile uv cannot install
// there, rather than let uv fail on resolution.
func RunScript(ctx context.Context, name string, args, inputs, outputs []string, opts ...Option) (engine.Result, error) {
	p, err := Script(name)
	if err != nil {
		return engine.Result{}, err
	}
	if intelMac(platform, procTranslated()) {
		lock, _ := scripts.ReadFile("scripts/" + name + ".lock") // Script has read it
		blocked, err := intelMacBlockers(lock)
		if err != nil {
			return engine.Result{}, fmt.Errorf("%s.lock: %w", name, err)
		}
		if len(blocked) > 0 {
			return engine.Result{}, fmt.Errorf("%s needs %s, which publish no Intel-Mac (macOS x86_64) build; run it on Apple Silicon or Linux", name, strings.Join(blocked, " and "))
		}
	}
	c := engine.Cmd{Engine: "uv", Args: append([]string{"run", "--locked", "--script", p}, args...), Inputs: inputs, Outputs: outputs, Timeout: Timeout}
	for _, o := range opts {
		o(&c)
	}
	return engine.Run(ctx, c)
}

// RunTool runs cmd from a pinned upstream package.
func RunTool(ctx context.Context, t pins.PyTool, cmd string, args, inputs, outputs []string) (engine.Result, error) {
	a := append([]string{"tool", "run", "--from", t.Package + "==" + t.Version, "--exclude-newer", t.ExcludeNewer, cmd}, args...)
	return engine.Run(ctx, engine.Cmd{Engine: "uv", Args: a, Inputs: inputs, Outputs: outputs, Timeout: Timeout})
}
