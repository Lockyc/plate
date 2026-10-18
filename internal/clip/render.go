package clip

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strconv"

	"github.com/lockyc/plate/internal/atomicfile"
	"github.com/lockyc/plate/internal/engine"
	"github.com/lockyc/plate/internal/outdir"
)

// FormatResult is one written output: its file name in --out, its size,
// and for a video encode its Sharpness.
type FormatResult struct {
	File      string  `json:"file"`
	Bytes     int64   `json:"bytes"`
	Sharpness float64 `json:"sharpness,omitempty"`
}

// Measurement is the render's sidecar, <name>.json: the clip's shape, the
// span of the master it shows, each output, and the measures its checks
// hold. Sharpness is the least of the video encodes' (sharpFormats), and
// 1 when the clip has none. The loop frames are LoopClose's: the frames at
// each end the loop close altered. Trim is the master span before IdleCuts;
// RemovedMS is how much of it the cuts dropped.
type Measurement struct {
	Name           string                  `json:"name"`
	Width          int                     `json:"width"`
	Height         int                     `json:"height"`
	FPS            int                     `json:"fps"`
	Frames         int                     `json:"frames"`
	DurationMS     float64                 `json:"duration_ms"`
	Trim           Window                  `json:"trim"`
	RemovedMS      float64                 `json:"removed_ms"`
	Formats        map[string]FormatResult `json:"formats"`
	Sharpness      float64                 `json:"sharpness"`
	LoopSeam       float64                 `json:"loop_seam"`
	LoopHeadFrames int                     `json:"loop_head_frames"`
	LoopTailFrames int                     `json:"loop_tail_frames"`
}

// RenderOptions are plate clip render's flags.
type RenderOptions struct {
	Script, Master, Events, Out, CaptionCSS string
}

