package generate

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lockyc/plate/internal/engine"
	"github.com/lockyc/plate/internal/pins"
)

var ctx = context.Background()

func needMagick(t *testing.T) {
	t.Helper()
	if _, err := engine.Resolve("magick"); err != nil {
		t.Skip(err)
	}
}

// pngOf encodes a w x h image of c.
func pngOf(t *testing.T, w, h int, c color.Color) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, c)
		}
	}
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func writeRef(t *testing.T, dir, name string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, pngOf(t, 8, 8, color.NRGBA{200, 30, 30, 255}), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// server is a fake Google endpoint. fail(i) says whether the i-th request
// (from 0) answers with finishReason SAFETY and no image.
type server struct {
	mu     sync.Mutex
	bodies []map[string]any
	n      atomic.Int32
}

func googleServer(t *testing.T, fail func(i int) bool) *server {
	t.Helper()
	m, err := pins.ImageModelFor("google", "")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(m.KeyEnv, "test-key-not-real")
	out := pngOf(t, 4, 4, color.NRGBA{0, 0, 255, 255})
	s := &server{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		i := int(s.n.Add(1) - 1)
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		s.mu.Lock()
		s.bodies = append(s.bodies, body)
		s.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if fail != nil && fail(i) {
			json.NewEncoder(w).Encode(map[string]any{"candidates": []any{map[string]any{
				"finishReason": "SAFETY",
				"content":      map[string]any{"role": "model", "parts": []any{}},
			}}})
			return
		}
		json.NewEncoder(w).Encode(map[string]any{
			"responseId": "resp-" + string(rune('a'+i)),
			"candidates": []any{map[string]any{
				"finishReason": "STOP",
				"content": map[string]any{"role": "model", "parts": []any{
					map[string]any{"inlineData": map[string]any{
						"mimeType": "image/png",
						"data":     base64.StdEncoding.EncodeToString(out),
					}},
				}},
			}},
		})
	}))
	t.Cleanup(srv.Close)
	t.Setenv("PLATE_GENAI_GOOGLE_URL", srv.URL)
	return s
}

// refsSent decodes the inline images of one request body.
func refsSent(t *testing.T, body map[string]any) [][]byte {
	t.Helper()
	var out [][]byte
	parts := body["contents"].([]any)[0].(map[string]any)["parts"].([]any)
	for _, p := range parts {
		inl, ok := p.(map[string]any)["inline_data"].(map[string]any)
		if !ok {
			continue
		}
		b, err := base64.StdEncoding.DecodeString(inl["data"].(string))
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, b)
	}
	return out
}

func fixClock(t *testing.T) {
	t.Helper()
	prev := now
	now = func() time.Time { return time.Date(2026, 10, 24, 9, 30, 0, 0, time.FixedZone("AEDT", 11*3600)) }
	t.Cleanup(func() { now = prev })
}

func gen(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var o, e bytes.Buffer
	code := Main(ctx, args, &o, &e)
	return code, o.String(), e.String()
}

func recordAt(t *testing.T, p string) Record {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	var r Record
	if err := json.Unmarshal(b, &r); err != nil {
		t.Fatal(err)
	}
	return r
}

