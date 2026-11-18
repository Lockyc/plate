package genai

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
)

// bubbleServer serves h on the in-memory network inside a synctest bubble,
// points every provider at it, and sends package calls through its client,
// so deadlines and waits run on the bubble's fake clock.
func bubbleServer(t *testing.T, h http.HandlerFunc) {
	t.Helper()
	srv := httptest.NewTestServer(t, h)
	prev := client
	client = &http.Client{Transport: srv.Client().Transport, CheckRedirect: checkRedirect}
	t.Cleanup(func() { client = prev })
	for _, p := range []string{"OPENROUTER", "GOOGLE", "BFL", "OPENAI"} {
		t.Setenv("PLATE_GENAI_"+p+"_URL", srv.URL)
	}
}

// hang holds a request until its client gives up.
func hang(r *http.Request) { <-r.Context().Done() }

// The phase a deadline lands in decides Unreported: after the request is
// written and before the answer starts, or mid-body, the attempt may have
// been charged. On the fake clock each deadline lands exactly where it is
// set, whatever the machine's load.
func TestOpenRouterDeadlinePhases(t *testing.T) {
	cases := []struct {
		name    string
		h       http.HandlerFunc
		timeout time.Duration // 0: only callTimeout bounds the call
	}{
		{"timed out after sending", func(w http.ResponseWriter, r *http.Request) {
			io.Copy(io.Discard, r.Body)
			hang(r)
		}, 200 * time.Millisecond},
		{"body cut off", func(w http.ResponseWriter, r *http.Request) {
			io.Copy(io.Discard, r.Body)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{"data":[{"b64_json":"`))
			w.(http.Flusher).Flush()
			hang(r)
		}, 300 * time.Millisecond},
		{"the call's own timeout after sending", func(w http.ResponseWriter, r *http.Request) {
			io.Copy(io.Discard, r.Body)
			hang(r)
		}, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				m := openrouterModel(t, "")
				bubbleServer(t, c.h)
				ctx := context.Background()
				if c.timeout > 0 {
					var cancel context.CancelFunc
					ctx, cancel = context.WithTimeout(ctx, c.timeout)
					defer cancel()
				}
				start := time.Now()
				_, fails, err := openrouterProvider(t).Generate(ctx, Request{Model: m, Prompt: "p", N: 1, Size: "1K"})
				if err != nil || len(fails) != 1 || !fails[0].Unreported {
					t.Errorf("err %v, failures %+v; want one Unreported", err, fails)
				}
				want := c.timeout
				if want == 0 {
					want = callTimeout
				}
				if d := time.Since(start); d != want {
					t.Errorf("gave up after %v, want %v", d, want)
				}
			})
		})
	}
}

// A Retry-After of exactly callTimeout is waited out on the real sleep, and
// the retry goes; one second more gives up at once.
func TestRetryAfterAtTheCallTimeout(t *testing.T) {
	for _, extra := range []int{0, 1} {
		t.Run(fmt.Sprintf("callTimeout+%ds", extra), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				var n atomic.Int32
				bubbleServer(t, func(w http.ResponseWriter, r *http.Request) {
					if n.Add(1) == 1 {
						w.Header().Set("Retry-After", strconv.Itoa(int(callTimeout/time.Second)+extra))
						http.Error(w, `{"error":{"message":"quota exhausted"}}`, http.StatusTooManyRequests)
						return
					}
					writeJSON(w, map[string]any{})
				})
				req, _ := http.NewRequest(http.MethodPost, "http://example.com/", strings.NewReader(`{}`))
				start := time.Now()
				err := do(context.Background(), "google", req, nil)
				d := time.Since(start)
				if extra == 0 && (err != nil || n.Load() != 2 || d != callTimeout) {
					t.Errorf("err %v after %d requests and %v; want the retry to succeed after %v", err, n.Load(), d, callTimeout)
				}
				if extra == 1 && (err == nil || !strings.Contains(err.Error(), "429") || n.Load() != 1 || d != 0) {
					t.Errorf("err %v after %d requests and %v; want the 429 at once", err, n.Load(), d)
				}
			})
		})
	}
}

// A job still generating when the round's deadline passes is a Failure
// naming its last status, polled on the real poll wait.
func TestBFLContextDeadlineWhilePolling(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		m := bflModel(t)
		bubbleServer(t, func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodPost {
				fmt.Fprint(w, `{"id":"j"}`)
				return
			}
			fmt.Fprint(w, `{"status":"Generating"}`)
		})
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()
		imgs, fails, err := (bfl{}).Generate(ctx, Request{Model: m, Prompt: "p", N: 1, Size: "1K"})
		if err != nil || len(imgs) != 0 || len(fails) != 1 || !strings.Contains(fails[0].Reason, "Generating") {
			t.Fatalf("imgs=%d fails=%v err=%v", len(imgs), fails, err)
		}
	})
}
