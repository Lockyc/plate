package fonts

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

// fakeGoogle serves css2 for "Served Sans" at 400 and 700 italic only, and
// the font file it names.
func fakeGoogle(t *testing.T) *atomic.Int32 {
	t.Helper()
	var hits atomic.Int32
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		switch r.URL.Path {
		case "/css2":
			fam := r.URL.Query().Get("family")
			if fam != "Served Sans:ital,wght@0,400" && fam != "Served Sans:ital,wght@1,700" {
				http.Error(w, "Font family not found", http.StatusBadRequest)
				return
			}
			fmt.Fprintf(w, "@font-face {\n  font-family: 'Served Sans';\n  src: url(%s/f/%s.ttf) format('truetype');\n}\n", srv.URL, strings.NewReplacer(":", "_", ",", "_", "@", "_", " ", "_").Replace(fam))
		case "/f/Served_Sans_ital_wght_0_400.ttf", "/f/Served_Sans_ital_wght_1_700.ttf":
			w.Write([]byte("TTF " + r.URL.Path))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	old := googleCSS
	googleCSS = srv.URL + "/css2"
	t.Cleanup(func() { googleCSS = old })
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	return &hits
}

func TestGoogleFetchesOnceThenCaches(t *testing.T) {
	hits := fakeGoogle(t)
	v := Variant{Family: "Served Sans", Weight: 700, Italic: true}
	f, err := Google(context.Background(), v)
	if err != nil {
		t.Fatal(err)
	}
	if f.Family != "Served Sans" || f.Weight != "700" || f.Style != "italic" || filepath.Ext(f.File) != ".ttf" {
		t.Errorf("face %+v", f)
	}
	if b, _ := os.ReadFile(f.File); string(b) != "TTF /f/Served_Sans_ital_wght_1_700.ttf" {
		t.Errorf("cached font %q", b)
	}
	n := hits.Load()
	if _, err := Google(context.Background(), v); err != nil {
		t.Fatal(err)
	}
	if hits.Load() != n {
		t.Error("a cached face was fetched again")
	}
}

func TestGoogleNotServed(t *testing.T) {
	fakeGoogle(t)
	for _, v := range []Variant{{Family: "Arial", Weight: 400}, {Family: "Served Sans", Weight: 700}} {
		if _, err := Google(context.Background(), v); !errors.Is(err, ErrNotServed) {
			t.Errorf("%s: err %v, want ErrNotServed", v, err)
		}
	}
}

func TestFontsGoogleFlag(t *testing.T) {
	fakeGoogle(t)
	out := filepath.Join(t.TempDir(), "fonts.css")
	var o, e bytes.Buffer
	if code := Main(context.Background(), []string{"--google", "Served Sans:400:normal", "-o", out}, &o, &e); code != 0 {
		t.Fatalf("code %d: %s", code, e.String())
	}
	css, _ := os.ReadFile(out)
	if !strings.Contains(string(css), `font-family: "Served Sans";`) || !strings.Contains(string(css), `format("truetype")`) {
		t.Errorf("css:\n%s", css)
	}
	for _, c := range []struct {
		arg  string
		code int
		want string
	}{
		{"Served Sans:400", 2, "FAMILY:WEIGHT:STYLE"},
		{"Served Sans:bold:normal", 2, "numeric weight"},
		{"Served Sans:400:oblique", 2, "numeric weight"},
		{"Arial:400:normal", 1, "not served by Google Fonts"},
	} {
		var o, e bytes.Buffer
		if code := Main(context.Background(), []string{"--google", c.arg, "-o", out}, &o, &e); code != c.code || !strings.Contains(e.String(), c.want) {
			t.Errorf("%q: code %d stderr %q; want %d containing %q", c.arg, code, e.String(), c.code, c.want)
		}
	}
	if _, err := os.Stat(out); err == nil {
		t.Error("a failed run left the earlier CSS in place")
	}
}
