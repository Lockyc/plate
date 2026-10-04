package fonts

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/lockyc/plate/internal/engine"
)

// Variant names one face of a family the way Google Fonts' css2 API takes
// it: a weight on the wght axis and upright or italic on the ital axis.
type Variant struct {
	Family string
	Weight int
	Italic bool
}

func (v Variant) String() string {
	style := "normal"
	if v.Italic {
		style = "italic"
	}
	return fmt.Sprintf("%s %d %s", v.Family, v.Weight, style)
}

// ErrNotServed means Google Fonts has no such face: the family is not one
// it serves (a system face such as Arial), or the family has no such weight
// or style.
var ErrNotServed = errors.New("not served by Google Fonts")

// googleCSS is the css2 endpoint. To a user agent it does not recognise as
// a browser, it answers with one @font-face per request, whose src is the
// whole face as TrueType, which hb-subset can cut.
var googleCSS = "https://fonts.googleapis.com/css2"

var srcRe = regexp.MustCompile(`src:\s*url\(([^)]+)\)\s*format\('truetype'\)`)

// parseVariant reads FAMILY:WEIGHT:STYLE, splitting from the right.
func parseVariant(s string) (Variant, error) {
	parts := strings.Split(s, ":")
	if len(parts) < 3 {
		return Variant{}, fmt.Errorf("%q: want FAMILY:WEIGHT:STYLE", s)
	}
	n := len(parts)
	f := Face{File: "x.ttf", Family: strings.Join(parts[:n-2], ":"), Weight: parts[n-2], Style: parts[n-1]}
	if err := f.check(); err != nil {
		return Variant{}, fmt.Errorf("%q: %w", s, err)
	}
	w, err := strconv.Atoi(f.Weight)
	if err != nil || f.Style == "oblique" {
		return Variant{}, fmt.Errorf("%q: Google Fonts takes a numeric weight and a style of normal or italic", s)
	}
	return Variant{Family: f.Family, Weight: w, Italic: f.Style == "italic"}, nil
}

// Google returns v as a Face whose file is a TrueType font in plate's
// cache, fetching it from Google Fonts the first time. Google serves the
// families Google Docs and Slides offer, including faces such as Calibri
// that are not in its public catalogue; a face it does not serve is
// ErrNotServed.
func Google(ctx context.Context, v Variant) (Face, error) {
	style := "normal"
	if v.Italic {
		style = "italic"
	}
	face := Face{Family: v.Family, Weight: strconv.Itoa(v.Weight), Style: style}
	if err := face.check(); err != nil {
		return Face{}, fmt.Errorf("%s: %w", v, err)
	}
	cache, err := engine.CacheDir()
	if err != nil {
		return Face{}, err
	}
	dir := filepath.Join(cache, "google-fonts")
	face.File = filepath.Join(dir, fmt.Sprintf("%s-%d-%s.ttf", strings.ReplaceAll(v.Family, " ", ""), v.Weight, style))
	if fi, err := os.Stat(face.File); err == nil && fi.Size() > 0 {
		return face, nil
	}
	ital := 0
	if v.Italic {
		ital = 1
	}
	q := url.Values{"family": {fmt.Sprintf("%s:ital,wght@%d,%d", v.Family, ital, v.Weight)}}
	css, err := fetch(ctx, googleCSS+"?"+q.Encode())
	if errors.Is(err, errBadRequest) {
		return Face{}, fmt.Errorf("%s: %w", v, ErrNotServed)
	}
	if err != nil {
		return Face{}, fmt.Errorf("%s: %w", v, err)
	}
	m := srcRe.FindAllSubmatch(css, -1)
	if len(m) != 1 {
		return Face{}, fmt.Errorf("%s: Google Fonts answered with %d TrueType sources, want 1", v, len(m))
	}
	data, err := fetch(ctx, string(m[0][1]))
	if err != nil {
		return Face{}, fmt.Errorf("%s: %w", v, err)
	}
	if len(data) == 0 {
		return Face{}, fmt.Errorf("%s: Google Fonts sent an empty font", v)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return Face{}, err
	}
	if err := writeAtomic(face.File, data); err != nil {
		return Face{}, err
	}
	return face, nil
}

var errBadRequest = errors.New("400 Bad Request")

// fetch GETs u and returns its body, or errBadRequest for a 400, which is
// how css2 says it has no such family or face.
func fetch(ctx context.Context, u string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "plate")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
		return io.ReadAll(resp.Body)
	case http.StatusBadRequest:
		return nil, errBadRequest
	}
	return nil, fmt.Errorf("GET %s: %s", u, resp.Status)
}
