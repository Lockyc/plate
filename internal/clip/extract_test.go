package clip

import (
	"context"
	"fmt"
	"image"
	"image/color"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/lockyc/plate/internal/engine"
	"github.com/lockyc/plate/internal/enginetest"
	"github.com/lockyc/plate/internal/pins"
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
	w := Window{Start: 1000, End: 1500}
	probe := enginetest.Stub(t, "ffprobe", `exit 1`)
	log := frameStub(t, 0)
	dir := t.TempDir()
	frames, err := ExtractFrames(context.Background(), "raw.mov", Colour{}, w, 60, dir)
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
		"-i", "raw.mov",
		"-vf", "tpad=stop_mode=clone:stop_duration=1.5,fps=60:round=down,scale=in_color_matrix=bt709:in_range=auto,format=rgb24",
		"-fps_mode", "cfr", "-ss", "1", "-frames:v", "30", "-start_number", "0", tmp}
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
	_, err := ExtractFrames(context.Background(), "raw.mov", Colour{}, Window{Start: 0, End: 500}, 60, dir)
	if err == nil || !strings.Contains(err.Error(), "raw.mov: decoded 29 of 30 frames") {
		t.Fatalf("err = %v", err)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Fatalf("a refused decode left %d entries in %s", len(entries), dir)
	}
}

func TestExtractFramesRefusesEmptyWindow(t *testing.T) {
	log := frameStub(t, 0)
	_, err := ExtractFrames(context.Background(), "raw.mov", Colour{}, Window{}, 60, t.TempDir())
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
	frames, err := ExtractActivity(context.Background(), "raw.mov", 60, dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(frames) != 3 || frames[0].Bounds() != image.Rect(0, 0, 4, 6) {
		t.Fatalf("frames = %d", len(frames))
	}
	got := enginetest.Calls(t, log)[0]
	want := []string{"-hide_banner", "-loglevel", "error", "-nostdin",
		"-i", "raw.mov",
		"-vf", "fps=60:round=down,scale=200:-2,format=gray",
		"-fps_mode", "cfr", "-start_number", "0", got[len(got)-1]}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("argv = %q\nwant   %q", got, want)
	}
	if _, err := os.Stat(filepath.Join(dir, "a00002.png")); err != nil {
		t.Fatal(err)
	}
}

