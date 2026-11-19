// Package press makes a print master from a PDF with one Ghostscript pass:
// every glyph outlined, every colour converted to the supplied CMYK profile,
// the page box forced from the source, images downsampled only well above
// --max-ppi and kept lossless unless --jpeg sets a quality for photographic
// colour ones. It then proves the master: no fonts, no RGB anywhere, the same page boxes and count, no more than --max-ink total ink beyond a speck, and a soft proof of the master
// (its CMYK read back through plate's own colour engine) that matches the source by RMSE over each page and each --region. The
// master is a separate deliverable: outlining destroys the text a text
// check reads, so check the source, and press the checked source.
package press

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/lockyc/plate/internal/cli"
	"github.com/lockyc/plate/internal/cmm"
	"github.com/lockyc/plate/internal/engine"
	"github.com/lockyc/plate/internal/pdf"
	"github.com/lockyc/plate/internal/raster"
)

const usage = "press --icc profile.icc [--cmyk RRGGBB=C,M,Y,K]... [--max-ppi N] [--jpeg Q] [--max-rmse R] [--region x,y,w,h:max]... [--proof-width PX] [--max-ink PCT] (<in.pdf> <out.pdf> | --verify <master.pdf> <in.pdf>)"

// spot is a brand colour with its own CMYK: a vector fill or stroke drawn in
// rgb is written as cmyk (percentages) instead of converted through the
// profile, so the master carries the values the brand specifies.
type spot struct {
	rgb  [3]int
	cmyk [4]float64
}

// parseSpot reads RRGGBB=C,M,Y,K, each of C, M, Y and K a percentage.
func parseSpot(s string) (spot, error) {
	hex, vals, ok := strings.Cut(strings.TrimPrefix(s, "#"), "=")
	parts := strings.Split(vals, ",")
	if !ok || len(hex) != 6 || len(parts) != 4 {
		return spot{}, fmt.Errorf("--cmyk %q: want RRGGBB=C,M,Y,K, as 17458f=100,84,12,3", s)
	}
	var sp spot
	for i := range 3 {
		v, err := strconv.ParseUint(hex[2*i:2*i+2], 16, 8)
		if err != nil {
			return spot{}, fmt.Errorf("--cmyk %q: %s is not a hex colour", s, hex)
		}
		sp.rgb[i] = int(v)
	}
	for i, p := range parts {
		v, ok := cli.Finite(strings.TrimSpace(p))
		if !ok || v < 0 || v > 100 {
			return spot{}, fmt.Errorf("--cmyk %q: %s is not a percentage", s, p)
		}
		sp.cmyk[i] = v / 100
	}
	return sp, nil
}

// rgbOp is an rgb fill (rg) or stroke (RG) in a content stream.
var rgbOp = regexp.MustCompile(`(^|[\s\[\]])([0-9]*\.?[0-9]+) ([0-9]*\.?[0-9]+) ([0-9]*\.?[0-9]+) (rg|RG)\b`)

// frac writes v in [0,1] as briefly as a content stream allows: 1, 0, .84.
func frac(v float64) string {
	s := strconv.FormatFloat(math.Round(v*1e4)/1e4, 'f', -1, 64)
	if strings.HasPrefix(s, "0.") {
		return s[1:]
	}
	return s
}

// withSpots rewrites every rg or RG in qdf whose colour is a spot's as that
// spot's k or K, outside image data, and returns how many it made. A
// replacement may be longer than the rgb it replaces (black's 0 0 0 1 k is),
// so the caller repairs QDF's stream lengths and offsets with fix-qdf.
func withSpots(qdf []byte, spots []spot) ([]byte, int) {
	var n int
	out := rewriteContent(qdf, func(stream []byte) []byte {
		return rgbOp.ReplaceAllFunc(stream, func(m []byte) []byte {
			g := rgbOp.FindSubmatch(m)
			var c [3]int
			for i := range 3 {
				v, _ := strconv.ParseFloat(string(g[2+i]), 64)
				c[i] = int(math.Round(v * 255))
			}
			for _, sp := range spots {
				if sp.rgb != c {
					continue
				}
				op := "k"
				if string(g[5]) == "RG" {
					op = "K"
				}
				n++
				return fmt.Appendf(nil, "%s%s %s %s %s %s", g[1], frac(sp.cmyk[0]), frac(sp.cmyk[1]), frac(sp.cmyk[2]), frac(sp.cmyk[3]), op)
			}
			return m
		})
	})
	return out, n
}

