package engine_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/lockyc/plate/internal/engine"
	"github.com/lockyc/plate/internal/enginetest"
	"github.com/lockyc/plate/internal/pins"
)

var ctx = context.Background()

func TestRunPassesArgsVerbatim(t *testing.T) {
	log := enginetest.Stub(t, "magick", `printf ok > "$1"`)
	out := filepath.Join(t.TempDir(), "out.png")
	args := []string{out, "a b", "$(rm -rf /)", "x;y", "*"}
	if _, err := engine.Run(ctx, engine.Cmd{Engine: "magick", Args: args, Outputs: []string{out}}); err != nil {
		t.Fatal(err)
	}
	calls := enginetest.Calls(t, log)
	if len(calls) != 1 || !slices.Equal(calls[0], args) {
		t.Fatalf("calls = %q, want one call with %q", calls, args)
	}
}

func TestRunRemovesStaleOutputAndFailsWhenNoneWritten(t *testing.T) {
	enginetest.Stub(t, "magick", `exit 0`)
	out := filepath.Join(t.TempDir(), "out.png")
	os.WriteFile(out, []byte("stale"), 0o644)
	_, err := engine.Run(ctx, engine.Cmd{Engine: "magick", Outputs: []string{out}})
	var re *engine.RunError
	if !errors.As(err, &re) || !strings.Contains(re.Reason, "produced no output") {
		t.Fatalf("err = %v, want RunError 'produced no output'", err)
	}
	if _, err := os.Stat(out); !os.IsNotExist(err) {
		t.Fatal("stale output survived")
	}
}

func TestRunEmptyOutputIsFailure(t *testing.T) {
	enginetest.Stub(t, "magick", `: > "$1"`)
	out := filepath.Join(t.TempDir(), "out.png")
	if _, err := engine.Run(ctx, engine.Cmd{Engine: "magick", Args: []string{out}, Outputs: []string{out}}); err == nil {
		t.Fatal("empty output accepted")
	}
}

func TestRunExitStatusFailsAndRemovesOutput(t *testing.T) {
	enginetest.Stub(t, "magick", `printf partial > "$1"; echo boom >&2; exit 3`)
	out := filepath.Join(t.TempDir(), "out.png")
	_, err := engine.Run(ctx, engine.Cmd{Engine: "magick", Args: []string{out}, Outputs: []string{out}})
	if err == nil || !strings.Contains(err.Error(), "exit status 3") || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("err = %v", err)
	}
	if _, err := os.Stat(out); !os.IsNotExist(err) {
		t.Fatal("output of a failed run survived")
	}
}

func TestFindRefusesAMissingOverride(t *testing.T) {
	t.Setenv(engine.EnvOverride("magick"), filepath.Join(t.TempDir(), "nope"))
	var nie *engine.NotInstalledError
	if _, err := engine.Resolve("magick"); !errors.As(err, &nie) || !strings.Contains(err.Error(), "PLATE_ENGINE_MAGICK") {
		t.Fatalf("err = %v", err)
	}
}

func TestFindRunsARelativeOverride(t *testing.T) {
	t.Chdir(t.TempDir())
	os.WriteFile("mystub", []byte("#!/bin/sh\nexit 0\n"), 0o755)
	t.Setenv("PATH", t.TempDir())
	t.Setenv(engine.EnvOverride("magick"), "mystub")
	if _, err := engine.Run(ctx, engine.Cmd{Engine: "magick"}); err != nil {
		t.Fatalf("bare-name override in the working directory: %v", err)
	}
}

func TestVersionReportsAnExecFailure(t *testing.T) {
	bad := filepath.Join(t.TempDir(), "magick")
	os.WriteFile(bad, []byte("#!/no/such/interpreter\n"), 0o755)
	t.Setenv(engine.EnvOverride("magick"), bad)
	e, _ := pins.Lookup("magick")
	if _, err := engine.Version(ctx, e); err == nil || strings.Contains(err.Error(), "no version in") {
		t.Fatalf("err = %v", err)
	}
}

func TestRunPassesDashPathsAsFiles(t *testing.T) {
	log := enginetest.Stub(t, "magick", `cp "$1" "$2"`)
	dir := t.TempDir()
	t.Chdir(dir)
	os.WriteFile("-in.png", []byte("x"), 0o644)
	if _, err := engine.Run(ctx, engine.Cmd{Engine: "magick", Args: []string{"-in.png", "-out.png"}, Inputs: []string{"-in.png"}, Outputs: []string{"-out.png"}}); err != nil {
		t.Fatal(err)
	}
	if got := enginetest.Calls(t, log)[0]; !slices.Equal(got, []string{"./-in.png", "./-out.png"}) {
		t.Fatalf("args = %q", got)
	}
}

