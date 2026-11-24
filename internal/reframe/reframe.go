// Package reframe cuts a flat view out of a 360 equirect: a clip from an
// equirect video, its sound kept, or a still from a video or an image, the
// view following a keyframed path of yaw, pitch and field of view.
package reframe

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
	"strconv"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/lockyc/plate/internal/cli"
	"github.com/lockyc/plate/internal/engine"
	"github.com/lockyc/plate/internal/video"
)

const usage = `reframe --path <path.toml> [--size 1080x1920] [--from 0] [--to end] [--at <s>] --out <out.mp4|out.jpg|out.png> <equirect video|image>

A 2:1 equirect video gives an .mp4 with the source's sound, cut to --from
and --to, or with --at a still. A 2:1 image gives a still.

path.toml is one or more keys, in time order:

  [[key]]
  t = 12.0     # seconds into the source
  yaw = 40     # degrees right of the equirect's centre
  pitch = -10  # degrees up
  fov = 80     # degrees across the output's width

Between keys each value eases with smoothstep; before the first key and
after the last the view holds. One key is a fixed view.
Yaw eases literally between keys: 350 to 10 turns 340°; write 370 for the short way.`

// maxFOV is the widest a view may be across either side of the output, in
// degrees: a flat view stretches its edges by 1/cos² of the half angle,
// 33 times at 160°.
const maxFOV = 160

// Key is one point on a path: at T seconds into the source, the view's
// centre Yaw degrees right of the equirect's centre and Pitch degrees up,
// FOV degrees across the output's width.
type Key struct {
	T     float64 `toml:"t"`
	Yaw   float64 `toml:"yaw"`
	Pitch float64 `toml:"pitch"`
	FOV   float64 `toml:"fov"`
}

// View is where the output looks at one moment.
type View struct{ Yaw, Pitch, FOV float64 }

// Path is a view's keys in strictly increasing time.
type Path []Key

func (k Key) name(i int) string {
	return fmt.Sprintf("key %d (t = %s)", i+1, strconv.FormatFloat(k.T, 'g', -1, 64))
}

func (k Key) view() View { return View{k.Yaw, k.Pitch, k.FOV} }

// At is the view at t: between two keys each value eased from one to the
// next by smoothstep, so a move starts and stops gently; held at the first
// key before it and at the last after it.
func (p Path) At(t float64) View {
	if t <= p[0].T {
		return p[0].view()
	}
	for i := 1; i < len(p); i++ {
		a, b := p[i-1], p[i]
		if t >= b.T {
			continue
		}
		u := (t - a.T) / (b.T - a.T)
		s := u * u * (3 - 2*u)
		mix := func(x, y float64) float64 { return x + (y-x)*s }
		return View{mix(a.Yaw, b.Yaw), mix(a.Pitch, b.Pitch), mix(a.FOV, b.FOV)}
	}
	return p[len(p)-1].view()
}

// LoadPath reads a path file: one or more [[key]] tables, each with t,
// yaw, pitch and fov, in strictly increasing t.
func LoadPath(file string) (Path, error) {
	var f struct {
		Key []Key `toml:"key"`
	}
	md, err := toml.DecodeFile(file, &f)
	if err != nil {
		return nil, err
	}
	if extra := md.Undecoded(); len(extra) > 0 {
		return nil, fmt.Errorf("%s: unknown fields %v (a key has t, yaw, pitch and fov)", file, extra)
	}
	if len(f.Key) == 0 {
		return nil, fmt.Errorf("%s: no [[key]]", file)
	}
	for i, k := range f.Key {
		for _, field := range []string{"t", "yaw", "pitch", "fov"} {
			if !definedAt(md, i, field) {
				return nil, fmt.Errorf("%s: %s lacks %s", file, k.name(i), field)
			}
		}
		switch {
		case i > 0 && k.T <= f.Key[i-1].T:
			return nil, fmt.Errorf("%s: %s is not after %s", file, k.name(i), f.Key[i-1].name(i-1))
		case k.T < 0:
			return nil, fmt.Errorf("%s: %s is before the source's start", file, k.name(i))
		case k.Pitch < -90 || k.Pitch > 90:
			return nil, fmt.Errorf("%s: %s: pitch %g: want -90 to 90", file, k.name(i), k.Pitch)
		case k.FOV <= 0 || k.FOV > maxFOV:
			return nil, fmt.Errorf("%s: %s: fov %g: want over 0 and at most %d", file, k.name(i), k.FOV, maxFOV)
		}
	}
	return f.Key, nil
}