// rewriteContent applies f to the data of every stream in qdf but images'.
func rewriteContent(qdf []byte, f func([]byte) []byte) []byte {
	var out []byte
	for {
		i := bytes.Index(qdf, []byte("\nstream\n"))
		if i < 0 {
			return append(out, qdf...)
		}
		head := i + len("\nstream\n")
		out = append(out, qdf[:head]...)
		dict := qdf[:i]
		if o := bytes.LastIndex(dict, []byte(" obj\n")); o >= 0 {
			dict = dict[o:]
		}
		end := bytes.Index(qdf[head:], []byte("\nendstream"))
		if end < 0 {
			return append(out, qdf[head:]...)
		}
		data := qdf[head : head+end]
		if !imageDict.Match(dict) {
			data = f(data)
		}
		out = append(out, data...)
		qdf = qdf[head+end:]
	}
}

// defaultMaxRMSE separated clean masters (at most 0.066) from masters with a
// hidden element (0.083 and above) on the print artboards press was built
// against. The quality cases bracket it: press-clean-master reads 0.0221 and
// press-proof-broken-master, the same page pressed without its footer bar,
// 0.1803. Calibrate a project's own value against a deliberately broken
// master: a gate that cannot fail is decoration.
const defaultMaxRMSE = 0.085

const gsTimeout = 30 * time.Minute

// nonCMYK matches every colour space and RGB fill/stroke operator that may
// not appear in a master, in qpdf's uncompressed QDF form, read without its
// image data (see withoutImageData).
var nonCMYK = regexp.MustCompile(`(?m)/(DeviceRGB|CalRGB|ICCBased|Lab|Indexed|Separation|DeviceN)\b| (rg|RG)$`)

type region struct {
	spec string
	f    raster.Frac
	max  float64
}

type job struct {
	in, out, icc    string
	prof            *cmm.Profile // icc, read by plate's own colour engine for the soft proof
	maxPPI, maxRMSE float64
	maxInk          float64 // total ink a pixel may carry, in percent
	proofWidth      int
	jpeg            int // colour images' JPEG quality; 0 keeps them lossless
	regions         []region
	spots           []spot
	log             io.Writer
}

