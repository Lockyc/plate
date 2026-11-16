package walkthrough

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func write(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "walk.toml")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoadRejects(t *testing.T) {
	cases := []struct{ name, body, want string }{
		{"unknown action", "version=1\nname=\"w\"\n[[step]]\naction=\"hover\"\n", "step[0].action"},
		{"click no selector", "version=1\nname=\"w\"\n[[step]]\naction=\"goto\"\nurl=\"/\"\n[[step]]\naction=\"goto\"\nurl=\"/\"\n[[step]]\naction=\"click\"\n", "step[2].selector"},
		{"goto no url", "version=1\nname=\"w\"\n[[step]]\naction=\"goto\"\n", "step[0].url"},
		{"bad key", "version=1\nname=\"w\"\n[[step]]\naction=\"press\"\nkey=\"f13\"\n", "step[0].key"},
		{"missing audio", "version=1\nname=\"w\"\n[[step]]\naction=\"goto\"\nurl=\"/\"\naudio=\"nope.wav\"\n", "step[0].audio"},
		{"bad version", "version=2\nname=\"w\"\n[[step]]\naction=\"goto\"\nurl=\"/\"\n", "version"},
		{"no steps", "version=1\nname=\"w\"\n", "step"},
		{"type no text", "version=1\nname=\"w\"\n[[step]]\naction=\"type\"\nselector=\"#a\"\n", "step[0].text"},
		{"odd width", "version=1\nname=\"w\"\nwidth=641\n[[step]]\naction=\"goto\"\nurl=\"/\"\n", "width: must be even"},
		{"odd height", "version=1\nname=\"w\"\nheight=401\n[[step]]\naction=\"goto\"\nurl=\"/\"\n", "height: must be even"},
		{"unknown key", "version=1\nname=\"w\"\nbogus=1\n[[step]]\naction=\"goto\"\nurl=\"/\"\n", "bogus"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := Load(write(t, c.body))
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("want error containing %q, got %v", c.want, err)
			}
		})
	}
}

func TestLoadDefaults(t *testing.T) {
	s, err := Load(write(t, "version=1\nname=\"w\"\n[[step]]\naction=\"wait_for\"\nselector=\"#a\"\n"))
	if err != nil {
		t.Fatal(err)
	}
	if s.Width != 1440 || s.Height != 900 || *s.FPS != 30 || *s.Steps[0].TimeoutMS != 10000 {
		t.Fatalf("defaults wrong: %+v", s)
	}
}
