// Package cdp speaks the Chrome DevTools protocol over Chrome's debugging
// pipe: NUL-delimited JSON, requests on its fd 3 and replies and events on
// its fd 4. One reader goroutine routes replies by id and events by
// (session, method).
package cdp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	"github.com/lockyc/plate/internal/engine"
)

// Error is a protocol error reply.
type Error struct {
	Code    int
	Message string
	Method  string
}

func (e *Error) Error() string {
	return fmt.Sprintf("cdp %s: %s (%d)", e.Method, e.Message, e.Code)
}

type reply struct {
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

type message struct {
	ID      int             `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
	Session string          `json:"sessionId"`
	reply
}

type key struct{ session, method string }

type waiter struct {
	key
	ch chan json.RawMessage
}

// Conn is a running Chrome and its protocol session.
type Conn struct {
	proc *engine.Proc

	wmu    sync.Mutex // serialises writes
	nextID int

	mu      sync.Mutex
	pending map[int]chan reply
	waiters []*waiter
	subs    map[key][]func(json.RawMessage)

	dead     chan struct{} // closed once the reader has stopped
	deadErr  error         // why, set before dead closes
	closeErr error
	closeOne sync.Once
}

// Launch starts Chrome with args, --remote-debugging-pipe and about:blank.
func Launch(ctx context.Context, args []string) (*Conn, error) {
	all := append(append([]string{}, args...), "--remote-debugging-pipe", "about:blank")
	p, err := engine.Start(ctx, engine.Cmd{Engine: "chrome-headless-shell", Args: all})
	if err != nil {
		return nil, err
	}
	c := &Conn{
		proc:    p,
		pending: map[int]chan reply{},
		subs:    map[key][]func(json.RawMessage){},
		dead:    make(chan struct{}),
	}
	go c.read()
	return c, nil
}

// shutdown closes the proc once and keeps its verdict.
func (c *Conn) shutdown() error {
	c.closeOne.Do(func() { c.closeErr = c.proc.Close() })
	return c.closeErr
}

func (c *Conn) read() {
	r := bufio.NewReaderSize(c.proc.Out, 1<<20)
	var readErr error
	for {
		line, err := r.ReadBytes(0)
		if err != nil {
			readErr = err
			break
		}
		var m message
		if err := json.Unmarshal(line[:len(line)-1], &m); err != nil {
			readErr = fmt.Errorf("malformed message: %w", err)
			break
		}
		if m.Method != "" {
			c.dispatch(m)
			continue
		}
		c.mu.Lock()
		ch := c.pending[m.ID]
		delete(c.pending, m.ID)
		c.mu.Unlock()
		if ch != nil {
			ch <- m.reply
		}
	}
	// A pipe error here means Chrome is gone or Close ran; Close's verdict
	// carries Chrome's stderr tail.
	cause := c.shutdown()
	if cause == nil {
		cause = fmt.Errorf("chrome closed the debugging pipe: %w", readErr)
	}
	c.deadErr = fmt.Errorf("chrome ended: %w", cause)
	close(c.dead)
}

func (c *Conn) dispatch(m message) {
	k := key{m.Session, m.Method}
	c.mu.Lock()
	subs := append([]func(json.RawMessage){}, c.subs[k]...)
	var hit []*waiter
	rest := c.waiters[:0]
	for _, w := range c.waiters {
		if w.key == k {
			hit = append(hit, w)
		} else {
			rest = append(rest, w)
		}
	}
	c.waiters = rest
	c.mu.Unlock()
	for _, w := range hit {
		w.ch <- m.Params
	}
	for _, fn := range subs {
		fn(m.Params)
	}
}

// Call sends method on session ("" is the browser) and decodes the result
// into result when it is not nil. A protocol error is an *Error.
func (c *Conn) Call(ctx context.Context, session, method string, params, result any) error {
	req := map[string]any{"method": method}
	if params != nil {
		req["params"] = params
	}
	if session != "" {
		req["sessionId"] = session
	}
	ch := make(chan reply, 1)

	c.wmu.Lock()
	c.nextID++
	id := c.nextID
	req["id"] = id
	b, err := json.Marshal(req)
	if err != nil {
		c.wmu.Unlock()
		return err
	}
	select {
	case <-c.dead:
		c.wmu.Unlock()
		return c.deadErr
	default:
	}
	c.mu.Lock()
	c.pending[id] = ch
	c.mu.Unlock()
	_, err = c.proc.In.Write(append(b, 0))
	c.wmu.Unlock()
	if err != nil {
		c.forget(id)
		select {
		case <-c.dead:
			return c.deadErr
		default:
			return fmt.Errorf("cdp %s: %w", method, err)
		}
	}

	select {
	case r := <-ch:
		if r.Error != nil {
			return &Error{Code: r.Error.Code, Message: r.Error.Message, Method: method}
		}
		if result != nil {
			if err := json.Unmarshal(r.Result, result); err != nil {
				return fmt.Errorf("cdp %s: result: %w", method, err)
			}
		}
		return nil
	case <-c.dead:
		return c.deadErr
	case <-ctx.Done():
		c.forget(id)
		return ctx.Err()
	}
}

func (c *Conn) forget(id int) {
	c.mu.Lock()
	delete(c.pending, id)
	c.mu.Unlock()
}

// Expect registers interest in the first (session, method) event from now on
// and returns the function that waits for it. Register before the call that
// triggers the event.
func (c *Conn) Expect(session, method string) func(ctx context.Context) (json.RawMessage, error) {
	w := &waiter{key: key{session, method}, ch: make(chan json.RawMessage, 1)}
	c.mu.Lock()
	c.waiters = append(c.waiters, w)
	c.mu.Unlock()
	return func(ctx context.Context) (json.RawMessage, error) {
		select {
		case p := <-w.ch:
			return p, nil
		case <-c.dead:
			select {
			case p := <-w.ch:
				return p, nil
			default:
			}
			return nil, c.deadErr
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

// Subscribe calls fn for every (session, method) event, in arrival order on
// the reader goroutine, so fn must not block on a Call.
func (c *Conn) Subscribe(session, method string, fn func(json.RawMessage)) {
	k := key{session, method}
	c.mu.Lock()
	c.subs[k] = append(c.subs[k], fn)
	c.mu.Unlock()
}

// OpenPage creates a blank page target and attaches to it, returning its
// session id.
func (c *Conn) OpenPage(ctx context.Context) (string, error) {
	var t struct {
		TargetID string `json:"targetId"`
	}
	if err := c.Call(ctx, "", "Target.createTarget", map[string]any{"url": "about:blank"}, &t); err != nil {
		return "", err
	}
	var a struct {
		SessionID string `json:"sessionId"`
	}
	if err := c.Call(ctx, "", "Target.attachToTarget", map[string]any{"targetId": t.TargetID, "flatten": true}, &a); err != nil {
		return "", err
	}
	if a.SessionID == "" {
		return "", errors.New("cdp Target.attachToTarget: no sessionId")
	}
	return a.SessionID, nil
}

// Close ends the session and Chrome. It returns the engine's verdict: nil for
// a clean exit, a *engine.RunError when Chrome failed or the session was cut
// off.
func (c *Conn) Close() error {
	err := c.shutdown()
	<-c.dead
	return err
}
