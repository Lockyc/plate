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
	"time"

	"github.com/lockyc/plate/internal/cli"
	"github.com/lockyc/plate/internal/ml"
	"github.com/lockyc/plate/internal/outdir"
	"github.com/lockyc/plate/internal/pano"
	"github.com/lockyc/plate/internal/pins"
	"github.com/lockyc/plate/internal/tsv"
)

// maxGapSteps is how many median steps apart two frames of a --track run
// may be: a longer gap is footage missing, across which every track would
// break and be counted again.
const maxGapSteps = 5

// trackFrame is the time a --track run allows each frame: four times the
// 1.9 s a 2880 frame takes on an M3 Max, for slower machines.
const trackFrame = 8 * time.Second

// trackBudget is how long a --track run of n frames may take: trackFrame a
// frame, plus ml.Timeout for a first run's downloads.
func trackBudget(n int) time.Duration {
	return ml.Timeout + time.Duration(n)*trackFrame
}

// panoFrame is one row of `plate pano --every`'s pano.tsv.
type panoFrame struct {
	path string
	t    float64 // seconds into the clip
	raw  string  // t as pano wrote it
}

// notFrames is readPano's refusal of a pano.tsv that does not list frames.
const notFrames = "--track reads `pano --every` frames, not a `--video` run"

// readPano reads dir/pano.tsv: its frames in time order, each an image
// file present and listed once, no two at one time and no gap over
// maxGapSteps median steps.
func readPano(dir string) ([]panoFrame, error) {
	path := filepath.Join(dir, "pano.tsv")
	if _, err := os.Stat(path); err != nil {
		return nil, fmt.Errorf("%s: --track takes a directory of `plate pano --every` frames and its pano.tsv: %w", dir, err)
	}
	t, err := tsv.Read(path, pano.ColFile, pano.ColT)
	if err != nil {
		return nil, err
	}
	var fs []panoFrame
	seen := map[string]bool{}
	for i := range t.Rows {
		file, raw := t.Get(i, pano.ColFile), t.Get(i, pano.ColT)
		if !slices.Contains(images, strings.ToLower(filepath.Ext(file))) {
			return nil, fmt.Errorf("%s lists %s, not a %s frame: %s", path, file, strings.Join(images, ", "), notFrames)
		}
		if seen[file] {
			return nil, fmt.Errorf("%s lists %s twice: %s", path, file, notFrames)
		}
		seen[file] = true
		v, ok := cli.Finite(raw)
		if !ok {
			return nil, fmt.Errorf("%s: %s: t %q is not a number", path, file, raw)
		}
		p := filepath.Join(dir, file)
		if _, err := os.Stat(p); err != nil {
			return nil, fmt.Errorf("%s lists %s: %w", path, file, err)
		}
		fs = append(fs, panoFrame{path: p, t: v, raw: raw})
	}
	if len(fs) < 2 {
		return nil, fmt.Errorf("%s lists %d frames; tracking needs at least two frames", path, len(fs))
	}
	slices.SortStableFunc(fs, func(a, b panoFrame) int { return cmp.Compare(a.t, b.t) })
	steps := make([]float64, len(fs)-1)
	for i := range steps {
		steps[i] = fs[i+1].t - fs[i].t
		if steps[i] <= 0 {
			return nil, fmt.Errorf("%s: %s and %s are both at t %s", path, filepath.Base(fs[i].path), filepath.Base(fs[i+1].path), fs[i].raw)
		}
	}
	sorted := slices.Clone(steps)
	slices.Sort(sorted)
	median := sorted[len(sorted)/2]
	for i, s := range steps {
		if s > maxGapSteps*median+1e-6 {
			return nil, fmt.Errorf("%s: %s s between %s (t %s) and %s (t %s), over %d times the %s s step: footage is missing, and every track would break across it",
				path, strconv.FormatFloat(s, 'f', -1, 64), filepath.Base(fs[i].path), fs[i].raw, filepath.Base(fs[i+1].path), fs[i+1].raw,
				maxGapSteps, strconv.FormatFloat(median, 'f', -1, 64))
		}
	}
	return fs, nil
}

// writeFrames writes frames as count.py's --frame-list, one "t<TAB>path"
// a line in the order to read them, to a new temporary file the caller
// removes.
func writeFrames(frames []panoFrame) (string, error) {
	var b strings.Builder
	for _, f := range frames {
		p, err := filepath.Abs(f.path)
		if err != nil {
			return "", err
		}
		fmt.Fprintf(&b, "%s\t%s\n", f.raw, p)
	}
	f, err := os.CreateTemp("", "plate-track-frames-*.tsv")
	if err != nil {
		return "", err
	}
	_, err = f.WriteString(b.String())
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(f.Name())
		return "", err
	}
	return f.Name(), nil
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
	list, err := writeFrames(frames)
	if err != nil {
		return err
	}
	defer os.Remove(list)
	inputs := []string{list}
	for _, f := range frames {
		inputs = append(inputs, f.path)
	}
	args := append(j.models(d),
		"--track", "--min-seconds", fmt.Sprint(j.minSeconds),
		"--reid", pins.CountReID.Repo, "--reid-revision", pins.CountReID.Revision, "--reid-file", pins.CountReID.File,
		"--frame-list", list)
	trackTSV, distinctTSV := filepath.Join(d, "track.tsv"), filepath.Join(d, "distinct.tsv")
	if _, err := ml.RunScript(ctx, "count.py", args, inputs, []string{trackTSV, distinctTSV}, ml.WithTimeout(trackBudget(len(frames)))); err != nil {
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
func readDistinct(path string) (map[string]string, error) {
	t, err := tsv.Read(path, "low", "high", "tracks", "min_seconds", "untracked_share")
	if err != nil {
		return nil, err
	}
	if len(t.Rows) != 1 {
		return nil, fmt.Errorf("%s: want a header and one row, not %d rows", path, len(t.Rows))
	}
	m := map[string]string{}
	for _, h := range t.Head {
		m[h] = t.Get(0, h)
	}
	s, ok := cli.Finite(m["untracked_share"])
	if !ok {
		return nil, fmt.Errorf("%s: untracked_share %q is not a number", path, m["untracked_share"])
	}
	m["untracked_pct"] = strconv.FormatFloat(s*100, 'f', 0, 64)
	return m, nil
}
