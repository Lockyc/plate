// Package pins is the one table of engine versions. doctor, every "not
// installed" error and the managed installer read it; no engine version is
// written anywhere else in plate.
package pins

import (
	"runtime"
	"slices"
	"strconv"
	"strings"
)

// Kind says how an engine is pinned.
type Kind int

const (
	// Minimum engines are found on PATH at or above Min.
	Minimum Kind = iota
	// Managed engines are downloaded by `plate doctor --install` at exactly
	// Download.Version into the data directory.
	Managed
)

// Engine is one external tool plate runs. For Minimum engines Name is also
// the executable looked up on PATH.
type Engine struct {
	Name        string
	Kind        Kind
	VersionArgs []string          // Minimum: arguments that make it print its version
	VersionRe   string            // Minimum: the first group captures the version
	Min         string            // Minimum: the lowest version plate accepts
	Install     map[string]string // Minimum: GOOS -> the command a person runs
	Download    *Download         // Managed
	GOOS        []string          // platforms it exists on; nil means every platform
	UsedBy      []string          // the plate commands that need it
}

// Download is a managed engine's pinned release.
type Download struct {
	Version string
	Assets  map[string]Asset // "GOOS/GOARCH" -> archive
}

// Asset is one platform's release archive. Bin is the executable's path
// inside the unpacked archive.
type Asset struct {
	URL, SHA256, Bin string
}

const chsVersion = "154.0.8037.92"

func chs(platform, sha string) Asset {
	return Asset{
		URL:    "https://storage.googleapis.com/chrome-for-testing-public/" + chsVersion + "/" + platform + "/chrome-headless-shell-" + platform + ".zip",
		SHA256: sha,
		Bin:    "chrome-headless-shell-" + platform + "/chrome-headless-shell",
	}
}

func poppler(name string, usedBy ...string) Engine {
	return Engine{
		Name: name, Kind: Minimum,
		VersionArgs: []string{"-v"}, VersionRe: `version (\d+\.\d+\.\d+)`, Min: "22.0.0",
		Install: map[string]string{"darwin": "brew install poppler", "linux": "sudo apt install poppler-utils"},
		UsedBy:  usedBy,
	}
}

// Engines is every external tool plate runs.
var Engines = []Engine{
	{
		Name: "magick", Kind: Minimum,
		VersionArgs: []string{"-version"}, VersionRe: `Version: ImageMagick (\d+\.\d+\.\d+(?:-\d+)?)`, Min: "7.1.0",
		Install: map[string]string{"darwin": "brew install imagemagick", "linux": "install ImageMagick from https://imagemagick.org/script/download.php (distribution packages are often too old)"},
		UsedBy:  []string{"cutout", "infill", "inpaint", "grade", "upscale", "shrink"},
	},
	{
		Name: "gs", Kind: Minimum,
		VersionArgs: []string{"--version"}, VersionRe: `^(\d+\.\d+(?:\.\d+)?)`, Min: "10.0.0",
		Install: map[string]string{"darwin": "brew install ghostscript", "linux": "sudo apt install ghostscript"},
		UsedBy:  []string{"press"},
	},
	poppler("pdfinfo", "render", "press", "pdf"),
	poppler("pdffonts", "press"),
	poppler("pdfimages", "press", "pdf"),
	poppler("pdftotext", "render", "pdf"),
	poppler("pdftoppm", "pdf"),
	{
		Name: "pandoc", Kind: Minimum,
		VersionArgs: []string{"--version"}, VersionRe: `pandoc (\d+\.\d+(?:\.\d+)*)`, Min: "3.0",
		Install: map[string]string{"darwin": "brew install pandoc", "linux": "install pandoc from https://github.com/jgm/pandoc/releases (distribution packages are often too old)"},
		UsedBy:  []string{"doc"},
	},
	{
		Name: "hb-subset", Kind: Minimum,
		VersionArgs: []string{"--version"}, VersionRe: `hb-subset \(HarfBuzz\) (\d+\.\d+\.\d+)`, Min: "6.0.0",
		Install: map[string]string{"darwin": "brew install harfbuzz", "linux": "sudo apt install libharfbuzz-bin"},
		UsedBy:  []string{"fonts"},
	},
	{
		Name: "qpdf", Kind: Minimum,
		VersionArgs: []string{"--version"}, VersionRe: `qpdf version (\d+\.\d+\.\d+)`, Min: "11.0.0",
		Install: map[string]string{"darwin": "brew install qpdf", "linux": "sudo apt install qpdf"},
		UsedBy:  []string{"press"},
	},
	{
		Name: "qrencode", Kind: Minimum,
		VersionArgs: []string{"--version"}, VersionRe: `qrencode version (\d+\.\d+\.\d+)`, Min: "4.0.0",
		Install: map[string]string{"darwin": "brew install qrencode", "linux": "sudo apt install qrencode"},
		UsedBy:  []string{"qr"},
	},
	{
		Name: "uv", Kind: Minimum,
		VersionArgs: []string{"--version"}, VersionRe: `uv (\d+\.\d+\.\d+)`, Min: "0.12.0",
		Install: map[string]string{"darwin": "brew install uv", "linux": "curl -LsSf https://astral.sh/uv/install.sh | sh"},
		UsedBy:  []string{"cutout", "grade", "inpaint", "upscale"},
	},
	{
		Name: "swiftc", Kind: Minimum, GOOS: []string{"darwin"},
		VersionArgs: []string{"--version"}, VersionRe: `Swift version (\d+\.\d+(?:\.\d+)?)`, Min: "5.9",
		Install: map[string]string{"darwin": "xcode-select --install"},
		UsedBy:  []string{"cutout"},
	},
	{
		Name: "chrome-headless-shell", Kind: Managed,
		Download: &Download{Version: chsVersion, Assets: map[string]Asset{
			"darwin/arm64": chs("mac-arm64", "77da14e75d7f2568e6f7898d3df7cdc6faac74b15e903b2c9d486ebb6ca9b929"),
			"darwin/amd64": chs("mac-x64", "a54292aaacbb77f76f6ef47558e7c51ab884044e0adacca315567f83c060bcc4"),
			"linux/amd64":  chs("linux64", "636aa5c79f2693632e9921b8bbb050038ba11672e02346c06c20f991aed096f9"),
		}},
		UsedBy: []string{"render", "doc", "slides"},
	},
}

