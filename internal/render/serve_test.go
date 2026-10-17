package render

import (
	"bufio"
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
	resp, err := proxied(t, srv).Get(PlateURL(p, ""))
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
	if got := srv.loads().Files; !reflect.DeepEqual(got, []string{p}) {
		t.Errorf("Files = %v", got)
	}
}

func TestServerMissingAndDirectory(t *testing.T) {
	tmp := t.TempDir()
	missing := filepath.Join(tmp, "nope.png")
	srv := startServer(t)
	c := proxied(t, srv)
	for _, p := range []string{missing, tmp + "/"} {
		resp, err := c.Get(PlateURL(p, ""))
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
	resp, err := proxied(t, srv).Get(PlateURL(p, "?m=date"))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status %d", resp.StatusCode)
	}
	if got := srv.loads().Files; !reflect.DeepEqual(got, []string{p}) {
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
	resp, err := proxied(t, srv).Get(PlateURL(p, ""))
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
