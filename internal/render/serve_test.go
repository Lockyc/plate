package render

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// proxied returns a client that sends everything through srv and refuses to
// follow redirects, so a redirect shows up as a test failure.
func proxied(t *testing.T, srv *server) *http.Client {
	t.Helper()
	pu, err := url.Parse(strings.TrimPrefix(srv.proxyFlag(), "--proxy-server="))
	if err != nil {
		t.Fatal(err)
	}
	return &http.Client{
		Transport:     &http.Transport{Proxy: http.ProxyURL(pu)},
		CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("redirected") },
	}
}

func startServer(t *testing.T) *server {
	t.Helper()
	srv, err := serve()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(srv.Close)
	return srv
}

func TestServerServesFileAndRecords(t *testing.T) {
	tmp := t.TempDir()
	p := filepath.Join(tmp, "a.css")
	if err := os.WriteFile(p, []byte("body{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	srv := startServer(t)
	resp, err := proxied(t, srv).Get(srv.url(p, ""))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 || string(body) != "body{}" {
		t.Fatalf("got %d %q", resp.StatusCode, body)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/css") {
		t.Errorf("Content-Type = %q", ct)
	}
	if got := srv.loads().Files; !reflect.DeepEqual(got, map[string]string{p: sumOf("body{}")}) {
		t.Errorf("Files = %v", got)
	}
}

// Only this run's host reads files: the canonical host, or any other guess at a
// subdomain of it, is refused and reads nothing.
func TestServerServesOnlyItsOwnHost(t *testing.T) {
	tmp := t.TempDir()
	p := filepath.Join(tmp, "secret.txt")
	if err := os.WriteFile(p, []byte("s"), 0o644); err != nil {
		t.Fatal(err)
	}
	srv := startServer(t)
	other := startServer(t)
	if srv.host == other.host || !strings.HasSuffix(srv.host, "."+plateHost) {
		t.Fatalf("hosts must be fresh per run: %q %q", srv.host, other.host)
	}
	for _, u := range []string{PlateURL(p, ""), other.url(p, "")} {
		resp, err := proxied(t, srv).Get(u)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("%s: got %d, want 403", u, resp.StatusCode)
		}
	}
	if got := srv.loads().Files; len(got) != 0 {
		t.Errorf("a refused host must read nothing: %v", got)
	}
}

func sumOf(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// A path keeps what its first request found for the server's lifetime: a
// file edited afterwards is served as first read, and a path first missing
// stays missing after it appears, so one set of bytes describes the run.
func TestServerKeepsFirstRead(t *testing.T) {
	tmp := t.TempDir()
	p := filepath.Join(tmp, "a.css")
	gone := filepath.Join(tmp, "b.css")
	srv := startServer(t)
	c := proxied(t, srv)
	get := func(p string) (int, string) {
		resp, err := c.Get(srv.url(p, ""))
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(body)
	}
	os.WriteFile(p, []byte("one"), 0o644)
	get(p)
	os.WriteFile(p, []byte("two"), 0o644)
	if code, body := get(p); code != 200 || body != "one" {
		t.Errorf("after an edit got %d %q, want the first read", code, body)
	}
	get(gone)
	os.WriteFile(gone, []byte("now"), 0o644)
	if code, _ := get(gone); code != 404 {
		t.Errorf("a path first missing served %d after it appeared", code)
	}
	l := srv.loads()
	if !reflect.DeepEqual(l.Files, map[string]string{p: sumOf("one")}) || !reflect.DeepEqual(l.Missing, []string{gone}) {
		t.Errorf("loads = %+v", l)
	}
}

// Range requests for one path read and hash it once, and each gets its own
// slice of the bytes.
func TestServerReadsAFileOnce(t *testing.T) {
	p := filepath.Join(t.TempDir(), "clip.mp4")
	if err := os.WriteFile(p, []byte("0123456789"), 0o644); err != nil {
		t.Fatal(err)
	}
	srv := startServer(t)
	var reads atomic.Int32
	srv.read = func(p string) ([]byte, time.Time, bool) {
		reads.Add(1)
		return readRegular(p)
	}
	c := proxied(t, srv)
	for _, tc := range []struct{ rng, want string }{{"bytes=0-3", "0123"}, {"bytes=6-", "6789"}} {
		req, _ := http.NewRequest(http.MethodGet, srv.url(p, ""), nil)
		req.Header.Set("Range", tc.rng)
		resp, err := c.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusPartialContent || string(body) != tc.want {
			t.Errorf("%s: got %d %q, want 206 %q", tc.rng, resp.StatusCode, body, tc.want)
		}
	}
	if n := reads.Load(); n != 1 {
		t.Errorf("read from disk %d times, want 1", n)
	}
	if got := srv.loads().Files; !reflect.DeepEqual(got, map[string]string{p: sumOf("0123456789")}) {
		t.Errorf("Files = %v", got)
	}
}

func TestServerMissingAndDirectory(t *testing.T) {
	tmp := t.TempDir()
	missing := filepath.Join(tmp, "nope.png")
	srv := startServer(t)
	c := proxied(t, srv)
	for _, p := range []string{missing, tmp + "/"} {
		resp, err := c.Get(srv.url(p, ""))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != 404 {
			t.Errorf("%s: status %d", p, resp.StatusCode)
		}
	}
	l := srv.loads()
	if len(l.Files) != 0 || len(l.Missing) != 2 {
		t.Errorf("loads = %+v", l)
	}
}

func TestServerKeepsQuery(t *testing.T) {
	tmp := t.TempDir()
	p := filepath.Join(tmp, "index.html")
	if err := os.WriteFile(p, []byte("<p>x"), 0o644); err != nil {
		t.Fatal(err)
	}
	srv := startServer(t)
	resp, err := proxied(t, srv).Get(srv.url(p, "?m=date"))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status %d", resp.StatusCode)
	}
	if got := srv.loads().Files; !reflect.DeepEqual(got, map[string]string{p: sumOf("<p>x")}) {
		t.Errorf("Files = %v", got)
	}
}

func TestServerExternal(t *testing.T) {
	srv := startServer(t)
	resp, err := proxied(t, srv).Get("http://example.invalid/x")
	if err == nil {
		resp.Body.Close()
	}
	if got := srv.loads().External; !reflect.DeepEqual(got, []string{"example.invalid"}) {
		t.Errorf("External = %v", got)
	}
}

func TestServerNoExtensionIsSniffed(t *testing.T) {
	tmp := t.TempDir()
	p := filepath.Join(tmp, "url")
	if err := os.WriteFile(p, []byte("auction.example\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	srv := startServer(t)
	resp, err := proxied(t, srv).Get(srv.url(p, ""))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Errorf("Content-Type = %q", ct)
	}
}

func TestServerCloseDropsTunnels(t *testing.T) {
	up, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer up.Close()
	go func() {
		for {
			c, err := up.Accept()
			if err != nil {
				return
			}
			defer c.Close()
		}
	}()
	srv, err := serve()
	if err != nil {
		t.Fatal(err)
	}
	conn, err := net.Dial("tcp", strings.TrimPrefix(srv.proxyFlag(), "--proxy-server=http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	fmt.Fprintf(conn, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", up.Addr(), up.Addr())
	br := bufio.NewReader(conn)
	if line, err := br.ReadString('\n'); err != nil || !strings.Contains(line, "200") {
		t.Fatalf("CONNECT reply %q, %v", line, err)
	}
	if blank, _ := br.ReadString('\n'); blank != "\r\n" {
		t.Fatalf("reply headers end in %q", blank)
	}
	srv.Close()
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := br.ReadByte(); err == nil || errors.Is(err, os.ErrDeadlineExceeded) {
		t.Errorf("tunnel still open after Close: %v", err)
	}
}
