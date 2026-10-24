---
type: architecture
links:
  - rel: see-also
    to: docs/roadmap.md
    note: the order the pieces below get built in
---

# plate — design

The architecture plate is built to; [roadmap.md](roadmap.md) tracks what is
built and what comes next.

## Shape

One Go binary, `go install`-able. Each subcommand is one generic operation;
nothing project-specific (crop boxes, paths, colours, thresholds tuned to one
image) lives here. A project keeps a short recipe that supplies those and
calls plate. A plain crop stays a `magick` call in the recipe, because
wrapping it would add nothing; a resize down for the web goes through
`shrink`, whose filter and encoders are measured.

```
main.go              dispatch
internal/<op>/       one package per command (cutout, inpaint, infill, upscale,
                     grade, render, build, press, pdf, impose, marks, doc,
                     slides, shrink, diff, clip, animate, fonts, qr, generate,
                     doctor)
internal/genai/      the one way a hosted image model is called
internal/cdp/        a DevTools-protocol session with Chrome over --remote-debugging-pipe
internal/video/      the encode, checks and whole-set publish clip and animate share
internal/sheet/      a PDF's pages redrawn as forms on new sheets (impose, marks)
internal/cli/        flag, exit-code and error conventions every command shares
internal/engine/     the one way an external tool is run
internal/atomicfile/ a file written whole or not at all: a temp file beside it, renamed
internal/outdir/     the --out directory a command writes several files into
internal/frame/      the source pre-pass the ML ops read: oriented, sRGB, 8-bit
                     RGB(A) PNG; or oriented only, for infill; and Probe, the
                     one read of a source's opacity, size and colourspace
internal/icc/        the embedded sRGB profile
internal/imgsize/    image dimensions without decoding pixels
internal/enginetest/ sh stubs that stand in for engines in tests
internal/pins/       the one table of engine versions and hosted image models
internal/pdf/        PDF page count, page boxes and text through poppler; the
                     pdf command; pinning a written PDF's dates
internal/policy/     the plate.toml synthesis policy
internal/raster/     in-process pixel work: load, measure, small PNG edits
internal/ml/         embedded single-file Python scripts (uv run --script)
internal/vision/     embedded Swift helper for Apple Vision (macOS)
quality/             the test images, their licences, and cases.toml
```

## Engines

`internal/engine` runs every external tool with the same rules:

- a timeout on every call;
- success means the output file exists and is non-empty, never the exit code
  alone, because Chrome exits 0 on failure and prints noise on success;
- a per-engine stderr policy: fatal for Ghostscript, where stderr is the only
  sign it dropped an image.

`internal/pins` is the only place an engine version is written down. `doctor`
reads it, and so does every "not installed" error, which prints the exact
install command.

`PLATE_ENGINE_<NAME>` points plate at a specific executable for an engine,
ahead of the data directory and `PATH`; tests use it to stand in sh stubs.

| Engine | How it is pinned |
|---|---|
| ImageMagick 7 | minimum version |
| chrome-headless-shell | exact version and sha256, installed by `doctor --install` under `$XDG_DATA_HOME/plate/` |
| Ghostscript, poppler, qpdf, qrencode, pandoc, hb-subset | minimum version |
| ffmpeg, ffprobe | minimum version; each encoder clip and animate need is a pins.Capability doctor checks |
| Apple Vision | the OS; macOS 14 or later, plus `swiftc` as a minimum-version engine |
| Python ML scripts (ViTMatte, BiRefNet, DAT, grade fit) | PEP 723 header with `exclude-newer`, and a committed `uv lock --script` lockfile run with `--locked`; ViTMatte and DAT weights pinned by Hugging Face commit in `internal/pins`, BiRefNet's by the pinned `rembg` version |
| LaMa | the `iopaint` CLI, run through `uv tool run` at a pinned version and `exclude-newer` (`pins.IOPaint`) |

Python runs only for the ML steps, where no Go, Rust or shell tool of
comparable quality exists. `uv` is the only Python tool a user installs.

Every torch model runs on the device `internal/ml/plate_device.py` picks,
which `$PLATE_DEVICE` overrides. The scripts import it; for LaMa, plate
runs it under iopaint's torch and passes the answer as iopaint's `--device`
(cpu, cuda or mps), and iopaint runs LaMa on the CPU when given mps.
BiRefNet runs on rembg's CPU build.

## Providers

