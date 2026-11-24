package imgsize

import "testing"

func TestIsEquirect(t *testing.T) {
	for _, c := range []struct {
		w, h int
		want bool
	}{{2048, 1024, true}, {2049, 1024, true}, {2047, 1024, true}, {2050, 1024, false}, {1920, 1080, false}, {0, 0, false}, {1, 0, false}} {
		if got := IsEquirect(c.w, c.h); got != c.want {
			t.Errorf("IsEquirect(%d, %d) = %v, want %v", c.w, c.h, got, c.want)
		}
	}
}
