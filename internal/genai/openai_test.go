package genai

import (
	"bytes"
	"context"
	"encoding/base64"
	"io"
	"math"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/lockyc/plate/internal/pins"
)

func openaiModel(t *testing.T) pins.ImageModel {
	t.Helper()
	m, err := pins.ImageModelFor("openai", "")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(m.KeyEnv, testKey)
	return m
}

func openaiServer(t *testing.T, h http.HandlerFunc) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	t.Setenv("PLATE_GENAI_OPENAI_URL", srv.URL)
}

func openaiProvider(t *testing.T) Provider {
	t.Helper()
	p, err := For("openai")
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func openaiImages(pngs ...[]byte) map[string]any {
	data := []any{}
	for _, p := range pngs {
		data = append(data, map[string]any{"b64_json": base64.StdEncoding.EncodeToString(p)})
	}
	return map[string]any{"created": 1, "data": data,
		"usage": map[string]any{"input_tokens": 1000, "output_tokens": 2000}}
}

// parts reads a multipart request into field -> values (files by content).
func parts(t *testing.T, r *http.Request) map[string][][]byte {
	t.Helper()
	_, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil {
		t.Fatalf("content type %q: %v", r.Header.Get("Content-Type"), err)
	}
	mr := multipart.NewReader(r.Body, params["boundary"])
	out := map[string][][]byte{}
	for {
		p, err := mr.NextPart()
		if err == io.EOF {
			return out
		}
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(p)
		out[p.FormName()] = append(out[p.FormName()], b)
	}
}

func TestOpenAIEditIsMultipart(t *testing.T) {
	m := openaiModel(t)
	ref := fixture(t, "ref.png")
	out := fixture(t, "out.png")
	var path, auth, method string
	var got map[string][][]byte
	openaiServer(t, func(w http.ResponseWriter, r *http.Request) {
		path, auth, method = r.URL.Path, r.Header.Get("Authorization"), r.Method
		got = parts(t, r)
		writeJSON(w, openaiImages(out, out))
	})
	imgs, fails, err := openaiProvider(t).Generate(context.Background(), Request{
		Model: m, Prompt: "the monster, waving", N: 2, Size: "1K",
		Refs: []Ref{{Path: "a.png", PNG: ref}, {Path: "b.png", PNG: ref}},
	})
	if err != nil || len(fails) != 0 || len(imgs) != 2 {
		t.Fatalf("err %v, failures %v, images %d", err, fails, len(imgs))
	}
	if method != http.MethodPost || path != "/v1/images/edits" || auth != "Bearer "+testKey {
		t.Errorf("%s %q auth %q", method, path, auth)
	}
	want := map[string]string{"model": m.ID, "prompt": "the monster, waving", "n": "2",
		"size": "1024x1024", "quality": "high", "output_format": "png"}
	for k, v := range want {
		if len(got[k]) != 1 || string(got[k][0]) != v {
			t.Errorf("field %s = %q, want %q", k, got[k], v)
		}
	}
	if len(got["image[]"]) != 2 || !bytes.Equal(got["image[]"][0], ref) {
		t.Errorf("image[] parts: %d", len(got["image[]"]))
	}
	if !bytes.Equal(imgs[0].PNG, out) {
		t.Errorf("image bytes differ")
	}
}

func TestOpenAISizeMapping(t *testing.T) {
	m := openaiModel(t)
	out := fixture(t, "out.png")
	var size string
	var n atomic.Int32
	openaiServer(t, func(w http.ResponseWriter, r *http.Request) {
		n.Add(1)
		size = string(parts(t, r)["size"][0])
		writeJSON(w, openaiImages(out))
	})
	p := openaiProvider(t)
	for _, c := range []struct{ size, aspect, want string }{
		{"1K", "", "1024x1024"},
		{"1K", "1:1", "1024x1024"},
		{"2K", "2:3", "1024x1536"},
		{"2K", "3:2", "1536x1024"},
	} {
		_, _, err := p.Generate(context.Background(), Request{Model: m, Prompt: "p", N: 1, Size: c.size, Aspect: c.aspect})
		if err != nil || size != c.want {
			t.Errorf("%s %q: size %q err %v, want %s", c.size, c.aspect, size, err, c.want)
		}
	}
	before := n.Load()
	_, _, err := p.Generate(context.Background(), Request{Model: m, Prompt: "p", N: 1, Size: "2K", Aspect: "16:9"})
	if err == nil || !strings.Contains(err.Error(), "16:9") || !strings.Contains(err.Error(), "1024x1536") {
		t.Errorf("aspect error %v, want one naming 16:9 and the sizes offered", err)
	}
	if n.Load() != before {
		t.Errorf("requests sent for unsupported sizes")
	}
}

func TestOpenAICostFromUsage(t *testing.T) {
	m := openaiModel(t)
	out := fixture(t, "out.png")
	openaiServer(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"data": openaiImages(out, out)["data"],
			"usage": map[string]any{"input_tokens": 1000, "output_tokens": 2000}})
	})
	imgs, _, err := openaiProvider(t).Generate(context.Background(), Request{Model: m, Prompt: "p", N: 2, Size: "1K"})
	if err != nil || len(imgs) != 2 {
		t.Fatalf("err %v, %d images", err, len(imgs))
	}
	want := (1000*m.InPerMTok + 2000*m.OutPerMTok) / 1e6 / 2
	for _, im := range imgs {
		if math.Abs(im.CostUSD-want) > 1e-12 || im.CostBasis != "usage" {
			t.Errorf("image %+v, want cost %v basis usage", im, want)
		}
	}
}

