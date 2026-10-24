package genai

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lockyc/plate/internal/pins"
)

const testKey = "test-key-not-real"

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// noSleep records each backoff wait instead of sleeping.
func noSleep(t *testing.T) *[]time.Duration {
	t.Helper()
	var mu sync.Mutex
	waits := []time.Duration{}
	prev := sleep
	sleep = func(ctx context.Context, d time.Duration) error {
		mu.Lock()
		waits = append(waits, d)
		mu.Unlock()
		return ctx.Err()
	}
	t.Cleanup(func() { sleep = prev })
	return &waits
}

func googleModel(t *testing.T) pins.ImageModel {
	t.Helper()
	m, err := pins.ImageModelFor("google", "")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(m.KeyEnv, testKey)
	return m
}

// googleServer points the google provider at h.
func googleServer(t *testing.T, h http.HandlerFunc) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	t.Setenv("PLATE_GENAI_GOOGLE_URL", srv.URL)
}

func imageResponse(png []byte, id string) map[string]any {
	return map[string]any{
		"responseId": id,
		"candidates": []any{map[string]any{
			"finishReason": "STOP",
			"content": map[string]any{"role": "model", "parts": []any{
				map[string]any{"inlineData": map[string]any{
					"mimeType": "image/png",
					"data":     base64.StdEncoding.EncodeToString(png),
				}},
			}},
		}},
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

func provider(t *testing.T) Provider {
	t.Helper()
	p, err := For("google")
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestGoogleSendsRefsAndPrompt(t *testing.T) {
	m := googleModel(t)
	ref := fixture(t, "ref.png")
	out := fixture(t, "out.png")
	var got struct {
		path, key string
		body      map[string]any
	}
	googleServer(t, func(w http.ResponseWriter, r *http.Request) {
		got.path = r.URL.Path
		got.key = r.Header.Get("x-goog-api-key")
		json.NewDecoder(r.Body).Decode(&got.body)
		writeJSON(w, imageResponse(out, "resp-1"))
	})
	imgs, fails, err := provider(t).Generate(context.Background(), Request{
		Model: m, Prompt: "the monster, waving", N: 1, Size: "2K", Aspect: "3:4",
		Refs: []Ref{{Path: "a.png", PNG: ref}, {Path: "b.png", PNG: ref}},
	})
	if err != nil || len(fails) != 0 {
		t.Fatalf("err %v, failures %v", err, fails)
	}
	if want := "/v1beta/models/" + m.ID + ":generateContent"; got.path != want {
		t.Errorf("path %q, want %q", got.path, want)
	}
	if got.key != testKey {
		t.Errorf("x-goog-api-key %q", got.key)
	}
	parts := got.body["contents"].([]any)[0].(map[string]any)["parts"].([]any)
	if len(parts) != 3 {
		t.Fatalf("parts %d, want 3 (2 refs + prompt)", len(parts))
	}
	for i := 0; i < 2; i++ {
		inl, ok := parts[i].(map[string]any)["inline_data"].(map[string]any)
		if !ok {
			t.Fatalf("part %d is not inline_data: %v", i, parts[i])
		}
		if inl["mime_type"] != "image/png" {
			t.Errorf("part %d mime_type %v", i, inl["mime_type"])
		}
		if dec, _ := base64.StdEncoding.DecodeString(inl["data"].(string)); !bytes.Equal(dec, ref) {
			t.Errorf("part %d data is not the ref", i)
		}
	}
	if parts[2].(map[string]any)["text"] != "the monster, waving" {
		t.Errorf("last part %v, want the prompt", parts[2])
	}
	gc := got.body["generationConfig"].(map[string]any)
	if mods, _ := json.Marshal(gc["responseModalities"]); string(mods) != `["IMAGE"]` {
		t.Errorf("responseModalities %s", mods)
	}
	ic := gc["imageConfig"].(map[string]any)
	if ic["imageSize"] != "2K" || ic["aspectRatio"] != "3:4" {
		t.Errorf("imageConfig %v", ic)
	}
	if len(imgs) != 1 || !bytes.Equal(imgs[0].PNG, out) || imgs[0].ProviderID != "resp-1" {
		t.Errorf("images %+v", imgs)
	}
}

func TestGoogleNAttemptsAreNRequests(t *testing.T) {
	m := googleModel(t)
	out := fixture(t, "out.png")
	var n atomic.Int32
	all := make(chan struct{})
	googleServer(t, func(w http.ResponseWriter, r *http.Request) {
		if n.Add(1) == 4 {
			close(all)
		}
		// Each request waits for all four: a serial client would never get here.
		select {
		case <-all:
		case <-time.After(5 * time.Second):
			http.Error(w, `{"error":{"message":"requests were not concurrent"}}`, http.StatusBadRequest)
			return
		}
		writeJSON(w, imageResponse(out, "r"))
	})
	imgs, fails, err := provider(t).Generate(context.Background(), Request{Model: m, Prompt: "p", N: 4, Size: "1K"})
	if err != nil || len(fails) != 0 {
		t.Fatalf("err %v, failures %v", err, fails)
	}
	if len(imgs) != 4 || n.Load() != 4 {
		t.Errorf("%d images from %d requests, want 4 and 4", len(imgs), n.Load())
	}
}

func TestGoogleNoImageIsAFailure(t *testing.T) {
	m := googleModel(t)
	googleServer(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"candidates": []any{map[string]any{
			"finishReason": "SAFETY",
			"content":      map[string]any{"role": "model", "parts": []any{}},
		}}})
	})
	imgs, fails, err := provider(t).Generate(context.Background(), Request{Model: m, Prompt: "p", N: 1, Size: "1K"})
	if err != nil {
		t.Fatal(err)
	}
	if len(imgs) != 0 || len(fails) != 1 {
		t.Fatalf("%d images, %d failures", len(imgs), len(fails))
	}
	if fails[0].Index != 0 || !strings.Contains(fails[0].Reason, "SAFETY") {
		t.Errorf("failure %+v", fails[0])
	}
}

