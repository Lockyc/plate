// Package doctor reports every engine in the pin table against this
// machine, and installs the engines plate manages (--install). Engines a
// package manager provides are reported with the command that installs them.
package doctor

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"runtime"
	"slices"
	"strings"
	"text/tabwriter"

	"github.com/lockyc/plate/internal/cli"
	"github.com/lockyc/plate/internal/engine"
	"github.com/lockyc/plate/internal/pins"
)

const usage = "doctor [--install]"

// Main runs `plate doctor`.
func Main(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := cli.Flags("doctor", usage, stderr)
	install := fs.Bool("install", false, "download and install the engines plate manages")
	if _, code, ok := cli.Parse(fs, args, 0); !ok {
		return code
	}
	return run(ctx, pins.Engines, *install, httpGet, stdout)
}

type status struct {
	engine      pins.Engine
	found, note string
	ok          bool
}

func run(ctx context.Context, engines []pins.Engine, install bool, get getter, out io.Writer) int {
	failed := false
	if install {
		for _, e := range engines {
			if e.Kind != pins.Managed || !e.Supported() {
				continue
			}
			if _, ok := e.Asset(); !ok {
				continue
			}
			if _, err := engine.Find(e); err == nil {
				continue
			}
			fmt.Fprintf(out, "installing %s %s\n", e.Name, e.Download.Version)
			if err := installManaged(ctx, e, get); err != nil {
				fmt.Fprintf(out, "  failed: %v\n", err)
				failed = true
			}
		}
	}
	var bad []string
	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "engine\twant\tfound\tstatus")
	for _, e := range engines {
		s := check(ctx, e)
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", e.Name, e.Want(), s.found, s.note)
		if !s.ok {
			for _, u := range e.UsedBy {
				if !slices.Contains(bad, u) {
					bad = append(bad, u)
				}
			}
		}
	}
	tw.Flush()
	keyReport(out)
	if len(bad) > 0 || failed {
		if len(bad) > 0 {
			fmt.Fprintf(out, "\nthese commands will not run until the rows above are fixed: %s\n", strings.Join(bad, ", "))
		}
		return 1
	}
	fmt.Fprintln(out, "\nevery engine is ready")
	return 0
}

func check(ctx context.Context, e pins.Engine) status {
	s := status{engine: e, found: "-"}
	if !e.Supported() {
		s.note, s.ok = "not used on "+runtime.GOOS, true
		return s
	}
	path, err := engine.Find(e)
	if err != nil {
		var nie *engine.NotInstalledError
		if errors.As(err, &nie) {
			s.note = "missing: " + nie.Hint
		} else {
			s.note = err.Error()
		}
		return s
	}
	if o := os.Getenv(engine.EnvOverride(e.Name)); o != "" {
		s.found = "override " + path
	}
	if e.Kind == pins.Managed {
		if s.found == "-" {
			s.found = e.Download.Version
		}
		s.note, s.ok = "ok", true
		return s
	}
	v, err := engine.Version(ctx, e)
	if err != nil {
		s.found, s.note = "?", err.Error()
		return s
	}
	if s.found == "-" {
		s.found = v
	} else {
		s.found = v + " (" + s.found + ")"
	}
	if !pins.AtLeast(v, e.Min) {
		s.note = "too old: " + e.Hint()
		return s
	}
	for _, c := range e.Capabilities {
		has, err := engine.Capable(ctx, e, c)
		if err != nil {
			s.note = err.Error()
			return s
		}
		if !has {
			s.note = "missing " + c.Token + " (" + c.For + ")"
			return s
		}
	}
	s.note, s.ok = "ok", true
	return s
}

// keyReport prints whether each image provider's API key is set. A missing
// key never fails doctor: only `generate` needs one, and the value is never
// printed.
func keyReport(out io.Writer) {
	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "\nprovider\tkey")
	seen := map[string]bool{}
	for _, m := range pins.ImageModels {
		if seen[m.KeyEnv] {
			continue
		}
		seen[m.KeyEnv] = true
		state := "set"
		if _, err := m.Key(); err != nil {
			state = "not set"
		}
		fmt.Fprintf(tw, "%s\t%s\n", m.Provider, state)
	}
	tw.Flush()
}
