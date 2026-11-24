package quality

import (
	"context"
	"flag"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"

	"github.com/lockyc/plate/internal/engine"
	"github.com/lockyc/plate/internal/insv"
	"github.com/lockyc/plate/internal/insv/insvtest"
	"github.com/lockyc/plate/internal/pano"
)

// helpers are steps a case runs in this process rather than as an engine:
// argv[0] names one, and it gets the rest of the expanded argv.
var helpers = map[string]func(args []string) error{
	"insv-pair":     insvPair,
	"track-walkers": trackWalkers,
}

// insvPair makes an X3 clip for a quality case from an equirect: the front
// lens file named last (VID_…_00_…) and its back lens file (_10_), each
// --seconds of the equirect seen through an ideal lens of --size² pixels at
// the field of view pano stitches an X3 with, back to back, the front one
// looking at the equirect's centre. The front file gets an X3 trailer: both
// lenses ideal (centred, the circle filling the frame, no residual turn)
// and a still IMU at 1 kHz held at --roll and --pitch degrees (a
// right-handed turn about the front lens's axis, then the nose up about the
// camera's right).
func insvPair(args []string) error {
	fs := flag.NewFlagSet("insv-pair", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	size := fs.Int("size", 1024, "lens frame side in pixels")
	seconds := fs.Float64("seconds", 3, "clip length")
	roll := fs.Float64("roll", 0, "degrees")
	pitch := fs.Float64("pitch", 0, "degrees")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 2 {
		return fmt.Errorf("insv-pair: want an equirect and the front lens file")
	}
	src, front := fs.Arg(0), fs.Arg(1)
	back := strings.Replace(front, "_00_", "_10_", 1)
	if back == front {
		return fmt.Errorf("insv-pair: %s is not a front lens file (_00_)", front)
	}
	const model = "Insta360 X3"
	fov, ok := pano.LensFOV(model)
	if !ok {
		return fmt.Errorf("insv-pair: pano has no field of view for a %s", model)
	}
	for _, l := range []struct {
		file string
		yaw  int
	}{{front, 0}, {back, 180}} {
		vf := fmt.Sprintf("v360=e:fisheye:h_fov=%g:v_fov=%g:w=%d:h=%d:yaw=%d:interp=cubic,format=yuv420p", fov, fov, *size, *size, l.yaw)
		a := []string{"-hide_banner", "-loglevel", "error", "-nostdin", "-y", "-loop", "1", "-i", src,
			"-t", strconv.FormatFloat(*seconds, 'f', -1, 64), "-r", "30", "-vf", vf, "-c:v", "libx264", "-crf", "12", "-f", "mp4", l.file}
		if _, err := engine.Run(context.Background(), engine.Cmd{Engine: "ffmpeg", Args: a, Inputs: []string{src}, Outputs: []string{l.file}}); err != nil {
			return err
		}
	}
	n := float64(*size)
	h := n / 2
	offset := fmt.Sprintf("2_%g_%g_%g_0_0_90_%g_%g_%g_0_0_90_%g_%g_0", h, h, h, h, n+h, h, 2*n, n)
	p, r := *pitch*math.Pi/180, *roll*math.Pi/180
	g := insv.StandardGravity
	accel := [3]float64{g * math.Sin(p), -g * math.Sin(r) * math.Cos(p), -g * math.Cos(r) * math.Cos(p)}
	m := insv.Meta{Model: model, Serial: "QUALITY", Width: *size, Height: *size, FPS: 30}
	for i := 0; float64(i) <= *seconds*1000; i++ {
		m.IMU = append(m.IMU, insv.IMU{T: float64(i) / 1000, Accel: accel})
	}
	return insvtest.Write(front, m, insvtest.Options{Offset: offset})
}
