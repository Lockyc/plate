// Package slides previews a Google Slides deck from its Slides API JSON
// (presentations.get), for a deck Drive will not export: each slide becomes
// an HTML page at 1 pt = 1 CSS px, rendered to PNG by render.PNG.
//
// plate never calls Google's APIs; the caller fetches the JSON. It does
// fetch what the JSON points at: the pictures (contentUrl, which expires
// about 30 minutes after the JSON was fetched) and every face the drawn
// text names, through fonts.Google, so text is measured in the deck's own
// faces and wraps where Slides wraps it.
//
// A slide's text inherits from its layout's and master's placeholders,
// level by level. Lines, tables, videos, charts and word art are not drawn,
// and the command says which slides had them; bullets, links and autofit
// are not drawn either.
package slides

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/lockyc/plate/internal/cli"
	"github.com/lockyc/plate/internal/fonts"
	"github.com/lockyc/plate/internal/render"
)

const usage = "slides [--pages LIST] [--out DIR] [--scale S] <deck.json|->"

// Main runs `plate slides`.
func Main(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := cli.Flags("slides", usage, stderr)
	pages := fs.String("pages", "", "only these slides: a comma-separated list of N, N-M (1-based) or slide object IDs")
	out := fs.String("out", "", "write the pages here, creating it if missing (default: a fresh temporary directory)")
	scale := cli.Float(fs, "scale", 1, "device scale factor; each PNG is the page size in pt × scale px")
	rest, code, ok := cli.Parse(fs, args, 1)
	if !ok {
		return code
	}
	if !(*scale > 0) {
		return cli.Usage(stderr, "slides", "--scale must be above 0")
	}
	deck, err := readDeck(rest[0])
	if err != nil {
		return cli.Fail(stderr, "slides", err)
	}
	picked, err := pick(deck, *pages)
	if err != nil {
		return cli.Usage(stderr, "slides", "--pages: %v", err)
	}
	paths, err := run(ctx, deck, picked, *out, *scale, stderr)
	if err != nil {
		return cli.Fail(stderr, "slides", err)
	}
	fmt.Fprintln(stdout, strings.Join(paths, "\n"))
	return 0
}

func readDeck(path string) (*Deck, error) {
	var data []byte
	var err error
	if path == "-" {
		data, err = io.ReadAll(os.Stdin)
	} else {
		data, err = os.ReadFile(path)
	}
	if err != nil {
		return nil, err
	}
	var d Deck
	if err := json.Unmarshal(data, &d); err != nil {
		return nil, fmt.Errorf("%s: not Slides API presentation JSON: %w", path, err)
	}
	if len(d.Slides) == 0 || d.PageSize.Width == nil || d.PageSize.Height == nil {
		return nil, fmt.Errorf("%s: no slides or no page size; give the JSON presentations.get returns", path)
	}
	return &d, nil
}

// pick returns the 0-based indexes of the slides list names, in deck
// order; every slide when list is "".
func pick(d *Deck, list string) ([]int, error) {
	want := make([]bool, len(d.Slides))
	if list == "" {
		for i := range want {
			want[i] = true
		}
	}
	ids := map[string]int{}
	for i, s := range d.Slides {
		ids[s.ObjectID] = i
	}
	for _, item := range strings.Split(list, ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			if list != "" {
				return nil, errors.New("an empty item")
			}
			continue
		}
		if i, ok := ids[item]; ok {
			want[i] = true
			continue
		}
		a, b, isRange := strings.Cut(item, "-")
		if !isRange {
			b = a
		}
		first, errA := strconv.Atoi(a)
		last, errB := strconv.Atoi(b)
		if errA != nil || errB != nil {
			return nil, fmt.Errorf("%q is neither a slide number, a range nor a slide's object ID", item)
		}
		if first < 1 || first > last || last > len(d.Slides) {
			return nil, fmt.Errorf("%q: the deck has slides 1-%d", item, len(d.Slides))
		}
		for i := first - 1; i < last; i++ {
			want[i] = true
		}
	}
	var out []int
	for i, w := range want {
		if w {
			out = append(out, i)
		}
	}
	return out, nil
}

// fontsName and imgDir are the files every page in --out shares.
const (
	fontsName = "fonts.css"
	imgDir    = "img"
)

