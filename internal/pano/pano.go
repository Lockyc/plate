package pano

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/lockyc/plate/internal/atomicfile"
	"github.com/lockyc/plate/internal/cli"
	"github.com/lockyc/plate/internal/engine"
	"github.com/lockyc/plate/internal/insv"
	"github.com/lockyc/plate/internal/outdir"
	"github.com/lockyc/plate/internal/video"
)

const usage = "pano [--every 30s | --video] [--width 5760] [--mode level|lock] [--centre back|front] [--from 0] [--to end] --out <dir> <either .insv>"

// firstFrame is how far into a stretch --every takes its first frame, so a
// stretch shorter than twice it gives one frame at its middle.
const firstFrame = 5.0

// resendAngle is how far, in radians, a --video frame's rotation must have
// turned from the last one sent to v360 before it is sent again: each send
// rebuilds v360's whole lookup table three times. 0.3° keeps the horizon
// within 0.3° of the frame's own.
const resendAngle = 0.3 * math.Pi / 180

// Main is `plate pano`: an Insta360 clip's two lens files stitched into a
// level equirect, as a JPEG every --every or as one video, with the
// rotation v360 applied to each frame in pano.tsv.
func Main(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := cli.Flags("pano", usage, stderr)
	every := fs.Duration("every", 30*time.Second, "write a frame this often, named by its wall-clock time to the millisecond (at least one frame interval)")
	asVideo := fs.Bool("video", false, "write the stretch as pano.mp4, with the front file's sound, instead of frames")
	width := cli.Int(fs, "width", 5760, "equirect width in pixels (even); the height is half")
	mode := fs.String("mode", "level", "level: undo tilt and the heading's shake; lock: undo all rotation")
	centre := fs.String("centre", "back", "the lens whose axis is the picture's centre: back (the _10_ file) or front (_00_)")
	from := cli.Float(fs, "from", 0, "start, seconds into the clip")
	to := cli.Float(fs, "to", 0, "end, seconds into the clip (0: the end)")
	out := fs.String("out", "", "destination directory (created if missing)")
	rest, code, ok := cli.Parse(fs, args, 1)
	if !ok {
		return code
	}
	everySet := false
	fs.Visit(func(f *flag.Flag) { everySet = everySet || f.Name == "every" })
	switch {
	case *out == "":
		return cli.Usage(stderr, "pano", "--out is required")
	case *asVideo && everySet:
		return cli.Usage(stderr, "pano", "--every and --video are each a whole output: pass one")
	case *every <= 0:
		return cli.Usage(stderr, "pano", "--every %v: want a positive duration", *every)
	case *centre != "back" && *centre != "front":
		return cli.Usage(stderr, "pano", "--centre %q: want back or front", *centre)
	case *width < 2 || *width%2 != 0:
		return cli.Usage(stderr, "pano", "--width %d: want an even number of pixels", *width)
	case *from < 0 || *to < 0 || (*to != 0 && *to <= *from):
		return cli.Usage(stderr, "pano", "--from %v --to %v: want 0 <= from < to", *from, *to)
	}
	m, ok := map[string]Mode{"level": Level, "lock": Lock}[*mode]
	if !ok {
		return cli.Usage(stderr, "pano", "--mode %q: want level or lock", *mode)
	}
	j := job{mode: m, width: *width, from: *from, to: *to, every: every.Seconds(), video: *asVideo, back: *centre == "back"}
	if err := j.run(ctx, rest[0], *out, stdout); err != nil {
		return cli.Fail(stderr, "pano", err)
	}
	return 0
}

type job struct {
	mode            Mode
	width           int
	from, to, every float64
	video, back     bool
}

// applied is the rotation v360 is given for r: r itself with the picture
// centred on lens 0, else turned a half turn about the stabilised picture's
// down axis first, so lens 1's heading is the centre. It is a rotation, not
// a map, so one set of cached maps serves both centres.
func (j job) applied(r Rot) Rot {
	if !j.back {
		return r
	}
	var out Rot
	out.Yaw, out.Pitch, out.Roll = toZYX(rotQ(r).mul(axisAngle([3]float64{0, 0, 1}, math.Pi)))
	return out
}

