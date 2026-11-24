package video

import (
	"context"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/lockyc/plate/internal/enginetest"
)

func TestProbe(t *testing.T) {
	enginetest.Stub(t, "ffprobe", `printf '{"streams":[{"width":2048,"height":1024,"r_frame_rate":"30000/1001","duration":"N/A"}],"format":{"duration":"12.5"}}'`)
	src, err := Probe(context.Background(), "in.mp4", false)
	if err != nil {
		t.Fatal(err)
	}
	if src.W != 2048 || src.H != 1024 || src.Rate != "30000/1001" || src.Dur != 12.5 || src.FPS < 29.97 || src.FPS > 29.98 {
		t.Errorf("Probe = %+v: want 2048x1024, 30000/1001, the container's 12.5 s", src)
	}
}

func TestParseProbe(t *testing.T) {
	still, err := parseProbe("a.png", []byte(`{"streams":[{"width":64,"height":32}]}`), true)
	if err != nil || still.W != 64 || still.H != 32 || still.FPS != 0 {
		t.Errorf("a still: %+v, %v", still, err)
	}
	stream, err := parseProbe("a.mp4", []byte(`{"streams":[{"r_frame_rate":"30/1","duration":"3.0"}],"format":{"duration":"9"}}`), false)
	if err != nil || stream.Dur != 3 || stream.FPS != 30 {
		t.Errorf("the stream's duration comes first: %+v, %v", stream, err)
	}
	for _, bad := range []string{
		`{"streams":[]}`,
		`not json`,
		`{"streams":[{"r_frame_rate":"0/0","duration":"3"}]}`,
		`{"streams":[{"r_frame_rate":"30/1"}]}`,
		`{"streams":[{"r_frame_rate":"30/1","duration":"0"}]}`,
	} {
		if _, err := parseProbe("a.mp4", []byte(bad), false); err == nil {
			t.Errorf("%s: no error", bad)
		}
	}
}

func TestSeekIsAQuarterFrameEarly(t *testing.T) {
	for _, c := range []struct {
		t, fps float64
		want   string
	}{{1.5, 30, "1.491667"}, {0, 30, "0.000000"}, {0.001, 30, "0.000000"}} {
		if got := Seek(c.t, c.fps); got != c.want {
			t.Errorf("Seek(%v, %v) = %s, want %s", c.t, c.fps, got, c.want)
		}
	}
}

func TestFrameTimes(t *testing.T) {
	if got, want := FrameTimes(0.4, 1, 5), []float64{0.4, 0.6, 0.8}; !slices.Equal(got, want) {
		t.Errorf("FrameTimes(0.4, 1, 5) = %v, want %v", got, want)
	}
	// from between frames starts at the next; end is exclusive
	if got, want := FrameTimes(0.05, 0.3, 10), []float64{0.1, 0.2}; !slices.Equal(got, want) {
		t.Errorf("FrameTimes(0.05, 0.3, 10) = %v, want %v", got, want)
	}
	if got := FrameTimes(0, 1, 30000.0/1001); len(got) != 30 {
		t.Errorf("1 s at 29.97 fps: %d frames, want 30", len(got))
	}
}

func TestFilterPath(t *testing.T) {
	if got, want := FilterPath(`/t/a:b,c;d[e]'f`), `/t/a\\:b\,c\;d\[e\]\\\'f`; got != want {
		t.Errorf("FilterPath = %s, want %s", got, want)
	}
}

// Every command leads its frame by a quarter frame, so at 30000/1001 fps
// one whose time's decimal form rounds up is not a frame late.
func TestWriteCommandsLeadsAQuarterFrame(t *testing.T) {
	p, err := WriteCommands([]Command{{0, "v360@a yaw 1"}, {1, "v360@a yaw 2, v360@a pitch 3"}}, 2)
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(p)
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if want := "0.000000 v360@a yaw 1;\n0.875000 v360@a yaw 2, v360@a pitch 3;\n"; string(b) != want {
		t.Errorf("script =\n%s\nwant\n%s", b, want)
	}
	fps := 30000.0 / 1001
	s := script([]Command{{1001.0 / 30000, "x"}}, fps)
	if !strings.HasPrefix(s, "0.025025 ") {
		t.Errorf("one frame at 29.97 fps: %q, want 0.025025 (0.033367 less a quarter frame)", s)
	}
}
