package generate

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"image"
	"image/color"
	"image/png"
	"io"
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
	return googleServerOn(t, fail, nil)
}

// googleServerOn is googleServer that also calls each before answering.
func googleServerOn(t *testing.T, fail func(i int) bool, each func()) *server {
	t.Helper()
	m, err := pins.ImageModelFor("google", "")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(m.KeyEnv, "test-key-not-real")
	out := pngOf(t, 4, 4, color.NRGBA{0, 0, 255, 255})
	s := &server{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if each != nil {
			each()
		}
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
		// The size asked for and the pixels that came back (the fake's 4x4).
		if r.Width != 4 || r.Height != 4 {
			t.Errorf("record %d: %dx%d, want the written PNG's 4x4", i, r.Width, r.Height)
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
	if !strings.HasSuffix(out, "pose total $0.40\n"+filepath.Join(pose, "sheet.html")+"\n"+filepath.Join(pose, "sheet.png")+"\n") {
		t.Errorf("stdout does not end with the pose total and sheet paths:\n%s", out)
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
	if _, err := os.Stat(filepath.Join(pose, "rounds", "04", "a01.png")); err != nil {
		t.Errorf("second run did not write round 04, above the partial: %v", err)
	}
	if _, err := os.Stat(filepath.Join(stale, "a01.json")); err != nil {
		t.Errorf("partial round not kept: %v", err)
	}
	if want := "plate generate: rounds/03.partial holds an unfinished round; its images are kept\n"; e != want {
		t.Errorf("stderr %q, want %q", e, want)
	}
	rs, err := LoadPose(pose)
	if err != nil {
		t.Fatal(err)
	}
	if len(rs) != 2 || rs[0].Round != 1 || rs[1].Round != 4 {
		t.Errorf("LoadPose: %+v", rs)
	}
	if !strings.HasSuffix(out, "pose total $0.20\n"+filepath.Join(pose, "sheet.html")+"\n"+filepath.Join(pose, "sheet.png")+"\n") {
		t.Errorf("total counts the partial round:\n%s", out)
	}
}

// TestClaimSkipsATakenNumber: another run's NN.partial or NN at the number
// claimed (here plain files, which the scan does not count) is stepped past.
func TestClaimSkipsATakenNumber(t *testing.T) {
	needMagick(t)
	googleServer(t, nil)
	dir := t.TempDir()
	pose := filepath.Join(dir, "wave")
	ref := writeRef(t, dir, "front.png")
	args := []string{"--pose", pose, "--prompt", "p", "--ref", ref, "--provider", "google", "--n", "1"}
	if code, _, e := gen(t, args...); code != 0 {
		t.Fatal(e)
	}
	rd := filepath.Join(pose, "rounds")
	os.WriteFile(filepath.Join(rd, "02.partial"), nil, 0o644)
	os.WriteFile(filepath.Join(rd, "03"), nil, 0o644)
	if code, _, e := gen(t, args...); code != 0 {
		t.Fatal(e)
	}
	if _, err := os.Stat(filepath.Join(rd, "04", "a01.png")); err != nil {
		t.Errorf("round not written at 04: %v", err)
	}
	if _, err := os.Stat(filepath.Join(rd, "03.partial")); !os.IsNotExist(err) {
		t.Errorf("03.partial left after stepping past 03: %v", err)
	}
}

// TestWriteFailureWritesTheRest: attempt 2 cannot be written (a folder
// stands where its image goes); 1, 3 and 4 still are, in the kept partial.
func TestWriteFailureWritesTheRest(t *testing.T) {
	needMagick(t)
	dir := t.TempDir()
	pose := filepath.Join(dir, "wave")
	partial := filepath.Join(pose, "rounds", "01.partial")
	// The round is claimed before the requests, so the folder is there now.
	googleServerOn(t, nil, func() { os.MkdirAll(filepath.Join(partial, "a02.png", "x"), 0o755) })
	ref := writeRef(t, dir, "front.png")
	code, _, errs := gen(t, "--pose", pose, "--prompt", "p", "--ref", ref, "--provider", "google")
	if code != 1 {
		t.Fatalf("code %d, want 1: %s", code, errs)
	}
	for _, n := range []string{"a01", "a03", "a04"} {
		for _, ext := range []string{".png", ".json"} {
			if fi, err := os.Stat(filepath.Join(partial, n+ext)); err != nil || fi.IsDir() {
				t.Errorf("%s%s not written: %v", n, ext, err)
			}
		}
	}
	if _, err := os.Stat(filepath.Join(partial, "a02.json")); !os.IsNotExist(err) {
		t.Errorf("a02.json written without its image: %v", err)
	}
	if !strings.Contains(errs, "attempt 2") || strings.Contains(errs, "attempt 1:") || !strings.Contains(errs, partial) {
		t.Errorf("stderr does not name attempt 2 and the partial folder:\n%s", errs)
	}
	if _, err := os.Stat(filepath.Join(pose, "rounds", "01")); !os.IsNotExist(err) {
		t.Errorf("an incomplete round was renamed complete: %v", err)
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

// Only a partial round holding something is named as keeping images; an
// empty one holds nothing paid. Both keep their numbers.
func TestEmptyPartialIsNotNamed(t *testing.T) {
	needMagick(t)
	googleServer(t, nil)
	dir := t.TempDir()
	pose := filepath.Join(dir, "wave")
	os.MkdirAll(filepath.Join(roundsDir(pose), "01.partial"), 0o755)
	full := filepath.Join(roundsDir(pose), "02.partial")
	os.MkdirAll(full, 0o755)
	os.WriteFile(filepath.Join(full, "a01.png"), pngOf(t, 4, 4, color.NRGBA{0, 0, 255, 255}), 0o644)
	code, _, errs := gen(t, "--pose", pose, "--prompt", "p", "--ref", writeRef(t, dir, "front.png"), "--provider", "google")
	if code != 0 {
		t.Fatalf("code %d: %s", code, errs)
	}
	if strings.Contains(errs, "01.partial") || !strings.Contains(errs, filepath.Join("rounds", "02.partial")+" holds an unfinished round") {
		t.Errorf("stderr %q: want 02.partial named and 01.partial not", errs)
	}
	if _, err := os.Stat(filepath.Join(roundsDir(pose), "03")); err != nil {
		t.Errorf("the round did not take the next free number: %v", err)
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

// A request the model cannot take is refused before a round is claimed or a
// request sent.
func TestRequestCheckedBeforeAnything(t *testing.T) {
	s := googleServer(t, nil)
	dir := t.TempDir()
	pose := filepath.Join(dir, "wave")
	ref := writeRef(t, dir, "front.png")
	for _, args := range [][]string{{"--size", "8K"}, {"--n", "0"}} {
		code, _, errs := gen(t, append([]string{"--pose", pose, "--prompt", "p", "--ref", ref, "--provider", "google"}, args...)...)
		if code != 2 || errs == "" {
			t.Errorf("%v: code %d, stderr %q", args, code, errs)
		}
	}
	if _, err := os.Stat(pose); !os.IsNotExist(err) {
		t.Errorf("pose folder created: %v", err)
	}
	if s.n.Load() != 0 {
		t.Error("the provider was called")
	}
}

func TestModelNeedsProvider(t *testing.T) {
	code, _, errs := gen(t, "--pose", t.TempDir(), "--prompt", "p", "--ref", "x.png", "--model", "gpt-image-2")
	if code != 2 || !strings.Contains(errs, "--model needs --provider") {
		t.Fatalf("code %d, stderr %q", code, errs)
	}
}

func TestBareRunUsesDefaultModel(t *testing.T) {
	needMagick(t)
	d := pins.DefaultImageModel()
	var path string
	t.Setenv(d.KeyEnv, "test-key-not-real")
	img := base64.StdEncoding.EncodeToString(pngOf(t, 4, 4, color.NRGBA{0, 0, 255, 255}))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		path = r.URL.Path + string(b)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(imageAnswer(img, 0.04))
	}))
	defer srv.Close()
	t.Setenv("PLATE_GENAI_"+strings.ToUpper(d.Provider)+"_URL", srv.URL)
	dir := t.TempDir()
	pose := filepath.Join(dir, "wave")
	code, _, e := gen(t, "--pose", pose, "--prompt", "p", "--ref", writeRef(t, dir, "front.png"), "--n", "1")
	if code != 0 {
		t.Fatalf("code %d: %s", code, e)
	}
	if !strings.Contains(path, d.ID) {
		t.Errorf("request did not name the default model %s: %.200s", d.ID, path)
	}
	recs, err := LoadPose(pose)
	if err != nil || len(recs) != 1 || recs[0].Provider != d.Provider || recs[0].Model != d.ID {
		t.Errorf("record %+v, err %v", recs, err)
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

// magickRef makes a ref with magick from args (the output path appended).
func magickRef(t *testing.T, name string, args ...string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if _, err := engine.Run(ctx, engine.Cmd{Engine: "magick", Args: append(args, p), Outputs: []string{p}}); err != nil {
		t.Fatal(err)
	}
	return p
}

// sentRef runs one attempt with ref and decodes the PNG the provider got.
func sentRef(t *testing.T, ref string) image.Image {
	t.Helper()
	s := googleServer(t, nil)
	pose := filepath.Join(t.TempDir(), "wave")
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
	return img
}

func TestRefCMYKJPEGGoesAsSRGB(t *testing.T) {
	needMagick(t)
	ref := magickRef(t, "cmyk.jpg", "-size", "64x64", "xc:red", "-colorspace", "CMYK")
	r, g, b, _ := sentRef(t, ref).At(32, 32).RGBA()
	if r>>8 < 200 || g>>8 > 60 || b>>8 > 60 {
		t.Errorf("CMYK red sent as rgb(%d,%d,%d), want red", r>>8, g>>8, b>>8)
	}
}

func TestRefLargeJPEGDownscaled(t *testing.T) {
	needMagick(t)
	ref := magickRef(t, "big.jpg", "-size", "6000x4000", "xc:gray")
	if b := sentRef(t, ref).Bounds(); b.Dx() != 2048 || b.Dy() != 1365 {
		t.Errorf("6000x4000 sent at %dx%d, want 2048x1365", b.Dx(), b.Dy())
	}
}

// TestInterruptedRoundKeepsWhatArrived: cancelled after two of four answers,
// the round holds those two and is complete.
func TestInterruptedRoundKeepsWhatArrived(t *testing.T) {
	needMagick(t)
	m, _ := pins.ImageModelFor("google", "")
	t.Setenv(m.KeyEnv, "test-key-not-real")
	out := pngOf(t, 4, 4, color.NRGBA{0, 0, 255, 255})
	cctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var n atomic.Int32
	var answered sync.WaitGroup
	answered.Add(2)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if n.Add(1) <= 2 {
			defer answered.Done()
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{"responseId": "r", "candidates": []any{map[string]any{
				"finishReason": "STOP",
				"content": map[string]any{"role": "model", "parts": []any{map[string]any{"inlineData": map[string]any{
					"mimeType": "image/png", "data": base64.StdEncoding.EncodeToString(out),
				}}}},
			}}})
			return
		}
		// The server sees the client hang up only once the body is read.
		io.Copy(io.Discard, r.Body)
		answered.Wait()
		// Give the client time to read the two answers before the cancel.
		time.Sleep(200 * time.Millisecond)
		cancel()
		select {
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}
	}))
	defer srv.Close()
	t.Setenv("PLATE_GENAI_GOOGLE_URL", srv.URL)
	dir := t.TempDir()
	pose := filepath.Join(dir, "wave")
	ref := writeRef(t, dir, "front.png")
	var o, e bytes.Buffer
	code := Main(cctx, []string{"--pose", pose, "--prompt", "p", "--ref", ref, "--provider", "google"}, &o, &e)
	if code != 1 {
		t.Fatalf("code %d, want 1: %s", code, e.String())
	}
	es, _ := os.ReadDir(filepath.Join(pose, "rounds"))
	if len(es) != 1 || es[0].Name() != "01" {
		t.Fatalf("rounds/ holds %v, want only 01", es)
	}
	got, _ := filepath.Glob(filepath.Join(pose, "rounds", "01", "a*.png"))
	if len(got) != 2 {
		t.Errorf("round 01 holds %d attempts, want 2", len(got))
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

// openrouterServer is a fake OpenRouter endpoint. answer(i, image) gives the
// i-th request's (from 0) JSON answer; image is a PNG's base64 to put in it.
func openrouterServer(t *testing.T, answer func(i int, image string) map[string]any) {
	t.Helper()
	m, err := pins.ImageModelFor("openrouter", "")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(m.KeyEnv, "test-key-not-real")
	img := base64.StdEncoding.EncodeToString(pngOf(t, 4, 4, color.NRGBA{0, 0, 255, 255}))
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(answer(int(n.Add(1)-1), img))
	}))
	t.Cleanup(srv.Close)
	t.Setenv("PLATE_GENAI_OPENROUTER_URL", srv.URL)
}

func imageAnswer(image string, cost any) map[string]any {
	v := map[string]any{"data": []any{map[string]any{"b64_json": image, "media_type": "image/png"}}}
	if cost != nil {
		v["usage"] = map[string]any{"cost": cost}
	}
	return v
}

func TestUnreportedCostWarns(t *testing.T) {
	needMagick(t)
	openrouterServer(t, func(i int, image string) map[string]any { return imageAnswer(image, nil) })
	dir := t.TempDir()
	pose := filepath.Join(dir, "wave")
	code, o, e := gen(t, "--pose", pose, "--prompt", "p", "--ref", writeRef(t, dir, "front.png"), "--provider", "openrouter", "--n", "2")
	if code != 0 {
		t.Fatalf("code %d, stderr %q", code, e)
	}
	if e != "plate generate: openrouter did not report the cost of 2 attempts; they were charged but are recorded at $0.00\n" {
		t.Errorf("stderr is not one warning line about the 2 unreported attempts:\n%q", e)
	}
	if !strings.Contains(o, "\nunreported 2\n") {
		t.Errorf("stdout lacks the unreported line:\n%s", o)
	}
	if !strings.Contains(o, filepath.Join(pose, "rounds", "01", "a02.png")) {
		t.Errorf("the paid images were not written:\n%s", o)
	}
	if r := recordAt(t, filepath.Join(pose, "rounds", "01", "a01.json")); r.CostBasis != "unreported" || r.CostUSD != 0 {
		t.Errorf("record %+v", r)
	}
	code, _, e = gen(t, "--pose", pose, "--prompt", "p", "--ref", writeRef(t, dir, "front.png"), "--provider", "openrouter", "--n", "1")
	if code != 0 || e != "plate generate: openrouter did not report the cost of 1 attempt; it was charged but is recorded at $0.00\n" {
		t.Errorf("one attempt: code %d, stderr %q", code, e)
	}
}

func TestChargedFailuresReachTheTotal(t *testing.T) {
	needMagick(t)
	// Requests 0 and 1 make an image at $0.05; the rest make none, charged $0.04.
	openrouterServer(t, func(i int, image string) map[string]any {
		if i < 2 {
			return imageAnswer(image, 0.05)
		}
		return map[string]any{"data": []any{}, "usage": map[string]any{"cost": 0.04}}
	})
	dir := t.TempDir()
	pose := filepath.Join(dir, "wave")
	code, o, e := gen(t, "--pose", pose, "--prompt", "p", "--ref", writeRef(t, dir, "front.png"), "--provider", "openrouter", "--n", "4")
	if code != 1 || !strings.Contains(e, "2 of 4 attempts failed") {
		t.Fatalf("code %d, stderr %q", code, e)
	}
	if !strings.Contains(o, "pose total $0.18\n") {
		t.Errorf("pose total does not cover the charged failures:\n%s", o)
	}
	fs, err := LoadFailures(pose)
	if err != nil || len(fs) != 1 || len(fs[0].Failures) != 2 || fs[0].Round != 1 || fs[0].Failures[0].CostUSD != 0.04 {
		t.Fatalf("failures %+v, %v", fs, err)
	}
	// A round where every attempt failed, but was charged, is kept.
	openrouterServer(t, func(i int, image string) map[string]any {
		return map[string]any{"data": []any{}, "usage": map[string]any{"cost": 0.04}}
	})
	code, o, _ = gen(t, "--pose", pose, "--prompt", "p", "--ref", writeRef(t, dir, "front.png"), "--provider", "openrouter", "--n", "2")
	if code != 1 || !strings.Contains(o, "pose total $0.26\n") {
		t.Errorf("all failed: code %d, stdout:\n%s", code, o)
	}
	es, err := os.ReadDir(filepath.Join(pose, "rounds", "02"))
	if err != nil || len(es) != 1 || es[0].Name() != "failures.json" {
		t.Errorf("round 02 holds %v (%v), want only failures.json", es, err)
	}
	// A round whose failures cost nothing leaves nothing.
	openrouterServer(t, func(i int, image string) map[string]any {
		return map[string]any{"data": []any{}, "usage": map[string]any{"cost": 0}}
	})
	gen(t, "--pose", pose, "--prompt", "p", "--ref", writeRef(t, dir, "front.png"), "--provider", "openrouter", "--n", "1")
	if _, err := os.Stat(filepath.Join(pose, "rounds", "03")); !os.IsNotExist(err) {
		t.Errorf("an uncharged failed round was kept: %v", err)
	}
}

// A failed attempt whose cost never came back may have been charged: the
// round is kept with it in failures.json, and it is counted as unreported.
func TestUnreportedFailuresKeepTheRound(t *testing.T) {
	needMagick(t)
	openrouterServer(t, func(i int, image string) map[string]any {
		if i == 0 {
			return imageAnswer(image, 0.05)
		}
		return map[string]any{"data": []any{}}
	})
	dir := t.TempDir()
	pose := filepath.Join(dir, "wave")
	code, o, e := gen(t, "--pose", pose, "--prompt", "p", "--ref", writeRef(t, dir, "front.png"), "--provider", "openrouter", "--n", "3")
	if code != 1 || !strings.Contains(e, "did not report the cost of 2 attempts") {
		t.Fatalf("code %d, stderr %q", code, e)
	}
	if !strings.Contains(o, "\nunreported 2\n") {
		t.Errorf("stdout lacks the unreported line:\n%s", o)
	}
	fs, err := LoadFailures(pose)
	if err != nil || len(fs) != 1 || len(fs[0].Failures) != 2 || !fs[0].Failures[0].Unreported {
		t.Fatalf("failures %+v, %v", fs, err)
	}
	// Every attempt failed unreported: the round is still kept.
	openrouterServer(t, func(i int, image string) map[string]any { return map[string]any{"data": []any{}} })
	code, o, _ = gen(t, "--pose", pose, "--prompt", "p", "--ref", writeRef(t, dir, "front.png"), "--provider", "openrouter", "--n", "1")
	if code != 1 || !strings.Contains(o, "\nunreported 1\n") {
		t.Errorf("all unreported: code %d, stdout:\n%s", code, o)
	}
	if _, err := os.Stat(filepath.Join(pose, "rounds", "02", failuresName)); err != nil {
		t.Errorf("round 02 not kept: %v", err)
	}
}

func TestListModels(t *testing.T) {
	code, o, e := gen(t, "--list-models", "--provider", "openrouter")
	var want []string
	for _, m := range pins.ImageModels {
		if m.Provider == "openrouter" {
			want = append(want, m.ID)
		}
	}
	if code != 0 || e != "" || o != strings.Join(want, "\n")+"\n" {
		t.Errorf("code %d, stdout %q, stderr %q", code, o, e)
	}
	if code, _, _ := gen(t, "--list-models", "--provider", "nobody"); code != 2 {
		t.Errorf("unknown provider: code %d, want 2", code)
	}
}

func TestListModelsAllProviders(t *testing.T) {
	code, o, e := gen(t, "--list-models")
	if code != 0 || e != "" {
		t.Fatalf("code %d, stderr %q", code, e)
	}
	lines := strings.Split(strings.TrimSuffix(o, "\n"), "\n")
	if len(lines) != len(pins.ImageModels) {
		t.Fatalf("%d lines for %d models:\n%s", len(lines), len(pins.ImageModels), o)
	}
	d := pins.DefaultImageModel()
	marked := 0
	for i, m := range pins.ImageModels {
		if !strings.HasPrefix(lines[i], m.Provider+" "+m.ID) {
			t.Errorf("line %d = %q", i, lines[i])
		}
		if strings.Contains(lines[i], "default") {
			marked++
			if m.ID != d.ID || m.Provider != d.Provider {
				t.Errorf("default marked on %q", lines[i])
			}
		}
	}
	if marked != 1 {
		t.Errorf("%d lines marked default", marked)
	}
}

func TestPoseTotal(t *testing.T) {
	if got := PoseTotal([]Record{{CostUSD: 0.25}, {CostUSD: 1.5}}); got != 1.75 {
		t.Errorf("PoseTotal = %v", got)
	}
}

// stubSheet replaces the sheet writer, which needs Chrome, with a recorder
// of the calls it gets; omitted and err are what it returns.
func stubSheet(t *testing.T, omitted int, err error) *[][]string {
	t.Helper()
	var calls [][]string
	prev := writeSheet
	writeSheet = func(_ context.Context, dir string, refs []string) (int, error) {
		calls = append(calls, append([]string{dir}, refs...))
		return omitted, err
	}
	t.Cleanup(func() { writeSheet = prev })
	return &calls
}

func TestSheetOmittedRoundsAreNamed(t *testing.T) {
	needMagick(t)
	googleServer(t, nil)
	stubSheet(t, 3, nil)
	dir := t.TempDir()
	pose := filepath.Join(dir, "wave")
	code, out, errs := gen(t, "--pose", pose, "--prompt", "p", "--ref", writeRef(t, dir, "front.png"), "--provider", "google")
	if code != 0 || strings.Count(errs, "\n") != 1 || !strings.Contains(errs, "the 3 older are only in sheet.html") {
		t.Errorf("code %d, stderr %q; want one line naming the 3 older rounds", code, errs)
	}
	if !strings.Contains(out, filepath.Join(pose, "sheet.png")) {
		t.Errorf("stdout %q lacks the sheet", out)
	}
}

func TestSheetAfterEveryRound(t *testing.T) {
	needMagick(t)
	googleServer(t, func(i int) bool { return i%2 == 1 })
	calls := stubSheet(t, 0, nil)
	dir := t.TempDir()
	pose := filepath.Join(dir, "wave")
	ref := writeRef(t, dir, "front.png")
	// A partial round (two attempts failed) still gets its sheet.
	code, out, errs := gen(t, "--pose", pose, "--prompt", "p", "--ref", ref, "--provider", "google")
	if code != 1 {
		t.Fatalf("code %d: %s", code, errs)
	}
	if !strings.Contains(out, filepath.Join(pose, "sheet.html")) || !strings.Contains(out, filepath.Join(pose, "sheet.png")) {
		t.Errorf("stdout %q lacks the sheet paths", out)
	}
	if len(*calls) != 1 || (*calls)[0][0] != pose || (*calls)[0][1] != ref {
		t.Errorf("sheet calls %v", *calls)
	}
}

func TestSheetFailureIsReported(t *testing.T) {
	needMagick(t)
	googleServer(t, nil)
	stubSheet(t, 0, errors.New("no chrome"))
	dir := t.TempDir()
	pose := filepath.Join(dir, "wave")
	ref := writeRef(t, dir, "front.png")
	code, out, errs := gen(t, "--pose", pose, "--prompt", "p", "--ref", ref, "--provider", "google")
	if code != 1 || !strings.Contains(errs, "no chrome") {
		t.Errorf("code %d, stderr %q", code, errs)
	}
	if _, err := os.Stat(filepath.Join(pose, "rounds", "01", "a01.png")); err != nil {
		t.Errorf("the paid round was disturbed: %v", err)
	}
	if !strings.Contains(out, "pose total") {
		t.Errorf("stdout %q", out)
	}
}

// TestMain keeps every test off Chrome: the sheet is stubbed out unless a
// test asks for the real one.
func TestMain(m *testing.M) {
	writeSheet = func(context.Context, string, []string) (int, error) { return 0, nil }
	os.Exit(m.Run())
}
