package atomicfile

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestWriteFileRenamesIntoPlace(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "out")
	if err := os.WriteFile(p, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := WriteFile(p, []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(p); string(b) != "new" {
		t.Errorf("content %q, want new", b)
	}
	if fi, _ := os.Stat(p); fi.Mode().Perm() != 0o644 {
		t.Errorf("mode %v, want 0644", fi.Mode().Perm())
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 1 {
		t.Errorf("directory holds %d entries, want only the output", len(entries))
	}
}

func TestWriteFailureLeavesPathAndNoTemp(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "out")
	if err := os.WriteFile(p, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	boom := errors.New("boom")
	err := Write(p, 0o644, func(w io.Writer) error {
		w.Write([]byte("partial"))
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("err %v, want boom", err)
	}
	if b, _ := os.ReadFile(p); string(b) != "old" {
		t.Errorf("content %q, want the file left as it was", b)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 1 {
		t.Errorf("directory holds %d entries, want the temporary file removed", len(entries))
	}
}
