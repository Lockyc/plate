package slides

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/lockyc/plate/internal/enginetest"
	"github.com/lockyc/plate/internal/fonts"
)

// deckJSON is a two-slide deck in the shape presentations.get returns: a
// master whose title placeholder sets Body Face 12pt and a theme colour, a
// layout placeholder between it and the slide, a background the slides
// inherit, and a slide with a grouped, rotated picture.
const deckJSON = `{
  "pageSize": {"width": {"magnitude": 9144000, "unit": "EMU"}, "height": {"magnitude": 5143500, "unit": "EMU"}},
  "masters": [{
    "objectId": "m",
    "pageProperties": {
      "pageBackgroundFill": {"solidFill": {"color": {"rgbColor": {"red": 1, "green": 0.5}}}},
      "colorScheme": {"colors": [{"type": "DARK1", "color": {}}, {"type": "ACCENT1", "color": {"blue": 1}}]}
    },
    "pageElements": [{"objectId": "m_title", "shape": {"shapeType": "TEXT_BOX", "placeholder": {"type": "TITLE"},
      "shapeProperties": {"contentAlignment": "MIDDLE"},
      "text": {"textElements": [
        {"paragraphMarker": {"style": {"lineSpacing": 90, "alignment": "CENTER"}}},
        {"textRun": {"content": "\n", "style": {"fontFamily": "Body Face", "weightedFontFamily": {"fontFamily": "Body Face", "weight": 400},
          "fontSize": {"magnitude": 12, "unit": "PT"}, "foregroundColor": {"opaqueColor": {"themeColor": "ACCENT1"}}}}}
      ]}}}]
  }],
  "layouts": [{
    "objectId": "l", "layoutProperties": {"masterObjectId": "m"},
    "pageProperties": {"pageBackgroundFill": {"propertyState": "INHERIT"}},
    "pageElements": [{"objectId": "l_title", "shape": {"shapeType": "TEXT_BOX", "placeholder": {"type": "TITLE", "parentObjectId": "m_title"},
      "text": {"textElements": [
        {"paragraphMarker": {"style": {}}},
        {"textRun": {"content": "\n", "style": {"fontSize": {"magnitude": 30, "unit": "PT"}}}}
      ]}}}]
  }],
  "slides": [{
    "objectId": "first",
    "slideProperties": {"layoutObjectId": "l", "masterObjectId": "m"},
    "pageProperties": {"pageBackgroundFill": {"propertyState": "INHERIT"}},
    "pageElements": [
      {"objectId": "title", "size": {"width": {"magnitude": 2540000, "unit": "EMU"}, "height": {"magnitude": 1270000, "unit": "EMU"}},
       "transform": {"scaleX": 1, "scaleY": 1, "translateX": 127000, "translateY": 254000, "unit": "EMU"},
       "shape": {"shapeType": "ROUND_RECTANGLE", "placeholder": {"type": "TITLE", "parentObjectId": "l_title"},
        "shapeProperties": {"shapeBackgroundFill": {"solidFill": {"color": {"rgbColor": {"green": 1}}, "alpha": 0.5}}},
        "text": {"textElements": [
          {"paragraphMarker": {"style": {}}},
          {"textRun": {"content": "Hello <world>", "style": {"bold": true}}},
          {"textRun": {"content": " light\u000bnext\n", "style": {"fontFamily": "Light Face", "weightedFontFamily": {"fontFamily": "Light Face", "weight": 300}, "italic": true}}},
          {"paragraphMarker": {"style": {}}},
          {"textRun": {"content": "\n", "style": {"fontSize": {"magnitude": 6, "unit": "PT"}}}}
        ]}}},
      {"objectId": "group", "transform": {"scaleX": 1, "scaleY": 1, "translateX": 1270000, "unit": "EMU"},
       "elementGroup": {"children": [
         {"objectId": "pic", "size": {"width": {"magnitude": 100, "unit": "PT"}, "height": {"magnitude": 50, "unit": "PT"}},
          "transform": {"scaleX": 0, "shearX": -2, "shearY": 2, "scaleY": 0, "unit": "PT"},
          "image": {"contentUrl": "IMAGE_URL?key=abc"}}
       ]}},
      {"objectId": "rule", "line": {"lineType": "STRAIGHT"}}
    ]
  }, {
    "objectId": "second",
    "pageProperties": {"pageBackgroundFill": {"solidFill": {"color": {"themeColor": "DARK1"}}}},
    "slideProperties": {"layoutObjectId": "l", "masterObjectId": "m"},
    "pageElements": []
  }]
}`

func parse(t *testing.T, src string) *Deck {
	t.Helper()
	var d Deck
	if err := json.Unmarshal([]byte(src), &d); err != nil {
		t.Fatal(err)
	}
	return &d
}

