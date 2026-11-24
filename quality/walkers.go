package quality

import (
	"flag"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	_ "image/png" // the walkers sheet
	"io"
	"math"
	"os"
	"path/filepath"

	"github.com/lockyc/plate/internal/pano"
)

// walkerCells is how many people the walkers sheet holds, one to a cell of
// equal width, each standing on its cell's bottom edge.
const walkerCells = 10

// Ground geometry of the track-walkers scene, in metres: the camera's height
// on its stick and a person's height.
const camHeight, personHeight = 1.6, 1.7

// segment is a stretch of time a walker is in view, walking a straight line
// on the ground: from start to end seconds, starting at lon0 degrees and
// dist0 metres from the camera, at vx metres a second towards lon 90 and vz
// towards lon 0.
type segment struct {
	start, end, lon0, dist0, vx, vz float64
}

// across is a segment walked at speed metres a second square to the line
// from the camera, so that lon grows; a negative speed turns it the other
// way.
func across(start, end, lon0, dist0, speed float64) segment {
	r := lon0 * math.Pi / 180
	return segment{start, end, lon0, dist0, speed * math.Cos(r), -speed * math.Sin(r)}
}

// away is a segment walked at speed metres a second straight away from the
// camera; a negative speed comes towards it.
func away(start, end, lon0, dist0, speed float64) segment {
	r := lon0 * math.Pi / 180
	return segment{start, end, lon0, dist0, speed * math.Sin(r), speed * math.Cos(r)}
}

// at is where the walker is at t: lon degrees, dist metres.
func (s segment) at(t float64) (lon, dist float64) {
	r := s.lon0 * math.Pi / 180
	x := s.dist0*math.Sin(r) + s.vx*(t-s.start)
	z := s.dist0*math.Cos(r) + s.vz*(t-s.start)
	return math.Atan2(x, z) * 180 / math.Pi, math.Hypot(x, z)
}

// walkerPaths is the track-walkers scene: one entry per walker, in the
// walkers sheet's cell order. Ten people and twelve tracks: walkers 0 and 7
// pass close and fast (up to 11 and 15 degrees a second) through the same
// stretch 4 s apart, then leave; walker 1 walks away and leaves at 12 s and
// comes back towards the camera 10 s later, 145 degrees round; walker 5
// leaves at 15 s and comes back 15 s later 114 degrees round; walkers 3 and
// 4 cross at about 7.5 s, the nearer in front; walker 6 drifts closer and
// walkers 2, 8 and 9 stand. No two walkers overlap but the crossing pair.
var walkerPaths = [walkerCells][]segment{
	{across(0, 10, -170, 4, 0.8)},
	{away(0, 12, 30, 5, 0.5), away(22, 40, 175, 10, -0.35)},
	{away(0, 40, 140, 4.5, 0)},
	{across(0, 40, -45, 7, 0.25)},
	{across(0, 40, -5, 4, -0.25)},
	{across(0, 15, 70, 6, 0.3), away(30, 40, -140, 9, -0.4)},
	{away(0, 40, 112, 8, -0.08)},
	{across(14, 22, -178, 3.5, 0.9)},
	{away(0, 40, 50, 6, 0)},
	{away(0, 40, -90, 5, 0)},
}

// trackWalkers writes a run of equirect frames as `plate pano --every`
// would, with its pano.tsv: the walkers sheet's people walking walkerPaths
// over an equirect background, each drawn upright from head to feet as the
// camera sees a person of personHeight on the ground, the nearer over the
// farther.
//
//	track-walkers [--every 0.5] [--seconds 40] [--width 2880] <background> <walkers.png> <dir>
func trackWalkers(args []string) error {
	fs := flag.NewFlagSet("track-walkers", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	every := fs.Float64("every", 0.5, "seconds between frames")
	seconds := fs.Float64("seconds", 40, "length of the run")
	width := fs.Int("width", 2880, "frame width")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 3 || *every <= 0 || *seconds <= 0 || *width < 64 || *width%2 != 0 {
		return fmt.Errorf("track-walkers: want a positive --every and --seconds, an even --width, a background, the walkers sheet and a directory")
	}
	bg, err := decode(fs.Arg(0))
	if err != nil {
		return err
	}
	sheet, err := decode(fs.Arg(1))
	if err != nil {
		return err
	}
	dir := fs.Arg(2)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	w, h := *width, *width/2
	ground := resample(bg, bg.Bounds(), w, h)
	cw := sheet.Bounds().Dx() / walkerCells
	var people [walkerCells]image.Rectangle
	for i := range people {
		people[i] = opaqueBounds(sheet, image.Rect(sheet.Bounds().Min.X+i*cw, sheet.Bounds().Min.Y, sheet.Bounds().Min.X+(i+1)*cw, sheet.Bounds().Max.Y))
		if people[i].Empty() {
			return fmt.Errorf("track-walkers: cell %d of %s is empty", i, fs.Arg(1))
		}
	}
	var rows []pano.Row
	for n := 0; float64(n)**every < *seconds-1e-9; n++ {
		t := float64(n) * *every
		frame := image.NewRGBA(ground.Bounds())
		copy(frame.Pix, ground.Pix)
		type placed struct {
			i         int
			lon, dist float64
		}
		var in []placed
		for i, segs := range walkerPaths {
			for _, s := range segs {
				if t >= s.start-1e-9 && t < s.end-1e-9 {
					lon, dist := s.at(t)
					in = append(in, placed{i, lon, dist})
				}
			}
		}
		// farthest first, so the nearer walker is drawn over
		for a := range in {
			for b := a + 1; b < len(in); b++ {
				if in[b].dist > in[a].dist {
					in[a], in[b] = in[b], in[a]
				}
			}
		}
		for _, p := range in {
			drawWalker(frame, sheet, people[p.i], p.lon, p.dist)
		}
		name := fmt.Sprintf("%06.0f_%03d.jpg", math.Floor(t), int(math.Round((t-math.Floor(t))*1000)))
		f, err := os.Create(filepath.Join(dir, name))
		if err != nil {
			return err
		}
		err = jpeg.Encode(f, frame, &jpeg.Options{Quality: 90})
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return err
		}
		rows = append(rows, pano.Row{File: name, T: t, Clock: fmt.Sprintf("00:00:%06.3f", t)})
	}
	return pano.WriteTSV(filepath.Join(dir, "pano.tsv"), rows)
}

