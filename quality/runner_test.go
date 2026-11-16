package quality

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func fakePlate(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "plate")
	os.WriteFile(p, []byte("#!/bin/sh\n"+body+"\n"), 0o755)
	return p
}

func TestRunCase(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	env := Env{Assets: "/assets", Tmp: tmp, Plate: fakePlate(t, `
case "$1" in
  ok) printf done > "$2" ;;
  refuse) echo "render would be 300.0 MP" >&2; exit 1 ;;
esac`)}
	c := Case{Name: "ok", Files: map[string]string{"plate.toml": `synthesis = "forbid"`}, Run: []string{"ok", "{tmp:out.txt}"}}
	if err := env.RunCase(ctx, c); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(tmp, "out.txt")); string(b) != "done" {
		t.Errorf("out.txt = %q", b)
	}
	if b, _ := os.ReadFile(filepath.Join(tmp, "plate.toml")); !strings.Contains(string(b), "forbid") {
		t.Error("files were not written into the case directory")
	}
	refuse := Case{Name: "r", Run: []string{"refuse"}, Exit: 1, Stderr: "MP", Absent: []string{"{tmp:out.png}"}}
	if err := env.RunCase(ctx, refuse); err != nil {
		t.Fatal(err)
	}
	wrong := refuse
	wrong.Stderr = "something else"
	if err := env.RunCase(ctx, wrong); err == nil {
		t.Error("stderr mismatch passed")
	}
	if err := env.RunCase(ctx, Case{Name: "x", Run: []string{"refuse"}}); err == nil {
		t.Error("unexpected exit 1 passed")
	}
}

func TestRunCaseServes(t *testing.T) {
	tmp := t.TempDir()
	env := Env{Assets: "/assets", Tmp: tmp, Plate: fakePlate(t, `printf %s "$1" > "$2"`)}
	c := Case{Name: "s", Serve: true, Run: []string{"{url}/page.html", "{tmp:url.txt}"}}
	if err := env.RunCase(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(tmp, "url.txt")); !strings.HasPrefix(string(b), "http://127.0.0.1:") || !strings.HasSuffix(string(b), "/page.html") {
		t.Errorf("{url} expanded to %q", b)
	}
}

func TestExpand(t *testing.T) {
	env := Env{Assets: "/a", Tmp: "/t", Plate: "/bin/plate", URL: "http://h"}
	if got := env.Expand("{asset:p.jpg}:{tmp:o.png}:{url}/x"); got != "/a/p.jpg:/t/o.png:http://h/x" {
		t.Errorf("Expand = %q", got)
	}
	p := env.ExpandParams(Params{"a": "{tmp:x.png}", "list": []any{"{asset:y}"}, "n": 3.0})
	if p["a"] != "/t/x.png" || p["list"].([]any)[0] != "/a/y" || p["n"] != 3.0 {
		t.Errorf("ExpandParams = %v", p)
	}
}
