// Package animate captures a page's own animation frame by frame on a clock
// plate owns.
package animate

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"image/png"
	"math"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/lockyc/plate/internal/cdp"
	"github.com/lockyc/plate/internal/render"
)

// clockJS replaces the page's time sources before any page script runs and
// exposes __plate.seek and __plate.settle.
//
//go:embed clock.js
var clockJS string

// Capture is a page open in Chrome on plate's clock.
type Capture struct {
	conn    *cdp.Conn
	session string
	pw, ph  int // the screenshot's pixel size
	budget  time.Duration
	at      float64 // the last seek, in ms

	mu     sync.Mutex
	thrown []string // exceptions since the last check
}

// Open loads l's page at w×h CSS px and scale, on plate's clock, and waits
// up to budget for it to settle: the load event, fonts loaded, every img
// complete and every video with a frame to show, in every same-origin
// frame. A seek waits up to budget for a video's frame too.
func Open(ctx context.Context, l render.Load, w, h int, scale float64, budget time.Duration) (_ *Capture, err error) {
	if err := render.CheckSize(w, h, scale); err != nil {
		return nil, err
	}
	conn, err := cdp.Launch(ctx, l.ChromeArgs)
	if err != nil {
		return nil, err
	}
	c := &Capture{conn: conn, pw: int(math.Round(float64(w) * scale)), ph: int(math.Round(float64(h) * scale)), budget: budget}
	defer func() {
		if err != nil {
			conn.Close()
		}
	}()
	if c.session, err = conn.OpenPage(ctx); err != nil {
		return nil, err
	}
	conn.Subscribe(c.session, "Runtime.exceptionThrown", c.noteThrown)
	steps := []struct {
		method string
		params any
	}{
		{"Page.enable", nil},
		{"Runtime.enable", nil},
		{"Page.addScriptToEvaluateOnNewDocument", map[string]any{"source": clockJS}},
		{"Emulation.setDeviceMetricsOverride", map[string]any{"width": w, "height": h, "deviceScaleFactor": scale, "mobile": false}},
	}
	for _, s := range steps {
		if err := conn.Call(ctx, c.session, s.method, s.params, nil); err != nil {
			return nil, err
		}
	}
	var nav struct {
		ErrorText string `json:"errorText"`
	}
	if err := conn.Call(ctx, c.session, "Page.navigate", map[string]any{"url": l.URL}, &nav); err != nil {
		return nil, err
	}
	if nav.ErrorText != "" {
		return nil, fmt.Errorf("%s: %s", l.URL, nav.ErrorText)
	}
	var pending []string
	if err := c.Eval(ctx, fmt.Sprintf("__plate.settle(%d)", budget.Milliseconds()), &pending); err != nil {
		return nil, err
	}
	if len(pending) > 0 {
		return nil, fmt.Errorf("page not settled after %d ms: %s", budget.Milliseconds(), strings.Join(pending, ", "))
	}
	return c, nil
}

// noteThrown runs on the reader goroutine for each uncaught exception.
func (c *Capture) noteThrown(p json.RawMessage) {
	var ev struct {
		ExceptionDetails exceptionDetails `json:"exceptionDetails"`
	}
	if json.Unmarshal(p, &ev) != nil {
		return
	}
	c.mu.Lock()
	c.thrown = append(c.thrown, ev.ExceptionDetails.String())
	c.mu.Unlock()
}

type exceptionDetails struct {
	Text      string `json:"text"`
	URL       string `json:"url"`
	Line      int    `json:"lineNumber"`
	Exception *struct {
		Description string `json:"description"`
	} `json:"exception"`
}

// String is the exception's first line, and where it was thrown.
func (d exceptionDetails) String() string {
	msg := d.Text
	if d.Exception != nil && d.Exception.Description != "" {
		msg, _, _ = strings.Cut(d.Exception.Description, "\n")
	}
	if d.URL != "" {
		msg += fmt.Sprintf(" (%s:%d)", d.URL, d.Line+1)
	}
	return msg
}

