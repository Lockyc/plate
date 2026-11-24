// Package count counts the people in photos and 360 equirect frames
// (internal/ml/scripts/count.py), writing a count per frame, each counted
// person's position, and on request marked frames to check the count by eye.
// An equirect frame is cut into perspective views first, so a person keeps
// the shape a detector was trained on; the frame's top and bottom caps, where
// the camera's holder stands, are left out.
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
)

const usage = `count [--equirect auto|yes|no] [--nadir-deg 35] [--zenith-deg 30] [--marked] --out <dir> <image|dir>...

Writes <dir>/counts.tsv (frame, detected, density, estimate), the counted
people's positions in <dir>/points/<frame>.tsv (lon and lat in degrees for an
equirect frame, else x and y in pixels), and with --marked each frame with
every counted person dotted and the density overlaid in <dir>/marked/, an
equirect frame's views beside it in <dir>/marked/<frame>/.`

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
	rest, code, ok := cli.Parse(fs, args, -1)
	if !ok {
		return code
	}
	switch {
	case *out == "":
		return cli.Usage(stderr, "count", "--out is required")
	case len(rest) == 0:
		return cli.Usage(stderr, "count", "want at least one image or directory")
	case *eq != "auto" && *eq != "yes" && *eq != "no":
		return cli.Usage(stderr, "count", "--equirect %q: want auto, yes or no", *eq)
	case *nadir < 0 || *nadir >= 90:
		return cli.Usage(stderr, "count", "--nadir-deg %v: want 0 to under 90", *nadir)
	case *zenith < 0 || *zenith >= 90:
		return cli.Usage(stderr, "count", "--zenith-deg %v: want 0 to under 90", *zenith)
	}
	frames, err := expand(rest)
	if err != nil {
		return cli.Fail(stderr, "count", err)
	}
	if err := sameStem(frames); err != nil {
		return cli.Usage(stderr, "count", "%v", err)
	}
	j := job{equirect: *eq, nadir: *nadir, zenith: *zenith, marked: *marked}
	if err := j.run(ctx, frames, *out, stdout); err != nil {
		return cli.Fail(stderr, "count", err)
	}
	return 0
}

type job struct {
	equirect      string
	nadir, zenith float64
	marked        bool
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

// isEquirect reports a 2:1 frame, a pixel either way.
func isEquirect(path string) (bool, error) {
	w, h, err := imgsize.Dims(path)
	if err != nil {
		return false, err
	}
	d := w - 2*h
	return d >= -1 && d <= 1, nil
}

func (j job) run(ctx context.Context, frames []string, out string, stdout io.Writer) error {
	dir, _, err := outdir.Make(out, "")
	if err != nil {
		return err
	}
	if err := outdir.RefuseExisting(dir, []string{"counts.tsv", "points", "marked"}); err != nil {
		return err
	}
	args := []string{
		"--out", dir,
		"--detector", pins.CountDetector.Repo, "--detector-revision", pins.CountDetector.Revision,
		"--density", pins.CountDensity.Repo, "--density-revision", pins.CountDensity.Revision, "--density-file", pins.CountDensity.File,
		"--nadir-deg", fmt.Sprint(j.nadir), "--zenith-deg", fmt.Sprint(j.zenith),
	}
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
	n, peak, at, err := summary(tsv)
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "wrote %s\nframes %d  peak %s at %s\n", tsv, n, peak, at)
	return nil
}

// summary reads counts.tsv: how many frames, and the highest estimate and
// its frame (the first, on a tie).
func summary(tsv string) (n int, peak, at string, err error) {
	b, err := os.ReadFile(tsv)
	if err != nil {
		return 0, "", "", err
	}
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	head := strings.Split(lines[0], "\t")
	fc, ec := slices.Index(head, "frame"), slices.Index(head, "estimate")
	if fc < 0 || ec < 0 {
		return 0, "", "", fmt.Errorf("%s: want frame and estimate columns, header is %q", tsv, lines[0])
	}
	best := -1.0
	for _, l := range lines[1:] {
		r := strings.Split(l, "\t")
		if len(r) <= max(fc, ec) {
			return 0, "", "", fmt.Errorf("%s: short row %q", tsv, l)
		}
		v, ok := cli.Finite(r[ec])
		if !ok {
			return 0, "", "", fmt.Errorf("%s: frame %s: estimate %q is not a number", tsv, r[fc], r[ec])
		}
		n++
		if v > best {
			best, peak, at = v, r[ec], r[fc]
		}
	}
	if n == 0 {
		return 0, "", "", fmt.Errorf("%s has no frames", tsv)
	}
	return n, peak, at, nil
}
