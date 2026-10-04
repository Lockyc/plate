// Package fonts writes a CSS file whose @font-face rules carry the fonts as
// base64 data: URIs, so a page renders the same opened from the filesystem,
// on any machine, with no network: Safari refuses file:// sibling
// subresources, and a data: URI has no origin to refuse. font-display is
// block by default, so a render never captures a fallback face. --subset
// cuts each font to a named set of characters with hb-subset first, so the
// CSS carries only the glyphs the page's language uses. --google takes a
// face by family name from Google Fonts (google.go) in place of a file.
package fonts

import (
	"context"
	"encoding/base64"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"unicode"

	"github.com/lockyc/plate/internal/cli"
	"github.com/lockyc/plate/internal/engine"
)

// displays are the font-display values CSS defines.
var displays = []string{"auto", "block", "swap", "fallback", "optional"}

const usage = "fonts [--display block] [--subset latin] [--google FAMILY:WEIGHT:STYLE]... -o fonts.css [FILE:FAMILY:WEIGHT:STYLE]..."

// subsets are the named character sets --subset takes, as hb-subset
// --unicodes ranges. latin is Basic Latin, Latin-1 Supplement and the
// General Punctuation English text uses (dashes, quotes, ellipsis, primes,
// guillemets), plus the euro sign.
var subsets = map[string]string{
	"latin": "20-7E,A0-FF,2010-2027,2030-203A,20AC",
}

var formats = map[string][2]string{
	".woff2": {"font/woff2", "woff2"},
	".woff":  {"font/woff", "woff"},
	".ttf":   {"font/ttf", "truetype"},
	".otf":   {"font/otf", "opentype"},
}

// Face is one @font-face: a font file and the family, weight and style a
// page asks for it by.
type Face struct{ File, Family, Weight, Style string }

// parseFace splits FILE:FAMILY:WEIGHT:STYLE from the right, so a colon in
// the file path survives.
func parseFace(s string) (Face, error) {
	parts := strings.Split(s, ":")
	n := len(parts)
	if n < 4 {
		return Face{}, fmt.Errorf("%q: want FILE:FAMILY:WEIGHT:STYLE", s)
	}
	f := Face{File: strings.Join(parts[:n-3], ":"), Family: parts[n-3], Weight: parts[n-2], Style: parts[n-1]}
	if err := f.check(); err != nil {
		return Face{}, fmt.Errorf("%q: %w", s, err)
	}
	if _, ok := formats[strings.ToLower(filepath.Ext(f.File))]; !ok {
		return Face{}, fmt.Errorf("%q: %s is not a web font; use .woff2, .woff, .ttf or .otf", s, filepath.Ext(f.File))
	}
	return f, nil
}

