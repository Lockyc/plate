package video

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// named lists the entries of dir whose names start with name + ".".
func named(t *testing.T, dir, name string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), name+".") {
			out = append(out, e.Name())
		}
	}
	return out
}

func TestPublishRemovesStaleSiblings(t *testing.T) {
	tmp, out := t.TempDir(), t.TempDir()
	files := []string{"adding-nodes.mp4", "adding-nodes.poster.png", "adding-nodes.json"}
	for _, f := range files {
		writeFile(t, filepath.Join(tmp, f), "new "+f)
	}
	for _, f := range []string{"adding-nodes.webp", "adding-nodes.json", "other.mp4", "adding-nodes.png"} {
		writeFile(t, filepath.Join(out, f), "old")
	}
	if err := Publish(tmp, out, "adding-nodes", files); err != nil {
		t.Fatal(err)
	}
	want := []string{"adding-nodes.json", "adding-nodes.mp4", "adding-nodes.png", "adding-nodes.poster.png"}
	if got := named(t, out, "adding-nodes"); !reflect.DeepEqual(got, want) {
		t.Fatalf("out holds %q, want %q", got, want)
	}
	if b, _ := os.ReadFile(filepath.Join(out, "adding-nodes.json")); string(b) != "new adding-nodes.json" {
		t.Fatalf("json = %q", b)
	}
	for _, f := range []string{"other.mp4", "adding-nodes.png"} {
		if b, _ := os.ReadFile(filepath.Join(out, f)); string(b) != "old" {
			t.Fatalf("%s was touched", f)
		}
	}
}

func TestPublishFailureLeavesNothing(t *testing.T) {
	tmp, out := filepath.Join(t.TempDir(), "tmp"), t.TempDir()
	if err := os.Mkdir(tmp, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(tmp, "adding-nodes.mp4"), "new")
	writeFile(t, filepath.Join(tmp, "adding-nodes.json"), "new")
	writeFile(t, filepath.Join(out, "adding-nodes.webm"), "old")
	err := Publish(tmp, out, "adding-nodes", []string{"adding-nodes.mp4", "adding-nodes.poster.png", "adding-nodes.json"})
	if err == nil {
		t.Fatal("publish with a missing file succeeded")
	}
	if n := named(t, out, "adding-nodes"); len(n) != 0 {
		t.Fatalf("out holds %q", n)
	}
	if _, err := os.Stat(tmp); !os.IsNotExist(err) {
		t.Fatalf("the temp dir survived: %v", err)
	}
}