// Main runs `plate press`.
func Main(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := cli.Flags("press", usage, stderr)
	icc := fs.String("icc", "", "the CMYK output profile (required)")
	maxPPI := cli.Float(fs, "max-ppi", 450, "downsample images above 1.5 × this resolution, to it")
	maxRMSE := cli.Float(fs, "max-rmse", defaultMaxRMSE, "soft-proof RMSE allowed over a whole page")
	proofWidth := cli.Int(fs, "proof-width", 3000, "soft-proof render width in px; RMSE settles by about 3000")
	maxInk := cli.Float(fs, "max-ink", 300, "total ink (C+M+Y+K) a page may carry over more than a speck of area, in percent; 0 to 400")
	jpeg := cli.Int(fs, "jpeg", 0, "store photographic colour images as JPEG at this `quality` (1-100), no chroma subsampling, flat artwork staying lossless; 0 keeps every image lossless")
	verifyOnly := fs.String("verify", "", "check this existing `master` against <in.pdf>, as press checks one it makes; writes nothing")
	var specs cli.Multi
	fs.Var(&specs, "region", "x,y,w,h:max: a page region, as fractions, held to its own RMSE (repeatable)")
	var cmyks cli.Multi
	fs.Var(&cmyks, "cmyk", "RRGGBB=C,M,Y,K: vector fills in this colour get exactly this CMYK, in percent (repeatable)")
	rest, code, ok := cli.Parse(fs, args, -1)
	if !ok {
		return code
	}
	if want := map[bool]int{false: 2, true: 1}[*verifyOnly != ""]; len(rest) != want {
		fs.Usage()
		return 2
	}
	if *icc == "" {
		defer fs.Usage()
		return cli.Usage(stderr, "press", "--icc is required")
	}
	for _, c := range []struct {
		flag string
		ok   bool
	}{{"max-ppi", *maxPPI > 0}, {"max-rmse", *maxRMSE > 0}, {"proof-width", *proofWidth > 0}, {"max-ink", *maxInk > 0}} {
		if !c.ok {
			return cli.Usage(stderr, "press", "--%s must be greater than 0", c.flag)
		}
	}
	if *maxInk > 400 {
		return cli.Usage(stderr, "press", "--max-ink must be greater than 0 and at most 400, the most four inks can lay down")
	}
	if *jpeg < 0 || *jpeg > 100 {
		return cli.Usage(stderr, "press", "--jpeg must be 1 to 100, or 0 for lossless")
	}
	j := job{maxPPI: *maxPPI, maxRMSE: *maxRMSE, proofWidth: *proofWidth, maxInk: *maxInk, jpeg: *jpeg, log: stdout}
	for _, s := range specs {
		r, err := parseRegion(s)
		if err != nil {
			return cli.Usage(stderr, "press", "%v", err)
		}
		j.regions = append(j.regions, r)
	}
	for _, s := range cmyks {
		sp, err := parseSpot(s)
		if err != nil {
			return cli.Usage(stderr, "press", "%v", err)
		}
		j.spots = append(j.spots, sp)
	}
	var err error
	if j.icc, err = filepath.Abs(*icc); err == nil {
		if _, err = os.Stat(j.icc); err == nil {
			if j.prof, err = cmm.Load(j.icc); err == nil {
				if *verifyOnly != "" {
					j.in, j.out = rest[0], *verifyOnly
					err = j.check(ctx)
				} else {
					j.in, j.out = rest[0], rest[1]
					err = j.run(ctx)
				}
			}
		}
	}
	if err != nil {
		return cli.Fail(stderr, "press", err)
	}
	return 0
}

func parseRegion(s string) (region, error) {
	box, max, ok := strings.Cut(s, ":")
	if !ok {
		return region{}, fmt.Errorf("--region %q: want x,y,w,h:max", s)
	}
	f, err := raster.ParseFrac(box)
	if err != nil {
		return region{}, err
	}
	m, ok := cli.Finite(max)
	if !ok || m <= 0 {
		return region{}, fmt.Errorf("--region %q: max must be a positive RMSE", s)
	}
	return region{spec: box, f: f, max: m}, nil
}

func pt(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) }

// check proves an existing master against its source, writing nothing.
func (j job) check(ctx context.Context) error {
	src, err := pdf.Read(ctx, j.in)
	if err != nil {
		return err
	}
	return j.verify(ctx, src)
}

