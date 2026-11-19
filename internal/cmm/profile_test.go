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