func sum(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func TestRoundWritesAttemptsAndRecords(t *testing.T) {
	needMagick(t)
	fixClock(t)
	googleServer(t, nil)
	dir := t.TempDir()
	pose := filepath.Join(dir, "wave")
	ref := writeRef(t, dir, "front.png")
	code, out, errs := gen(t, "--pose", pose, "--prompt", "the monster, waving", "--ref", ref, "--provider", "google", "--aspect", "3:4")
	if code != 0 {
		t.Fatalf("code %d: %s", code, errs)
	}
	m, _ := pins.ImageModelFor("google", "")
	for i := 1; i <= 4; i++ {
		img := filepath.Join(pose, "rounds", "01", "a0"+string(rune('0'+i))+".png")
		if _, err := os.Stat(img); err != nil {
			t.Errorf("attempt %d: %v", i, err)
		}
		if !strings.Contains(out, img+"\n") {
			t.Errorf("stdout lacks %s:\n%s", img, out)
		}
		r := recordAt(t, strings.TrimSuffix(img, ".png")+".json")
		if r.Provider != "google" || r.Model != m.ID || r.Prompt != "the monster, waving" || r.Size != "2K" || r.Aspect != "3:4" {
			t.Errorf("record %d: %+v", i, r)
		}
		if r.Round != 1 || r.Attempt != i {
			t.Errorf("record %d: round %d attempt %d", i, r.Round, r.Attempt)
		}
		if r.CostUSD != m.PerImage["2K"] || r.CostBasis != "rate" || !strings.HasPrefix(r.ProviderID, "resp-") {
			t.Errorf("record %d cost/id: %+v", i, r)
		}
		if r.Created != "2026-10-23T22:30:00Z" {
			t.Errorf("record %d created %q", i, r.Created)
		}
		if len(r.Refs) != 1 || r.Refs[0].Path != ref || r.Refs[0].SHA256 != sum(t, ref) || r.Refs[0].Attempt != nil {
			t.Errorf("record %d refs %+v", i, r.Refs)
		}
	}
	if _, err := os.Stat(filepath.Join(pose, "rounds", "01.partial")); !os.IsNotExist(err) {
		t.Errorf("01.partial left behind: %v", err)
	}
	if !strings.HasSuffix(out, "pose total $0.40\n") {
		t.Errorf("stdout does not end with the pose total:\n%s", out)
	}
	if errs != "" {
		t.Errorf("stderr %q", errs)
	}
}

func TestRoundNumbering(t *testing.T) {
	needMagick(t)
	googleServer(t, nil)
	dir := t.TempDir()
	pose := filepath.Join(dir, "wave")
	ref := writeRef(t, dir, "front.png")
	args := []string{"--pose", pose, "--prompt", "p", "--ref", ref, "--provider", "google", "--n", "1"}
	if code, _, e := gen(t, args...); code != 0 {
		t.Fatal(e)
	}
	stale := filepath.Join(pose, "rounds", "03.partial")
	os.MkdirAll(stale, 0o755)
	os.WriteFile(filepath.Join(stale, "a01.json"), []byte(`{"cost_usd": 50}`), 0o644)
	code, out, e := gen(t, args...)
	if code != 0 {
		t.Fatal(e)
	}
	if _, err := os.Stat(filepath.Join(pose, "rounds", "02", "a01.png")); err != nil {
		t.Errorf("second run did not write round 02: %v", err)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Errorf("stale partial round kept: %v", err)
	}
	rs, err := LoadPose(pose)
	if err != nil {
		t.Fatal(err)
	}
	if len(rs) != 2 || rs[0].Round != 1 || rs[1].Round != 2 {
		t.Errorf("LoadPose: %+v", rs)
	}
	if !strings.HasSuffix(out, "pose total $0.20\n") {
		t.Errorf("total counts the partial round:\n%s", out)
	}
}

func TestPartialRoundKeepsSuccesses(t *testing.T) {
	needMagick(t)
	googleServer(t, func(i int) bool { return i%2 == 1 })
	dir := t.TempDir()
	pose := filepath.Join(dir, "wave")
	ref := writeRef(t, dir, "front.png")
	code, out, errs := gen(t, "--pose", pose, "--prompt", "p", "--ref", ref, "--provider", "google")
	if code != 1 {
		t.Fatalf("code %d, want 1: %s", code, errs)
	}
	for _, name := range []string{"a01.png", "a01.json", "a02.png", "a02.json"} {
		if _, err := os.Stat(filepath.Join(pose, "rounds", "01", name)); err != nil {
			t.Error(err)
		}
	}
	if _, err := os.Stat(filepath.Join(pose, "rounds", "01", "a03.png")); !os.IsNotExist(err) {
		t.Errorf("a03.png exists: %v", err)
	}
	if !strings.Contains(errs, "2 of 4 attempts failed") || strings.Count(errs, "SAFETY") != 2 {
		t.Errorf("stderr does not list the failures:\n%s", errs)
	}
	if !strings.Contains(out, "pose total $0.20") {
		t.Errorf("stdout %q", out)
	}
}

func TestKeyMissingBeforeAnything(t *testing.T) {
	m, _ := pins.ImageModelFor("google", "")
	t.Setenv(m.KeyEnv, "")
	var hit atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hit.Store(true) }))
	defer srv.Close()
	t.Setenv("PLATE_GENAI_GOOGLE_URL", srv.URL)
	dir := t.TempDir()
	pose := filepath.Join(dir, "wave")
	ref := writeRef(t, dir, "front.png")
	code, _, errs := gen(t, "--pose", pose, "--prompt", "p", "--ref", ref, "--provider", "google")
	if code != 1 || !strings.Contains(errs, m.KeyEnv) {
		t.Errorf("code %d, stderr %q", code, errs)
	}
	if _, err := os.Stat(pose); !os.IsNotExist(err) {
		t.Errorf("pose folder created: %v", err)
	}
	if hit.Load() {
		t.Error("the provider was called")
	}
}

