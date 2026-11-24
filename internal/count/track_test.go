package count

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/lockyc/plate/internal/enginetest"
	"github.com/lockyc/plate/internal/ml"
	"github.com/lockyc/plate/internal/pano"
	"github.com/lockyc/plate/internal/pins"
)

// trackStub writes $TRACK as track.tsv and $DISTINCT as distinct.tsv in
// --out, and a copy of --frame-list as frame-list.seen.
const trackStub = `out=; list=
while [ $# -gt 0 ]; do
  case "$1" in --out) out=$2 ;; --frame-list) list=$2 ;; esac
  shift
done
printf "$TRACK" > "$out/track.tsv"
printf "$DISTINCT" > "$out/distinct.tsv"
[ -z "$list" ] || cp "$list" "$out/frame-list.seen"`

// panoRows writes a frame for each (file, t) row and a pano.tsv listing
// them, as `plate pano` does.
func panoRows(t *testing.T, rows ...[2]string) string {
	t.Helper()
	dir := t.TempDir()
	var rs []pano.Row
	for _, r := range rows {
		frame(t, dir, r[0], 400, 200)
		v, err := strconv.ParseFloat(r[1], 64)
		if err != nil {
			t.Fatal(err)
		}
		rs = append(rs, pano.Row{File: r[0], T: v, Clock: "00:00:00.000"})
	}
	if err := pano.WriteTSV(filepath.Join(dir, "pano.tsv"), rs); err != nil {
		t.Fatal(err)
	}
	return dir
}

// panoDir is panoRows for frames named for times ts, as `plate pano
// --every` names them.
func panoDir(t *testing.T, ts ...string) string {
	t.Helper()
	var rows [][2]string
	for _, s := range ts {
		rows = append(rows, [2]string{"f" + strings.ReplaceAll(s, ".", "_") + ".jpg", s})
	}
	return panoRows(t, rows...)
}

func track(t *testing.T, args ...string) run {
	t.Helper()
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	if os.Getenv("TRACK") == "" {
		t.Setenv("TRACK", `t\ton_screen\n0.5\t3\n1\t4\n`)
		t.Setenv("DISTINCT", `low\thigh\ttracks\tmin_seconds\tuntracked_share\n5\t6\t9\t3\t0.1\n`)
	}
	r := run{uv: enginetest.Stub(t, "uv", trackStub), out: filepath.Join(t.TempDir(), "out")}
	var o, e bytes.Buffer
	r.code = Main(context.Background(), append([]string{"--out", r.out}, args...), &o, &e)
	r.stdout, r.stderr = o.String(), e.String()
	return r
}

func TestTrackArgs(t *testing.T) {
	dir := panoDir(t, "0.5", "1.0", "1.5")
	r := track(t, "--track", dir, "--min-seconds", "4")
	if r.code != 0 {
		t.Fatalf("code %d: %s", r.code, r.stderr)
	}
	c := r.call(t)
	if !slices.Contains(c, "--track") {
		t.Errorf("no --track: %q", c)
	}
	if slices.Contains(c, "--pano") {
		t.Errorf("pano.tsv passed for count.py to read again: %q", c)
	}
	seen, err := os.ReadFile(filepath.Join(r.out, "frame-list.seen"))
	if err != nil {
		t.Fatalf("no --frame-list: %v", err)
	}
	abs, _ := filepath.Abs(dir)
	if want := "0.500\t" + filepath.Join(abs, "f0_5.jpg") + "\n1.000\t" + filepath.Join(abs, "f1_0.jpg") + "\n1.500\t" + filepath.Join(abs, "f1_5.jpg") + "\n"; string(seen) != want {
		t.Errorf("frame list =\n%s\nwant\n%s", seen, want)
	}
	if slices.Contains(c, "--equirect") || slices.Contains(c, "--photo") || slices.Contains(c, "--times") {
		t.Errorf("frames passed one by one: %q", c)
	}
	for _, want := range [][2]string{
		{"--min-seconds", "4"},
		{"--reid", pins.CountReID.Repo}, {"--reid-revision", pins.CountReID.Revision}, {"--reid-file", pins.CountReID.File},
		{"--detector", pins.CountDetector.Repo}, {"--density", pins.CountDensity.Repo},
	} {
		if !after(c, want[0], want[1]) {
			t.Errorf("args lack %s %s: %q", want[0], want[1], c)
		}
	}
	if !strings.Contains(r.stdout, "frames 2  peak 4 at 1 s") || !strings.Contains(r.stdout, "distinct 5 to 6 people (9 tracks, 3 s or longer; 10% of those on screen untracked)") {
		t.Errorf("stdout %q", r.stdout)
	}
	for _, f := range []string{"track.tsv", "distinct.tsv"} {
		if !strings.Contains(r.stdout, filepath.Join(r.out, f)) {
			t.Errorf("stdout does not name %s: %q", f, r.stdout)
		}
	}
}

