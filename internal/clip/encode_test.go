package clip

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/lockyc/plate/internal/engine"
	"github.com/lockyc/plate/internal/enginetest"
)

// refsDir is a directory of n composed frames, r%05d.png.
func refsDir(t *testing.T, n int) string {
	dir := t.TempDir()
	for k := range n {
		writePNG(t, filepath.Join(dir, "r0000"+string(rune('0'+k))+".png"), solid(4, 2, red))
	}
	return dir
}

// inOrder reports whether want appears in got as a subsequence.
func inOrder(got, want []string) bool {
	i := 0
	for _, a := range got {
		if i < len(want) && a == want[i] {
			i++
		}
	}
	return i == len(want)
}

const writeLast = `for a; do o=$a; done; o=${o#WEBP:}; printf x > "$o"`

func TestEncodeArgs(t *testing.T) {
	refs := refsDir(t, 3)
	out := t.TempDir()
	ctx := context.Background()
	ffmpeg := enginetest.Stub(t, "ffmpeg", writeLast)
	magick := enginetest.Stub(t, "magick", writeLast)

	if err := Encode(ctx, "mp4", refs, 60, 4, 2, filepath.Join(out, "c.mp4")); err != nil {
		t.Fatal(err)
	}
	mp4 := enginetest.Calls(t, ffmpeg)[0]
	floor := []string{"-c:v", "libx264", "-pix_fmt", "yuv420p",
		"-colorspace", "bt709", "-color_primaries", "bt709", "-color_trc", "bt709", "-color_range", "tv",
		"-movflags", "+faststart", "-an", "-fps_mode", "cfr", "-r", "60", filepath.Join(out, "c.mp4")}
	if !inOrder(mp4, floor) {
		t.Fatalf("mp4 argv %q\nlacks the floor, in order: %q", mp4, floor)
	}
	if !inOrder(mp4, []string{"-framerate", "60", "-i", filepath.Join(refs, "r%05d.png"), "-vf"}) ||
		!slices.Contains(mp4, toYUV) || !strings.HasPrefix(toYUV, "scale=out_color_matrix=bt709:out_range=tv:flags=accurate_rnd+full_chroma_int+lanczos,") {
		t.Fatalf("mp4 input or scale: %q", mp4)
	}
	if !inOrder(mp4, []string{"-crf", "24", "-x264-params", "chroma-qp-offset=-6"}) {
		t.Fatalf("mp4 quality: %q", mp4)
	}

	if err := Encode(ctx, "webm", refs, 60, 4, 2, filepath.Join(out, "c.webm")); err != nil {
		t.Fatal(err)
	}
	webm := enginetest.Calls(t, ffmpeg)[1]
	if !inOrder(webm, []string{"-vf", toYUV, "-c:v", "libsvtav1", "-preset", "4", "-crf", "32", "-pix_fmt", "yuv420p", "-color_trc", "bt709", "-an"}) {
		t.Fatalf("webm argv %q", webm)
	}

	if err := Encode(ctx, "gif", refs, 60, 4, 2, filepath.Join(out, "c.gif")); err != nil {
		t.Fatal(err)
	}
	gif := enginetest.Calls(t, ffmpeg)[2]
	if !slices.ContainsFunc(gif, func(a string) bool { return strings.HasPrefix(a, "fps=50,") }) {
		t.Fatalf("gif argv %q", gif)
	}

	if err := Encode(ctx, "webp", refs, 60, 4, 2, filepath.Join(out, "c.webp")); err != nil {
		t.Fatal(err)
	}
	webp := enginetest.Calls(t, magick)[0]
	if !inOrder(webp, []string{"-delay", "1x60", "-loop", "0", filepath.Join(refs, "r00000.png"), filepath.Join(refs, "r00002.png"),
		"-define", "webp:lossless=false", "-quality", "90", "WEBP:" + filepath.Join(out, "c.webp")}) {
		t.Fatalf("webp argv %q", webp)
	}

	if err := Encode(ctx, "poster", refs, 60, 4, 2, filepath.Join(out, "c.poster.png")); err != nil {
		t.Fatal(err)
	}
	if n := len(enginetest.Calls(t, ffmpeg)) + len(enginetest.Calls(t, magick)); n != 4 {
		t.Fatalf("poster ran an engine: %d calls", n)
	}
	a, _ := os.ReadFile(filepath.Join(refs, "r00000.png"))
	b, _ := os.ReadFile(filepath.Join(out, "c.poster.png"))
	if !bytes.Equal(a, b) {
		t.Fatal("the poster is not the first frame's bytes")
	}

	if err := Encode(ctx, "tiff", refs, 60, 4, 2, filepath.Join(out, "c.tiff")); err == nil {
		t.Fatal("an unknown format encoded")
	}
	if err := Encode(ctx, "mp4", refs, 60, 5, 2, filepath.Join(out, "odd.mp4")); err == nil || !strings.Contains(err.Error(), "even") {
		t.Fatalf("odd width: err = %v", err)
	}
}

func TestRequireEncoders(t *testing.T) {
	enginetest.Stub(t, "ffmpeg", `echo " V....D libx264              libx264 H.264"`)
	ctx := context.Background()
	err := requireEncoders(ctx, []string{"mp4", "webm"})
	if err == nil || err.Error() != "ffmpeg lacks encoder libsvtav1 (clip webm)" {
		t.Fatalf("err = %v", err)
	}
	if err := requireEncoders(ctx, []string{"mp4"}); err != nil {
		t.Fatal(err)
	}
}

// TestEncodeMP4MeetsFloor encodes with the real ffmpeg and reads the mp4
// back: checkMP4's floor, and the BT.709 tags, which an encoder option
// alone leaves unknown when the filtered frames carry none.
func TestEncodeMP4MeetsFloor(t *testing.T) {
	if _, err := engine.Resolve("ffmpeg"); err != nil {
		t.Skip(err)
	}
	if err := requireEncoders(context.Background(), []string{"mp4"}); err != nil {
		t.Skip(err)
	}
	refs := t.TempDir()
	for k := range 3 {
		writePNG(t, filepath.Join(refs, fmt.Sprintf("r%05d.png", k)), solid(16, 16, red))
	}
	out := filepath.Join(t.TempDir(), "c.mp4")
	ctx := context.Background()
	if err := Encode(ctx, "mp4", refs, 60, 16, 16, out); err != nil {
		t.Fatal(err)
	}
	if err := checkMP4(ctx, out, 3, 60); err != nil {
		t.Fatal(err)
	}
	res, err := engine.Run(ctx, engine.Cmd{Engine: "ffprobe", Args: []string{"-v", "error",
		"-show_entries", "stream=color_space,color_primaries,color_transfer,color_range", "-of", "csv=p=0", out}})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(res.Stdout)); got != "tv,bt709,bt709,bt709" {
		t.Fatalf("range, space, transfer, primaries = %s", got)
	}
}
