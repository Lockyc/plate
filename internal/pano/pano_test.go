package pano

import (
	"bytes"
	"context"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lockyc/plate/internal/enginetest"
	"github.com/lockyc/plate/internal/insv"
	"github.com/lockyc/plate/internal/insv/insvtest"
)

const x3Offset = "2_1440_1440_1440_0_0_90_1440_4320_1440_0_0_90_5760_2880_0"

// clip writes a two-file clip of seconds at 30 fps, held upright and still,
// into a fresh directory, and returns the front file and the back file.
func clip(t *testing.T, name string, seconds float64) (front, back string) {
	t.Helper()
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	dir := t.TempDir()
	front = filepath.Join(dir, name)
	back = filepath.Join(dir, strings.Replace(name, "_00_", "_10_", 1))
	for _, p := range []string{front, back} {
		if err := os.WriteFile(p, []byte("video"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	m := insv.Meta{Model: "Insta360 X3", Serial: "TEST", Width: 2880, Height: 2880, FPS: 30}
	for i := 0; float64(i) <= seconds*100; i++ {
		m.IMU = append(m.IMU, insv.IMU{T: float64(i) / 100, Accel: [3]float64{0, 0, -insv.StandardGravity}})
	}
	if err := insvtest.Write(front, m, insvtest.Options{Offset: x3Offset}); err != nil {
		t.Fatal(err)
	}
	return front, back
}

// stubs stand in for ffprobe, stating a clip of seconds at 30 fps, and for
// ffmpeg, which writes its last argument.
func stubs(t *testing.T, seconds string) (probe, mpeg string) {
	probe = enginetest.Stub(t, "ffprobe", `printf '{"streams":[{"r_frame_rate":"30/1","duration":"`+seconds+`"}]}'`)
	mpeg = enginetest.Stub(t, "ffmpeg", `for a; do last=$a; done; printf x > "$last"`)
	return probe, mpeg
}

func TestEveryShortClip(t *testing.T) {
	front, _ := clip(t, "VID_20261122_120701_00_001.insv", 3)
	_, mpeg := stubs(t, "3.0")
	out := t.TempDir()
	var stdout, stderr bytes.Buffer
	if code := Main(context.Background(), []string{"--every", "30s", "--width", "256", "--out", out, front}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	calls := enginetest.Calls(t, mpeg)
	if len(calls) != 1 {
		t.Fatalf("%d ffmpeg calls, want 1", len(calls))
	}
	args := strings.Join(calls[0], " ")
	if !strings.Contains(args, "-ss 1.49") {
		t.Errorf("ffmpeg does not seek to 1.5 s: %s", args)
	}
	if !strings.Contains(args, "v360=e:e:") {
		t.Errorf("no levelling rotation: %s", args)
	}
	if _, err := os.Stat(filepath.Join(out, "120702_500.jpg")); err != nil {
		t.Error(err)
	}
	tsv, err := os.ReadFile(filepath.Join(out, "pano.tsv"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(tsv)), "\n")
	if len(lines) != 2 || lines[0] != "file\tt\tclock\tyaw\tpitch\troll" || !strings.HasPrefix(lines[1], "120702_500.jpg\t1.500\t12:07:02.500\t") {
		t.Errorf("pano.tsv = %q", tsv)
	}
}

// An upright, still camera: the back lens's axis is the picture's centre by
// default, a half turn of yaw; --centre front leaves the picture alone.
func TestCentre(t *testing.T) {
	for _, c := range []struct {
		flag []string
		yaw  []string
	}{{nil, []string{"yaw=180.0000", "yaw=-180.0000"}}, {[]string{"--centre", "front"}, []string{"yaw=0.0000", "yaw=-0.0000"}}} {
		front, _ := clip(t, "VID_20261122_120701_00_001.insv", 3)
		_, mpeg := stubs(t, "3.0")
		var stdout, stderr bytes.Buffer
		args := append(append([]string{"--width", "256", "--out", t.TempDir()}, c.flag...), front)
		if code := Main(context.Background(), args, &stdout, &stderr); code != 0 {
			t.Fatalf("exit %d: %s", code, stderr.String())
		}
		got := strings.Join(enginetest.Calls(t, mpeg)[0], " ")
		if !strings.Contains(got, c.yaw[0]) && !strings.Contains(got, c.yaw[1]) {
			t.Errorf("%v: want %s: %s", c.flag, c.yaw[0], got)
		}
	}
}

// --every goes down to one frame interval; frames are named to the
// millisecond.
func TestEverySubSecond(t *testing.T) {
	front, _ := clip(t, "VID_20261122_120701_00_001.insv", 2)
	_, mpeg := stubs(t, "2.0")
	out := t.TempDir()
	var stdout, stderr bytes.Buffer
	if code := Main(context.Background(), []string{"--every", "0.5s", "--width", "256", "--out", out, front}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	if n := len(enginetest.Calls(t, mpeg)); n != 2 {
		t.Errorf("%d frames from 2 s every 0.5 s, want 2 (1.0, 1.5 s)", n)
	}
	for _, f := range []string{"120702_000.jpg", "120702_500.jpg"} {
		if _, err := os.Stat(filepath.Join(out, f)); err != nil {
			t.Error(err)
		}
	}
	stderr.Reset()
	if code := Main(context.Background(), []string{"--every", "20ms", "--width", "256", "--out", t.TempDir(), front}, &stdout, &stderr); code != 1 || !strings.Contains(stderr.String(), "frame") {
		t.Errorf("--every under a frame interval: exit %d, %s", code, stderr.String())
	}
}

// A time near the end rounds to the frame grid but never past the last frame.
func TestEveryTimesLastFrame(t *testing.T) {
	got := everyTimes(0, 10.02, 5.0175, 30)
	if len(got) != 2 || got[0] != 5 || got[1] != 10 {
		t.Errorf("everyTimes = %v, want [5 10]: the last frame of a 10.02 s clip at 30 fps is at 10 s", got)
	}
}

func TestEveryFrames(t *testing.T) {
	front, _ := clip(t, "VID_20261122_120701_00_001.insv", 70)
	_, mpeg := stubs(t, "70.0")
	out := t.TempDir()
	var stdout, stderr bytes.Buffer
	if code := Main(context.Background(), []string{"--every", "30s", "--width", "256", "--out", out, front}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	if n := len(enginetest.Calls(t, mpeg)); n != 3 {
		t.Errorf("%d frames from 70 s every 30 s, want 3 (5, 35, 65 s)", n)
	}
	for _, f := range []string{"120706_000.jpg", "120736_000.jpg", "120806_000.jpg"} {
		if _, err := os.Stat(filepath.Join(out, f)); err != nil {
			t.Error(err)
		}
	}
}

func TestVideo(t *testing.T) {
	front, back := clip(t, "VID_20261122_120701_00_001.insv", 2)
	_, mpeg := stubs(t, "2.0")
	out := t.TempDir()
	var stdout, stderr bytes.Buffer
	if code := Main(context.Background(), []string{"--video", "--width", "256", "--out", out, back}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	calls := enginetest.Calls(t, mpeg)
	if len(calls) != 1 {
		t.Fatalf("%d ffmpeg calls, want 1", len(calls))
	}
	args := strings.Join(calls[0], " ")
	for _, want := range []string{"sendcmd=f=", "reset_rot=1", "-c:a copy", "libx264", front, back} {
		if !strings.Contains(args, want) {
			t.Errorf("ffmpeg lacks %q: %s", want, args)
		}
	}
	_, after, ok := strings.Cut(args, "sendcmd=f=")
	if !ok {
		t.Fatal("no sendcmd file")
	}
	cmdFile := strings.ReplaceAll(strings.SplitN(after, ",v360", 2)[0], `\`, "")
	if !filepath.IsAbs(cmdFile) {
		t.Fatalf("sendcmd file %q", cmdFile)
	}
	if _, err := os.Stat(cmdFile); !os.IsNotExist(err) {
		t.Errorf("the sendcmd file %s is left behind: %v", cmdFile, err)
	}
	tsv, _ := os.ReadFile(filepath.Join(out, "pano.tsv"))
	if n := strings.Count(string(tsv), "\n"); n != 1+60 {
		t.Errorf("pano.tsv has %d lines, want a header and 60 frames", n)
	}
}

func TestPairMissing(t *testing.T) {
	front, back := clip(t, "VID_20261122_120701_00_001.insv", 1)
	os.Remove(back)
	probe, mpeg := stubs(t, "1.0")
	var stdout, stderr bytes.Buffer
	if code := Main(context.Background(), []string{"--out", t.TempDir(), front}, &stdout, &stderr); code != 1 {
		t.Fatalf("exit %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), filepath.Base(back)) {
		t.Errorf("stderr does not name the missing file: %s", stderr.String())
	}
	if n := len(enginetest.Calls(t, probe)) + len(enginetest.Calls(t, mpeg)); n != 0 {
		t.Errorf("%d engine calls for a clip missing a lens", n)
	}
}

func TestFrameNames(t *testing.T) {
	for _, c := range []struct {
		t    float64
		want string
	}{{905, "12:22:06.000"}, {1.5, "12:07:02.500"}, {0.0334, "12:07:01.033"}} {
		got, err := clockName("/x/VID_20261122_120701_00_001.insv", c.t)
		if err != nil || got != c.want {
			t.Errorf("clockName(%v) = %q, %v; want %s", c.t, got, err, c.want)
		}
	}
	if _, err := clockName("/x/clip_00_001.insv", 0); err == nil {
		t.Error("a name with no start time gave a clock")
	}
}

func TestUsage(t *testing.T) {
	front, _ := clip(t, "VID_20261122_120701_00_001.insv", 1)
	for _, args := range [][]string{
		{"--every", "30s", "--video", "--out", "o", front},
		{"--width", "1001", "--out", "o", front},
		{"--mode", "spin", "--out", "o", front},
		{"--every", "0s", "--out", "o", front},
		{"--centre", "side", "--out", "o", front},
		{front},
	} {
		var stdout, stderr bytes.Buffer
		if code := Main(context.Background(), args, &stdout, &stderr); code != 2 {
			t.Errorf("%v: exit %d, want 2", args, code)
		}
	}
}

// A rotation turning 0.1° a frame is re-sent each time it has turned more
// than the threshold from the last one sent.
func TestResent(t *testing.T) {
	rots := make([]Rot, 10)
	for k := range rots {
		rots[k].Roll = float64(k) * 0.1 * math.Pi / 180
	}
	var got []int
	for k, s := range resent(rots, 0.25*math.Pi/180) {
		if s {
			got = append(got, k)
		}
	}
	if fmt.Sprint(got) != "[0 3 6 9]" {
		t.Errorf("sent at %v, want [0 3 6 9]", got)
	}
}
