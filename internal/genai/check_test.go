package genai

import (
	"strings"
	"testing"

	"github.com/lockyc/plate/internal/pins"
)

func TestCheck(t *testing.T) {
	model := func(provider, id string) pins.ImageModel {
		m, err := pins.ImageModelFor(provider, id)
		if err != nil {
			t.Fatal(err)
		}
		return m
	}
	google := model("google", "")
	bfl := model("bfl", "")
	for _, c := range []struct {
		name string
		m    pins.ImageModel
		n    int
		refs int
		size string
		want string // "" when the request is fine
	}{
		{"fine", google, 4, 1, "2K", ""},
		{"no attempts", google, 0, 1, "1K", "at least 1"},
		{"negative attempts", google, -1, 1, "1K", "at least 1"},
		{"too many refs", bfl, 1, bfl.MaxRefs + 1, "1K", "at most"},
		{"refs at the limit", bfl, 1, bfl.MaxRefs, "1K", ""},
		{"unknown size", google, 1, 1, "8K", `"8K"`},
		{"empty size", google, 1, 1, "", `(sizes: 1K, 2K, 4K)`},
		// FLUX.2 makes at most 4 MP, so 4K would be 2K at a 4K price.
		{"bfl 4K", bfl, 1, 1, "4K", "1K, 2K"},
		{"openai 4K", model("openai", ""), 1, 1, "4K", "1K, 2K"},
		{"openrouter flux 4K", model("openrouter", "black-forest-labs/flux.2-pro"), 1, 1, "4K", "1K, 2K"},
		{"openrouter gemini pro 512", model("openrouter", "google/gemini-3-pro-image"), 1, 1, "512", "1K, 2K, 4K"},
		{"openrouter gemini flash 512", model("openrouter", "google/gemini-3.1-flash-image"), 1, 1, "512", ""},
	} {
		err := Check(c.m, c.n, c.refs, c.size)
		switch {
		case c.want == "" && err != nil:
			t.Errorf("%s: %v", c.name, err)
		case c.want != "" && (err == nil || !strings.Contains(err.Error(), c.want)):
			t.Errorf("%s: err %v, want one naming %q", c.name, err, c.want)
		}
	}
}
