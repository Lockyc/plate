package reframe

import (
	"bytes"
	"context"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lockyc/plate/internal/enginetest"
)

func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func TestPathInterp(t *testing.T) {
	p := Path{{T: 0, Yaw: 0, Pitch: 0, FOV: 80}, {T: 2, Yaw: 90, Pitch: -10, FOV: 60}}
	if v := p.At(1); !near(v.Yaw, 45) || !near(v.Pitch, -5) || !near(v.FOV, 70) {
		t.Fatalf("At(1) = %+v, want the midpoint", v)
	}
	if v := p.At(0.5); !near(v.Yaw, 14.0625) {
		t.Fatalf("At(0.5).Yaw = %v, want 14.0625 (smoothstep)", v.Yaw)
	}
	wrap := Path{{T: 0, Yaw: 350, FOV: 80}, {T: 2, Yaw: 370, FOV: 80}}
	if v := wrap.At(1); !near(v.Yaw, 360) {
		t.Fatalf("350 to 370 at its midpoint: yaw %v, want 360 (the short way, through 0)", v.Yaw)
	}
	if v := p.At(-1); v != (View{0, 0, 80}) {
		t.Fatalf("before the first key: %+v", v)
	}
	if v := p.At(5); v != (View{90, -10, 60}) {
		t.Fatalf("after the last key: %+v", v)
	}
}

func TestOneKey(t *testing.T) {
	p := Path{{T: 3, Yaw: 40, Pitch: -10, FOV: 80}}
	for _, at := range []float64{0, 3, 100} {
		if v := p.At(at); v != (View{40, -10, 80}) {
			t.Fatalf("At(%v) = %+v, want the one key's view", at, v)
		}
	}
}

func writePath(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "path.toml")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoadPathRefuses(t *testing.T) {
	for body, want := range map[string]string{
		"":                                     "no [[key]]",
		"[[key]]\nt = 0\nyaw = 0\npitch = 0\n": "key 1 (t = 0) lacks fov",
		"[[key]]\nt = 0\nyaw = 0\npitch = 0\nfov = 80\n[[key]]\nt = 0\nyaw = 1\npitch = 0\nfov = 80\n": "key 2 (t = 0) is not after",
		"[[key]]\nt = 0\nyaw = 0\npitch = 95\nfov = 80\n":                                              "pitch 95",
		"[[key]]\nt = 0\nyaw = 0\npitch = 0\nfov = 180\n":                                              "fov 180",
		"[[key]]\nt = 0\nyaw = 0\npitch = 0\nfov = 80\nroll = 3\n":                                     "roll",
	} {
		_, err := LoadPath(writePath(t, body))
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: err = %v, want %q", body, err, want)
		}
	}
	p, err := LoadPath(writePath(t, "[[key]]\nt = 12.0\nyaw = 40\npitch = -10\nfov = 80\n"))
	if err != nil || len(p) != 1 || p[0] != (Key{12, 40, -10, 80}) {
		t.Fatalf("p = %+v, err = %v", p, err)
	}
}

func TestFovFits(t *testing.T) {
	// fov is across the width: 80° wide at 9:16 is 112° tall, but
	// 150° wide would be 163° tall.
	if err := fits(Path{{FOV: 80}}, 1080, 1920); err != nil {
		t.Fatal(err)
	}
	if err := fits(Path{{T: 2, FOV: 150}}, 1080, 1920); err == nil || !strings.Contains(err.Error(), "key 1 (t = 2)") {
		t.Fatalf("err = %v", err)
	}
}

func run(t *testing.T, args ...string) (int, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := Main(context.Background(), args, &stdout, &stderr)
	return code, stderr.String()
}

func TestStillAt(t *testing.T) {
	path := writePath(t, "[[key]]\nt = 0\nyaw = 0\npitch = 0\nfov = 80\n")
	img := filepath.Join(t.TempDir(), "frame.jpg")
	os.WriteFile(img, []byte("x"), 0o644)
	out := filepath.Join(t.TempDir(), "out.jpg")
	code, stderr := run(t, "--path", path, "--at", "3", "--out", out, img)
	if code != 2 || !strings.Contains(stderr, "a still from an image takes no --at") {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	code, stderr = run(t, "--path", path, "--from", "3", "--out", out, img)
	if code != 2 || !strings.Contains(stderr, "--from") {
		t.Fatalf("--from on an image: exit %d: %s", code, stderr)
	}
	code, stderr = run(t, "--path", path, "--out", filepath.Join(t.TempDir(), "out.mp4"), img)
	if code != 2 || !strings.Contains(stderr, "an image gives a still") {
		t.Fatalf("mp4 from an image: exit %d: %s", code, stderr)
	}
}

func TestCommandsSendOnlyChanges(t *testing.T) {
	p := Path{{T: 1, Yaw: 0, Pitch: 0, FOV: 80}, {T: 2, Yaw: 30, Pitch: 0, FOV: 80}}
	got := commands(p, []float64{0, 1, 1.5, 2, 2.5}, 1080, 1920, 2)
	want := "1.375000 v360@view yaw 15.0000;\n1.875000 v360@view yaw 30.0000;\n"
	if got != want {
		t.Fatalf("commands =\n%s\nwant\n%s", got, want)
	}
	if got := commands(Path{{FOV: 80}}, []float64{0, 1}, 1080, 1920, 30); got != "" {
		t.Fatalf("a fixed view sends %q", got)
	}
}

func TestClipCutsAndKeepsSound(t *testing.T) {
	enginetest.Stub(t, "ffprobe", `printf '{"streams":[{"width":2048,"height":1024,"r_frame_rate":"30/1","duration":"20.0"}]}'`)
	mpeg := enginetest.Stub(t, "ffmpeg", `for a; do last=$a; done; printf x > "$last"`)
	src := filepath.Join(t.TempDir(), "pano.mp4")
	os.WriteFile(src, []byte("v"), 0o644)
	path := writePath(t, "[[key]]\nt = 5\nyaw = 0\npitch = 0\nfov = 80\n[[key]]\nt = 15\nyaw = 90\npitch = 0\nfov = 80\n")
	out := filepath.Join(t.TempDir(), "c.mp4")
	if code, stderr := run(t, "--path", path, "--from", "5", "--to", "15", "--out", out, src); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	a := strings.Join(enginetest.Calls(t, mpeg)[0], " ")
	for _, want := range []string{"-ss 4.991667 -t 10.000000 -i " + src, "sendcmd=f=", "v360@view=e:flat:yaw=0.0000", "-map 0:a:0? -c:a copy", "-r 30/1"} {
		if !strings.Contains(a, want) {
			t.Errorf("argv lacks %q:\n%s", want, a)
		}
	}

	late := writePath(t, "[[key]]\nt = 25\nyaw = 0\npitch = 0\nfov = 80\n")
	if code, stderr := run(t, "--path", late, "--out", out, src); code != 1 || !strings.Contains(stderr, "key 1 (t = 25) is past the source's end at 20.000 s") {
		t.Fatalf("a key past the end: exit %d: %s", code, stderr)
	}
	enginetest.Stub(t, "ffprobe", `printf '{"streams":[{"width":1920,"height":1080,"r_frame_rate":"30/1","duration":"20.0"}]}'`)
	if code, stderr := run(t, "--path", path, "--out", out, src); code != 1 || !strings.Contains(stderr, "is 1920x1080, not a 2:1 equirect") {
		t.Fatalf("a flat source: exit %d: %s", code, stderr)
	}
}
