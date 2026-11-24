// Package build renders every target in a manifest through render.Run, in
// parallel, so a project's set of pages is one command and the targets
// whose inputs and outputs are unchanged are skipped by the render cache.
package build

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"

	"github.com/lockyc/plate/internal/animate"
	"github.com/lockyc/plate/internal/atomicfile"
	"github.com/lockyc/plate/internal/cli"
	"github.com/lockyc/plate/internal/engine"
	"github.com/lockyc/plate/internal/impose"
	"github.com/lockyc/plate/internal/marks"
	"github.com/lockyc/plate/internal/pdf"
	"github.com/lockyc/plate/internal/render"
	"github.com/lockyc/plate/internal/video"
)

const usage = "build [--out DIR] [--jobs N] [--force] [--fail-if TEXT]... <manifest> [NAME...]"

// result is one target's render: its warnings, buffered so they print with
// its line, and how it went.
type result struct {
	warn    bytes.Buffer
	res     render.Result
	derived []string // log lines for the 2up sheet and marked copy written
	err     error
	done    chan struct{}
}

// derivation is a file a target's options write from its PDF: the 2up
// sheet or the marked copy.
type derivation struct {
	option, verb, path string
	opts               string // what shapes the file besides the PDF; see stale
	run                func(ctx context.Context, in, out string) error
}

func (t Target) derivations() []derivation {
	var ds []derivation
	if t.TwoUp != "" {
		ds = append(ds, derivation{"2up", "imposed", t.TwoUp, "", func(ctx context.Context, in, out string) error {
			return impose.Run(ctx, in, out, false, true)
		}})
	}
	if t.Marks != "" {
		ds = append(ds, derivation{"marks", "marked", t.Marks, t.marksOpts(), func(ctx context.Context, in, out string) error {
			return marks.Run(ctx, in, out, marks.DefaultBleed, t.Trim, true)
		}})
	}
	return ds
}

// marksOpts is what shapes t's marked copies besides the PDF they mark.
func (t Target) marksOpts() string {
	return fmt.Sprintf("bleed=%g trim=%gx%g", float64(marks.DefaultBleed), t.Trim.W, t.Trim.H)
}

// derive writes d after t's render: when the PDF was rendered, or d's file
// is stale. A render that failed leaves no PDF, so d's file goes too.
func derive(ctx context.Context, t Target, d derivation, res render.Result, renderErr error, warn io.Writer) (wrote bool, err error) {
	if renderErr != nil {
		if err := os.Remove(d.path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return false, errors.Join(renderErr, err)
		}
		return false, renderErr
	}
	if res.Outcome == render.Unchanged {
		if s, err := stale(d.path, d.opts, t.Opts.PDF); err != nil || !s {
			return false, err
		}
	}
	if err := write(d.path, d.opts, warn, func() error { return d.run(ctx, t.Opts.PDF, d.path) }); err != nil {
		os.Remove(d.path)
		return false, fmt.Errorf("%s: %w", d.option, err)
	}
	return true, nil
}

// runVideo captures a video line. A still the line no longer lists is
// removed first: the cache claims only listed files, so it would sit beside
// the new video looking current.
func runVideo(ctx context.Context, t Target, force bool, warn io.Writer) (render.Result, error) {
	a := *t.Anim
	a.Force = force
	listed := t.Outputs()
	for _, ext := range []string{".png", ".jpg"} {
		p := filepath.Join(a.Out, a.Name+ext)
		if slices.ContainsFunc(listed, func(o string) bool { return engine.SameFile(o, p) }) {
			continue
		}
		if err := os.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return render.Result{}, err
		}
	}
	res, _, err := animate.Run(ctx, a, warn)
	return res, err
}

