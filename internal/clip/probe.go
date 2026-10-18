package clip

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strconv"

	"github.com/lockyc/plate/internal/engine"
)

// Master is what the recording says about itself; times are milliseconds.
type Master struct {
	W, H     int
	FirstPTS float64
	Duration float64
}

// ProbeMaster reads the master's size, duration and the presentation time of
// its first frame, which event times are relative to.
func ProbeMaster(ctx context.Context, path string) (Master, error) {
	res, err := engine.Run(ctx, engine.Cmd{
		Engine: "ffprobe",
		Args: []string{"-v", "error", "-select_streams", "v:0",
			"-show_entries", "stream=width,height:format=duration",
			"-show_frames", "-read_intervals", "%+#1", "-of", "json", path},
		Inputs: []string{path},
	})
	if err != nil {
		return Master{}, err
	}
	var out struct {
		Streams []struct{ Width, Height int }
		Frames  []struct {
			PTSTime string `json:"pts_time"`
		}
		Format struct{ Duration string }
	}
	if err := json.Unmarshal(res.Stdout, &out); err != nil {
		return Master{}, fmt.Errorf("ffprobe: unreadable output: %w", err)
	}
	if len(out.Streams) == 0 || len(out.Frames) == 0 {
		return Master{}, fmt.Errorf("%s has no video frame", path)
	}
	pts, err := strconv.ParseFloat(out.Frames[0].PTSTime, 64)
	if err != nil {
		return Master{}, fmt.Errorf("%s: first frame has no pts_time", path)
	}
	dur, err := strconv.ParseFloat(out.Format.Duration, 64)
	if err != nil {
		return Master{}, fmt.Errorf("%s: no duration", path)
	}
	ms := func(s float64) float64 { return math.Round(s*1e6) / 1e3 }
	return Master{W: out.Streams[0].Width, H: out.Streams[0].Height, FirstPTS: ms(pts), Duration: ms(dur)}, nil
}