func decode(path string) (image.Image, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	img, _, err := image.Decode(f)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return img, nil
}

// opaqueBounds is the smallest rectangle within r holding every pixel of
// img with any alpha.
func opaqueBounds(img image.Image, r image.Rectangle) image.Rectangle {
	out := image.Rectangle{}
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			if _, _, _, a := img.At(x, y).RGBA(); a > 0 {
				out = out.Union(image.Rect(x, y, x+1, y+1))
			}
		}
	}
	return out
}

// sample is img's bilinear colour at (x, y) in pixel-centre coordinates,
// premultiplied, 0 to 1.
func sample(img image.Image, r image.Rectangle, x, y float64) [4]float64 {
	x0, y0 := math.Floor(x-0.5), math.Floor(y-0.5)
	fx, fy := x-0.5-x0, y-0.5-y0
	var out [4]float64
	for _, c := range [4][3]float64{{0, 0, (1 - fx) * (1 - fy)}, {1, 0, fx * (1 - fy)}, {0, 1, (1 - fx) * fy}, {1, 1, fx * fy}} {
		px := min(max(int(x0+c[0]), r.Min.X), r.Max.X-1)
		py := min(max(int(y0+c[1]), r.Min.Y), r.Max.Y-1)
		cr, cg, cb, ca := img.At(px, py).RGBA()
		for k, v := range [4]uint32{cr, cg, cb, ca} {
			out[k] += c[2] * float64(v) / 0xffff
		}
	}
	return out
}

// resample scales the r part of img to w×h, bilinear.
func resample(img image.Image, r image.Rectangle, w, h int) *image.RGBA {
	out := image.NewRGBA(image.Rect(0, 0, w, h))
	sx, sy := float64(r.Dx())/float64(w), float64(r.Dy())/float64(h)
	for y := range h {
		for x := range w {
			c := sample(img, r, float64(r.Min.X)+(float64(x)+0.5)*sx, float64(r.Min.Y)+(float64(y)+0.5)*sy)
			out.SetRGBA(x, y, color.RGBA{uint8(c[0]*255 + 0.5), uint8(c[1]*255 + 0.5), uint8(c[2]*255 + 0.5), 255})
		}
	}
	return out
}

// drawWalker draws the person in r of the sheet standing dist metres away at
// lon degrees: from head (personHeight) to feet (the ground, camHeight
// below the camera), as wide as the sheet's aspect gives at that height,
// widened for the equirect's stretch at that latitude.
func drawWalker(frame *image.RGBA, sheet image.Image, r image.Rectangle, lon, dist float64) {
	w, h := frame.Bounds().Dx(), frame.Bounds().Dy()
	deg := float64(h) / 180
	top := 90 - math.Atan2(personHeight-camHeight, dist)*180/math.Pi
	bottom := 90 + math.Atan2(camHeight, dist)*180/math.Pi
	ph := (bottom - top) * deg
	mid := 90 - (top+bottom)/2
	pw := ph * float64(r.Dx()) / float64(r.Dy()) / math.Cos(mid*math.Pi/180)
	cx := (lon/360 + 0.5) * float64(w)
	y0 := top * deg
	for y := int(y0); y < int(math.Ceil(y0+ph)); y++ {
		for xi := int(cx - pw/2); xi < int(math.Ceil(cx+pw/2)); xi++ {
			u := (float64(xi) + 0.5 - (cx - pw/2)) / pw
			v := (float64(y) + 0.5 - y0) / ph
			if u < 0 || u >= 1 || v < 0 || v >= 1 || y < 0 || y >= h {
				continue
			}
			c := sample(sheet, r, float64(r.Min.X)+u*float64(r.Dx()), float64(r.Min.Y)+v*float64(r.Dy()))
			if c[3] <= 0 {
				continue
			}
			x := ((xi % w) + w) % w
			o := frame.RGBAAt(x, y)
			blend := func(src float64, dst uint8) uint8 {
				return uint8(math.Round(math.Min(255, (src+float64(dst)/255*(1-c[3]))*255)))
			}
			frame.SetRGBA(x, y, color.RGBA{blend(c[0], o.R), blend(c[1], o.G), blend(c[2], o.B), 255})
		}
	}
}