// definedAt reports whether the i-th [[key]] sets field. toml.MetaData
// names an array of tables' fields without the table's index, so the keys
// are walked in document order, each "key" starting the next table.
func definedAt(md toml.MetaData, i int, field string) bool {
	n := -1
	for _, k := range md.Keys() {
		switch {
		case len(k) == 1 && k[0] == "key":
			n++
		case n == i && len(k) == 2 && k[0] == "key" && k[1] == field:
			return true
		}
	}
	return false
}

// vfov is the field of view, in degrees, across the height of a w×h flat
// view fov degrees across its width.
func vfov(fov float64, w, h int) float64 {
	return 2 * math.Atan(math.Tan(fov*math.Pi/360)*float64(h)/float64(w)) * 180 / math.Pi
}

// fits refuses a key whose fov across the width makes the view wider than
// maxFOV across the height at w×h.
func fits(p Path, w, h int) error {
	for i, k := range p {
		if v := vfov(k.FOV, w, h); v > maxFOV {
			return fmt.Errorf("%s: fov %g across %d px is %.0f° across %d px, over %d°", k.name(i), k.FOV, w, v, h, maxFOV)
		}
	}
	return nil
}

var (
	stills = map[string]bool{".jpg": true, ".jpeg": true, ".png": true}
	images = map[string]bool{".jpg": true, ".jpeg": true, ".png": true, ".tif": true, ".tiff": true, ".webp": true}
)

// Main is `plate reframe`.
func Main(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := cli.Flags("reframe", usage, stderr)
	pathFile := fs.String("path", "", "the view's keys (toml)")
	size := fs.String("size", "1080x1920", "output width x height in pixels")
	out := fs.String("out", "", "output: .mp4 from a video, or a .jpg or .png still")
	at := cli.Float(fs, "at", 0, "a still from a video: seconds into it")
	from := cli.Float(fs, "from", 0, "a clip's start, seconds into the source")
	to := cli.Float(fs, "to", 0, "a clip's end, seconds into the source (0: the end)")
	rest, code, ok := cli.Parse(fs, args, 1)
	if !ok {
		return code
	}
	set := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { set[f.Name] = true })
	in := rest[0]
	image := images[strings.ToLower(filepath.Ext(in))]
	ext := strings.ToLower(filepath.Ext(*out))
	still := stills[ext]
	switch {
	case *pathFile == "":
		return cli.Usage(stderr, "reframe", "--path is required")
	case *out == "":
		return cli.Usage(stderr, "reframe", "--out is required")
	case !still && ext != ".mp4":
		return cli.Usage(stderr, "reframe", "--out %s: want .mp4, .jpg or .png", *out)
	case image && set["at"]:
		return cli.Usage(stderr, "reframe", "a still from an image takes no --at")
	case image && (set["from"] || set["to"]):
		return cli.Usage(stderr, "reframe", "--from and --to cut a video; an image takes neither")
	case image && !still:
		return cli.Usage(stderr, "reframe", "an image gives a still: --out a .jpg or .png")
	case !image && still && !set["at"]:
		return cli.Usage(stderr, "reframe", "a still from a video needs --at")
	case !image && still && (set["from"] || set["to"]):
		return cli.Usage(stderr, "reframe", "a still takes --at, not --from or --to")
	case !image && !still && set["at"]:
		return cli.Usage(stderr, "reframe", "--at picks a still from a video; an .mp4 takes --from and --to")
	case *at < 0 || *from < 0 || *to < 0 || (*to != 0 && *to <= *from):
		return cli.Usage(stderr, "reframe", "--at %v --from %v --to %v: want 0 <= from < to and at >= 0", *at, *from, *to)
	}
	w, h, err := parseSize(*size, !still)
	if err != nil {
		return cli.Usage(stderr, "reframe", "%v", err)
	}
	p, err := LoadPath(*pathFile)
	if err != nil {
		return cli.Fail(stderr, "reframe", err)
	}
	if err := fits(p, w, h); err != nil {
		return cli.Fail(stderr, "reframe", fmt.Errorf("%s: %w", *pathFile, err))
	}
	j := job{path: p, w: w, h: h, at: *at, from: *from, to: *to}
	switch {
	case image:
		err = j.imageStill(ctx, in, *out)
	case still:
		err = j.videoStill(ctx, in, *out)
	default:
		err = j.clip(ctx, in, *out)
	}
	if err != nil {
		return cli.Fail(stderr, "reframe", err)
	}
	fmt.Fprintln(stdout, *out)
	return 0
}

