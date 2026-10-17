package render

import (
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// plateHost is the made-up host a rendered page is loaded from. Chrome sends
// every request for it to the loopback proxy, which maps the URL path onto
// the local file of the same absolute path. ".invalid" never resolves, so a
// request that somehow bypassed the proxy fails rather than reaching a
// real server.
const plateHost = "plate.invalid"

// PlateURL is the URL Chrome loads for the local file at path local, with
// query (including its "?") kept so a page can still be parameterised.
func PlateURL(local, query string) string {
	return "http://" + plateHost + (&url.URL{Path: local}).EscapedPath() + query
}

// loadSet is everything a page asked for, each list sorted and deduplicated:
// Files are local files that were served, Missing are paths that were not
// there (or were directories), External are hosts the page reached out to.
type loadSet struct{ Files, Missing, External []string }

// server is a loopback HTTP proxy that Chrome is pointed at for the length
// of one render. It exists so plate sees every file the page loads, which is
// what the render cache keys on: Chrome's NetLog does not log file:// loads,
// and a CDP client would be a whole new protocol client for the same answer.
//
// It never redirects and never lists a directory. http.ServeFile would turn
// /index.html into ./, so a page's own address would stop being the file
// that was recorded.
type server struct {
	ln  net.Listener
	srv *http.Server

	mu                  sync.Mutex
	files, missing, ext map[string]bool
	tunnels             map[net.Conn]bool // hijacked CONNECT conns, which http.Server.Close leaves open
	closed              bool
}

// serve starts the proxy on an ephemeral loopback port.
func serve() (*server, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	s := &server{
		ln:      ln,
		files:   map[string]bool{},
		missing: map[string]bool{},
		ext:     map[string]bool{},
		tunnels: map[net.Conn]bool{},
	}
	s.srv = &http.Server{Handler: s}
	go s.srv.Serve(ln)
	return s, nil
}

// Close stops the proxy and drops its open connections, CONNECT tunnels
// included.
func (s *server) Close() {
	s.srv.Close()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	for c := range s.tunnels {
		c.Close()
	}
}

// track registers a tunnel conn for Close; false when the server is already
// closed.
func (s *server) track(c net.Conn) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return false
	}
	s.tunnels[c] = true
	return true
}

func (s *server) untrack(c net.Conn) {
	s.mu.Lock()
	delete(s.tunnels, c)
	s.mu.Unlock()
}

// proxyFlag is the Chrome flag that routes the page through the proxy. The
// matching --proxy-bypass-list=<-loopback> is the caller's: without it Chrome
// skips the proxy for loopback hosts.
func (s *server) proxyFlag() string {
	return "--proxy-server=http://" + s.ln.Addr().String()
}

// loads reports what has been requested so far.
func (s *server) loads() loadSet {
	s.mu.Lock()
	defer s.mu.Unlock()
	keys := func(m map[string]bool) []string {
		out := make([]string, 0, len(m))
		for k := range m {
			out = append(out, k)
		}
		sort.Strings(out)
		return out
	}
	return loadSet{Files: keys(s.files), Missing: keys(s.missing), External: keys(s.ext)}
}

func (s *server) note(m map[string]bool, k string) {
	s.mu.Lock()
	m[k] = true
	s.mu.Unlock()
}

func (s *server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.Method == http.MethodConnect:
		s.tunnel(w, r)
	case r.URL.IsAbs() && r.URL.Hostname() == plateHost:
		s.file(w, r)
	case r.URL.IsAbs():
		s.forward(w, r)
	default:
		http.Error(w, "plate: not a proxy request", http.StatusBadRequest)
	}
}

// file serves the local file named by the URL path, bytes only.
func (s *server) file(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	p := filepath.Clean(r.URL.Path)
	fi, err := os.Stat(p)
	if err != nil || !fi.Mode().IsRegular() {
		s.note(s.missing, p)
		http.NotFound(w, r)
		return
	}
	f, err := os.Open(p)
	if err != nil {
		s.note(s.missing, p)
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	ct := mime.TypeByExtension(filepath.Ext(p))
	if ct == "" {
		var head [512]byte
		n, _ := io.ReadFull(f, head[:])
		ct = http.DetectContentType(head[:n])
		f.Seek(0, io.SeekStart)
	}
	s.note(s.files, p)
	w.Header().Set("Content-Type", ct)
	// ServeContent adds Last-Modified and ranges; with Content-Type set it
	// never sniffs and never redirects.
	http.ServeContent(w, r, "", fi.ModTime(), f)
}

// forward relays a request for any other host and records the host.
func (s *server) forward(w http.ResponseWriter, r *http.Request) {
	s.note(s.ext, r.URL.Hostname())
	out := r.Clone(r.Context())
	out.RequestURI = ""
	resp, err := http.DefaultTransport.RoundTrip(out)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	for k, v := range resp.Header {
		w.Header()[k] = v
	}
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body)
}

// tunnel carries an https connection untouched and records the host.
func (s *server) tunnel(w http.ResponseWriter, r *http.Request) {
	host, _, err := net.SplitHostPort(r.Host)
	if err != nil {
		host = r.Host
	}
	s.note(s.ext, host)
	up, err := net.DialTimeout("tcp", r.Host, 10*time.Second)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	hj, ok := w.(http.Hijacker)
	if !ok {
		up.Close()
		http.Error(w, "cannot hijack", http.StatusInternalServerError)
		return
	}
	down, _, err := hj.Hijack()
	if err != nil {
		up.Close()
		return
	}
	if !s.track(down) {
		down.Close()
		up.Close()
		return
	}
	defer s.untrack(down)
	down.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n"))
	go func() { io.Copy(up, down); up.Close() }()
	io.Copy(down, up)
	down.Close()
	up.Close()
}
