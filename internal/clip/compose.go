package clip

import (
	"context"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"math"
	"path/filepath"
	"runtime"
	"slices"
	"sync"

	"github.com/lockyc/plate/internal/video"
)

// maxWorkers caps Compose's parallelism: each worker holds a master frame
// and its output in 16 bits, about 100 MB at phone capture size.
const maxWorkers = 8

// sprite is an overlay image ready to draw: padded with one transparent
// pixel each side so a resample past its edge reads nothing, and the
// largest Op.Scale it is drawn at, which is the scale it was rendered at.
type sprite struct {
	img   *image.RGBA64 // bounds (-1,-1)–(w+1,h+1); the picture sits at 0,0
	w, h  float64
	max   float64
	point bool
}

func prepare(sprites map[string]*image.RGBA64, ops [][]Op) (map[string]*sprite, error) {
	out := map[string]*sprite{}
	for _, frame := range ops {
		for _, op := range frame {
			if s := out[op.Key]; s != nil {
				s.max = math.Max(s.max, op.Scale)
				continue
			}
			img := sprites[op.Key]
			if img == nil {
				return nil, fmt.Errorf("no sprite rendered for overlay %s", op.Key)
			}
			sz := img.Bounds().Size()
			pad := image.NewRGBA64(image.Rect(-1, -1, sz.X+1, sz.Y+1))
			draw.Draw(pad, image.Rect(0, 0, sz.X, sz.Y), img, img.Bounds().Min, draw.Src)
			out[op.Key] = &sprite{img: pad, w: float64(sz.X), h: float64(sz.Y), max: op.Scale,
				point: slices.Contains(pointKinds, op.Key)}
		}
	}
	return out, nil
}

// draw composites op's sprite over dst, sRGB over with premultiplied alpha,
// at Op.Scale/max of its rendered size: a point sprite centred on X,Y, a
// full-frame one with its top-left there.
func (s *sprite) draw(dst *image.RGBA64, op Op) {
	if op.Opacity <= 0 || op.Scale <= 0 {
		return
	}
	f := op.Scale / s.max
	w, h := s.w*f, s.h*f
	x, y := op.X, op.Y
	if s.point {
		x, y = x-w/2, y-h/2
	}
	if f == 1 {
		// At its rendered size a sprite lands on whole pixels, rounded as
		// the region's rows are, so a caption easing with a strip stays sharp.
		x, y = math.Round(x), math.Round(y)
	}
	box := image.Rect(int(math.Floor(x)), int(math.Floor(y)), int(math.Ceil(x+w)), int(math.Ceil(y+h))).Intersect(dst.Bounds())
	if box.Empty() {
		return
	}
	layer := image.NewRGBA64(box)
	Resample(layer, box, s.img, Rect{(float64(box.Min.X) - x) / f, (float64(box.Min.Y) - y) / f,
		float64(box.Dx()) / f, float64(box.Dy()) / f})
	o := math.Min(1, op.Opacity)
	for yy := box.Min.Y; yy < box.Max.Y; yy++ {
		for xx := box.Min.X; xx < box.Max.X; xx++ {
			sc := layer.RGBA64At(xx, yy)
			if sc.A == 0 {
				continue
			}
			dc := dst.RGBA64At(xx, yy)
			keep := 1 - float64(sc.A)*o/0xffff
			over := func(s, d uint16) uint16 { return clamp16(float64(s)*o + float64(d)*keep) }
			dst.SetRGBA64(xx, yy, color.RGBA64{over(sc.R, dc.R), over(sc.G, dc.G), over(sc.B, dc.B), over(sc.A, dc.A)})
		}
	}
}

// Compose renders each output frame: master frame k (px, scale px per pt)
// framed by views[k], the shown pin strips cut in, and ops[k]'s overlays on
// top, as r%05d.png in dir, then closes the loop as loop (one of video.Loops)
// says: video.CloseLoop.
func Compose(ctx context.Context, frames []string, fps int, scale float64, lay Layout, views []View, ops [][]Op, sprites map[string]*image.RGBA64, loop, dir string) (refs []string, lc video.LoopClose, err error) {
	n := len(frames)
	if n == 0 || len(views) != n || len(ops) != n {
		return nil, lc, fmt.Errorf("compose: %d frames, %d views and %d overlay frames", n, len(views), len(ops))
	}
	sp, err := prepare(sprites, ops)
	if err != nil {
		return nil, lc, err
	}
	refs = make([]string, n)
	for k := range refs {
		refs[k] = filepath.Join(dir, fmt.Sprintf("r%05d.png", k))
	}
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	jobs := make(chan int)
	var wg sync.WaitGroup
	for range min(runtime.NumCPU(), maxWorkers) {
		wg.Go(func() {
			for k := range jobs {
				if ctx.Err() == nil {
					if err := composeFrame(frames[k], refs[k], scale, lay, views[k], ops[k], sp); err != nil {
						cancel(err)
					}
				}
			}
		})
	}
feed:
	for k := range n {
		select {
		case jobs <- k:
		case <-ctx.Done():
			break feed
		}
	}
	close(jobs)
	wg.Wait()
	if ctx.Err() != nil {
		return nil, lc, context.Cause(ctx)
	}
	if lc, err = video.CloseLoop(refs, fps, loop); err != nil {
		return nil, video.LoopClose{}, err
	}
	return refs, lc, nil
}

func composeFrame(in, out string, scale float64, lay Layout, v View, ops []Op, sp map[string]*sprite) error {
	src, err := video.LoadRGBA64(in)
	if err != nil {
		return err
	}
	dst := image.NewRGBA64(image.Rect(0, 0, lay.W, lay.H))
	rows := func(y, h float64) image.Rectangle {
		return image.Rect(0, int(math.Round(y)), lay.W, int(math.Round(y+h)))
	}
	px := func(r Rect) Rect { return Rect{r.X * scale, r.Y * scale, r.W * scale, r.H * scale} }
	Resample(dst, rows(v.RegionY, v.RegionH), src, px(v.Region))
	for _, p := range v.Pins {
		if p.Shown > 0 {
			Resample(dst, rows(p.Y, p.H), src, px(p.Src))
		}
	}
	for _, op := range ops {
		sp[op.Key].draw(dst, op)
	}
	return video.SavePNG(out, dst)
}