// outputs are the names a render of name may write into --out: one per
// format a script can ask for, and the sidecar.
func outputs(name string) []string {
	var out []string
	for _, f := range Formats {
		out = append(out, name+"."+Ext(f))
	}
	return append(out, name+".json")
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
	if err := removeOutputs(out, s.Name); err != nil {
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
	if err := requireEncoders(ctx, s.Output.Formats); err != nil {
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
	frames, cuts, err := ApplyCuts(frames, trim, fps, IdleCuts(s, l, activity, trim, fps))
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

	meas := Measurement{Name: s.Name, Width: lay.W, Height: lay.H, FPS: fps, Frames: len(refs),
		DurationMS: float64(len(refs)) * 1000 / float64(fps), Trim: trim, RemovedMS: Removed(cuts),
		Formats: map[string]FormatResult{}, LoopHeadFrames: lc.Head, LoopTailFrames: lc.Tail}
	var files []string
	for _, f := range s.Output.Formats {
		name := s.Name + "." + Ext(f)
		p := filepath.Join(tmp, name)
		if f == "poster" {
			err = writePoster(refs, lc, p)
		} else {
			err = Encode(ctx, f, refDir, fps, lay.W, lay.H, p)
		}
		if err != nil {
			return Measurement{}, fmt.Errorf("%s: %w", f, err)
		}
		st, err := os.Stat(p)
		if err != nil {
			return Measurement{}, err
		}
		meas.Formats[f] = FormatResult{File: name, Bytes: st.Size()}
		files = append(files, name)
	}

	if meas.LoopSeam, err = loopSeam(refs); err != nil {
		return Measurement{}, err
	}
	err = scoreSharpness(&meas, func(file string) (float64, error) {
		decDir, err := work("decoded-" + file)
		if err != nil {
			return 0, err
		}
		return sharpness(ctx, filepath.Join(tmp, file), refs, decDir)
	})
	if err != nil {
		return Measurement{}, err
	}
	if err := check(ctx, s, meas, tmp); err != nil {
		return meas, err
	}
	sidecar, err := json.MarshalIndent(meas, "", "  ")
	if err != nil {
		return Measurement{}, err
	}
	if err := atomicfile.WriteFile(filepath.Join(tmp, s.Name+".json"), append(sidecar, '\n'), 0o644); err != nil {
		return Measurement{}, err
	}
	if err := publish(tmp, out, s.Name, append(files, s.Name+".json")); err != nil {
		return Measurement{}, err
	}
	return meas, nil
}

// loopSeam is Seam between the composed clip's last frame and its first,
// where it loops.
func loopSeam(refs []string) (float64, error) {
	first, err := loadRGBA64(refs[0])
	if err != nil {
		return 0, err
	}
	last, err := loadRGBA64(refs[len(refs)-1])
	if err != nil {
		return 0, err
	}
	return Seam(first, last)
}

// sharpness is Sharpness of the encoded file against the composed frames'
// lossless 4:2:0 copy (Lossless420) at SampleFrames.
func sharpness(ctx context.Context, file string, refs []string, dir string) (float64, error) {
	idx := SampleFrames(len(refs))
	got, err := ExtractEncoded(ctx, file, idx, dir)
	if err != nil {
		return 0, err
	}
	want, err := Lossless420(ctx, filepath.Dir(refs[0]), idx, dir)
	if err != nil {
		return 0, err
	}
	return Sharpness(want, got)
}

// sharpFormats are the encodes Sharpness scores: the video ones, whose
// encoders lose fine detail. GIF and WebP are palette and still formats
// with their own losses, unscored.
var sharpFormats = []string{"mp4", "webm"}

// scoreSharpness scores every video encode in meas with score (given its
// file name), and sets meas.Sharpness to the least, or 1 with none.
func scoreSharpness(meas *Measurement, score func(file string) (float64, error)) error {
	meas.Sharpness = 1
	for _, f := range sharpFormats {
		r, ok := meas.Formats[f]
		if !ok {
			continue
		}
		v, err := score(r.File)
		if err != nil {
			return fmt.Errorf("%s: %w", f, err)
		}
		r.Sharpness = v
		meas.Formats[f] = r
		meas.Sharpness = math.Min(meas.Sharpness, v)
	}
	return nil
}

// checkSharpness holds each video encode to MinSharpness.
func checkSharpness(meas Measurement) error {
	for _, f := range sharpFormats {
		if r, ok := meas.Formats[f]; ok && r.Sharpness < MinSharpness {
			return fmt.Errorf("%s sharpness %.3f is below %.2f: the encode loses the frames' fine detail", f, r.Sharpness, MinSharpness)
		}
	}
	return nil
}

// check holds the encodes to the floor and the measures to their limits:
// the mp4 as ffprobe reads it, budget_kb, MinSharpness and MaxSeam.
func check(ctx context.Context, s *Script, meas Measurement, tmp string) error {
	if r, ok := meas.Formats["mp4"]; ok {
		if err := checkMP4(ctx, filepath.Join(tmp, r.File), meas.Frames, meas.FPS); err != nil {
			return err
		}
		if kb := (r.Bytes + 1023) / 1024; s.Output.BudgetKB > 0 && kb > int64(s.Output.BudgetKB) {
			return fmt.Errorf("mp4 is %d KB, above budget_kb %d", kb, s.Output.BudgetKB)
		}
	}
	if err := checkSharpness(meas); err != nil {
		return err
	}
	if meas.LoopSeam > MaxSeam {
		return fmt.Errorf("loop seam %.3g%% is above %.3g%%: the loop shows a cut", meas.LoopSeam, MaxSeam)
	}
	return nil
}

// checkMP4 reads the mp4 back with ffprobe: one H.264 4:2:0 stream of
// frames frames at a constant fps, and no audio.
func checkMP4(ctx context.Context, file string, frames, fps int) error {
	res, err := engine.Run(ctx, engine.Cmd{Engine: "ffprobe", Inputs: []string{file}, Args: []string{"-v", "error", "-count_frames",
		"-show_entries", "stream=codec_type,codec_name,pix_fmt,nb_read_frames,avg_frame_rate,r_frame_rate", "-of", "json", file}})
	if err != nil {
		return err
	}
	var out struct {
		Streams []struct {
			Type    string `json:"codec_type"`
			Codec   string `json:"codec_name"`
			PixFmt  string `json:"pix_fmt"`
			Frames  string `json:"nb_read_frames"`
			AvgRate string `json:"avg_frame_rate"`
			Rate    string `json:"r_frame_rate"`
		}
	}
	if err := json.Unmarshal(res.Stdout, &out); err != nil {
		return fmt.Errorf("ffprobe: unreadable output: %w", err)
	}
	var video int
	for _, st := range out.Streams {
		if st.Type == "audio" {
			return errors.New("mp4 has an audio stream")
		}
		if st.Type != "video" {
			continue
		}
		video++
		rate := strconv.Itoa(fps) + "/1"
		switch {
		case st.Codec != "h264":
			return fmt.Errorf("mp4 codec is %s, not h264", st.Codec)
		case st.PixFmt != "yuv420p":
			return fmt.Errorf("mp4 pix_fmt is %s, not yuv420p", st.PixFmt)
		case st.Frames != strconv.Itoa(frames):
			return fmt.Errorf("mp4 has %s frames, not %d", st.Frames, frames)
		case st.AvgRate != rate || st.Rate != rate:
			return fmt.Errorf("mp4 frame rate is %s (average %s), not %s", st.Rate, st.AvgRate, rate)
		}
	}
	if video != 1 {
		return fmt.Errorf("mp4 has %d video streams, not 1", video)
	}
	return nil
}

// removeOutputs removes from out every output a render of name may write.
func removeOutputs(out, name string) error {
	for _, n := range outputs(name) {
		if err := os.Remove(filepath.Join(out, n)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	return nil
}

// publish moves files (names in tmp) into out as name's outputs. It first
// removes every output a render of name may write, so none from an earlier
// run with other formats survives, then renames each file in, the sidecar
// JSON last, so a present sidecar marks a whole set. On any failure it
// removes what it moved; tmp is removed either way.
func publish(tmp, out, name string, files []string) error {
	defer os.RemoveAll(tmp)
	if err := removeOutputs(out, name); err != nil {
		return err
	}
	for _, f := range files {
		if _, err := os.Stat(filepath.Join(tmp, f)); err != nil {
			return err
		}
	}
	order := slices.Clone(files)
	sidecar := name + ".json"
	if i := slices.Index(order, sidecar); i >= 0 {
		order = append(slices.Delete(order, i, i+1), sidecar)
	}
	for i, f := range order {
		if err := os.Rename(filepath.Join(tmp, f), filepath.Join(out, f)); err != nil {
			for _, done := range order[:i] {
				os.Remove(filepath.Join(out, done))
			}
			return err
		}
	}
	return nil
}
