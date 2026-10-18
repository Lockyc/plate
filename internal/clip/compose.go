package clip

import (
	"context"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"io"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sync"

	"github.com/lockyc/plate/internal/atomicfile"
)

// maxWorkers caps Compose's parallelism: each worker holds a master frame
// and its output in 16 bits, about 100 MB at phone capture size.
const maxWorkers = 8

func loadImage(path string) (image.Image, error) {
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

func loadRGBA64(path string) (*image.RGBA64, error) {
	img, err := loadImage(path)
	if err != nil {
		return nil, err
	}
	out := image.NewRGBA64(img.Bounds())
	if m, ok := img.(*image.RGBA); ok && m.Stride == 4*m.Rect.Dx() {
		for i, v := range m.Pix {
			out.Pix[2*i], out.Pix[2*i+1] = v, v // v·257, big-endian
		}
		return out, nil
	}
	draw.Draw(out, out.Bounds(), img, img.Bounds().Min, draw.Src)
	return out, nil
}

// savePNG writes img as an opaque 8-bit RGB PNG, whole or not at all.
func savePNG(path string, img *image.RGBA64) error {
	out := image.NewRGBA(img.Bounds())
	for i := range out.Pix {
		if i%4 == 3 {
			out.Pix[i] = 0xff
			continue
		}
		v := uint32(img.Pix[2*i])<<8 | uint32(img.Pix[2*i+1])
		out.Pix[i] = uint8((v*0xff + 0x7fff) / 0xffff)
	}
	enc := png.Encoder{CompressionLevel: png.BestSpeed}
	return atomicfile.Write(path, 0o644, func(w io.Writer) error { return enc.Encode(w, out) })
}

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
// top, as r%05d.png in dir. A clip whose last frame does not match its
// first crossfades its last crossfadeMS into the first frame, and
// crossfade is how many frames that took.
func Compose(ctx context.Context, frames []string, fps int, scale float64, lay Layout, views []View, ops [][]Op, sprites map[string]*image.RGBA64, dir string) (refs []string, crossfade int, err error) {
	n := len(frames)
	if n == 0 || len(views) != n || len(ops) != n {
		return nil, 0, fmt.Errorf("compose: %d frames, %d views and %d overlay frames", n, len(views), len(ops))
	}
	sp, err := prepare(sprites, ops)
	if err != nil {
		return nil, 0, err
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
		return nil, 0, context.Cause(ctx)
	}
	crossfade, err = closeLoop(refs, fps)
	if err != nil {
		return nil, 0, err
	}
	return refs, crossfade, nil
}

func composeFrame(in, out string, scale float64, lay Layout, v View, ops []Op, sp map[string]*sprite) error {
	src, err := loadRGBA64(in)
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
	return savePNG(out, dst)
}

// closeLoop measures the seam from the last frame back to the first and,
// when it is above MaxSeam, the most a loop may show, blends the last n
// frames toward the first, frame i of n at weight i/(n+1); at or under it
// the clip loops as it is.
func closeLoop(refs []string, fps int) (int, error) {
	first, err := loadRGBA64(refs[0])
	if err != nil {
		return 0, err
	}
	last, err := loadRGBA64(refs[len(refs)-1])
	if err != nil {
		return 0, err
	}
	seam, err := Seam(first, last)
	if err != nil {
		return 0, err
	}
	if seam <= MaxSeam {
		return 0, nil
	}
	n := min(int(math.Round(crossfadeMS*float64(fps)/1000)), len(refs)-1)
	for i := 1; i <= n; i++ {
		k := len(refs) - n - 1 + i
		img, err := loadRGBA64(refs[k])
		if err != nil {
			return 0, err
		}
		w := float64(i) / float64(n+1)
		for j := 0; j < len(img.Pix); j += 2 {
			a := float64(uint16(img.Pix[j])<<8 | uint16(img.Pix[j+1]))
			b := float64(uint16(first.Pix[j])<<8 | uint16(first.Pix[j+1]))
			v := clamp16(a*(1-w) + b*w)
			img.Pix[j], img.Pix[j+1] = uint8(v>>8), uint8(v)
		}
		if err := savePNG(refs[k], img); err != nil {
			return 0, err
		}
	}
	return n, nil
}
