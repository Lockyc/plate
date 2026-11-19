package quality

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestAssetRegistry(t *testing.T) {
	assets, err := LoadAssets("assets.toml")
	if err != nil {
		t.Fatal(err)
	}
	listed := map[string]bool{}
	for _, a := range assets {
		listed[a.File] = true
		if a.Source == "" || a.Licence == "" {
			t.Errorf("%s: source and licence are required", a.File)
		}
		if a.Source == "synthetic" && a.Recipe == "" {
			t.Errorf("%s: a synthetic asset needs its recipe", a.File)
		}
		b, err := os.ReadFile(filepath.Join("assets", a.File))
		if err != nil {
			t.Errorf("%s: listed but unreadable: %v", a.File, err)
			continue
		}
		sum := sha256.Sum256(b)
		if got := hex.EncodeToString(sum[:]); got != a.SHA256 {
			t.Errorf("%s: sha256 %s, registry says %s", a.File, got, a.SHA256)
		}
	}
	entries, _ := os.ReadDir("assets")
	for _, e := range entries {
		if e.Name() == ".gitkeep" {
			continue
		}
		if !listed[e.Name()] {
			t.Errorf("assets/%s is not in assets.toml", e.Name())
		}
	}
}

func TestCasesAreWellFormed(t *testing.T) {
	cases, err := LoadCases("cases.toml")
	if err != nil {
		t.Fatal(err)
	}
	assets, _ := LoadAssets("assets.toml")
	known := map[string]bool{}
	for _, a := range assets {
		known[a.File] = true
	}
	ref := regexp.MustCompile(`\{asset:([^}]+)\}`)
	names := map[string]bool{}
	for _, c := range cases {
		if c.Name == "" || names[c.Name] {
			t.Errorf("case %q: name empty or repeated", c.Name)
		}
		names[c.Name] = true
		if c.Why == "" {
			t.Errorf("%s: why is required (the shortfall this case pins)", c.Name)
		}
		if c.OS != "" && c.OS != "darwin" && c.OS != "linux" {
			t.Errorf("%s: os %q: want darwin or linux", c.Name, c.OS)
		}
		if len(c.Run) == 0 {
			t.Errorf("%s: run is empty", c.Name)
		}
		if c.Exit != 0 && c.Stderr == "" && c.Stdout == "" {
			t.Errorf("%s: a failing case must say what its stderr or stdout contains", c.Name)
		}
		if c.Metric != "" {
			if _, ok := Metrics[c.Metric]; !ok {
				t.Errorf("%s: unknown metric %q", c.Name, c.Metric)
			}
			if c.Max == nil && c.Min == nil {
				t.Errorf("%s: metric %s has no threshold", c.Name, c.Metric)
			}
		}
		if !c.Serve && strings.Contains(c.text(), "{url}") {
			t.Errorf("%s: {url} needs serve = true", c.Name)
		}
		for _, m := range ref.FindAllStringSubmatch(c.text(), -1) {
			if !known[m[1]] {
				t.Errorf("%s: {asset:%s} is not registered", c.Name, m[1])
			}
		}
	}
}
