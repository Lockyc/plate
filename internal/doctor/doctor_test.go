package doctor

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/lockyc/plate/internal/engine"
	"github.com/lockyc/plate/internal/enginetest"
	"github.com/lockyc/plate/internal/pins"
)

var ctx = context.Background()

func minimum(name, min string) pins.Engine {
	return pins.Engine{
		Name: name, Kind: pins.Minimum, VersionArgs: []string{"-v"}, VersionRe: `v(\d+\.\d+)`, Min: min,
		Install: map[string]string{runtime.GOOS: "get " + name}, UsedBy: []string{"cmd-" + name},
	}
}

func TestReport(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	enginetest.Stub(t, "good", `echo v2.5`)
	enginetest.Stub(t, "old", `echo v1.0`)
	engines := []pins.Engine{minimum("good", "2.0"), minimum("old", "2.0"), minimum("gone", "1.0")}
	var out bytes.Buffer
	code := run(ctx, engines, false, nil, &out)
	if code != 1 {
		t.Fatalf("code %d, want 1", code)
	}
	o := out.String()
	for _, want := range []string{"good", "2.5", "ok", "too old: get old", "missing: get gone", "cmd-old", "cmd-gone"} {
		if !strings.Contains(o, want) {
			t.Errorf("report lacks %q:\n%s", want, o)
		}
	}
	out.Reset()
	if code := run(ctx, engines[:1], false, nil, &out); code != 0 {
		t.Fatalf("all-ok code %d:\n%s", code, out.String())
	}
}

func TestDoctorReportsMissingCapability(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	enginetest.Stub(t, "ffmpeg", `case "$1 $2" in
  "-hide_banner -version") echo 'ffmpeg version 9.0.2' ;;
  "-hide_banner -encoders") echo ' V....D libx264   libx264 H.264' ;;
esac`)
	e := pins.Engine{
		Name: "ffmpeg", Kind: pins.Minimum, VersionArgs: []string{"-hide_banner", "-version"}, VersionRe: `ffmpeg version (\d+\.\d+\.\d+)`, Min: "7.1",
		Install: map[string]string{runtime.GOOS: "get ffmpeg"}, UsedBy: []string{"clip"},
		Capabilities: []pins.Capability{
			{Args: []string{"-hide_banner", "-encoders"}, Token: "libx264", For: "video mp4"},
			{Args: []string{"-hide_banner", "-encoders"}, Token: "libsvtav1", For: "video webm"},
		},
	}
	var out bytes.Buffer
	if code := run(ctx, []pins.Engine{e}, false, nil, &out); code != 1 {
		t.Fatalf("code %d, want 1:\n%s", code, out.String())
	}
	for _, want := range []string{"missing libsvtav1 (video webm)", "will not run until the rows above are fixed: clip"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("report lacks %q:\n%s", want, out.String())
		}
	}
	if strings.Contains(out.String(), "libx264 (") {
		t.Errorf("libx264 is present but reported:\n%s", out.String())
	}
}

func testZip(t *testing.T, entries map[string]string, link [2]string) []byte {
	t.Helper()
	var b bytes.Buffer
	w := zip.NewWriter(&b)
	for name, body := range entries {
		h := &zip.FileHeader{Name: name, Method: zip.Deflate}
		h.SetMode(0o755)
		f, _ := w.CreateHeader(h)
		io.WriteString(f, body)
	}
	if link[0] != "" {
		h := &zip.FileHeader{Name: link[0]}
		h.SetMode(os.ModeSymlink | 0o777)
		f, _ := w.CreateHeader(h)
		io.WriteString(f, link[1])
	}
	w.Close()
	return b.Bytes()
}

func managed(archive []byte, sha string) pins.Engine {
	if sha == "" {
		s := sha256.Sum256(archive)
		sha = hex.EncodeToString(s[:])
	}
	return pins.Engine{
		Name: "fake-managed", Kind: pins.Managed, UsedBy: []string{"render"},
		Download: &pins.Download{Version: "9.9", Assets: map[string]pins.Asset{
			runtime.GOOS + "/" + runtime.GOARCH: {URL: "https://example.invalid/a.zip", SHA256: sha, Bin: "pkg/tool"},
		}},
	}
}

