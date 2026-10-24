package genai

import (
	"go/build"
	"strings"
	"testing"
)

const (
	modulePath = "github.com/lockyc/plate"
	genaiPath  = modulePath + "/internal/genai"
)

// reach walks the imports of the package at dir (and transitively every
// package of this module it imports) and returns the import chain that arrives
// at target, or nil if it is not reached.
func reach(t *testing.T, start, dir, target string) []string {
	t.Helper()
	seen := map[string]bool{}
	var walk func(path, dir string, chain []string) []string
	walk = func(path, dir string, chain []string) []string {
		chain = append(append([]string{}, chain...), path)
		if path == target {
			return chain
		}
		if seen[path] {
			return nil
		}
		seen[path] = true
		p, err := build.ImportDir(dir, 0)
		if err != nil {
			t.Fatalf("import %s: %v", dir, err)
		}
		imps := append(append([]string{}, p.Imports...), p.TestImports...)
		imps = append(imps, p.XTestImports...)
		for _, imp := range imps {
			if imp != modulePath && !strings.HasPrefix(imp, modulePath+"/") {
				continue
			}
			rel := strings.TrimPrefix(imp, modulePath)
			if c := walk(imp, "../.."+rel, chain); c != nil {
				return c
			}
		}
		return nil
	}
	return walk(start, dir, nil)
}

func TestNoBuildPathReachesGenai(t *testing.T) {
	for _, pkg := range []string{"build", "render", "animate"} {
		if c := reach(t, modulePath+"/internal/"+pkg, "../"+pkg, genaiPath); c != nil {
			t.Errorf("internal/%s reaches internal/genai: %s", pkg, strings.Join(c, " -> "))
		}
	}
}

func TestGuardSeesAReach(t *testing.T) {
	c := reach(t, modulePath+"/internal/generate", "../generate", genaiPath)
	if c == nil {
		t.Fatal("walker did not see internal/generate import genai")
	}
}

func TestForUnknownProvider(t *testing.T) {
	if _, err := For("nope"); err == nil {
		t.Fatal("For(unknown) returned no error")
	}
}
