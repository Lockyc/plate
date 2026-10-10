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
                     grade, render, press, pdf, doc, slides, shrink, diff,
                     fonts, qr, doctor)
internal/cli/        flag, exit-code and error conventions every command shares
internal/engine/     the one way an external tool is run
internal/atomicfile/ the one way plate writes a file in process: whole or not at all
internal/frame/      the source pre-pass the ML ops read: oriented, sRGB, 8-bit
                     RGB(A) PNG; or oriented only, for infill
internal/icc/        the embedded sRGB profile
internal/imgsize/    image dimensions without decoding pixels
internal/enginetest/ sh stubs that stand in for engines in tests
internal/pins/       the one table of engine versions
internal/pdf/        PDF page count, page boxes and text through poppler; the
                     pdf command
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

## Synthesis policy

`inpaint`, `infill` and `upscale` create pixels the camera or artist never
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
- **render** — refuses a PNG beyond the largest size verified whole on the
  pinned engine (`render.maxSide`), since Chrome has cut large screenshots
  short with exit 0; checks the PNG is exactly size × scale. It warns when a
  PDF page size is not a multiple of 8 CSS px, which Chrome's PDF backend
  rounds to, and fails when the PDF's page box does not match `--size` (no
  matching `@page` prints Letter). It deletes stale outputs first, stamps the
  PNG's pHYs at 96 × scale DPI, and fails if the page text contains a
  `--fail-if` string: dumped DOM for PNG and HTML, `pdftotext` for PDF.
  `--html` judges Chrome's output by content, not exit status: a dump ending
  in `</html>` is whole, since Chrome's teardown watchdog can exit non-zero
  after writing the full page (`engine.Cmd.AllowExit`).
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
- **inpaint, fonts, qr, pdf, doc, slides** — as the README table and each package's
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
models and runs locally, not in CI.