// Lookup finds an engine by name.
func Lookup(name string) (Engine, bool) {
	for _, e := range Engines {
		if e.Name == name {
			return e, true
		}
	}
	return Engine{}, false
}

// Supported reports whether the engine exists on this platform.
func (e Engine) Supported() bool { return e.supportedOn(runtime.GOOS) }

func (e Engine) supportedOn(goos string) bool {
	return e.GOOS == nil || slices.Contains(e.GOOS, goos)
}

// Asset is the managed engine's archive for this platform.
func (e Engine) Asset() (Asset, bool) {
	if e.Download == nil {
		return Asset{}, false
	}
	a, ok := e.Download.Assets[runtime.GOOS+"/"+runtime.GOARCH]
	return a, ok
}

// Want is the version requirement as doctor prints it.
func (e Engine) Want() string {
	if e.Kind == Managed {
		return "= " + e.Download.Version
	}
	return ">= " + e.Min
}

// Hint says how to get the engine, for "not installed" errors and doctor.
func (e Engine) Hint() string {
	if !e.Supported() {
		return "not available on " + runtime.GOOS
	}
	if e.Kind == Managed {
		if _, ok := e.Asset(); !ok {
			return "no build for this platform"
		}
		return "run `plate doctor --install`"
	}
	if c := e.Install[runtime.GOOS]; c != "" {
		return c
	}
	return "install " + e.Name
}

// AtLeast compares dotted versions numerically, field by field. Non-digit
// runs separate fields, so ImageMagick's "7.1.2-31" reads as 7.1.2.31.
func AtLeast(have, min string) bool {
	h, m := fields(have), fields(min)
	for i := range m {
		hv := 0
		if i < len(h) {
			hv = h[i]
		}
		if hv != m[i] {
			return hv > m[i]
		}
	}
	return true
}

func fields(v string) []int {
	var out []int
	for _, f := range strings.FieldsFunc(v, func(r rune) bool { return r < '0' || r > '9' }) {
		n, _ := strconv.Atoi(f)
		out = append(out, n)
	}
	return out
}

// Model is a machine-learning model an ML script downloads on first run,
// pinned to a Hugging Face commit so a re-uploaded model cannot change a
// result silently. File names the weights file when the script loads one
// file rather than the whole repo.
type Model struct{ Repo, Revision, File string }

// ViTMatte refines a coarse mask into a per-strand alpha matte.
var ViTMatte = Model{Repo: "hustvl/vitmatte-base-composition-1k", Revision: "bf486d01a7d9e3dbcc8400f7942835caf0eaf76e"}

// DAT is the Dual Aggregation Transformer, ×4, trained for fidelity to the
// original (PSNR) rather than for invented texture.
var DAT = Model{Repo: "OzzyGT/DAT_X4", Revision: "549c8c9c3611084e1dc0ce235eba8c5fa8bca9ae", File: "DAT_x4.safetensors"}

// PyTool is an upstream Python CLI run with `uv tool run`, resolved as of
// ExcludeNewer so its transitive dependencies cannot drift.
type PyTool struct{ Package, Version, ExcludeNewer string }

// IOPaint runs LaMa.
var IOPaint = PyTool{Package: "iopaint", Version: "1.6.0", ExcludeNewer: "2026-10-01T00:00:00Z"}
