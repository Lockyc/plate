// Package generate makes attempts at a character pose from reference images
// through a hosted image model (internal/genai). Each run is one round in a
// pose folder: every attempt image with its provenance record beside it. A
// person picks among the attempts; nothing here scores or filters them.
package generate

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/lockyc/plate/internal/atomicfile"
	"github.com/lockyc/plate/internal/cli"
	"github.com/lockyc/plate/internal/engine"
	"github.com/lockyc/plate/internal/frame"
	"github.com/lockyc/plate/internal/genai"
	"github.com/lockyc/plate/internal/imgsize"
	"github.com/lockyc/plate/internal/pins"
)

const usage = "generate --pose DIR --prompt TEXT --ref IMG [--ref IMG]... --provider P [--model ID] [--n 4] [--size 2K] [--aspect W:H]"

// maxRefEdge is the longest edge a reference is sent at; a larger one is
// downscaled, since the models gain nothing from it and the upload grows.
const maxRefEdge = 2048

// softLimit is the pose total, in USD, past which each round warns. It is
// a warning, never a cap.
const softLimit = 10.00

// now is the clock records are stamped with; a var so tests can fix it.
var now = time.Now

// Main runs `plate generate`.
func Main(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	return roundMain(ctx, args, stdout, stderr)
}

// providerNames lists each provider in pins.ImageModels once, in table order.
func providerNames() []string {
	var names []string
	seen := map[string]bool{}
	for _, m := range pins.ImageModels {
		if !seen[m.Provider] {
			seen[m.Provider] = true
			names = append(names, m.Provider)
		}
	}
	return names
}

func roundMain(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := cli.Flags("generate", usage, stderr)
	pose := fs.String("pose", "", "the pose folder; each run adds a round under DIR/rounds (required)")
	prompt := fs.String("prompt", "", "what to make of the references (required)")
	var refs cli.Multi
	fs.Var(&refs, "ref", "a reference image; repeat for more (at least one)")
	provider := fs.String("provider", "", "the hosted model's provider: "+strings.Join(providerNames(), ", ")+" (required)")
	model := fs.String("model", "", "the provider's model id (default: the provider's first)")
	n := cli.Int(fs, "n", 4, "attempts in this round")
	size := fs.String("size", "2K", "output size: 1K, 2K or 4K")
	aspect := fs.String("aspect", "", "aspect ratio W:H (default: the model's choice)")
	if _, code, ok := cli.Parse(fs, args, 0); !ok {
		return code
	}
	if *provider == "" {
		return cli.Usage(stderr, "generate", "give --provider; there is no default provider yet (%s)", strings.Join(providerNames(), ", "))
	}
	if *pose == "" || *prompt == "" || len(refs) == 0 {
		return cli.Usage(stderr, "generate", "give --pose, --prompt and at least one --ref")
	}
	if *n < 1 {
		return cli.Usage(stderr, "generate", "--n must be at least 1")
	}
	m, err := pins.ImageModelFor(*provider, *model)
	if err != nil {
		return cli.Usage(stderr, "generate", "%v", err)
	}
	if m.MaxRefs > 0 && len(refs) > m.MaxRefs {
		return cli.Usage(stderr, "generate", "%s takes at most %d references; %d given", m.ID, m.MaxRefs, len(refs))
	}
	if _, err := m.Key(); err != nil {
		return cli.Fail(stderr, "generate", err)
	}
	left, err := partialRounds(*pose)
	if err != nil {
		return cli.Fail(stderr, "generate", err)
	}
	for _, p := range left {
		fmt.Fprintf(stderr, "plate generate: %s holds an unfinished round; its images are kept\n", p)
	}
	r := run{pose: *pose, req: genai.Request{Model: m, Prompt: *prompt, N: *n, Size: *size, Aspect: *aspect}, refs: refs}
	written, fails, err := r.round(ctx)
	code := 0
	if len(fails) > 0 {
		fmt.Fprintf(stderr, "plate generate: %d of %d attempts failed:\n", len(fails), *n)
		for _, f := range fails {
			fmt.Fprintf(stderr, "  %s\n", f.Reason)
		}
		code = 1
	}
	if err != nil {
		return cli.Fail(stderr, "generate", err)
	}
	if len(written) == 0 {
		return code
	}
	for _, p := range written {
		fmt.Fprintln(stdout, p)
	}
	all, err := LoadPose(*pose)
	if err != nil {
		return cli.Fail(stderr, "generate", err)
	}
	total := PoseTotal(all)
	fmt.Fprintf(stdout, "pose total $%.2f\n", total)
	if total > softLimit {
		fmt.Fprintf(stderr, "plate generate: this pose has cost $%.2f, over $%.2f\n", total, softLimit)
	}
	// The sheet is a view of what is on disk; failing to draw it leaves the
	// paid round as it is.
	if err := writeSheet(ctx, *pose, refs); err != nil {
		fmt.Fprintf(stderr, "plate generate: contact sheet: %v\n", err)
		code = 1
	}
	return code
}

