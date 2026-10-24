package generate

import (
	"image/color"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// seedRound writes a complete round of attempts (image and record) under pose.
func seedRound(t *testing.T, pose string, round int, model, prompt string, costs ...float64) {
	t.Helper()
	dir := filepath.Join(roundsDir(pose), roundName(round))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for i, c := range costs {
		base := attemptBase(i + 1)
		if err := os.WriteFile(filepath.Join(dir, base+".png"), pngOf(t, 16, 16, color.NRGBA{30, 60, 200, 255}), 0o644); err != nil {
			t.Fatal(err)
		}
		r := Record{Provider: "google", Model: model, Prompt: prompt, Round: round, Attempt: i + 1, CostUSD: c}
		if err := writeRecord(filepath.Join(dir, base+".json"), r); err != nil {
			t.Fatal(err)
		}
	}
}

func TestSheetHTML(t *testing.T) {
	root := t.TempDir()
	pose := filepath.Join(root, "wave")
	refs := filepath.Join(root, "refs")
	os.MkdirAll(refs, 0o755)
	ref1 := writeRef(t, refs, "front.png")
	ref2 := writeRef(t, refs, "side.png")
	seedRound(t, pose, 1, "model-one", "first <b>wave</b>", 0.10, 0.10)
	seedRound(t, pose, 2, "model-two", "second wave", 0.25, 0.25, 0.25)
	// A partial round is not shown.
	os.MkdirAll(filepath.Join(roundsDir(pose), "03.partial"), 0o755)

	if err := writeSheetHTML(pose, []string{ref1, ref2}); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(pose, "sheet.html"))
	if err != nil {
		t.Fatal(err)
	}
	h := string(b)
	at := func(s string) int {
		i := strings.Index(h, s)
		if i < 0 {
			t.Fatalf("sheet.html lacks %q:\n%s", s, h)
		}
		return i
	}
	// References first, in their own column, then rounds newest first.
	if !(at("../refs/front.png") < at("../refs/side.png") && at("../refs/side.png") < at("Round 2") && at("Round 2") < at("Round 1")) {
		t.Errorf("order wrong:\n%s", h)
	}
	for _, want := range []string{"model-two", "second wave", "$0.75", "model-one", "$0.20",
		"r02 a03 · $0.25", "r01 a01 · $0.10", `src="rounds/02/a03.png"`, `src="rounds/01/a02.png"`, "Pose total $0.95"} {
		at(want)
	}
	if strings.Contains(h, "first <b>wave</b>") || !strings.Contains(h, "first &lt;b&gt;wave&lt;/b&gt;") {
		t.Errorf("prompt not escaped")
	}
	if strings.Contains(h, "03.partial") || strings.Contains(h, "r03") {
		t.Errorf("partial round shown")
	}
	for _, m := range regexp.MustCompile(`font-size:\s*([0-9.]+)px`).FindAllStringSubmatch(h, -1) {
		if px, _ := strconv.ParseFloat(m[1], 64); px < 16 {
			t.Errorf("text %spx is under 16px", m[1])
		}
	}
	if strings.Contains(h, "<script") {
		t.Errorf("script in sheet")
	}
	// A record carrying its pixels shows them.
	r := recordAt(t, filepath.Join(roundsDir(pose), "02", "a01.json"))
	r.Width, r.Height = 1824, 2176
	if err := writeRecord(filepath.Join(roundsDir(pose), "02", "a01.json"), r); err != nil {
		t.Fatal(err)
	}
	if err := writeSheetHTML(pose, nil); err != nil {
		t.Fatal(err)
	}
	b, _ = os.ReadFile(filepath.Join(pose, "sheet.html"))
	if !strings.Contains(string(b), "r02 a01 · 1824×2176 · $0.25") {
		t.Errorf("sheet.html lacks the attempt's pixels:\n%s", b)
	}
}

func TestSheetShowsChargedFailures(t *testing.T) {
	pose := filepath.Join(t.TempDir(), "wave")
	seedRound(t, pose, 1, "model-one", "first", 0.10)
	rf := RoundFailures{Provider: "openrouter", Model: "model-one", Prompt: "first", Round: 1,
		Failures: []FailureRecord{{Index: 1, Reason: "no image", CostUSD: 0.04}, {Index: 2, Reason: "no image", CostUSD: 0.03}}}
	if err := writeFailures(filepath.Join(roundsDir(pose), "01"), rf); err != nil {
		t.Fatal(err)
	}
	// A round where every attempt failed but was charged.
	os.MkdirAll(filepath.Join(roundsDir(pose), "02"), 0o755)
	rf2 := RoundFailures{Provider: "openrouter", Model: "model-two", Prompt: "second", Round: 2,
		Failures: []FailureRecord{{Index: 0, Reason: "no image", CostUSD: 0.05}}}
	if err := writeFailures(filepath.Join(roundsDir(pose), "02"), rf2); err != nil {
		t.Fatal(err)
	}
	if err := writeSheetHTML(pose, nil); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(pose, "sheet.html"))
	h := string(b)
	for _, want := range []string{"Round 2", "model-two", "1 failed, $0.05 charged", "Round 1", "2 failed, $0.07 charged", "model-one · $0.17", "Pose total $0.22"} {
		if !strings.Contains(h, want) {
			t.Errorf("sheet.html lacks %q:\n%s", want, h)
		}
	}
	if strings.Index(h, "Round 2") > strings.Index(h, "Round 1") {
		t.Errorf("rounds not newest first")
	}
	// A round whose failure cost never came back says so.
	os.MkdirAll(filepath.Join(roundsDir(pose), "03"), 0o755)
	rf3 := RoundFailures{Provider: "openrouter", Model: "model-three", Prompt: "third", Round: 3,
		Failures: []FailureRecord{{Index: 0, Reason: "timed out", Unreported: true}}}
	if err := writeFailures(filepath.Join(roundsDir(pose), "03"), rf3); err != nil {
		t.Fatal(err)
	}
	if err := writeSheetHTML(pose, nil); err != nil {
		t.Fatal(err)
	}
	b, _ = os.ReadFile(filepath.Join(pose, "sheet.html"))
	if !strings.Contains(string(b), "1 failed, $0.00 charged, 1 cost not reported") {
		t.Errorf("sheet.html lacks the unreported failure:\n%s", b)
	}
}
