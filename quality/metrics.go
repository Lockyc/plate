package quality

import (
	"fmt"
	"image"
	"math"
	"os"
	"strconv"
	"strings"

	"github.com/makiuchi-d/gozxing"
	"github.com/makiuchi-d/gozxing/qrcode"

	"github.com/lockyc/plate/internal/raster"
)

// Params are a case's metric parameters, placeholders already expanded.
type Params map[string]any

func (p Params) Path(key string) (string, error) {
	s, ok := p[key].(string)
	if !ok || s == "" {
		return "", fmt.Errorf("param %q: want a file path", key)
	}
	return s, nil
}

func (p Params) String(key, def string) string {
	if s, ok := p[key].(string); ok {
		return s
	}
	return def
}

func (p Params) Float(key string, def float64) (float64, error) {
	switch v := p[key].(type) {
	case nil:
		return def, nil
	case float64:
		return v, nil
	case int64:
		return float64(v), nil
	}
	return 0, fmt.Errorf("param %q: want a number", key)
}

func (p Params) Frac(key string) (raster.Frac, bool, error) {
	s, ok := p[key].(string)
	if !ok {
		return raster.Frac{}, false, nil
	}
	f, err := raster.ParseFrac(s)
	return f, true, err
}

// Metric measures a case's output.
type Metric func(Params) (float64, error)

// Metrics is every metric a case may name.
var Metrics = map[string]Metric{
	"rmse": rmse,
}

// rmse compares images a and b. Optional: region (x,y,w,h fractions),
// channels ("rgb", the default, or "alpha") and mask (an image whose white
// pixels are the ones measured).
func rmse(p Params) (float64, error) {
	ap, err := p.Path("a")
	if err != nil {
		return 0, err
	}
	bp, err := p.Path("b")
	if err != nil {
		return 0, err
	}
	a, err := raster.Load(ap)
	if err != nil {
		return 0, err
	}
	b, err := raster.Load(bp)
	if err != nil {
		return 0, err
	}
	r := a.Bounds()
	if f, ok, err := p.Frac("region"); err != nil {
		return 0, err
	} else if ok {
		r = f.In(r)
	}
	ch := raster.RGB
	switch p.String("channels", "rgb") {
	case "rgb":
	case "alpha":
		ch = raster.Alpha
	default:
		return 0, fmt.Errorf("channels: want rgb or alpha")
	}
	if mp, err := p.Path("mask"); err == nil {
		if _, ok := p["region"]; ok {
			return 0, fmt.Errorf("mask and region cannot be combined")
		}
		mask, err := raster.Load(mp)
		if err != nil {
			return 0, err
		}
		return raster.RMSEMasked(a, b, mask, ch)
	}
	return raster.RMSE(a, b, r, ch)
}

func init() {
	Metrics["dpi"] = dpi
	Metrics["pixel"] = pixel
	Metrics["qr-decodes"] = qrDecodes
}

func dpi(p Params) (float64, error) {
	path, err := p.Path("image")
	if err != nil {
		return 0, err
	}
	return raster.DPI(path)
}

// pixel is the RMS channel distance, 0..1, between the pixel at `at` (x,y
// fractions) and `color`.
func pixel(p Params) (float64, error) {
	path, err := p.Path("image")
	if err != nil {
		return 0, err
	}
	img, err := raster.Load(path)
	if err != nil {
		return 0, err
	}
	xs, ys, _ := strings.Cut(p.String("at", ""), ",")
	fx, err1 := strconv.ParseFloat(xs, 64)
	fy, err2 := strconv.ParseFloat(ys, 64)
	hex := strings.TrimPrefix(p.String("color", ""), "#")
	want, err3 := strconv.ParseUint(hex, 16, 32)
	inUnit := func(f float64) bool { return f >= 0 && f <= 1 } // false for NaN
	if err1 != nil || err2 != nil || err3 != nil || len(hex) != 6 || !inUnit(fx) || !inUnit(fy) {
		return 0, fmt.Errorf("pixel: want at = \"x,y\" and color = \"#rrggbb\"")
	}
	b := img.Bounds()
	c := img.RGBA64At(b.Min.X+int(math.Round(fx*float64(b.Dx()-1))), b.Min.Y+int(math.Round(fy*float64(b.Dy()-1))))
	var sum float64
	for i, v := range []uint16{c.R, c.G, c.B} {
		w := float64((want>>(16-8*i))&0xff) / 255
		d := float64(v)/65535 - w
		sum += d * d
	}
	return math.Sqrt(sum / 3), nil
}

// qrDecodes is 1 when image decodes to text, else 0.
func qrDecodes(p Params) (float64, error) {
	path, err := p.Path("image")
	if err != nil {
		return 0, err
	}
	img, err := raster.Load(path)
	if err != nil {
		return 0, err
	}
	bmp, err := gozxing.NewBinaryBitmapFromImage(img)
	if err != nil {
		return 0, err
	}
	res, err := qrcode.NewQRCodeReader().Decode(bmp, nil)
	if err != nil || res.GetText() != p.String("text", "") {
		return 0, nil
	}
	return 1, nil
}

