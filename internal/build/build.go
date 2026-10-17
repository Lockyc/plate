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
	"os"
	"path/filepath"
	"runtime"

	"github.com/lockyc/plate/internal/cli"
	"github.com/lockyc/plate/internal/engine"
	"github.com/lockyc/plate/internal/render"
)

const usage = "build [--out DIR] [--jobs N] [--force] [--fail-if TEXT]... <manifest> [NAME...]"

// result is one target's render: its warnings, buffered so they print with
// its line, and how it went.
type result struct {
	warn bytes.Buffer
	res  render.Result
	err  error
	done chan struct{}
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
		}()
	}
	var rendered, unchanged, failed int
	for i, r := range results {
		<-r.done
		name := targets[i].Name
		stderr.Write(r.warn.Bytes())
		if r.err == nil {
			r.err = loadsOutput(targets[i], r.res.Inputs, all)
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
func loadsOutput(t Target, inputs []string, all []Target) error {
	for _, o := range all {
		if o.Name == t.Name {
			continue
		}
		for _, out := range o.Opts.Outputs() {
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
// when none is named.
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
		if want[n] {
			return nil, fmt.Errorf("no target %q in %s", n, path)
		}
	}
	return ts, nil
}