`internal/genai` is the one way a hosted image model is called, as
`internal/engine` is for executables. Each provider is a `genai.Provider`
registered by name (`genai.For`); its calls go through the shared client in
internal/genai/http.go, which bounds each call (`callTimeout`), retries a
429 or 5xx (`backoff`, `Retry-After`), starts every error with the provider
and names the key variable on an auth refusal, and refuses a cross-host
redirect that would carry a key header. `PLATE_GENAI_<PROVIDER>_URL`
points a provider at an httptest server, as `PLATE_ENGINE_<NAME>` does for
an engine.

Every model a provider can call is a `pins.ImageModel` in
internal/pins/imagemodels.go: its ID, its key variable, its billing shape
(each attempt's cost and `genai.Image.CostBasis` come from it), the sizes,
references and request fields it takes. The first entry per provider is
that provider's default model; there is no default provider. A key is read
only from the environment (`pins.ImageModel.Key`) and is never part of an
error or a record.

Nothing on a build, render or animate path may import `internal/genai`
(`TestNoBuildPathReachesGenai` walks their imports), so a re-render never
reaches the network for pixels.

## Synthesis policy

`inpaint`, `infill`, `upscale` and `generate` create pixels the camera or artist never
made. plate looks for `plate.toml` in the working directory and each parent;
if the nearest one sets `synthesis = "forbid"`, those commands exit with an
error naming the file. Everything else only computes an alpha channel, moves
colours, or renders what it is given.

## Operations

- **cutout** — a coarse mask (`--coarse vision`, the default on macOS, or
  `birefnet`), refused as "no foreground found" when it is empty or its soft
  edge is wider than `maxBand` allows. Then ViTMatte twice: over a wide band
  in one whole-frame pass bounded by `contextSide`, which re-solves
  background the mask swallowed behind hair (`--band-in`, `--band-out`), then
  over a narrow band around that result at full resolution, in `--tile` px
  tiles, so memory stays flat at any size and shape; `--tile 0` runs the whole
  frame instead. Then the foreground colour is estimated so soft edges lose
  the background's tint. `--despill` takes a named preset (`warm-on-green`)
  that pulls a contaminating hue toward the local clean colour; each of its
  values has its own `--despill-*` flag, which overrides the preset.
  `--height` sets the working size, and it must never be smaller than the
  largest size the cut-out will be drawn at.
- **render** — a local page loads from `render.plateHost` through a
  loopback proxy (`render.server`) that reads each path once, recording the
  sha256 of the bytes it serves from then on, or that it was absent; other
  hosts are forwarded and recorded. Those loads are the cache's inputs:
  through `render.Cached`, a successful run writes a `render.record` under
  `$XDG_CACHE_HOME/plate/render/`, named by `render.cacheKey`, and the next
  run with that key skips Chrome while
  every input and output keeps its hash. A remote page or an external load is
  never cached, nor are `render.PDF` and `render.PNG`. Each output is written,
  checked and hashed under a temp name beside it, then renamed in. The checks:
  a PNG beyond `render.maxSide` is refused, since Chrome has cut large
  screenshots short with exit 0, and must be exactly size × scale, pHYs at
  96 × scale DPI; a PDF page size off a multiple of 8 CSS px warns, which
  Chrome's PDF backend rounds to, and a page box unlike `--size` fails (no
  matching `@page` prints Letter); a `--fail-if` string in the dumped DOM, or
  a PDF's `pdftotext`, fails; so does ink in the `--blank-margin` band
  (`render.checkMargin`), and a `file://` load Chrome refused, which it
  reports only on stderr (`render.blockedRe`). `--jpeg` converts the same screenshot. `--html`
  judges Chrome's output by content: a dump ending in `</html>` is whole,
  since Chrome's teardown watchdog can exit non-zero after writing the full
  page (`engine.Cmd.AllowExit`).
- **build** — parses a manifest into `render.Options`, each checked by
  `render.Validate` before anything renders, and runs `render.Run` on up to
  `--jobs` workers, logging in manifest order (`build.ParseManifest`). A
  target whose `render.Result.Inputs` holds another target's output fails
  (`build.loadsOutput`). A `2up` target's sheet is `impose.Run` on its
  PDF, and a `marks` target's copy `marks.Run`, each written when the PDF
  rendered or the file is missing or older (`build.derive`).
- **press** — Ghostscript pdfwrite with outlined text, CMYK conversion to a
  supplied ICC, fixed media from the input's page box, and images downsampled
  only above 1.5 × `--max-ppi`. It then checks the result has no fonts, no
  RGB, and unchanged page boxes, and compares a soft proof against the source
  by RMSE over the page and any `--region`.
