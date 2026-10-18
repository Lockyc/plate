# plate

[![Release](https://img.shields.io/github/v/release/lockyc/plate?sort=semver&label=release)](https://github.com/lockyc/plate/releases/latest)
![Platform](https://img.shields.io/badge/platform-macOS%20%7C%20Linux-555)
![Go](https://img.shields.io/badge/go-1.27%2B-00ADD8?logo=go&logoColor=white)
[![License](https://img.shields.io/github/license/lockyc/plate)](LICENSE)

Image, render and document operations for print and web pipelines, as one
command: lift a subject out of a photo, fill, enlarge or shrink artwork, render HTML to
PNG, JPEG, PDF or its settled DOM, cut a screen recording into a short looping demo clip, preview a Google Slides deck, turn markdown into a
PDF, look inside a PDF, make a press-ready PDF, put an A5 flyer two-up on
an A4 sheet to print and cut, match a colour grade. Each
operation wraps an engine at a pinned version, and its output is checked
against a committed set of test images.

## Status

Every command below works on Apple Silicon Macs and on Linux, with
the exceptions listed under [Platforms](#platforms). What comes next is in
[docs/roadmap.md](docs/roadmap.md), and how it is built is in
[docs/design.md](docs/design.md).

| Command | Does | Usage |
|---|---|---|
| `cutout` | Lifts the subject out of an image as RGBA, down to hair and fur | `plate cutout [--coarse vision\|birefnet] [--height PX] [--tile PX] [--band-in D] [--band-out D] [--despill PRESET] [--despill-hue A:B] [--despill-clean A:B] [--despill-chroma A:B] [--despill-lmax L] [--despill-hue-end H] <in> <out.png>` |
| `inpaint` ⚠ | Fills a masked region with LaMa | `plate inpaint --mask mask.png <in> <out.png>` |
| `infill` ⚠ | Fills a hole in a flat ground, matching the surrounding colour and grain | `plate infill --mask mask.png [--levels 150,60,20,6] [--grain 1] [--seed 1] <in> <out.png>` |
| `upscale` ⚠ | Enlarges 4× with DAT, sharpening existing detail without inventing texture | `plate upscale <in> <out.png>` |
| `grade fit` | Recovers a reference image's colour grade as a lookup table | `plate grade fit --ref ref.png [--ref-crop x,y,w,h] [--exclude x,y,w,h]... [--level 8] [--min-inliers 40] <subject.png> <out-hald.png>` |
| `grade apply` | Applies that lookup table, keeping transparency | `plate grade apply --clut hald.png <in> <out.png>` |
| `render` | HTML to PNG, JPEG, PDF or settled HTML with headless Chrome, guarded against Chrome's silent failures, skipped when nothing changed | `plate render [--png out.png [--transparent]] [--jpeg out.jpg [--quality Q]] [--pdf out.pdf] [--html out.html] [--size WxH] [--scale S] [--blank-margin PX] [--budget MS] [--fail-if TEXT]... [--force] <page.html\|http(s) or file URL>` |
| `build` | Renders every target in a manifest in parallel, skipping the unchanged ones | `plate build [--out DIR] [--jobs N] [--force] [--fail-if TEXT]... <manifest> [NAME...]` |
| `press` | Converts a PDF to a print master (text as outlines, CMYK colour) and checks it against a soft proof | `plate press --icc profile.icc [--max-ppi N] [--max-rmse R] [--region x,y,w,h:max]... [--proof-width PX] <in.pdf> <out.pdf>` |
| `pdf info` | Prints a PDF's metadata and whether it has a text layer, which says whether to read its text or render its pages | `plate pdf info <in.pdf>` |
| `pdf text` | Extracts a PDF's text with its layout | `plate pdf text [--pages N\|N-M] <in.pdf>` |
| `pdf pages` | Renders PDF pages to PNG and prints each path | `plate pdf pages [--pages N\|N-M] [--out DIR] [--dpi N] <in.pdf>` |
| `pdf images` | Extracts a PDF's embedded images and prints each path | `plate pdf images [--pages N\|N-M] [--out DIR] <in.pdf>` |
| `impose` | Puts an A5 flyer's front and back two-up on both sides of an A4 sheet: print double-sided, flip on short edge, cut down the middle | `plate impose [--cut-mark] [--force] -o out.pdf <in.pdf>` |
| `doc` | Renders markdown or HTML to a PDF with a plain document look: A4, a running head from the first heading, page numbers, ruled tables | `plate doc [--css FILE] <in.md\|in.html> <out.pdf>` |
| `slides` | Previews a Google Slides deck that Drive will not export, from its Slides API JSON: one PNG per slide, in the fonts the deck names | `plate slides [--pages LIST] [--out DIR] [--scale S] <deck.json\|->` |
| `shrink` | Resizes down to a small, sharp web asset: PNG at maximum lossless compression, or WebP, alpha kept, never enlarging | `plate shrink (--width PX \| --height PX \| --width PX --height PX \| --fit PX) [--quality Q] <in> <out.png\|out.webp>...` |
| `diff` | Compares two images after lining them up, so a small shift doesn't fail everything | `plate diff [--tolerance N] [--max-shift PX] [--step PX] [--threshold PCT] [--out diff.png] <a> <b>` |
| `clip check` | Validates a demo-clip script and prints it normalised, the form a runner reads | `plate clip check [--json] <clip.toml>` |
| `clip render` | Turns a screen recording and its event log into a short looping clip: trimmed, framed by a fixed or following camera, with captions and touch, click, key or cursor overlays; MP4, WebM, poster, WebP or GIF, with a JSON of its measurements | `plate clip render --script clip.toml --master raw.mov --events events.jsonl --out DIR [--caption-css FILE]` |
| `fonts` | Embeds web fonts, from files or from Google Fonts by family name, into a CSS file so a page renders from the filesystem, optionally cut to the characters a language uses | `plate fonts [--display block] [--subset latin] [--google FAMILY:WEIGHT:STYLE]... -o fonts.css [FILE:FAMILY:WEIGHT:STYLE]...` |
| `qr` | Makes a QR code SVG that any phone camera decodes | `plate qr [--ec L\|M\|Q\|H] [--fg RRGGBB] [--bg RRGGBB] -o out.svg <text>` |
| `doctor` | Checks every engine against its pin, installs the ones plate manages, and prints the install command for the rest | `plate doctor [--install]` |
| `version` | Prints plate's version | `plate version` |

`plate <command> -h` lists each flag with its default; for `pdf` and
`grade`, `plate <command> <verb> -h` does.

`render --html` writes the page's DOM after its scripts have run, a static
copy to publish; it needs no `--size`, and refuses a page that does not end in
`</html>` or that contains a `--fail-if` string.

`render --png --transparent` gives the PNG an alpha channel: whatever the
page leaves transparent (a `background: transparent` html and body) is
alpha 0 rather than white, for artwork laid over other artwork.

`render --jpeg` writes the screenshot as a JPEG at `--quality` (default 90);
it needs `--size` and refuses `--transparent`.

`render --blank-margin PX` fails when ink sits within PX CSS px of a page
edge, where a printer cannot print: on every page of the PDF, or on the PNG
when there is no PDF.

`render` fails a local page that loads a `file://` URL, which Chrome blocks
on a served page; reference the file by a relative or absolute path.

`render` skips a local page whose outputs are still those its last
successful run with the same options made, and prints `unchanged: <output>`.
plate records every file the page loaded and every output, each with its
sha256, in `$XDG_CACHE_HOME/plate/render/` (`~/.cache/plate/render/` when
that is unset); a new plate build, `SOURCE_DATE_EPOCH` or
`PLATE_ENGINE_CHROME_HEADLESS_SHELL` also misses. A remote page, or one that
loads anything from another host, always renders. System fonts, the clock,
the locale, the installed ImageMagick and poppler versions and a different
Chrome at the same override path are not tracked: after changing one, pass
`--force`.

`build` reads a manifest, one target per line, `#` starting a comment:

```
# name  page        WxH       scale  outputs  [option...]
flyer   flyer.html  792x1120  2      pdf,png  blank-margin=23
```

The page is relative to the manifest and may carry a `?query`; each output
(`pdf`, `png`, `jpeg`, `html`) goes to `--out`, by default `dist` beside the
manifest, as `name.pdf`, `name.png`, `name.jpg` or `name.html`. The options
are `blank-margin=PX`, `transparent`, `budget=MS`, `quality=Q` and `2up`,
which imposes the target's PDF onto `name-2up-a4.pdf` as `impose --force`
does (the PDF must be an A5 front and back). Naming targets renders only
those. It logs `rendered NAME`, `rendered NAME (not cached: REASON)`,
`unchanged NAME` or `failed NAME: ERROR` for each target in manifest order,
with `imposed NAME-2up-a4.pdf` after a target whose sheet was written, then
a count. Targets render in parallel, so a target
that loads another target's output fails. A failed target exits 1, and so does an
unreadable manifest; an invalid manifest line or an unknown target name
exits 2 before anything renders.

A PDF from `render --pdf` or `doc` is dated `SOURCE_DATE_EPOCH`, or
1970-01-01 when that is unset, so an unchanged page renders to identical
bytes and a checksum says whether it changed.

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
was fetched, so run `slides` within that window. Lines, tables, videos, charts and word art are not drawn, and
it says which slides had them; bullets, autofit and a crop's rotation are not
drawn either.

`impose` takes exactly two pages, the front then the back, each A5 to
within 1 mm, both portrait or both landscape; anything else is refused with
the page count and size it found. Each copy keeps the source's vectors,
text, fonts and images, unscaled; a landscape flyer is turned a quarter to
fit its half. A `press` master's output intent is kept; links and tags are
dropped. It refuses to replace an existing
output unless given `--force`.

`shrink` scales the image down to fit `--width`, `--height` or both, or with
`--fit` so its longest side is that size. Each output's extension picks its
format; `o.png o.webp` writes both, so you can keep the smaller. WebP is
lossy at `--quality` (default 90) with lossless alpha.

`clip render` writes `<name>.<ext>` into `--out` for each format the script
lists (`mp4`, `webm`, `poster.png` for `poster`, `webp`, `gif`), plus
`<name>.json`: the clip's size, frame rate, frame count and duration, the
span of the recording it shows, each file's size, and its sharpness and
loop-seam scores. It prints `wrote <path> (<bytes> bytes)` for each file.
The MP4 is muted, constant frame rate and faststart, so a web view or Electron
autoplays it inline, and it draws a colour as the page beside it does in Safari.
The WebM is AV1, which Safari plays only on hardware with an AV1 decoder,
so a page lists the MP4 as its fallback source.
The WebP holds every frame in memory at 16 bits per channel while it
encodes, about 20 MB per 1080×2348 frame, so a 10-second clip at 60 fps
needs over 12 GB; list it only for short or small clips.
`--caption-css` loads after plate's caption style.

`clip render` refuses before decoding anything when the event log's sync
error exceeds one frame, when the recording's screen size differs from the
log's, or when the recording ends before the log's first action; it warns
when the recording ends before the last action. A recording that stops
early because its screen stood still is fine: its last frame is held to the
log's end. It also refuses a camera that would upsample the recording.
After encoding it fails when the
MP4 is over the script's `budget_kb`, when the encode loses too much of the
frames' fine detail (sharpness, measured per colour channel, so smeared
coloured text counts), or when the loop's last frame visibly differs from
its first. Each failure names the measured value, and leaves no output: an
earlier run's files for that name are removed first, and the new ones are
moved in only once every check has passed.

`render` runs the page outside Chrome's sandbox with open network access. A
local page, and any script it runs, including one loaded from the network,
can read any file you can by absolute path through plate's proxy; a remote
page cannot. The proxy listens on loopback for the render's duration, so
another account on the machine could reach it too. Give it only pages you
trust.

⚠ These commands create pixels the source never had. A project that forbids
this puts `synthesis = "forbid"` in a `plate.toml`, and they refuse to run.

## Install

```bash
go install github.com/lockyc/plate@latest
plate doctor --install
```

Run `plate doctor --install` first. It downloads the engine plate manages
itself, chrome-headless-shell for `render`, then checks every other engine
against its pin (for ffmpeg, the encoders `clip` needs too) and prints the install command for any that is missing or too
old, naming the commands that will not run until it is fixed.

`uv` is the only Python tool to install. The ML commands (`cutout`, `inpaint`,
`upscale`, `grade fit`) run through it, and the first run of each downloads
its Python dependencies and, for the models, their weights: several GB on
Linux, where PyTorch ships with CUDA. These land in uv's, Hugging Face's,
rembg's and PyTorch's own caches in your home directory, not in plate's.

ViTMatte (`cutout`) and DAT (`upscale`) run on the GPU when PyTorch finds one;
BiRefNet (`cutout --coarse birefnet`) runs on the CPU, and so does LaMa
(`inpaint`) on a Mac. `PLATE_DEVICE=cpu` (or `mps`, `cuda`)
chooses the PyTorch device instead.

### Platforms

- **macOS 14 or later on Apple Silicon**: every command; `clip` needs ffmpeg at
  its pinned minimum.
- **Linux with glibc 2.28 or later, x86_64 or arm64**: every command except
  `cutout --coarse vision`, which needs Apple Vision (`clip` runs wherever
  ffmpeg at its pinned minimum is present); `cutout` uses
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
needs every engine and model installed.

## Development

```bash
just build    # ./plate
just test
just gate     # gofmt check + vet + tests
just quality  # the quality cases over quality/
just lock-ml  # re-resolve the ML scripts' lockfiles after editing a PEP 723 header
just install  # go install, then print the version
```
