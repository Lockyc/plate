// Package chrometest stands in for chrome-headless-shell in tests: the test
// binary itself, started again as a fake that speaks the debugging pipe.
// A package whose tests reach Chrome runs its tests through Main.
//
// The fake loads a page through plate's proxy as Chrome would and answers
// with fixtures named by environment variables:
//
//	FIXTURE_PNG      the screenshot's bytes (FIXTURE_DIR/<w>x<h>.png when set)
//	FIXTURE_PDF      the printed PDF's bytes (else a stub PDF)
//	FIXTURE_DOM      the page's markup (else <html>NAME</html>, NAME the page's)
//	FIXTURE_PENDING  what __plateSettle.pending() names, comma-separated
//	FIXTURE_CAPTURE  where the page's bytes as served are written, and its
//	                 first linked stylesheet's to the same path plus .css
//	FIXTURE_LOADS    paths loaded from the page's origin after the page
//	FIXTURE_URLS     absolute URLs loaded after those
//	FIXTURE_LOG      a log entry Chrome writes once the page has loaded
//	FIXTURE_AFTER    a sh command run once the page has loaded
//	FIXTURE_RELOAD   when set, FIXTURE_LOADS are loaded again after it
//	FIXTURE_FAIL     page names whose load ends Chrome with status 1
//	CHROME_EXIT      the status Chrome ends with when plate closes it
//	CANCEL_URL       a URL requested once the page has loaded, then Chrome hangs
package chrometest

import (
	"bufio"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/lockyc/plate/internal/enginetest"
)

const (
	envFake = "PLATE_FAKE_CHROME"
	envLog  = "PLATE_FAKE_CHROME_LOG"
)

// Main runs the fake when the test binary was started as Chrome, and the
// tests otherwise.
func Main(m *testing.M) {
	ServeIfFake()
	os.Exit(m.Run())
}

// ServeIfFake runs the fake and exits when the test binary was started as
// Chrome, for a TestMain with work of its own to do first in a test run.
func ServeIfFake() {
	if os.Getenv(envFake) != "" {
		os.Exit(serve())
	}
}

// Fake is a stubbed Chrome: Launches is the enginetest log of each start's
// arguments, Protocol the log of every protocol call made to it.
type Fake struct {
	Launches, Protocol string
}

// Stub points plate's Chrome at the fake for t.
func Stub(t *testing.T) Fake {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	proto := filepath.Join(t.TempDir(), "protocol.log")
	t.Setenv(envFake, "1")
	t.Setenv(envLog, proto)
	return Fake{enginetest.Stub(t, "chrome-headless-shell", `exec '`+exe+`' "$@"`), proto}
}

// Call is one protocol call the fake answered.
type Call struct {
	Method string
	Params map[string]any
}

// Calls reads f's protocol log, in order across every start.
func (f Fake) Calls(t *testing.T) []Call {
	t.Helper()
	b, err := os.ReadFile(f.Protocol)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var calls []Call
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		if line == "" {
			continue
		}
		var c Call
		if err := json.Unmarshal([]byte(line), &c); err != nil {
			t.Fatal(err)
		}
		calls = append(calls, c)
	}
	return calls
}

// Methods is the method of each of f's protocol calls, in order.
func (f Fake) Methods(t *testing.T) []string {
	t.Helper()
	var ms []string
	for _, c := range f.Calls(t) {
		ms = append(ms, c.Method)
	}
	return ms
}

// Starts is how many times Chrome was started.
func (f Fake) Starts(t *testing.T) int {
	t.Helper()
	return len(enginetest.Calls(t, f.Launches))
}

