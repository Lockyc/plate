// Package clip checks a demo-clip script and renders a recording into a
// looping clip.
package clip

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/lockyc/plate/internal/cli"
	"github.com/lockyc/plate/internal/video"
)

const usage = "usage: plate clip <check|render> [flags] [args]\n" +
	"  check [--json] <clip.toml>   validate a script and print it normalised\n" +
	"  render --script FILE --master FILE --events FILE --out DIR [--caption-css FILE]\n" +
	"                               render a recording into the script's clip\n"

// Main dispatches the clip verbs.
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

func renderMain(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := cli.Flags("clip render", "clip render --script FILE --master FILE --events FILE --out DIR [--caption-css FILE]", stderr)
	var o RenderOptions
	fs.StringVar(&o.Script, "script", "", "the clip script (clip.toml)")
	fs.StringVar(&o.Master, "master", "", "the screen recording")
	fs.StringVar(&o.Events, "events", "", "the recording's event log (JSON Lines)")
	fs.StringVar(&o.Out, "out", "", "the directory the clip is written to")
	fs.StringVar(&o.CaptionCSS, "caption-css", "", "a stylesheet loaded after plate's caption style")
	if _, code, ok := cli.Parse(fs, args, 0); !ok {
		return code
	}
	for _, f := range []struct{ name, v string }{{"script", o.Script}, {"master", o.Master}, {"events", o.Events}, {"out", o.Out}} {
		if f.v == "" {
			fs.Usage()
			return cli.Usage(stderr, "clip render", "--%s is required", f.name)
		}
	}
	m, err := Render(ctx, o, stderr)
	if err != nil {
		return cli.Fail(stderr, "clip render", err)
	}
	dir, err := filepath.Abs(o.Out)
	if err != nil {
		return cli.Fail(stderr, "clip render", err)
	}
	for _, f := range video.Formats {
		if r, ok := m.Formats[f]; ok {
			fmt.Fprintf(stdout, "wrote %s (%d bytes)\n", filepath.Join(dir, r.File), r.Bytes)
		}
	}
	sidecar := filepath.Join(dir, m.Name+".json")
	st, err := os.Stat(sidecar)
	if err != nil {
		return cli.Fail(stderr, "clip render", err)
	}
	fmt.Fprintf(stdout, "wrote %s (%d bytes)\n", sidecar, st.Size())
	return 0
}
