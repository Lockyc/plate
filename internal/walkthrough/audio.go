package walkthrough

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/lockyc/plate/internal/engine"
)

// audioDelay is how long after its step starts a step's audio begins: the
// step's caption and cursor are on screen before the narration speaks.
const audioDelay = 300 * time.Millisecond

// mixRate is the narration track's sample rate; adelay counts its delays
// in samples at it, so a clip starts on the sample its ms asks for.
const mixRate = 48000

// probeDuration is how long the audio file at path plays, as ffprobe reads
// its container.
func probeDuration(ctx context.Context, path string) (time.Duration, error) {
	res, err := engine.Run(ctx, engine.Cmd{Engine: "ffprobe", Inputs: []string{path},
		Args: []string{"-v", "error", "-select_streams", "a:0", "-show_entries", "stream=codec_type:format=duration", "-of", "json", path}})
	if err != nil {
		return 0, err
	}
	var out struct {
		Streams []struct {
			Type string `json:"codec_type"`
		}
		Format struct{ Duration string }
	}
	if err := json.Unmarshal(res.Stdout, &out); err != nil {
		return 0, fmt.Errorf("ffprobe: unreadable output: %w", err)
	}
	if len(out.Streams) == 0 {
		return 0, fmt.Errorf("%s has no audio stream", path)
	}
	s, err := strconv.ParseFloat(out.Format.Duration, 64)
	if err != nil || s <= 0 || math.IsInf(s, 0) {
		return 0, fmt.Errorf("%s: no duration", path)
	}
	return time.Duration(s * float64(time.Second)), nil
}

// mixAudio writes to out one AAC track totalMS long: silence, with clips[i]
// laid over it from starts[i] ms. Clips that overlap are summed, never
// scaled down. With no clips the track is silent.
func mixAudio(ctx context.Context, clips []string, starts []float64, totalMS float64, out string) error {
	if len(clips) != len(starts) {
		return fmt.Errorf("%d starts for %d clips", len(starts), len(clips))
	}
	total := strconv.FormatFloat(totalMS/1000, 'f', 3, 64)
	layout := fmt.Sprintf("aresample=%d,aformat=sample_fmts=fltp:channel_layouts=stereo", mixRate)
	args := []string{"-hide_banner", "-loglevel", "error", "-nostdin",
		"-f", "lavfi", "-t", total, "-i", fmt.Sprintf("anullsrc=r=%d:cl=stereo", mixRate)}
	graph := []string{"[0:a]" + layout + "[a0]"}
	mix := "[a0]"
	for i, c := range clips {
		args = append(args, "-i", c)
		delay := int64(math.Round(starts[i] * mixRate / 1000))
		graph = append(graph, fmt.Sprintf("[%d:a]%s,adelay=delays=%dS:all=1[a%d]", i+1, layout, delay, i+1))
		mix += fmt.Sprintf("[a%d]", i+1)
	}
	// duration=first: the silence sets the length, so a clip running past
	// the end is cut there.
	graph = append(graph, fmt.Sprintf("%samix=inputs=%d:duration=first:normalize=0[mix]", mix, len(clips)+1))
	args = append(args, "-filter_complex", strings.Join(graph, ";"), "-map", "[mix]",
		"-c:a", "aac", "-b:a", "160k", "-t", total, out)
	_, err := engine.Run(ctx, engine.Cmd{Engine: "ffmpeg", Args: args, Inputs: clips, Outputs: []string{out}})
	return err
}

// mux writes video's one video stream and audio's one audio stream to out,
// both copied as they are.
func mux(ctx context.Context, video, audio, out string) error {
	_, err := engine.Run(ctx, engine.Cmd{Engine: "ffmpeg", Inputs: []string{video, audio}, Outputs: []string{out},
		Args: []string{"-hide_banner", "-loglevel", "error", "-nostdin", "-i", video, "-i", audio,
			"-map", "0:v:0", "-map", "1:a:0", "-c", "copy", "-movflags", "+faststart", out}})
	return err
}
