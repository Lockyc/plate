package genai

import "testing"

func TestParseAspect(t *testing.T) {
	for in, ok := range map[string]bool{
		"3:2": true, "1.5:1": true, "16:9": true,
		"": false, "wide": false, "3": false, "0:1": false, "-1:2": false,
		"NaN:1": false, "1:NaN": false, "Inf:1": false, "1:+Inf": false, "-Inf:1": false, "1e400:1": false,
	} {
		if _, _, got := parseAspect(in); got != ok {
			t.Errorf("parseAspect(%q) ok = %v, want %v", in, got, ok)
		}
	}
}
