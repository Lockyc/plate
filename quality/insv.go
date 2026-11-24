package quality

import (
	"flag"
	"fmt"
	"io"
	"math"

	"github.com/lockyc/plate/internal/insv"
	"github.com/lockyc/plate/internal/insv/insvtest"
)

// helpers are steps a case runs in this process rather than as an engine:
// argv[0] names one, and it gets the rest of the expanded argv.
var helpers = map[string]func(args []string) error{
	"insv-trailer": insvTrailer,
}

// insvTrailer appends an X3 trailer to a lens file of --size² pixels made
// for a quality case: both lenses ideal (centred, the circle filling the
// frame, no residual turn) and --seconds of a still IMU at 1 kHz, held at
// --roll and --pitch degrees (a right-handed turn about the front lens's
// axis, then the nose up about the camera's right).
func insvTrailer(args []string) error {
	fs := flag.NewFlagSet("insv-trailer", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	size := fs.Int("size", 1024, "lens frame side in pixels")
	seconds := fs.Float64("seconds", 3, "IMU span")
	roll := fs.Float64("roll", 0, "degrees")
	pitch := fs.Float64("pitch", 0, "degrees")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("insv-trailer: want one lens file")
	}
	n := float64(*size)
	h := n / 2
	offset := fmt.Sprintf("2_%g_%g_%g_0_0_90_%g_%g_%g_0_0_90_%g_%g_0", h, h, h, h, n+h, h, 2*n, n)
	p, r := *pitch*math.Pi/180, *roll*math.Pi/180
	g := insv.StandardGravity
	accel := [3]float64{g * math.Sin(p), -g * math.Sin(r) * math.Cos(p), -g * math.Cos(r) * math.Cos(p)}
	m := insv.Meta{Model: "Insta360 X3", Serial: "QUALITY", Width: *size, Height: *size, FPS: 30}
	for i := 0; float64(i) <= *seconds*1000; i++ {
		m.IMU = append(m.IMU, insv.IMU{T: float64(i) / 1000, Accel: accel})
	}
	return insvtest.Write(fs.Arg(0), m, insvtest.Options{Offset: offset})
}