// check refuses a family, weight or style that would break out of the CSS
// or that CSS does not define.
func (f Face) check() error {
	if f.Family == "" || strings.ContainsAny(f.Family, `"\`) || strings.IndexFunc(f.Family, unicode.IsControl) >= 0 {
		return errors.New("family must be non-empty, without quotes, backslashes or control characters")
	}
	if w, err := strconv.Atoi(f.Weight); (err != nil || w < 1 || w > 1000) && f.Weight != "normal" && f.Weight != "bold" {
		return errors.New("weight must be 1-1000, normal or bold")
	}
	if f.Style != "normal" && f.Style != "italic" && f.Style != "oblique" {
		return errors.New("style must be normal, italic or oblique")
	}
	return nil
}

type multi []string

func (m *multi) String() string     { return strings.Join(*m, ", ") }
func (m *multi) Set(s string) error { *m = append(*m, s); return nil }

// Main runs `plate fonts`.
func Main(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := cli.Flags("fonts", usage, stderr)
	out := fs.String("o", "", "the CSS file to write (required)")
	display := fs.String("display", "block", "font-display value")
	subset := fs.String("subset", "", "cut each font to a named character set before embedding: latin (needs .ttf or .otf input)")
	var google multi
	fs.Var(&google, "google", "a face from Google Fonts by family name, `FAMILY:WEIGHT:STYLE` (repeatable; fetched once into plate's cache)")
	rest, code, ok := cli.Parse(fs, args, -1)
	if !ok {
		return code
	}
	usageErr := func(err error) int { return cli.Usage(stderr, "fonts", "%v", err) }
	if *out == "" || len(rest)+len(google) == 0 {
		return usageErr(fmt.Errorf("give -o and at least one FILE:FAMILY:WEIGHT:STYLE or --google FAMILY:WEIGHT:STYLE"))
	}
	if !slices.Contains(displays, *display) {
		return usageErr(fmt.Errorf("--display %q: use %s", *display, strings.Join(displays, ", ")))
	}
	given := false
	fs.Visit(func(f *flag.Flag) { given = given || f.Name == "subset" })
	if _, ok := subsets[*subset]; given && !ok {
		return usageErr(fmt.Errorf("--subset %q: use latin", *subset))
	}
	var faces []Face
	for _, s := range rest {
		f, err := parseFace(s)
		if err != nil {
			return usageErr(err)
		}
		if ext := strings.ToLower(filepath.Ext(f.File)); *subset != "" && ext != ".ttf" && ext != ".otf" {
			return usageErr(fmt.Errorf("%q: --subset needs .ttf or .otf input; hb-subset cannot read %s", s, ext))
		}
		if engine.SameFile(*out, f.File) {
			return usageErr(fmt.Errorf("-o is the same file as a font input: %q", f.File))
		}
		faces = append(faces, f)
	}
	var wanted []Variant
	for _, s := range google {
		v, err := parseVariant(s)
		if err != nil {
			return usageErr(fmt.Errorf("--google %w", err))
		}
		wanted = append(wanted, v)
	}
	// A failure below must not leave an earlier run's CSS looking current.
	if err := os.Remove(*out); err != nil && !errors.Is(err, os.ErrNotExist) {
		return cli.Fail(stderr, "fonts", err)
	}
	for _, v := range wanted {
		f, err := Google(ctx, v)
		if err != nil {
			return cli.Fail(stderr, "fonts", err)
		}
		faces = append(faces, f)
	}
	css, err := CSS(ctx, faces, *display, *subset)
	if err != nil {
		return cli.Fail(stderr, "fonts", err)
	}
	if err := writeAtomic(*out, []byte(css)); err != nil {
		return cli.Fail(stderr, "fonts", err)
	}
	fmt.Fprintf(stdout, "wrote %s (%d faces, %d bytes)\n", *out, len(faces), len(css))
	return 0
}

// CSS returns the stylesheet embedding faces with font-display display,
// each cut to the named subset first when subset is not "".
func CSS(ctx context.Context, faces []Face, display, subset string) (string, error) {
	unicodes, subsetting := subsets[subset]
	if subset != "" && !subsetting {
		return "", fmt.Errorf("no subset named %q", subset)
	}
	var b strings.Builder
	b.WriteString("/* Generated by plate fonts. Regenerate it rather than editing it. */\n\n")
	var tmp string
	if subsetting {
		d, err := os.MkdirTemp("", "plate-fonts-")
		if err != nil {
			return "", err
		}
		defer os.RemoveAll(d)
		tmp = d
	}
	for i, f := range faces {
		if err := f.check(); err != nil {
			return "", fmt.Errorf("%s: %w", f.File, err)
		}
		fm, ok := formats[strings.ToLower(filepath.Ext(f.File))]
		if !ok {
			return "", fmt.Errorf("%s is not a web font; use .woff2, .woff, .ttf or .otf", f.File)
		}
		src := f.File
		if subsetting {
			src = filepath.Join(tmp, strconv.Itoa(i)+filepath.Ext(f.File))
			if _, err := engine.Run(ctx, engine.Cmd{Engine: "hb-subset", Args: []string{"--unicodes=" + unicodes, "--output-file=" + src, f.File},
				Inputs: []string{f.File}, Outputs: []string{src}}); err != nil {
				return "", err
			}
		}
		data, err := os.ReadFile(src)
		if err != nil {
			return "", err
		}
		fmt.Fprintf(&b, "@font-face {\n  font-family: %q;\n  font-style: %s;\n  font-weight: %s;\n  font-display: %s;\n  src: url(data:%s;base64,%s) format(%q);\n}\n",
			f.Family, f.Style, f.Weight, display, fm[0], base64.StdEncoding.EncodeToString(data), fm[1])
	}
	return b.String(), nil
}

// writeAtomic writes data to a temporary file beside path and renames it
// into place, so path is never a partly written file.
func writeAtomic(path string, data []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".plate-fonts-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Chmod(0o644); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}
