package cmm

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	fogra = "../../quality/assets/Coated_Fogra39L_VIGC_300.icc"
	srgb  = "../../quality/assets/sRGB-v4.icc"
)

func TestLoadRefusesRGB(t *testing.T) {
	_, err := Load(srgb)
	if err == nil || !strings.Contains(err.Error(), "not a CMYK output profile") {
		t.Fatalf("Load(sRGB) = %v, want a refusal", err)
	}
}

func TestLoadUnknownTagType(t *testing.T) {
	b, err := os.ReadFile(fogra)
	if err != nil {
		t.Fatal(err)
	}
	n := int(binary.BigEndian.Uint32(b[128:]))
	found := false
	for i := range n {
		e := b[132+12*i:]
		if string(e[:4]) == "A2B1" {
			copy(b[binary.BigEndian.Uint32(e[4:]):], "xxxx")
			found = true
		}
	}
	if !found {
		t.Fatal("the Fogra profile has no A2B1 tag")
	}
	p := filepath.Join(t.TempDir(), "bad.icc")
	if err := os.WriteFile(p, b, 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = Load(p)
	if err == nil || !strings.Contains(err.Error(), "A2B1") || !strings.Contains(err.Error(), `"xxxx"`) {
		t.Fatalf("Load = %v, want a refusal naming A2B1 and xxxx", err)
	}
}

// TestLoadRefusesUnpairedB2A: with A2B1 converting, the black point must
// come from B2A1, never B2A0.
func TestLoadRefusesUnpairedB2A(t *testing.T) {
	b, err := os.ReadFile(fogra)
	if err != nil {
		t.Fatal(err)
	}
	n := int(binary.BigEndian.Uint32(b[128:]))
	for i := range n {
		if e := b[132+12*i:]; string(e[:4]) == "B2A1" {
			copy(e, "zzzz")
		}
	}
	p := filepath.Join(t.TempDir(), "nob2a1.icc")
	if err := os.WriteFile(p, b, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(p); err == nil || !strings.Contains(err.Error(), "no B2A1 tag") {
		t.Fatalf("Load = %v, want a refusal naming B2A1", err)
	}
}

func TestLoadRefusesMissingWhitePoint(t *testing.T) {
	b, err := os.ReadFile(fogra)
	if err != nil {
		t.Fatal(err)
	}
	n := int(binary.BigEndian.Uint32(b[128:]))
	for i := range n {
		if e := b[132+12*i:]; string(e[:4]) == "wtpt" {
			copy(e, "zzzz")
		}
	}
	p := filepath.Join(t.TempDir(), "nowtpt.icc")
	if err := os.WriteFile(p, b, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(p); err == nil || !strings.Contains(err.Error(), "wtpt") {
		t.Fatalf("Load = %v, want a refusal naming wtpt", err)
	}
}

// lutHeader is a lut16Type tag's 52-byte header (in inputs, out outputs, a
// grid of grid points on each, two-entry curves) and its input curves,
// with no CLUT after them.
func lutHeader(in, out, grid byte) []byte {
	b := make([]byte, 52+int(in)*2*2)
	copy(b, "mft2")
	b[8], b[9], b[10] = in, out, grid
	binary.BigEndian.PutUint16(b[48:], 2)
	binary.BigEndian.PutUint16(b[50:], 2)
	return b
}

// TestParseLutRefusesBeforeAllocating: a malformed header refuses before
// the CLUT is allocated, whatever its declared size.
func TestParseLutRefusesBeforeAllocating(t *testing.T) {
	for _, c := range []struct {
		name            string
		in, out, grid   byte
		wantIn, wantOut int
		refusal         string
	}{
		{"an A2B with 8 inputs on a 255 grid", 8, 3, 255, 4, 3, "want 4 to 3"},
		{"an A2B with 4 inputs on a 255 grid", 4, 3, 255, 4, 3, "more than"},
		{"a B2A with 3 inputs on a 255 grid and no data", 3, 4, 255, 3, 4, "shorter"},
	} {
		_, err := parseLut(lutHeader(c.in, c.out, c.grid), false, c.wantIn, c.wantOut)
		if err == nil || !strings.Contains(err.Error(), c.refusal) {
			t.Errorf("%s: %v, want a refusal with %q", c.name, err, c.refusal)
		}
	}
}
