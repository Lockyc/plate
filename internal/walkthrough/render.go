package walkthrough

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"time"

	"github.com/lockyc/plate/internal/atomicfile"
	"github.com/lockyc/plate/internal/outdir"
	"github.com/lockyc/plate/internal/video"
)

// formats are the outputs a walkthrough render writes, beside its sidecar.
var formats = []string{"mp4", "poster"}

// Measurement is the render's sidecar, <name>.json: the video's measurement
// and when each step started and ended, in ms on the video's clock.
type Measurement struct {
	video.Measurement
	Steps []StepTiming `json:"steps"`
}

// StepTiming is a step's span in the sidecar.
type StepTiming struct {
	StartMS float64 `json:"start_ms"`
	EndMS   float64 `json:"end_ms"`
}

// RenderOptions are plate walkthrough render's flags. Warmup paths are
// resolved against BaseURL.
type RenderOptions struct {
	Script, BaseURL, Out string
	Warmup               []string
}

// Render records the script against the app at o.BaseURL and writes its
// narrated MP4 (H.264 and AAC), its poster and its sidecar to o.Out. Once
// the script loads, an earlier run's outputs of its name are removed, so
// any later failure leaves none that look current. The new outputs are
// published only once every check has passed.
func Render(ctx context.Context, o RenderOptions) (Measurement, error) {
	s, err := Load(o.Script)
	if err != nil {
		return Measurement{}, err
	}
	out, _, err := outdir.Make(o.Out, "plate-walkthrough-")
	if err != nil {
		return Measurement{}, err
	}
	if err := video.RemoveOutputs(out, s.Name); err != nil {
		return Measurement{}, err
	}
	if err := video.RequireEncoders(ctx, formats); err != nil {
		return Measurement{}, err
	}
	dir := filepath.Dir(o.Script)
	durations := make([]time.Duration, len(s.Steps))
	var clips []string
	for i, st := range s.Steps {
		if st.Audio == "" {
			continue
		}
		p := filepath.Join(dir, st.Audio)
		if durations[i], err = probeDuration(ctx, p); err != nil {
			return Measurement{}, fmt.Errorf("step %d audio: %w", i+1, err)
		}
		clips = append(clips, p)
	}

	tmp, err := os.MkdirTemp(out, "."+s.Name+"-")
	if err != nil {
		return Measurement{}, err
	}
	defer os.RemoveAll(tmp)
	work := func(name string) (string, error) {
		d := filepath.Join(tmp, name)
		return d, os.Mkdir(d, 0o755)
	}
	frameDir, err := work("frames")
	if err != nil {
		return Measurement{}, err
	}
	rec, err := Record(ctx, s, o.BaseURL, o.Warmup, durations, frameDir)
	if err != nil {
		return Measurement{}, err
	}
	fps := *s.FPS
	totalMS := rec.Steps[len(rec.Steps)-1].EndMS
	shown := resample(rec.Frames, totalMS, fps)
	if len(shown) == 0 {
		return Measurement{}, errors.New("the recording is shorter than one frame")
	}
	refDir, err := work("refs")
	if err != nil {
		return Measurement{}, err
	}
	refs := make([]string, len(shown))
	for i, f := range shown {
		refs[i] = filepath.Join(refDir, fmt.Sprintf("r%05d.png", i))
		if err := os.Link(filepath.Join(rec.FramesDir, f), refs[i]); err != nil {
			return Measurement{}, err
		}
	}

	meas := Measurement{Measurement: video.Measurement{Name: s.Name, Width: s.Width, Height: s.Height, FPS: fps,
		Frames: len(refs), DurationMS: float64(len(refs)) * 1000 / float64(fps), Formats: map[string]video.FormatResult{}}}
	for _, sp := range rec.Steps {
		meas.Steps = append(meas.Steps, StepTiming(sp))
	}
	mp4 := s.Name + "." + video.Ext("mp4")
	poster := s.Name + "." + video.Ext("poster")
	if err := video.Encode(ctx, "mp4", refDir, fps, s.Width, s.Height, filepath.Join(tmp, mp4)); err != nil {
		return Measurement{}, fmt.Errorf("mp4: %w", err)
	}
	if err := video.Poster(refs, posterFrame(rec.Steps[0], fps, len(refs)), filepath.Join(tmp, poster)); err != nil {
		return Measurement{}, fmt.Errorf("poster: %w", err)
	}
	for f, name := range map[string]string{"mp4": mp4, "poster": poster} {
		st, err := os.Stat(filepath.Join(tmp, name))
		if err != nil {
			return Measurement{}, err
		}
		meas.Formats[f] = video.FormatResult{File: name, Bytes: st.Size()}
	}
	// The checks read the silent encode: Check holds the mp4 to one video
	// stream and its frame count, and the audio is muxed in after.
	if err := video.ScoreSharpness(ctx, &meas.Measurement, tmp, refs, video.LoopClose{}, work); err != nil {
		return Measurement{}, err
	}
	if err := video.Check(ctx, meas.Measurement, tmp, video.Checks{}); err != nil {
		return Measurement{}, err
	}

	starts := make([]float64, 0, len(clips))
	for i, st := range s.Steps {
		if st.Audio != "" {
			starts = append(starts, rec.Steps[i].StartMS+float64(audioDelay.Milliseconds()))
		}
	}
	audio := filepath.Join(tmp, "narration.m4a")
	if err := mixAudio(ctx, clips, starts, meas.DurationMS, audio); err != nil {
		return Measurement{}, fmt.Errorf("audio: %w", err)
	}
	narrated := filepath.Join(tmp, "narrated.mp4")
	if err := mux(ctx, filepath.Join(tmp, mp4), audio, narrated); err != nil {
		return Measurement{}, fmt.Errorf("mp4: %w", err)
	}
	if err := os.Rename(narrated, filepath.Join(tmp, mp4)); err != nil {
		return Measurement{}, err
	}
	st, err := os.Stat(filepath.Join(tmp, mp4))
	if err != nil {
		return Measurement{}, err
	}
	r := meas.Formats["mp4"]
	r.Bytes = st.Size()
	meas.Formats["mp4"] = r

	sidecar, err := json.MarshalIndent(meas, "", "  ")
	if err != nil {
		return Measurement{}, err
	}
	if err := atomicfile.WriteFile(filepath.Join(tmp, s.Name+".json"), append(sidecar, '\n'), 0o644); err != nil {
		return Measurement{}, err
	}
	if err := video.Publish(tmp, out, s.Name, []string{mp4, poster, s.Name + ".json"}); err != nil {
		return Measurement{}, err
	}
	return meas, nil
}

// resample is the frame file shown at each of the round(totalMS × fps /
// 1000) ticks of a constant-rate video: the latest frame drawn at or before
// the tick. Chrome sends a frame only when the page repaints, so a still
// page holds its last frame. frames are in time order, the first at 0.
func resample(frames []FrameStamp, totalMS float64, fps int) []string {
	if len(frames) == 0 {
		return nil
	}
	n := int(math.Round(totalMS * float64(fps) / 1000))
	out := make([]string, n)
	j := 0
	for i := range out {
		t := float64(i) * 1000 / float64(fps)
		for j+1 < len(frames) && frames[j+1].MS <= t {
			j++
		}
		out[i] = frames[j].File
	}
	return out
}

// posterFrame is the last frame of the first step: its page as it stands
// once its action has played, rather than the blank page the recording
// opens on.
func posterFrame(first StepSpan, fps, frames int) int {
	i := int(math.Ceil(first.EndMS*float64(fps)/1000)) - 1
	return max(0, min(i, frames-1))
}
