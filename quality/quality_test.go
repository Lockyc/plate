//go:build quality

package quality

import (
	"context"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

func TestQuality(t *testing.T) {
	cases, err := LoadCases("cases.toml")
	if err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(t.TempDir(), "plate")
	if out, err := exec.Command("go", "build", "-o", bin, "..").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	assets, _ := filepath.Abs("assets")
	t.Logf("%d cases", len(cases))
	for _, c := range cases {
		t.Run(c.Name, func(t *testing.T) {
			if c.OS != "" && c.OS != runtime.GOOS {
				t.Skipf("runs on %s only", c.OS)
			}
			env := Env{Assets: assets, Tmp: t.TempDir(), Plate: bin}
			if err := env.RunCase(context.Background(), c); err != nil {
				t.Fatal(err)
			}
			if c.Metric == "" {
				return
			}
			v, err := Metrics[c.Metric](env.ExpandParams(c.Params))
			if err != nil {
				t.Fatal(err)
			}
			t.Logf("%s = %.6g", c.Metric, v)
			if c.Max != nil && v > *c.Max {
				t.Errorf("%s = %.6g, above the max %.6g", c.Metric, v, *c.Max)
			}
			if c.Min != nil && v < *c.Min {
				t.Errorf("%s = %.6g, below the min %.6g", c.Metric, v, *c.Min)
			}
		})
	}
}
