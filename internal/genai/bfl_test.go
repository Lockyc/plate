package genai

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
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

func bflModel(t *testing.T) pins.ImageModel {
	t.Helper()
	m, err := pins.ImageModelFor("bfl", "")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(m.KeyEnv, testKey)
	return m
}

func bflServer(t *testing.T, h http.Handler) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	t.Setenv("PLATE_GENAI_BFL_URL", srv.URL)
	return srv
}

func pngOf(w, h int) []byte {
	var b bytes.Buffer
	_ = png.Encode(&b, image.NewGray(image.Rect(0, 0, w, h)))
	return b.Bytes()
}

func TestBFLSubmitAndPoll(t *testing.T) {
	m := bflModel(t)
	noSleep(t)
	out := fixture(t, "out.png")
	ref := fixture(t, "ref.png")
	var polls, submits int32
	var srv *httptest.Server
	srv = bflServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/"+m.ID:
			atomic.AddInt32(&submits, 1)
			if r.Header.Get("x-key") != testKey {
				t.Errorf("x-key = %q", r.Header.Get("x-key"))
			}
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			if body["prompt"] != "a monster" || body["output_format"] != "png" {
				t.Errorf("body = %v", body)
			}
			for i, k := range []string{"input_image", "input_image_2"} {
				s, _ := body[k].(string)
				if got, err := base64.StdEncoding.DecodeString(s); err != nil || !bytes.Equal(got, ref) {
					t.Errorf("%s (ref %d) is not the ref in base64", k, i+1)
				}
			}
			if _, ok := body["input_image_3"]; ok {
				t.Error("input_image_3 sent for two refs")
			}
			if body["width"] != float64(992) || body["height"] != float64(992) {
				t.Errorf("size = %v x %v", body["width"], body["height"])
			}
			// polling_url is ignored: the poll goes to the base host.
			fmt.Fprintf(w, `{"id":"job-%d","polling_url":"http://127.0.0.1:1/never"}`, atomic.LoadInt32(&submits))
		case r.URL.Path == "/v1/get_result":
			if r.URL.Query().Get("id") != "job-1" {
				t.Errorf("poll id = %q", r.URL.Query().Get("id"))
			}
			if r.Header.Get("x-key") != testKey {
				t.Errorf("poll x-key = %q", r.Header.Get("x-key"))
			}
			if atomic.AddInt32(&polls, 1) <= 2 {
				fmt.Fprint(w, `{"status":"Pending"}`)
				return
			}
			fmt.Fprintf(w, `{"status":"Ready","result":{"sample":"%s/sample.png"}}`, srv.URL)
		case r.URL.Path == "/sample.png":
			if r.Header.Get("x-key") != "" {
				t.Error("key sent to the sample host")
			}
			w.Write(out)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
	}))
	imgs, fails, err := (bfl{}).Generate(context.Background(), Request{
		Model: m, Prompt: "a monster", Refs: []Ref{{PNG: ref}, {PNG: ref}}, N: 1, Size: "1K", Aspect: "1:1"})
	if err != nil || len(fails) != 0 || len(imgs) != 1 {
		t.Fatalf("imgs=%d fails=%v err=%v", len(imgs), fails, err)
	}
	if !bytes.Equal(imgs[0].PNG, out) || imgs[0].ProviderID != "job-1" || imgs[0].CostBasis != "rate" {
		t.Errorf("image = %+v", imgs[0])
	}
}

func TestBFLPollGivesUp(t *testing.T) {
	m := bflModel(t)
	bflServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			fmt.Fprint(w, `{"id":"j"}`)
			return
		}
		fmt.Fprint(w, `{"status":"Pending"}`)
	}))
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	// Polls come back at once; the context's deadline ends the wait.
	prev := sleep
	sleep = func(c context.Context, d time.Duration) error {
		time.Sleep(10 * time.Millisecond)
		return c.Err()
	}
	t.Cleanup(func() { sleep = prev })
	imgs, fails, err := (bfl{}).Generate(ctx, Request{Model: m, Prompt: "p", N: 1, Size: "1K"})
	if err != nil || len(imgs) != 0 || len(fails) != 1 {
		t.Fatalf("imgs=%d fails=%v err=%v", len(imgs), fails, err)
	}
	if !strings.Contains(fails[0].Reason, "Pending") || fails[0].CostUSD != m.FirstMP {
		t.Errorf("failure %+v, want the last status and CostUSD %v", fails[0], m.FirstMP)
	}
}

