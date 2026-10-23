// Package video encodes a sequence of composed frames into the video and
// still formats plate writes, checks the encodes, and publishes a whole
// set of outputs. clip render and animate share it.
package video

import (
	"fmt"
	"image"
	"image/draw"
	"image/png"
	"io"
	"os"

	"github.com/lockyc/plate/internal/atomicfile"
)

// Formats are the outputs a render can write.
var Formats = []string{"mp4", "webm", "poster", "webp", "gif"}

// Ext is the file extension a format is written with.
func Ext(format string) string {
	if format == "poster" {
		return "poster.png"
	}
	return format
}

// FormatResult is one written output: its file name in --out, its size,
// and for a video encode its Sharpness.
type FormatResult struct {
	File      string  `json:"file"`
	Bytes     int64   `json:"bytes"`
	Sharpness float64 `json:"sharpness,omitempty"`
}

// Measurement is a render's sidecar, <name>.json: the shape of the video,
// each output, and the measures its checks hold. Sharpness is the least of
// the video encodes' (sharpFormats), and 1 when there are none. The loop
// frames are LoopClose's: the frames at each end the loop close altered.
type Measurement struct {
	Name           string                  `json:"name"`
	Width          int                     `json:"width"`
	Height         int                     `json:"height"`
	FPS            int                     `json:"fps"`
	Frames         int                     `json:"frames"`
	DurationMS     float64                 `json:"duration_ms"`
	Formats        map[string]FormatResult `json:"formats"`
	Sharpness      float64                 `json:"sharpness"`
	LoopSeam       float64                 `json:"loop_seam"`
	LoopHeadFrames int                     `json:"loop_head_frames"`
	LoopTailFrames int                     `json:"loop_tail_frames"`
}

// LoadImage decodes the image file at path.
func LoadImage(path string) (image.Image, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	img, _, err := image.Decode(f)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return img, nil
}

// LoadRGBA64 decodes the image file at path to 16-bit RGBA.
func LoadRGBA64(path string) (*image.RGBA64, error) {
	img, err := LoadImage(path)
	if err != nil {
		return nil, err
	}
	out := image.NewRGBA64(img.Bounds())
	if m, ok := img.(*image.RGBA); ok && m.Stride == 4*m.Rect.Dx() {
		for i, v := range m.Pix {
			out.Pix[2*i], out.Pix[2*i+1] = v, v // v·257, big-endian
		}
		return out, nil
	}
	draw.Draw(out, out.Bounds(), img, img.Bounds().Min, draw.Src)
	return out, nil
}

// SavePNG writes img as an opaque 8-bit RGB PNG, whole or not at all.
func SavePNG(path string, img *image.RGBA64) error {
	out := image.NewRGBA(img.Bounds())
	for i := range out.Pix {
		if i%4 == 3 {
			out.Pix[i] = 0xff
			continue
		}
		v := uint32(img.Pix[2*i])<<8 | uint32(img.Pix[2*i+1])
		out.Pix[i] = uint8((v*0xff + 0x7fff) / 0xffff)
	}
	enc := png.Encoder{CompressionLevel: png.BestSpeed}
	return atomicfile.Write(path, 0o644, func(w io.Writer) error { return enc.Encode(w, out) })
}