// TestTrackGapInTimeOrder: pano.tsv rows out of order are checked in time
// order, so no gap is reported between rows that are neighbours only in the
// file.
func TestTrackGapInTimeOrder(t *testing.T) {
	dir := panoDir(t, "3.0", "0.5", "1.0", "1.5", "2.0", "2.5")
	r := track(t, "--track", dir)
	if r.code != 0 {
		t.Fatalf("code %d, %q", r.code, r.stderr)
	}
	seen, _ := os.ReadFile(filepath.Join(r.out, "frame-list.seen"))
	if lines := strings.Split(string(seen), "\n"); len(lines) < 6 || !strings.HasPrefix(lines[0], "0.500\t") || !strings.HasPrefix(lines[5], "3.000\t") {
		t.Errorf("frame list not in time order:\n%s", seen)
	}
}

// A `pano --video` run's pano.tsv names pano.mp4 on every row: refused
// before the models load.
func TestTrackRefusesVideoRun(t *testing.T) {
	video := panoRows(t, [2]string{"pano.mp4", "0.0"}, [2]string{"pano.mp4", "0.033"})
	twice := panoRows(t, [2]string{"a.jpg", "0.0"}, [2]string{"a.jpg", "0.5"}, [2]string{"b.jpg", "1.0"})
	for name, dir := range map[string]string{"pano.mp4": video, "a file twice": twice} {
		r := track(t, "--track", dir)
		if r.code != 1 || !strings.Contains(r.stderr, "--track reads `pano --every` frames, not a `--video` run") {
			t.Errorf("%s: code %d, %q", name, r.code, r.stderr)
		}
		if len(enginetest.Calls(t, r.uv)) != 0 {
			t.Errorf("%s: uv ran", name)
		}
	}
}

// TestTrackBudget: a run is allowed trackFrame a frame on top of a first
// run's ml.Timeout, so a long run is not cut off at the default.
func TestTrackBudget(t *testing.T) {
	if got := trackBudget(0); got != ml.Timeout {
		t.Errorf("0 frames: %v", got)
	}
	if got, want := trackBudget(3472), ml.Timeout+3472*8*time.Second; got != want {
		t.Errorf("3472 frames: %v, want %v", got, want)
	}
	if trackBudget(3472) <= time.Duration(3472)*1900*time.Millisecond+ml.Timeout {
		t.Error("budget below the measured 1.9 s a frame")
	}
}

func TestTrackRefusesGap(t *testing.T) {
	dir := panoDir(t, "0.5", "1.0", "1.5", "2.0", "5.0", "5.5")
	r := track(t, "--track", dir)
	if r.code != 1 || !strings.Contains(r.stderr, "f2_0.jpg") || !strings.Contains(r.stderr, "f5_0.jpg") || !strings.Contains(r.stderr, "3 s") {
		t.Errorf("code %d, %q", r.code, r.stderr)
	}
	if len(enginetest.Calls(t, r.uv)) != 0 {
		t.Error("uv ran")
	}
}

// TestTrackAllowsSmallGap: a gap of five steps is not refused.
func TestTrackAllowsSmallGap(t *testing.T) {
	dir := panoDir(t, "0.5", "1.0", "1.5", "2.0", "4.5", "5.0")
	if r := track(t, "--track", dir); r.code != 0 {
		t.Errorf("code %d, %q", r.code, r.stderr)
	}
}

func TestTrackRefuses(t *testing.T) {
	noTSV := t.TempDir()
	frame(t, noTSV, "a.jpg", 400, 200)
	missing := panoDir(t, "0.5", "1.0")
	os.Remove(filepath.Join(missing, "f1_0.jpg"))
	repeat := panoRows(t, [2]string{"a.jpg", "0.5"}, [2]string{"b.jpg", "0.5"}, [2]string{"c.jpg", "1.0"})
	one := panoDir(t, "0.5")
	for name, tc := range map[string]struct {
		args []string
		code int
		want string
	}{
		"no pano.tsv":    {[]string{"--track", noTSV}, 1, "pano.tsv"},
		"missing frame":  {[]string{"--track", missing}, 1, "f1_0.jpg"},
		"repeated time":  {[]string{"--track", repeat}, 1, "0.5"},
		"one frame":      {[]string{"--track", one}, 1, "two frames"},
		"positional too": {[]string{"--track", missing, "x.jpg"}, 2, "--track"},
		"marked":         {[]string{"--track", missing, "--marked"}, 2, "--marked"},
		"photo":          {[]string{"--track", missing, "--equirect", "no"}, 2, "--equirect"},
		"min-seconds":    {[]string{"--track", missing, "--min-seconds", "-1"}, 2, "--min-seconds"},
	} {
		r := track(t, tc.args...)
		if r.code != tc.code || !strings.Contains(r.stderr, tc.want) {
			t.Errorf("%s: code %d, %q", name, r.code, r.stderr)
		}
		if len(enginetest.Calls(t, r.uv)) != 0 {
			t.Errorf("%s: uv ran", name)
		}
	}
}

func TestTrackRefusesUsedOut(t *testing.T) {
	dir := panoDir(t, "0.5", "1.0")
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	uv := enginetest.Stub(t, "uv", trackStub)
	out := t.TempDir()
	os.WriteFile(filepath.Join(out, "distinct.tsv"), []byte("old"), 0o644)
	var o, e bytes.Buffer
	if code := Main(context.Background(), []string{"--out", out, "--track", dir}, &o, &e); code != 1 || !strings.Contains(e.String(), "distinct.tsv") {
		t.Errorf("code %d, %q", code, e.String())
	}
	if len(enginetest.Calls(t, uv)) != 0 {
		t.Error("uv ran")
	}
}
