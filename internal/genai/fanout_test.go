package genai

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// One attempt refused on the key beside paid ones: the paid images are kept
// and the refusal is that attempt's Failure, never the round's error.
func TestGoogleOneAuthRefusalKeepsThePaidImages(t *testing.T) {
	m := googleModel(t)
	out := fixture(t, "out.png")
	var n atomic.Int32
	googleServer(t, func(w http.ResponseWriter, r *http.Request) {
		if n.Add(1) == 1 {
			http.Error(w, `{"error":{"message":"API key not valid"}}`, http.StatusForbidden)
			return
		}
		writeJSON(w, imageResponse(out, "resp"))
	})
	imgs, fails, err := provider(t).Generate(context.Background(), Request{Model: m, Prompt: "p", N: 3, Size: "1K"})
	if err != nil || len(imgs) != 2 || len(fails) != 1 || !strings.Contains(fails[0].Reason, "API key not valid") {
		t.Errorf("err %v, %d images, failures %+v; want 2 images and the refusal as a failure", err, len(imgs), fails)
	}
}

func TestBFLOneAuthRefusalKeepsThePaidImages(t *testing.T) {
	m := bflModel(t)
	noSleep(t)
	out := fixture(t, "out.png")
	var srv *httptest.Server
	var submits atomic.Int32
	srv = bflServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost:
			i := submits.Add(1)
			if i == 1 {
				w.WriteHeader(http.StatusForbidden)
				fmt.Fprint(w, `{"detail":"Not authenticated"}`)
				return
			}
			fmt.Fprintf(w, `{"id":"j%d"}`, i)
		case r.URL.Path == "/v1/get_result":
			fmt.Fprintf(w, `{"status":"Ready","result":{"sample":"%s/s.png"}}`, srv.URL)
		default:
			w.Write(out)
		}
	}))
	imgs, fails, err := (bfl{}).Generate(context.Background(), Request{Model: m, Prompt: "p", N: 3, Size: "1K"})
	if err != nil || len(imgs) != 2 || len(fails) != 1 {
		t.Errorf("err %v, %d images, failures %+v; want 2 images and the refusal as a failure", err, len(imgs), fails)
	}
}

// OpenRouter answers 403 for a prompt its moderation flagged, never for a bad
// key (that is 401): each such attempt is a Failure carrying OpenRouter's
// message, and nothing points at the key.
func TestOpenRouterForbiddenIsModeration(t *testing.T) {
	m := openrouterModel(t, "")
	out := fixture(t, "out.png")
	var n atomic.Int32
	openrouterServer(t, func(w http.ResponseWriter, r *http.Request) {
		if n.Add(1) == 1 {
			writeJSON(w, openrouterImages(0.03, out))
			return
		}
		w.WriteHeader(http.StatusForbidden)
		w.Write([]byte(`{"error":{"message":"Your chosen model requires moderation and your input was flagged","code":403}}`))
	})
	imgs, fails, err := openrouterProvider(t).Generate(context.Background(), Request{Model: m, Prompt: "p", N: 3, Size: "1K"})
	if err != nil || len(imgs) != 1 || len(fails) != 2 {
		t.Fatalf("err %v, %d images, failures %+v", err, len(imgs), fails)
	}
	for _, f := range fails {
		if !strings.Contains(f.Reason, "input was flagged") || strings.Contains(f.Reason, m.KeyEnv) {
			t.Errorf("failure %q: want OpenRouter's message and no key hint", f.Reason)
		}
	}
	// Every attempt flagged is still a set of failures, not a key error.
	openrouterServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		w.Write([]byte(`{"error":{"message":"input was flagged"}}`))
	})
	_, fails, err = openrouterProvider(t).Generate(context.Background(), Request{Model: m, Prompt: "p", N: 2, Size: "1K"})
	if err != nil || len(fails) != 2 {
		t.Errorf("all flagged: err %v, failures %+v", err, fails)
	}
}

func TestFanOutAuthRule(t *testing.T) {
	auth := &statusError{provider: "google", status: http.StatusUnauthorized, msg: "bad key"}
	other := fmt.Errorf("google: connection reset")
	img := []Image{{PNG: []byte("x")}}
	for _, c := range []struct {
		name        string
		results     []func() ([]Image, Failure, error)
		roundErr    bool
		imgs, fails int
	}{
		{"all auth", []func() ([]Image, Failure, error){
			func() ([]Image, Failure, error) { return nil, Failure{}, auth },
			func() ([]Image, Failure, error) { return nil, Failure{}, auth },
		}, true, 0, 0},
		{"auth beside an image", []func() ([]Image, Failure, error){
			func() ([]Image, Failure, error) { return nil, Failure{}, auth },
			func() ([]Image, Failure, error) { return img, Failure{}, nil },
		}, false, 1, 1},
		{"auth beside a charged failure", []func() ([]Image, Failure, error){
			func() ([]Image, Failure, error) { return nil, Failure{}, auth },
			func() ([]Image, Failure, error) { return nil, Failure{Reason: "r", CostUSD: 0.1}, nil },
		}, false, 0, 2},
		{"auth beside another error", []func() ([]Image, Failure, error){
			func() ([]Image, Failure, error) { return nil, Failure{}, auth },
			func() ([]Image, Failure, error) { return nil, Failure{}, other },
		}, false, 0, 2},
	} {
		imgs, fails, err := fanOut(len(c.results), func(i int) ([]Image, Failure, error) {
			return c.results[i]()
		})
		if (err != nil) != c.roundErr || len(imgs) != c.imgs || len(fails) != c.fails {
			t.Errorf("%s: err %v, %d images, %d failures", c.name, err, len(imgs), len(fails))
		}
	}
}