func (j job) run(ctx context.Context, path, out string, stdout io.Writer) error {
	front, back, err := insv.Pair(path)
	if err != nil {
		return err
	}
	if _, err := clockName(front, 0); err != nil {
		return err
	}
	meta, err := insv.Read(front)
	if err != nil {
		return err
	}
	if _, ok := lensFOV[meta.Model]; !ok {
		return fmt.Errorf("%s: a %q's lenses are not calibrated in plate", front, meta.Model)
	}
	fps, rate, dur, err := probe(ctx, front)
	if err != nil {
		return err
	}
	end := dur
	if j.to != 0 {
		end = math.Min(j.to, dur)
	}
	if j.from >= end {
		return fmt.Errorf("--from %v is past the clip's end at %.3f s", j.from, dur)
	}
	if !j.video && j.every < 1/fps-1e-9 {
		return fmt.Errorf("--every %gs is under one frame interval (%.4f s at %s fps)", j.every, 1/fps, rate)
	}
	dir, made, err := outdir.Make(out, "pano-")
	if err != nil {
		return err
	}
	maps, err := cachedMaps(meta, j.width)
	if err == nil {
		if j.video {
			err = j.writeVideo(ctx, front, back, maps, meta, fps, rate, end, dir, stdout)
		} else {
			err = j.writeFrames(ctx, front, back, maps, meta, fps, end, dir, stdout)
		}
	}
	if err != nil && made {
		os.RemoveAll(dir)
	}
	return err
}

// everyTimes are --every's frame times in [from, end) of a clip at fps: the
// first min(firstFrame, half the stretch) in, then one every every, each on
// the frame nearest it, never past the clip's last frame and never one
// frame twice.
func everyTimes(from, end, every, fps float64) []float64 {
	last := math.Ceil(end*fps-1e-9) - 1
	var times []float64
	for t := from + math.Min(firstFrame, (end-from)/2); t < end; t += every {
		f := math.Min(math.Round(t*fps), last)
		if n := len(times); n > 0 && f/fps <= times[n-1] {
			continue
		}
		times = append(times, f/fps)
	}
	return times
}

func (j job) writeFrames(ctx context.Context, front, back, maps string, meta insv.Meta, fps, end float64, dir string, stdout io.Writer) error {
	times := everyTimes(j.from, end, j.every, fps)
	rots, err := Rotations(meta.IMU, times, j.mode)
	if err != nil {
		return err
	}
	names := make([]string, len(times))
	rows := make([]string, len(times))
	for k, t := range times {
		rots[k] = j.applied(rots[k])
		clock, _ := clockName(front, t)
		names[k] = strings.NewReplacer(":", "", ".", "_").Replace(clock) + ".jpg"
		rows[k] = row(names[k], t, clock, rots[k])
	}
	if err := outdir.RefuseExisting(dir, append([]string{"pano.tsv"}, names...)); err != nil {
		return err
	}
	for k, t := range times {
		dst := filepath.Join(dir, names[k])
		args := append(lensInputs(front, back, seek(t, fps), ""), mapInputs(maps)...)
		args = append(args, "-filter_complex", graph(level(rots[k])), "-frames:v", "1", "-q:v", "2", "-update", "1", dst)
		if _, err := engine.Run(ctx, engine.Cmd{Engine: "ffmpeg", Args: args, Inputs: []string{front, back}, Outputs: []string{dst}}); err != nil {
			return err
		}
		fmt.Fprintln(stdout, dst)
	}
	return writeTSV(filepath.Join(dir, "pano.tsv"), rows)
}