func TestPage(t *testing.T) {
	d := parse(t, deckJSON)
	b := newBuilder(d)
	got := b.page(&d.Slides[0], 720, 405, "fonts.css")
	for _, want := range []string{
		`<link rel=stylesheet href="fonts.css">`,
		"width:720px;height:405px",
		// The master's background, through the layout's INHERIT.
		"background:rgb(255,128,0);",
		// 0.1in / 0.2in in pt, at 1pt = 1px; the fill keeps its alpha.
		"transform:matrix(1,0,0,1,10,20);",
		"background:rgba(0,255,0,0.5);",
		"border-radius:16px;",
		// The master's alignment and paragraph style reach the slide.
		"justify-content:center;",
		"text-align:center;line-height:1.08;",
		// Bold over the inherited 400 is 700, in the master's face, at the
		// layout's size, in the master's theme colour.
		`<span style="font-family:&#34;Body Face&#34;;font-size:30px;font-weight:700;color:rgb(0,0,255);">Hello &lt;world&gt;</span>`,
		// Bold is inherited from nowhere here: a 300 weight stays 300.
		`font-family:&#34;Light Face&#34;;font-size:30px;font-weight:300;font-style:italic;`,
		" light\nnext</span>",
		// An empty paragraph keeps its own, smaller line.
		"font-size:6px;font-weight:400;\">&#8203;</p>",
		// The group's translate composes with the picture's rotation.
		"transform:matrix(0,1,-1,0,100,0);",
		`<img src="img/`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("page lacks %s\n%s", want, got)
		}
	}
	if strings.Contains(got, "rule") {
		t.Error("the line was drawn")
	}
	if b.undrawn["line"] != 1 {
		t.Errorf("undrawn %v, want one line", b.undrawn)
	}
	vs := slices.SortedFunc(func(yield func(fonts.Variant) bool) {
		for v := range b.variants {
			if !yield(v) {
				return
			}
		}
	}, func(a, b fonts.Variant) int { return strings.Compare(a.String(), b.String()) })
	want := []fonts.Variant{{Family: "Body Face", Weight: 400}, {Family: "Body Face", Weight: 700}, {Family: "Light Face", Weight: 300, Italic: true}}
	if !slices.Equal(vs, want) {
		t.Errorf("variants %v, want %v", vs, want)
	}
	if len(b.images) != 1 || b.images["IMAGE_URL?key=abc"] != imageName("IMAGE_URL?other") {
		t.Errorf("images %v: a picture is named by its URL less the query", b.images)
	}

	second := b.page(&d.Slides[1], 720, 405, "fonts.css")
	if !strings.Contains(second, "background:rgb(0,0,0);") {
		t.Errorf("a theme-coloured background, DARK1 with no channels, is black:\n%s", second)
	}
}

func TestWeight(t *testing.T) {
	yes, no := true, false
	for _, c := range []struct {
		weight int
		bold   *bool
		want   int
	}{{0, nil, 400}, {0, &yes, 700}, {300, &yes, 400}, {500, &yes, 700}, {800, &yes, 800}, {300, &no, 300}} {
		s := TextStyle{Bold: c.bold}
		if c.weight > 0 {
			s.WeightedFontFamily = &struct {
				FontFamily string `json:"fontFamily"`
				Weight     int    `json:"weight"`
			}{"F", c.weight}
		}
		if got := s.weight(); got != c.want {
			t.Errorf("weight %d bold %v: %d, want %d", c.weight, c.bold, got, c.want)
		}
	}
}

