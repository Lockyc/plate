package vision

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/lockyc/plate/internal/engine"
	"github.com/lockyc/plate/internal/enginetest"
)

func TestAvailable(t *testing.T) {
	for _, c := range []struct{ goos, version, want string }{
		{"linux", "", "--coarse birefnet"},
		{"darwin", "13.6.1", "this is macOS 13.6.1"},
		{"darwin", "14.0", ""},
		{"darwin", "26.1", ""},
		{"darwin", "", ""}, // unknown: swiftc and Vision speak for themselves
	} {
		err := available(c.goos, c.version)
		if (c.want == "") != (err == nil) || (err != nil && (!strings.Contains(err.Error(), c.want) || !strings.Contains(err.Error(), "--coarse birefnet"))) {
			t.Errorf("available(%s, %q) = %v, want %q", c.goos, c.version, err, c.want)
		}
	}
	if runtime.GOOS == "darwin" && macOSVersion() == "" {
		t.Error("no macOS version read on macOS")
	}
}

// TestHelperBuildIgnoresAStaleTemp: a temp file another run left (or is
// writing) at a fixed name does not stop the build.
func TestHelperBuildIgnoresAStaleTemp(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	enginetest.Stub(t, "swiftc", `while [ "$1" != "-o" ]; do shift; done
printf '#!/bin/sh\n' > "$2"; chmod +x "$2"`)
	sum := sha256.Sum256(source)
	cache, err := engine.CacheDir()
	if err != nil {
		t.Fatal(err)
	}
	stale := filepath.Join(cache, "vision", hex.EncodeToString(sum[:])[:16], "plate-vision.tmp")
	if err := os.MkdirAll(stale, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := helper(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestMaskCompilesOnceAndRuns(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("Vision is macOS-only")
	}
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	// The stub compiler writes a helper that copies its input to its output.
	log := enginetest.Stub(t, "swiftc", `while [ "$1" != "-o" ]; do shift; done
printf '#!/bin/sh\ncp "$1" "$2"\n' > "$2"; chmod +x "$2"`)
	dir := t.TempDir()
	in, out := filepath.Join(dir, "in.png"), filepath.Join(dir, "out.png")
	os.WriteFile(in, []byte("png"), 0o644)
	for i := 0; i < 2; i++ {
		if err := Mask(context.Background(), in, out); err != nil {
			t.Fatal(err)
		}
	}
	if n := len(enginetest.Calls(t, log)); n != 1 {
		t.Errorf("swiftc ran %d times, want 1", n)
	}
	if b, _ := os.ReadFile(out); string(b) != "png" {
		t.Error("helper did not run")
	}
}

func TestMaskRefusesSameFile(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("Vision is macOS-only")
	}
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	enginetest.Stub(t, "swiftc", `while [ "$1" != "-o" ]; do shift; done
printf '#!/bin/sh\ncp "$1" "$2"\n' > "$2"; chmod +x "$2"`)
	in := filepath.Join(t.TempDir(), "in.png")
	os.WriteFile(in, []byte("png"), 0o644)
	if err := Mask(context.Background(), in, in); err == nil {
		t.Fatal("in == out accepted")
	}
	if b, _ := os.ReadFile(in); string(b) != "png" {
		t.Error("source did not survive")
	}
}

// TestRealVision compiles the helper with the real swiftc and cuts a real
// image. Opt-in (PLATE_REAL=1): it is not part of gate.
func TestRealVision(t *testing.T) {
	if os.Getenv("PLATE_REAL") == "" || runtime.GOOS != "darwin" {
		t.Skip("set PLATE_REAL=1 on macOS")
	}
	in := os.Getenv("PLATE_REAL_IN")
	if in == "" {
		t.Skip("set PLATE_REAL_IN to a PNG")
	}
	out := filepath.Join(t.TempDir(), "out.png")
	if err := Mask(context.Background(), in, out); err != nil {
		t.Fatal(err)
	}
	t.Logf("wrote %s", out)
	if dst := os.Getenv("PLATE_REAL_OUT"); dst != "" {
		b, _ := os.ReadFile(out)
		os.WriteFile(dst, b, 0o644)
	}
}