- **grade** — `fit` registers the image to the reference (SIFT + RANSAC),
  fits an RGB→RGB thin-plate map on opaque, eroded pixels, and bakes a 16-bit
  HALD CLUT from sRGB frames. `apply` reads the source upright in sRGB too,
  keeping its depth, runs the CLUT through ImageMagick and restores alpha,
  which `-hald-clut` drops.
- **diff** — crops both images to their overlap, searches a vertical shift,
  and scores the share of pixels whose per-channel difference has a luma above
  the tolerance.
- **infill** — a blur pyramid over the known pixels, whose levels skip any
  other ground cut off from a hole by a strong edge (internal/infill/reach.go),
  then the ground's own grain transferred in patches from around each hole
  (internal/infill/texture.go).
- **upscale** — DAT ×4 over the normalised frame in overlapping tiles
  (internal/ml/scripts/upscale.py); alpha, which the model does not take, is
  enlarged separately.
- **clip** — ffmpeg only decodes and encodes (`clip.ExtractActivity`,
  `clip.ExtractFrames`, `video.Encode`); everything between is Go: the trim
  (`clip.TrimWindow`) and its idle cuts (`clip.IdleCuts`, applied to the
  frames by `clip.ApplyCuts` and to the log by `clip.CompressLog` before
  anything reads its times), the camera (`clip.NewLayout`, `clip.Path`, its
  keyboard rises and drops timed from the master by `clip.TimeKeyboard`), the
  overlay timeline (`clip.Timeline`) and the composite (`clip.Compose`,
  through `clip.Resample`), which also closes the loop (`video.CloseLoop`,
  one of `video.Loops`; a dip eases to the ground). Overlay sprites are
  HTML pages rendered once each through `render.PNGAlpha`. `clip.Render`
  refuses before decoding what the log and probe can show, holds the encode
  to its checks, and publishes the set only whole (`video.Publish`). The
  encode settings and the resampling kernel are measured end to end, 4:2:0
  included, by the `clip-text-*` and `clip-caption-sharp` quality cases;
  the render's own sharpness check scores the encoder alone, against a
  lossless 4:2:0 copy (`video.Lossless420`). `video.MaxSeam` is measured by
  `clip-loop-crossfade`, and the dip held by `clip-loop-dip`. The codec
  settings are documented at `mp4CRF`, the colour tagging at `bt709` (both
  internal/video/encode.go), the hold of a static tail at
  `clip.ExtractFrames`, and the target refusals at `validateRef`.
