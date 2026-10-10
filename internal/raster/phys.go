package raster

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"math"
	"os"

	"github.com/lockyc/plate/internal/atomicfile"
)

var pngSig = []byte("\x89PNG\r\n\x1a\n")

// SetDPI records dpi in a PNG's pHYs chunk, replacing any already there,
// without touching the pixel data. A render of tens of megapixels is not
// worth a full decode and re-encode for nine bytes.
func SetDPI(path string, dpi float64) error {
	// Validate dpi: must be finite, positive, and round to a valid uint32
	if math.IsNaN(dpi) || math.IsInf(dpi, 0) || dpi <= 0 {
		return fmt.Errorf("dpi must be finite and positive, got %v", dpi)
	}
	ppm := uint32(math.Round(dpi / 0.0254))
	if ppm < 1 {
		return fmt.Errorf("dpi %v rounds to ppm %d, must be >= 1", dpi, ppm)
	}

	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	chunks, err := split(b)
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}

	data := make([]byte, 9)
	binary.BigEndian.PutUint32(data[0:], ppm)
	binary.BigEndian.PutUint32(data[4:], ppm)
	data[8] = 1 // unit: metre

	var out bytes.Buffer
	out.Write(pngSig)
	foundIHDR := false
	for _, c := range chunks {
		if c.typ == "pHYs" {
			continue
		}
		out.Write(c.raw)
		if c.typ == "IHDR" {
			foundIHDR = true
			writeChunk(&out, "pHYs", data)
		}
	}
	if !foundIHDR {
		return fmt.Errorf("%s: PNG has no IHDR chunk", path)
	}

	// The file is edited, not made, so it keeps its own mode.
	fi, err := os.Stat(path)
	if err != nil {
		return err
	}
	return atomicfile.WriteFile(path, out.Bytes(), fi.Mode().Perm())
}

// DPI reads a PNG's pHYs resolution; 0 when it has none or none in metres.
func DPI(path string) (float64, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	chunks, err := split(b)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", path, err)
	}
	for _, c := range chunks {
		if c.typ == "pHYs" && len(c.data) == 9 && c.data[8] == 1 {
			return float64(binary.BigEndian.Uint32(c.data[0:])) * 0.0254, nil
		}
	}
	return 0, nil
}

type chunk struct {
	typ       string
	data, raw []byte
}

func split(b []byte) ([]chunk, error) {
	if !bytes.HasPrefix(b, pngSig) {
		return nil, fmt.Errorf("not a PNG")
	}
	var out []chunk
	for pos := len(pngSig); pos < len(b); {
		if pos+8 > len(b) {
			return nil, fmt.Errorf("truncated chunk header")
		}
		n := int(binary.BigEndian.Uint32(b[pos:]))
		end := pos + 12 + n
		if n < 0 || end > len(b) {
			return nil, fmt.Errorf("truncated chunk")
		}
		out = append(out, chunk{typ: string(b[pos+4 : pos+8]), data: b[pos+8 : pos+8+n], raw: b[pos:end]})
		pos = end
	}
	if len(out) == 0 || out[len(out)-1].typ != "IEND" {
		return nil, fmt.Errorf("truncated PNG: no IEND chunk")
	}
	return out, nil
}

func writeChunk(w *bytes.Buffer, typ string, data []byte) {
	binary.Write(w, binary.BigEndian, uint32(len(data)))
	body := append([]byte(typ), data...)
	w.Write(body)
	binary.Write(w, binary.BigEndian, crc32.ChecksumIEEE(body))
}