// serve speaks the debugging pipe: NUL-delimited JSON on fd 3 (in) and fd 4
// (out), until fd 3 reaches EOF, as Chrome does.
func serve() int {
	in, out := os.NewFile(3, "in"), os.NewFile(4, "out")
	r := bufio.NewReader(in)
	send := func(v any) {
		b, _ := json.Marshal(v)
		out.Write(append(b, 0))
	}
	var proxy string
	for _, a := range os.Args[1:] {
		if p, ok := strings.CutPrefix(a, "--proxy-server="); ok {
			proxy = p
		}
	}
	var page string
	var w, h int
	for {
		line, err := r.ReadBytes(0)
		if err != nil {
			return exitStatus()
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
		logCall(m.Method, m.Params)
		reply := func(result any) {
			send(map[string]any{"id": m.ID, "result": result, "sessionId": m.Session})
		}
		event := func(method string, params any) {
			send(map[string]any{"method": method, "params": params, "sessionId": m.Session})
		}
		var p map[string]any
		json.Unmarshal(m.Params, &p)
		switch m.Method {
		case "Target.createTarget":
			reply(map[string]any{"targetId": "T1"})
		case "Target.attachToTarget":
			reply(map[string]any{"sessionId": "S1"})
		case "Emulation.setDeviceMetricsOverride":
			w, h = int(p["width"].(float64)), int(p["height"].(float64))
			reply(map[string]any{})
		case "Page.navigate":
			page, _ = p["url"].(string)
			if code := load(page, proxy); code >= 0 {
				return code
			}
			reply(map[string]any{"frameId": "F1"})
			if s := os.Getenv("FIXTURE_LOG"); s != "" {
				event("Log.entryAdded", map[string]any{"entry": map[string]any{"text": s}})
			}
			event("Page.loadEventFired", map[string]any{})
		case "Emulation.setVirtualTimePolicy":
			reply(map[string]any{})
			event("Emulation.virtualTimeBudgetExpired", map[string]any{})
		case "Runtime.evaluate":
			reply(evaluate(p["expression"].(string), page))
		case "Page.captureScreenshot":
			fix := os.Getenv("FIXTURE_PNG")
			if d := os.Getenv("FIXTURE_DIR"); d != "" {
				fix = filepath.Join(d, fmt.Sprintf("%dx%d.png", w, h))
			}
			b, _ := os.ReadFile(fix)
			reply(map[string]any{"data": base64.StdEncoding.EncodeToString(b)})
		case "Page.printToPDF":
			b := []byte("%PDF-1.4 stub")
			if fix := os.Getenv("FIXTURE_PDF"); fix != "" {
				b, _ = os.ReadFile(fix)
			}
			reply(map[string]any{"data": base64.StdEncoding.EncodeToString(b)})
		// The protocol itself, for internal/cdp's tests.
		case "Echo":
			reply(m.Params)
		case "Fail":
			send(map[string]any{"id": m.ID, "error": map[string]any{"code": -32000, "message": "nope"}})
		case "Emit":
			reply(map[string]any{})
			event("Ev", m.Params)
		case "Die":
			fmt.Fprintln(os.Stderr, "fake chrome crashed")
			return 3
		default:
			reply(map[string]any{})
		}
	}
}

// logCall appends one call to the protocol log.
func logCall(method string, params json.RawMessage) {
	p := os.Getenv(envLog)
	if p == "" {
		return
	}
	f, err := os.OpenFile(p, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	if len(params) == 0 {
		params = json.RawMessage("{}")
	}
	b, _ := json.Marshal(struct {
		Method string          `json:"Method"`
		Params json.RawMessage `json:"Params"`
	}{method, params})
	f.Write(append(b, '\n'))
}

// name is a page URL's file name without its extension.
func name(page string) string {
	u, err := url.Parse(page)
	if err != nil {
		return ""
	}
	n := path.Base(u.Path)
	return strings.TrimSuffix(n, path.Ext(n))
}

// load fetches page and the fixtures' loads through proxy, and returns the
// status Chrome ends with when it ends here, or -1.
func load(page, proxy string) int {
	for _, f := range strings.Fields(os.Getenv("FIXTURE_FAIL")) {
		if f == name(page) {
			return 1
		}
	}
	client := http.DefaultClient
	if proxy != "" {
		pu, err := url.Parse(proxy)
		if err == nil {
			client = &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(pu)}}
		}
	}
	fetch := func(u string) []byte {
		res, err := client.Get(u)
		if err != nil {
			return nil
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return b
	}
	get := func(u string) { fetch(u) }
	origin := page
	if u, err := url.Parse(page); err == nil {
		origin = u.Scheme + "://" + u.Host
	}
	loads := func() {
		for _, p := range strings.Fields(os.Getenv("FIXTURE_LOADS")) {
			get(origin + p)
		}
	}
	if proxy != "" {
		body := fetch(page)
		if c := os.Getenv("FIXTURE_CAPTURE"); c != "" {
			os.WriteFile(c, body, 0o644)
			if m := stylesheetRe.FindSubmatch(body); m != nil {
				os.WriteFile(c+".css", fetch(origin+string(m[1])), 0o644)
			}
		}
		loads()
		for _, u := range strings.Fields(os.Getenv("FIXTURE_URLS")) {
			get(u)
		}
	}
	if s := os.Getenv("FIXTURE_AFTER"); s != "" {
		exec.Command("sh", "-c", s).Run()
	}
	if os.Getenv("FIXTURE_RELOAD") != "" && proxy != "" {
		loads()
	}
	if u := os.Getenv("CANCEL_URL"); u != "" {
		if res, err := http.Get(u); err == nil {
			res.Body.Close()
		}
		time.Sleep(30 * time.Second)
	}
	return -1
}

// stylesheetRe finds a page's first linked stylesheet by its absolute path.
var stylesheetRe = regexp.MustCompile(`<link rel="stylesheet" href="(/[^"]*)">`)

// exitStatus is CHROME_EXIT, the status the fake ends with once plate
// closes the pipe.
func exitStatus() int {
	c, _ := strconv.Atoi(os.Getenv("CHROME_EXIT"))
	return c
}

// evaluate answers the expressions plate evaluates in a page.
func evaluate(expr, page string) map[string]any {
	var v any
	switch {
	case strings.Contains(expr, "__plateSettle.pending()"):
		pending := []string{}
		if s := os.Getenv("FIXTURE_PENDING"); s != "" {
			pending = strings.Split(s, ",")
		}
		v = pending
	case strings.Contains(expr, "__plateSettle.painted()"):
		v = true
	case strings.Contains(expr, "outerHTML"):
		dom, ok := os.LookupEnv("FIXTURE_DOM")
		if !ok {
			dom = "<html>" + name(page) + "</html>"
		}
		v = dom
	}
	return map[string]any{"result": map[string]any{"value": v}}
}
