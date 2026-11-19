package cmm

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const chart = "../../quality/assets/FOGRA39.txt"

func TestReadCGATS(t *testing.T) {
	cmyk, lab, err := ReadCGATS(chart)
	if err != nil {
		t.Fatal(err)
	}
	if len(cmyk) != 1617 || cmyk[1] != [4]float64{0, 10, 0, 0} || lab[0] != [3]float64{95, 0, -2} {
		t.Fatalf("read %d patches, patch 2 %v, patch 1 Lab %v", len(cmyk), cmyk[1], lab[0])
	}
	bad := filepath.Join(t.TempDir(), "bad.txt")
	if err := os.WriteFile(bad, []byte("BEGIN_DATA_FORMAT\nSAMPLE_ID CMYK_C\nEND_DATA_FORMAT\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ReadCGATS(bad); err == nil || !strings.Contains(err.Error(), "no CMYK_M column") {
		t.Fatalf("ReadCGATS(bad) = %v", err)
	}
}

// TestAbsoluteLabAgainstFOGRA39: the profile is built from FOGRA39, so its
// absolute colorimetry lands on Fogra's measurements; a PCS decoding error
// (lut16's legacy Lab read as v4) shows here as a mean near 0.5.
func TestAbsoluteLabAgainstFOGRA39(t *testing.T) {
	p := fogra39(t)
	cmyk, lab, err := ReadCGATS(chart)
	if err != nil {
		t.Fatal(err)
	}
	var sum float64
	for i, c := range cmyk {
		sum += DE2000(p.AbsoluteLab([4]float64{c[0] / 100, c[1] / 100, c[2] / 100, c[3] / 100}), lab[i])
	}
	if mean := sum / float64(len(cmyk)); mean > 0.05 {
		t.Errorf("mean ΔE2000 from FOGRA39 = %.4f, want <= 0.05", mean)
	}
}
