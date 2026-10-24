package genai

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lockyc/plate/internal/pins"
)

func openrouterModel(t *testing.T, id string) pins.ImageModel {
	t.Helper()
	m, err := pins.ImageModelFor("openrouter", id)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(m.KeyEnv, testKey)
	return m
}

func openrouterServer(t *testing.T, h http.HandlerFunc) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	t.Setenv("PLATE_GENAI_OPENROUTER_URL", srv.URL)
}

func openrouterProvider(t *testing.T) Provider {
	t.Helper()
	p, err := For("openrouter")
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// openrouterImages is a /api/v1/images answer carrying pngs, billed cost.
func openrouterImages(cost float64, pngs ...[]byte) map[string]any {
	data := []any{}
	for _, p := range pngs {
		data = append(data, map[string]any{"b64_json": base64.StdEncoding.EncodeToString(p), "media_type": "image/png"})
	}
	return map[string]any{"created": 1, "data": data,
		"usage": map[string]any{"prompt_tokens": 10, "completion_tokens": 20, "total_tokens": 30, "cost": cost}}
}

func TestOpenRouterSendsRefsAndSize(t *testing.T) {
	m := openrouterModel(t, "")
	ref := fixture(t, "ref.png")
	out := fixture(t, "out.png")
	var path, auth, method string
	var body map[string]any
	openrouterServer(t, func(w http.ResponseWriter, r *http.Request) {
		path, auth, method = r.URL.Path, r.Header.Get("Authorization"), r.Method
		body = nil
		json.NewDecoder(r.Body).Decode(&body)
		writeJSON(w, openrouterImages(0.05, out))
	})
	p := openrouterProvider(t)
	imgs, fails, err := p.Generate(context.Background(), Request{
		Model: m, Prompt: "the monster, waving", N: 1, Size: "2K", Aspect: "3:2",
		Refs: []Ref{{Path: "a.png", PNG: ref}, {Path: "b.png", PNG: ref}},
	})
	if err != nil || len(fails) != 0 || len(imgs) != 1 {
		t.Fatalf("err %v, failures %v, images %d", err, fails, len(imgs))
	}
	if method != http.MethodPost || path != "/api/v1/images" || auth != "Bearer "+testKey {
		t.Errorf("%s %q auth %q", method, path, auth)
	}
	for k, v := range map[string]string{"model": m.ID, "prompt": "the monster, waving", "resolution": "2K", "aspect_ratio": "3:2"} {
		if body[k] != v {
			t.Errorf("%s = %v, want %q", k, body[k], v)
		}
	}
	if _, ok := body["n"]; ok {
		t.Errorf("n sent: %v", body["n"])
	}
	refs, _ := body["input_references"].([]any)
	if len(refs) != 2 {
		t.Fatalf("input_references: %v", body["input_references"])
	}
	want := "data:image/png;base64," + base64.StdEncoding.EncodeToString(ref)
	for i, r := range refs {
		e, _ := r.(map[string]any)
		u, _ := e["image_url"].(map[string]any)
		if e["type"] != "image_url" || u["url"] != want {
			t.Errorf("ref %d = %v", i, r)
		}
	}
	if _, _, err := p.Generate(context.Background(), Request{Model: m, Prompt: "p", N: 1, Size: "1K"}); err != nil {
		t.Fatal(err)
	}
	if _, ok := body["aspect_ratio"]; ok {
		t.Errorf("aspect_ratio sent with no Aspect: %v", body["aspect_ratio"])
	}
}

func TestOpenRouterModelParams(t *testing.T) {
	m := openrouterModel(t, "black-forest-labs/flux.2-pro")
	out := fixture(t, "out.png")
	var body map[string]any
	openrouterServer(t, func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&body)
		writeJSON(w, openrouterImages(0.03, out))
	})
	if _, _, err := openrouterProvider(t).Generate(context.Background(), Request{Model: m, Prompt: "p", N: 1, Size: "1K"}); err != nil {
		t.Fatal(err)
	}
	if body["output_format"] != "png" {
		t.Errorf("body %v, want output_format png", body)
	}
}

