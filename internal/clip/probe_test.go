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
{"programs":[],"streams":[{"width":1206,"height":2622}],"frames":[{"pts_time":"0.033300"}],"format":{"duration":"8.000000"}}
EOF`)
	m, err := ProbeMaster(context.Background(), "raw.mov")
	if err != nil {
		t.Fatal(err)
	}
	if m != (Master{W: 1206, H: 2622, FirstPTS: 33.3, Duration: 8000}) {
		t.Fatalf("master = %+v", m)
	}
	want := strings.Fields("-v error -select_streams v:0 -show_entries stream=width,height:format=duration -show_frames -read_intervals %+#1 -of json raw.mov")
	if calls := enginetest.Calls(t, log); len(calls) != 1 || !reflect.DeepEqual(calls[0], want) {
		t.Fatalf("calls = %q, want %q", calls, want)
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
	if err := l.CheckMaster(Master{W: 1206, H: 2622, Duration: 8300}); err != nil {
		t.Fatal(err)
	}
	if err := l.CheckMaster(Master{W: 1205, H: 2623, Duration: 8300}); err != nil {
		t.Fatalf("within 1 px: %v", err)
	}
	err = l.CheckMaster(Master{W: 1170, H: 2532, Duration: 8300})
	if err == nil || !strings.Contains(err.Error(), "master is 1170x2532, events say 1206x2622") {
		t.Fatalf("err = %v", err)
	}
	err = l.CheckMaster(Master{W: 1206, H: 2622, Duration: 8000})
	if err == nil || !strings.Contains(err.Error(), "past the master's 8000 ms") {
		t.Fatalf("err = %v", err)
	}
}
