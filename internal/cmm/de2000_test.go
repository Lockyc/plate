package cmm

import (
	"math"
	"testing"
)

// TestDE2000Sharma checks pairs 1, 17 and 34 of Sharma, Wu and Dalal's
// CIEDE2000 test data, which the paper gives to four decimals.
func TestDE2000Sharma(t *testing.T) {
	for _, c := range []struct {
		a, b [3]float64
		want float64
	}{
		{[3]float64{50, 2.6772, -79.7751}, [3]float64{50, 0, -82.7485}, 2.0425},
		{[3]float64{50, 2.5, 0}, [3]float64{73, 25, -18}, 27.1492},
		{[3]float64{22.7233, 20.0904, -46.6940}, [3]float64{23.0331, 14.9730, -42.5619}, 2.0373},
	} {
		for _, pair := range [][2][3]float64{{c.a, c.b}, {c.b, c.a}} {
			if got := DE2000(pair[0], pair[1]); math.Abs(got-c.want) > 1e-4 {
				t.Errorf("DE2000(%v, %v) = %.5f, want %.4f", pair[0], pair[1], got, c.want)
			}
		}
	}
}