// A pixels-form model gets an explicit size: the tier's megapixels (capped at
// the model's), at --aspect or else the first reference's aspect, each side
// rounded down to a multiple of 16. No resolution or aspect_ratio goes with
// it: OpenRouter refuses an aspect_ratio that disagrees with the size.
func TestOpenRouterPixelsSize(t *testing.T) {
	ref := pngOf(520, 620)
	other := pngOf(900, 300)
	for _, c := range []struct {
		model, size, aspect string
		refs                [][]byte
		want                string
	}{
		// sqrt(4e6*520/620) = 1831.6 -> 1824; 1831.6*620/520 = 2183.9 -> 2176.
		{"black-forest-labs/flux.2-pro", "2K", "", [][]byte{ref, other}, "1824x2176"},
		// sqrt(1e6*1.5) = 1224.7 -> 1216; 816.5 -> 816.
		{"black-forest-labs/flux.2-max", "1K", "3:2", [][]byte{ref}, "1216x816"},
		{"black-forest-labs/flux.2-pro", "2K", "", nil, "2000x2000"},
		{"openai/gpt-image-2", "2K", "", [][]byte{ref}, "1824x2176"},
		// 4K is capped at GPT Image's 8,294,400 pixels.
		{"openai/gpt-image-2", "4K", "16:9", nil, "3840x2160"},
		// ... and at its 3840 pixel edge: 3840 x 1645.7 -> 1632.
		{"openai/gpt-image-2", "4K", "21:9", nil, "3840x1632"},
	} {
		m := openrouterModel(t, c.model)
		var body map[string]any
		openrouterServer(t, func(w http.ResponseWriter, r *http.Request) {
			json.NewDecoder(r.Body).Decode(&body)
			writeJSON(w, openrouterImages(0.03, fixture(t, "out.png")))
		})
		var refs []Ref
		for _, p := range c.refs {
			refs = append(refs, Ref{Path: "r.png", PNG: p})
		}
		_, _, err := openrouterProvider(t).Generate(context.Background(), Request{Model: m, Prompt: "p", N: 1, Size: c.size, Aspect: c.aspect, Refs: refs})
		if err != nil {
			t.Fatalf("%s %s %q: %v", c.model, c.size, c.aspect, err)
		}
		if body["size"] != c.want {
			t.Errorf("%s %s %q: size %v, want %s", c.model, c.size, c.aspect, body["size"], c.want)
		}
		for _, k := range []string{"resolution", "aspect_ratio"} {
			if _, ok := body[k]; ok {
				t.Errorf("%s %s %q: %s sent: %v", c.model, c.size, c.aspect, k, body[k])
			}
		}
	}
}

func TestOpenRouterPixelsRefused(t *testing.T) {
	var n atomic.Int32
	openrouterServer(t, func(w http.ResponseWriter, r *http.Request) { n.Add(1) })
	for _, c := range []struct {
		model, size, aspect string
		refs                []Ref
		want                string
	}{
		// FLUX.2 makes at most 4 MP, so 4K would be 2K at a 4K price.
		{"black-forest-labs/flux.2-pro", "4K", "", nil, "1K, 2K"},
		{"black-forest-labs/flux.2-pro", "2K", "", []Ref{{Path: "r.jpg", PNG: []byte("\xff\xd8\xff")}}, "reference 1"},
		{"black-forest-labs/flux.2-pro", "2K", "wide", nil, "W:H"},
	} {
		m := openrouterModel(t, c.model)
		_, _, err := openrouterProvider(t).Generate(context.Background(), Request{Model: m, Prompt: "p", N: 1, Size: c.size, Aspect: c.aspect, Refs: c.refs})
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s %s %q: err %v, want one naming %q", c.model, c.size, c.aspect, err, c.want)
		}
	}
	if n.Load() != 0 {
		t.Errorf("%d requests made; want none", n.Load())
	}
}

func TestOpenRouterSizeChecked(t *testing.T) {
	m := openrouterModel(t, "google/gemini-3-pro-image")
	var n atomic.Int32
	openrouterServer(t, func(w http.ResponseWriter, r *http.Request) { n.Add(1) })
	_, _, err := openrouterProvider(t).Generate(context.Background(), Request{Model: m, Prompt: "p", N: 1, Size: "512"})
	if err == nil || !strings.Contains(err.Error(), "1K, 2K, 4K") || n.Load() != 0 {
		t.Errorf("err %v, %d requests; want an error naming the sizes and no request", err, n.Load())
	}
}

func TestOpenRouterCostIsReported(t *testing.T) {
	m := openrouterModel(t, "")
	out := fixture(t, "out.png")
	openrouterServer(t, func(w http.ResponseWriter, r *http.Request) { writeJSON(w, openrouterImages(0.0625, out)) })
	p := openrouterProvider(t)
	imgs, _, err := p.Generate(context.Background(), Request{Model: m, Prompt: "p", N: 2, Size: "1K"})
	if err != nil || len(imgs) != 2 {
		t.Fatalf("err %v, %d images", err, len(imgs))
	}
	for _, im := range imgs {
		if im.CostUSD != 0.0625 || im.CostBasis != "reported" {
			t.Errorf("image cost %v basis %q, want 0.0625 reported", im.CostUSD, im.CostBasis)
		}
	}
	// One request answered with two images: both are kept, splitting the bill.
	openrouterServer(t, func(w http.ResponseWriter, r *http.Request) { writeJSON(w, openrouterImages(0.10, out, out)) })
	imgs, _, err = p.Generate(context.Background(), Request{Model: m, Prompt: "p", N: 1, Size: "1K"})
	if err != nil || len(imgs) != 2 || math.Abs(imgs[0].CostUSD-0.05) > 1e-12 || math.Abs(imgs[1].CostUSD-0.05) > 1e-12 {
		t.Errorf("err %v, images %+v, want two at half the bill each", err, imgs)
	}
	// Two images, one unusable: the usable one carries the whole bill.
	openrouterServer(t, func(w http.ResponseWriter, r *http.Request) {
		v := openrouterImages(0.10, out, out)
		v["data"].([]any)[1].(map[string]any)["b64_json"] = "!!not base64!!"
		writeJSON(w, v)
	})
	imgs, fails, err := p.Generate(context.Background(), Request{Model: m, Prompt: "p", N: 1, Size: "1K"})
	if err != nil || len(fails) != 0 || len(imgs) != 1 || math.Abs(imgs[0].CostUSD-0.10) > 1e-12 {
		t.Errorf("err %v, failures %+v, images %+v, want one image at the whole bill", err, fails, imgs)
	}
	// No usage.cost: the paid image is kept, marked unreported, never dropped.
	openrouterServer(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"data": openrouterImages(0, out)["data"], "usage": map[string]any{"total_tokens": 30}})
	})
	imgs, fails, err = p.Generate(context.Background(), Request{Model: m, Prompt: "p", N: 1, Size: "1K"})
	if err != nil || len(fails) != 0 || len(imgs) != 1 || imgs[0].CostUSD != 0 || imgs[0].CostBasis != "unreported" {
		t.Errorf("err %v, failures %v, images %+v; want one unreported image", err, fails, imgs)
	}
}

