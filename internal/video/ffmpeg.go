package video

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"strconv"
	"strings"

	"github.com/lockyc/plate/internal/engine"
)

// Source is what a probe of a file's first picture stream says.
type Source struct {
	W, H int
	FPS  float64 // frames a second; 0 for a still
	Rate string  // FPS in ffmpeg's form, such as 30000/1001
	Dur  float64 // seconds; 0 for a still
}

// Probe reads path's first picture stream: its size and, unless still, its
// frame rate and duration, the stream's or else the container's. The rate
// is the stream's exact one, not a measured one.
func Probe(ctx context.Context, path string, still bool) (Source, error) {
	res, err := engine.Run(ctx, engine.Cmd{Engine: "ffprobe", Inputs: []string{path}, Args: []string{"-v", "error",
		"-select_streams", "v:0", "-show_entries", "stream=width,height,r_frame_rate,duration:format=duration", "-of", "json", path}})
	if err != nil {
		return Source{}, err
	}
	return parseProbe(path, res.Stdout, still)
}

func parseProbe(path string, out []byte, still bool) (Source, error) {
	var p struct {
		Streams []struct {
			W        int    `json:"width"`
			H        int    `json:"height"`
			Rate     string `json:"r_frame_rate"`
			Duration string `json:"duration"`
		} `json:"streams"`
		Format struct {
			Duration string `json:"duration"`
		} `json:"format"`
	}
	if err := json.Unmarshal(out, &p); err != nil || len(p.Streams) == 0 {
		return Source{}, fmt.Errorf("ffprobe: %s has no readable picture", path)
	}
	s := p.Streams[0]
	src := Source{W: s.W, H: s.H}
	if still {
		return src, nil
	}
	d := s.Duration
	if d == "" || d == "N/A" {
		d = p.Format.Duration
	}
	num, den, ok := strings.Cut(s.Rate, "/")
	n, err1 := strconv.ParseFloat(num, 64)
	dn, err2 := strconv.ParseFloat(den, 64)
	dur, err3 := strconv.ParseFloat(d, 64)
	if !ok || errors.Join(err1, err2, err3) != nil || n <= 0 || dn <= 0 || dur <= 0 {
		return Source{}, fmt.Errorf("ffprobe: %s: frame rate %q, duration %q", path, s.Rate, d)
	}
	src.FPS, src.Rate, src.Dur = n/dn, s.Rate, dur
	return src, nil
}

// Seek is ffmpeg's -ss for the frame at t: a quarter frame early, so it
// lands on that frame whatever the rounding of t's decimal form.
func Seek(t, fps float64) string {
	return strconv.FormatFloat(math.Max(0, t-0.25/fps), 'f', 6, 64)
}

// FrameTimes are the times of a video's frames at fps from the first at or
// after from to the last before end.
func FrameTimes(from, end, fps float64) []float64 {
	var times []float64
	for k := int(math.Ceil(from*fps - 1e-6)); float64(k)/fps < end-1e-6; k++ {
		times = append(times, float64(k)/fps)
	}
	return times
}

// FilterPath escapes a path for a filtergraph option value.
func FilterPath(p string) string {
	r := strings.NewReplacer(`\`, `\\\\`, `'`, `\\\'`, `:`, `\\:`, `,`, `\,`, `;`, `\;`, `[`, `\[`, `]`, `\]`)
	return r.Replace(p)
}

// Command is one sendcmd entry: at T seconds into the output, Args, such as
// "v360@view yaw 15.0000, v360@view pitch 3.0000".
type Command struct {
	T    float64
	Args string
}

// script is cmds as a sendcmd script for a video at fps, each sent a
// quarter frame before its T (never before 0), so it reaches the filter
// before the frame at T whatever the rounding of T's decimal form.
func script(cmds []Command, fps float64) string {
	var b strings.Builder
	for _, c := range cmds {
		fmt.Fprintf(&b, "%.6f %s;\n", math.Max(0, c.T-0.25/fps), c.Args)
	}
	return b.String()
}

// WriteCommands writes cmds as a sendcmd script to a new temporary file and
// returns its path, which the caller removes; FilterPath escapes it for
// sendcmd's f option.
func WriteCommands(cmds []Command, fps float64) (string, error) {
	f, err := os.CreateTemp("", "plate-sendcmd-*.txt")
	if err != nil {
		return "", err
	}
	_, err = f.WriteString(script(cmds, fps))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(f.Name())
		return "", err
	}
	return f.Name(), nil
}
