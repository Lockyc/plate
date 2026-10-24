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

const usage = "generate --pose DIR --prompt TEXT --ref IMG [--ref IMG]... [--provider P [--model ID]] [--n 4] [--size 2K] [--aspect W:H]\n       generate --list-models [--provider P]"

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
	if len(args) > 0 && args[0] == "accept" {
		return acceptMain(ctx, args[1:], stdout, stderr)
	}
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
	provider := fs.String("provider", "", "the hosted model's provider: "+strings.Join(providerNames(), ", ")+" (default: "+pins.DefaultImageModel().Provider+")")
	model := fs.String("model", "", "the provider's model id; needs --provider (default: "+pins.DefaultImageModel().ID+" without --provider, else the provider's first)")
	n := cli.Int(fs, "n", 4, "attempts in this round")
	size := fs.String("size", "2K", "output size: 1K, 2K or 4K")
	aspect := fs.String("aspect", "", "aspect ratio W:H (default: the model's choice)")
	list := fs.Bool("list-models", false, "print the model ids, one per line, and exit (every provider's as `provider id` without --provider)")
	if _, code, ok := cli.Parse(fs, args, 0); !ok {
		return code
	}
	if *model != "" && *provider == "" {
		return cli.Usage(stderr, "generate", "--model needs --provider (%s)", strings.Join(providerNames(), ", "))
	}
	if *list {
		return listModels(*provider, stdout, stderr)
	}
	if *pose == "" || *prompt == "" || len(refs) == 0 {
		return cli.Usage(stderr, "generate", "give --pose, --prompt and at least one --ref")
	}
	m := pins.DefaultImageModel()
	if *provider != "" {
		var err error
		if m, err = pins.ImageModelFor(*provider, *model); err != nil {
			return cli.Usage(stderr, "generate", "%v", err)
		}
	}
	if err := genai.Check(m, *n, len(refs), *size); err != nil {
		return cli.Usage(stderr, "generate", "%v", err)
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
	written, unreported, fails, err := r.round(ctx)
	code := 0
	if len(fails) > 0 {
		fmt.Fprintf(stderr, "plate generate: %d of %d attempts failed:\n", len(fails), *n)
		for _, f := range fails {
			if f.CostUSD > 0 {
				fmt.Fprintf(stderr, "  %s (charged $%.2f)\n", f.Reason, f.CostUSD)
			} else {
				fmt.Fprintf(stderr, "  %s\n", f.Reason)
			}
		}
		code = 1
	}
	if unreported == 1 {
		fmt.Fprintf(stderr, "plate generate: %s did not report the cost of 1 attempt; it was charged but is recorded at $0.00\n", m.Provider)
	} else if unreported > 1 {
		fmt.Fprintf(stderr, "plate generate: %s did not report the cost of %d attempts; they were charged but are recorded at $0.00\n", m.Provider, unreported)
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
	if unreported > 0 {
		fmt.Fprintf(stdout, "unreported %d\n", unreported)
	}
	all, err := LoadPose(*pose)
	if err != nil {
		return cli.Fail(stderr, "generate", err)
	}
	charged, err := LoadFailures(*pose)
	if err != nil {
		return cli.Fail(stderr, "generate", err)
	}
	total := PoseTotal(all) + ChargedTotal(charged)
	fmt.Fprintf(stdout, "pose total $%.2f\n", total)
	if total > softLimit {
		fmt.Fprintf(stderr, "plate generate: this pose has cost $%.2f, over $%.2f\n", total, softLimit)
	}
	// The sheet is a view of what is on disk; failing to draw it leaves the
	// paid round as it is.
	if err := writeSheet(ctx, *pose, refs); err != nil {
		fmt.Fprintf(stderr, "plate generate: contact sheet: %v\n", err)
		code = 1
	} else {
		fmt.Fprintln(stdout, filepath.Join(*pose, "sheet.html"))
		fmt.Fprintln(stdout, filepath.Join(*pose, "sheet.png"))
	}
	return code
}

// listModels prints provider's model ids from pins, one per line, default
// first; with no provider it prints every model as `provider id`, the overall
// default marked.
func listModels(provider string, stdout, stderr io.Writer) int {
	if provider == "" {
		for _, m := range pins.ImageModels {
			mark := ""
			if m.Default {
				mark = " (default)"
			}
			fmt.Fprintf(stdout, "%s %s%s\n", m.Provider, m.ID, mark)
		}
		return 0
	}
	var ids []string
	for _, m := range pins.ImageModels {
		if m.Provider == provider {
			ids = append(ids, m.ID)
		}
	}
	if len(ids) == 0 {
		return cli.Usage(stderr, "generate", "unknown provider %q (%s)", provider, strings.Join(providerNames(), ", "))
	}
	for _, id := range ids {
		fmt.Fprintln(stdout, id)
	}
	return 0
}

type run struct {
	pose string
	req  genai.Request
	refs []string
}

// round normalises the refs, claims a round, asks the provider for the
// attempts and writes the ones that came back, and failures.json when the
// provider charged for a failed attempt or may have without reporting it
// (Failure.Unreported). It returns the paths written, how many attempts
// carry an unreported cost (images written with CostBasis "unreported" and
// Unreported failures, even alongside an error, since they were charged),
// and the failures even alongside an error. A round in which nothing came
// back and nothing was or may have been charged leaves nothing; one with
// only such failures is kept with only failures.json.
// When a file cannot be written, every other one still is, the round stays
// NN.partial, and the error names each one that failed and the partial
// folder.
func (r run) round(ctx context.Context) ([]string, int, []genai.Failure, error) {
	p, err := genai.For(r.req.Model.Provider)
	if err != nil {
		return nil, 0, nil, err
	}
	rrs := make([]RefRecord, len(r.refs))
	for i, ref := range r.refs {
		if rrs[i], err = refRecord(ref); err != nil {
			return nil, 0, nil, err
		}
	}
	if r.req.Refs, err = normalise(ctx, r.refs); err != nil {
		return nil, 0, nil, err
	}
	num, dir, err := claimRound(r.pose)
	if err != nil {
		return nil, 0, nil, err
	}
	imgs, fails, err := p.Generate(ctx, r.req)
	charged, unreported := false, 0
	for _, f := range fails {
		charged = charged || f.CostUSD > 0 || f.Unreported
		if f.Unreported {
			unreported++
		}
	}
	if err != nil || (len(imgs) == 0 && !charged) {
		os.Remove(dir)
		return nil, 0, fails, err
	}
	created := now().UTC().Format(time.RFC3339)
	var names []string
	var errs []error
	if charged {
		rf := RoundFailures{Provider: r.req.Model.Provider, Model: r.req.Model.ID, Prompt: r.req.Prompt, Round: num}
		for _, f := range fails {
			rf.Failures = append(rf.Failures, FailureRecord{Index: f.Index, Reason: f.Reason, CostUSD: f.CostUSD, Unreported: f.Unreported})
		}
		if err := writeFailures(dir, rf); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", failuresName, err))
		} else {
			names = append(names, failuresName)
		}
	}
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
		if img.CostBasis == "unreported" {
			unreported++
		}
	}
	final := filepath.Join(roundsDir(r.pose), roundName(num))
	if len(errs) == 0 {
		if err := os.Rename(dir, final); err != nil {
			errs = append(errs, err)
		}
	}
	if len(errs) > 0 {
		return nil, unreported, fails, fmt.Errorf("%w; the round so far is kept in %s", errors.Join(errs...), dir)
	}
	written := make([]string, len(names))
	for i, n := range names {
		written[i] = filepath.Join(final, n)
	}
	return written, unreported, fails, nil
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
