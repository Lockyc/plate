package cmm

import (
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"
)

// ReadCGATS reads a CGATS characterisation file's CMYK_C/M/Y/K (percent)
// and LAB_L/A/B columns.
func ReadCGATS(path string) (cmyk [][4]float64, lab [][3]float64, err error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, err
	}
	var fields []string
	var inFormat, inData bool
	want := []string{"CMYK_C", "CMYK_M", "CMYK_Y", "CMYK_K", "LAB_L", "LAB_A", "LAB_B"}
	var col []int
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Fields(line)
		switch {
		case len(f) == 0:
		case f[0] == "BEGIN_DATA_FORMAT":
			inFormat = true
		case f[0] == "END_DATA_FORMAT":
			inFormat = false
			for _, w := range want {
				i := slices.Index(fields, w)
				if i < 0 {
					return nil, nil, fmt.Errorf("%s: no %s column", path, w)
				}
				col = append(col, i)
			}
		case inFormat:
			fields = append(fields, f...)
		case f[0] == "BEGIN_DATA":
			if col == nil {
				return nil, nil, fmt.Errorf("%s: data before its format", path)
			}
			inData = true
		case f[0] == "END_DATA":
			inData = false
		case inData:
			if len(f) != len(fields) {
				return nil, nil, fmt.Errorf("%s: %q has %d fields, want %d", path, strings.TrimSpace(line), len(f), len(fields))
			}
			var v [7]float64
			for k, i := range col {
				if v[k], err = strconv.ParseFloat(f[i], 64); err != nil {
					return nil, nil, fmt.Errorf("%s: %w", path, err)
				}
			}
			cmyk = append(cmyk, [4]float64{v[0], v[1], v[2], v[3]})
			lab = append(lab, [3]float64{v[4], v[5], v[6]})
		}
	}
	if len(cmyk) == 0 {
		return nil, nil, fmt.Errorf("%s: no data", path)
	}
	return cmyk, lab, nil
}