func (j job) writeVideo(ctx context.Context, front, back, maps string, meta insv.Meta, fps float64, rate string, end float64, dir string, stdout io.Writer) error {
	dst := filepath.Join(dir, "pano.mp4")
	if err := outdir.RefuseExisting(dir, []string{"pano.mp4", "pano.tsv"}); err != nil {
		return err
	}
	first := int(math.Ceil(j.from*fps - 1e-6))
	var times []float64
	for k := first; float64(k)/fps < end-1e-6; k++ {
		times = append(times, float64(k)/fps)
	}
	rots, err := Rotations(meta.IMU, times, j.mode)
	if err != nil {
		return err
	}
	for k := range rots {
		rots[k] = j.applied(rots[k])
	}
	send := resent(rots, resendAngle)
	var cmds strings.Builder
	rows := make([]string, len(times))
	shown := 0
	for k, t := range times {
		if send[k] {
			shown = k
			r := degrees(rots[k])
			// Each command reconfigures v360; with reset_rot the three
			// angles together replace the last rotation sent.
			fmt.Fprintf(&cmds, "%.6f v360@level yaw %.4f, v360@level pitch %.4f, v360@level roll %.4f;\n", t-times[0], r[0], r[1], r[2])
		}
		clock, _ := clockName(front, t)
		rows[k] = row("pano.mp4", t, clock, rots[shown])
	}
	cmdFile, err := os.CreateTemp("", "pano-commands-*.txt")
	if err != nil {
		return err
	}
	defer os.Remove(cmdFile.Name())
	if _, err := cmdFile.WriteString(cmds.String()); err != nil {
		cmdFile.Close()
		return err
	}
	if err := cmdFile.Close(); err != nil {
		return err
	}
	dur := strconv.FormatFloat(float64(len(times))/fps, 'f', 6, 64)
	args := append(lensInputs(front, back, seek(times[0], fps), dur), mapInputs(maps)...)
	r0 := degrees(rots[0])
	rot := fmt.Sprintf("sendcmd=f=%s,v360@level=e:e:yaw=%.4f:pitch=%.4f:roll=%.4f:reset_rot=1:interp=cubic", filterPath(cmdFile.Name()), r0[0], r0[1], r0[2])
	args = append(args, "-filter_complex", graph(rot)+"[v]", "-map", "[v]")
	args = append(args, video.H264(rate, nil, true)...)
	args = append(args, dst)
	if _, err := engine.Run(ctx, engine.Cmd{Engine: "ffmpeg", Args: args, Timeout: 24 * time.Hour,
		Inputs: []string{front, back}, Outputs: []string{dst}}); err != nil {
		return err
	}
	fmt.Fprintln(stdout, dst)
	return writeTSV(filepath.Join(dir, "pano.tsv"), rows)
}

// seek is ffmpeg's -ss for the frame at t: a quarter frame early, so it
// lands on that frame whatever the rounding of t's decimal form.
func seek(t, fps float64) string {
	return strconv.FormatFloat(math.Max(0, t-0.25/fps), 'f', 6, 64)
}

// lensInputs are ffmpeg's inputs 0 (front) and 1 (back), each from ss, for
// dur seconds when dur is set.
func lensInputs(front, back, ss, dur string) []string {
	a := []string{"-hide_banner", "-loglevel", "error", "-nostdin"}
	for _, f := range []string{front, back} {
		a = append(a, "-ss", ss)
		if dur != "" {
			a = append(a, "-t", dur)
		}
		a = append(a, "-i", f)
	}
	return a
}

// mapInputs are ffmpeg's inputs 2 to 6: mapFiles in maps.
func mapInputs(maps string) []string {
	var a []string
	for _, f := range mapFiles {
		a = append(a, "-i", filepath.Join(maps, f))
	}
	return a
}

// graph stitches inputs 0 (front) and 1 (back) through the maps of
// mapInputs, blending lens 0 over lens 1 by its weight, then rot.
func graph(rot string) string {
	return "[0:v][2][3]remap[f];[1:v][4][5]remap[b];[6]format=gray[m];[f][m]alphamerge[fa];" +
		"[b][fa]overlay=format=yuv444," + rot
}

// level is the v360 filter that applies r to a whole equirect.
func level(r Rot) string {
	d := degrees(r)
	return fmt.Sprintf("v360=e:e:yaw=%.4f:pitch=%.4f:roll=%.4f:interp=cubic", d[0], d[1], d[2])
}

func degrees(r Rot) [3]float64 {
	return [3]float64{r.Yaw * 180 / math.Pi, r.Pitch * 180 / math.Pi, r.Roll * 180 / math.Pi}
}