func run(ctx context.Context, d *Deck, picked []int, out string, scale float64, warn io.Writer) (paths []string, err error) {
	made := false
	if out == "" {
		if out, err = os.MkdirTemp("", "plate-slides-"); err != nil {
			return nil, err
		}
		made = true
	} else if err := os.MkdirAll(out, 0o755); err != nil {
		return nil, err
	}
	// created lists, in order, what this run wrote into a caller's --out; a
	// failed run removes it, newest first, so the same --out takes a retry.
	var created []string
	defer func() {
		if err == nil {
			return
		}
		if made {
			os.RemoveAll(out)
			return
		}
		for _, p := range slices.Backward(created) {
			os.Remove(p)
		}
	}()
	digits := max(2, len(strconv.Itoa(len(d.Slides))))
	name := func(i int) string { return fmt.Sprintf("s%0*d", digits, i+1) }
	names := []string{fontsName}
	for _, i := range picked {
		names = append(names, name(i)+".html", name(i)+".png")
	}
	for _, n := range names {
		if _, err := os.Lstat(filepath.Join(out, n)); err == nil {
			return nil, fmt.Errorf("%s already holds %s; pass a fresh --out (nothing was deleted)", out, n)
		}
	}

	w, h := d.PageSize.Width.pt(0), d.PageSize.Height.pt(0)
	b := newBuilder(d)
	html := map[int]string{}
	for _, i := range picked {
		b.undrawn = nil
		html[i] = b.page(&d.Slides[i], w, h, fontsName)
		if len(b.undrawn) > 0 {
			var kinds []string
			for _, k := range slices.Sorted(maps.Keys(b.undrawn)) {
				kinds = append(kinds, fmt.Sprintf("%d %s", b.undrawn[k], k))
			}
			fmt.Fprintf(warn, "plate slides: slide %d (%s): not drawn: %s\n", i+1, d.Slides[i].ObjectID, strings.Join(kinds, ", "))
		}
	}
	css, err := faces(ctx, b.variants, warn)
	if err != nil {
		return nil, err
	}
	created = append(created, filepath.Join(out, fontsName))
	if err := os.WriteFile(created[0], []byte(css), 0o644); err != nil {
		return nil, err
	}
	if err := fetchImages(ctx, b.images, out, &created); err != nil {
		return nil, err
	}
	pw, ph := int(math.Round(w)), int(math.Round(h))
	for _, i := range picked {
		page := filepath.Join(out, name(i)+".html")
		png := filepath.Join(out, name(i)+".png")
		created = append(created, page, png)
		if err := os.WriteFile(page, []byte(html[i]), 0o644); err != nil {
			return nil, err
		}
		if err := render.PNG(ctx, page, png, pw, ph, scale); err != nil {
			return nil, fmt.Errorf("slide %d: %w", i+1, err)
		}
		paths = append(paths, png)
	}
	return paths, nil
}

// faces fetches every variant the pages use and returns their stylesheet.
// A face Google Fonts does not serve is left to Chrome, which draws a
// system face of that name or a fallback; the caller is told which.
func faces(ctx context.Context, variants map[fonts.Variant]bool, warn io.Writer) (string, error) {
	vs := slices.SortedFunc(maps.Keys(variants), func(a, b fonts.Variant) int {
		return strings.Compare(a.String(), b.String())
	})
	var got []fonts.Face
	served := map[string]bool{}
	var missing []fonts.Variant
	for _, v := range vs {
		f, err := fonts.Google(ctx, v)
		if errors.Is(err, fonts.ErrNotServed) {
			missing = append(missing, v)
			continue
		}
		if err != nil {
			return "", err
		}
		served[v.Family] = true
		got = append(got, f)
	}
	told := map[string]bool{}
	for _, v := range missing {
		switch {
		case !served[v.Family] && !told[v.Family]:
			told[v.Family] = true
			fmt.Fprintf(warn, "plate slides: Google Fonts does not serve %s; it is drawn in this machine's face of that name, or a fallback\n", v.Family)
		case served[v.Family]:
			fmt.Fprintf(warn, "plate slides: Google Fonts does not serve %s; Chrome synthesises it from the family's other faces\n", v)
		}
	}
	return fonts.CSS(ctx, got, "block", "")
}

// imageName is the local name of a picture: its URL less the query, hashed.
func imageName(url string) string {
	base, _, _ := strings.Cut(url, "?")
	sum := sha256.Sum256([]byte(base))
	return imgDir + "/" + hex.EncodeToString(sum[:])[:16]
}

// fetchImages downloads each picture once into out, skipping one already
// there, and appends to created each path it makes.
func fetchImages(ctx context.Context, images map[string]string, out string, created *[]string) error {
	if len(images) == 0 {
		return nil
	}
	dir := filepath.Join(out, imgDir)
	if _, err := os.Lstat(dir); errors.Is(err, fs.ErrNotExist) {
		if err := os.Mkdir(dir, 0o755); err != nil {
			return err
		}
		*created = append(*created, dir)
	}
	for _, u := range slices.Sorted(maps.Keys(images)) {
		p := filepath.Join(out, images[u])
		if fi, err := os.Stat(p); err == nil && fi.Size() > 0 {
			continue
		}
		data, err := get(ctx, u)
		if err != nil {
			return fmt.Errorf("fetching a picture (contentUrl expires about 30 minutes after the JSON was fetched; fetch it again): %w", err)
		}
		*created = append(*created, p)
		if err := os.WriteFile(p, data, 0o644); err != nil {
			return err
		}
	}
	return nil
}

func get(ctx context.Context, u string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s", resp.Status)
	}
	data, err := io.ReadAll(resp.Body)
	if err == nil && len(bytes.TrimSpace(data)) == 0 {
		err = errors.New("an empty picture")
	}
	return data, err
}
