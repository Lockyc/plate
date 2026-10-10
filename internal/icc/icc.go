// Package icc carries the colour profile plate converts images to before
// it strips their metadata: the ICC's sRGB2014 (licence beside it).
package icc

import (
	_ "embed"
	"os"
	"path/filepath"

	"github.com/lockyc/plate/internal/atomicfile"
	"github.com/lockyc/plate/internal/engine"
)

//go:embed sRGB2014.icc
var srgb []byte

const sha = "384b832de3412066743b52a75ee906b6fb9fb8d9e09e936fc2c43223815c6e0a"

// SRGB returns the path of the sRGB profile, written to the cache on first use.
func SRGB() (string, error) {
	cache, err := engine.CacheDir()
	if err != nil {
		return "", err
	}
	p := filepath.Join(cache, "icc", sha[:16]+"-sRGB2014.icc")
	if _, err := os.Stat(p); err == nil {
		return p, nil
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return "", err
	}
	if err := atomicfile.WriteFile(p, srgb, 0o644); err != nil {
		return "", err
	}
	return p, nil
}