func TestBFLContextDeadlineWhilePolling(t *testing.T) {
	m := bflModel(t)
	bflServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			fmt.Fprint(w, `{"id":"j"}`)
			return
		}
		fmt.Fprint(w, `{"status":"Generating"}`)
	}))
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	imgs, fails, err := (bfl{}).Generate(ctx, Request{Model: m, Prompt: "p", N: 1, Size: "1K"})
	if err != nil || len(imgs) != 0 || len(fails) != 1 || !strings.Contains(fails[0].Reason, "Generating") {
		t.Fatalf("imgs=%d fails=%v err=%v", len(imgs), fails, err)
	}
}

func TestBFLReadyButUnusableIsBilled(t *testing.T) {
	m := bflModel(t)
	noSleep(t)
	var srv *httptest.Server
	srv = bflServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost:
			fmt.Fprint(w, `{"id":"j"}`)
		case r.URL.Path == "/v1/get_result":
			fmt.Fprintf(w, `{"status":"Ready","result":{"sample":"%s/gone.png"}}`, srv.URL)
		default:
			http.Error(w, "expired", http.StatusNotFound)
		}
	}))
	imgs, fails, err := (bfl{}).Generate(context.Background(), Request{Model: m, Prompt: "p", N: 1, Size: "1K"})
	if err != nil || len(imgs) != 0 || len(fails) != 1 {
		t.Fatalf("imgs=%d fails=%v err=%v", len(imgs), fails, err)
	}
	if fails[0].CostUSD != m.FirstMP || strings.Contains(fails[0].Reason, "billed") {
		t.Errorf("failure %+v, want CostUSD %v and no billing note in the reason", fails[0], m.FirstMP)
	}
}

func TestBFLTaskNotFoundGrace(t *testing.T) {
	m := bflModel(t)
	noSleep(t)
	out := fixture(t, "out.png")
	var srv *httptest.Server
	var polls int32
	srv = bflServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost:
			fmt.Fprint(w, `{"id":"j"}`)
		case r.URL.Path == "/v1/get_result":
			if atomic.AddInt32(&polls, 1) <= 2 {
				fmt.Fprint(w, `{"status":"Task not found"}`)
				return
			}
			fmt.Fprintf(w, `{"status":"Ready","result":{"sample":"%s/s.png"}}`, srv.URL)
		default:
			w.Write(out)
		}
	}))
	imgs, fails, err := (bfl{}).Generate(context.Background(), Request{Model: m, Prompt: "p", N: 1, Size: "1K"})
	if err != nil || len(imgs) != 1 || len(fails) != 0 {
		t.Fatalf("two not-found polls should be tolerated: imgs=%d fails=%v err=%v", len(imgs), fails, err)
	}

	// A third in a row is final.
	srv2 := bflServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			fmt.Fprint(w, `{"id":"j"}`)
			return
		}
		fmt.Fprint(w, `{"status":"Task not found"}`)
	}))
	_ = srv2
	imgs, fails, err = (bfl{}).Generate(context.Background(), Request{Model: m, Prompt: "p", N: 1, Size: "1K"})
	if err != nil || len(imgs) != 0 || len(fails) != 1 || !strings.Contains(fails[0].Reason, "Task not found") {
		t.Fatalf("imgs=%d fails=%v err=%v", len(imgs), fails, err)
	}
}

func TestBFLRefusesARedirectToAnotherHost(t *testing.T) {
	m := bflModel(t)
	var other int32
	elsewhere := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&other, 1)
	}))
	defer elsewhere.Close()
	bflServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, elsewhere.URL+"/steal", http.StatusFound)
	}))
	imgs, fails, err := (bfl{}).Generate(context.Background(), Request{Model: m, Prompt: "p", N: 1, Size: "1K"})
	if err != nil || len(imgs) != 0 || len(fails) != 1 || !strings.Contains(fails[0].Reason, "refusing a redirect") {
		t.Fatalf("imgs=%d fails=%v err=%v", len(imgs), fails, err)
	}
	if other != 0 {
		t.Errorf("the other host saw %d requests", other)
	}
}

