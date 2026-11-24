package tsv

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func write(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "a.tsv")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestRead(t *testing.T) {
	tb, err := Read(write(t, "frame\testimate\r\nb\t3\n\na\t4\textra\n"), "estimate", "frame")
	if err != nil {
		t.Fatal(err)
	}
	if len(tb.Rows) != 2 || tb.Get(0, "frame") != "b" || tb.Get(1, "estimate") != "4" || tb.Col("nope") != -1 {
		t.Errorf("table %+v", tb)
	}
}

func TestReadRefuses(t *testing.T) {
	for body, want := range map[string]string{
		"":                        "empty",
		"\n\n":                    "empty",
		"frame\tdetected\nx\t1\n": "want frame, estimate columns",
		"frame\testimate\nx\n":    "short row",
	} {
		if _, err := Read(write(t, body), "frame", "estimate"); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: err %v, want %q", body, err, want)
		}
	}
	if _, err := Read(filepath.Join(t.TempDir(), "none.tsv")); err == nil {
		t.Error("a missing file: no error")
	}
}