func parseSize(s string, even bool) (w, h int, err error) {
	a, b, ok := strings.Cut(s, "x")
	w, err1 := strconv.Atoi(a)
	h, err2 := strconv.Atoi(b)
	switch {
	case !ok || err1 != nil || err2 != nil || w < 2 || h < 2:
		return 0, 0, fmt.Errorf("--size %q: want WIDTHxHEIGHT", s)
	case even && (w%2 != 0 || h%2 != 0):
		return 0, 0, fmt.Errorf("--size %s: a video needs even sides", s)
	}
	return w, h, nil
}

type job struct {
	path         Path
	w, h         int
	at, from, to float64
}

// source is what a probe of the input says.
type source struct {
	fps  float64
	rate string  // fps in ffmpeg's form
	dur  float64 // seconds; 0 for an image
}

// probe reads the input's picture size, refusing one that is not 2:1, and
// for a video its frame rate and duration.
func probe(ctx context.Context, in string, image bool) (source, error) {
	res, err := engine.Run(ctx, engine.Cmd{Engine: "ffprobe", Inputs: []string{in}, Args: []string{"-v", "error",
		"-select_streams", "v:0", "-show_entries", "stream=width,height,r_frame_rate,duration:format=duration", "-of", "json", in}})
	if err != nil {
		return source{}, err
	}
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
	if err := json.Unmarshal(res.Stdout, &p); err != nil || len(p.Streams) == 0 {
		return source{}, fmt.Errorf("ffprobe: %s has no readable picture", in)
	}
	s := p.Streams[0]
	if s.H == 0 || s.W != 2*s.H {
		return source{}, fmt.Errorf("%s is %dx%d, not a 2:1 equirect", in, s.W, s.H)
	}
	if image {
		return source{}, nil
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
		return source{}, fmt.Errorf("ffprobe: %s: frame rate %q, duration %q", in, s.Rate, d)
	}
	return source{fps: n / dn, rate: s.Rate, dur: dur}, nil
}

// inside refuses a key past the source's end.
func (j job) inside(src source) error {
	for i, k := range j.path {
		if k.T > src.dur {
			return fmt.Errorf("%s is past the source's end at %.3f s", k.name(i), src.dur)
		}
	}
	return nil
}

// flat is the v360 filter, named name, that cuts view v out of an
// equirect at w×h.
func (j job) flat(v View, name string) string {
	return fmt.Sprintf("v360%s=e:flat:yaw=%.4f:pitch=%.4f:h_fov=%.4f:v_fov=%.4f:w=%d:h=%d:interp=cubic",
		name, v.Yaw, v.Pitch, v.FOV, vfov(v.FOV, j.w, j.h), j.w, j.h)
}

var head = []string{"-hide_banner", "-loglevel", "error", "-nostdin"}

func (j job) still(ctx context.Context, in, out, ss string, v View) error {
	args := append([]string{}, head...)
	if ss != "" {
		args = append(args, "-ss", ss)
	}
	args = append(args, "-i", in, "-vf", j.flat(v, ""), "-frames:v", "1", "-update", "1")
	if e := strings.ToLower(filepath.Ext(out)); e == ".jpg" || e == ".jpeg" {
		args = append(args, "-q:v", "2")
	}
	args = append(args, out)
	_, err := engine.Run(ctx, engine.Cmd{Engine: "ffmpeg", Args: args, Inputs: []string{in}, Outputs: []string{out}})
	return err
}