func (j job) run(ctx context.Context) (err error) {
	for _, other := range []string{j.in, j.icc} {
		if engine.SameFile(other, j.out) {
			return fmt.Errorf("the output is the same file as an input, %s", j.out)
		}
	}
	if err := os.Remove(j.out); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	defer func() {
		if err != nil {
			os.Remove(j.out)
		}
	}()
	src, err := pdf.Read(ctx, j.in)
	if err != nil {
		return err
	}
	sizes, err := pdf.PageSizes(ctx, j.in, src.Pages)
	if err != nil {
		return err
	}
	for i, s := range sizes {
		if s != sizes[0] {
			return fmt.Errorf("page %d is %gx%g pt and page 1 is %gx%g pt; press fixes one media size for the whole file", i+1, s[0], s[1], sizes[0][0], sizes[0][1])
		}
	}
	// The spots go in before the pass: gs converts rgb through the profile
	// and keeps a cmyk already in the output profile's space as it is.
	gsIn := j.in
	if len(j.spots) > 0 {
		tmp, err := os.MkdirTemp("", "press-spots-")
		if err != nil {
			return err
		}
		defer os.RemoveAll(tmp)
		qdf := filepath.Join(tmp, "in.qdf.pdf")
		if _, err := engine.Run(ctx, engine.Cmd{Engine: "qpdf", Args: []string{"--warning-exit-0", "--qdf", "--object-streams=disable", j.in, qdf}, Inputs: []string{j.in}, Outputs: []string{qdf}}); err != nil {
			return err
		}
		b, err := os.ReadFile(qdf)
		if err != nil {
			return err
		}
		b, n := withSpots(b, j.spots)
		if err := os.WriteFile(qdf, b, 0o644); err != nil {
			return err
		}
		fixed := filepath.Join(tmp, "spots.pdf")
		if _, err := engine.Run(ctx, engine.Cmd{Engine: "fix-qdf", Args: []string{qdf, fixed}, Inputs: []string{qdf}, Outputs: []string{fixed}}); err != nil {
			return err
		}
		fmt.Fprintf(j.log, "%d fill(s) and stroke(s) set to their brand CMYK\n", n)
		gsIn = fixed
	}
	ppi := pt(j.maxPPI)
	// gs imposes its own media and shifts the artwork onto it unless the
	// source's box is forced. Its pdfwrite defaults downsample to 72 dpi and
	// re-encode JPEG; every image flag below undoes one of those.
	// --permit-file-read: -dSAFER otherwise refuses the profile and reports
	// it only after writing a plausible file.
	// -dCompatibilityLevel=1.3 flattens transparency, painting each
	// transparent area as a CMYK image at -r: left live, a soft-masked
	// tiling pattern (Chrome's CSS gradient with a transparent stop) comes
	// out with an XStep wider than its BBox, which macOS draws as seams and
	// a second tile, and a print RIP may too.
	args := []string{"-q", "-dBATCH", "-dNOPAUSE", "-dSAFER",
		"--permit-file-read=" + j.icc,
		"-sDEVICE=pdfwrite", "-dNoOutputFonts",
		"-dCompatibilityLevel=1.3", "-r" + ppi,
		"-sColorConversionStrategy=CMYK", "-sOutputICCProfile=" + j.icc,
		"-dRenderIntent=1", "-dBlackPtComp=1",
		"-dFIXEDMEDIA", "-dDEVICEWIDTHPOINTS=" + pt(src.W), "-dDEVICEHEIGHTPOINTS=" + pt(src.H),
		"-dAutoFilterColorImages=" + strconv.FormatBool(j.jpeg > 0), "-dAutoFilterGrayImages=false",
		"-dColorImageFilter=/FlateEncode", "-dGrayImageFilter=/FlateEncode",
		"-dDownsampleColorImages=true", "-dDownsampleGrayImages=true",
		"-dColorImageDownsampleType=/Bicubic", "-dGrayImageDownsampleType=/Bicubic",
		"-dColorImageResolution=" + ppi, "-dGrayImageResolution=" + ppi,
		"-dColorImageDownsampleThreshold=1.5", "-dGrayImageDownsampleThreshold=1.5",
		"-dDownsampleMonoImages=false",
		"-sOutputFile=" + gsLiteral(j.out)}
	if len(j.spots) > 0 {
		// The spots' cmyk is in the output profile's space, so gs keeps it.
		args = append(args, "-sDefaultCMYKProfile="+j.icc)
	}
	if j.jpeg > 0 {
		args = append(args, "-c", jpegParams(j.jpeg), "-f")
	}
	args = append(args, gsIn)
	if _, err := engine.Run(ctx, engine.Cmd{Engine: "gs", Args: args, StderrFatal: true, Inputs: []string{gsIn, j.icc}, Outputs: []string{j.out}, Timeout: gsTimeout}); err != nil {
		return err
	}
	return j.verify(ctx, src)
}

