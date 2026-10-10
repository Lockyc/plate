// Package atomicfile writes a file whole or not at all.
package atomicfile

import (
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

// Write hands write a temporary file in path's directory, gives it mode and
// renames it over path, so path is either as it was or the complete new
// file, never a partial one. Each call has a temporary file of its own, so
// concurrent writers never share one, and it never outlives the call.
func Write(path string, mode fs.FileMode, write func(io.Writer) error) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".plate-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	err = write(f)
	if err == nil {
		err = f.Chmod(mode)
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}

// WriteFile is Write for data already in memory.
func WriteFile(path string, data []byte, mode fs.FileMode) error {
	return Write(path, mode, func(w io.Writer) error {
		_, err := w.Write(data)
		return err
	})
}