func TestGoogleNonPNGIsAFailure(t *testing.T) {
	m := googleModel(t)
	googleServer(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, imageResponse([]byte("\xff\xd8\xff not a png"), "r"))
	})
	imgs, fails, err := provider(t).Generate(context.Background(), Request{Model: m, Prompt: "p", N: 1, Size: "1K"})
	if err != nil {
		t.Fatal(err)
	}
	if len(imgs) != 0 || len(fails) != 1 || !strings.Contains(fails[0].Reason, "PNG") {
		t.Errorf("images %d, failures %+v", len(imgs), fails)
	}
}

func TestGoogleCostIsTheRate(t *testing.T) {
	m := googleModel(t)
	out := fixture(t, "out.png")
	googleServer(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, imageResponse(out, "r"))
	})
	imgs, _, err := provider(t).Generate(context.Background(), Request{Model: m, Prompt: "p", N: 1, Size: "4K"})
	if err != nil || len(imgs) != 1 {
		t.Fatalf("err %v, %d images", err, len(imgs))
	}
	if imgs[0].CostUSD != m.PerImage["4K"] || imgs[0].CostBasis != "rate" {
		t.Errorf("cost %v basis %q, want %v rate", imgs[0].CostUSD, imgs[0].CostBasis, m.PerImage["4K"])
	}
}

func TestGoogleMissingKeyIsTheRoundError(t *testing.T) {
	m := googleModel(t)
	t.Setenv(m.KeyEnv, "")
	_, _, err := provider(t).Generate(context.Background(), Request{Model: m, Prompt: "p", N: 2, Size: "1K"})
	if err == nil || !strings.Contains(err.Error(), m.KeyEnv) {
		t.Errorf("err %v, want one naming %s", err, m.KeyEnv)
	}
}

func TestGoogleAuthFailureIsTheRoundError(t *testing.T) {
	m := googleModel(t)
	googleServer(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":{"message":"API key not valid"}}`, http.StatusForbidden)
	})
	imgs, _, err := provider(t).Generate(context.Background(), Request{Model: m, Prompt: "p", N: 2, Size: "1K"})
	if err == nil || len(imgs) != 0 || !strings.Contains(err.Error(), "check "+m.KeyEnv) {
		t.Errorf("err %v, images %d", err, len(imgs))
	}
}

func TestRetryHonoursRetryAfter(t *testing.T) {
	waits := noSleep(t)
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch n.Add(1) {
		case 1:
			w.Header().Set("Retry-After", "7")
			http.Error(w, `{"error":{"message":"slow down"}}`, http.StatusTooManyRequests)
		case 2:
			http.Error(w, `{"error":{"message":"overloaded"}}`, http.StatusServiceUnavailable)
		default:
			var in map[string]any
			json.NewDecoder(r.Body).Decode(&in)
			writeJSON(w, map[string]any{"echo": in["x"]})
		}
	}))
	defer srv.Close()
	req, _ := http.NewRequest(http.MethodPost, srv.URL, strings.NewReader(`{"x":"body survives a retry"}`))
	var out struct{ Echo string }
	if err := do(context.Background(), "google", req, &out); err != nil {
		t.Fatal(err)
	}
	if out.Echo != "body survives a retry" {
		t.Errorf("echo %q", out.Echo)
	}
	if len(*waits) != 2 || (*waits)[0] != 7*time.Second || (*waits)[1] != backoff[1] {
		t.Errorf("waits %v, want [7s %v]", *waits, backoff[1])
	}
}