func init() {
	Metrics["green-fringe"] = greenFringe
	Metrics["seethrough"] = seethrough
	Metrics["std-ratio"] = stdRatio
}

func hexColour(s string) ([3]float64, error) {
	s = strings.TrimPrefix(s, "#")
	v, err := strconv.ParseUint(s, 16, 32)
	if err != nil || len(s) != 6 {
		return [3]float64{}, fmt.Errorf("%q: want #rrggbb", s)
	}
	return [3]float64{float64(v>>16&0xff) / 255, float64(v>>8&0xff) / 255, float64(v&0xff) / 255}, nil
}

// greenFringe composites the cut-out over bg (premultiplied colour + (1-a)·bg)
// and counts green-dominant pixels among those above 2% coverage, as a share
// of every pixel in the region.
func greenFringe(p Params) (float64, error) {
	path, err := p.Path("image")
	if err != nil {
		return 0, err
	}
	img, err := raster.Load(path)
	if err != nil {
		return 0, err
	}
	bg, err := hexColour(p.String("bg", ""))
	if err != nil {
		return 0, err
	}
	margin, err := p.Float("margin", 10)
	if err != nil {
		return 0, err
	}
	m := margin / 255
	r := img.Bounds()
	if f, ok, err := p.Frac("region"); err != nil {
		return 0, err
	} else if ok {
		r = f.In(r)
	}
	var n, total int
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			c := img.RGBA64At(x, y)
			total++
			a := float64(c.A) / 65535
			if a <= 0.02 {
				continue
			}
			R := float64(c.R)/65535 + (1-a)*bg[0]
			G := float64(c.G)/65535 + (1-a)*bg[1]
			B := float64(c.B)/65535 + (1-a)*bg[2]
			if G > R+m && G > B+m {
				n++
			}
		}
	}
	if total == 0 {
		return 0, fmt.Errorf("the region selects no pixels")
	}
	return float64(n) / float64(total), nil
}

func parsePolygon(s string) ([][2]float64, error) {
	var pts [][2]float64
	for _, pair := range strings.Fields(s) {
		xs, ys, ok := strings.Cut(pair, ",")
		x, e1 := strconv.ParseFloat(xs, 64)
		y, e2 := strconv.ParseFloat(ys, 64)
		if !ok || e1 != nil || e2 != nil {
			return nil, fmt.Errorf("polygon point %q: want x,y fractions", pair)
		}
		pts = append(pts, [2]float64{x, y})
	}
	if len(pts) < 3 {
		return nil, fmt.Errorf("a polygon needs at least 3 points")
	}
	return pts, nil
}

func inside(pts [][2]float64, x, y float64) bool {
	in := false
	for i, j := 0, len(pts)-1; i < len(pts); j, i = i, i+1 {
		xi, yi, xj, yj := pts[i][0], pts[i][1], pts[j][0], pts[j][1]
		if (yi > y) != (yj > y) && x < (xj-xi)*(y-yi)/(yj-yi)+xi {
			in = !in
		}
	}
	return in
}

// seethrough is the share of pixels inside the solid polygon that are not
// nearly opaque: fur or body the matte left transparent.
func seethrough(p Params) (float64, error) {
	path, err := p.Path("image")
	if err != nil {
		return 0, err
	}
	img, err := raster.Load(path)
	if err != nil {
		return 0, err
	}
	pts, err := parsePolygon(p.String("solid", ""))
	if err != nil {
		return 0, err
	}
	b := img.Bounds()
	var n, total int
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			if !inside(pts, (float64(x-b.Min.X)+0.5)/float64(b.Dx()), (float64(y-b.Min.Y)+0.5)/float64(b.Dy())) {
				continue
			}
			total++
			if float64(img.RGBA64At(x, y).A)/65535 < 0.9 {
				n++
			}
		}
	}
	if total == 0 {
		return 0, fmt.Errorf("the solid polygon covers no pixels")
	}
	return float64(n) / float64(total), nil
}

func luma(img *image.RGBA64, x, y int) float64 {
	c := img.RGBA64At(x, y)
	return (0.2126*float64(c.R) + 0.7152*float64(c.G) + 0.0722*float64(c.B)) / 65535
}

// std is the population standard deviation, by Welford's update, which
// gives exactly 0 for a constant fill.
func std(vals []float64) float64 {
	var mean, sq float64
	for i, v := range vals {
		d := v - mean
		mean += d / float64(i+1)
		sq += d * (v - mean)
	}
	return math.Sqrt(sq / float64(len(vals)))
}