// removeVideoSet removes the video set an earlier run of the still line t
// left under its name: when <name>.json is a capture's sidecar for t (its
// name, and the formats it wrote), the
// files its formats name and the sidecar, except files another target
// writes. Files under the name that no capture of t wrote are left.
func removeVideoSet(t Target, dir string, all []Target) error {
	sidecar := filepath.Join(dir, t.Name+".json")
	b, err := os.ReadFile(sidecar)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var m video.Measurement
	if json.Unmarshal(b, &m) != nil || m.Name != t.Name || len(m.Formats) == 0 {
		return nil
	}
	var set []string
	for _, f := range video.Formats {
		if _, ok := m.Formats[f]; ok {
			set = append(set, t.Name+"."+video.Ext(f))
		}
	}
	for _, n := range append(set, t.Name+".json") {
		p := filepath.Join(dir, n)
		if slices.ContainsFunc(all, func(o Target) bool {
			return o.Name != t.Name && slices.ContainsFunc(o.Outputs(), func(f string) bool { return engine.SameFile(f, p) })
		}) {
			continue
		}
		if err := os.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	return nil
}

// Main runs `plate build`. A target that fails does not stop the others;
// the log is in manifest order whatever order the renders finish in.
func Main(ctx context.Context, args []string, _, stderr io.Writer) int {
	fs := cli.Flags("build", usage, stderr)
	out := fs.String("out", "", "write the outputs to `DIR` (default: dist beside the manifest)")
	jobs := cli.Int(fs, "jobs", min(4, runtime.NumCPU()), "render up to `N` targets at once")
	force := fs.Bool("force", false, "render every target even when nothing it loaded or wrote has changed")
	var failIf cli.Multi
	fs.Var(&failIf, "fail-if", "fail a target when `TEXT` appears in its page, as render --fail-if (repeatable)")
	rest, code, ok := cli.Parse(fs, args, -1)
	if !ok {
		return code
	}
	if len(rest) == 0 {
		fs.Usage()
		return 2
	}
	if *jobs < 1 {
		return cli.Usage(stderr, "build", "--jobs must be at least 1")
	}
	path := rest[0]
	dir := *out
	if dir == "" {
		dir = filepath.Join(filepath.Dir(path), "dist")
	}
	dir, err := filepath.Abs(dir)
	if err != nil {
		return cli.Fail(stderr, "build", err)
	}
	all, err := ParseManifest(path, dir, failIf)
	if le := (*LineError)(nil); errors.As(err, &le) {
		return cli.Usage(stderr, "build", "%v", err)
	}
	if err != nil {
		return cli.Fail(stderr, "build", err)
	}
	targets, err := selectTargets(all, rest[1:], path)
	if err != nil {
		return cli.Usage(stderr, "build", "%v", err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return cli.Fail(stderr, "build", err)
	}

	results := make([]*result, len(targets))
	sem := make(chan struct{}, *jobs)
	for i, t := range targets {
		r := &result{done: make(chan struct{})}
		results[i] = r
		t.Opts.Force = *force
		go func() {
			defer close(r.done)
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				r.err = ctx.Err()
				return
			}
			defer func() { <-sem }()
			if t.Anim != nil {
				r.res, r.err = runVideo(ctx, t, *force, &r.warn)
			} else if r.err = removeVideoSet(t, dir, all); r.err == nil {
				r.res, r.err = render.Run(ctx, t.Opts, &r.warn)
			}
			// Once one fails, the rest see its error and remove their files.
			for _, d := range t.derivations() {
				var wrote bool
				wrote, r.err = derive(ctx, t, d, r.res, r.err, &r.warn)
				if wrote {
					r.derived = append(r.derived, d.verb+" "+filepath.Base(d.path))
				}
			}
		}()
	}
	var rendered, unchanged, failed int
	for i, r := range results {
		<-r.done
		name := targets[i].Name
		stderr.Write(r.warn.Bytes())
		if r.err == nil {
			if r.err = loadsOutput(targets[i], r.res.Inputs, all); r.err != nil {
				for _, o := range targets[i].Outputs() {
					os.Remove(o)
				}
			}
		}
		if t := targets[i]; t.Duplex != "" {
			var back *result
			if j := slices.IndexFunc(targets, func(o Target) bool { return o.Name == t.Back }); j >= 0 {
				back = results[j]
			}
			var wrote []string
			wrote, r.err = writeDuplex(ctx, t, r, back, stderr)
			r.derived = append(r.derived, wrote...)
		}
		switch {
		case r.err != nil:
			failed++
			fmt.Fprintf(stderr, "failed %s: %v\n", name, r.err)
		case r.res.Outcome == render.Unchanged:
			unchanged++
			fmt.Fprintf(stderr, "unchanged %s\n", name)
		case r.res.Outcome == render.Uncacheable:
			rendered++
			fmt.Fprintf(stderr, "rendered %s (not cached: %s)\n", name, r.res.Reason)
		default:
			rendered++
			fmt.Fprintf(stderr, "rendered %s\n", name)
		}
		if r.err == nil {
			for _, line := range r.derived {
				fmt.Fprintln(stderr, line)
			}
		}
	}
	if failed > 0 {
		fmt.Fprintf(stderr, "%d rendered, %d unchanged, %d failed\n", rendered, unchanged, failed)
		return 1
	}
	fmt.Fprintf(stderr, "%d rendered, %d unchanged\n", rendered, unchanged)
	return 0
}

// writeDuplex writes t's duplex file once its render and its back's are
// done, and its marked copy when t has marks, returning a log line for each
// file written. Each is written when either PDF was rendered, or it is
// missing or older than what it is made from. back is the back target's
// result, or nil when it was not named this run and its PDF from an
// earlier one is used. A failed render on either side leaves neither file.
// Main calls it as it collects results, so the back's render, in parallel
// or later in the manifest, is never read half-written.
func writeDuplex(ctx context.Context, t Target, own, back *result, warn io.Writer) ([]string, error) {
	fail := func(err error) ([]string, error) {
		for _, p := range []string{t.Duplex, t.DuplexMarks} {
			if rmErr := os.Remove(p); p != "" && rmErr != nil && !errors.Is(rmErr, fs.ErrNotExist) {
				err = errors.Join(err, rmErr)
			}
		}
		return nil, err
	}
	if own.err != nil {
		return fail(own.err)
	}
	rendered := own.res.Outcome != render.Unchanged
	if back != nil {
		<-back.done
		if back.err != nil {
			return fail(fmt.Errorf("back: %s failed", t.Back))
		}
		rendered = rendered || back.res.Outcome != render.Unchanged
	}
	var wrote []string
	s, err := stale(t.Duplex, "", t.Opts.PDF, t.BackPDF)
	if err != nil {
		return fail(fmt.Errorf("back: %w", err))
	}
	if rendered || s {
		if err := write(t.Duplex, "", warn, func() error { return pdf.Duplex(ctx, t.Opts.PDF, t.BackPDF, t.Duplex, true) }); err != nil {
			return fail(fmt.Errorf("back: %w", err))
		}
		wrote = append(wrote, "duplexed "+filepath.Base(t.Duplex))
	}
	if t.DuplexMarks == "" {
		return wrote, nil
	}
	if s, err := stale(t.DuplexMarks, t.marksOpts(), t.Duplex); err != nil || (len(wrote) == 0 && !s) {
		return wrote, err
	}
	if err := write(t.DuplexMarks, t.marksOpts(), warn, func() error {
		return marks.Run(ctx, t.Duplex, t.DuplexMarks, marks.DefaultBleed, t.Trim, true)
	}); err != nil {
		return fail(fmt.Errorf("marks: %w", err))
	}
	return append(wrote, "marked "+filepath.Base(t.DuplexMarks)), nil
}

// stale reports whether the derived file out needs writing again: it is
// missing, older than any of ins (each of which must exist), or its stamp
// does not name opts and out's bytes. opts is everything besides ins that
// shapes out, so a trim= edit alone rewrites a marked copy.
func stale(out, opts string, ins ...string) (bool, error) {
	o, err := os.Stat(out)
	for _, p := range ins {
		in, inErr := os.Stat(p)
		if inErr != nil {
			return false, inErr
		}
		if err == nil && o.ModTime().Before(in.ModTime()) {
			return true, nil
		}
	}
	if err != nil {
		return true, nil
	}
	s, ok := readStamp(out)
	return !ok || s.Opts != opts || s.SHA256 != render.HashFile(out), nil
}

// stamp records the options a derived file was written with and the hash
// of the bytes written, in plate's cache dir beside the render records so
// it never ships with the outputs.
type stamp struct {
	Opts   string
	SHA256 string
}

// stampPath is where out's stamp lives, named for out's absolute path.
func stampPath(out string) (string, error) {
	dir, err := engine.CacheDir()
	if err != nil {
		return "", err
	}
	abs, err := filepath.Abs(out)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(abs))
	return filepath.Join(dir, "build", hex.EncodeToString(sum[:])+".json"), nil
}