// jpegParams is the PostScript that sets the JPEG quality q, run before the
// input file: setdistillerparams is the one route pdfwrite reads a filter's
// parameters from. With --jpeg, Ghostscript's auto filter picks per colour
// image: JPEG through ColorACSImageDict for a photographic one, Flate for
// flat artwork, which JPEG makes bigger (a map page doubled when every image
// went to JPEG). Grey images, soft masks among them, always stay Flate.
// Ghostscript takes the quality as a QFactor on its base tables, the IJG
// scale: (200 - 2q) / 100 at q >= 50, 50 / q below. Samples of 1 on every
// channel keep colour at full resolution, since a print master's coloured
// edges matter as much as its brightness.
func jpegParams(q int) string {
	f := 50.0 / float64(q)
	if q >= 50 {
		f = float64(200-2*q) / 100
	}
	return fmt.Sprintf("<< /ColorACSImageDict << /QFactor %s /Blend 1 /HSamples [1 1 1 1] /VSamples [1 1 1 1] >> >> setdistillerparams", strconv.FormatFloat(f, 'f', -1, 64))
}

// gsLiteral escapes a path for -sOutputFile, where Ghostscript reads %d
// and its kin as a page-number template.
func gsLiteral(path string) string { return strings.ReplaceAll(path, "%", "%%") }

// withoutImageData is a QDF file with the data of every image stream cut
// out: press stores images Flate-encoded and QDF decodes them, so their
// pixel bytes would otherwise be scanned as if they were page content.
func withoutImageData(qdf []byte) []byte {
	var out []byte
	for {
		i := bytes.Index(qdf, []byte("\nstream\n"))
		if i < 0 {
			return append(out, qdf...)
		}
		head := i + len("\nstream\n")
		out = append(out, qdf[:head]...)
		dict := qdf[:i]
		if o := bytes.LastIndex(dict, []byte(" obj\n")); o >= 0 {
			dict = dict[o:]
		}
		end := bytes.Index(qdf[head:], []byte("\nendstream"))
		if end < 0 {
			return out
		}
		if !imageDict.Match(dict) {
			out = append(out, qdf[head:head+end]...)
		}
		qdf = qdf[head+end:]
	}
}

var imageDict = regexp.MustCompile(`/Subtype\s*/Image\b`)

func tableRows(out []byte) []string {
	lines := strings.Split(strings.TrimRight(string(out), "\n"), "\n")
	var rows []string
	for i, l := range lines {
		if i >= 2 && strings.TrimSpace(l) != "" {
			rows = append(rows, l)
		}
	}
	return rows
}

