package generate

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// hand builds an attempt on disk without a provider: round NN (a folder
// named name), attempt MM, with the bytes and record given.
func hand(t *testing.T, pose, round string, attempt int, png []byte, rec Record) {
	t.Helper()
	dir := filepath.Join(pose, "rounds", round)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	base := attemptBase(attempt)
	if err := os.WriteFile(filepath.Join(dir, base+".png"), png, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := writeRecord(filepath.Join(dir, base+".json"), rec); err != nil {
		t.Fatal(err)
	}
}

func accept(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	return gen(t, append([]string{"accept"}, args...)...)
}

func TestAcceptCopiesAndRecords(t *testing.T) {
	pose := filepath.Join(t.TempDir(), "wave")
	rec := Record{Provider: "google", Model: "m", Prompt: "wave", Round: 1, Attempt: 2, CostUSD: 0.04, Created: "2026-10-24T00:00:00Z",
		Refs: []RefRecord{{Path: "front.png", SHA256: "abc"}}}
	hand(t, pose, "01", 2, []byte("pixels"), rec)
	out := filepath.Join(t.TempDir(), "assets", "wave.png")
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		t.Fatal(err)
	}
	code, stdout, errs := accept(t, "--pose", pose, "R01-A02", out)
	if code != 0 {
		t.Fatalf("code %d: %s", code, errs)
	}
	if b, _ := os.ReadFile(out); string(b) != "pixels" {
		t.Errorf("copy is %q", b)
	}
	prov := filepath.Join(filepath.Dir(out), "wave.provenance.json")
	got := recordAt(t, prov)
	if got.Prompt != "wave" || got.Round != 1 || got.Attempt != 2 || got.CostUSD != 0.04 || len(got.Refs) != 1 || got.Refs[0].SHA256 != "abc" {
		t.Errorf("provenance %+v", got)
	}
	if !strings.Contains(stdout, out) || !strings.Contains(stdout, prov) {
		t.Errorf("stdout %q", stdout)
	}
}

func TestAcceptRefusesExisting(t *testing.T) {
	for _, existing := range []string{"wave.png", "wave.provenance.json"} {
		pose := filepath.Join(t.TempDir(), "wave")
		hand(t, pose, "01", 1, []byte("pixels"), Record{Round: 1, Attempt: 1})
		dir := t.TempDir()
		keep := filepath.Join(dir, existing)
		if err := os.WriteFile(keep, []byte("old"), 0o644); err != nil {
			t.Fatal(err)
		}
		code, _, errs := accept(t, "--pose", pose, "r01-a01", filepath.Join(dir, "wave.png"))
		if code == 0 || !strings.Contains(errs, existing) {
			t.Errorf("%s: code %d, stderr %q", existing, code, errs)
		}
		if b, _ := os.ReadFile(keep); string(b) != "old" {
			t.Errorf("%s changed to %q", existing, b)
		}
		other := "wave.png"
		if existing == other {
			other = "wave.provenance.json"
		}
		if _, err := os.Stat(filepath.Join(dir, other)); !os.IsNotExist(err) {
			t.Errorf("%s was written beside an existing %s", other, existing)
		}
	}
}

func TestAcceptUnknownAttempt(t *testing.T) {
	pose := filepath.Join(t.TempDir(), "wave")
	hand(t, pose, "01", 1, []byte("a"), Record{Round: 1, Attempt: 1})
	hand(t, pose, "01", 2, []byte("b"), Record{Round: 1, Attempt: 2})
	hand(t, pose, "02.partial", 1, []byte("c"), Record{Round: 2, Attempt: 1})
	out := filepath.Join(t.TempDir(), "x.png")
	for _, id := range []string{"r01-a09", "r02-a01", "nonsense"} {
		code, _, errs := accept(t, "--pose", pose, id, out)
		if code == 0 || !strings.Contains(errs, "r01-a01") || !strings.Contains(errs, "r01-a02") || strings.Contains(errs[strings.Index(errs, "it has "):], "r02") {
			t.Errorf("%s: code %d, stderr %q", id, code, errs)
		}
	}
	if _, err := os.Stat(out); !os.IsNotExist(err) {
		t.Errorf("an output was written")
	}
}

func TestAcceptOutputMustBePNG(t *testing.T) {
	pose := filepath.Join(t.TempDir(), "wave")
	hand(t, pose, "01", 1, []byte("a"), Record{Round: 1, Attempt: 1})
	if code, _, _ := accept(t, "--pose", pose, "r01-a01", filepath.Join(t.TempDir(), "x.jpg")); code != 2 {
		t.Errorf("code %d", code)
	}
}

func TestAcceptChainSurvivesDeletion(t *testing.T) {
	root := t.TempDir()
	pose := filepath.Join(root, "wave")
	first := Record{Provider: "google", Prompt: "first", Round: 1, Attempt: 3}
	hand(t, pose, "01", 3, []byte("one"), first)
	second := Record{Provider: "google", Prompt: "second", Round: 2, Attempt: 1,
		Refs: []RefRecord{{Path: "rounds/01/a03.png", SHA256: "h", Attempt: &first}}}
	hand(t, pose, "02", 1, []byte("two"), second)
	out := filepath.Join(root, "final.png")
	if code, _, errs := accept(t, "--pose", pose, "r02-a01", out); code != 0 {
		t.Fatalf("code %d: %s", code, errs)
	}
	if err := os.RemoveAll(pose); err != nil {
		t.Fatal(err)
	}
	got := recordAt(t, filepath.Join(root, "final.provenance.json"))
	if got.Prompt != "second" || len(got.Refs) != 1 || got.Refs[0].Attempt == nil || got.Refs[0].Attempt.Prompt != "first" {
		t.Errorf("chain lost: %+v", got)
	}
}

func TestAcceptImageWriteFailureLeavesNothing(t *testing.T) {
	pose := filepath.Join(t.TempDir(), "wave")
	hand(t, pose, "01", 1, []byte("pixels"), Record{Round: 1, Attempt: 1})
	prev := writeImage
	writeImage = func(string, []byte, os.FileMode) error { return errors.New("disk full") }
	t.Cleanup(func() { writeImage = prev })
	dir := t.TempDir()
	code, _, errs := accept(t, "--pose", pose, "r01-a01", filepath.Join(dir, "x.png"))
	if code == 0 || !strings.Contains(errs, "disk full") {
		t.Errorf("code %d, stderr %q", code, errs)
	}
	if es, _ := os.ReadDir(dir); len(es) != 0 {
		t.Errorf("left behind: %v", es)
	}
}