// Seek moves the page's clock to ms and returns once every video shows its
// frame. It refuses a page that threw since the last seek, a video that
// cannot play or shows no frame within the budget, and a cross-origin
// iframe, whose clock plate cannot drive.
func (c *Capture) Seek(ctx context.Context, ms float64) error {
	var r struct {
		Errors []struct {
			Src  string `json:"src"`
			Code int    `json:"code"`
		} `json:"errors"`
		Stalled     []string `json:"stalled"`
		CrossOrigin []string `json:"crossOrigin"`
		Restless    int      `json:"restless"`
	}
	c.at = ms
	err := c.Eval(ctx, fmt.Sprintf("__plate.seek(%s)", jsNumber(ms)), &r)
	if err := c.threw(); err != nil {
		return err
	}
	switch {
	case err != nil:
		return err
	case len(r.CrossOrigin) > 0:
		return fmt.Errorf("iframe %s is cross-origin: plate's clock cannot drive it", r.CrossOrigin[0])
	case len(r.Errors) > 0:
		return fmt.Errorf("video %s cannot play: MediaError %d", r.Errors[0].Src, r.Errors[0].Code)
	case len(r.Stalled) > 0:
		return fmt.Errorf("video %s not ready after %d ms at %.0f ms", r.Stalled[0], c.budget.Milliseconds(), ms)
	case r.Restless > 0:
		return fmt.Errorf("page still starting animations, media seeks or timers after %d passes at %.0f ms", r.Restless, ms)
	}
	return nil
}

// threw refuses the page when it threw since the last check.
func (c *Capture) threw() error {
	c.mu.Lock()
	thrown := c.thrown
	c.thrown = nil
	c.mu.Unlock()
	if len(thrown) > 0 {
		return fmt.Errorf("page threw at %.0f ms: %s", c.at, strings.Join(thrown, "; "))
	}
	return nil
}

func jsNumber(f float64) string {
	b, _ := json.Marshal(f)
	return string(b)
}

// Frame writes the page as it stands to path as a PNG, refusing one that is
// not w×h × scale pixels, or a page that threw since the last seek.
func (c *Capture) Frame(ctx context.Context, path string) error {
	if err := c.threw(); err != nil {
		return err
	}
	var shot struct {
		Data string `json:"data"`
	}
	if err := c.conn.Call(ctx, c.session, "Page.captureScreenshot", map[string]any{"format": "png"}, &shot); err != nil {
		return err
	}
	b, err := base64.StdEncoding.DecodeString(shot.Data)
	if err != nil {
		return fmt.Errorf("screenshot: %w", err)
	}
	cfg, err := png.DecodeConfig(bytes.NewReader(b))
	if err != nil {
		return fmt.Errorf("screenshot: %w", err)
	}
	if cfg.Width != c.pw || cfg.Height != c.ph {
		return fmt.Errorf("Chrome captured %dx%d, expected %dx%d (size × scale)", cfg.Width, cfg.Height, c.pw, c.ph)
	}
	return os.WriteFile(path, b, 0o644)
}

// Eval evaluates expr in the page, awaiting a promise, and decodes its value
// into out when out is not nil. A thrown exception is an error.
func (c *Capture) Eval(ctx context.Context, expr string, out any) error {
	var r struct {
		Result struct {
			Value json.RawMessage `json:"value"`
		} `json:"result"`
		ExceptionDetails *exceptionDetails `json:"exceptionDetails"`
	}
	params := map[string]any{"expression": expr, "awaitPromise": true, "returnByValue": true}
	if err := c.conn.Call(ctx, c.session, "Runtime.evaluate", params, &r); err != nil {
		return err
	}
	if r.ExceptionDetails != nil {
		return fmt.Errorf("%s: %s", expr, r.ExceptionDetails)
	}
	if out == nil {
		return nil
	}
	if len(r.Result.Value) == 0 {
		return errors.New(expr + ": no value")
	}
	if err := json.Unmarshal(r.Result.Value, out); err != nil {
		return fmt.Errorf("%s: %w", expr, err)
	}
	return nil
}

// Close ends the page and Chrome.
func (c *Capture) Close() error { return c.conn.Close() }
