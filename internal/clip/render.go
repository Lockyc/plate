package clip

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/lockyc/plate/internal/atomicfile"
	"github.com/lockyc/plate/internal/outdir"
	"github.com/lockyc/plate/internal/video"
)

// Measurement is the render's sidecar, <name>.json: the video's measurement
// and the span of the master it shows. Trim is the master span before
// IdleCuts; RemovedMS is how much of it the cuts dropped.
type Measurement struct {
	video.Measurement
	Trim      Window
	RemovedMS float64
}

// MarshalJSON writes the sidecar's fields in their documented order: the
// trim sits between the video's duration and its formats.
func (m Measurement) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Name           string                        `json:"name"`
		Width          int                           `json:"width"`
		Height         int                           `json:"height"`
		FPS            int                           `json:"fps"`
		Frames         int                           `json:"frames"`
		DurationMS     float64                       `json:"duration_ms"`
		Trim           Window                        `json:"trim"`
		RemovedMS      float64                       `json:"removed_ms"`
		Formats        map[string]video.FormatResult `json:"formats"`
		Sharpness      float64                       `json:"sharpness"`
		LoopSeam       float64                       `json:"loop_seam"`
		LoopHeadFrames int                           `json:"loop_head_frames"`
		LoopTailFrames int                           `json:"loop_tail_frames"`
	}{m.Name, m.Width, m.Height, m.FPS, m.Frames, m.DurationMS, m.Trim, m.RemovedMS,
		m.Formats, m.Sharpness, m.LoopSeam, m.LoopHeadFrames, m.LoopTailFrames})
}

// RenderOptions are plate clip render's flags.
type RenderOptions struct {
	Script, Master, Events, Out, CaptionCSS string
}

