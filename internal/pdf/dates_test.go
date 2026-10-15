package pdf

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestPinDatesKeepsLengthAndTheRest(t *testing.T) {
	in := "%PDF-1.4\n<</Producer (Skia/PDF m155)\n/CreationDate (D:20261015072108+00'00')\n/ModDate (D:20261015072108+00'00')>>\nstream\n(D:20261015072108) stays\nendstream\n"
	path := filepath.Join(t.TempDir(), "a.pdf")
	if err := os.WriteFile(path, []byte(in), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := PinDates(path, time.Unix(0, 0)); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(path)
	want := "%PDF-1.4\n<</Producer (Skia/PDF m155)\n/CreationDate (D:19700101000000+00'00')\n/ModDate (D:19700101000000+00'00')>>\nstream\n(D:20261015072108) stays\nendstream\n"
	if string(got) != want {
		t.Fatalf("got\n%s", got)
	}
	if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o640 {
		t.Errorf("mode %v", fi.Mode().Perm())
	}
}

func TestSourceDate(t *testing.T) {
	t.Setenv("SOURCE_DATE_EPOCH", "")
	if d, err := SourceDate(); err != nil || !d.Equal(time.Unix(0, 0)) {
		t.Errorf("unset: %v, %v", d, err)
	}
	t.Setenv("SOURCE_DATE_EPOCH", "1760000000")
	if d, err := SourceDate(); err != nil || d.Unix() != 1760000000 {
		t.Errorf("set: %v, %v", d, err)
	}
	t.Setenv("SOURCE_DATE_EPOCH", "yesterday")
	if _, err := SourceDate(); err == nil {
		t.Error("bad value accepted")
	}
}
