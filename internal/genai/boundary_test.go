package genai

import (
	"errors"
	"go/build"
	"io/fs"
	"path/filepath"
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

// reachesGenai lists every package of the module allowed to reach genai:
// generate, the one command that calls a provider, and main, which
// registers the commands. Every other package, build, render and animate
// among them, must not.
var reachesGenai = map[string]bool{
	modulePath:                        true,
	modulePath + "/internal/generate": true,
}

func TestOnlyGenerateReachesGenai(t *testing.T) {
	walked := 0
	err := filepath.WalkDir("../..", func(dir string, d fs.DirEntry, err error) error {
		if err != nil || !d.IsDir() {
			return err
		}
		name := d.Name()
		if dir != "../.." && (name == "testdata" || strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_")) {
			return filepath.SkipDir
		}
		if _, err := build.ImportDir(dir, 0); err != nil {
			var none *build.NoGoError
			if errors.As(err, &none) {
				return nil
			}
			return err
		}
		rel, _ := filepath.Rel("../..", dir)
		path := modulePath
		if rel != "." {
			path += "/" + filepath.ToSlash(rel)
		}
		if path == genaiPath {
			return nil
		}
		walked++
		if c := reach(t, path, dir, genaiPath); c != nil && !reachesGenai[path] {
			t.Errorf("%s reaches internal/genai: %s", path, strings.Join(c, " -> "))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if walked < 10 {
		t.Fatalf("walked %d packages; the walk missed the module", walked)
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
