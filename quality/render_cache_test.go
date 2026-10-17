//go:build quality

package quality

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/lockyc/plate/internal/engine"
	"github.com/lockyc/plate/internal/raster"
	"github.com/lockyc/plate/internal/render"
)

const cachePage = `<!doctype html><meta charset=utf-8>
<style>@page{size:600px 200px;margin:0}</style><body style="margin:0;font:48px sans-serif"><p id=o>pending</p>
<script>var x=new XMLHttpRequest();x.onload=function(){var m=/id="v">([^<]*)</.exec(x.responseText);document.getElementById('o').textContent=m?m[1]:'none'};x.open('GET','facts.html');x.send()</script>`

func factsHTML(v string) string { return fmt.Sprintf(`<p id="v">%s</p>`, v) }

func writePage(t *testing.T, dir, v string) string {
	t.Helper()
	page := filepath.Join(dir, "index.html")
	for name, body := range map[string]string{"index.html": cachePage, "facts.html": factsHTML(v)} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return page
}

func TestRealRenderCache(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	dir := t.TempDir()
	page := writePage(t, dir, "ALPHA")
	out := t.TempDir()
	o := render.Options{
		Page: page, PNG: filepath.Join(out, "a.png"), PDF: filepath.Join(out, "a.pdf"),
		W: 600, H: 200, Scale: 1, Budget: render.DefaultBudget,
	}
	run := func(want render.Outcome, label string) {
		t.Helper()
		res, err := render.Run(context.Background(), o, io.Discard)
		if err != nil {
			t.Fatalf("%s: %v", label, err)
		}
		if res.Outcome != want {
			t.Fatalf("%s: outcome %v (%s), want %v", label, res.Outcome, res.Reason, want)
		}
	}
	run(render.Rendered, "first")
	first := filepath.Join(out, "first.png")
	if err := os.WriteFile(first, mustRead(t, o.PNG), 0o644); err != nil {
		t.Fatal(err)
	}
	pdf1 := sha256.Sum256(mustRead(t, o.PDF))
	if _, err := os.Stat(o.PDF); err != nil {
		t.Fatal(err)
	}
	run(render.Unchanged, "second")
	if err := os.WriteFile(filepath.Join(dir, "unrelated.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(render.Unchanged, "after unrelated edit")
	if err := os.WriteFile(filepath.Join(dir, "facts.html"), []byte(factsHTML("BRAVO")), 0o644); err != nil {
		t.Fatal(err)
	}
	run(render.Rendered, "after facts edit")
	if diffPixels(t, first, o.PNG) == 0 {
		t.Error("PNG did not change after facts.html changed")
	}
	if sha256.Sum256(mustRead(t, o.PDF)) == pdf1 {
		t.Error("PDF was not rewritten after facts.html changed")
	}
}

func TestRealRenderMatchesFileURL(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	dir := t.TempDir()
	page := writePage(t, dir, "CHARLIE")
	out := t.TempDir()
	got := filepath.Join(out, "got.png")
	ref := filepath.Join(out, "ref.png")
	const w, h = 600, 200
	o := render.Options{Page: page, PNG: got, W: w, H: h, Scale: 1, Budget: render.DefaultBudget}
	if _, err := render.Run(context.Background(), o, io.Discard); err != nil {
		t.Fatal(err)
	}
	chrome, err := engine.Resolve("chrome-headless-shell")
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(chrome, "--headless", "--disable-gpu", "--no-sandbox",
		"--allow-file-access-from-files", "--hide-scrollbars", "--virtual-time-budget=5000",
		"--force-device-scale-factor=1", fmt.Sprintf("--window-size=%d,%d", w, h),
		"--screenshot="+ref, "file://"+page)
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("reference: %v\n%s", err, b)
	}
	if n := diffPixels(t, got, ref); n != 0 {
		t.Errorf("%d pixels differ from the file:// reference", n)
	}
	// Controls: the quoted sibling value must move pixels, or a failed load
	// in both renders would pass vacuously.
	for name, facts := range map[string]string{"other value": factsHTML("DELTA"), "no facts.html": ""} {
		cdir := t.TempDir()
		cpage := writePage(t, cdir, "x")
		if facts == "" {
			os.Remove(filepath.Join(cdir, "facts.html"))
		} else {
			os.WriteFile(filepath.Join(cdir, "facts.html"), []byte(facts), 0o644)
		}
		cp := filepath.Join(out, "control.png")
		co := render.Options{Page: cpage, PNG: cp, W: w, H: h, Scale: 1, Budget: render.DefaultBudget}
		if _, err := render.Run(context.Background(), co, io.Discard); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if diffPixels(t, got, cp) == 0 {
			t.Errorf("control %q rendered identically: the sibling value is not reaching the page", name)
		}
	}
}

func mustRead(t *testing.T, p string) []byte {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// diffPixels counts pixels that differ between two PNGs of equal size.
func diffPixels(t *testing.T, a, b string) int {
	t.Helper()
	ia, err := raster.Load(a)
	if err != nil {
		t.Fatal(err)
	}
	ib, err := raster.Load(b)
	if err != nil {
		t.Fatal(err)
	}
	if ia.Bounds() != ib.Bounds() {
		t.Fatalf("sizes differ: %v vs %v", ia.Bounds(), ib.Bounds())
	}
	n := 0
	for y := ia.Bounds().Min.Y; y < ia.Bounds().Max.Y; y++ {
		for x := ia.Bounds().Min.X; x < ia.Bounds().Max.X; x++ {
			if ia.At(x, y) != ib.At(x, y) {
				n++
			}
		}
	}
	return n
}
