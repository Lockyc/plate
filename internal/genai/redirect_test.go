package genai

import (
	"net/http"
	"testing"
)

func TestRedirectKeepsTheKeyOnItsOrigin(t *testing.T) {
	get := func(url string, key bool) *http.Request {
		r, err := http.NewRequest(http.MethodGet, url, nil)
		if err != nil {
			t.Fatal(err)
		}
		if key {
			r.Header.Set("x-key", testKey)
		}
		return r
	}
	for _, c := range []struct {
		name, from, to string
		key, refused   bool
	}{
		{"same origin", "https://api.example/a", "https://api.example/b", true, false},
		{"another host", "https://api.example/a", "https://cdn.example/b", true, true},
		{"https to http on the same host", "https://api.example/a", "http://api.example/b", true, true},
		{"keyless to another host", "https://api.example/a", "https://cdn.example/b", false, false},
		{"keyless to http", "https://api.example/a", "http://api.example/b", false, false},
	} {
		err := checkRedirect(get(c.to, false), []*http.Request{get(c.from, c.key)})
		if (err != nil) != c.refused {
			t.Errorf("%s: err %v, refused want %v", c.name, err, c.refused)
		}
	}
}
