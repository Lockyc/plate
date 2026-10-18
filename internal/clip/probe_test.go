package clip

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/lockyc/plate/internal/enginetest"
)

func TestProbeMasterArgs(t *testing.T) {
	log := enginetest.Stub(t, "ffprobe", `cat <<'EOF'
{"programs":[],"streams":[{"width":1206,"height":2622,"color_range":"tv","color_transfer":"iec61966-2-1","color_primaries":"smpte432"}],"frames":[{"pts_time":"0.033300"}],"format":{"duration":"8.000000"}}
EOF`)
	m, err := ProbeMaster(context.Background(), "raw.mov")
	if err != nil {
		t.Fatal(err)
	}
	want := Master{W: 1206, H: 2622, FirstPTS: 33.3, Duration: 8000,
		Colour: Colour{Primaries: "smpte432", Transfer: "iec61966-2-1", Range: "tv"}}
	if m != want {
		t.Fatalf("master = %+v", m)
	}
	args := strings.Fields("-v error -select_streams v:0 -show_entries stream=width,height,color_primaries,color_transfer,color_range:format=duration -show_frames -read_intervals %+#1 -of json raw.mov")
	if calls := enginetest.Calls(t, log); len(calls) != 1 || !reflect.DeepEqual(calls[0], args) {
		t.Fatalf("calls = %q, want %q", calls, args)
	}
}

func TestProbeMasterUntaggedColour(t *testing.T) {
	enginetest.Stub(t, "ffprobe", `echo '{"streams":[{"width":4,"height":2,"color_primaries":"unknown"}],"frames":[{"pts_time":"0"}],"format":{"duration":"1"}}'`)
	m, err := ProbeMaster(context.Background(), "raw.mov")
	if err != nil || m.Colour != (Colour{}) {
		t.Fatalf("master = %+v, %v; want no colour tags", m, err)
	}
}

func TestProbeMasterRefusesNoFrame(t *testing.T) {
	enginetest.Stub(t, "ffprobe", `echo '{"streams":[{"width":1,"height":2}],"frames":[],"format":{"duration":"1"}}'`)
	if _, err := ProbeMaster(context.Background(), "raw.mov"); err == nil || !strings.Contains(err.Error(), "no video frame") {
		t.Fatalf("err = %v", err)
	}
}

func TestCheckMaster(t *testing.T) {
	l, err := parse(t, load(t, contractExample), contractLog...)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range []Master{{W: 1206, H: 2622, Duration: 8300}, {W: 1205, H: 2623, Duration: 8300},
		// simctl writes no frame while the screen is still, so a master
		// whose last change came early probes short of the log's end.
		{W: 1206, H: 2622, Duration: 6500}} {
		if warn, err := l.CheckMaster(m); err != nil || warn != "" {
			t.Fatalf("%+v: warning %q, err %v", m, warn, err)
		}
	}
	_, err = l.CheckMaster(Master{W: 1170, H: 2532, Duration: 8300})
	if err == nil || !strings.Contains(err.Error(), "master is 1170x2532, events say 1206x2622") {
		t.Fatalf("err = %v", err)
	}
}

func TestCheckMasterRefusesTruncated(t *testing.T) {
	l, err := parse(t, load(t, contractExample), contractLog...)
	if err != nil {
		t.Fatal(err)
	}
	_, err = l.CheckMaster(Master{W: 1206, H: 2622, FirstPTS: 20, Duration: 1200})
	if err == nil || err.Error() != "the master's last frame is at 1180 ms, before the first action at 1520 ms: it is cut short or not this log's recording" {
		t.Fatalf("err = %v", err)
	}
	warn, err := l.CheckMaster(Master{W: 1206, H: 2622, Duration: 3000})
	if err != nil || warn != "the master's last frame is at 3000 ms, before the last action at 6400 ms; the screen holds still from there" {
		t.Fatalf("warning %q, err %v", warn, err)
	}
}