type run struct {
	pose string
	req  genai.Request
	refs []string
}

// round normalises the refs, claims a round, asks the provider for the
// attempts and writes the ones that came back. It returns the paths
// written, and the failures even alongside an error. A round in which no
// attempt succeeded leaves nothing. When an attempt cannot be written, every
// other attempt still is, the round stays NN.partial, and the error names
// each attempt that failed and the partial folder.
func (r run) round(ctx context.Context) ([]string, []genai.Failure, error) {
	p, err := genai.For(r.req.Model.Provider)
	if err != nil {
		return nil, nil, err
	}
	rrs := make([]RefRecord, len(r.refs))
	for i, ref := range r.refs {
		if rrs[i], err = refRecord(ref); err != nil {
			return nil, nil, err
		}
	}
	if r.req.Refs, err = normalise(ctx, r.refs); err != nil {
		return nil, nil, err
	}
	num, dir, err := claimRound(r.pose)
	if err != nil {
		return nil, nil, err
	}
	imgs, fails, err := p.Generate(ctx, r.req)
	if err != nil || len(imgs) == 0 {
		os.Remove(dir)
		return nil, fails, err
	}
	created := now().UTC().Format(time.RFC3339)
	var names []string
	var errs []error
	for i, img := range imgs {
		base := attemptBase(i + 1)
		if err := atomicfile.WriteFile(filepath.Join(dir, base+".png"), img.PNG, 0o644); err != nil {
			errs = append(errs, fmt.Errorf("attempt %d: %w", i+1, err))
			continue
		}
		rec := Record{
			Provider: r.req.Model.Provider, Model: r.req.Model.ID, Prompt: r.req.Prompt,
			Size: r.req.Size, Aspect: r.req.Aspect, Refs: rrs, Round: num, Attempt: i + 1,
			CostUSD: img.CostUSD, CostBasis: img.CostBasis, ProviderID: img.ProviderID, Created: created,
		}
		if err := writeRecord(filepath.Join(dir, base+".json"), rec); err != nil {
			errs = append(errs, fmt.Errorf("attempt %d record: %w", i+1, err))
			continue
		}
		names = append(names, base+".png", base+".json")
	}
	final := filepath.Join(roundsDir(r.pose), roundName(num))
	if len(errs) == 0 {
		if err := os.Rename(dir, final); err != nil {
			errs = append(errs, err)
		}
	}
	if len(errs) > 0 {
		return nil, fails, fmt.Errorf("%w; the round so far is kept in %s", errors.Join(errs...), dir)
	}
	written := make([]string, len(names))
	for i, n := range names {
		written[i] = filepath.Join(final, n)
	}
	return written, fails, nil
}

// normalise frames each ref as an 8-bit sRGB RGBA PNG and downscales any
// whose long edge is over maxRefEdge.
func normalise(ctx context.Context, paths []string) ([]genai.Ref, error) {
	tmp, err := os.MkdirTemp("", "plate-generate-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmp)
	refs := make([]genai.Ref, len(paths))
	for i, in := range paths {
		out := filepath.Join(tmp, "ref"+strconv.Itoa(i)+".png")
		if err := frame.Write(ctx, in, out, frame.Options{}); err != nil {
			return nil, err
		}
		w, h, err := imgsize.Dims(out)
		if err != nil {
			return nil, err
		}
		if max(w, h) > maxRefEdge {
			small := filepath.Join(tmp, "ref"+strconv.Itoa(i)+"-small.png")
			edge := strconv.Itoa(maxRefEdge)
			if _, err := engine.Run(ctx, engine.Cmd{Engine: "magick", Args: []string{out, "-resize", edge + "x" + edge, "PNG32:" + small},
				Inputs: []string{out}, Outputs: []string{small}}); err != nil {
				return nil, err
			}
			out = small
		}
		b, err := os.ReadFile(out)
		if err != nil {
			return nil, err
		}
		refs[i] = genai.Ref{Path: in, PNG: b}
	}
	return refs, nil
}
