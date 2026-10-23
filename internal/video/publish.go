package video

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
)

// Outputs are the names a render of name may write into --out: one per
// format, and the sidecar.
func Outputs(name string) []string {
	var out []string
	for _, f := range Formats {
		out = append(out, name+"."+Ext(f))
	}
	return append(out, name+".json")
}

// RemoveOutputs removes from out every output a render of name may write.
func RemoveOutputs(out, name string) error {
	for _, n := range Outputs(name) {
		if err := os.Remove(filepath.Join(out, n)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	return nil
}

// Publish moves files (names in tmp) into out as name's outputs. It first
// removes every output a render of name may write, so none from an earlier
// run with other formats survives, then renames each file in, the sidecar
// JSON last, so a present sidecar marks a whole set. On any failure it
// removes what it moved; tmp is removed either way.
func Publish(tmp, out, name string, files []string) error {
	defer os.RemoveAll(tmp)
	if err := RemoveOutputs(out, name); err != nil {
		return err
	}
	for _, f := range files {
		if _, err := os.Stat(filepath.Join(tmp, f)); err != nil {
			return err
		}
	}
	order := slices.Clone(files)
	sidecar := name + ".json"
	if i := slices.Index(order, sidecar); i >= 0 {
		order = append(slices.Delete(order, i, i+1), sidecar)
	}
	for i, f := range order {
		if err := os.Rename(filepath.Join(tmp, f), filepath.Join(out, f)); err != nil {
			for _, done := range order[:i] {
				os.Remove(filepath.Join(out, done))
			}
			return err
		}
	}
	return nil
}
