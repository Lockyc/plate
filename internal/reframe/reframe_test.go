package reframe

import (
	"bytes"
	"context"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
		"":                                    "no [[key]]",
		"[[key]]\nt = 0\nyaw = 0\npitch = 0\n": "key 1 (t = 0) lacks fov",
		"[[key]]\nt = 0\nyaw = 0\npitch = 0\nfov = 80\n[[key]]\nt = 0\nyaw = 1\npitch = 0\nfov = 80\n": "key 2 (t = 0) is not after",
		"[[key]]\nt = 0\nyaw = 0\npitch = 95\nfov = 80\n":                                                "pitch 95",
		"[[key]]\nt = 0\nyaw = 0\npitch = 0\nfov = 180\n":                                               "fov 180",
		"[[key]]\nt = 0\nyaw = 0\npitch = 0\nfov = 80\nroll = 3\n":                                      "roll",
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
