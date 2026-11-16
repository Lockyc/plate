package walkthrough

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"image/png"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/lockyc/plate/internal/cdp"
	"github.com/lockyc/plate/internal/render"
)

// layersJS draws the cursor and the caption bar on every document the
// recording visits, and exposes __plate.caption, __plate.glide and
// __plate.find.
//
//go:embed layers.js
var layersJS string

// Recording is a script as Chrome played it: every screencast frame and when
// each step ran, in ms since the first frame.
type Recording struct {
	FramesDir string       // PNG per screencast frame
	Frames    []FrameStamp // ms since recording start, per frame
	Steps     []StepSpan   // per step: StartMS, EndMS
}

// FrameStamp is one frame's file, relative to FramesDir, and when Chrome
// drew it.
type FrameStamp struct {
	File string
	MS   float64
}

// StepSpan is when a step started and when its hold ended; each step starts
// where the one before ended.
type StepSpan struct{ StartMS, EndMS float64 }

// A step's timing: the cursor glides for glideTime before a click or type,
// the action is followed by a wait for the network to go quiet (nothing in
// flight for quietTime) capped at settleCap, and the step is held until
// max(audio + audioPad, minHold) has passed since it started.
const (
	glideTime  = 400 * time.Millisecond
	settleCap  = 1500 * time.Millisecond
	quietTime  = 500 * time.Millisecond
	audioPad   = 400 * time.Millisecond
	minHold    = 800 * time.Millisecond
	keystroke  = 50 * time.Millisecond // between typed characters
	pollTime   = 50 * time.Millisecond
	firstFrame = 5 * time.Second // the longest Chrome may take to send its first frame
)

// afterStep, when set, runs after each step's hold, with the page as it
// stands; tests use it to inspect the page.
var afterStep func(ctx context.Context, r *recorder, i int) error

// Record plays s against the app at baseURL in headless Chrome, in real
// time, and writes every frame Chrome draws to dir. It first opens each of
// warmup (resolved against baseURL), which is never recorded; the session it
// leaves, cookies and all, is the one recorded, starting from a blank page.
// durations[i] is step i's audio length, 0 when it has none. Omitted script
// keys take their defaults; script itself is left as it is.
//
// A step whose target does not match exactly one visible, uncovered element
// within its timeout_ms ends the recording with an error naming the step.
// The recording ends with the last step's hold: no frame is stamped later.
func Record(ctx context.Context, script *Script, baseURL string, warmup []string, durations []time.Duration, dir string) (_ *Recording, err error) {
	sc := *script
	sc.Steps = slices.Clone(script.Steps)
	sc.fillDefaults()
	s := &sc
	if len(durations) != len(s.Steps) {
		return nil, fmt.Errorf("%d durations for %d steps", len(durations), len(s.Steps))
	}
	base, err := url.Parse(baseURL)
	if err != nil || (base.Scheme != "http" && base.Scheme != "https") || base.Host == "" {
		return nil, fmt.Errorf("base URL %q: want an absolute http or https URL", baseURL)
	}
	if err := render.CheckSize(s.Width, s.Height, 1); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	conn, err := cdp.Launch(ctx, render.Remote(baseURL).ChromeArgs, sessionBudget(s, warmup, durations))
	if err != nil {
		return nil, err
	}
	r := &recorder{conn: conn, s: s, base: base, dir: dir}
	defer func() {
		cerr := conn.Close()
		r.frames.stop() // a recording that failed leaves its sink running
		if err == nil && cerr != nil {
			err = cerr
		}
	}()
	if err := r.open(ctx); err != nil {
		return nil, err
	}
	for _, u := range warmup {
		if err := r.navigate(ctx, u, time.Duration(DefaultTimeoutMS)*time.Millisecond); err != nil {
			return nil, fmt.Errorf("warmup %s: %w", u, err)
		}
	}
	if len(warmup) > 0 {
		// The warmup's landing page is not the recording's to show.
		if err := r.navigate(ctx, "about:blank", time.Duration(DefaultTimeoutMS)*time.Millisecond); err != nil {
			return nil, err
		}
	}
	t0, err := r.startScreencast(ctx)
	if err != nil {
		return nil, err
	}
	rec := &Recording{FramesDir: dir}
	ms := func(t time.Time) float64 { return float64(t.Sub(t0).Microseconds()) / 1000 }
	start := t0
	for i, st := range s.Steps {
		if err := r.step(ctx, st); err != nil {
			return nil, fmt.Errorf("step %d (%s): %w", i+1, describe(st), err)
		}
		if err := sleepUntil(ctx, start.Add(max(durations[i]+audioPad, minHold))); err != nil {
			return nil, err
		}
		end := time.Now()
		rec.Steps = append(rec.Steps, StepSpan{StartMS: ms(start), EndMS: ms(end)})
		if err := r.frames.failed(); err != nil {
			return nil, err
		}
		if afterStep != nil {
			if err := afterStep(ctx, r, i); err != nil {
				return nil, err
			}
		}
		start = end
	}
	frames, err := r.stopScreencast(ctx)
	if err != nil {
		return nil, err
	}
	last := rec.Steps[len(rec.Steps)-1].EndMS
	for _, f := range frames {
		if f.MS <= last {
			rec.Frames = append(rec.Frames, f)
		}
	}
	return rec, nil
}