// TestExtractFramesHoldsVFRFrames decodes a real variable frame rate master,
// whose frames sit at pts 0.5 s red, 1.5 s lime, 2.5 s blue and 3.5 s, and
// checks each output frame holds the master frame on screen at its time.
func TestExtractFramesHoldsVFRFrames(t *testing.T) {
	if _, err := engine.Resolve("ffmpeg"); err != nil {
		t.Skip(err)
	}
	fix := t.TempDir()
	colours := map[string]color.RGBA64{"red": red, "lime": {0, 0xffff, 0, 0xffff}, "blue": {0, 0, 0xffff, 0xffff}}
	for name, c := range colours {
		writePNG(t, filepath.Join(fix, name+".png"), solid(16, 16, c))
	}
	var list strings.Builder
	for _, name := range []string{"red", "lime", "blue"} {
		fmt.Fprintf(&list, "file '%s'\nduration 1\n", filepath.Join(fix, name+".png"))
	}
	fmt.Fprintf(&list, "file '%s'\n", filepath.Join(fix, "blue.png"))
	concat, master := filepath.Join(fix, "list.txt"), filepath.Join(fix, "vfr.mov")
	if err := os.WriteFile(concat, []byte(list.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := engine.Run(context.Background(), engine.Cmd{Engine: "ffmpeg",
		Args: []string{"-hide_banner", "-loglevel", "error", "-nostdin", "-f", "concat", "-safe", "0", "-i", concat,
			"-fps_mode", "passthrough", "-c:v", "png", "-output_ts_offset", "0.5", master},
		Inputs: []string{concat}, Outputs: []string{master}}); err != nil {
		t.Fatal(err)
	}
	// 700 ms after the first frame is pts 1.2 s: three red ticks, ten lime, then blue.
	frames, err := ExtractFrames(context.Background(), master, Colour{}, Window{Start: 700, End: 2100}, 10, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	want := "RRRLLLLLLLLLLB"
	var got strings.Builder
	for _, f := range frames {
		c := readRGBA64(t, f).RGBA64At(8, 8)
		switch {
		case c.R > 0xc000 && c.G < 0x4000 && c.B < 0x4000:
			got.WriteByte('R')
		case c.G > 0xc000 && c.R < 0x4000 && c.B < 0x4000:
			got.WriteByte('L')
		case c.B > 0xc000 && c.R < 0x4000 && c.G < 0x4000:
			got.WriteByte('B')
		default:
			got.WriteByte('?')
		}
	}
	if got.String() != want {
		t.Fatalf("frames = %s, want %s", got.String(), want)
	}
}

func TestExtractFramesConvertsWideGamutArgs(t *testing.T) {
	log := frameStub(t, 0)
	p3 := Colour{Primaries: "smpte432", Transfer: "iec61966-2-1", Range: "tv"}
	if _, err := ExtractFrames(context.Background(), "raw.mov", p3, Window{Start: 0, End: 100}, 60, t.TempDir()); err != nil {
		t.Fatal(err)
	}
	got := enginetest.Calls(t, log)[0]
	want := "colorspace=ispace=bt709:irange=tv:iprimaries=smpte432:itrc=iec61966-2-1:" +
		"space=bt709:range=pc:primaries=bt709:trc=iec61966-2-1:format=yuv444p12," +
		"tpad=stop_mode=clone:stop_duration=0.1,fps=60:round=down,scale=in_color_matrix=bt709:in_range=auto,format=rgb24"
	if got[7] != want {
		t.Fatalf("-vf %q\nwant %q", got[7], want)
	}
	for _, c := range []Colour{{}, {Primaries: "bt709", Transfer: "bt709", Range: "tv"}} {
		log := frameStub(t, 0)
		if _, err := ExtractFrames(context.Background(), "raw.mov", c, Window{Start: 0, End: 100}, 60, t.TempDir()); err != nil {
			t.Fatal(err)
		}
		if vf := enginetest.Calls(t, log)[0][7]; strings.Contains(vf, "colorspace") {
			t.Fatalf("%+v: -vf %q converts a BT.709 master", c, vf)
		}
	}
}

// TestExtractFramesConvertsDisplayP3 decodes a real master tagged as
// simctl tags its recordings (Display P3 primaries, sRGB transfer, BT.709
// matrix), whose pixel is sRGB red written in P3, and expects sRGB red.
func TestExtractFramesConvertsDisplayP3(t *testing.T) {
	if _, err := engine.Resolve("ffmpeg"); err != nil {
		t.Skip(err)
	}
	master := filepath.Join(t.TempDir(), "p3.mp4")
	if _, err := engine.Run(context.Background(), engine.Cmd{Engine: "ffmpeg",
		Args: []string{"-hide_banner", "-loglevel", "error", "-nostdin", "-f", "lavfi", "-i", "color=c=0xEA3324:s=64x64:r=60:d=0.2",
			"-vf", "scale=out_color_matrix=bt709:out_range=tv,format=yuv444p,setparams=color_primaries=smpte432:color_trc=iec61966-2-1:colorspace=bt709:range=tv",
			"-c:v", "libx264", "-qp", "0", master},
		Outputs: []string{master}}); err != nil {
		t.Fatal(err)
	}
	m, err := ProbeMaster(context.Background(), master)
	if err != nil {
		t.Fatal(err)
	}
	for name, c := range map[string]Colour{"tagged": m.Colour, "read as BT.709": {}} {
		frames, err := ExtractFrames(context.Background(), master, c, Window{Start: 0, End: 50}, 60, t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		px := readRGBA64(t, frames[0]).RGBA64At(32, 32)
		r, g, b := px.R>>8, px.G>>8, px.B>>8
		isRed := r >= 250 && g <= 6 && b <= 6
		if isRed != (name == "tagged") {
			t.Errorf("%s: pixel = %d,%d,%d", name, r, g, b)
		}
	}
}

func TestExtractEncodedArgs(t *testing.T) {
	log := enginetest.Stub(t, "ffmpeg", `for a; do pat=$a; done
for i in 0 1 2; do cp "$RGB" "$(printf "$pat" $i)"; done`)
	rgb := filepath.Join(t.TempDir(), "c.png")
	writePNG(t, rgb, solid(4, 6, red))
	t.Setenv("RGB", rgb)
	frames, err := ExtractEncoded(context.Background(), "clip.mp4", []int{0, 30, 59}, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if len(frames) != 3 || frames[2].RGBA64At(1, 1) != red {
		t.Fatalf("frames = %d", len(frames))
	}
	got := enginetest.Calls(t, log)[0]
	want := []string{"-hide_banner", "-loglevel", "error", "-nostdin",
		"-i", "clip.mp4",
		"-vf", `select=eq(n\,0)+eq(n\,30)+eq(n\,59),scale=in_color_matrix=bt709:in_range=auto:flags=accurate_rnd+full_chroma_int+bilinear,format=rgb24`,
		"-fps_mode", "passthrough", "-start_number", "0", got[len(got)-1]}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("argv = %q\nwant   %q", got, want)
	}
	enginetest.Stub(t, "ffmpeg", `for a; do pat=$a; done
cp "$RGB" "$(printf "$pat" 0)"`)
	if _, err := ExtractEncoded(context.Background(), "clip.mp4", []int{0, 30}, t.TempDir()); err == nil || !strings.Contains(err.Error(), "decoded 1 of 2 frames") {
		t.Fatalf("short decode: err = %v", err)
	}
}

// TestExtractFramesHoldsStaticTail decodes masters shaped like simctl's:
// variable frame rate, frames only when the screen changes, here at 0 and
// 0.5 s and none after, while the log runs to 3000 ms. Every output frame
// must exist, the tail holding the 0.5 s frame; simctl's default codec,
// HEVC, is decoded as well as H.264.
func TestExtractFramesHoldsStaticTail(t *testing.T) {
	if _, err := engine.Resolve("ffmpeg"); err != nil {
		t.Skip(err)
	}
	fix := t.TempDir()
	writePNG(t, filepath.Join(fix, "red.png"), solid(16, 16, red))
	writePNG(t, filepath.Join(fix, "lime.png"), solid(16, 16, color.RGBA64{0, 0xffff, 0, 0xffff}))
	concat := filepath.Join(fix, "list.txt")
	list := fmt.Sprintf("file '%s'\nduration 0.5\nfile '%s'\nduration 0.0167\nfile '%s'\n",
		filepath.Join(fix, "red.png"), filepath.Join(fix, "lime.png"), filepath.Join(fix, "lime.png"))
	if err := os.WriteFile(concat, []byte(list), 0o644); err != nil {
		t.Fatal(err)
	}
	ffmpeg, _ := pins.Lookup("ffmpeg")
	for _, codec := range [][]string{{"libx264"}, {"libx265", "-x265-params", "log-level=none"}} {
		has, err := engine.Capable(context.Background(), ffmpeg, pins.Capability{Args: []string{"-hide_banner", "-encoders"}, Token: codec[0]})
		if err != nil {
			t.Fatal(err)
		}
		if !has && codec[0] == "libx265" {
			t.Logf("libx265 is not in this ffmpeg; HEVC unchecked")
			continue
		}
		master := filepath.Join(fix, codec[0]+".mp4")
		args := append([]string{"-hide_banner", "-loglevel", "error", "-nostdin", "-f", "concat", "-safe", "0", "-i", concat,
			"-fps_mode", "passthrough", "-pix_fmt", "yuv420p", "-c:v"}, codec...)
		if _, err := engine.Run(context.Background(), engine.Cmd{Engine: "ffmpeg", Args: append(args, master),
			Inputs: []string{concat}, Outputs: []string{master}}); err != nil {
			t.Fatalf("%s: %v", codec[0], err)
		}
		codec := codec[0]
		m, err := ProbeMaster(context.Background(), master)
		if err != nil || m.Duration > 1000 {
			t.Fatalf("%s: probe %+v, %v; want a master well short of 3000 ms", codec, m, err)
		}
		frames, err := ExtractFrames(context.Background(), master, m.Colour, Window{Start: 0, End: 3000}, 60, t.TempDir())
		if err != nil {
			t.Fatalf("%s: %v", codec, err)
		}
		if len(frames) != 180 {
			t.Fatalf("%s: %d frames, want 180", codec, len(frames))
		}
		for _, k := range []int{0, 29, 31, 120, 179} {
			c := readRGBA64(t, frames[k]).RGBA64At(8, 8)
			lime := c.G > 0xc000 && c.R < 0x4000
			if lime != (k > 30) {
				t.Errorf("%s: frame %d is %v", codec, k, c)
			}
		}
	}
}