func (j job) imageStill(ctx context.Context, in, out string) error {
	if len(j.path) != 1 {
		return fmt.Errorf("an image is one moment: its path takes one key, not %d", len(j.path))
	}
	if _, err := probe(ctx, in, true); err != nil {
		return err
	}
	return j.still(ctx, in, out, "", j.path[0].view())
}

func (j job) videoStill(ctx context.Context, in, out string) error {
	src, err := probe(ctx, in, false)
	if err != nil {
		return err
	}
	if err := j.inside(src); err != nil {
		return err
	}
	if j.at >= src.dur {
		return fmt.Errorf("--at %v is past the source's end at %.3f s", j.at, src.dur)
	}
	return j.still(ctx, in, out, seek(j.at, src.fps), j.path.At(j.at))
}

// seek is ffmpeg's -ss for the frame at t: a quarter frame early, so it
// lands on that frame whatever the rounding of t's decimal form.
func seek(t, fps float64) string {
	return strconv.FormatFloat(math.Max(0, t-0.25/fps), 'f', 6, 64)
}

func (j job) clip(ctx context.Context, in, out string) error {
	src, err := probe(ctx, in, false)
	if err != nil {
		return err
	}
	if err := j.inside(src); err != nil {
		return err
	}
	end := src.dur
	if j.to != 0 {
		end = math.Min(j.to, src.dur)
	}
	if j.from >= end {
		return fmt.Errorf("--from %v is past the source's end at %.3f s", j.from, src.dur)
	}
	first := int(math.Ceil(j.from*src.fps - 1e-6))
	var times []float64
	for k := first; float64(k)/src.fps < end-1e-6; k++ {
		times = append(times, float64(k)/src.fps)
	}
	vf := j.flat(j.path.At(times[0]), "@view")
	if cmds := commands(j.path, times, j.w, j.h, src.fps); cmds != "" {
		cmdFile, err := os.CreateTemp("", "reframe-commands-*.txt")
		if err != nil {
			return err
		}
		defer os.Remove(cmdFile.Name())
		if _, err := cmdFile.WriteString(cmds); err != nil {
			cmdFile.Close()
			return err
		}
		if err := cmdFile.Close(); err != nil {
			return err
		}
		vf = "sendcmd=f=" + filterPath(cmdFile.Name()) + "," + vf
	}
	dur := strconv.FormatFloat(float64(len(times))/src.fps, 'f', 6, 64)
	args := append(append([]string{}, head...), "-ss", seek(times[0], src.fps), "-t", dur, "-i", in,
		"-vf", vf, "-map", "0:v:0")
	args = append(args, video.H264(src.rate, nil, true)...)
	args = append(args, out)
	_, err = engine.Run(ctx, engine.Cmd{Engine: "ffmpeg", Args: args, Timeout: 24 * time.Hour,
		Inputs: []string{in}, Outputs: []string{out}})
	return err
}

// commands is the sendcmd script that moves v360@view along p over the
// frames at times: for each frame after the first, the values that differ
// from the frame before, sent a quarter frame before it is shown. Every
// value sent rebuilds v360's lookup table, so an unchanged one is never
// sent.
func commands(p Path, times []float64, w, h int, fps float64) string {
	var b strings.Builder
	last := p.At(times[0])
	for _, t := range times[1:] {
		v := p.At(t)
		var sends []string
		if v.Yaw != last.Yaw {
			sends = append(sends, fmt.Sprintf("v360@view yaw %.4f", v.Yaw))
		}
		if v.Pitch != last.Pitch {
			sends = append(sends, fmt.Sprintf("v360@view pitch %.4f", v.Pitch))
		}
		if v.FOV != last.FOV {
			sends = append(sends, fmt.Sprintf("v360@view h_fov %.4f, v360@view v_fov %.4f", v.FOV, vfov(v.FOV, w, h)))
		}
		if len(sends) > 0 {
			fmt.Fprintf(&b, "%.6f %s;\n", t-times[0]-0.25/fps, strings.Join(sends, ", "))
		}
		last = v
	}
	return b.String()
}

// filterPath escapes a path for a filtergraph option value.
func filterPath(p string) string {
	r := strings.NewReplacer(`\`, `\\\\`, `'`, `\\\'`, `:`, `\\:`, `,`, `\,`, `;`, `\;`, `[`, `\[`, `]`, `\]`)
	return r.Replace(p)
}
