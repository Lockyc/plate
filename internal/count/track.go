package count

import (
	"cmp"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/lockyc/plate/internal/cli"
	"github.com/lockyc/plate/internal/ml"
	"github.com/lockyc/plate/internal/outdir"
	"github.com/lockyc/plate/internal/pins"
)

// maxGapSteps is how many median steps apart two frames of a --track run
// may be: a longer gap is footage missing, across which every track would
// break and be counted again.
const maxGapSteps = 5

// panoFrame is one row of `plate pano --every`'s pano.tsv.
type panoFrame struct {
	path string
	t    float64 // seconds into the clip
	raw  string  // t as pano wrote it
}

// readPano reads dir/pano.tsv: its frames in time order, each file present,
// no two at one time and no gap over maxGapSteps median steps.
func readPano(dir string) ([]panoFrame, error) {
	tsv := filepath.Join(dir, "pano.tsv")
	b, err := os.ReadFile(tsv)
	if err != nil {
		return nil, fmt.Errorf("%s: --track takes a directory of `plate pano --every` frames and its pano.tsv: %w", dir, err)
	}
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	head := strings.Split(lines[0], "\t")
	fc, tc := slices.Index(head, "file"), slices.Index(head, "t")
	if fc < 0 || tc < 0 {
		return nil, fmt.Errorf("%s: want file and t columns, header is %q", tsv, lines[0])
	}
	var fs []panoFrame
	for _, l := range lines[1:] {
		r := strings.Split(l, "\t")
		if len(r) <= max(fc, tc) {
			return nil, fmt.Errorf("%s: short row %q", tsv, l)
		}
		v, ok := cli.Finite(r[tc])
		if !ok {
			return nil, fmt.Errorf("%s: %s: t %q is not a number", tsv, r[fc], r[tc])
		}
		p := filepath.Join(dir, r[fc])
		if _, err := os.Stat(p); err != nil {
			return nil, fmt.Errorf("%s lists %s: %w", tsv, r[fc], err)
		}
		fs = append(fs, panoFrame{path: p, t: v, raw: r[tc]})
	}
	if len(fs) < 2 {
		return nil, fmt.Errorf("%s lists %d frames; tracking needs at least two frames", tsv, len(fs))
	}
	slices.SortStableFunc(fs, func(a, b panoFrame) int { return cmp.Compare(a.t, b.t) })
	steps := make([]float64, len(fs)-1)
	for i := range steps {
		steps[i] = fs[i+1].t - fs[i].t
		if steps[i] <= 0 {
			return nil, fmt.Errorf("%s: %s and %s are both at t %s", tsv, filepath.Base(fs[i].path), filepath.Base(fs[i+1].path), fs[i].raw)
		}
	}
	sorted := slices.Clone(steps)
	slices.Sort(sorted)
	median := sorted[len(sorted)/2]
	for i, s := range steps {
		if s > maxGapSteps*median+1e-6 {
			return nil, fmt.Errorf("%s: %s s between %s (t %s) and %s (t %s), over %d times the %s s step: footage is missing, and every track would break across it",
				tsv, strconv.FormatFloat(s, 'f', -1, 64), filepath.Base(fs[i].path), fs[i].raw, filepath.Base(fs[i+1].path), fs[i+1].raw,
				maxGapSteps, strconv.FormatFloat(median, 'f', -1, 64))
		}
	}
	return fs, nil
}

func (j job) track(ctx context.Context, dir, out string, stdout io.Writer) error {
	frames, err := readPano(dir)
	if err != nil {
		return err
	}
	d, _, err := outdir.Make(out, "")
	if err != nil {
		return err
	}
	if err := outdir.RefuseExisting(d, []string{"track.tsv", "distinct.tsv"}); err != nil {
		return err
	}
	times := make([]string, len(frames))
	inputs := make([]string, len(frames))
	for i, f := range frames {
		times[i], inputs[i] = f.raw, f.path
	}
	args := append(j.models(d),
		"--track", "--min-seconds", fmt.Sprint(j.minSeconds),
		"--reid", pins.CountReID.Repo, "--reid-revision", pins.CountReID.Revision, "--reid-file", pins.CountReID.File,
		"--times", strings.Join(times, ","))
	for _, f := range inputs {
		args = append(args, "--equirect", f)
	}
	trackTSV, distinctTSV := filepath.Join(d, "track.tsv"), filepath.Join(d, "distinct.tsv")
	if _, err := ml.RunScript(ctx, "count.py", args, inputs, []string{trackTSV, distinctTSV}); err != nil {
		return err
	}
	n, peak, at, err := peakOf(trackTSV, "t", "on_screen")
	if err != nil {
		return err
	}
	dist, err := readDistinct(distinctTSV)
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "wrote %s\nwrote %s\nframes %d  peak %s at %s s\ndistinct %s to %s people (%s tracks, %s s or longer; %s%% of those on screen untracked)\n",
		trackTSV, distinctTSV, n, peak, at, dist["low"], dist["high"], dist["tracks"], dist["min_seconds"], dist["untracked_pct"])
	return nil
}

// readDistinct reads distinct.tsv's one row, with untracked_share also as a
// whole percentage (untracked_pct).
func readDistinct(tsv string) (map[string]string, error) {
	b, err := os.ReadFile(tsv)
	if err != nil {
		return nil, err
	}
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	if len(lines) != 2 {
		return nil, fmt.Errorf("%s: want a header and one row", tsv)
	}
	head, row := strings.Split(lines[0], "\t"), strings.Split(lines[1], "\t")
	if len(head) != len(row) {
		return nil, fmt.Errorf("%s: row %q does not match header %q", tsv, lines[1], lines[0])
	}
	m := map[string]string{}
	for i, h := range head {
		m[h] = row[i]
	}
	for _, k := range []string{"low", "high", "tracks", "min_seconds", "untracked_share"} {
		if _, ok := m[k]; !ok {
			return nil, fmt.Errorf("%s: no %s column, header is %q", tsv, k, lines[0])
		}
	}
	s, ok := cli.Finite(m["untracked_share"])
	if !ok {
		return nil, fmt.Errorf("%s: untracked_share %q is not a number", tsv, m["untracked_share"])
	}
	m["untracked_pct"] = strconv.FormatFloat(s*100, 'f', 0, 64)
	return m, nil
}
