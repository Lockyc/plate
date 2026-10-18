package clip

import (
	"context"
	"image"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/lockyc/plate/internal/enginetest"
)

// frameStub is an ffmpeg stand-in writing short frames fewer than its
// -frames:v asks for to the pattern in its last argument.
func frameStub(t *testing.T, short int) string {
	return enginetest.Stub(t, "ffmpeg", `n=0; prev=; pat=
for a; do [ "$prev" = "-frames:v" ] && n=$a; prev=$a; pat=$a; done
n=$((n - `+strconv.Itoa(short)+`)); i=0
while [ $i -lt $n ]; do printf x > "$(printf "$pat" $i)"; i=$((i + 1)); done`)
}

func TestExtractFramesArgs(t *testing.T) {
	m := Master{W: 1206, H: 2622, FirstPTS: 33.3, Duration: 8000}
	w := Window{Start: 1000, End: 1500}
	probe := enginetest.Stub(t, "ffprobe", `exit 1`)
	log := frameStub(t, 0)
	dir := t.TempDir()
	frames, err := ExtractFrames(context.Background(), "raw.mov", m, w, 60, dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(frames) != 30 || frames[0] != filepath.Join(dir, "m00000.png") || frames[29] != filepath.Join(dir, "m00029.png") {
		t.Fatalf("frames = %d, %q … %q", len(frames), frames[0], frames[len(frames)-1])
	}
	for _, f := range frames {
		if _, err := os.Stat(f); err != nil {
			t.Fatal(err)
		}
	}
	calls := enginetest.Calls(t, log)
	if len(calls) != 1 {
		t.Fatalf("calls = %q", calls)
	}
	got := calls[0]
	tmp := got[len(got)-1]
	if filepath.Dir(filepath.Dir(tmp)) != dir || filepath.Base(tmp) != "m%05d.png" {
		t.Fatalf("output pattern %q is not in a temporary directory under %s", tmp, dir)
	}
	want := []string{"-hide_banner", "-loglevel", "error", "-nostdin",
		"-ss", "1.0333", "-i", "raw.mov",
		"-vf", "fps=60:round=down,scale=in_color_matrix=bt709:in_range=auto,format=rgb24",
		"-fps_mode", "cfr", "-frames:v", "30", "-start_number", "0", tmp}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("argv = %q\nwant   %q", got, want)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 30 {
		t.Fatalf("dir holds %d entries, want the 30 frames alone", len(entries))
	}
	if c := enginetest.Calls(t, probe); len(c) != 0 {
		t.Fatalf("ffprobe ran: %q", c)
	}
}

func TestExtractFramesRefusesShortMaster(t *testing.T) {
	frameStub(t, 1)
	dir := t.TempDir()
	_, err := ExtractFrames(context.Background(), "raw.mov", Master{}, Window{Start: 0, End: 500}, 60, dir)
	if err == nil || !strings.Contains(err.Error(), "master ends before the trim window: decoded 29 of 30 frames") {
		t.Fatalf("err = %v", err)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Fatalf("a refused decode left %d entries in %s", len(entries), dir)
	}
}

func TestExtractFramesRefusesEmptyWindow(t *testing.T) {
	log := frameStub(t, 0)
	_, err := ExtractFrames(context.Background(), "raw.mov", Master{}, Window{}, 60, t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "nothing to render") {
		t.Fatalf("err = %v", err)
	}
	if c := enginetest.Calls(t, log); len(c) != 0 {
		t.Fatalf("ffmpeg ran: %q", c)
	}
}

func TestExtractActivityArgs(t *testing.T) {
	log := enginetest.Stub(t, "ffmpeg", `for a; do pat=$a; done
for i in 0 1 2; do cp "$GRAY" "$(printf "$pat" $i)"; done`)
	gray := filepath.Join(t.TempDir(), "g.png")
	writePNG(t, gray, image.NewGray(image.Rect(0, 0, 4, 6)))
	t.Setenv("GRAY", gray)
	dir := t.TempDir()
	frames, err := ExtractActivity(context.Background(), "raw.mov", Master{FirstPTS: 33.3}, 60, dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(frames) != 3 || frames[0].Bounds() != image.Rect(0, 0, 4, 6) {
		t.Fatalf("frames = %d", len(frames))
	}
	got := enginetest.Calls(t, log)[0]
	want := []string{"-hide_banner", "-loglevel", "error", "-nostdin",
		"-ss", "0.0333", "-i", "raw.mov",
		"-vf", "fps=60:round=down,scale=200:-2,format=gray",
		"-fps_mode", "cfr", "-start_number", "0", got[len(got)-1]}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("argv = %q\nwant   %q", got, want)
	}
	if _, err := os.Stat(filepath.Join(dir, "a00002.png")); err != nil {
		t.Fatal(err)
	}
}
