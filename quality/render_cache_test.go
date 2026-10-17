//go:build quality

package quality

import (
	"bytes"
	"context"
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
	first, err := os.ReadFile(o.PNG)
	if err != nil {
		t.Fatal(err)
	}
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
	last, err := os.ReadFile(o.PNG)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(first, last) {
		t.Error("PNG did not change after facts.html changed")
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
	a, err := raster.Load(got)
	if err != nil {
		t.Fatal(err)
	}
	b, err := raster.Load(ref)
	if err != nil {
		t.Fatal(err)
	}
	if a.Bounds() != b.Bounds() {
		t.Fatalf("sizes differ: %v vs %v", a.Bounds(), b.Bounds())
	}
	diff := 0
	for y := a.Bounds().Min.Y; y < a.Bounds().Max.Y; y++ {
		for x := a.Bounds().Min.X; x < a.Bounds().Max.X; x++ {
			if a.At(x, y) != b.At(x, y) {
				diff++
			}
		}
	}
	if diff != 0 {
		t.Errorf("%d pixels differ from the file:// reference (%s vs %s)", diff, got, ref)
	}
}
