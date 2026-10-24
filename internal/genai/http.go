package genai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/lockyc/plate/internal/pins"
)

// callTimeout bounds one HTTP call; an image model can take minutes on a 4K image.
const callTimeout = 180 * time.Second

// backoff is the wait before each retry of a 429 or 5xx; its length is the
// retry count. A Retry-After header replaces the wait it falls on; one longer
// than callTimeout gives up instead.
var backoff = []time.Duration{2 * time.Second, 4 * time.Second, 8 * time.Second}

// maxBody caps a response body: a 4K PNG in base64 is well under it. A var so
// tests can shrink it.
var maxBody int64 = 256 << 20

// sleep waits d or until ctx ends. A var so tests can record waits instead.
var sleep = func(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// statusError is a non-2xx answer from a provider.
type statusError struct {
	provider string
	status   int
	msg      string
}

func (e *statusError) Error() string {
	s := fmt.Sprintf("%s: %s (HTTP %d)", e.provider, e.msg, e.status)
	if e.auth() {
		s += "; check " + keyEnv(e.provider)
	}
	return s
}

func (e *statusError) auth() bool {
	return e.status == http.StatusUnauthorized || e.status == http.StatusForbidden
}

// isAuth reports whether err is a provider refusing the key, which fails a
// whole round rather than one attempt.
func isAuth(err error) bool {
	var se *statusError
	return errors.As(err, &se) && se.auth()
}

// keyEnv names the environment variable holding provider's key.
func keyEnv(provider string) string {
	for _, m := range pins.ImageModels {
		if m.Provider == provider {
			return m.KeyEnv
		}
	}
	return "the " + provider + " API key"
}

// baseURL is def, or the PLATE_GENAI_<PROVIDER>_URL override (for tests).
func baseURL(provider, def string) string {
	if v := os.Getenv("PLATE_GENAI_" + strings.ToUpper(provider) + "_URL"); v != "" {
		return strings.TrimRight(v, "/")
	}
	return def
}

// do sends req (see doRaw) and decodes the JSON answer into out, unless out is
// nil.
func do(ctx context.Context, provider string, req *http.Request, out any) error {
	body, err := doRaw(ctx, provider, req)
	if err != nil {
		return err
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("%s: decoding the response: %w", provider, err)
	}
	return nil
}

// doRaw sends req, retrying a 429 or 5xx after each backoff wait, and returns
// the body of a 2xx answer. Each call has its own timeout. A request with a
// body must be replayable (http.NewRequest sets GetBody for the standard
// readers). Errors start "<provider>: " and carry the provider's own message.
func doRaw(ctx context.Context, provider string, req *http.Request) ([]byte, error) {
	for attempt := 0; ; attempt++ {
		r := req
		if attempt > 0 && req.Body != nil {
			if req.GetBody == nil {
				return nil, fmt.Errorf("%s: request body cannot be replayed for a retry", provider)
			}
			body, err := req.GetBody()
			if err != nil {
				return nil, fmt.Errorf("%s: %w", provider, err)
			}
			r = req.Clone(ctx)
			r.Body = body
		}
		body, status, hdr, err := call(ctx, r)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", provider, err)
		}
		if status/100 == 2 {
			return body, nil
		}
		se := &statusError{provider: provider, status: status, msg: bodyMessage(body, status)}
		retryable := status == http.StatusTooManyRequests || status >= 500
		if !retryable || attempt >= len(backoff) {
			return nil, se
		}
		wait := backoff[attempt]
		if ra, ok := retryAfter(hdr.Get("Retry-After")); ok {
			if ra > callTimeout {
				return nil, se
			}
			wait = ra
		}
		if err := sleep(ctx, wait); err != nil {
			return nil, fmt.Errorf("%s: %w (waiting to retry after: %s)", provider, err, se.msg)
		}
	}
}

// call makes one HTTP round trip under callTimeout and reads the whole body.
func call(ctx context.Context, req *http.Request) ([]byte, int, http.Header, error) {
	cctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	resp, err := http.DefaultClient.Do(req.WithContext(cctx))
	if err != nil {
		return nil, 0, nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
	if err == nil && int64(len(body)) > maxBody {
		err = fmt.Errorf("response is larger than %d MiB", maxBody>>20)
	}
	if err != nil {
		return nil, 0, nil, fmt.Errorf("reading the response: %w", err)
	}
	return body, resp.StatusCode, resp.Header, nil
}

// retryAfter parses a Retry-After header: delay seconds or an HTTP date. A
// seconds value past callTimeout (one too large for an int64 included) comes
// back as callTimeout+1s rather than overflowing, so do's cap still gives up.
// time.Until saturates, so a far-future date cannot wrap.
func retryAfter(v string) (time.Duration, bool) {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0, false
	}
	if strings.Trim(v, "0123456789") == "" {
		s, err := strconv.ParseInt(v, 10, 64)
		if err != nil || s > int64(callTimeout/time.Second) {
			return callTimeout + time.Second, true
		}
		return time.Duration(s) * time.Second, true
	}
	if t, err := http.ParseTime(v); err == nil {
		d := time.Until(t)
		if d < 0 {
			d = 0
		}
		return d, true
	}
	return 0, false
}

// bodyMessage pulls the provider's error message out of an error body: the
// {"error":{"message"}} shape (Google, OpenAI), {"error":"..."}, {"detail"}
// (BFL) or {"message"}; otherwise the body text itself, shortened.
func bodyMessage(body []byte, status int) string {
	var v struct {
		Error   json.RawMessage `json:"error"`
		Detail  json.RawMessage `json:"detail"`
		Message string          `json:"message"`
	}
	if json.Unmarshal(body, &v) == nil {
		var e struct {
			Message string `json:"message"`
		}
		var s string
		switch {
		case json.Unmarshal(v.Error, &e) == nil && e.Message != "":
			return e.Message
		case json.Unmarshal(v.Error, &s) == nil && s != "":
			return s
		case json.Unmarshal(v.Detail, &s) == nil && s != "":
			return s
		case len(v.Detail) > 0 && string(v.Detail) != "null":
			return string(v.Detail)
		case v.Message != "":
			return v.Message
		}
	}
	text := strings.TrimSpace(string(body))
	if text == "" {
		return http.StatusText(status)
	}
	if len(text) > 500 {
		text = text[:500] + "…"
	}
	return text
}
