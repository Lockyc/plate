package genai

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// A request sent with no cost in the answer may have been charged: its
// Failure is marked Unreported. One that was never sent is not.
func TestOpenRouterUnreportedFailures(t *testing.T) {
	m := openrouterModel(t, "")
	p := openrouterProvider(t)

	openrouterServer(t, func(w http.ResponseWriter, r *http.Request) { writeJSON(w, map[string]any{"data": []any{}}) })
	_, fails, err := p.Generate(context.Background(), Request{Model: m, Prompt: "p", N: 1, Size: "1K"})
	if err != nil || len(fails) != 1 || !fails[0].Unreported {
		t.Errorf("200, no data, no cost: err %v, failures %+v; want one Unreported", err, fails)
	}

	openrouterServer(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"data": []any{}, "usage": map[string]any{"cost": 0}})
	})
	_, fails, _ = p.Generate(context.Background(), Request{Model: m, Prompt: "p", N: 1, Size: "1K"})
	if len(fails) != 1 || fails[0].Unreported {
		t.Errorf("a reported $0: failures %+v; want one, not Unreported", fails)
	}

	openrouterServer(t, func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		select {
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	_, fails, err = p.Generate(ctx, Request{Model: m, Prompt: "p", N: 1, Size: "1K"})
	if err != nil || len(fails) != 1 || !fails[0].Unreported {
		t.Errorf("timed out after sending: err %v, failures %+v; want one Unreported", err, fails)
	}

	srv := httptest.NewServer(http.NotFoundHandler())
	srv.Close()
	t.Setenv("PLATE_GENAI_OPENROUTER_URL", srv.URL)
	_, fails, err = p.Generate(context.Background(), Request{Model: m, Prompt: "p", N: 1, Size: "1K"})
	if err != nil || len(fails) != 1 || fails[0].Unreported {
		t.Errorf("never sent: err %v, failures %+v; want one, not Unreported", err, fails)
	}
}

// A poll that fails after the job was submitted may leave a billed job: it
// carries the job's cost, as giving up does.
func TestBFLPollErrorAfterSubmitIsCharged(t *testing.T) {
	m := bflModel(t)
	noSleep(t)
	bflServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			fmt.Fprint(w, `{"id":"j"}`)
			return
		}
		http.Error(w, `{"detail":"bad request"}`, http.StatusBadRequest)
	}))
	_, fails, err := (bfl{}).Generate(context.Background(), Request{Model: m, Prompt: "p", N: 1, Size: "1K"})
	if err != nil || len(fails) != 1 || fails[0].CostUSD != m.FirstMP {
		t.Errorf("err %v, failures %+v; want one charged %v", err, fails, m.FirstMP)
	}
}