func TestOpenRouterNAttempts(t *testing.T) {
	m := openrouterModel(t, "")
	out := fixture(t, "out.png")
	const n = 4
	var mu sync.Mutex
	arrived := 0
	all := make(chan struct{})
	openrouterServer(t, func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		if _, ok := body["n"]; ok {
			t.Errorf("n sent: %v", body["n"])
		}
		mu.Lock()
		arrived++
		if arrived == n {
			close(all)
		}
		mu.Unlock()
		// Every request is in flight at once, or this waits out the timer.
		select {
		case <-all:
		case <-time.After(5 * time.Second):
			http.Error(w, `{"error":{"message":"not concurrent"}}`, http.StatusBadRequest)
			return
		}
		writeJSON(w, openrouterImages(0.01, out))
	})
	imgs, fails, err := openrouterProvider(t).Generate(context.Background(), Request{Model: m, Prompt: "p", N: n, Size: "1K"})
	if err != nil || len(fails) != 0 || len(imgs) != n || arrived != n {
		t.Errorf("err %v, failures %v, %d images, %d requests", err, fails, len(imgs), arrived)
	}
}

func TestOpenRouterNoImageIsAFailure(t *testing.T) {
	m := openrouterModel(t, "")
	openrouterServer(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"data": []any{}, "error": map[string]any{"message": "content policy"}, "usage": map[string]any{"cost": 0}})
	})
	imgs, fails, err := openrouterProvider(t).Generate(context.Background(), Request{Model: m, Prompt: "p", N: 2, Size: "1K"})
	if err != nil || len(imgs) != 0 || len(fails) != 2 || !strings.Contains(fails[0].Reason, "content policy") || fails[0].CostUSD != 0 {
		t.Errorf("err %v, %d images, failures %+v", err, len(imgs), fails)
	}
	openrouterServer(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"data": []any{}, "usage": map[string]any{"cost": 0.04}})
	})
	_, fails, _ = openrouterProvider(t).Generate(context.Background(), Request{Model: m, Prompt: "p", N: 1, Size: "1K"})
	if len(fails) != 1 || !strings.Contains(fails[0].Reason, "no image") || fails[0].CostUSD != 0.04 {
		t.Errorf("failures %+v, want one saying no image, costing the $0.04 billed", fails)
	}
}

func TestOpenRouterNonPNGIsAFailure(t *testing.T) {
	m := openrouterModel(t, "")
	openrouterServer(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"data": []any{map[string]any{
			"b64_json": base64.StdEncoding.EncodeToString([]byte("\xff\xd8\xff jpeg")), "media_type": "image/jpeg",
		}}, "usage": map[string]any{"cost": 0.03}})
	})
	imgs, fails, err := openrouterProvider(t).Generate(context.Background(), Request{Model: m, Prompt: "p", N: 1, Size: "1K"})
	if err != nil || len(imgs) != 0 || len(fails) != 1 || !strings.Contains(fails[0].Reason, "PNG") || !strings.Contains(fails[0].Reason, "image/jpeg") || fails[0].CostUSD != 0.03 {
		t.Errorf("err %v, %d images, failures %+v", err, len(imgs), fails)
	}
}

func TestOpenRouterAuthIsRoundError(t *testing.T) {
	m := openrouterModel(t, "")
	openrouterServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"error":{"message":"No auth credentials found","code":401}}`))
	})
	_, _, err := openrouterProvider(t).Generate(context.Background(), Request{Model: m, Prompt: "p", N: 2, Size: "1K"})
	if err == nil || !strings.Contains(err.Error(), "OPENROUTER_API_KEY") || strings.Contains(err.Error(), testKey) {
		t.Errorf("err %v", err)
	}
}
