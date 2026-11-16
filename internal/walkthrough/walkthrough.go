package walkthrough

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/lockyc/plate/internal/cli"
)

const usage = "usage: plate walkthrough <check|render> [flags] [args]\n" +
	"  check [--json] <walk.toml>   validate a script and print it normalised\n" +
	"  render --script FILE --base-url URL [--warmup PATH]... --out DIR\n" +
	"                               record the script against a live app as a narrated MP4\n"

// Main dispatches the walkthrough verbs.
func Main(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) > 0 {
		switch args[0] {
		case "check":
			return checkMain(args[1:], stdout, stderr)
		case "render":
			return renderMain(ctx, args[1:], stdout, stderr)
		}
	}
	fmt.Fprint(stderr, usage)
	return cli.VerbUsageCode(args)
}

func renderMain(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := cli.Flags("walkthrough render", "walkthrough render --script FILE --base-url URL [--warmup PATH]... --out DIR", stderr)
	var o RenderOptions
	var warmup cli.Multi
	fs.StringVar(&o.Script, "script", "", "the walkthrough script (walk.toml)")
	fs.StringVar(&o.BaseURL, "base-url", "", "the app's address; the script's URLs resolve against it")
	fs.Var(&warmup, "warmup", "a page opened before recording, never shown (repeatable)")
	fs.StringVar(&o.Out, "out", "", "the directory the video is written to")
	if _, code, ok := cli.Parse(fs, args, 0); !ok {
		return code
	}
	for _, f := range []struct{ name, v string }{{"script", o.Script}, {"base-url", o.BaseURL}, {"out", o.Out}} {
		if f.v == "" {
			return cli.Usage(stderr, "walkthrough render", "--%s is required", f.name)
		}
	}
	o.Warmup = warmup
	m, err := Render(ctx, o)
	if err != nil {
		return cli.Fail(stderr, "walkthrough render", err)
	}
	dir, err := filepath.Abs(o.Out)
	if err != nil {
		return cli.Fail(stderr, "walkthrough render", err)
	}
	for _, f := range formats {
		r := m.Formats[f]
		fmt.Fprintf(stdout, "wrote %s (%d bytes)\n", filepath.Join(dir, r.File), r.Bytes)
	}
	sidecar := filepath.Join(dir, m.Name+".json")
	st, err := os.Stat(sidecar)
	if err != nil {
		return cli.Fail(stderr, "walkthrough render", err)
	}
	fmt.Fprintf(stdout, "wrote %s (%d bytes)\n", sidecar, st.Size())
	return 0
}

func checkMain(args []string, stdout, stderr io.Writer) int {
	fs := cli.Flags("walkthrough check", "walkthrough check [--json] <script>", stderr)
	asJSON := fs.Bool("json", false, "print the normalised script as JSON")
	rest, code, ok := cli.Parse(fs, args, 1)
	if !ok {
		return code
	}
	s, err := Load(rest[0])
	if err != nil {
		return cli.Fail(stderr, "walkthrough check", err)
	}
	b, err := s.JSON()
	if err != nil {
		return cli.Fail(stderr, "walkthrough check", err)
	}
	if *asJSON {
		stdout.Write(b)
		return 0
	}
	fmt.Fprintf(stdout, "ok %s\n", s.Name)
	stdout.Write(b)
	return 0
}