func TestOpenAIModerationIsRoundError(t *testing.T) {
	m := openaiModel(t)
	var n atomic.Int32
	openaiServer(t, func(w http.ResponseWriter, r *http.Request) {
		n.Add(1)
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"error":{"code":"moderation_blocked","message":"Your request was rejected by the safety system."}}`))
	})
	imgs, fails, err := openaiProvider(t).Generate(context.Background(), Request{Model: m, Prompt: "p", N: 2, Size: "1K"})
	if err == nil || !strings.Contains(err.Error(), "rejected by the safety system") || len(imgs) != 0 || len(fails) != 0 {
		t.Errorf("err %v, images %d, failures %v", err, len(imgs), fails)
	}
	if n.Load() != 1 {
		t.Errorf("%d requests, want 1", n.Load())
	}
}

func TestOpenAIShortfallIsFailures(t *testing.T) {
	m := openaiModel(t)
	out := fixture(t, "out.png")
	openaiServer(t, func(w http.ResponseWriter, r *http.Request) { writeJSON(w, openaiImages(out)) })
	imgs, fails, err := openaiProvider(t).Generate(context.Background(), Request{Model: m, Prompt: "p", N: 3, Size: "1K"})
	if err != nil || len(imgs) != 1 || len(fails) != 2 || fails[0].Index != 1 || fails[1].Index != 2 {
		t.Errorf("err %v, %d images, failures %+v", err, len(imgs), fails)
	}
}

func TestOpenAIRetryReplaysTheBody(t *testing.T) {
	noSleep(t)
	m := openaiModel(t)
	ref := fixture(t, "ref.png")
	out := fixture(t, "out.png")
	var n atomic.Int32
	var refs int
	openaiServer(t, func(w http.ResponseWriter, r *http.Request) {
		got := parts(t, r)
		if n.Add(1) == 1 {
			http.Error(w, `{"error":{"message":"overloaded"}}`, http.StatusServiceUnavailable)
			return
		}
		refs = len(got["image[]"])
		writeJSON(w, openaiImages(out))
	})
	imgs, _, err := openaiProvider(t).Generate(context.Background(), Request{Model: m, Prompt: "p", N: 1, Size: "1K", Refs: []Ref{{PNG: ref}}})
	if err != nil || len(imgs) != 1 || refs != 1 || n.Load() != 2 {
		t.Errorf("err %v, %d images, %d refs on retry, %d requests", err, len(imgs), refs, n.Load())
	}
}

func TestOpenAIBadInputsAreRoundErrors(t *testing.T) {
	m := openaiModel(t)
	t.Setenv(m.KeyEnv, "")
	if _, _, err := openaiProvider(t).Generate(context.Background(), Request{Model: m, Prompt: "p", N: 1, Size: "1K"}); err == nil || !strings.Contains(err.Error(), m.KeyEnv) {
		t.Errorf("missing key: %v", err)
	}
}

func TestOpenAICostSumsToTheBill(t *testing.T) {
	m := openaiModel(t)
	out := fixture(t, "out.png")
	openaiServer(t, func(w http.ResponseWriter, r *http.Request) { writeJSON(w, openaiImages(out, out, out, out)) })
	// 3 asked, 4 returned (1000 in, 2000 out): the extra is billed, so it is folded in.
	imgs, _, err := openaiProvider(t).Generate(context.Background(), Request{Model: m, Prompt: "p", N: 3, Size: "1K"})
	bill := (1000*m.InPerMTok + 2000*m.OutPerMTok) / 1e6
	if err != nil || len(imgs) != 3 {
		t.Fatalf("err %v, %d images", err, len(imgs))
	}
	var sum float64
	for _, im := range imgs {
		sum += im.CostUSD
	}
	if math.Abs(sum-bill) > 1e-12 {
		t.Errorf("sum %v, want the bill %v", sum, bill)
	}
	openaiServer(t, func(w http.ResponseWriter, r *http.Request) { writeJSON(w, openaiImages(out)) })
	imgs, fails, _ := openaiProvider(t).Generate(context.Background(), Request{Model: m, Prompt: "p", N: 3, Size: "1K"})
	if len(imgs) != 1 || len(fails) != 2 || math.Abs(imgs[0].CostUSD-bill) > 1e-12 {
		t.Errorf("3 asked, 1 returned: %d images, %d failures, cost %v, want %v", len(imgs), len(fails), imgs[0].CostUSD, bill)
	}
}

func TestOpenAIBadImageDataIsAFailure(t *testing.T) {
	m := openaiModel(t)
	out := fixture(t, "out.png")
	openaiServer(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"data": []any{
			map[string]any{"b64_json": "!!not base64!!"},
			map[string]any{"b64_json": base64.StdEncoding.EncodeToString([]byte("\xff\xd8\xff jpeg"))},
			map[string]any{"b64_json": base64.StdEncoding.EncodeToString(out)},
		}, "usage": map[string]any{"input_tokens": 1, "output_tokens": 1}})
	})
	imgs, fails, err := openaiProvider(t).Generate(context.Background(), Request{Model: m, Prompt: "p", N: 3, Size: "1K"})
	if err != nil || len(imgs) != 1 || len(fails) != 2 || fails[0].Index != 0 || fails[1].Index != 1 ||
		!strings.Contains(fails[0].Reason, "base64") || !strings.Contains(fails[1].Reason, "PNG") {
		t.Errorf("err %v, %d images, failures %+v", err, len(imgs), fails)
	}
}

func TestOpenAINoUsableImageSplitsTheBillOverFailures(t *testing.T) {
	m := openaiModel(t)
	openaiServer(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"data": []any{}, "usage": map[string]any{"input_tokens": 1000000, "output_tokens": 0}})
	})
	imgs, fails, err := openaiProvider(t).Generate(context.Background(), Request{Model: m, Prompt: "p", N: 2, Size: "1K"})
	if err != nil || len(imgs) != 0 || len(fails) != 2 {
		t.Fatalf("err %v, %d images, failures %+v", err, len(imgs), fails)
	}
	var sum float64
	for _, f := range fails {
		sum += f.CostUSD
	}
	if math.Abs(sum-m.InPerMTok) > 1e-12 || fails[0].CostUSD != fails[1].CostUSD {
		t.Errorf("failures %+v, want the $%v bill split evenly", fails, m.InPerMTok)
	}
}

func TestOpenAIShortfallFailuresCarryNoCost(t *testing.T) {
	m := openaiModel(t)
	out := fixture(t, "out.png")
	openaiServer(t, func(w http.ResponseWriter, r *http.Request) { writeJSON(w, openaiImages(out)) })
	_, fails, _ := openaiProvider(t).Generate(context.Background(), Request{Model: m, Prompt: "p", N: 2, Size: "1K"})
	if len(fails) != 1 || fails[0].CostUSD != 0 {
		t.Errorf("failures %+v: the bill is on the image returned", fails)
	}
}