// readStamp reads out's stamp; anything short of a well-formed one is a
// miss, which costs one rewrite.
func readStamp(out string) (stamp, bool) {
	p, err := stampPath(out)
	if err != nil {
		return stamp{}, false
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return stamp{}, false
	}
	var s stamp
	return s, json.Unmarshal(b, &s) == nil
}

// write writes the derived file out with run, from opts, and stamps it.
// The old stamp goes first, so a write that fails or is killed never
// leaves one vouching for the old bytes. A stamp that cannot be kept is a
// warning: its cost is one rewrite next run.
func write(out, opts string, warn io.Writer, run func() error) error {
	p, err := stampPath(out)
	if err == nil {
		if rmErr := os.Remove(p); rmErr != nil && !errors.Is(rmErr, fs.ErrNotExist) {
			err = rmErr
		}
	}
	if err != nil {
		fmt.Fprintf(warn, "plate build: warning: stamp for %s: %v\n", filepath.Base(out), err)
		return run()
	}
	if err := run(); err != nil {
		return err
	}
	b, _ := json.Marshal(stamp{opts, render.HashFile(out)})
	if err = os.MkdirAll(filepath.Dir(p), 0o755); err == nil {
		err = atomicfile.WriteFile(p, b, 0o644)
	}
	if err != nil {
		fmt.Fprintf(warn, "plate build: warning: stamp for %s: %v\n", filepath.Base(out), err)
	}
	return nil
}

