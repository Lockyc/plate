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
// top, as r%05d.png in dir, then closes the loop as loop (one of Loops)
// says: closeLoop.
func Compose(ctx context.Context, frames []string, fps int, scale float64, lay Layout, views []View, ops [][]Op, sprites map[string]*image.RGBA64, loop, dir string) (refs []string, lc LoopClose, err error) {
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
	if lc, err = closeLoop(refs, fps, loop); err != nil {
		return nil, LoopClose{}, err
	}
	return refs, lc, nil
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

// LoopClose is what closeLoop altered: how many of the clip's first (Head)
// and last (Tail) frames. Both are 0 for a clip that loops as it is.
type LoopClose struct{ Head, Tail int }

// closeLoop measures the seam from the last frame back to the first and,
// when it is above MaxSeam, the most a loop may show, closes it over
// loopMS; at or under it the clip loops as it is.
//
// crossfade blends the last n frames toward the first, frame i of n at
// weight i/(n+1). dip eases the last n/2 frames toward the first frame's
// ground and the first n-n/2 back from it, both by easeInOutCubic, so the
// two seam frames are the ground itself and no frame shows the end and the
// start at once.
func closeLoop(refs []string, fps int, loop string) (LoopClose, error) {
	first, err := loadRGBA64(refs[0])
	if err != nil {
		return LoopClose{}, err
	}
	last, err := loadRGBA64(refs[len(refs)-1])
	if err != nil {
		return LoopClose{}, err
	}
	seam, err := Seam(first, last)
	if err != nil {
		return LoopClose{}, err
	}
	if seam <= MaxSeam {
		return LoopClose{}, nil
	}
	n := int(math.Round(loopMS * float64(fps) / 1000))
	switch loop {
	case "crossfade":
		n = min(n, len(refs)-1)
		for i := 1; i <= n; i++ {
			err := mixInto(refs[len(refs)-n-1+i], float64(i)/float64(n+1), func(j int) uint16 {
				return uint16(first.Pix[j])<<8 | uint16(first.Pix[j+1])
			})
			if err != nil {
				return LoopClose{}, err
			}
		}
		return LoopClose{Tail: n}, nil
	case "dip":
		g := ground(first)
		at := [4]uint16{g.R, g.G, g.B, g.A}
		to := func(j int) uint16 { return at[j/2%4] }
		// One frame short of the clip, so frame Head, the poster, is as composed.
		n = max(2, min(n, len(refs)-1))
		lc := LoopClose{Head: n - n/2, Tail: n / 2}
		for i := 1; i <= lc.Tail; i++ {
			if err := mixInto(refs[len(refs)-lc.Tail-1+i], easeInOutCubic(float64(i)/float64(lc.Tail)), to); err != nil {
				return LoopClose{}, err
			}
		}
		for i := range lc.Head {
			if err := mixInto(refs[i], easeInOutCubic(1-float64(i)/float64(lc.Head)), to); err != nil {
				return LoopClose{}, err
			}
		}
		return lc, nil
	}
	return LoopClose{}, fmt.Errorf("compose: unknown loop %q (want one of %v)", loop, Loops)
}

// mixInto rewrites the frame at path as itself at weight 1-w over to's
// sample at w, to(j) giving the 16-bit sample for Pix[j:j+2].
func mixInto(path string, w float64, to func(j int) uint16) error {
	img, err := loadRGBA64(path)
	if err != nil {
		return err
	}
	for j := 0; j < len(img.Pix); j += 2 {
		a := float64(uint16(img.Pix[j])<<8 | uint16(img.Pix[j+1]))
		v := clamp16(a*(1-w) + float64(to(j))*w)
		img.Pix[j], img.Pix[j+1] = uint8(v>>8), uint8(v)
	}
	return savePNG(path, img)
}

// groundReach is how far, in 8-bit levels per channel, a pixel may sit from
// ground's first estimate and still count toward the ground.
const groundReach = 8

// ground is img's dominant colour, the one a UI's background gives most of
// its pixels. Each channel is cut to 8-level bins; the peak is the bin
// whose 3×3×3 neighbourhood holds the most pixels (then its own count), so
// a ground split across a bin edge still counts whole. The colour is the
// mean of the pixels within groundReach of that bin's mean, so a card in a
// neighbouring bin but further off is not averaged in. Unlike a per-channel
// median, it is a colour the frame shows.
func ground(img *image.RGBA64) color.RGBA64 {
	bin := func(j int) int {
		return int(img.Pix[j])>>3<<10 | int(img.Pix[j+2])>>3<<5 | int(img.Pix[j+4])>>3
	}
	var count [1 << 15]int
	var used []int
	for j := 0; j < len(img.Pix); j += 8 {
		b := bin(j)
		if count[b] == 0 {
			used = append(used, b)
		}
		count[b]++
	}
	near := func(b int) int {
		r, g, bl := b>>10, b>>5&31, b&31
		n := 0
		for dr := -1; dr <= 1; dr++ {
			for dg := -1; dg <= 1; dg++ {
				for db := -1; db <= 1; db++ {
					x, y, z := r+dr, g+dg, bl+db
					if x >= 0 && x < 32 && y >= 0 && y < 32 && z >= 0 && z < 32 {
						n += count[x<<10|y<<5|z]
					}
				}
			}
		}
		return n
	}
	top, topNear := -1, -1
	for _, b := range used {
		if n := near(b); n > topNear || n == topNear && count[b] > count[top] {
			top, topNear = b, n
		}
	}
	sample := func(j, c int) float64 { return float64(uint16(img.Pix[j+2*c])<<8 | uint16(img.Pix[j+2*c+1])) }
	mean := func(keep func(j int) bool) [3]float64 {
		var sum [3]float64
		n := 0.0
		for j := 0; j < len(img.Pix); j += 8 {
			if keep(j) {
				for c := range 3 {
					sum[c] += sample(j, c)
				}
				n++
			}
		}
		return [3]float64{sum[0] / n, sum[1] / n, sum[2] / n}
	}
	est := mean(func(j int) bool { return bin(j) == top })
	g := mean(func(j int) bool {
		for c := range 3 {
			if math.Abs(sample(j, c)-est[c]) > groundReach*257 {
				return false
			}
		}
		return true
	})
	return color.RGBA64{clamp16(g[0]), clamp16(g[1]), clamp16(g[2]), 0xffff}
}
