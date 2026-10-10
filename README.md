# plate

[![Release](https://img.shields.io/github/v/release/lockyc/plate?sort=semver&label=release)](https://github.com/lockyc/plate/releases/latest)
[![CI](https://github.com/lockyc/plate/actions/workflows/ci.yml/badge.svg)](https://github.com/lockyc/plate/actions/workflows/ci.yml)
![Platform](https://img.shields.io/badge/platform-macOS%20%7C%20Linux-555)
![Go](https://img.shields.io/badge/go-1.27%2B-00ADD8?logo=go&logoColor=white)
[![License](https://img.shields.io/github/license/lockyc/plate)](LICENSE)

Image, render and document operations for print and web pipelines, as one
command: lift a subject out of a photo, fill, enlarge or shrink artwork, render HTML to
PNG, PDF or its settled DOM, preview a Google Slides deck, turn markdown into a
PDF, look inside a PDF, make a press-ready PDF, match a colour grade. Each
operation wraps the best engine
for the job at a pinned version, and its quality is measured against a
committed set of test images rather than judged by eye.

## Status

Every command below works on Apple Silicon Macs and on Linux, with
the exceptions listed under [Platforms](#platforms). What comes next is in
[docs/roadmap.md](docs/roadmap.md), and how it is built is in
[docs/design.md](docs/design.md).

| Command | Does | Usage |
|---|---|---|
| `cutout` | Lifts the subject out of an image as RGBA, down to hair and fur | `plate cutout [--coarse vision\|birefnet] [--height PX] [--tile PX] [--band-in D] [--band-out D] [--despill PRESET] [--despill-hue A:B] [--despill-clean A:B] [--despill-chroma A:B] [--despill-lmax L] [--despill-hue-end H] <in> <out.png>` |
| `inpaint` ⚠ | Fills a masked region with LaMa | `plate inpaint --mask mask.png <in> <out.png>` |
| `infill` ⚠ | Fills a hole in a flat ground by blending blurred copies of its surroundings, then lays the ground's own grain over it | `plate infill --mask mask.png [--levels 150,60,20,6] [--grain 1] [--seed 1] <in> <out.png>` |
| `upscale` ⚠ | Enlarges 4× with DAT, a super-resolution model that sharpens what the small image holds rather than inventing texture | `plate upscale <in> <out.png>` |
| `grade fit` | Recovers a reference image's colour grade as a lookup table | `plate grade fit --ref ref.png [--ref-crop x,y,w,h] [--exclude x,y,w,h]... [--level 8] [--min-inliers 40] <subject.png> <out-hald.png>` |
| `grade apply` | Applies that lookup table, keeping transparency | `plate grade apply --clut hald.png <in> <out.png>` |
| `render` | HTML to PNG, PDF or settled HTML with headless Chrome, guarded against Chrome's silent failures | `plate render [--png out.png [--transparent]] [--pdf out.pdf] [--html out.html] [--size WxH] [--scale S] [--budget MS] [--fail-if TEXT]... <page.html\|http(s) or file URL>` |
| `press` | Converts a PDF to a print master (text as outlines, CMYK colour) and checks it against a soft proof | `plate press --icc profile.icc [--max-ppi N] [--max-rmse R] [--region x,y,w,h:max]... [--proof-width PX] <in.pdf> <out.pdf>` |
| `pdf info` | Prints a PDF's metadata and whether it has a text layer, which says whether to read its text or render its pages | `plate pdf info <in.pdf>` |
| `pdf text` | Extracts a PDF's text with its layout | `plate pdf text [--pages N\|N-M] <in.pdf>` |
| `pdf pages` | Renders PDF pages to PNG and prints each path | `plate pdf pages [--pages N\|N-M] [--out DIR] [--dpi N] <in.pdf>` |
| `pdf images` | Extracts a PDF's embedded images and prints each path | `plate pdf images [--pages N\|N-M] [--out DIR] <in.pdf>` |
| `doc` | Renders markdown or HTML to a PDF with a plain document look: A4, a running head from the first heading, page numbers, ruled tables | `plate doc [--css FILE] <in.md\|in.html> <out.pdf>` |
| `slides` | Previews a Google Slides deck from its Slides API JSON, one PNG per slide, in the faces the deck names: for a deck Drive will not export | `plate slides [--pages LIST] [--out DIR] [--scale S] <deck.json\|->` |
| `shrink` | Resizes down to a small, sharp web asset: PNG compressed losslessly as far as zlib goes, or WebP, alpha kept, never enlarging | `plate shrink (--width PX \| --height PX \| --fit PX) [--quality Q] <in> <out.png\|out.webp>...` |
| `diff` | Compares two images after lining them up, so a small shift doesn't fail everything | `plate diff [--tolerance N] [--max-shift PX] [--step PX] [--threshold PCT] [--out diff.png] <a> <b>` |
| `fonts` | Embeds web fonts, from files or from Google Fonts by family name, into a CSS file so a page renders from the filesystem, optionally cut to the characters a language uses | `plate fonts [--display block] [--subset latin] [--google FAMILY:WEIGHT:STYLE]... -o fonts.css [FILE:FAMILY:WEIGHT:STYLE]...` |
| `qr` | Makes a QR code SVG that any phone camera decodes | `plate qr [--ec L\|M\|Q\|H] [--fg RRGGBB] [--bg RRGGBB] -o out.svg <text>` |
| `doctor` | Checks every engine against its pin, installs the ones plate manages, and prints the install command for the rest | `plate doctor [--install]` |
| `version` | Prints plate's version | `plate version` |

`plate <command> -h` lists each flag with its default.

`render --html` writes the page's DOM after its scripts have run, a static
copy to publish; it needs no `--size`, and refuses a page that does not end in
`</html>` or that contains a `--fail-if` string.

`render --png --transparent` gives the PNG an alpha channel: whatever the
page leaves transparent (a `background: transparent` html and body) is
alpha 0 rather than white, for artwork laid over other artwork.

`fonts --subset latin` cuts each font to Basic Latin, Latin-1 Supplement,
the common punctuation (dashes, curly quotes, ellipsis) and the euro sign
before embedding it, which takes a typical text face from about 150 KB to
about 35 KB. It needs `.ttf` or `.otf` input and keeps that format.

`fonts --google` fetches a face from Google Fonts once into plate's cache.
Google also serves the faces Docs and Slides offer outside its catalogue,
such as Calibri; a face it does not serve, such as Arial, is an error.

`slides` takes the JSON `presentations.get` returns, as a file or `-` for
stdin; plate never calls Google's APIs itself. It writes each slide as
`sNN.html` beside `sNN.png` in `--out` and prints the PNG paths. `--pages`
takes slide numbers, ranges and slide object IDs, comma-separated. It
fetches the pictures and, through `fonts --google`, every face the slides
name, so text wraps where Slides wraps it; a face Google does not serve is
named on stderr. Picture URLs in the JSON expire about 30 minutes after it
was fetched. Lines, tables, videos, charts and word art are not drawn, and
it says which slides had them; bullets and autofit are not drawn either.

`shrink` fits the image inside `--width` × `--height` (either alone, or `--fit` for
the longest side), with ImageMagick's Catrom filter, the one closest to the
same artwork drawn natively at the small size. Each output's extension picks
its format, and several outputs share one resample, so `o.png o.webp` writes
both to keep the smaller. WebP is lossy at `--quality` (default 90) with
lossless alpha.

`render` runs the page outside Chrome's sandbox, with read access to your
files and open network access: give it only pages you trust.

⚠ These commands create pixels the source never had. A project that forbids
this puts `synthesis = "forbid"` in a `plate.toml`, and they refuse to run.

## Install

```bash
go install github.com/lockyc/plate@latest
plate doctor --install
```

Run `plate doctor --install` first. It downloads the engine plate manages
itself, chrome-headless-shell for `render`, then checks every other engine
against its pin and prints the install command for any that is missing or too
old, naming the commands that will not run until it is fixed.

`uv` is the only Python tool to install. The ML commands (`cutout`, `inpaint`,
`upscale`, `grade fit`) run through it, and the first run of each downloads
its Python dependencies and, for the models, their weights: several GB on
Linux, where PyTorch ships with CUDA. These land in uv's, Hugging Face's,
rembg's and PyTorch's own caches in your home directory, not in plate's.

ViTMatte and DAT run on the GPU when PyTorch finds one; BiRefNet runs on the
CPU, and so does LaMa on a Mac. `PLATE_DEVICE=cpu` (or `mps`, `cuda`)
chooses the PyTorch device instead.

### Platforms

- **macOS 14 or later on Apple Silicon**: every command.
- **Linux with glibc 2.28 or later, x86_64 or arm64**: every command except
  `cutout --coarse vision`, which needs Apple Vision; `cutout` uses
  `--coarse birefnet` there. On arm64, Chrome for Testing publishes no
  chrome-headless-shell build, so `render` needs one supplied through
  `PLATE_ENGINE_CHROME_HEADLESS_SHELL`. chrome-headless-shell needs the usual
  Chrome shared libraries (libnss3 and the like) from the distribution.
- **Intel Macs**: `cutout` and `upscale` refuse to run, because PyTorch and
  ONNX Runtime publish no build for them; `grade fit` needs macOS 14 or later;
  `inpaint` is untested. The other commands work.

## Quality

`just quality` runs the quality cases over the openly licensed and synthetic
images in `quality/` and checks each result against a measured threshold. It
needs every engine and model, so it runs locally, not in CI.

## Development

```bash
just build    # ./plate
just test
just gate     # gofmt check + vet + tests
just quality  # the quality cases over quality/
just lock-ml  # re-resolve the ML scripts' lockfiles after editing a PEP 723 header
just install  # go install, then print the version
```
