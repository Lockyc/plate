package pins

import (
	"regexp"
	"runtime"
	"strings"
	"testing"
)

func TestAtLeast(t *testing.T) {
	cases := []struct {
		have, min string
		want      bool
	}{
		{"7.1.2-31", "7.1.0", true},
		{"7.0.11-2", "7.1.0", false},
		{"10.08.0", "10.0.0", true},
		{"9.56.1", "10.0.0", false},
		{"26.09.0", "22.0.0", true},
		{"4.1.1", "4.1.1", true},
		{"0.12.19", "0.12.0", true},
		{"0.11.9", "0.12.0", false},
		{"6.4", "5.9", true},
		{"9.0.2", "7.1", true},
		{"7.0.2", "7.1", false},
	}
	for _, c := range cases {
		if got := AtLeast(c.have, c.min); got != c.want {
			t.Errorf("AtLeast(%q, %q) = %v, want %v", c.have, c.min, got, c.want)
		}
	}
}

func TestTableIsWellFormed(t *testing.T) {
	seen := map[string]bool{}
	hex64 := regexp.MustCompile(`^[0-9a-f]{64}$`)
	for _, e := range Engines {
		if seen[e.Name] {
			t.Errorf("%s: listed twice", e.Name)
		}
		seen[e.Name] = true
		for _, c := range e.Capabilities {
			if len(c.Args) == 0 || c.Token == "" || c.For == "" {
				t.Errorf("%s: capability %+v needs Args, Token and For", e.Name, c)
			}
		}
		if len(e.UsedBy) == 0 {
			t.Errorf("%s: UsedBy is empty; an engine nothing uses does not belong in the table", e.Name)
		}
		switch e.Kind {
		case Minimum:
			re, err := regexp.Compile(e.VersionRe)
			if err != nil || re.NumSubexp() != 1 {
				t.Errorf("%s: VersionRe must compile with exactly one group", e.Name)
			}
			if e.Min == "" || len(e.VersionArgs) == 0 {
				t.Errorf("%s: Minimum engine needs Min and VersionArgs", e.Name)
			}
			for _, goos := range []string{"darwin", "linux"} {
				if e.supportedOn(goos) && e.Install[goos] == "" {
					t.Errorf("%s: no install command for %s", e.Name, goos)
				}
			}
		case Managed:
			if e.Download == nil || e.Download.Version == "" {
				t.Fatalf("%s: Managed engine needs a Download with a Version", e.Name)
			}
			for _, plat := range []string{"darwin/arm64", "darwin/amd64", "linux/amd64"} {
				a, ok := e.Download.Assets[plat]
				if !ok {
					t.Errorf("%s: no asset for %s", e.Name, plat)
					continue
				}
				if !hex64.MatchString(a.SHA256) || a.URL == "" || a.Bin == "" {
					t.Errorf("%s %s: asset needs URL, Bin and a 64-hex SHA256", e.Name, plat)
				}
			}
		}
	}
	for _, name := range []string{"magick", "gs", "pdfinfo", "pdffonts", "pdfimages", "pdftotext", "pdftoppm", "pandoc", "hb-subset", "qpdf", "qrencode", "uv", "swiftc", "chrome-headless-shell", "ffmpeg", "ffprobe"} {
		if _, ok := Lookup(name); !ok {
			t.Errorf("Lookup(%q) failed", name)
		}
	}
}

func TestHint(t *testing.T) {
	here := runtime.GOOS + "/" + runtime.GOARCH
	m := Engine{Name: "x", Kind: Managed, Download: &Download{Version: "1", Assets: map[string]Asset{here: {}}}}
	if got := m.Hint(); got != "run `plate doctor --install`" {
		t.Errorf("managed hint = %q", got)
	}
	m.Download.Assets = nil
	if got := m.Hint(); got != "no build for this platform" {
		t.Errorf("managed hint without an asset = %q", got)
	}
	s := Engine{Name: "y", Kind: Minimum, GOOS: []string{"plan9"}}
	if s.Supported() {
		t.Error("plan9-only engine reported supported")
	}
}

func TestMLPins(t *testing.T) {
	for name, m := range map[string]Model{"ViTMatte": ViTMatte, "DAT": DAT} {
		if !regexp.MustCompile(`^[0-9a-f]{40}$`).MatchString(m.Revision) {
			t.Errorf("%s revision %q is not a commit sha", name, m.Revision)
		}
	}
	if IOPaint.Version == "" || IOPaint.ExcludeNewer == "" {
		t.Error("IOPaint needs a version and an exclude-newer date")
	}
}

func TestImageModelsHaveAKeyAndAPrice(t *testing.T) {
	if len(ImageModels) == 0 {
		t.Fatal("no image models")
	}
	for _, m := range ImageModels {
		if m.KeyEnv == "" || m.Provider == "" || m.ID == "" {
			t.Errorf("%+v: provider, id and key variable are required", m)
		}
		if m.MaxRefs <= 0 {
			t.Errorf("%s/%s has MaxRefs %d, want > 0", m.Provider, m.ID, m.MaxRefs)
		}
		shapes := 0
		if len(m.PerImage) > 0 {
			shapes++
		}
		if m.InPerMTok > 0 || m.OutPerMTok > 0 {
			shapes++
		}
		if m.FirstMP > 0 || m.ExtraMP > 0 || m.RefMP > 0 {
			shapes++
		}
		if shapes != 1 {
			t.Errorf("%s/%s fills %d billing shapes, want 1", m.Provider, m.ID, shapes)
		}
	}
}

func TestImageModelFor(t *testing.T) {
	d, err := ImageModelFor("google", "")
	if err != nil || d.ID != ImageModels[0].ID {
		t.Fatalf("default: %+v, %v", d, err)
	}
	m, err := ImageModelFor("google", "gemini-3-pro-image")
	if err != nil || m.ID != "gemini-3-pro-image" {
		t.Fatalf("named: %+v, %v", m, err)
	}
	_, err = ImageModelFor("google", "nope")
	if err == nil || !strings.Contains(err.Error(), "gemini-3-pro-image") {
		t.Fatalf("unknown id error should list IDs: %v", err)
	}
	if _, err := ImageModelFor("nobody", ""); err == nil {
		t.Fatal("unknown provider should fail")
	}
}

func TestKeyUnset(t *testing.T) {
	m := ImageModel{Provider: "openai", KeyEnv: "PLATE_TEST_KEY"}
	t.Setenv("PLATE_TEST_KEY", "")
	_, err := m.Key()
	if err == nil || err.Error() != "PLATE_TEST_KEY is not set; openai needs an API key" {
		t.Fatalf("got %v", err)
	}
	t.Setenv("PLATE_TEST_KEY", "sk-secret")
	if k, err := m.Key(); err != nil || k != "sk-secret" {
		t.Fatalf("got %q, %v", k, err)
	}
}
