package render

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"github.com/lockyc/plate/internal/atomicfile"
	"github.com/lockyc/plate/internal/engine"
	"github.com/lockyc/plate/internal/pins"
)

// FileHash is one file in a record: its absolute path and the sha256 of its
// bytes, "" for a file that was absent.
type FileHash struct {
	Path   string
	SHA256 string
}

// Load is a page as Chrome loads it: its URL (PlateURL for a local page) and
// every Chrome call's base flags, the proxy's included for a local page. The
// virtual-time budget is the caller's to add.
type Load struct {
	URL        string
	ChromeArgs []string
}

// Cached runs do through the render cache, keyed on kind, key and the page.
// When the record of the last success still matches every file the page
// loaded and every file in outputs (absolute paths), do is skipped and the
// Result is Unchanged. force runs do regardless and still records. A remote
// page, a page that reached another host, or a key that cannot be made
// leaves no record, so do runs every time. The old record goes before do
// runs, so a run that fails or is killed never leaves one behind.
//
// do loads l and returns the hash of every output it published, each hashed
// under a temporary name before it was renamed into place (job.run says
// why). key is the caller's, normalised so that equal runs marshal equally;
// warn receives cache warnings, named for kind.
func Cached(ctx context.Context, kind, page string, key any, outputs []string, force bool, warn io.Writer,
	do func(ctx context.Context, l Load) ([]FileHash, error)) (Result, error) {
	u, local, query, err := pageURL(page)
	if err != nil {
		return Result{}, err
	}
	cacheWarn := func(err error) { fmt.Fprintf(warn, "plate %s: warning: render cache: %v\n", kind, err) }
	k, err := cacheKey(kind, key, address(u, local, query))
	if err != nil {
		// No key, no record to trust or keep: run uncached.
		cacheWarn(err)
		loads, _, runErr := loadPage(ctx, u, local, query, do)
		if runErr != nil {
			return Result{}, runErr
		}
		return Result{Outcome: Uncacheable, Reason: "no cache key: " + err.Error(), Inputs: loads.paths()}, nil
	}
	if local != "" && !force {
		if r, ok := loadRecord(k); ok && r.writes(outputs) && r.current() {
			return Result{Outcome: Unchanged, Inputs: r.inputPaths()}, nil
		}
	}
	if err := dropRecord(k); err != nil {
		cacheWarn(err)
	}
	loads, hashes, err := loadPage(ctx, u, local, query, do)
	if err != nil {
		return Result{}, err
	}
	if local == "" {
		return Result{Outcome: Uncacheable, Reason: "remote page"}, nil
	}
	r, why := newRecord(loads, local, hashes)
	if why != "" {
		return Result{Outcome: Uncacheable, Reason: why, Inputs: loads.paths()}, nil
	}
	if err := saveRecord(k, r); err != nil {
		cacheWarn(err)
	}
	return Result{Outcome: Rendered, Inputs: r.inputPaths()}, nil
}

// Outcome is what Cached did with a run that succeeded.
type Outcome int

const (
	Rendered    Outcome = iota // rendered, and recorded for next time
	Unchanged                  // skipped: nothing it loaded or wrote has changed
	Uncacheable                // rendered, but no record can vouch for it; Result.Reason says why
)

// Result is Cached's outcome, with the reason when it is Uncacheable.
type Result struct {
	Outcome Outcome
	Reason  string
	// Inputs is every local file the page loaded or asked for and found
	// missing, from the record on a skip; nil for a remote page.
	Inputs []string
}

// record is what one successful render read and wrote. It certifies itself:
// while every input still has its recorded hash and every output is present
// with its recorded hash, the outputs on disk are the ones that render made
// from the inputs on disk, so the render can be skipped.
type record struct {
	Inputs  []FileHash
	Outputs []FileHash
}

// current reports whether every input and output still matches. A record
// with no inputs or no outputs describes no render and is never current.
func (r record) current() bool {
	if len(r.Inputs) == 0 || len(r.Outputs) == 0 {
		return false
	}
	for _, f := range r.Inputs {
		if f.Path == "" || HashFile(f.Path) != f.SHA256 {
			return false
		}
	}
	for _, f := range r.Outputs {
		if f.SHA256 == "" || HashFile(f.Path) != f.SHA256 {
			return false
		}
	}
	return true
}

// inputPaths is the path of every input, served or missing.
func (r record) inputPaths() []string {
	paths := make([]string, len(r.Inputs))
	for i, f := range r.Inputs {
		paths[i] = f.Path
	}
	return paths
}

// paths is every local file l served or found missing.
func (l loadSet) paths() []string {
	paths := slices.Sorted(maps.Keys(l.Files))
	return append(paths, l.Missing...)
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

// HashFile is the sha256 of p, or "" when p is absent, not a regular file or
// unreadable: the same judgement the proxy makes when it serves p.
func HashFile(p string) string {
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

// cacheKey names the record for a kind of run with the caller's key, with
// page the canonical page address (PlateURL of its absolute path and query,
// or the remote URL). It covers everything besides the loaded files that
// shapes the outputs: the kind, plate's own build, the pinned Chrome, the
// caller's options, and the environment a run reads — SOURCE_DATE_EPOCH,
// which stamps a PDF, and the engine override, which swaps the Chrome that
// runs.
func cacheKey(kind string, key any, page string) (string, error) {
	exe, err := exeHash()
	if err != nil {
		return "", err
	}
	e, ok := pins.Lookup(chrome)
	if !ok || e.Download == nil {
		return "", fmt.Errorf("%s has no pinned download", chrome)
	}
	b, err := json.Marshal(struct {
		Kind   string            `json:"kind"`
		Exe    string            `json:"exe"`
		Chrome string            `json:"chrome"`
		Env    map[string]string `json:"env"`
		Page   string            `json:"page"`
		Opts   any               `json:"opts"`
	}{kind, exe, e.Download.Version, map[string]string{
		"SOURCE_DATE_EPOCH":        os.Getenv("SOURCE_DATE_EPOCH"),
		engine.EnvOverride(chrome): os.Getenv(engine.EnvOverride(chrome)),
	}, page, key})
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
// or the page itself was never served, so nothing says which bytes it
// rendered.
func newRecord(l loadSet, page string, outputs []FileHash) (record, string) {
	if len(l.External) > 0 {
		return record{}, "loads " + strings.Join(l.External, ", ")
	}
	if _, ok := l.Files[page]; !ok {
		return record{}, "the page was never served"
	}
	var r record
	for p, sum := range l.Files {
		r.Inputs = append(r.Inputs, FileHash{p, sum})
	}
	for _, p := range l.Missing {
		r.Inputs = append(r.Inputs, FileHash{p, ""})
	}
	slices.SortFunc(r.Inputs, func(a, b FileHash) int { return cmp.Compare(a.Path, b.Path) })
	r.Outputs = outputs
	return r, ""
}