func (j job) verify(ctx context.Context, src pdf.Info) error {
	res, err := engine.Run(ctx, engine.Cmd{Engine: "pdffonts", Args: []string{j.out}, Inputs: []string{j.out}})
	if err != nil {
		return err
	}
	if rows := tableRows(res.Stdout); len(rows) > 0 {
		return fmt.Errorf("%d font(s) survived outlining:\n%s", len(rows), strings.Join(rows, "\n"))
	}
	res, err = engine.Run(ctx, engine.Cmd{Engine: "pdfimages", Args: []string{"-list", j.out}, Inputs: []string{j.out}})
	if err != nil {
		return err
	}
	var bad []string
	for _, row := range tableRows(res.Stdout) {
		f := strings.Fields(row)
		// A stencil mask (an /ImageMask) has no colour space of its own: it
		// paints in the fill colour, which the operator scan below checks.
		if len(f) < 6 || (f[2] != "stencil" && f[5] != "cmyk" && f[5] != "gray") {
			bad = append(bad, row)
		}
	}
	if len(bad) > 0 {
		return fmt.Errorf("non-CMYK image(s) in the master:\n%s", strings.Join(bad, "\n"))
	}
	tmp, err := os.MkdirTemp("", "plate-press-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	qdf := filepath.Join(tmp, "qdf.pdf")
	if _, err := engine.Run(ctx, engine.Cmd{Engine: "qpdf", Args: []string{"--warning-exit-0", "--qdf", "--object-streams=disable", j.out, qdf}, Inputs: []string{j.out}, Outputs: []string{qdf}}); err != nil {
		return err
	}
	b, err := os.ReadFile(qdf)
	if err != nil {
		return err
	}
	var spaces []string
	for _, m := range nonCMYK.FindAll(withoutImageData(b), -1) {
		s := strings.TrimSpace(string(m))
		if !slices.Contains(spaces, s) {
			spaces = append(spaces, s)
		}
	}
	if len(spaces) > 0 {
		return fmt.Errorf("non-CMYK colour in the master: %s", strings.Join(spaces, " "))
	}
	got, err := pdf.Read(ctx, j.out)
	if err != nil {
		return err
	}
	if got.W != src.W || got.H != src.H {
		return fmt.Errorf("the page box changed from %gx%g to %gx%g pt", src.W, src.H, got.W, got.H)
	}
	if got.Pages != src.Pages {
		return fmt.Errorf("the page count changed from %d to %d", src.Pages, got.Pages)
	}
	dpi := j.dpi(src)
	cmyk, err := renderCMYK(ctx, tmp, j.out, pt(dpi), src.Pages)
	if err != nil {
		return err
	}
	for p, path := range cmyk {
		img, err := raster.ReadPAM(path)
		if err != nil {
			return err
		}
		over, bad := inkLimit(img, j.maxInk, dpi)
		fmt.Fprintf(j.log, "page %d: peak ink %.0f%% (max %g%%)\n", p+1, over.Peak, j.maxInk)
		if bad {
			return fmt.Errorf("page %d: %.0f%% ink over %.1f mm² around %s, above --max-ink %g; lower the colours there or raise --max-ink to what the printer accepts", p+1, over.Peak, over.Area, boxSpec(over.Box), j.maxInk)
		}
	}
	if err := j.proof(ctx, tmp, src, cmyk); err != nil {
		return err
	}
	fmt.Fprintf(j.log, "wrote %s: no fonts, CMYK only, %d page(s) at %gx%g pt\n", j.out, src.Pages, src.W, src.H)
	return nil
}

// proof renders the source with Ghostscript and the master's CMYK pages
// (cmyk, the PAMs the ink check read) through plate's own colour engine, in
// the relative colorimetric intent with black point compensation that press
// converts with, and compares them: what the press will print against what
// the screen showed. The regions exist because a page average hides a small
// element that went missing.
func (j job) proof(ctx context.Context, tmp string, src pdf.Info, cmyk []string) error {
	dpi := pt(j.dpi(src))
	page := func(p int) string { return filepath.Join(tmp, "src-"+strconv.Itoa(p)+".png") }
	args := append(renderBase(dpi), "-sDEVICE=png16m", "-sOutputFile="+gsLiteral(tmp)+string(filepath.Separator)+"src-%d.png", j.in)
	if _, err := engine.Run(ctx, engine.Cmd{Engine: "gs", Args: args, StderrFatal: true, Inputs: []string{j.in}, Outputs: []string{page(1)}, Timeout: gsTimeout}); err != nil {
		return err
	}
	for p := 1; p <= src.Pages; p++ {
		a, err := raster.Load(page(p))
		if err != nil {
			return err
		}
		m, err := raster.ReadPAM(cmyk[p-1])
		if err != nil {
			return err
		}
		if sz := a.Bounds().Size(); sz.X != m.W || sz.Y != m.H {
			return fmt.Errorf("page %d: the source renders %dx%d px and the master %dx%d px", p, sz.X, sz.Y, m.W, m.H)
		}
		b := j.prof.ToSRGB(m)
		v, err := raster.RMSE(a, b, a.Bounds(), raster.RGB)
		if err != nil {
			return err
		}
		fmt.Fprintf(j.log, "page %d: soft-proof RMSE %.4f (max %g)\n", p, v, j.maxRMSE)
		if v > j.maxRMSE {
			return fmt.Errorf("page %d: the soft proof differs from the source by RMSE %.4f, above %g; open both and look", p, v, j.maxRMSE)
		}
		for _, r := range j.regions {
			v, err := raster.RMSE(a, b, r.f.In(a.Bounds()), raster.RGB)
			if err != nil {
				return err
			}
			fmt.Fprintf(j.log, "page %d region %s: soft-proof RMSE %.4f (max %g)\n", p, r.spec, v, r.max)
			if v > r.max {
				return fmt.Errorf("page %d region %s: the soft proof differs from the source by RMSE %.4f, above %g; open both and look", p, r.spec, v, r.max)
			}
		}
	}
	return nil
}
