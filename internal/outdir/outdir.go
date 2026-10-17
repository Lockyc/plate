// Package outdir is the destination directory of a command that writes
// several files into --out.
package outdir

import (
	"fmt"
	"os"
	"path/filepath"
)

// Make makes the destination: out, created if missing (poppler creates
// none and dies with a message that reads like a corrupt file), else a
// fresh temporary directory named by pattern, as os.MkdirTemp takes it.
// made says the directory is plate's own, to remove when the command fails.
func Make(out, pattern string) (dir string, made bool, err error) {
	if out == "" {
		dir, err = os.MkdirTemp("", pattern)
		return dir, err == nil, err
	}
	if err := os.MkdirAll(out, 0o755); err != nil {
		return "", false, err
	}
	// Absolute, so a directory named like a flag never reaches an engine as one.
	dir, err = filepath.Abs(out)
	return dir, false, err
}

// RefuseExisting fails when any of names is already in dir. A destination
// that holds an earlier run's files would mix them with this run's; it is
// refused, never cleared.
func RefuseExisting(dir string, names []string) error {
	for _, n := range names {
		if _, err := os.Lstat(filepath.Join(dir, n)); err == nil {
			return fmt.Errorf("%s already holds %s; pass a fresh --out (nothing was deleted)", dir, n)
		}
	}
	return nil
}