// loadsOutput refuses t when one of its inputs is another target's output:
// render deletes its outputs before it starts and targets run in parallel,
// so t rendered against a file that may have been missing or half-made.
// Main then removes t's outputs, which its cache record names, so the
// record cannot hit either.
func loadsOutput(t Target, inputs []string, all []Target) error {
	for _, o := range all {
		if o.Name == t.Name {
			continue
		}
		for _, out := range o.Outputs() {
			for _, in := range inputs {
				if engine.SameFile(in, out) {
					return fmt.Errorf("loads %s's output %s; render %s in a step before plate build, or split the manifest", o.Name, out, o.Name)
				}
			}
		}
	}
	return nil
}

// selectTargets is the targets named, in manifest order, or every target
// when none is named. An unknown name starting "-" is a flag given after
// the manifest, where the flag package takes it as a name.
func selectTargets(all []Target, names []string, path string) ([]Target, error) {
	if len(names) == 0 {
		return all, nil
	}
	want := map[string]bool{}
	for _, n := range names {
		want[n] = true
	}
	var ts []Target
	for _, t := range all {
		if want[t.Name] {
			ts = append(ts, t)
			delete(want, t.Name)
		}
	}
	for _, n := range names {
		if want[n] && strings.HasPrefix(n, "-") {
			return nil, fmt.Errorf("%s: flags go before the manifest", n)
		}
		if want[n] {
			return nil, fmt.Errorf("no target %q in %s", n, path)
		}
	}
	return ts, nil
}