// Render turns a master recording and its event log into the script's
// clip in o.Out. It refuses everything it can before decoding anything.
// A log or master that fails its checks leaves an earlier run's outputs
// alone; once both pass, those outputs are removed so a later failure
// leaves none that look current. The new outputs are published only once
// every check has passed.
// Warnings are written to warn.
func Render(ctx context.Context, o RenderOptions, warn io.Writer) (Measurement, error) {
	s, err := Load(o.Script)
	if err != nil {
		return Measurement{}, err
	}
	out, _, err := outdir.Make(o.Out, "plate-clip-")
	if err != nil {
		return Measurement{}, err
	}
	ev, err := os.Open(o.Events)
	if err != nil {
		return Measurement{}, err
	}
	l, err := ParseEvents(ev, s)
	ev.Close()
	if err != nil {
		return Measurement{}, err
	}
	m, err := ProbeMaster(ctx, o.Master)
	if err != nil {
		return Measurement{}, err
	}
	msg, err := l.CheckMaster(m)
	if err != nil {
		return Measurement{}, err
	}
	if msg != "" {
		fmt.Fprintf(warn, "plate clip render: warning: %s\n", msg)
	}
	if err := video.RemoveOutputs(out, s.Name); err != nil {
		return Measurement{}, err
	}
	var css string
	if o.CaptionCSS != "" {
		b, err := os.ReadFile(o.CaptionCSS)
		if err != nil {
			return Measurement{}, err
		}
		css = string(b)
	}
	if err := video.RequireEncoders(ctx, s.Output.Formats); err != nil {
		return Measurement{}, err
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
	fps := *s.FPS

	actDir, err := work("activity")
	if err != nil {
		return Measurement{}, err
	}
	act, err := ExtractActivity(ctx, o.Master, fps, actDir)
	if err != nil {
		return Measurement{}, err
	}
	l, kbWarns := TimeKeyboard(s, l, act, fps)
	for _, msg := range kbWarns {
		fmt.Fprintf(warn, "plate clip render: warning: %s\n", msg)
	}
	head := Settle
	if s.Camera.Mode == "follow" {
		head = FollowHead()
	}
	activity := Activity(act)
	trim := TrimWindow(l, activity, fps, head)
	if trim.Frames(fps) <= 0 {
		return Measurement{}, errEmptyWindow
	}

	masterDir, err := work("master")
	if err != nil {
		return Measurement{}, err
	}
	frames, err := ExtractFrames(ctx, o.Master, m.Colour, trim, fps, masterDir)
	if err != nil {
		return Measurement{}, err
	}
	tr0 := NewTrack(l)
	lay0, err := NewLayout(s, l, tr0)
	if err != nil {
		return Measurement{}, err
	}
	moves, err := FollowMoves(s, l, tr0, lay0, trim)
	if err != nil {
		return Measurement{}, err
	}
	frames, cuts, err := ApplyCuts(frames, trim, fps, IdleCuts(s, l, activity, trim, fps, moves),
		func(msg string) { fmt.Fprintf(warn, "plate clip render: warning: %s\n", msg) })
	if err != nil {
		return Measurement{}, err
	}
	// From here the log and the window are on the cut timeline.
	l, w := CompressLog(l, cuts), Compress(trim, cuts)

	tr := NewTrack(l)
	lay, err := NewLayout(s, l, tr)
	if err != nil {
		return Measurement{}, err
	}
	views, err := Path(s, l, tr, lay, w)
	if err != nil {
		return Measurement{}, err
	}
	ops, specs, warnings := Timeline(s, l, lay, views, w)
	for _, msg := range warnings {
		fmt.Fprintf(warn, "plate clip render: warning: %s\n", msg)
	}
	spriteDir, err := work("sprites")
	if err != nil {
		return Measurement{}, err
	}
	sprites, err := RenderSprites(ctx, specs, css, spriteDir)
	if err != nil {
		return Measurement{}, err
	}

	refDir, err := work("refs")
	if err != nil {
		return Measurement{}, err
	}
	refs, lc, err := Compose(ctx, frames, fps, l.Meta.Scale, lay, views, ops, sprites, s.Output.Loop, refDir)
	if err != nil {
		return Measurement{}, err
	}

	meas := Measurement{Trim: trim, RemovedMS: Removed(cuts), Measurement: video.Measurement{Name: s.Name, Width: lay.W, Height: lay.H, FPS: fps,
		Frames: len(refs), DurationMS: float64(len(refs)) * 1000 / float64(fps),
		Formats: map[string]video.FormatResult{}, LoopHeadFrames: lc.Head, LoopTailFrames: lc.Tail}}
	var files []string
	for _, f := range s.Output.Formats {
		name := s.Name + "." + video.Ext(f)
		p := filepath.Join(tmp, name)
		if f == "poster" {
			err = video.Poster(refs, min(lc.Head, len(refs)-1), p)
		} else {
			err = video.Encode(ctx, f, refDir, fps, lay.W, lay.H, p)
		}
		if err != nil {
			return Measurement{}, fmt.Errorf("%s: %w", f, err)
		}
		st, err := os.Stat(p)
		if err != nil {
			return Measurement{}, err
		}
		meas.Formats[f] = video.FormatResult{File: name, Bytes: st.Size()}
		files = append(files, name)
	}

	if meas.LoopSeam, err = video.LoopSeam(refs); err != nil {
		return Measurement{}, err
	}
	if err := video.ScoreSharpness(ctx, &meas.Measurement, tmp, refs, lc, work); err != nil {
		return Measurement{}, err
	}
	if err := video.Check(ctx, meas.Measurement, tmp, video.Checks{BudgetKB: s.Output.BudgetKB, Seam: true}); err != nil {
		return meas, err
	}
	sidecar, err := json.MarshalIndent(meas, "", "  ")
	if err != nil {
		return Measurement{}, err
	}
	if err := atomicfile.WriteFile(filepath.Join(tmp, s.Name+".json"), append(sidecar, '\n'), 0o644); err != nil {
		return Measurement{}, err
	}
	if err := video.Publish(tmp, out, s.Name, append(files, s.Name+".json")); err != nil {
		return Measurement{}, err
	}
	return meas, nil
}