func row(file string, t float64, clock string, r Rot) string {
	d := degrees(r)
	return fmt.Sprintf("%s\t%.3f\t%s\t%.2f\t%.2f\t%.2f", file, t, clock, d[0], d[1], d[2])
}

func writeTSV(path string, rows []string) error {
	body := "file\tt\tclock\tyaw\tpitch\troll\n" + strings.Join(rows, "\n") + "\n"
	return atomicfile.WriteFile(path, []byte(body), 0o644)
}

// filterPath escapes a path for a filtergraph option value.
func filterPath(p string) string {
	r := strings.NewReplacer(`\`, `\\\\`, `'`, `\\\'`, `:`, `\\:`, `,`, `\,`, `;`, `\;`, `[`, `\[`, `]`, `\]`)
	return r.Replace(p)
}

var startName = regexp.MustCompile(`^VID_(\d{8}_\d{6})_`)

// clockName is the wall-clock time, HH:MM:SS.mmm to the nearest
// millisecond, t seconds into the clip whose file name states its start
// (VID_YYYYMMDD_HHMMSS_…).
func clockName(path string, t float64) (string, error) {
	m := startName.FindStringSubmatch(filepath.Base(path))
	if m == nil {
		return "", fmt.Errorf("%s: the name does not state the clip's start (VID_YYYYMMDD_HHMMSS_…), which names the frames", path)
	}
	start, err := time.Parse("20060102_150405", m[1])
	if err != nil {
		return "", fmt.Errorf("%s: %w", path, err)
	}
	return start.Add(time.Duration(math.Round(t*1000)) * time.Millisecond).Format("15:04:05.000"), nil
}

// probe reads the clip's video frame rate (as a number and in ffmpeg's
// form) and duration from the file itself: the trailer's rate is measured,
// the stream's is exact.
func probe(ctx context.Context, path string) (fps float64, rate string, dur float64, err error) {
	res, err := engine.Run(ctx, engine.Cmd{Engine: "ffprobe", Inputs: []string{path}, Args: []string{"-v", "error",
		"-select_streams", "v:0", "-show_entries", "stream=r_frame_rate,duration", "-of", "json", path}})
	if err != nil {
		return 0, "", 0, err
	}
	var p struct {
		Streams []struct {
			Rate     string `json:"r_frame_rate"`
			Duration string `json:"duration"`
		} `json:"streams"`
	}
	if err := json.Unmarshal(res.Stdout, &p); err != nil || len(p.Streams) == 0 {
		return 0, "", 0, fmt.Errorf("ffprobe: %s has no readable video stream", path)
	}
	s := p.Streams[0]
	num, den, ok := strings.Cut(s.Rate, "/")
	n, err1 := strconv.ParseFloat(num, 64)
	d, err2 := strconv.ParseFloat(den, 64)
	dur, err3 := strconv.ParseFloat(s.Duration, 64)
	if !ok || errors.Join(err1, err2, err3) != nil || n <= 0 || d <= 0 || dur <= 0 {
		return 0, "", 0, fmt.Errorf("ffprobe: %s: frame rate %q, duration %q", path, s.Rate, s.Duration)
	}
	return n / d, s.Rate, dur, nil
}

// rotQ is r as a quaternion: Rz(Yaw)·Ry(Pitch)·Rx(Roll).
func rotQ(r Rot) quat {
	return axisAngle([3]float64{0, 0, 1}, r.Yaw).mul(axisAngle([3]float64{0, 1, 0}, r.Pitch)).mul(axisAngle([3]float64{1, 0, 0}, r.Roll))
}

// qAngle is the angle of the turn from a to b.
func qAngle(a, b quat) float64 {
	d := a.conj().mul(b)
	return 2 * math.Atan2(math.Sqrt(d[1]*d[1]+d[2]*d[2]+d[3]*d[3]), math.Abs(d[0]))
}

// resent says which frames' rotations v360 is sent: the first, and each
// that has turned more than thresh radians from the last one sent.
func resent(rots []Rot, thresh float64) []bool {
	out := make([]bool, len(rots))
	var last quat
	for k, r := range rots {
		q := rotQ(r)
		if k == 0 || qAngle(last, q) > thresh {
			out[k], last = true, q
		}
	}
	return out
}
