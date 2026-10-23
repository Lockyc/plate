package cdp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lockyc/plate/internal/engine"
)

func TestMain(m *testing.M) {
	if os.Getenv("PLATE_FAKE_CHROME") != "" {
		fakeChrome()
		return
	}
	os.Exit(m.Run())
}

// fakeChrome speaks the debugging pipe: NUL-delimited JSON on fd 3 (in) and
// fd 4 (out). It exits 0 when fd 3 reaches EOF, as Chrome does.
func fakeChrome() {
	in, out := os.NewFile(3, "in"), os.NewFile(4, "out")
	r := bufio.NewReader(in)
	send := func(v any) {
		b, _ := json.Marshal(v)
		out.Write(append(b, 0))
	}
	for {
		line, err := r.ReadBytes(0)
		if err != nil {
			return
		}
		var m struct {
			ID      int             `json:"id"`
			Method  string          `json:"method"`
			Params  json.RawMessage `json:"params"`
			Session string          `json:"sessionId"`
		}
		if json.Unmarshal(line[:len(line)-1], &m) != nil {
			continue
		}
		reply := func(result any) {
			send(map[string]any{"id": m.ID, "result": result, "sessionId": m.Session})
		}
		switch m.Method {
		case "Target.createTarget":
			reply(map[string]any{"targetId": "T1"})
		case "Target.attachToTarget":
			reply(map[string]any{"sessionId": "S1"})
		case "Echo":
			reply(m.Params)
		case "Fail":
			send(map[string]any{"id": m.ID, "error": map[string]any{"code": -32000, "message": "nope"}})
		case "Emit":
			reply(map[string]any{})
			send(map[string]any{"method": "Ev", "params": m.Params, "sessionId": m.Session})
		case "Die":
			fmt.Fprintln(os.Stderr, "fake chrome crashed")
			os.Exit(3)
		}
	}
}

func launch(t *testing.T) *Conn {
	t.Helper()
	return launchFor(t, 0)
}

// launchFor starts the fake Chrome for a session of at most timeout.
func launchFor(t *testing.T, timeout time.Duration) *Conn {
	t.Helper()
	t.Setenv("PLATE_FAKE_CHROME", "1")
	t.Setenv(engine.EnvOverride("chrome-headless-shell"), os.Args[0])
	c, err := Launch(context.Background(), nil, timeout)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

func TestCallRoundTrip(t *testing.T) {
	c := launch(t)
	var got struct{ A int }
	if err := c.Call(context.Background(), "", "Echo", map[string]int{"A": 7}, &got); err != nil {
		t.Fatal(err)
	}
	if got.A != 7 {
		t.Fatalf("got %+v", got)
	}
}

func TestCallError(t *testing.T) {
	c := launch(t)
	err := c.Call(context.Background(), "", "Fail", nil, nil)
	var pe *Error
	if !errors.As(err, &pe) {
		t.Fatalf("err = %v, want *Error", err)
	}
	if pe.Message != "nope" || pe.Code != -32000 || pe.Method != "Fail" {
		t.Fatalf("got %+v", pe)
	}
}

func TestExpectBeforeTrigger(t *testing.T) {
	c := launch(t)
	wait := c.Expect("S1", "Ev")
	if err := c.Call(context.Background(), "S1", "Emit", map[string]int{"N": 1}, nil); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	raw, err := wait(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != `{"N":1}` {
		t.Fatalf("params = %s", raw)
	}
}

func TestSubscribeSeesEvery(t *testing.T) {
	c := launch(t)
	var n atomic.Int32
	c.Subscribe("S1", "Ev", func(json.RawMessage) { n.Add(1) })
	for range 3 {
		if err := c.Call(context.Background(), "S1", "Emit", nil, nil); err != nil {
			t.Fatal(err)
		}
	}
	// The event follows its reply on the pipe; a further round trip orders after it.
	if err := c.Call(context.Background(), "S1", "Echo", nil, nil); err != nil {
		t.Fatal(err)
	}
	if n.Load() != 3 {
		t.Fatalf("saw %d events, want 3", n.Load())
	}
}

func TestCallFailsWhenChromeDies(t *testing.T) {
	c := launch(t)
	done := make(chan error, 1)
	go func() { done <- c.Call(context.Background(), "", "Die", nil, nil) }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("Call succeeded after Chrome died")
		}
		if got := err.Error(); !strings.Contains(got, "fake chrome crashed") {
			t.Fatalf("error lacks stderr tail: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Call still pending 2s after Chrome died")
	}
	if err := c.Call(context.Background(), "", "Echo", nil, nil); err == nil {
		t.Fatal("Call on a dead connection succeeded")
	}
}

func TestOpenPage(t *testing.T) {
	c := launch(t)
	s, err := c.OpenPage(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if s != "S1" {
		t.Fatalf("session = %q", s)
	}
}

func TestCloseIsClean(t *testing.T) {
	c := launch(t)
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestLaunchEndsSessionAtTimeout(t *testing.T) {
	c := launchFor(t, 300*time.Millisecond)
	select {
	case <-c.dead:
	case <-time.After(10 * time.Second):
		t.Fatal("chrome outlived its session timeout")
	}
	if err := c.Call(context.Background(), "", "Echo", nil, nil); err == nil {
		t.Fatal("a call after the session timeout succeeded")
	}
}