// sessionBudget is the longest the recording can take: every wait at its
// limit, plus a minute for Chrome's own start and stop.
func sessionBudget(s *Script, warmup []string, durations []time.Duration) time.Duration {
	ms := func(n int) time.Duration { return time.Duration(n) * time.Millisecond }
	d := time.Minute + firstFrame + time.Duration(len(warmup))*ms(DefaultTimeoutMS)
	for i, st := range s.Steps {
		d += ms(*st.TimeoutMS) + glideTime + settleCap + max(durations[i]+audioPad, minHold) +
			time.Duration(len([]rune(st.Text)))*keystroke
	}
	return d
}

func describe(st Step) string {
	switch st.Action {
	case "goto":
		return "goto " + st.URL
	case "press":
		return "press " + st.Key
	}
	return st.Action + " " + st.Selector
}

func sleepUntil(ctx context.Context, t time.Time) error {
	d := time.Until(t)
	if d <= 0 {
		return nil
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// recorder is a page open in Chrome with the layers installed.
type recorder struct {
	conn    *cdp.Conn
	session string
	s       *Script
	base    *url.URL
	dir     string
	net     network
	frames  frameSink

	stateID string    // the new-document script that sets __plateState
	state   pageState // what it sets
}

// pageState is what a new document's layers start with.
type pageState struct {
	Caption string   `json:"caption"`
	X       *float64 `json:"x"`
	Y       *float64 `json:"y"`
}

func (r *recorder) eval(ctx context.Context, expr string, out any) error {
	return r.conn.Eval(ctx, r.session, expr, out)
}

func (r *recorder) call(ctx context.Context, method string, params, result any) error {
	return r.conn.Call(ctx, r.session, method, params, result)
}

func (r *recorder) open(ctx context.Context) (err error) {
	if r.session, err = r.conn.OpenPage(ctx); err != nil {
		return err
	}
	r.net.watch(r.conn, r.session)
	steps := []struct {
		method string
		params any
	}{
		{"Page.enable", nil},
		{"Network.enable", nil},
		{"Page.addScriptToEvaluateOnNewDocument", map[string]any{"source": layersJS}},
		{"Emulation.setDeviceMetricsOverride", map[string]any{"width": r.s.Width, "height": r.s.Height, "deviceScaleFactor": 1, "mobile": false}},
	}
	for _, s := range steps {
		if err := r.call(ctx, s.method, s.params, nil); err != nil {
			return err
		}
	}
	if err := r.setState(ctx, pageState{}); err != nil {
		return err
	}
	// The blank page Chrome opened with predates the script above.
	return r.eval(ctx, layersJS, nil)
}

// setState makes st what every new document's layers start with, and shows
// its caption on the page as it stands.
func (r *recorder) setState(ctx context.Context, st pageState) error {
	b, err := json.Marshal(st)
	if err != nil {
		return err
	}
	src := "window.__plateState = " + string(b) + ";"
	if r.stateID != "" {
		// A navigation can drop the script before this removes it. A state
		// script left behind is harmless either way: the one added below
		// runs after it.
		err := r.call(ctx, "Page.removeScriptToEvaluateOnNewDocument", map[string]any{"identifier": r.stateID}, nil)
		var cerr *cdp.Error
		if err != nil && !(errors.As(err, &cerr) && strings.Contains(cerr.Message, "not found")) {
			return err
		}
	}
	var added struct {
		Identifier string `json:"identifier"`
	}
	if err := r.call(ctx, "Page.addScriptToEvaluateOnNewDocument", map[string]any{"source": src}, &added); err != nil {
		return err
	}
	r.stateID, r.state = added.Identifier, st
	// A document mid-navigation has no context to evaluate in, and the
	// document replacing it starts from the script above; so an eval that
	// fails is retried until the next document can take it.
	expr := src + " window.__plate && __plate.caption(__plateState.caption);"
	deadline := time.Now().Add(time.Duration(DefaultTimeoutMS) * time.Millisecond)
	for {
		err := r.eval(ctx, expr, nil)
		if err == nil || ctx.Err() != nil || time.Now().After(deadline) {
			return err
		}
		if err := sleepUntil(ctx, time.Now().Add(pollTime)); err != nil {
			return err
		}
	}
}

// navigate loads u, resolved against the base URL, and waits up to limit
// for its load event. A same-document navigation (a new fragment) loads
// nothing, so it has no load event to wait for.
func (r *recorder) navigate(ctx context.Context, u string, limit time.Duration) error {
	ref, err := url.Parse(u)
	if err != nil {
		return err
	}
	target := r.base.ResolveReference(ref).String()
	loaded := r.conn.Expect(r.session, "Page.loadEventFired")
	var nav struct {
		LoaderID  string `json:"loaderId"`
		ErrorText string `json:"errorText"`
	}
	if err := r.call(ctx, "Page.navigate", map[string]any{"url": target}, &nav); err != nil {
		return err
	}
	if nav.ErrorText != "" {
		return fmt.Errorf("%s: %s", target, nav.ErrorText)
	}
	if nav.LoaderID == "" {
		return nil
	}
	wctx, cancel := context.WithTimeout(ctx, limit)
	defer cancel()
	if _, err := loaded(wctx); err != nil {
		if ctx.Err() == nil && errors.Is(err, context.DeadlineExceeded) {
			return fmt.Errorf("%s did not load within %d ms", target, limit.Milliseconds())
		}
		return err
	}
	return nil
}

// step plays st: its caption, the glide, the action, then the wait for the
// network to go quiet. The hold is the caller's.
func (r *recorder) step(ctx context.Context, st Step) error {
	limit := time.Duration(*st.TimeoutMS) * time.Millisecond
	if err := r.setState(ctx, pageState{Caption: st.Caption, X: r.state.X, Y: r.state.Y}); err != nil {
		return err
	}
	switch st.Action {
	case "goto":
		if err := r.navigate(ctx, st.URL, limit); err != nil {
			return err
		}
	case "click", "type":
		x, y, err := r.find(ctx, st.Selector, limit)
		if err != nil {
			return err
		}
		if err := r.glide(ctx, x, y); err != nil {
			return err
		}
		if err := r.click(ctx, x, y); err != nil {
			return err
		}
		if st.Action == "type" {
			if err := r.typeText(ctx, st.Text); err != nil {
				return err
			}
		}
	case "press":
		if err := r.press(ctx, st.Key); err != nil {
			return err
		}
	case "wait_for":
		if _, _, err := r.find(ctx, st.Selector, limit); err != nil {
			return err
		}
	default:
		return fmt.Errorf("unknown action %q", st.Action)
	}
	return r.settle(ctx, time.Now())
}

// find waits up to limit for sel to match exactly one visible, uncovered
// element, and
// returns its centre in CSS px once it is scrolled into view.
func (r *recorder) find(ctx context.Context, sel string, limit time.Duration) (float64, float64, error) {
	q, err := json.Marshal(sel)
	if err != nil {
		return 0, 0, err
	}
	deadline := time.Now().Add(limit)
	for {
		var f struct {
			Count   int     `json:"count"`
			Visible bool    `json:"visible"`
			Covered bool    `json:"covered"`
			X       float64 `json:"x"`
			Y       float64 `json:"y"`
		}
		// A document mid-navigation has no context to evaluate in; that
		// passes, so the error only counts if it is still there at the end.
		evalErr := r.eval(ctx, "__plate.find("+string(q)+")", &f)
		if evalErr == nil && f.Count == 1 && f.Visible {
			return f.X, f.Y, nil
		}
		if ctx.Err() != nil {
			return 0, 0, ctx.Err()
		}
		if time.Now().After(deadline) {
			ms := limit.Milliseconds()
			switch {
			case evalErr != nil:
				return 0, 0, evalErr
			case f.Count == 0:
				return 0, 0, fmt.Errorf("no element matches within %d ms", ms)
			case f.Count > 1:
				return 0, 0, fmt.Errorf("%d elements match after %d ms; a target must match exactly one", f.Count, ms)
			case f.Covered:
				return 0, 0, fmt.Errorf("the element it matches is still covered by another element after %d ms", ms)
			default:
				return 0, 0, fmt.Errorf("the element it matches is still hidden after %d ms", ms)
			}
		}
		if err := sleepUntil(ctx, time.Now().Add(pollTime)); err != nil {
			return 0, 0, err
		}
	}
}

// glide moves the drawn cursor to (x, y), and makes that where the next
// document's cursor starts.
func (r *recorder) glide(ctx context.Context, x, y float64) error {
	if err := r.eval(ctx, fmt.Sprintf("__plate.glide(%g, %g, %d)", x, y, glideTime.Milliseconds()), nil); err != nil {
		return err
	}
	return r.setState(ctx, pageState{Caption: r.state.Caption, X: &x, Y: &y})
}

func (r *recorder) click(ctx context.Context, x, y float64) error {
	for _, typ := range []string{"mouseMoved", "mousePressed", "mouseReleased"} {
		p := map[string]any{"type": typ, "x": x, "y": y}
		if typ != "mouseMoved" {
			p["button"], p["clickCount"] = "left", 1
		}
		if err := r.call(ctx, "Input.dispatchMouseEvent", p, nil); err != nil {
			return err
		}
	}
	return nil
}

// typeText enters text into the focused element a character at a time.
func (r *recorder) typeText(ctx context.Context, text string) error {
	for i, c := range []rune(text) {
		if i > 0 {
			if err := sleepUntil(ctx, time.Now().Add(keystroke)); err != nil {
				return err
			}
		}
		if err := r.call(ctx, "Input.insertText", map[string]any{"text": string(c)}, nil); err != nil {
			return err
		}
	}
	return nil
}

// keyEvent is a script key as Chrome's Input domain names it.
type keyEvent struct {
	key, code string
	vk        int
	text      string // what the key types, "" for none
}

// keyEvents covers every key in clip.Keys.
var keyEvents = map[string]keyEvent{
	"return": {"Enter", "Enter", 13, "\r"},
	"delete": {"Backspace", "Backspace", 8, ""},
	"tab":    {"Tab", "Tab", 9, ""},
	"escape": {"Escape", "Escape", 27, ""},
	"space":  {" ", "Space", 32, " "},
	"up":     {"ArrowUp", "ArrowUp", 38, ""},
	"down":   {"ArrowDown", "ArrowDown", 40, ""},
	"left":   {"ArrowLeft", "ArrowLeft", 37, ""},
	"right":  {"ArrowRight", "ArrowRight", 39, ""},
}

func (r *recorder) press(ctx context.Context, key string) error {
	k, ok := keyEvents[key]
	if !ok {
		return fmt.Errorf("unknown key %q", key)
	}
	down := map[string]any{"type": "rawKeyDown", "key": k.key, "code": k.code, "windowsVirtualKeyCode": k.vk}
	if k.text != "" {
		down["type"], down["text"] = "keyDown", k.text
	}
	if err := r.call(ctx, "Input.dispatchKeyEvent", down, nil); err != nil {
		return err
	}
	return r.call(ctx, "Input.dispatchKeyEvent", map[string]any{"type": "keyUp", "key": k.key, "code": k.code, "windowsVirtualKeyCode": k.vk}, nil)
}

// settle waits until the network has been quiet since the action ended at
// from, or settleCap has passed, whichever is first.
func (r *recorder) settle(ctx context.Context, from time.Time) error {
	limit := from.Add(settleCap)
	for !r.net.quiet(from) && time.Now().Before(limit) {
		if err := sleepUntil(ctx, time.Now().Add(pollTime/2)); err != nil {
			return err
		}
	}
	return nil
}

// network counts the page's requests in flight.
type network struct {
	mu       sync.Mutex
	inFlight map[string]bool
	last     time.Time // when a request last started or ended
}

func (n *network) watch(conn *cdp.Conn, session string) {
	n.inFlight = map[string]bool{}
	track := func(start bool) func(json.RawMessage) {
		return func(p json.RawMessage) {
			var ev struct {
				RequestID string `json:"requestId"`
			}
			if json.Unmarshal(p, &ev) != nil {
				return
			}
			n.mu.Lock()
			if start {
				n.inFlight[ev.RequestID] = true
			} else {
				delete(n.inFlight, ev.RequestID)
			}
			n.last = time.Now()
			n.mu.Unlock()
		}
	}
	conn.Subscribe(session, "Network.requestWillBeSent", track(true))
	conn.Subscribe(session, "Network.loadingFinished", track(false))
	conn.Subscribe(session, "Network.loadingFailed", track(false))
}

// quiet is whether nothing has been in flight for quietTime, counting from
// no earlier than from.
func (n *network) quiet(from time.Time) bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	since := from
	if n.last.After(since) {
		since = n.last
	}
	return len(n.inFlight) == 0 && time.Since(since) >= quietTime
}

// startScreencast starts Chrome sending frames and returns the first one's
// time, which is the recording's zero.
func (r *recorder) startScreencast(ctx context.Context) (time.Time, error) {
	r.frames.start(ctx, r)
	params := map[string]any{"format": "png", "maxWidth": r.s.Width, "maxHeight": r.s.Height, "everyNthFrame": 1}
	if err := r.call(ctx, "Page.startScreencast", params, nil); err != nil {
		return time.Time{}, err
	}
	timer := time.NewTimer(firstFrame)
	defer timer.Stop()
	select {
	case <-r.frames.first:
		return r.frames.t0, nil
	case <-timer.C:
		if err := r.frames.failed(); err != nil {
			return time.Time{}, err
		}
		return time.Time{}, fmt.Errorf("Chrome sent no screencast frame within %d ms", firstFrame.Milliseconds())
	case <-ctx.Done():
		return time.Time{}, ctx.Err()
	}
}

// stopScreencast stops the frames and returns every one written.
func (r *recorder) stopScreencast(ctx context.Context) ([]FrameStamp, error) {
	if err := r.call(ctx, "Page.stopScreencast", nil, nil); err != nil {
		return nil, err
	}
	return r.frames.stop()
}

// frameSink writes screencast frames to disk off the reader goroutine, which
// may not call Chrome, and acknowledges each so Chrome sends the next.
type frameSink struct {
	r     *recorder
	first chan struct{} // closed once t0 is set
	t0    time.Time
	wake  chan struct{}
	quit  chan struct{}
	done  chan struct{}

	stopOnce sync.Once

	mu     sync.Mutex
	queue  []json.RawMessage
	frames []FrameStamp
	err    error
}

func (f *frameSink) start(ctx context.Context, r *recorder) {
	f.r = r
	f.first = make(chan struct{})
	f.wake = make(chan struct{}, 1)
	f.quit = make(chan struct{})
	f.done = make(chan struct{})
	r.conn.Subscribe(r.session, "Page.screencastFrame", func(p json.RawMessage) {
		f.mu.Lock()
		f.queue = append(f.queue, p)
		f.mu.Unlock()
		select {
		case f.wake <- struct{}{}:
		default:
		}
	})
	go f.run(ctx)
}

func (f *frameSink) run(ctx context.Context) {
	defer close(f.done)
	quitting := false
	for {
		f.mu.Lock()
		batch := f.queue
		f.queue = nil
		f.mu.Unlock()
		for _, p := range batch {
			if err := f.write(ctx, p); err != nil {
				f.mu.Lock()
				f.err = err
				f.mu.Unlock()
				return
			}
		}
		if len(batch) > 0 {
			continue
		}
		if quitting {
			return
		}
		select {
		case <-f.wake:
		case <-f.quit:
			quitting = true // one more pass for frames queued meanwhile
		case <-ctx.Done():
			return
		}
	}
}

func (f *frameSink) write(ctx context.Context, p json.RawMessage) error {
	var ev struct {
		Data      string `json:"data"`
		SessionID int    `json:"sessionId"`
		Metadata  struct {
			Timestamp *float64 `json:"timestamp"`
		} `json:"metadata"`
	}
	if err := json.Unmarshal(p, &ev); err != nil {
		return fmt.Errorf("screencast frame: %w", err)
	}
	if err := f.r.call(ctx, "Page.screencastFrameAck", map[string]any{"sessionId": ev.SessionID}, nil); err != nil {
		return err
	}
	if ev.Metadata.Timestamp == nil {
		return errors.New("screencast frame has no timestamp")
	}
	at := time.UnixMicro(int64(*ev.Metadata.Timestamp * 1e6))
	b, err := base64.StdEncoding.DecodeString(ev.Data)
	if err != nil {
		return fmt.Errorf("screencast frame: %w", err)
	}
	cfg, err := png.DecodeConfig(bytes.NewReader(b))
	if err != nil {
		return fmt.Errorf("screencast frame: %w", err)
	}
	if cfg.Width != f.r.s.Width || cfg.Height != f.r.s.Height {
		return fmt.Errorf("Chrome sent a %dx%d frame, expected the %dx%d viewport", cfg.Width, cfg.Height, f.r.s.Width, f.r.s.Height)
	}
	f.mu.Lock()
	n := len(f.frames)
	f.mu.Unlock()
	if n == 0 {
		f.t0 = at
	}
	name := fmt.Sprintf("frame-%06d.png", n+1)
	if err := os.WriteFile(filepath.Join(f.r.dir, name), b, 0o644); err != nil {
		return err
	}
	f.mu.Lock()
	f.frames = append(f.frames, FrameStamp{File: name, MS: float64(at.Sub(f.t0).Microseconds()) / 1000})
	f.mu.Unlock()
	if n == 0 {
		close(f.first)
	}
	return nil
}

// failed is the error that stopped the sink, if any.
func (f *frameSink) failed() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.err
}

// stop ends the sink once the frames already sent are written. It is safe
// to call more than once, and on a sink never started.
func (f *frameSink) stop() ([]FrameStamp, error) {
	if f.quit == nil {
		return nil, nil
	}
	f.stopOnce.Do(func() { close(f.quit) })
	<-f.done
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.frames, f.err
}
