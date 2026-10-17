package cli

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

func TestParse(t *testing.T) {
	var errb bytes.Buffer
	fs := Flags("demo", "demo [--n N] <in> <out>", &errb)
	n := fs.Int("n", 1, "a number")
	rest, code, ok := Parse(fs, []string{"--n", "3", "a", "b"}, 2)
	if !ok || code != 0 || *n != 3 || len(rest) != 2 {
		t.Fatalf("Parse = %q %d %v, n=%d", rest, code, ok, *n)
	}
	errb.Reset()
	if _, code, ok := Parse(Flags("demo", "demo <in>", &errb), []string{"a", "b"}, 1); ok || code != 2 {
		t.Fatalf("wrong positional count: code %d ok %v", code, ok)
	}
	if !strings.Contains(errb.String(), "usage: plate demo <in>") {
		t.Errorf("usage not printed: %q", errb.String())
	}
	if _, code, ok := Parse(Flags("demo", "demo", &errb), []string{"-h"}, 0); ok || code != 0 {
		t.Fatalf("-h: code %d ok %v", code, ok)
	}
	if _, code, ok := Parse(Flags("demo", "demo", &errb), []string{"--bogus"}, 0); ok || code != 2 {
		t.Fatalf("unknown flag: code %d ok %v", code, ok)
	}
}

func TestFail(t *testing.T) {
	var errb bytes.Buffer
	if code := Fail(&errb, "render", errors.New("render would be 300.0 MP")); code != 1 {
		t.Fatalf("code %d", code)
	}
	if errb.String() != "plate render: render would be 300.0 MP\n" {
		t.Errorf("stderr %q", errb.String())
	}
}

func TestFloatRefusesNonFinite(t *testing.T) {
	for _, v := range []string{"NaN", "Inf", "-Inf"} {
		var errb bytes.Buffer
		fs := Flags("demo", "demo", &errb)
		Float(fs, "x", 1, "a number")
		if _, code, ok := Parse(fs, []string{"--x", v}, 0); ok || code != 2 {
			t.Errorf("--x %s: code %d ok %v", v, code, ok)
		}
	}
	fs := Flags("demo", "demo", &bytes.Buffer{})
	x := Float(fs, "x", 1, "a number")
	if _, _, ok := Parse(fs, []string{"--x", "2.5"}, 0); !ok || *x != 2.5 {
		t.Fatalf("--x 2.5: ok %v x %v", ok, *x)
	}
}

func TestIntIsDecimal(t *testing.T) {
	for in, want := range map[string]int{"0150": 150, "0999": 999, "-3": -3, "12": 12} {
		fs := Flags("demo", "demo", &bytes.Buffer{})
		n := Int(fs, "n", 7, "")
		if err := fs.Parse([]string{"--n", in}); err != nil || *n != want {
			t.Errorf("--n %s = %d, %v; want %d", in, *n, err, want)
		}
	}
	for _, in := range []string{"0x10", "1e3", "", "1.5"} {
		fs := Flags("demo", "demo", &bytes.Buffer{})
		Int(fs, "n", 7, "")
		if err := fs.Parse([]string{"--n", in}); err == nil {
			t.Errorf("--n %q accepted", in)
		}
	}
}

func TestMulti(t *testing.T) {
	var errb bytes.Buffer
	fs := Flags("demo", "demo [--x X]...", &errb)
	var xs Multi
	fs.Var(&xs, "x", "repeatable")
	if _, _, ok := Parse(fs, []string{"--x", "a", "--x", "b c"}, 0); !ok {
		t.Fatal(errb.String())
	}
	if len(xs) != 2 || xs[0] != "a" || xs[1] != "b c" {
		t.Errorf("Multi = %q, want [a, b c]", xs)
	}
}
