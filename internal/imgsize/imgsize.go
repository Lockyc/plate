// Package imgsize reads an image's dimensions without decoding its pixels.
package imgsize

import (
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"os"
)

// Dims returns path's width and height.
func Dims(path string) (int, int, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, 0, err
	}
	defer f.Close()
	c, _, err := image.DecodeConfig(f)
	if err != nil {
		return 0, 0, fmt.Errorf("%s: %w", path, err)
	}
	return c.Width, c.Height, nil
}

// IsEquirect reports a w×h picture that is a 360 equirect: 2:1, a pixel
// either way.
func IsEquirect(w, h int) bool {
	d := w - 2*h
	return h > 0 && d >= -1 && d <= 1
}
