// Package clip checks a demo-clip script and renders a recording into a
// looping clip.
package clip

import (
	"context"
	"fmt"
	"io"

	"github.com/lockyc/plate/internal/cli"
)

const usage = "usage: plate clip <check|render> [flags] [args]\n" +
	"  check [--json] <clip.toml>   validate a script and print it normalised\n"

// Main dispatches the clip verbs.
func Main(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) > 0 {
		switch args[0] {
		case "check":
			return checkMain(args[1:], stdout, stderr)
		case "render":
			// Wired in a later task.
		}
	}
	fmt.Fprint(stderr, usage)
	return cli.VerbUsageCode(args)
}

func checkMain(args []string, stdout, stderr io.Writer) int {
	fs := cli.Flags("clip check", "clip check [--json] <script>", stderr)
	asJSON := fs.Bool("json", false, "print the normalised script as JSON")
	rest, code, ok := cli.Parse(fs, args, 1)
	if !ok {
		return code
	}
	s, err := Load(rest[0])
	if err != nil {
		return cli.Fail(stderr, "clip check", err)
	}
	if *asJSON {
		b, err := s.JSON()
		if err != nil {
			return cli.Fail(stderr, "clip check", err)
		}
		stdout.Write(b)
		return 0
	}
	fmt.Fprintf(stdout, "ok %s\n", s.Name)
	return 0
}