func TestRunStderrPolicy(t *testing.T) {
	enginetest.Stub(t, "gs", `echo "   **** Error: rangecheck" >&2`)
	if _, err := engine.Run(ctx, engine.Cmd{Engine: "gs"}); err != nil {
		t.Fatalf("stderr was fatal without StderrFatal: %v", err)
	}
	if _, err := engine.Run(ctx, engine.Cmd{Engine: "gs", StderrFatal: true}); err == nil {
		t.Fatal("stderr ignored under StderrFatal")
	}
}

func TestRunSuccessWithStrayChild(t *testing.T) {
	// The backgrounded sleep stays in the engine's process group and holds
	// stderr open after the leader exits 0. Run must return at the leader's
	// exit, keep the output, and leave no group member behind.
	dir := t.TempDir()
	out, pidfile := filepath.Join(dir, "out.png"), filepath.Join(dir, "child.pid")
	enginetest.Stub(t, "magick", `printf ok > "$1"; sleep 30 >&2 & echo $! > "$2"; exit 0`)
	start := time.Now()
	_, err := engine.Run(ctx, engine.Cmd{Engine: "magick", Args: []string{out, pidfile}, Outputs: []string{out}})
	if elapsed := time.Since(start); elapsed > 1500*time.Millisecond {
		t.Fatalf("Run took %v; it waited on the stray child", elapsed)
	}
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if _, err := os.Stat(out); err != nil {
		t.Fatal("output file was removed on success")
	}
	waitGone(t, readPid(t, pidfile))
}

// readPid reads the pid a stub wrote to file.
func readPid(t *testing.T, file string) int {
	t.Helper()
	b, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	var pid int
	if _, err := fmt.Sscanf(strings.TrimSpace(string(b)), "%d", &pid); err != nil {
		t.Fatalf("invalid pid in file: %v", err)
	}
	return pid
}