// stdRatio compares a fill's texture with the ground around the hole: the
// luma std-dev inside the mask in a, over the std-dev of the ring (the mask's
// bounding box grown by ring px, less the mask) in b. Below 1 means the
// fill is flatter than its surroundings.
func stdRatio(p Params) (float64, error) {
	var imgs [3]*image.RGBA64
	for i, k := range []string{"a", "b", "mask"} {
		path, err := p.Path(k)
		if err != nil {
			return 0, err
		}
		if imgs[i], err = raster.Load(path); err != nil {
			return 0, err
		}
	}
	a, b, mask := imgs[0], imgs[1], imgs[2]
	if a.Bounds() != b.Bounds() || a.Bounds() != mask.Bounds() {
		return 0, fmt.Errorf("a, b and mask sizes differ")
	}
	ringF, err := p.Float("ring", 20)
	if err != nil {
		return 0, err
	}
	ring := int(ringF)
	box := image.Rectangle{}
	var fill []float64
	for y := mask.Bounds().Min.Y; y < mask.Bounds().Max.Y; y++ {
		for x := mask.Bounds().Min.X; x < mask.Bounds().Max.X; x++ {
			if mask.RGBA64At(x, y).R > 0x7fff {
				fill = append(fill, luma(a, x, y))
				box = box.Union(image.Rect(x, y, x+1, y+1))
			}
		}
	}
	if len(fill) == 0 {
		return 0, fmt.Errorf("the mask is empty")
	}
	var around []float64
	grown := box.Inset(-ring).Intersect(b.Bounds())
	for y := grown.Min.Y; y < grown.Max.Y; y++ {
		for x := grown.Min.X; x < grown.Max.X; x++ {
			if mask.RGBA64At(x, y).R <= 0x7fff {
				around = append(around, luma(b, x, y))
			}
		}
	}
	if len(around) == 0 {
		return 0, fmt.Errorf("the ring around the mask is empty")
	}
	s := std(around)
	if s == 0 {
		return 0, fmt.Errorf("the ground around the mask has no texture to compare with")
	}
	return std(fill) / s, nil
}

func init() {
	Metrics["detail"] = detail
}

// detail scores how much of b's fine detail a reproduces, where they line
// up: 1 - Σ(hf(a)-hf(b))² / Σhf(b)², over luma, with hf the image less its
// Gaussian blur at sigma px (default 2). 1 is every fine stroke back in
// place; 0 is a blur that lost them all; below 0 is invented detail that
// misses the original's. RMSE alone is dominated by tone, so a soft result
// and a sharp one with the same tone error read alike there.
func detail(p Params) (float64, error) {
	var imgs [2]*image.RGBA64
	for i, k := range []string{"a", "b"} {
		path, err := p.Path(k)
		if err != nil {
			return 0, err
		}
		if imgs[i], err = raster.Load(path); err != nil {
			return 0, err
		}
	}
	a, b := imgs[0], imgs[1]
	if a.Bounds() != b.Bounds() {
		return 0, fmt.Errorf("a and b sizes differ")
	}
	sigma, err := p.Float("sigma", 2)
	if err != nil {
		return 0, err
	}
	if !(sigma > 0) {
		return 0, fmt.Errorf("sigma: want a positive number")
	}
	ha, hb := highPass(a, sigma), highPass(b, sigma)
	var miss, energy float64
	for i := range hb {
		d := ha[i] - hb[i]
		miss += d * d
		energy += hb[i] * hb[i]
	}
	if energy < 1e-12 {
		return 0, fmt.Errorf("b has no fine detail to recover")
	}
	return 1 - miss/energy, nil
}

// highPass is img's luma less its separable Gaussian blur, edges clamped.
func highPass(img *image.RGBA64, sigma float64) []float64 {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	l := make([]float64, w*h)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			l[y*w+x] = luma(img, b.Min.X+x, b.Min.Y+y)
		}
	}
	r := int(math.Ceil(3 * sigma))
	k := make([]float64, 2*r+1)
	var ks float64
	for i := range k {
		d := float64(i - r)
		k[i] = math.Exp(-d * d / (2 * sigma * sigma))
		ks += k[i]
	}
	clamp := func(v, n int) int { return min(max(v, 0), n-1) }
	tmp, blur := make([]float64, w*h), make([]float64, w*h)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			var s float64
			for i, kv := range k {
				s += kv * l[y*w+clamp(x+i-r, w)]
			}
			tmp[y*w+x] = s / ks
		}
	}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			var s float64
			for i, kv := range k {
				s += kv * tmp[clamp(y+i-r, h)*w+x]
			}
			blur[y*w+x] = s / ks
		}
	}
	for i := range l {
		l[i] -= blur[i]
	}
	return l
}

func init() {
	Metrics["bytes"] = fileBytes
}

// fileBytes is the size of image in bytes, for an encoder whose output size
// is the result.
func fileBytes(p Params) (float64, error) {
	path, err := p.Path("image")
	if err != nil {
		return 0, err
	}
	fi, err := os.Stat(path)
	if err != nil {
		return 0, err
	}
	return float64(fi.Size()), nil
}
