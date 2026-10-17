// Package inpaint fills a masked region with LaMa (iopaint). It synthesises
// pixels, so plate.toml can forbid it. LaMa needs the same kind of texture
// around a hole: next to a large flat area it fills flat grey. A hole in a
// flat ground is infill's job.
package inpaint

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/lockyc/plate/internal/cli"
	"github.com/lockyc/plate/internal/engine"
	"github.com/lockyc/plate/internal/frame"
	"github.com/lockyc/plate/internal/imgsize"
	"github.com/lockyc/plate/internal/ml"
	"github.com/lockyc/plate/internal/pins"
)

const usage = "inpaint --mask mask.png <in> <out.png>"

// Main runs `plate inpaint`.
func Main(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := cli.Flags("inpaint", usage, stderr)
	mask := fs.String("mask", "", "white where LaMa fills, same size as the image (required)")
	rest, code, ok := cli.Parse(fs, args, 2)
	if !ok {
		return code
	}
	if *mask == "" || strings.ToLower(filepath.Ext(rest[1])) != ".png" {
		return cli.Usage(stderr, "inpaint", "give --mask and an output ending .png")
	}
	if err := run(ctx, rest[0], *mask, rest[1]); err != nil {
		return cli.Fail(stderr, "inpaint", err)
	}
	fmt.Fprintf(stdout, "wrote %s\n", rest[1])
	return 0
}

func run(ctx context.Context, in, mask, out string) error {
	// The step that writes <out> reads a temp file, so no engine call names
	// both <in> or <mask> and <out>; this is the one place that can refuse
	// before the temp work runs.
	if engine.SameFile(in, out) || engine.SameFile(mask, out) {
		return fmt.Errorf("%s is also an input; write the result elsewhere", out)
	}
	tmp, err := os.MkdirTemp("", "plate-inpaint-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	// LaMa reads the normalised frame under a fixed name, so iopaint's
	// output name is known. The mask is framed the same way, so a mask drawn
	// on the displayed image lines up, and as 8-bit: iopaint reads it with
	// PIL's convert("L"), which clips a 16-bit grey PNG to white.
	src, m, outDir := filepath.Join(tmp, "in.png"), filepath.Join(tmp, "mask.png"), filepath.Join(tmp, "out")
	if err := os.Mkdir(outDir, 0o755); err != nil {
		return err
	}
	if _, err := frame.RequireOpaque(ctx, in); err != nil {
		return err
	}
	if err := frame.Write(ctx, in, src, frame.Options{Opaque: true}); err != nil {
		return err
	}
	if err := frame.Write(ctx, mask, m, frame.Options{Opaque: true}); err != nil {
		return err
	}
	iw, ih, err := imgsize.Dims(src)
	if err != nil {
		return err
	}
	mw, mh, err := imgsize.Dims(m)
	if err != nil {
		return err
	}
	if iw != mw || ih != mh {
		return fmt.Errorf("the mask is %dx%d and the image %dx%d as displayed; iopaint would resize the mask silently", mw, mh, iw, ih)
	}
	device, err := ml.ToolDevice(ctx, pins.IOPaint)
	if err != nil {
		return err
	}
	painted := filepath.Join(outDir, "in.png")
	if _, err := ml.RunTool(ctx, pins.IOPaint, "iopaint", []string{"run", "--model=lama", "--device=" + device,
		"--image=" + src, "--mask=" + m, "--output=" + outDir}, []string{src, m}, []string{painted}); err != nil {
		return err
	}
	_, err = engine.Run(ctx, engine.Cmd{Engine: "magick", Args: []string{painted, out}, Inputs: []string{painted}, Outputs: []string{out}})
	return err
}
