package quality

import (
	"strings"
	"testing"

	"github.com/lockyc/plate/internal/enginetest"
)

func TestAVLength(t *testing.T) {
	file := t.TempDir() + "/c.mp4"
	enginetest.Stub(t, "ffprobe", `printf '{"streams":[{"codec_type":"video","duration":"3.010"},{"codec_type":"audio","duration":"2.970"}]}'`)
	if v, err := Metrics["av-length"](Params{"file": file, "want": 3.0}); err != nil || v < 0.0299 || v > 0.0301 {
		t.Fatalf("v = %v, err = %v: want the audio's 0.03 s short", v, err)
	}
	enginetest.Stub(t, "ffprobe", `printf '{"streams":[{"codec_type":"video","duration":"3.0"}]}'`)
	if _, err := Metrics["av-length"](Params{"file": file, "want": 3.0}); err == nil || !strings.Contains(err.Error(), "no audio") {
		t.Fatalf("a mute clip: err = %v", err)
	}
	if _, err := Metrics["av-length"](Params{"file": file}); err == nil {
		t.Fatal("no want: no error")
	}
}