func TestPick(t *testing.T) {
	d := &Deck{Slides: make([]Page, 5)}
	for i, id := range []string{"a", "b", "c", "d", "e"} {
		d.Slides[i].ObjectID = id
	}
	for _, c := range []struct {
		list string
		want []int
	}{{"", []int{0, 1, 2, 3, 4}}, {"2", []int{1}}, {"4-5,1", []int{0, 3, 4}}, {"c, 3", []int{2}}, {"e,a", []int{0, 4}}} {
		got, err := pick(d, c.list)
		if err != nil || !slices.Equal(got, c.want) {
			t.Errorf("%q: %v %v, want %v", c.list, got, err, c.want)
		}
	}
	for _, bad := range []string{"0", "6", "3-2", "x", "1,,2", "2-"} {
		if _, err := pick(d, bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

const chromeStub = `for a in "$@"; do
  case "$a" in --screenshot=*) cp "$FIXTURE_PNG" "${a#--screenshot=}" ;; esac
done`

// commandSetup stubs Chrome and fills plate's font cache, so nothing asks
// Google, and writes deckJSON with its picture served by pic. It returns the
// temporary directory, the deck and the Chrome call log.
func commandSetup(t *testing.T, pic http.HandlerFunc) (dir, deck, chrome string) {
	t.Helper()
	dir = t.TempDir()
	fix := filepath.Join(dir, "fixture.png")
	f, _ := os.Create(fix)
	png.Encode(f, image.NewRGBA(image.Rect(0, 0, 720, 405)))
	f.Close()
	t.Setenv("FIXTURE_PNG", fix)
	chrome = enginetest.Stub(t, "chrome-headless-shell", chromeStub)
	cache := filepath.Join(dir, "cache")
	t.Setenv("XDG_CACHE_HOME", cache)
	os.MkdirAll(filepath.Join(cache, "plate", "google-fonts"), 0o755)
	for _, n := range []string{"BodyFace-400-normal.ttf", "BodyFace-700-normal.ttf", "LightFace-300-italic.ttf"} {
		os.WriteFile(filepath.Join(cache, "plate", "google-fonts", n), []byte("TTF"), 0o644)
	}
	srv := httptest.NewServer(pic)
	t.Cleanup(srv.Close)
	deck = filepath.Join(dir, "deck.json")
	os.WriteFile(deck, []byte(strings.ReplaceAll(deckJSON, "IMAGE_URL", srv.URL+"/pic")), 0o644)
	return dir, deck, chrome
}

func TestCommand(t *testing.T) {
	pics := 0
	dir, deck, chrome := commandSetup(t, func(w http.ResponseWriter, r *http.Request) {
		pics++
		w.Write([]byte("PICTURE"))
	})

	out := filepath.Join(dir, "out")
	var o, e bytes.Buffer
	if code := Main(context.Background(), []string{"--pages", "first", "--out", out, deck}, &o, &e); code != 0 {
		t.Fatalf("code %d: %s", code, e.String())
	}
	if o.String() != filepath.Join(out, "s01.png")+"\n" {
		t.Errorf("stdout %q", o.String())
	}
	if !strings.Contains(e.String(), "slide 1 (first): not drawn: 1 line") {
		t.Errorf("stderr %q does not name the undrawn line", e.String())
	}
	css, _ := os.ReadFile(filepath.Join(out, "fonts.css"))
	if strings.Count(string(css), "@font-face") != 3 || !strings.Contains(string(css), `font-family: "Light Face";`) {
		t.Errorf("fonts.css:\n%s", css)
	}
	page, _ := os.ReadFile(filepath.Join(out, "s01.html"))
	m := strings.Index(string(page), `<img src="`)
	name := string(page[m+len(`<img src="`):])
	name = name[:strings.Index(name, `"`)]
	if b, _ := os.ReadFile(filepath.Join(out, name)); string(b) != "PICTURE" || pics != 1 {
		t.Errorf("picture %s: %q after %d fetches", name, b, pics)
	}
	if calls := enginetest.Calls(t, chrome); len(calls) != 1 || !slices.Contains(calls[0], "--window-size=720,405") {
		t.Errorf("chrome calls %q", calls)
	}

	// A second run into the same directory is refused, not mixed in.
	e.Reset()
	if code := Main(context.Background(), []string{"--out", out, deck}, &o, &e); code != 1 || !strings.Contains(e.String(), "already holds") {
		t.Errorf("rerun: code %d stderr %q", code, e.String())
	}
}

func TestCommandRejects(t *testing.T) {
	dir := t.TempDir()
	bad := filepath.Join(dir, "bad.json")
	os.WriteFile(bad, []byte(`{"slides": []}`), 0o644)
	deck := filepath.Join(dir, "deck.json")
	os.WriteFile(deck, []byte(deckJSON), 0o644)
	for _, c := range []struct {
		args []string
		code int
		want string
	}{
		{[]string{bad}, 1, "no slides"},
		{[]string{"--pages", "9", deck}, 2, "slides 1-2"},
		{[]string{"--scale", "0", deck}, 2, "--scale"},
		{[]string{}, 2, "usage"},
	} {
		var o, e bytes.Buffer
		if code := Main(context.Background(), c.args, &o, &e); code != c.code || !strings.Contains(e.String(), c.want) {
			t.Errorf("%q: code %d stderr %q; want %d containing %q", c.args, code, e.String(), c.code, c.want)
		}
	}
}

// TestCommandFailureCleansOut: a run into a caller's --out that fails on an
// expired picture removes what it wrote and nothing else, so the retry the
// error asks for can use the same --out.
func TestCommandFailureCleansOut(t *testing.T) {
	expired := true
	dir, deck, _ := commandSetup(t, func(w http.ResponseWriter, r *http.Request) {
		if expired {
			http.Error(w, "expired", http.StatusForbidden)
			return
		}
		w.Write([]byte("PICTURE"))
	})

	out := filepath.Join(dir, "out")
	os.MkdirAll(out, 0o755)
	os.WriteFile(filepath.Join(out, "keep.txt"), []byte("mine"), 0o644)
	var o, e bytes.Buffer
	if code := Main(context.Background(), []string{"--out", out, deck}, &o, &e); code != 1 || !strings.Contains(e.String(), "fetch it again") {
		t.Fatalf("expired picture: code %d stderr %q", code, e.String())
	}
	entries, _ := os.ReadDir(out)
	if len(entries) != 1 || entries[0].Name() != "keep.txt" {
		var names []string
		for _, en := range entries {
			names = append(names, en.Name())
		}
		t.Errorf("--out after the failed run holds %q, want only keep.txt", names)
	}

	expired = false
	e.Reset()
	if code := Main(context.Background(), []string{"--out", out, deck}, &o, &e); code != 0 {
		t.Errorf("retry into the same --out: code %d stderr %q", code, e.String())
	}
}
