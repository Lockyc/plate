// Package engine is the one way plate runs an external tool. Every call gets
// a timeout; success means exit status 0 and every
// declared output existing and non-empty, because Chrome exits 0 when it
// renders nothing and prints noise on stderr when it succeeds; a declared
// output is deleted before the run and again if the run fails, so a failure
// never leaves a plausible-looking file behind; and stderr is fatal where a
// call says so (Ghostscript, whose stderr is the only sign it dropped an
// image).
package engine

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/lockyc/plate/internal/pins"
)

// DefaultTimeout applies when a Cmd sets none.
const DefaultTimeout = 10 * time.Minute

// EnvOverride names the variable that points plate at a specific executable
// for an engine, ahead of the data directory and PATH.
func EnvOverride(name string) string {
	return "PLATE_ENGINE_" + strings.ToUpper(strings.ReplaceAll(name, "-", "_"))
}

// DataDir holds managed engines: $XDG_DATA_HOME/plate, else ~/.local/share/plate.
func DataDir() (string, error) { return xdg("XDG_DATA_HOME", ".local/share") }

// CacheDir holds rebuildable state (materialised scripts, compiled helpers):
// $XDG_CACHE_HOME/plate, else ~/.cache/plate.
func CacheDir() (string, error) { return xdg("XDG_CACHE_HOME", ".cache") }

func xdg(env, fallback string) (string, error) {
	if d := os.Getenv(env); d != "" {
		return filepath.Join(d, "plate"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, fallback, "plate"), nil
}

// Cached returns a cache directory holding files, named by a hash of their
// names and contents so a new plate never runs a file an older one left
// behind. When the directory is absent it is built in a fresh one: files are
// written, then build (if not nil) runs in it, then it is renamed into place
// whole, so a reader never sees a half-built directory and concurrent builds
// of one key cannot interleave.
func Cached(kind string, files map[string][]byte, build func(dir string) error) (string, error) {
	names := slices.Sorted(maps.Keys(files))
	h := sha256.New()
	for _, n := range names {
		fmt.Fprintf(h, "%s\x00%d\x00", n, len(files[n]))
		h.Write(files[n])
	}
	cache, err := CacheDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(cache, kind, hex.EncodeToString(h.Sum(nil))[:16])
	if _, err := os.Stat(dir); err == nil {
		return dir, nil
	}
	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		return "", err
	}
	tmp, err := os.MkdirTemp(filepath.Dir(dir), "."+kind+"-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(tmp)
	for _, n := range names {
		if err := os.WriteFile(filepath.Join(tmp, n), files[n], 0o644); err != nil {
			return "", err
		}
	}
	if build != nil {
		if err := build(tmp); err != nil {
			return "", err
		}
	}
	if err := os.Rename(tmp, dir); err != nil {
		if _, statErr := os.Stat(dir); statErr != nil {
			return "", err
		}
	}
	return dir, nil
}

// ManagedDir is where a managed engine's pinned version is unpacked.
func ManagedDir(e pins.Engine) (string, error) {
	d, err := DataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, e.Name, e.Download.Version), nil
}

// NotInstalledError says an engine is missing and how to get it.
type NotInstalledError struct{ Engine, Hint string }

func (e *NotInstalledError) Error() string {
	return e.Engine + " is not installed: " + e.Hint
}

// Resolve finds the executable for the named engine.
func Resolve(name string) (string, error) {
	e, ok := pins.Lookup(name)
	if !ok {
		return "", fmt.Errorf("engine %q is not in the pin table", name)
	}
	return Find(e)
}

// Find locates e: its override variable, then (Managed) the data directory
// or (Minimum) PATH. The override is made absolute, because exec searches
// PATH for a bare name, which is not the file checked here.
func Find(e pins.Engine) (string, error) {
	if p := os.Getenv(EnvOverride(e.Name)); p != "" {
		if abs, err := filepath.Abs(p); err == nil {
			p = abs
		}
		if !executable(p) {
			return "", &NotInstalledError{Engine: e.Name, Hint: EnvOverride(e.Name) + "=" + p + " is not an executable file"}
		}
		return p, nil
	}
	missing := &NotInstalledError{Engine: e.Name, Hint: e.Hint()}
	if !e.Supported() {
		return "", missing
	}
	if e.Kind == pins.Managed {
		a, ok := e.Asset()
		if !ok {
			return "", missing
		}
		dir, err := ManagedDir(e)
		if err != nil {
			return "", err
		}
		if p := filepath.Join(dir, a.Bin); executable(p) {
			return p, nil
		}
		return "", missing
	}
	p, err := exec.LookPath(e.Name)
	if err != nil {
		return "", missing
	}
	return p, nil
}

func executable(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.Mode().IsRegular() && fi.Mode()&0o111 != 0
}

// Cmd is one engine invocation. Inputs and Outputs are paths as plate sees
// them, relative to its own working directory.
type Cmd struct {
	Engine      string // pin-table name; resolves the executable unless Path is set
	Path        string // an executable plate built itself; Engine is then only a label
	Args        []string
	Timeout     time.Duration // 0 means DefaultTimeout
	StderrFatal bool          // any stderr output fails the call
	AllowExit   bool          // a non-zero exit status is not a failure; the caller judges the output (Chrome's teardown watchdog exits non-zero after writing a whole page)
	Inputs      []string      // files the engine reads; none may also be an output
	Outputs     []string      // files that must exist and be non-empty afterwards
}

