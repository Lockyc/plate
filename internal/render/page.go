package render

import (
	"context"
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/lockyc/plate/internal/cdp"
)

// SettleJS defines __plateSettle in every frame: what the page is still
// loading, and a wait for it to be painted. Every capture plate takes of a
// page, a render's or an animation's, waits on it through Settle.
//
//go:embed settle.js
var SettleJS string

// Settle waits up to budget for the page in session to settle: nothing
// __plateSettle.pending names is still loading, every image is decoded and
// two frames have been painted since. Chrome lays text out with whatever
// font it has when the first frame is drawn and again once the rest arrive,
// so a capture taken sooner shows text at another width; under load it is
// taken sooner often enough to tell runs apart.
//
// Nothing must be pending after the painted frames as well as before: a
// page can start a load once it has loaded, as one that adds a font when
// document.fonts is ready does.
func Settle(ctx context.Context, c *cdp.Conn, session string, budget time.Duration) error {
	deadline := time.Now().Add(budget)
	idle := false // nothing was pending at the last look
	for {
		var pending []string
		if err := c.Eval(ctx, session, "__plateSettle.pending()", &pending); err != nil {
			return err
		}
		switch {
		case len(pending) == 0 && idle:
			return nil
		case len(pending) == 0:
			if err := c.Eval(ctx, session, "__plateSettle.painted()", nil); err != nil {
				return err
			}
			idle = true
			continue
		}
		idle = false
		if !time.Now().Before(deadline) {
			return fmt.Errorf("page not settled after %d ms: %s", budget.Milliseconds(), strings.Join(pending, ", "))
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(10 * time.Millisecond):
		}
	}
}

// dumpDOM is the page's markup as Chrome's --dump-dom writes it: the
// doctype, then the root element.
const dumpDOM = `(document.doctype ? new XMLSerializer().serializeToString(document.doctype) + "\n" : "") + document.documentElement.outerHTML`

// capture loads j's page in Chrome over the debugging pipe, runs j's
// virtual-time budget, waits for the page to settle, then calls shoot. setup
// runs before the page loads. A file:// subresource Chrome refused fails the
// capture: Chrome draws the page without it and says so only in its log.
// Chrome's exit status fails it too unless allowExit is set, for a caller
// that judges what shoot got by its content: Chrome's teardown watchdog can
// exit non-zero after the page was read whole.
func (j job) capture(ctx context.Context, setup []call, allowExit bool, shoot func(ctx context.Context, c *cdp.Conn, session string) error) (err error) {
	conn, err := cdp.Launch(ctx, j.args, 15*time.Minute)
	if err != nil {
		return err
	}
	defer func() {
		if cerr := conn.Close(); err == nil && cerr != nil && !allowExit {
			err = cerr
		}
	}()
	session, err := conn.OpenPage(ctx)
	if err != nil {
		return err
	}
	var mu sync.Mutex
	var blocked []string
	conn.Subscribe(session, "Log.entryAdded", func(p json.RawMessage) {
		var ev struct {
			Entry struct {
				Text string `json:"text"`
			} `json:"entry"`
		}
		if json.Unmarshal(p, &ev) != nil {
			return
		}
		for _, m := range blockedRe.FindAllStringSubmatch(ev.Entry.Text, -1) {
			mu.Lock()
			if !slices.Contains(blocked, m[1]) {
				blocked = append(blocked, m[1])
			}
			mu.Unlock()
		}
	})
	steps := append([]call{
		{"Page.enable", nil},
		{"Log.enable", nil},
		{"Page.addScriptToEvaluateOnNewDocument", map[string]any{"source": SettleJS}},
	}, setup...)
	for _, s := range steps {
		if err := conn.Call(ctx, session, s.method, s.params, nil); err != nil {
			return err
		}
	}
	loaded := conn.Expect(session, "Page.loadEventFired")
	var nav struct {
		ErrorText string `json:"errorText"`
	}
	if err := conn.Call(ctx, session, "Page.navigate", map[string]any{"url": j.url}, &nav); err != nil {
		return err
	}
	if nav.ErrorText != "" {
		return fmt.Errorf("%s: %s", j.url, nav.ErrorText)
	}
	if _, err := loaded(ctx); err != nil {
		return err
	}
	// Then the budget runs on virtual time, paused while a fetch is
	// pending, so the page's timers are fast-forwarded and what they load
	// is waited for. Only chrome-headless-shell fast-forwards, which is why
	// that is the pinned engine.
	if j.budget > 0 {
		expired := conn.Expect(session, "Emulation.virtualTimeBudgetExpired")
		if err := conn.Call(ctx, session, "Emulation.setVirtualTimePolicy", map[string]any{
			"policy": "pauseIfNetworkFetchesPending", "budget": j.budget}, nil); err != nil {
			return err
		}
		if _, err := expired(ctx); err != nil {
			return err
		}
	}
	if err := Settle(ctx, conn, session, time.Duration(j.budget)*time.Millisecond); err != nil {
		return err
	}
	if err := shoot(ctx, conn, session); err != nil {
		return err
	}
	mu.Lock()
	defer mu.Unlock()
	if len(blocked) > 0 {
		return fmt.Errorf("Chrome blocked %s: a local page cannot load file:// URLs; reference the file by a relative or absolute path", strings.Join(blocked, ", "))
	}
	return nil
}

// call is one protocol call: a method and its params.
type call struct {
	method string
	params any
}

// writeData writes the base64 payload Chrome returned for method to path.
func writeData(method, b64, path string) error {
	b, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return fmt.Errorf("%s: %w", method, err)
	}
	if len(b) == 0 {
		return fmt.Errorf("%s produced no output at %s", chrome, path)
	}
	return os.WriteFile(path, b, 0o644)
}
