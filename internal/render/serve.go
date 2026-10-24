package render

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"maps"
	"mime"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// plateHost is the canonical host of a local page's address — what a cache key
// names (PlateURL). Chrome never loads it: each run's proxy serves files only
// under its own random subdomain of it (server.host), so another process on
// the machine that finds the proxy port cannot read files through it. ".invalid"
// never resolves, so a request that somehow bypassed the proxy fails rather than
// reaching a real server.
const plateHost = "plate.invalid"

// PlateURL is the canonical address of the local file at path local, with query
// (including its "?") kept so a page can still be parameterised.
func PlateURL(local, query string) string {
	return plateURLAt(plateHost, local, query)
}

// PlatePath is local as the root-relative URL path a loaded page resolves on its
// own host — what a page plate writes uses, so it loads under whichever run's host.
func PlatePath(local string) string {
	return (&url.URL{Path: local}).EscapedPath()
}

func plateURLAt(host, local, query string) string {
	return "http://" + host + PlatePath(local) + query
}

// loadSet is everything a page asked for, each list sorted and deduplicated.
// Files maps each local file that was served to the sha256 of the bytes
// served: a file edited after Chrome read it is recorded as Chrome saw it,
// not as it ended up. Missing are paths that were not there (or were
// directories), External are hosts the page reached out to.
type loadSet struct {
	Files             map[string]string
	Missing, External []string
}

// server is a loopback HTTP proxy that Chrome is pointed at for the length
// of one render. It exists so plate sees every file the page loads, which is
// what the render cache keys on: Chrome's NetLog does not log file:// loads,
// and a CDP client would be a whole new protocol client for the same answer.
//
// It never redirects and never lists a directory. http.ServeFile would turn
// /index.html into ./, so a page's own address would stop being the file
// that was recorded.
//
// Each path is read and hashed on its first request and answered from
// memory after that, missing included, for the server's lifetime. Every
// load in one run therefore sees one set of bytes, the set recorded, and a
// recording seeked through with range requests is read once, not once per
// seek.
type server struct {
	ln   net.Listener
	srv  *http.Server
	host string                                   // this run's host: 128 random bits under plateHost
	read func(p string) ([]byte, time.Time, bool) // readRegular; a test counts through it

	mu           sync.Mutex
	content      map[string]*content
	files        map[string]string // path → sha256 of the bytes served
	missing, ext map[string]bool
	tunnels      map[net.Conn]bool // hijacked CONNECT conns, which http.Server.Close leaves open
	closed       bool
}

// content is one path as its first request found it; ok is false for a path
// that was missing.
type content struct {
	once sync.Once
	data []byte
	mod  time.Time
	ok   bool
}

// loadPage runs do on the page pageURL resolved to and returns everything it
// loaded. A local page is served through a proxy for the length of do, so
// each load is recorded; a remote page goes to Chrome directly and loads
// nothing local.
func loadPage(ctx context.Context, u, local, query string, do func(context.Context, Load) ([]FileHash, error)) (loadSet, []FileHash, error) {
	if local == "" {
		hashes, err := do(ctx, Load{URL: u, ChromeArgs: baseArgs("")})
		return loadSet{}, hashes, err
	}
	srv, err := serve()
	if err != nil {
		return loadSet{}, nil, err
	}
	defer srv.Close()
	hashes, err := do(ctx, Load{URL: srv.url(local, query), ChromeArgs: baseArgs(srv.proxyFlag())})
	return srv.loads(), hashes, err
}

// serve starts the proxy on an ephemeral loopback port, under a fresh host.
func serve() (*server, error) {
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return nil, err
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	s := &server{
		ln:      ln,
		host:    hex.EncodeToString(nonce[:]) + "." + plateHost,
		read:    readRegular,
		content: map[string]*content{},
		files:   map[string]string{},
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

// url is the address Chrome loads local at for this run: PlateURL's path on the
// run's own host.
func (s *server) url(local, query string) string {
	return plateURLAt(s.host, local, query)
}

// proxyFlag is the Chrome flag that routes the page through the proxy. The
// matching --proxy-bypass-list=<-loopback> is baseArgs': without it Chrome
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
	return loadSet{Files: maps.Clone(s.files), Missing: keys(s.missing), External: keys(s.ext)}
}

func (s *server) note(m map[string]bool, k string) {
	s.mu.Lock()
	m[k] = true
	s.mu.Unlock()
}

// lookup is p as its first request found it, reading, hashing and recording
// it on that request. Concurrent first requests share the one read.
func (s *server) lookup(p string) *content {
	s.mu.Lock()
	c := s.content[p]
	if c == nil {
		c = &content{}
		s.content[p] = c
	}
	s.mu.Unlock()
	c.once.Do(func() {
		c.data, c.mod, c.ok = s.read(p)
		var sum string
		if c.ok {
			h := sha256.Sum256(c.data)
			sum = hex.EncodeToString(h[:])
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		if c.ok {
			s.files[p] = sum
		} else {
			s.missing[p] = true
		}
	})
	return c
}

func (s *server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.Method == http.MethodConnect:
		s.tunnel(w, r)
	case r.URL.IsAbs() && r.URL.Hostname() == s.host:
		s.file(w, r)
	case r.URL.IsAbs() && (r.URL.Hostname() == plateHost || strings.HasSuffix(r.URL.Hostname(), "."+plateHost)):
		// Any other plate host is a guess, not this run's page: refuse it rather
		// than forward it (it would only fail to resolve) or record it as external.
		http.Error(w, "plate: not this run's host", http.StatusForbidden)
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
	c := s.lookup(p)
	if !c.ok {
		http.NotFound(w, r)
		return
	}
	ct := mime.TypeByExtension(filepath.Ext(p))
	if ct == "" {
		ct = http.DetectContentType(c.data)
	}
	w.Header().Set("Content-Type", ct)
	// The bytes hashed are the bytes served. ServeContent adds Last-Modified
	// and ranges; with Content-Type set it never sniffs and never redirects.
	http.ServeContent(w, r, "", c.mod, bytes.NewReader(c.data))
}

// readRegular reads p whole when it is a regular file; false when it is
// absent, not a regular file, or unreadable. The server and the cache's
// check both judge a path through it, so a path the server called missing
// still hashes as absent when the record is checked.
func readRegular(p string) ([]byte, time.Time, bool) {
	f, err := os.Open(p)
	if err != nil {
		return nil, time.Time{}, false
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() {
		return nil, time.Time{}, false
	}
	data, err := io.ReadAll(f)
	if err != nil {
		return nil, time.Time{}, false
	}
	return data, fi.ModTime(), true
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