// waitGone fails unless pid stops existing within a second.
func waitGone(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for syscall.Kill(pid, 0) != syscall.ESRCH {
		if time.Now().After(deadline) {
			t.Fatalf("process %d still alive", pid)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestRunRefusesOutputThatIsAnInput(t *testing.T) {
	enginetest.Stub(t, "magick", `printf new > "$1"`)
	dir := t.TempDir()
	src := filepath.Join(dir, "in.png")
	os.WriteFile(src, []byte("source"), 0o644)
	for _, c := range []struct{ in, out string }{
		{src, src},
		{src, filepath.Join(dir, ".", "in.png")},
		{filepath.Join(dir, "missing.png"), filepath.Join(dir, "sub", "..", "missing.png")},
	} {
		_, err := engine.Run(ctx, engine.Cmd{Engine: "magick", Args: []string{c.out}, Inputs: []string{c.in}, Outputs: []string{c.out}})
		if err == nil || !strings.Contains(err.Error(), "is both an input and an output") {
			t.Fatalf("in %s, out %s: err = %v, want refusal", c.in, c.out, err)
		}
	}
	if b, _ := os.ReadFile(src); string(b) != "source" {
		t.Fatalf("source = %q, want it untouched", b)
	}
}

func TestRunRemovesStaleOutputWhenEngineMissing(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	t.Setenv(engine.EnvOverride("magick"), "")
	out := filepath.Join(t.TempDir(), "out.png")
	os.WriteFile(out, []byte("stale"), 0o644)
	var ni *engine.NotInstalledError
	if _, err := engine.Run(ctx, engine.Cmd{Engine: "magick", Outputs: []string{out}}); !errors.As(err, &ni) {
		t.Fatalf("err = %v, want NotInstalledError", err)
	}
	if _, err := os.Stat(out); !os.IsNotExist(err) {
		t.Fatal("stale output survived a missing engine")
	}
}

func TestRunTimeoutRemovesPartialOutput(t *testing.T) {
	dir := t.TempDir()
	out, pidfile := filepath.Join(dir, "out.png"), filepath.Join(dir, "child.pid")
	enginetest.Stub(t, "magick", `sleep 30 & echo $! > "$2"; printf partial > "$1"; wait`)
	start := time.Now()
	_, err := engine.Run(ctx, engine.Cmd{Engine: "magick", Args: []string{out, pidfile}, Outputs: []string{out}, Timeout: time.Second})
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("err = %v", err)
	}
	if time.Since(start) > 3*time.Second {
		t.Fatal("Run took too long; process group may not have been killed")
	}
	waitGone(t, readPid(t, pidfile))
	if _, err := os.Stat(out); !os.IsNotExist(err) {
		t.Fatal("partial output survived a timeout")
	}
}

func TestResolveNotInstalled(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	t.Setenv(engine.EnvOverride("magick"), "")
	_, err := engine.Resolve("magick")
	var ni *engine.NotInstalledError
	if !errors.As(err, &ni) {
		t.Fatalf("err = %v, want NotInstalledError", err)
	}
	e, _ := pins.Lookup("magick")
	if ni.Hint != e.Hint() {
		t.Errorf("hint %q, want the pin table's %q", ni.Hint, e.Hint())
	}
}

func TestResolveManaged(t *testing.T) {
	e, _ := pins.Lookup("chrome-headless-shell")
	a, ok := e.Asset()
	if !ok {
		t.Skip("no chrome-headless-shell build for " + runtime.GOOS + "/" + runtime.GOARCH)
	}
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv(engine.EnvOverride(e.Name), "")
	if _, err := engine.Resolve(e.Name); err == nil {
		t.Fatal("resolved before install")
	}
	dir, _ := engine.ManagedDir(e)
	bin := filepath.Join(dir, a.Bin)
	os.MkdirAll(filepath.Dir(bin), 0o755)
	os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o755)
	got, err := engine.Resolve(e.Name)
	if err != nil || got != bin {
		t.Fatalf("Resolve = %q, %v; want %q", got, err, bin)
	}
}

func TestVersionIgnoresExitStatus(t *testing.T) {
	enginetest.Stub(t, "pdfinfo", `echo "pdfinfo version 26.09.0" >&2; exit 99`)
	e, _ := pins.Lookup("pdfinfo")
	v, err := engine.Version(ctx, e)
	if err != nil || v != "26.09.0" {
		t.Fatalf("Version = %q, %v", v, err)
	}
}

func TestRunAllowExitReturnsStdoutOfNonZeroExit(t *testing.T) {
	enginetest.Stub(t, "magick", `printf whole; exit 2`)
	res, err := engine.Run(context.Background(), engine.Cmd{Engine: "magick", AllowExit: true})
	if err != nil || string(res.Stdout) != "whole" {
		t.Fatalf("stdout %q, err %v; want whole, nil", res.Stdout, err)
	}
	if _, err := engine.Run(context.Background(), engine.Cmd{Engine: "magick"}); err == nil {
		t.Error("a non-zero exit without AllowExit must fail")
	}
}

func TestRunAllowExitStillFailsOnTimeout(t *testing.T) {
	enginetest.Stub(t, "magick", `sleep 30`)
	if _, err := engine.Run(context.Background(), engine.Cmd{Engine: "magick", AllowExit: true, Timeout: 200 * time.Millisecond}); err == nil {
		t.Error("a timeout must fail even with AllowExit")
	}
}

func TestCapable(t *testing.T) {
	e := pins.Engine{Name: "ffmpeg", Kind: pins.Minimum}
	enginetest.Stub(t, "ffmpeg", "echo ' V....D libx264   libx264 H.264'")
	for token, want := range map[string]bool{"libx264": true, "libx26": false} {
		got, err := engine.Capable(ctx, e, pins.Capability{Args: []string{"-encoders"}, Token: token, For: "x"})
		if err != nil || got != want {
			t.Errorf("Capable(%q) = %v, %v; want %v", token, got, err, want)
		}
	}
	enginetest.Stub(t, "ffmpeg", "echo libx264; exit 1")
	if got, err := engine.Capable(ctx, e, pins.Capability{Args: []string{"-encoders"}, Token: "libx264", For: "x"}); err != nil || !got {
		t.Errorf("exit status 1 with the token printed: %v, %v; want true", got, err)
	}
}

func TestCapableTimeoutIsAnError(t *testing.T) {
	e := pins.Engine{Name: "ffmpeg", Kind: pins.Minimum}
	enginetest.Stub(t, "ffmpeg", "sleep 30")
	tctx, cancel := context.WithTimeout(ctx, 300*time.Millisecond)
	defer cancel()
	got, err := engine.Capable(tctx, e, pins.Capability{Args: []string{"-encoders"}, Token: "libx264", For: "x"})
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("Capable = %v, %v; want a timed-out error, not a missing capability", got, err)
	}
}
