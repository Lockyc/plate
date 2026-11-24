// Package count counts the people in photos and 360 equirect frames
// (internal/ml/scripts/count.py), writing a count per frame, each counted
// person's position, and on request marked frames to check the count by eye.
// An equirect frame is cut into perspective views first, so a person keeps
// the shape a detector was trained on; the frame's top and bottom caps, where
// the camera's holder stands, are left out. With --track it follows the
// people through a run of `plate pano --every` frames and counts how many
// different people passed, as a range.
package count

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/lockyc/plate/internal/cli"
	"github.com/lockyc/plate/internal/imgsize"
	"github.com/lockyc/plate/internal/ml"
	"github.com/lockyc/plate/internal/outdir"
	"github.com/lockyc/plate/internal/pins"
	"github.com/lockyc/plate/internal/tsv"
)

const usage = `count [--equirect auto|yes|no] [--nadir-deg 35] [--zenith-deg 30] [--marked] --out <dir> <image|dir>...
count --track <pano-dir> [--min-seconds 3] [--nadir-deg 35] [--zenith-deg 30] --out <dir>

Writes <dir>/counts.tsv: per frame the people a detector boxed (detected),
the people counted from the crowd density map, too small or packed to box
(density), and their sum (estimate); the counted people's positions in <dir>/points/<frame>.tsv (lon and lat in degrees for an
equirect frame, else x and y in pixels), and with --marked each frame with
every counted person dotted and the density overlaid in <dir>/marked/, an
equirect frame's views beside it in <dir>/marked/<frame>/.

--track reads a directory of plate pano --every frames (lock mode, so a
person standing still stays still) in the order of its pano.tsv, refusing a
gap of over 5 steps. It writes <dir>/track.tsv, per frame its time t and the
people on screen (the estimate, its density part re-read every 2 s), and
<dir>/distinct.tsv: high, the tracks of boxed people lasting --min-seconds or
longer; low, those tracks after joining the ones whose appearance matches
across a gap of up to 60 s (one person leaving and coming back); tracks,
every track however short; and untracked_share, the share of the people on
screen counted only from the density map, who cannot be tracked and are in
neither low nor high.`

// images are the extensions a directory argument is expanded to.
var images = []string{".jpg", ".jpeg", ".png"}

// Main runs `plate count`.
func Main(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := cli.Flags("count", usage, stderr)
	out := fs.String("out", "", "destination directory (created if missing)")
	eq := fs.String("equirect", "auto", "auto: a 2:1 frame is a 360 equirect; yes or no to say so")
	nadir := cli.Float(fs, "nadir-deg", 35, "leave out this many degrees around straight down, where the camera's holder is")
	zenith := cli.Float(fs, "zenith-deg", 30, "leave out this many degrees around straight up")
	marked := fs.Bool("marked", false, "also write marked frames to check the count by eye")
	trackDir := fs.String("track", "", "a directory of plate pano --every frames to follow the people through")
	minSeconds := cli.Float(fs, "min-seconds", 3, "with --track, the shortest track counted as a person")
	rest, code, ok := cli.Parse(fs, args, -1)
	if !ok {
		return code
	}
	switch {
	case *out == "":
		return cli.Usage(stderr, "count", "--out is required")
	case *trackDir != "" && len(rest) > 0:
		return cli.Usage(stderr, "count", "--track reads its frames from the directory it names; give no other images")
	case *trackDir != "" && *marked:
		return cli.Usage(stderr, "count", "--marked is not available with --track")
	case *trackDir != "" && *eq == "no":
		return cli.Usage(stderr, "count", "--equirect no: --track frames are pano's equirects")
	case *minSeconds < 0:
		return cli.Usage(stderr, "count", "--min-seconds %v: want 0 or more", *minSeconds)
	case *trackDir == "" && len(rest) == 0:
		return cli.Usage(stderr, "count", "want at least one image or directory")
	case *eq != "auto" && *eq != "yes" && *eq != "no":
		return cli.Usage(stderr, "count", "--equirect %q: want auto, yes or no", *eq)
	case *nadir < 0 || *nadir >= 90:
		return cli.Usage(stderr, "count", "--nadir-deg %v: want 0 to under 90", *nadir)
	case *zenith < 0 || *zenith >= 90:
		return cli.Usage(stderr, "count", "--zenith-deg %v: want 0 to under 90", *zenith)
	}
	j := job{equirect: *eq, nadir: *nadir, zenith: *zenith, marked: *marked, minSeconds: *minSeconds}
	if *trackDir != "" {
		if err := j.track(ctx, *trackDir, *out, stdout); err != nil {
			return cli.Fail(stderr, "count", err)
		}
		return 0
	}
	frames, err := expand(rest)
	if err != nil {
		return cli.Fail(stderr, "count", err)
	}
	if err := sameStem(frames); err != nil {
		return cli.Usage(stderr, "count", "%v", err)
	}
	if err := j.run(ctx, frames, *out, stdout); err != nil {
		return cli.Fail(stderr, "count", err)
	}
	return 0
}

