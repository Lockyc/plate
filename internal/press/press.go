// Package press makes a print master from a PDF with one Ghostscript pass:
// every glyph outlined, every colour converted to the supplied CMYK profile,
// the page box forced from the source, images kept lossless and downsampled
// only well above --max-ppi. It then proves the master: no fonts, no RGB
// anywhere, the same page boxes and count, and a soft proof of the master
// that matches the source by RMSE over each page and each --region. The
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
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/lockyc/plate/internal/cli"
	"github.com/lockyc/plate/internal/engine"
	"github.com/lockyc/plate/internal/pdf"
	"github.com/lockyc/plate/internal/raster"
)

const usage = "press --icc profile.icc [--max-ppi N] [--max-rmse R] [--region x,y,w,h:max]... [--proof-width PX] <in.pdf> <out.pdf>"

// defaultMaxRMSE separated clean masters (at most 0.066) from masters with a
// hidden element (0.083 and above) on the print artboards press was built
// against. Calibrate a project's own value against a deliberately broken
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

type multi []string

func (m *multi) String() string     { return strings.Join(*m, " ") }
func (m *multi) Set(s string) error { *m = append(*m, s); return nil }

type job struct {
	in, out, icc    string
	maxPPI, maxRMSE float64
	proofWidth      int
	regions         []region
	log             io.Writer
}

// Main runs `plate press`.
func Main(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := cli.Flags("press", usage, stderr)
	icc := fs.String("icc", "", "the CMYK output profile (required)")
	maxPPI := cli.Float(fs, "max-ppi", 450, "downsample images above 1.5 × this resolution, to it")
	maxRMSE := cli.Float(fs, "max-rmse", defaultMaxRMSE, "soft-proof RMSE allowed over a whole page")
	proofWidth := cli.Int(fs, "proof-width", 3000, "soft-proof render width in px; RMSE settles by about 3000")
	var specs multi
	fs.Var(&specs, "region", "x,y,w,h:max: a page region, as fractions, held to its own RMSE (repeatable)")
	rest, code, ok := cli.Parse(fs, args, 2)
	if !ok {
		return code
	}
	if *icc == "" {
		defer fs.Usage()
		return cli.Usage(stderr, "press", "--icc is required")
	}
	for _, c := range []struct {
		flag string
		ok   bool
	}{{"max-ppi", *maxPPI > 0}, {"max-rmse", *maxRMSE > 0}, {"proof-width", *proofWidth > 0}} {
		if !c.ok {
			return cli.Usage(stderr, "press", "--%s must be greater than 0", c.flag)
		}
	}
	j := job{maxPPI: *maxPPI, maxRMSE: *maxRMSE, proofWidth: *proofWidth, log: stdout}
	for _, s := range specs {
		r, err := parseRegion(s)
		if err != nil {
			return cli.Usage(stderr, "press", "%v", err)
		}
		j.regions = append(j.regions, r)
	}
	var err error
	if j.icc, err = filepath.Abs(*icc); err == nil {
		if _, err = os.Stat(j.icc); err == nil {
			j.in, j.out = rest[0], rest[1]
			err = j.run(ctx)
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
	ppi := pt(j.maxPPI)
	// gs imposes its own media and shifts the artwork onto it unless the
	// source's box is forced. Its pdfwrite defaults downsample to 72 dpi and
	// re-encode JPEG; every image flag below undoes one of those.
	// --permit-file-read: -dSAFER otherwise refuses the profile and reports
	// it only after writing a plausible file.
	args := []string{"-q", "-dBATCH", "-dNOPAUSE", "-dSAFER",
		"--permit-file-read=" + j.icc,
		"-sDEVICE=pdfwrite", "-dNoOutputFonts",
		"-sColorConversionStrategy=CMYK", "-sOutputICCProfile=" + j.icc,
		"-dRenderIntent=1", "-dBlackPtComp=1",
		"-dFIXEDMEDIA", "-dDEVICEWIDTHPOINTS=" + pt(src.W), "-dDEVICEHEIGHTPOINTS=" + pt(src.H),
		"-dAutoFilterColorImages=false", "-dAutoFilterGrayImages=false",
		"-dColorImageFilter=/FlateEncode", "-dGrayImageFilter=/FlateEncode",
		"-dDownsampleColorImages=true", "-dDownsampleGrayImages=true",
		"-dColorImageDownsampleType=/Bicubic", "-dGrayImageDownsampleType=/Bicubic",
		"-dColorImageResolution=" + ppi, "-dGrayImageResolution=" + ppi,
		"-dColorImageDownsampleThreshold=1.5", "-dGrayImageDownsampleThreshold=1.5",
		"-dDownsampleMonoImages=false",
		"-sOutputFile=" + gsLiteral(j.out), j.in}
	if _, err := engine.Run(ctx, engine.Cmd{Engine: "gs", Args: args, StderrFatal: true, Inputs: []string{j.in, j.icc}, Outputs: []string{j.out}, Timeout: gsTimeout}); err != nil {
		return err
	}
	return j.verify(ctx, src)
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
	if _, err := engine.Run(ctx, engine.Cmd{Engine: "qpdf", Args: []string{"--qdf", "--object-streams=disable", j.out, qdf}, Inputs: []string{j.out}, Outputs: []string{qdf}}); err != nil {
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
	if err := j.proof(ctx, tmp, src); err != nil {
		return err
	}
	fmt.Fprintf(j.log, "wrote %s: no fonts, CMYK only, %d page(s) at %gx%g pt\n", j.out, src.Pages, src.W, src.H)
	return nil
}

// proof renders source and master with Ghostscript, the master's CMYK read
// back through the same profile, and compares them: what the press will
// print against what the screen showed. The regions exist because a page
// average hides a small element that went missing.
func (j job) proof(ctx context.Context, tmp string, src pdf.Info) error {
	dpi := pt(float64(j.proofWidth) * 72 / src.W)
	common := []string{"-q", "-dBATCH", "-dNOPAUSE", "-dSAFER", "-sDEVICE=png16m", "-r" + dpi, "-dTextAlphaBits=4", "-dGraphicsAlphaBits=4"}
	page := func(name string, p int) string { return filepath.Join(tmp, name+"-"+strconv.Itoa(p)+".png") }
	pattern := func(name string) string { return gsLiteral(tmp) + string(filepath.Separator) + name + "-%d.png" }
	if _, err := engine.Run(ctx, engine.Cmd{Engine: "gs", Args: append(common, "-sOutputFile="+pattern("src"), j.in), StderrFatal: true, Inputs: []string{j.in}, Outputs: []string{page("src", 1)}, Timeout: gsTimeout}); err != nil {
		return err
	}
	outArgs := append(common, "--permit-file-read="+j.icc, "-sDefaultCMYKProfile="+j.icc, "-dRenderIntent=1", "-dBlackPtComp=1", "-sOutputFile="+pattern("out"), j.out)
	if _, err := engine.Run(ctx, engine.Cmd{Engine: "gs", Args: outArgs, StderrFatal: true, Inputs: []string{j.out, j.icc}, Outputs: []string{page("out", 1)}, Timeout: gsTimeout}); err != nil {
		return err
	}
	for p := 1; p <= src.Pages; p++ {
		a, err := raster.Load(page("src", p))
		if err != nil {
			return err
		}
		b, err := raster.Load(page("out", p))
		if err != nil {
			return err
		}
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
