package cdp

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lockyc/plate/internal/chrometest"
	"github.com/lockyc/plate/internal/engine"
)

func TestMain(m *testing.M) { chrometest.Main(m) }

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
