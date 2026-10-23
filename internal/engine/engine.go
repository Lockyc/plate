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
	path, err := executablePath(c)
	if err != nil {
		return Result{}, err
	}
	res, err := run(ctx, path, c)
	if err != nil {
		for _, o := range c.Outputs {
			os.Remove(o)
		}
	}
	return res, err
}

// executablePath is c.Path, else the resolved engine.
func executablePath(c Cmd) (string, error) {
	if c.Path != "" {
		return c.Path, nil
	}
	return Resolve(c.Engine)
}

// timeoutOf is c.Timeout, or DefaultTimeout when it sets none.
func timeoutOf(c Cmd) time.Duration {
	if c.Timeout == 0 {
		return DefaultTimeout
	}
	return c.Timeout
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
	timeout := timeoutOf(c)
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

// Capable reports whether the engine build has c: c.Args run, and c.Token
// appears in the output as a whole word. Like Version, the exit status is
// ignored and stdout and stderr are read together. A run cut off by the
// timeout or ctx is an error, not a build that lacks c.
func Capable(ctx context.Context, e pins.Engine, c pins.Capability) (bool, error) {
	path, err := Find(e)
	if err != nil {
		return false, err
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, c.Args...)
	killGroupOnCancel(cmd)
	cmd.WaitDelay = time.Second
	out, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		why := "cancelled"
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			why = "timed out"
		}
		return false, fmt.Errorf("%s %s: %s: %w", e.Name, strings.Join(c.Args, " "), why, ctx.Err())
	}
	var ee *exec.ExitError
	if err != nil && !errors.As(err, &ee) {
		return false, fmt.Errorf("%s: %w", e.Name, err)
	}
	return regexp.MustCompile(`(?m)\b` + regexp.QuoteMeta(c.Token) + `\b`).Match(out), nil
}

func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

// Proc is an engine kept running behind two pipes: In is its fd 3 and Out its
// fd 4, the channel Chrome's --remote-debugging-pipe speaks.
type Proc struct {
	In  io.WriteCloser
	Out io.ReadCloser

	engine  string
	cmd     *exec.Cmd
	stderr  *os.File
	ctx     context.Context
	cancel  context.CancelFunc
	timeout time.Duration
	done    chan struct{} // closed once the engine has exited and its group is killed
	waitErr error
}

// Start launches c with pipes on fd 3 and 4 and returns at once. The timeout
// (0 is DefaultTimeout) covers the whole session, the engine runs in its own
// process group, and the group is killed when ctx ends or Close gives up.
// Outputs and Inputs are not checked: the caller owns the session.
func Start(ctx context.Context, c Cmd) (*Proc, error) {
	path, err := executablePath(c)
	if err != nil {
		return nil, err
	}
	timeout := timeoutOf(c)
	ctx, cancel := context.WithTimeout(ctx, timeout)
	stderr, err := scratch()
	if err != nil {
		cancel()
		return nil, err
	}
	r3, w3, err := os.Pipe()
	if err != nil {
		cancel()
		stderr.Close()
		return nil, err
	}
	r4, w4, err := os.Pipe()
	if err != nil {
		cancel()
		stderr.Close()
		r3.Close()
		w3.Close()
		return nil, err
	}
	cmd := exec.CommandContext(ctx, path, pathArgs(c)...)
	cmd.Stderr = stderr
	cmd.ExtraFiles = []*os.File{r3, w4}
	killGroupOnCancel(cmd)
	err = cmd.Start()
	r3.Close()
	w4.Close()
	if err != nil {
		cancel()
		stderr.Close()
		w3.Close()
		r4.Close()
		return nil, err
	}
	p := &Proc{In: w3, Out: r4, engine: c.Engine, cmd: cmd, stderr: stderr, ctx: ctx, cancel: cancel, timeout: timeout, done: make(chan struct{})}
	go func() {
		p.waitErr = cmd.Wait()
		killGroup(cmd)
		close(p.done)
	}()
	return p, nil
}

// Close closes both pipes, waits up to 5 s for the engine to exit, then kills
// its group. It returns nil after a clean session and a *RunError carrying the
// stderr tail when the session timed out, was cancelled, or the engine failed
// without being killed by Close.
func (p *Proc) Close() error {
	p.In.Close()
	p.Out.Close()
	killed := false
	select {
	case <-p.done:
	case <-time.After(5 * time.Second):
		killed = true
		killGroup(p.cmd)
		<-p.done
	}
	defer p.stderr.Close()
	defer p.cancel()
	fail := func(reason string) error {
		return &RunError{Engine: p.engine, Reason: reason, Stderr: string(readAll(p.stderr))}
	}
	switch {
	case errors.Is(p.ctx.Err(), context.DeadlineExceeded):
		return fail("timed out after " + p.timeout.String())
	case p.ctx.Err() != nil:
		return fail("cancelled")
	case p.waitErr != nil && !killed:
		return fail(p.waitErr.Error())
	}
	return nil
}