type job struct {
	equirect      string
	nadir, zenith float64
	marked        bool
	minSeconds    float64
}

// models are count.py's arguments every run takes: where it writes, the
// pinned models and the cuts.
func (j job) models(dir string) []string {
	return []string{
		"--out", dir,
		"--detector", pins.CountDetector.Repo, "--detector-revision", pins.CountDetector.Revision,
		"--density", pins.CountDensity.Repo, "--density-revision", pins.CountDensity.Revision, "--density-file", pins.CountDensity.File,
		"--nadir-deg", fmt.Sprint(j.nadir), "--zenith-deg", fmt.Sprint(j.zenith),
	}
}

// expand replaces each directory with its images, in name order.
func expand(args []string) ([]string, error) {
	var out []string
	for _, a := range args {
		fi, err := os.Stat(a)
		if err != nil {
			return nil, err
		}
		if !fi.IsDir() {
			out = append(out, a)
			continue
		}
		es, err := os.ReadDir(a)
		if err != nil {
			return nil, err
		}
		n := len(out)
		for _, e := range es {
			if !e.IsDir() && slices.Contains(images, strings.ToLower(filepath.Ext(e.Name()))) {
				out = append(out, filepath.Join(a, e.Name()))
			}
		}
		if len(out) == n {
			return nil, fmt.Errorf("%s holds no %s images", a, strings.Join(images, ", "))
		}
	}
	return out, nil
}

func stem(p string) string { return strings.TrimSuffix(filepath.Base(p), filepath.Ext(p)) }

// sameStem refuses two frames that would write the same counts.tsv row and
// points file.
func sameStem(frames []string) error {
	seen := map[string]string{}
	for _, f := range frames {
		if prev, ok := seen[stem(f)]; ok {
			return fmt.Errorf("%s and %s are both frame %q; count them into separate --out directories", prev, f, stem(f))
		}
		seen[stem(f)] = f
	}
	return nil
}

func isEquirect(path string) (bool, error) {
	w, h, err := imgsize.Dims(path)
	if err != nil {
		return false, err
	}
	return imgsize.IsEquirect(w, h), nil
}

func (j job) run(ctx context.Context, frames []string, out string, stdout io.Writer) error {
	dir, _, err := outdir.Make(out, "")
	if err != nil {
		return err
	}
	if err := outdir.RefuseExisting(dir, []string{"counts.tsv", "points", "marked"}); err != nil {
		return err
	}
	args := j.models(dir)
	if j.marked {
		args = append(args, "--marked")
	}
	for _, f := range frames {
		e := j.equirect == "yes"
		if j.equirect == "auto" {
			if e, err = isEquirect(f); err != nil {
				return err
			}
		}
		if e {
			args = append(args, "--equirect", f)
		} else {
			args = append(args, "--photo", f)
		}
	}
	tsv := filepath.Join(dir, "counts.tsv")
	if _, err := ml.RunScript(ctx, "count.py", args, frames, []string{tsv}); err != nil {
		return err
	}
	n, peak, at, err := peakOf(tsv, "frame", "estimate")
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "wrote %s\nframes %d  peak %s at %s\n", tsv, n, peak, at)
	return nil
}

// peakOf reads a per-frame table (counts.tsv or track.tsv): how many frames,
// and the highest value in column val and its frame's key (the first, on a
// tie).
func peakOf(path, key, val string) (n int, peak, at string, err error) {
	t, err := tsv.Read(path, key, val)
	if err != nil {
		return 0, "", "", err
	}
	best := -1.0
	for i := range t.Rows {
		k, s := t.Get(i, key), t.Get(i, val)
		v, ok := cli.Finite(s)
		if !ok {
			return 0, "", "", fmt.Errorf("%s: %s %s: %s %q is not a number", path, key, k, val, s)
		}
		if v > best {
			best, peak, at = v, s, k
		}
	}
	if len(t.Rows) == 0 {
		return 0, "", "", fmt.Errorf("%s has no frames", path)
	}
	return len(t.Rows), peak, at, nil
}
