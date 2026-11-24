package count

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/lockyc/plate/internal/enginetest"
	"github.com/lockyc/plate/internal/ml"
	"github.com/lockyc/plate/internal/pins"
)

// trackStub writes $TRACK as track.tsv and $DISTINCT as distinct.tsv in --out.
const trackStub = `while [ "$1" != "--out" ]; do shift; done
printf "$TRACK" > "$2/track.tsv"
printf "$DISTINCT" > "$2/distinct.tsv"`

// panoDir writes frames named for times ts and a pano.tsv listing them, as
// `plate pano --every` does.
func panoDir(t *testing.T, ts ...string) string {
	t.Helper()
	dir := t.TempDir()
	tsv := "file\tt\tclock\tyaw\tpitch\troll\n"
	for _, s := range ts {
		name := "f" + strings.ReplaceAll(s, ".", "_") + ".jpg"
		frame(t, dir, name, 400, 200)
		tsv += name + "\t" + s + "\t00:00:00.000\t0\t0\t0\n"
	}
	if err := os.WriteFile(filepath.Join(dir, "pano.tsv"), []byte(tsv), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
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
	if !after(c, "--pano", filepath.Join(dir, "pano.tsv")) {
		t.Errorf("pano.tsv not passed: %q", c)
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
	if r := track(t, "--track", dir); r.code != 0 {
		t.Errorf("code %d, %q", r.code, r.stderr)
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
	repeat := panoDir(t, "0.5", "0.5", "1.0")
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