- **animate** — a page's own animation, captured frame by frame on a clock
  plate owns, and encoded as `clip` encodes. Generic: the scene, its
  styles and its copy are the consuming project's HTML, CSS and JS; plate
  ships no animation styles. Its flags are in the README's command table;
  `--fps` defaults to 30, `--loop` to `none`, since an ad has an end; the
  poster is the last frame. `--scale` is the device scale factor, as in
  `render`: the video is WxH × scale pixels. `--budget` is render's settle
  wait, spent once, before frame 0.
  - *The clock.* Before any page script runs
    (`Page.addScriptToEvaluateOnNewDocument`, every frame of the page),
    plate replaces `performance.now`, `Date`, `requestAnimationFrame`,
    `setTimeout` and `setInterval` with a clock that moves only when plate
    seeks it, and seeds `Math.random`. Each frame, plate seeks to `n/fps`:
    due timers fire, queued animation-frame callbacks run, every
    `document.getAnimations()` entry (CSS animations and transitions, the
    Web Animations API, pseudo-elements included) is paused at that time,
    SVG documents get `setCurrentTime`, and every `<video>` is paused and
    seeked to its own media time, the frame waiting on each `seeked`; then
    `Page.captureScreenshot`. The clock keeps each video's media time as a
    browser would: the shim takes over `play()`, `pause()`, `currentTime`,
    `loop`, `playbackRate` and `autoplay`, and their events, so a video the
    page starts at 4 s, from partway in, looping or hidden and shown by its
    own script, captures as it plays in a browser, and a page needs no
    plate-specific markup.
    Frame 0 waits for the load event, `document.fonts.ready` and no pending
    loads. This is the one route that is exact on macOS: Chrome refuses
    `HeadlessExperimental.beginFrame` there ("BeginFrameControl is not
    supported on MacOS yet", chrome-headless-shell 154), and its virtual
    time advanced a 33.3 ms budget by about 50 ms a frame, while the clock
    put a CSS animation and a requestAnimationFrame loop at their exact
    positions on every frame, at about 40 ms a frame for a 600×300 page.
  - *Footage.* A recording, or a `clip render` output, goes in the page as
    a muted `<video>`; the clock seeks it to the frame (page frame 42 of a
    30 fps H.264 source matched source frame 42, RMSE 0.0029 against 0.0075
    for its neighbours). Composing a promo from recordings is page layout,
    so `clip render` itself is unchanged.
  - *Transport.* `internal/cdp` speaks the protocol over
    `--remote-debugging-pipe` (fds 3 and 4, NUL-delimited JSON), standard
    library only. Chrome is started through `internal/engine` (`engine.Start`),
    a long-lived process with those pipes, killed with its context. A local
    page loads through render's proxy, so its loads are recorded and the
    render cache can skip an unchanged animation.
  - *Encode and checks.* Frames go to `internal/video`, the encode, the
    sharpness, budget and MP4 checks, the loop closing and the whole-set
    publish that `clip` and `animate` share.
    `animate` also refuses a capture whose frames are all one colour or
    all identical, which is a page whose animation never ran on plate's
    clock. Its JSON is clip's measurement shape.
  - *Build.* A `plate build` manifest line takes `mp4`, `webm`, `gif`,
    `webp` and `poster` outputs beside `png` and `jpeg`, with options
    `dur=S` (which makes it a video line), `fps=N`, `loop=` and
    `budget-kb=`, and render's `budget=`; `scale` means what it means for
    a still. On such a line `png` and `jpeg` are the last frame of the same
    capture, so an ad's still and its video always end alike, and
    `<name>.json` lands beside them. `pdf`, `html`, `transparent`,
    `blank-margin`, `2up` and `marks` are refused there: each reads a
    settled page, which an animation has only at its end.
- **generate** — an iteration tool, never a build step: a person picks
  among attempts, and the accepted image, not a re-run, is the source. A
  pose folder's layout is at `partialSuffix` (internal/generate/pose.go): a
  round is claimed as `NN.partial` before any request is sent (`claimRound`)
  and renamed once every file is in, and a partial round is never deleted
  or read. Each attempt's `generate.Record` names what made it; a reference
  that was itself an attempt embeds that attempt's record (`RefRecord`), so
  an accepted record carries its whole chain once the pose folder is gone.
  A failed attempt the provider charged for lands in `failures.json`
  (`RoundFailures`) and counts in the pose total, past `softLimit` of which
  each round warns. `WriteSheet` writes the contact sheet as HTML and
  renders it to PNG (`render.PNG`), so the agent sees what the person sees;
  `generate accept` writes the record before the image. Every file a person
  keeps goes through `internal/atomicfile`.
- **inpaint, fonts, qr, pdf, impose, marks, doc, slides** — as the README table and each package's
  doc comment describe.

## Platforms and errors

macOS is primary. The floor is the locked wheels': Apple Silicon on macOS 14
or later, and Linux with glibc 2.28 or later on x86_64 or arm64. On Linux,
everything works except `--coarse vision`, which exits with a message pointing
at `birefnet`; on arm64, Chrome for Testing publishes no
chrome-headless-shell, so `render` runs only with one supplied through
`PLATE_ENGINE_CHROME_HEADLESS_SHELL`. On an Intel Mac, `ml.RunScript` refuses
a script whose lockfile holds a package built for Apple Silicon only (PyTorch,
ONNX Runtime), so `cutout` and `upscale` refuse before uv runs; `grade fit`
needs macOS 14 or later there, and `inpaint`, whose iopaint resolves its own
older PyTorch there, is untested. No command depends on GNU-only flags. An
operation that cannot do the job fails with the measured reason ("no
foreground found", "render would be … px, beyond the … px verified
whole"), and never leaves a plausible-looking wrong file behind.

## Quality

`go test ./...` covers everything that needs no model or browser, standing
sh stubs in for engines through `PLATE_ENGINE_<NAME>`. `just quality` runs
the quality cases over `quality/`: openly licensed or synthetic test images,
each asset registered in `quality/assets.toml` with its source and licence,
each case in `quality/cases.toml` (fields: `quality.Case`) with its metric
(`quality.Metrics`) and threshold, which only ever tightens. It needs the
models and runs locally, outside `just gate`.
