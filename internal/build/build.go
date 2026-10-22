// Package build renders every target in a manifest through render.Run, in
// parallel, so a project's set of pages is one command and the targets
// whose inputs and outputs are unchanged are skipped by the render cache.
package build

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/lockyc/plate/internal/cli"
	"github.com/lockyc/plate/internal/engine"
	"github.com/lockyc/plate/internal/impose"
	"github.com/lockyc/plate/internal/marks"
	"github.com/lockyc/plate/internal/render"
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
	run                func(ctx context.Context, in, out string) error
}

func (t Target) derivations() []derivation {
	var ds []derivation
	if t.TwoUp != "" {
		ds = append(ds, derivation{"2up", "imposed", t.TwoUp, func(ctx context.Context, in, out string) error {
			return impose.Run(ctx, in, out, false, true)
		}})
	}
	if t.Marks != "" {
		ds = append(ds, derivation{"marks", "marked", t.Marks, func(ctx context.Context, in, out string) error {
			return marks.Run(ctx, in, out, marks.DefaultBleed, true)
		}})
	}
	return ds
}

// derive writes d after t's render: when the PDF was rendered, or d's file
// is missing or older than the PDF. A render that failed leaves no PDF, so
// d's file goes too.
func derive(ctx context.Context, t Target, d derivation, res render.Result, renderErr error) (wrote bool, err error) {
	if renderErr != nil {
		if err := os.Remove(d.path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return false, errors.Join(renderErr, err)
		}
		return false, renderErr
	}
	if res.Outcome == render.Unchanged {
		pdf, err := os.Stat(t.Opts.PDF)
		if err != nil {
			return false, err
		}
		if out, err := os.Stat(d.path); err == nil && !out.ModTime().Before(pdf.ModTime()) {
			return false, nil
		}
	}
	if err := d.run(ctx, t.Opts.PDF, d.path); err != nil {
		os.Remove(d.path)
		return false, fmt.Errorf("%s: %w", d.option, err)
	}
	return true, nil
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
			r.res, r.err = render.Run(ctx, t.Opts, &r.warn)
			// Once one fails, the rest see its error and remove their files.
			for _, d := range t.derivations() {
				var wrote bool
				wrote, r.err = derive(ctx, t, d, r.res, r.err)
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