func TestRetryGivesUpWithTheMessage(t *testing.T) {
	waits := noSleep(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":{"message":"overloaded"}}`, http.StatusInternalServerError)
	}))
	defer srv.Close()
	req, _ := http.NewRequest(http.MethodPost, srv.URL, strings.NewReader(`{}`))
	err := do(context.Background(), "google", req, nil)
	if err == nil || !strings.HasPrefix(err.Error(), "google: ") || !strings.Contains(err.Error(), "overloaded") {
		t.Errorf("err %v", err)
	}
	if len(*waits) != len(backoff) {
		t.Errorf("waits %v, want %v", *waits, backoff)
	}
}

func TestAuthErrorNamesTheKey(t *testing.T) {
	waits := noSleep(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":{"message":"API key not valid"}}`, http.StatusUnauthorized)
	}))
	defer srv.Close()
	req, _ := http.NewRequest(http.MethodPost, srv.URL, strings.NewReader(`{}`))
	req.Header.Set("x-goog-api-key", testKey)
	err := do(context.Background(), "google", req, nil)
	if err == nil {
		t.Fatal("no error")
	}
	msg := err.Error()
	if !strings.HasPrefix(msg, "google: ") || !strings.Contains(msg, "API key not valid") || !strings.Contains(msg, "check GEMINI_API_KEY") {
		t.Errorf("err %q", msg)
	}
	if strings.Contains(msg, testKey) {
		t.Errorf("err %q carries the key", msg)
	}
	if len(*waits) != 0 {
		t.Errorf("auth error retried: %v", *waits)
	}
}

func TestBaseURLOverride(t *testing.T) {
	if got := baseURL("google", "https://def.example"); got != "https://def.example" {
		t.Errorf("default %q", got)
	}
	t.Setenv("PLATE_GENAI_GOOGLE_URL", "http://127.0.0.1:9/")
	if got := baseURL("google", "https://def.example"); got != "http://127.0.0.1:9" {
		t.Errorf("override %q", got)
	}
}

func TestGoogleUnknownSizeIsTheRoundError(t *testing.T) {
	m := googleModel(t)
	var n atomic.Int32
	googleServer(t, func(w http.ResponseWriter, r *http.Request) { n.Add(1) })
	for _, size := range []string{"", "8K"} {
		_, _, err := provider(t).Generate(context.Background(), Request{Model: m, Prompt: "p", N: 1, Size: size})
		if err == nil || !strings.Contains(err.Error(), `"`+size+`"`) || !strings.Contains(err.Error(), "1K, 2K, 4K") {
			t.Errorf("size %q: err %v, want one naming the size and 1K, 2K, 4K", size, err)
		}
	}
	if n.Load() != 0 {
		t.Errorf("%d requests sent for an unknown size", n.Load())
	}
}

func TestGoogleNoAttemptsIsTheRoundError(t *testing.T) {
	m := googleModel(t)
	for _, n := range []int{0, -1} {
		if _, _, err := provider(t).Generate(context.Background(), Request{Model: m, Prompt: "p", N: n, Size: "1K"}); err == nil {
			t.Errorf("N=%d: no error", n)
		}
	}
}

func TestRetryAfterLongerThanACallGivesUp(t *testing.T) {
	waits := noSleep(t)
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n.Add(1)
		w.Header().Set("Retry-After", strconv.Itoa(int(callTimeout/time.Second)+1))
		http.Error(w, `{"error":{"message":"quota exhausted"}}`, http.StatusTooManyRequests)
	}))
	defer srv.Close()
	req, _ := http.NewRequest(http.MethodPost, srv.URL, strings.NewReader(`{}`))
	err := do(context.Background(), "google", req, nil)
	if err == nil || !strings.Contains(err.Error(), "quota exhausted") || !strings.Contains(err.Error(), "429") {
		t.Errorf("err %v", err)
	}
	if len(*waits) != 0 || n.Load() != 1 {
		t.Errorf("waits %v after %d requests, want none after 1", *waits, n.Load())
	}
}

func TestOversizedBodyIsAnError(t *testing.T) {
	prev := maxBody
	maxBody = 16
	t.Cleanup(func() { maxBody = prev })
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"x":"` + strings.Repeat("a", 64) + `"}`))
	}))
	defer srv.Close()
	req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
	var out map[string]any
	err := do(context.Background(), "google", req, &out)
	if err == nil || !strings.HasPrefix(err.Error(), "google: ") || !strings.Contains(err.Error(), "larger than") {
		t.Errorf("err %v", err)
	}
}
