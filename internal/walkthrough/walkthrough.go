package walkthrough

import (
	"context"
	"fmt"
	"io"

	"github.com/lockyc/plate/internal/cli"
)

const usage = "usage: plate walkthrough <check> [flags] [args]\n" +
	"  check [--json] <walk.toml>   validate a script and print it normalised\n"

// Main dispatches the walkthrough verbs.
func Main(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) > 0 && args[0] == "check" {
		return checkMain(args[1:], stdout, stderr)
	}
	fmt.Fprint(stderr, usage)
	return cli.VerbUsageCode(args)
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