func serve(b []byte) getter {
	return func(context.Context, string) (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(b)), nil }
}

func TestInstallManaged(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	archive := testZip(t, map[string]string{"pkg/tool": "#!/bin/sh\n", "pkg/lib/data.pak": "x"}, [2]string{"pkg/libEGL.dylib", "lib/data.pak"})
	e := managed(archive, "")
	var out bytes.Buffer
	if code := run(ctx, []pins.Engine{e}, true, serve(archive), &out); code != 0 {
		t.Fatalf("code %d:\n%s", code, out.String())
	}
	p, err := engine.Find(e)
	if err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(p); fi.Mode()&0o111 == 0 {
		t.Error("installed binary is not executable")
	}
	if target, err := os.Readlink(filepath.Join(filepath.Dir(p), "libEGL.dylib")); err != nil || target != "lib/data.pak" {
		t.Errorf("symlink = %q, %v", target, err)
	}
}

func TestInstallRejectsBadChecksum(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	archive := testZip(t, map[string]string{"pkg/tool": "#!/bin/sh\n"}, [2]string{})
	e := managed(archive, strings.Repeat("0", 64))
	var out bytes.Buffer
	if code := run(ctx, []pins.Engine{e}, true, serve(archive), &out); code != 1 {
		t.Fatalf("code %d", code)
	}
	if !strings.Contains(out.String(), "sha256") {
		t.Errorf("no checksum message:\n%s", out.String())
	}
	if _, err := engine.Find(e); err == nil {
		t.Fatal("a checksum failure left an installed engine")
	}
}

func TestInstallRejectsZipSlip(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	archive := testZip(t, map[string]string{"pkg/tool": "#!/bin/sh\n", "../../escape": "x"}, [2]string{})
	e := managed(archive, "")
	var out bytes.Buffer
	if code := run(ctx, []pins.Engine{e}, true, serve(archive), &out); code != 1 {
		t.Fatalf("code %d", code)
	}
	if _, err := engine.Find(e); err == nil {
		t.Fatal("a zip-slip archive was installed")
	}
}

func TestManagedMissingPointsAtInstall(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	e := managed([]byte("x"), "")
	t.Setenv(engine.EnvOverride(e.Name), "")
	var out bytes.Buffer
	run(ctx, []pins.Engine{e}, false, nil, &out)
	if !strings.Contains(out.String(), "plate doctor --install") {
		t.Errorf("report:\n%s", out.String())
	}
}

// TestEnginesListAnimate pins the commands doctor names for the engines a
// video capture runs, through animate and build's video lines.
func TestEnginesListAnimate(t *testing.T) {
	for name, cmds := range map[string][]string{
		"chrome-headless-shell": {"animate", "build"},
		"ffmpeg":                {"animate", "build", "clip"},
		"ffprobe":               {"animate", "build", "clip"},
		"magick":                {"animate", "build", "clip"},
	} {
		e, ok := pins.Lookup(name)
		if !ok {
			t.Fatalf("no engine %s", name)
		}
		for _, c := range cmds {
			if !slices.Contains(e.UsedBy, c) {
				t.Errorf("%s: UsedBy %v lacks %s", name, e.UsedBy, c)
			}
		}
	}
}

func TestDoctorReportsProviderKeys(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	t.Setenv("OPENAI_API_KEY", "sk-test")
	t.Setenv("GEMINI_API_KEY", "")
	t.Setenv("BFL_API_KEY", "")
	var out bytes.Buffer
	code := run(ctx, nil, false, nil, &out)
	if code != 0 {
		t.Fatalf("a missing key must not change the exit status, got %d", code)
	}
	o := out.String()
	// Each row names the variable to set, never its value.
	if !regexp.MustCompile(`openai +OPENAI_API_KEY +set\b`).MatchString(o) {
		t.Errorf("no 'openai  OPENAI_API_KEY  set' row:\n%s", o)
	}
	if !regexp.MustCompile(`google +GEMINI_API_KEY +not set`).MatchString(o) {
		t.Errorf("no 'google  GEMINI_API_KEY  not set' row:\n%s", o)
	}
	if strings.Contains(o, "sk-test") {
		t.Errorf("key value leaked:\n%s", o)
	}
}