func TestProviderRequired(t *testing.T) {
	code, _, errs := gen(t, "--pose", t.TempDir(), "--prompt", "p", "--ref", "x.png")
	if code != 2 || !strings.Contains(errs, "no default") {
		t.Fatalf("code %d, stderr %q", code, errs)
	}
	for _, p := range []string{"google", "openai", "bfl"} {
		if !strings.Contains(errs, p) {
			t.Errorf("stderr does not list %s: %q", p, errs)
		}
	}
	if strings.Count(errs, "google") != 1 {
		t.Errorf("providers not deduplicated: %q", errs)
	}
}

func TestRefNormalised(t *testing.T) {
	needMagick(t)
	s := googleServer(t, nil)
	dir := t.TempDir()
	ref := filepath.Join(dir, "big.webp")
	if _, err := engine.Run(ctx, engine.Cmd{Engine: "magick", Args: []string{"-size", "3000x2000", "xc:rgba(255,0,0,0.5)", ref}, Outputs: []string{ref}}); err != nil {
		t.Fatal(err)
	}
	pose := filepath.Join(dir, "wave")
	if code, _, e := gen(t, "--pose", pose, "--prompt", "p", "--ref", ref, "--provider", "google", "--n", "1"); code != 0 {
		t.Fatal(e)
	}
	sent := refsSent(t, s.bodies[0])
	if len(sent) != 1 {
		t.Fatalf("%d refs sent", len(sent))
	}
	img, err := png.Decode(bytes.NewReader(sent[0]))
	if err != nil {
		t.Fatalf("ref sent is not a PNG: %v", err)
	}
	if b := img.Bounds(); b.Dx() != 2048 || b.Dy() > 2048 {
		t.Errorf("ref sent at %dx%d, want a long edge of 2048", b.Dx(), b.Dy())
	}
	if _, _, _, a := img.At(10, 10).RGBA(); a == 0xffff {
		t.Error("ref sent lost its alpha")
	}
	r := recordAt(t, filepath.Join(pose, "rounds", "01", "a01.json"))
	if r.Refs[0].SHA256 != sum(t, ref) {
		t.Errorf("record SHA256 %s is not the original file's", r.Refs[0].SHA256)
	}
}

func TestRefChainEmbedded(t *testing.T) {
	needMagick(t)
	googleServer(t, nil)
	dir := t.TempDir()
	pose := filepath.Join(dir, "wave")
	ref := writeRef(t, dir, "front.png")
	if code, _, e := gen(t, "--pose", pose, "--prompt", "first", "--ref", ref, "--provider", "google", "--n", "2"); code != 0 {
		t.Fatal(e)
	}
	prev := filepath.Join(pose, "rounds", "01", "a02.png")
	if code, _, e := gen(t, "--pose", pose, "--prompt", "second", "--ref", prev, "--ref", ref, "--provider", "google", "--n", "1"); code != 0 {
		t.Fatal(e)
	}
	r := recordAt(t, filepath.Join(pose, "rounds", "02", "a01.json"))
	if len(r.Refs) != 2 {
		t.Fatalf("refs %+v", r.Refs)
	}
	a := r.Refs[0].Attempt
	if a == nil || a.Round != 1 || a.Attempt != 2 || a.Prompt != "first" || a.Refs[0].Path != ref {
		t.Errorf("chain %+v", a)
	}
	if r.Refs[1].Attempt != nil {
		t.Errorf("a plain ref has an attempt: %+v", r.Refs[1].Attempt)
	}
}

func TestSoftWarning(t *testing.T) {
	needMagick(t)
	googleServer(t, nil)
	dir := t.TempDir()
	pose := filepath.Join(dir, "wave")
	ref := writeRef(t, dir, "front.png")
	args := []string{"--pose", pose, "--prompt", "p", "--ref", ref, "--provider", "google"}
	if code, _, e := gen(t, args...); code != 0 || e != "" {
		t.Fatalf("below $10: code %d, stderr %q", code, e)
	}
	// A round already on disk that brings the pose to just under $10.
	old := filepath.Join(pose, "rounds", "02")
	os.MkdirAll(old, 0o755)
	b, _ := json.Marshal(Record{Provider: "google", Round: 2, Attempt: 1, CostUSD: 9.5})
	os.WriteFile(filepath.Join(old, "a01.json"), b, 0o644)
	code, _, e := gen(t, args...)
	if code != 0 {
		t.Fatal(e)
	}
	if strings.Count(e, "\n") != 1 || !strings.Contains(e, "$10.31") || !strings.Contains(e, "$10.00") {
		t.Errorf("stderr is not one warning line with the total:\n%q", e)
	}
}

func TestPoseTotal(t *testing.T) {
	if got := PoseTotal([]Record{{CostUSD: 0.25}, {CostUSD: 1.5}}); got != 1.75 {
		t.Errorf("PoseTotal = %v", got)
	}
}