// Result is what the engine printed.
type Result struct{ Stdout, Stderr []byte }

// RunError is a failed engine call.
type RunError struct{ Engine, Reason, Stderr string }

func (e *RunError) Error() string {
	msg := e.Engine + ": " + e.Reason
	if s := lastLines(e.Stderr, 20); s != "" {
		msg += "\n" + s
	}
	return msg
}

// Run executes c under the package rules. It refuses a call whose output is
// also one of its inputs before touching anything, because deleting the
// stale output would destroy the source.
func Run(ctx context.Context, c Cmd) (Result, error) {
	for _, o := range c.Outputs {
		for _, in := range c.Inputs {
			if SameFile(in, o) {
				return Result{}, fmt.Errorf("%s: %s is both an input and an output; write the result elsewhere", c.Engine, o)
			}
		}
	}
	for _, o := range c.Outputs {
		if err := os.Remove(o); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return Result{}, err
		}
	}
	path := c.Path
	if path == "" {
		p, err := Resolve(c.Engine)
		if err != nil {
			return Result{}, err
		}
		path = p
	}
	res, err := run(ctx, path, c)
	if err != nil {
		for _, o := range c.Outputs {
			os.Remove(o)
		}
	}
	return res, err
}

// SameFile reports whether a and b name one file: the same inode when both
// exist, else the same absolute path.
func SameFile(a, b string) bool {
	fa, errA := os.Stat(a)
	fb, errB := os.Stat(b)
	if errA == nil && errB == nil {
		return os.SameFile(fa, fb)
	}
	absA, errA := filepath.Abs(a)
	absB, errB := filepath.Abs(b)
	return errA == nil && errB == nil && absA == absB
}

// run gives the engine files, not pipes, for stdin, stdout and stderr, so
// Wait returns when the engine exits rather than when the last process
// holding its output closes it; then it kills the engine's process group,
// so nothing the engine left behind outlives the call.
func run(ctx context.Context, path string, c Cmd) (Result, error) {
	timeout := c.Timeout
	if timeout == 0 {
		timeout = DefaultTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, pathArgs(c)...)
	stdout, err := scratch()
	if err != nil {
		return Result{}, err
	}
	defer stdout.Close()
	stderr, err := scratch()
	if err != nil {
		return Result{}, err
	}
	defer stderr.Close()
	cmd.Stdout, cmd.Stderr = stdout, stderr
	killGroupOnCancel(cmd)
	if err = cmd.Start(); err == nil {
		err = cmd.Wait()
		killGroup(cmd)
	}
	res := Result{Stdout: readAll(stdout), Stderr: readAll(stderr)}
	fail := func(reason string) (Result, error) {
		return res, &RunError{Engine: c.Engine, Reason: reason, Stderr: string(res.Stderr)}
	}
	switch {
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		return fail("timed out after " + timeout.String())
	case ctx.Err() != nil:
		return fail("cancelled")
	}
	var exit *exec.ExitError
	if err != nil && !(c.AllowExit && errors.As(err, &exit)) {
		return fail(err.Error())
	}
	if c.StderrFatal && len(bytes.TrimSpace(res.Stderr)) > 0 {
		return fail("reported a problem on stderr")
	}
	for _, o := range c.Outputs {
		if fi, err := os.Stat(o); err != nil || fi.Size() == 0 {
			return fail("produced no output at " + o)
		}
	}
	return res, nil
}

// pathArgs is c.Args with each argument that is one of c's Inputs or Outputs
// and begins with "-" prefixed "./", so the engine reads a file named
// "-x.pdf" as a file rather than a flag.
func pathArgs(c Cmd) []string {
	args := slices.Clone(c.Args)
	for i, a := range args {
		if strings.HasPrefix(a, "-") && (slices.Contains(c.Inputs, a) || slices.Contains(c.Outputs, a)) {
			args[i] = "./" + a
		}
	}
	return args
}

// scratch is an anonymous temporary file: unlinked at once, gone on Close.
func scratch() (*os.File, error) {
	f, err := os.CreateTemp("", "plate-")
	if err != nil {
		return nil, err
	}
	if err := os.Remove(f.Name()); err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}

// readAll reads a scratch file the engine wrote from the start.
func readAll(f *os.File) []byte {
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return nil
	}
	b, _ := io.ReadAll(f)
	return b
}

// Version reads a Minimum engine's version. The exit status is ignored,
// because some tools exit non-zero from their version flag, and stdout and
// stderr are read together, because some print it on stderr.
func Version(ctx context.Context, e pins.Engine) (string, error) {
	if e.Kind != pins.Minimum {
		return "", fmt.Errorf("%s: version is pinned by its install path, not read", e.Name)
	}
	path, err := Find(e)
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	// The group kill and WaitDelay stop a child that keeps the pipe open
	// from holding CombinedOutput past the timeout.
	cmd := exec.CommandContext(ctx, path, e.VersionArgs...)
	killGroupOnCancel(cmd)
	cmd.WaitDelay = time.Second
	out, err := cmd.CombinedOutput()
	m := regexp.MustCompile(e.VersionRe).FindSubmatch(out)
	if m == nil {
		var ee *exec.ExitError
		if err != nil && !errors.As(err, &ee) {
			return "", fmt.Errorf("%s: %w", e.Name, err)
		}
		return "", fmt.Errorf("%s: no version in %q", e.Name, lastLines(string(out), 1))
	}
	return string(m[1]), nil
}

func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}
