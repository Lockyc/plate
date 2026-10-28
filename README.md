# plate

[![Release](https://img.shields.io/github/v/release/lockyc/plate?sort=semver&label=release)](https://github.com/lockyc/plate/releases/latest)
![Platform](https://img.shields.io/badge/platform-macOS%20%7C%20Linux-555)
![Go](https://img.shields.io/badge/go-1.27%2B-00ADD8?logo=go&logoColor=white)
[![License](https://img.shields.io/github/license/lockyc/plate)](LICENSE)

Image, render and document operations for print and web pipelines, as one
command: lift a subject out of a photo, fill, enlarge or shrink artwork, make a macOS app icon, render HTML to
PNG, JPEG, PDF or its settled DOM, cut a screen recording into a short looping demo clip, animate a page into a short video, preview a Google Slides deck, turn markdown into a
PDF, look inside a PDF, make a press-ready PDF, put an A5 flyer two-up on
an A4 sheet to print and cut, match a colour grade, draw new poses of a
character from reference images with a hosted image model. Each
operation wraps an engine or model at a pinned version, and its output is checked
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
| `marks` | A print shop's copy of a PDF: each page unchanged at its trim size, with bleed (default 3 mm) and crop marks, TrimBox and BleedBox set | `plate marks [--bleed MM] [--force] -o out.pdf <in.pdf>` |
| `doc` | Renders markdown or HTML to a PDF with a plain document look: A4, a running head from the first heading, page numbers, ruled tables | `plate doc [--css FILE] <in.md\|in.html> <out.pdf>` |
| `slides` | Previews a Google Slides deck that Drive will not export, from its Slides API JSON: one PNG per slide, in the fonts the deck names | `plate slides [--pages LIST] [--out DIR] [--scale S] <deck.json\|->` |
| `shrink` | Resizes down to a small, sharp web asset: PNG at maximum lossless compression, or WebP, alpha kept, never enlarging | `plate shrink (--width PX \| --height PX \| --width PX --height PX \| --fit PX) [--quality Q] <in> <out.png\|out.webp>...` |
| `icon` | Makes a macOS app icon (`.icns`) and PNGs from a tile drawn edge to edge, placed in Apple's icon grid and rendered natively at every size | `plate icon [--icns out.icns] [--png PX:out.png]... <tile.svg\|tile.png>` |
| `diff` | Compares two images after lining them up, so a small shift doesn't fail everything | `plate diff [--tolerance N] [--max-shift PX] [--step PX] [--threshold PCT] [--out diff.png] <a> <b>` |
| `clip check` | Validates a demo-clip script and prints it normalised, the form a runner reads | `plate clip check [--json] <clip.toml>` |
| `clip render` | Turns a screen recording and its event log into a short looping clip: trimmed, framed by a fixed or following camera, with captions and touch, click, key or cursor overlays; MP4, WebM, poster, WebP or GIF, with a JSON of its measurements | `plate clip render --script clip.toml --master raw.mov --events events.jsonl --out DIR [--caption-css FILE]` |
| `animate` | Captures a page's own animation frame by frame as a muted MP4, WebM, poster, WebP or GIF, with a JSON of its measurements, skipped when nothing changed | `plate animate --size WxH [--scale F] --duration S [--fps N] [--format mp4,webm,poster,webp,gif] [--loop none\|crossfade\|dip] [--budget-kb N] [--budget MS] --out DIR [--name N] [--fail-if TEXT]... [--force] <page.html\|http(s) or file URL>` |
| `fonts` | Embeds web fonts, from files or from Google Fonts by family name, into a CSS file so a page renders from the filesystem, optionally cut to the characters a language uses | `plate fonts [--display block] [--subset latin] [--google FAMILY:WEIGHT:STYLE]... -o fonts.css [FILE:FAMILY:WEIGHT:STYLE]...` |
| `qr` | Makes a QR code SVG that any phone camera decodes | `plate qr [--ec L\|M\|Q\|H] [--fg RRGGBB] [--bg RRGGBB] -o out.svg <text>` |
| `generate` ⚠ | Makes a round of attempts at a character pose from reference images through a hosted image model, each with a provenance record, and redraws the pose's contact sheet | `plate generate --pose DIR --prompt TEXT --ref IMG [--ref IMG]... [--provider P [--model ID]] [--n 4] [--size TIER] [--aspect W:H]` or `plate generate --list-models [--provider P]` |
| `generate accept` | Copies the attempt you picked out of a pose folder, with its provenance beside it | `plate generate accept --pose DIR <rNN-aMM> <out.png>` |
| `doctor` | Checks every engine against its pin, installs the ones plate manages, prints the install command for the rest, and says which providers' API keys are set | `plate doctor [--install]` |
| `version` | Prints plate's version | `plate version` |

`plate <command> -h` lists each flag with its default; for `pdf`,
`grade` and `generate accept`, `plate <command> <verb> -h` does.

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
are `blank-margin=PX`, `transparent`, `budget=MS`, `quality=Q`, `2up`,
which imposes the target's PDF onto `name-2up-a4.pdf` as `impose --force`
does (the PDF must be an A5 front and back), and `marks`, which writes it to
`name-marks.pdf` as `marks --force` does. A line with `dur=S` (seconds, e.g. `8s` or `2.5`)
is captured as `animate` does: its outputs are `mp4`, `webm`, `gif`, `webp`
and `poster` (`name.poster.png`), with `png` and `jpeg` the last frame and
`name.json` beside them, and `fps=N`, `loop=none|crossfade|dip` and
`budget-kb=N` tune it. A video output needs `dur=`, and `fps=`, `loop=` and
`budget-kb=` need it too; `pdf`, `html`, `transparent`, `blank-margin`, `2up`
and `marks` are refused on a video line. Naming targets renders only
those. It logs `rendered NAME`, `rendered NAME (not cached: REASON)`,
`unchanged NAME` or `failed NAME: ERROR` for each target in manifest order,
with `imposed NAME-2up-a4.pdf` and `marked NAME-marks.pdf` after a target
whose sheet or copy was written, then a count. Targets render in parallel, so a target
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

`marks` sets each page at the ISO A size it is within 1 mm of (so A4 as
Chrome prints it, 594 x 840 pt, becomes exactly 210 x 297 mm), or at its own
size, in the middle of a sheet with the bleed round it and hairline crop
marks in registration colour starting at the bleed's edge. The bleed holds
nothing past the page's edge, which is right for a page with a white
margin; a page inked to its edge needs its artwork extended first. Like
`impose`, it keeps the source's vectors, text, fonts and images, and
refuses to replace an existing output unless given `--force`.

`shrink` scales the image down to fit `--width`, `--height` or both, or with
`--fit` so its longest side is that size. Each output's extension picks its
format; `o.png o.webp` writes both, so you can keep the smaller. WebP is
lossy at `--quality` (default 90) with lossless alpha.

`icon` takes the tile as it should look, rounded corners included, filling
its square, and draws it in the 824 px box centred on a 1024 px canvas that
every macOS app icon uses; an icon drawn to the canvas edge shows larger than
every other app in the Dock. The `.icns` holds the ten entries macOS reads,
16 to 1024 px at 1x and 2x, and each `--png` is the same icon at PX px (1 to
1024). Name `--icns`, `--png` or both.

`clip render` writes `<name>.<ext>` into `--out` for each format the script
lists (`mp4`, `webm`, `poster.png` for `poster`, `webp`, `gif`), plus
`<name>.json`: the clip's size, frame rate, frame count and duration, the
span of the recording it shows and how much idle time was cut from it, each
file's size, its sharpness and loop-seam scores, and how many frames at each
end closing the loop changed.
It prints `wrote <path> (<bytes> bytes)` for each file.
Any still stretch between steps longer than the script's `[output]
max_idle_ms` (default 500, 100 to 60000) is cut to that length from its
middle, so the time a driver spends between steps never shows; captions,
touches and the camera stay with the picture. A blinking caret counts as
still. An action, its `hold_ms` and its touch, click or key overlay, a
caption's fade, a `pause` step, and a following camera's pan and lead
before the action it moves to are never cut.
A clip whose last frame visibly differs from its first has its loop closed
over its final and opening frames. The script's `[output] loop` picks how:
`crossfade` (the default) blends the end into the start, and `dip` eases
both ends to the opening frame's dominant colour and back, so no frame
shows the end and the start at once. Use `dip` when the clip ends in a
different state from the one it starts in, such as a row added. The poster
is the first frame the loop leaves unchanged.
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
MP4 is over the script's `budget_kb`, when the MP4 or WebM loses too much of
the frames' fine detail (sharpness, measured per colour channel, so smeared
coloured text counts), or when the loop's last frame visibly differs from
its first. Each failure names the measured value. A refused log or
recording leaves an earlier run's files in place; any later failure leaves
none for that name, and new files are moved in only once every check has
passed.

`animate` writes `<name>.<ext>` into `--out` for each `--format` (default
`mp4`), named for the page unless `--name` says otherwise, plus
`<name>.json` in `clip render`'s shape without the trim: size, frame rate,
frame count and duration, each file's size, its sharpness and loop-seam
scores, and how many frames at each end closing the loop changed. It prints
`wrote <path> (<bytes> bytes)` for each file, or `unchanged <name>` when
nothing the page loaded and no output has changed since its last success
(`--force` captures anyway). The page's animation runs on plate's clock:
CSS, Web Animations, requestAnimationFrame, timers, SVG and video,
frame-exact. Frame n is the page at n/`--fps` s (default 30, up to 120),
for `--duration` × `--fps` frames, at `--size` × `--scale` px. The last
frame, the poster, is the page at `--duration` − 1/`--fps` s, so an ending
meant for the poster holds for at least a frame. `--budget` (default 5000
ms) is how long the page may take to settle before the first frame.
`--loop` defaults to `none`; `crossfade` and `dip` close the loop as `clip
render`'s do. Encodes, sharpness and `--budget-kb` are as `clip render`'s.

`animate` refuses before Chrome starts an MP4 or WebM with an odd pixel
side, an fps outside 1 to 120, or a duration under two frames. It refuses
a page that does not settle within `--budget` (naming what is pending), a
page that does not open within `--budget` plus 5 s, a page that throws, a
video that cannot play, a cross-origin iframe, a frame that does not
finish within `--budget` plus 5 s, a page whose every frame is one colour,
a page where no frame differs from the first, and a last frame whose
markup contains a `--fail-if` string. A refusal before Chrome starts
leaves an earlier run's files in place; a capture that fails leaves none
of its files for that name.

`render` and `animate` run the page outside Chrome's sandbox with open
network access. A local page, and any script it runs, including one loaded
from the network, can read any file you can by absolute path through
plate's proxy; a remote page cannot. Give them only pages you trust.

⚠ `inpaint`, `infill`, `upscale` and `generate` create pixels the source never had. A
project that forbids this puts `synthesis = "forbid"` in a `plate.toml`, and
they refuse to run.

### Generating a pose

`generate` is the one command that needs an API key. Each provider reads
its key from an environment variable; set it only for the providers you
use. A round without one stops before anything is sent and names the
variable, `plate doctor` names each provider's variable and whether it is
set (never the value), and no key is ever a flag or written to a file.
Without `--provider` a run uses the default model, marked in
`--list-models`; with `--provider`, `--model` defaults to that provider's
first model, and `--model` always needs `--provider`.

The loop, for one pose in a gitignored working folder of your own repo:

1. **Run a round.** `plate generate --pose work/pointing --ref hero.png
   --prompt "…"` sends `--n` attempts (default 4) and writes
   `rounds/NN/aMM.png`, each with `aMM.json` beside it: provider, model,
   prompt, size asked for and pixels made, the sha256 of each reference,
   cost and date. It prints each path, the pose's total spend, and
   `sheet.html` and `sheet.png`.
2. **Look at the sheet.** It shows the references, then every attempt of
   every round, newest round first, each labelled `rNN aMM` with its pixels
   and cost; `sheet.png` holds the newest rounds that fit one picture, and
   says how many older ones only `sheet.html` shows. The next round
   rewords the prompt, adds or drops a reference, tries another model, or
   passes a near-miss attempt as a `--ref`; that attempt's record is then
   carried inside the new one's.
3. **Accept one.** `plate generate accept --pose work/pointing r03-a02
   assets/pointing.png` writes the image and `assets/pointing.provenance.json`,
   the record with the full chain of attempts it came from. It never
   overwrites either file.
4. **Commit the accepted image and its provenance** in your own repo, and
   run your pipeline (`cutout`, `upscale`, `render`) from that image like any
   supplied source. The record says where the image came from, not how to
   make it again: models retire, so the image is what you keep.

Never in a build: no `plate build` line, `render` or `animate` can reach a
provider, so re-rendering never spends money or changes the art.

Spend is shown, not capped. Each round prints `pose total $N`, including
failed attempts the provider charged for (recorded in the round's
`failures.json`), and warns on stderr once a pose passes the soft limit
(`softLimit` in internal/generate). An attempt the provider charged, or may
have, without saying how much is recorded at $0.00, and the round prints
`unreported N`. A round in which some
attempts failed exits 1 with the reasons. A run cut short leaves
`rounds/NN.partial`, which keeps any images it paid for and is never read
as part of the pose.

## Install

```bash
go install github.com/lockyc/plate@latest
plate doctor --install
```

Run `plate doctor --install` first. It downloads the engine plate manages
itself, chrome-headless-shell for every command that renders a page
(`render`, `build`, `doc`, `slides`, `clip`, `animate`, `icon`), then checks every
other engine against its pin (for ffmpeg, the encoders `clip` and `animate`
need too) and prints the install command for any that is missing or too
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

- **macOS 14 or later on Apple Silicon**: every command; `clip` and
  `animate` need ffmpeg at its pinned minimum.
- **Linux with glibc 2.28 or later, x86_64 or arm64**: every command except
  `cutout --coarse vision`, which needs Apple Vision (`clip` and `animate` run
  wherever ffmpeg at its pinned minimum is present); `cutout` uses
  `--coarse birefnet` there. On arm64, Chrome for Testing publishes no
  chrome-headless-shell build, so the commands that render a page need one supplied
  through `PLATE_ENGINE_CHROME_HEADLESS_SHELL`. chrome-headless-shell needs
  the usual Chrome shared libraries (libnss3 and the like) from the
  distribution.
- **Intel Macs**: `cutout` and `upscale` refuse to run, because PyTorch and
  ONNX Runtime publish no build for them; `grade fit` needs macOS 14 or later;
  `inpaint` is untested. The other commands work.

## Quality

`just quality` runs the quality cases over the openly licensed and synthetic
images in `quality/` and checks each result against a measured threshold. It
needs every engine and model installed.

`just bakeoff` runs every OpenRouter model over a set of poses of an openly
licensed character and writes each pose's contact sheet, for a person to
compare. It spends real money, needs the OpenRouter key, and has no
threshold.

## Development

```bash
just build    # ./plate
just test
just gate     # gofmt check + vet + tests
just quality  # the quality cases over quality/
just bakeoff  # every OpenRouter model over the bake-off poses (paid)
just lock-ml  # re-resolve the ML scripts' lockfiles after editing a PEP 723 header
just install  # go install, then print the version
```