func TestBFLModeratedIsFailure(t *testing.T) {
	for _, status := range []string{"Request Moderated", "Content Moderated"} {
		t.Run(status, func(t *testing.T) {
			m := bflModel(t)
			noSleep(t)
			bflServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodPost {
					fmt.Fprint(w, `{"id":"j"}`)
					return
				}
				fmt.Fprintf(w, `{"status":%q}`, status)
			}))
			imgs, fails, err := (bfl{}).Generate(context.Background(), Request{Model: m, Prompt: "p", N: 2, Size: "1K"})
			if err != nil || len(imgs) != 0 || len(fails) != 2 {
				t.Fatalf("imgs=%d fails=%v err=%v", len(imgs), fails, err)
			}
			for i, f := range fails {
				if f.Index != i || !strings.Contains(f.Reason, status) || f.CostUSD != 0 {
					t.Errorf("fail %d = %+v", i, f)
				}
			}
		})
	}
}

func TestBFLCostFromMegapixels(t *testing.T) {
	m := bflModel(t)
	noSleep(t)
	out := fixture(t, "out.png")
	var srv *httptest.Server
	srv = bflServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost:
			fmt.Fprint(w, `{"id":"j"}`)
		case r.URL.Path == "/v1/get_result":
			fmt.Fprintf(w, `{"status":"Ready","result":{"sample":"%s/s.png"}}`, srv.URL)
		default:
			w.Write(out)
		}
	}))
	// 2K at 1:1 is 1984x1984 = 3.94 MP: 2.94 beyond the first, rounded up to 3.
	// Refs are 1.0 MP and 0.6 MP: 1.6 summed, rounded up to 2.
	refs := []Ref{{PNG: pngOf(1000, 1000)}, {PNG: pngOf(600, 1000)}}
	imgs, fails, err := (bfl{}).Generate(context.Background(), Request{
		Model: m, Prompt: "p", Refs: refs, N: 2, Size: "2K", Aspect: "1:1"})
	if err != nil || len(fails) != 0 || len(imgs) != 2 {
		t.Fatalf("imgs=%d fails=%v err=%v", len(imgs), fails, err)
	}
	want := m.FirstMP + 3*m.ExtraMP + 2*m.RefMP
	for i, img := range imgs {
		if math.Abs(img.CostUSD-want) > 1e-9 || img.CostBasis != "rate" {
			t.Errorf("image %d: cost %v basis %q, want %v rate", i, img.CostUSD, img.CostBasis, want)
		}
	}
}

func TestBFLRefusesBeforeAnyRequest(t *testing.T) {
	m := bflModel(t)
	var hits int32
	bflServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { atomic.AddInt32(&hits, 1) }))
	cases := map[string]Request{
		"bad aspect":     {Model: m, Prompt: "p", N: 1, Size: "1K", Aspect: "wide"},
		"unreadable ref": {Model: m, Prompt: "p", N: 1, Size: "1K", Refs: []Ref{{PNG: []byte("\xff\xd8\xff")}}},
	}
	for name, r := range cases {
		if _, _, err := (bfl{}).Generate(context.Background(), r); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
	if hits != 0 {
		t.Errorf("%d requests sent", hits)
	}
}

func TestBFLAuthFailsTheRound(t *testing.T) {
	m := bflModel(t)
	bflServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		fmt.Fprint(w, `{"detail":"Not authenticated"}`)
	}))
	_, _, err := (bfl{}).Generate(context.Background(), Request{Model: m, Prompt: "p", N: 2, Size: "1K"})
	if err == nil || !strings.Contains(err.Error(), "BFL_API_KEY") {
		t.Fatalf("err = %v", err)
	}
}

func TestBFLOneJobFailingKeepsTheOthers(t *testing.T) {
	m := bflModel(t)
	noSleep(t)
	out := fixture(t, "out.png")
	var srv *httptest.Server
	var mu sync.Mutex
	next := 0
	srv = bflServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost:
			mu.Lock()
			next++
			id := next
			mu.Unlock()
			fmt.Fprintf(w, `{"id":"j%d"}`, id)
		case r.URL.Path == "/v1/get_result" && r.URL.Query().Get("id") == "j1":
			fmt.Fprint(w, `{"status":"Content Moderated"}`)
		case r.URL.Path == "/v1/get_result":
			fmt.Fprintf(w, `{"status":"Ready","result":{"sample":"%s/s.png"}}`, srv.URL)
		default:
			w.Write(out)
		}
	}))
	imgs, fails, err := (bfl{}).Generate(context.Background(), Request{Model: m, Prompt: "p", N: 3, Size: "1K"})
	if err != nil || len(imgs) != 2 || len(fails) != 1 {
		t.Fatalf("imgs=%d fails=%v err=%v", len(imgs), fails, err)
	}
	if imgs[0].CostUSD != m.FirstMP {
		t.Errorf("cost = %v, want %v", imgs[0].CostUSD, m.FirstMP)
	}
}
