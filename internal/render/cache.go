package render

import (
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"github.com/lockyc/plate/internal/atomicfile"
	"github.com/lockyc/plate/internal/engine"
	"github.com/lockyc/plate/internal/pins"
)

// fileHash is one file in a record: its absolute path and the sha256 of its
// bytes, "" for a file that was absent.
type fileHash struct {
	Path   string
	SHA256 string
}

// record is what one successful render read and wrote. It certifies itself:
// while every input still has its recorded hash and every output is present
// with its recorded hash, the outputs on disk are the ones that render made
// from the inputs on disk, so the render can be skipped.
type record struct {
	Inputs  []fileHash
	Outputs []fileHash
}

// current reports whether every input and output still matches. A record
// with no inputs or no outputs describes no render and is never current.
func (r record) current() bool {
	if len(r.Inputs) == 0 || len(r.Outputs) == 0 {
		return false
	}
	for _, f := range r.Inputs {
		if f.Path == "" || hashFile(f.Path) != f.SHA256 {
			return false
		}
	}
	for _, f := range r.Outputs {
		if f.SHA256 == "" || hashFile(f.Path) != f.SHA256 {
			return false
		}
	}
	return true
}

// writes reports whether the record's outputs are exactly paths.
func (r record) writes(paths []string) bool {
	got := make([]string, len(r.Outputs))
	for i, f := range r.Outputs {
		got[i] = f.Path
	}
	slices.Sort(got)
	want := slices.Sorted(slices.Values(paths))
	return slices.Equal(got, want)
}

// hashFile is the sha256 of p, or "" when p is absent, not a regular file or
// unreadable: the same judgement the proxy makes when it serves p.
func hashFile(p string) string {
	data, _, ok := readRegular(p)
	if !ok {
		return ""
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// exeHash is the sha256 of the running plate's own bytes, so a dev build
// that has not bumped VERSION still misses every record an older build
// wrote.
var exeHash = sync.OnceValues(func() (string, error) {
	p, err := os.Executable()
	if err != nil {
		return "", err
	}
	f, err := os.Open(p)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
})

// cacheKey names the record for o, with page the canonical page address
// (its absolute path and query, or the remote URL). It covers everything
// besides the loaded files that shapes the outputs: plate's own build, the
// pinned Chrome, every option with outputs made absolute, and the
// environment the render reads — SOURCE_DATE_EPOCH, which stamps a PDF, and
// the engine override, which swaps the Chrome that runs. Force only says
// whether to look, so it is left out.
func cacheKey(o Options, page string) (string, error) {
	exe, err := exeHash()
	if err != nil {
		return "", err
	}
	e, ok := pins.Lookup(chrome)
	if !ok || e.Download == nil {
		return "", fmt.Errorf("%s has no pinned download", chrome)
	}
	o.Page, o.Force = page, false
	for _, p := range []*string{&o.PNG, &o.PDF, &o.HTML} {
		if *p != "" {
			if *p, err = filepath.Abs(*p); err != nil {
				return "", err
			}
		}
	}
	b, err := json.Marshal(struct {
		Exe    string            `json:"exe"`
		Chrome string            `json:"chrome"`
		Env    map[string]string `json:"env"`
		Opts   Options           `json:"opts"`
	}{exe, e.Download.Version, map[string]string{
		"SOURCE_DATE_EPOCH":        os.Getenv("SOURCE_DATE_EPOCH"),
		engine.EnvOverride(chrome): os.Getenv(engine.EnvOverride(chrome)),
	}, o})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}

// recordPath is where key's record lives: plate's cache dir, never beside
// the outputs, where it would ship inside a deployed folder and leak the
// machine's paths.
func recordPath(key string) (string, error) {
	dir, err := engine.CacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "render", key+".json"), nil
}

// loadRecord reads key's record. Anything short of a well-formed record —
// absent, unreadable, corrupt — is a miss, never an error: the cost of a
// miss is one render.
func loadRecord(key string) (record, bool) {
	p, err := recordPath(key)
	if err != nil {
		return record{}, false
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return record{}, false
	}
	var r record
	if err := json.Unmarshal(b, &r); err != nil {
		return record{}, false
	}
	return r, true
}

// saveRecord writes key's record whole or not at all.
func saveRecord(key string, r record) error {
	p, err := recordPath(key)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	return atomicfile.WriteFile(p, b, 0o644)
}

// dropRecord removes key's record, if any.
func dropRecord(key string) error {
	p, err := recordPath(key)
	if err != nil {
		return err
	}
	if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// newRecord builds the record for a successful render of the local page
// that loaded l and wrote outputs, hashed by the render itself. When the
// render cannot be cached it returns why instead: it reached another host,
// a file changed while it was served, or the page itself was never served,
// so nothing says which bytes it rendered.
func newRecord(l loadSet, page string, outputs []fileHash) (record, string) {
	if len(l.External) > 0 {
		return record{}, "loads " + strings.Join(l.External, ", ")
	}
	if l.Unstable {
		return record{}, "a file changed while it rendered"
	}
	if _, ok := l.Files[page]; !ok {
		return record{}, "the page was never served"
	}
	var r record
	for p, sum := range l.Files {
		r.Inputs = append(r.Inputs, fileHash{p, sum})
	}
	for _, p := range l.Missing {
		r.Inputs = append(r.Inputs, fileHash{p, ""})
	}
	slices.SortFunc(r.Inputs, func(a, b fileHash) int { return cmp.Compare(a.Path, b.Path) })
	r.Outputs = outputs
	return r, ""
}
