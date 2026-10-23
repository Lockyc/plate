---
type: roadmap
links:
  - rel: see-also
    to: docs/design.md
    note: the architecture each phase builds toward
---

# plate — roadmap

Phases in build order. Each ships when its acceptance gate passes.

| Phase | Lane | Scope | Gate |
|---|---|---|---|
| 0 | Shipped | Repo, CLI stub, `version` | `just gate` |
| 1 | Shipped | `internal/engine`, `internal/pins`, `doctor`, the `plate.toml` synthesis policy, the `quality/` harness | stub-engine tests; `doctor` reports this machine accurately |
| 2 | Shipped | Deterministic ops: `render`, `press`, `diff`, `fonts`, `qr`, `grade apply` | unit tests and quality cases pass |
| 3 | Shipped | ML ops: `cutout`, `grade fit`, `inpaint`, `upscale`, `infill` | quality cases pass at their recorded thresholds |
| 4 | Shipped | `v0.1.0` release | release gate green; README curated |
| 5 | Not started | Regression check against the pipelines plate replaces, then each project's cutover | no measured regression; each cutover approved by its owner |
| 6 | Shipped | Documents and PDFs: a PDF inspect command (metadata with a text-layer check, text, page renders, embedded images) and markdown to PDF with a neutral default document stylesheet, pandoc pinned | unit tests with stub engines; a rendered document checked page by page |
| 7 | Shipped | Demo clips: `clip check` (the clip-script schema a runner reads) and `clip render` (trim, idle compression, camera, overlays, a crossfaded or dipped loop, MP4/WebM/poster/WebP/GIF, measured checks), ffmpeg pinned | unit tests; `clip-*` quality cases at their recorded thresholds |
| 8 | Next | Motion: `animate` (a page's animation captured on plate's clock, embedded video seeked with it), `internal/cdp`, clip's encode and checks moved to `internal/video`, video outputs and `dur=`/`fps=` in `plate build` | unit tests; `animate-*` quality cases: two captures frame-identical, thin moving text at clip's sharpness floor, embedded video frame-matched to its source |

**Phase 5 changes nothing in a consuming project until its owner approves
that project's cutover.** The check runs plate beside the existing pipeline,
reading the project's assets without writing to its tree.

## Sequencing

Phases run in order, except where one needs nothing from the phase before
it: phases 6, 7 and 8 add commands and cut no project over, so they run
independently of phase 5. Within a phase, order is open.

## Deferred

- **animate has no sound.** Every video plate writes is muted. Deferred
  because the first ads are captioned and silent. Unlock: an optional
  `--audio FILE` (and an `audio=` build option) muxed under the frames,
  trimmed and faded to the duration, AAC in the MP4 and Opus in the WebM.
- **animate's clock does not reach workers.** A Web Worker, and an
  OffscreenCanvas drawn from one, keeps real time. Deferred because no scene
  needs one. Unlock: such a scene, then the clock injected into the worker.

- **doctor runs no managed engine.** `doctor` reports chrome-headless-shell
  ok once its pinned build is unpacked, without starting it, so a Linux host
  missing Chrome's shared libraries (libnss3 and the like) passes `doctor`
  and fails at the first `render`. Deferred because the hosts plate is
  measured on have them. Unlock: `doctor` smoke-runs each managed engine
  with `--version` and reports the loader's error.

- **render --png captures the viewport, not the whole page.** The PNG is
  exactly `--size`; content below it is cut off. Full-page capture needs the
  DevTools protocol (`Page.captureScreenshot` with `captureBeyondViewport`),
  which a command-line Chrome call cannot reach. Deferred because no caller
  has needed a long page. Unlock: such a caller, then a CDP driver behind a
  `--full-page` flag.

- **fonts --subset embeds TTF or OTF, not woff2.** hb-subset writes only
  the input's sfnt format, so a subset face is about twice the size of the
  same face as woff2 (Open Sans latin: 33 KB against 16 KB). The woff2
  encoder (`woff2_compress`) prints no version, so it cannot be pinned like
  the other engines. Unlock: a pinnable woff2 encoder, or one built into
  plate, measured on the subset.

- **cutout on semi-transparent subjects.** Smoke, glass or a dandelion
  clock may be refused as "no foreground found": their mask is soft across
  the whole subject, not just at its edge, and the empty-frame check
  (`maxBand` in internal/cutout) reads that as a haze. Deferred because no
  measured subject is like that. Unlock: a `quality/` case with such a
  subject, then a `--max-band` flag measured on it.
- **infill edge stops across holes in one cluster.** Holes whose crops
  overlap (within 3× the coarsest `--levels` radius of each other) share one
  edge-stop flood, so two holes on opposite sides of a strong edge each
  reach, and keep, the other's ground, and both bleed as before. Deferred
  because no measured mask has holes like that: the cases have one hole,
  and a per-hole flood costs a crop per hole. Unlock: a `quality/` case with
  two holes either side of an edge, then a flood per hole (one hole's
  pieces cutting only its own levels) measured on it.
- **infill edge stops at the finer levels.** A ground's pull on a hole is
  weighed at the coarsest `--levels` radius only, so a small dark object at
  a hole's border can be kept while the finer levels, which fill the hole's
  rim, smudge it in. Deferred because no measured case shows it. Unlock:
  a `quality/` case with a small dark object within about 2× the finest
  radius of a hole's border, then a per-level pull measured on it.
- **cutout alpha in the sure regions.** On every tile with unknown
  pixels, the full-resolution pass keeps ViTMatte's alpha across the whole
  tile, including where its trimap says sure foreground or background,
  rather than pinning those pixels to opaque and clear. Deferred because no
  measured case shows ViTMatte straying there. Unlock: a `quality/` case
  where it does, then alpha pinned to the trimap outside the unknown band,
  measured on it.
- **infill edge stops by luma alone.** The edge test compares luma, so a
  ground of the same lightness but a different hue reads as one ground and
  is not cut off; and a strip narrower than about twice the smoothing
  radius is never cut off at all. Deferred because no measured case has
  either. Unlock: a `quality/` case with a same-luma, different-hue ground
  beside a hole, or a thin strip of a different ground, then an edge test
  that sees hue or a finer smoothing, measured on it.
- **upscale colour bleed at alpha edges.** DAT enlarges the colour under
  transparent pixels as if it were picture, so a cut-out whose transparent
  pixels hold junk colour bleeds it into the enlarged soft edge. Deferred
  because no measured case has such a source. Unlock: a `quality/` case
  with a cut-out over junk RGB, then the colour extended under the
  transparency before enlarging, measured on it.
- **press soft-proof default unproven.** `defaultMaxRMSE` sits above
  the 0.083 its docblock gives for a master with a hidden element, and no
  `quality/` case presses a broken master, so the default gate is not shown
  to fail. Deferred because the artboards it was calibrated on are not in
  the tree. Unlock: a `quality/` case pressing a master with an element
  removed, measured beside `press-clean-master`, then the default and the
  docblock set from those two numbers.
- **qr contrast floor.** `qr` refuses light-on-dark but accepts any darker
  fg, so `--fg 7F7F7F --bg 808080` writes a code no camera reads. Deferred
  because no case measures where decoding fails. Unlock: `qr-decodes` cases
  at falling contrast, then a minimum contrast ratio set below the last
  pass.
- **infill memory on scattered holes.** Holes whose crops overlap share
  one crop (`cluster` in internal/infill), so dust specks a few hundred px
  apart chain into a crop the size of the frame, and the edge-stop and
  texture passes allocate over all of it. Deferred because no measured
  case has many small holes on a large scan. Unlock: a `quality/` case of
  scattered specks on a large frame with its peak memory recorded, then
  per-component crops measured on it.
- **diff reads pixels as stored.** It decodes in Go (`raster.Load`),
  which ignores EXIF orientation, so a phone JPEG stored on its side
  compared with its upright export scores as almost wholly changed. Every
  other op reads sources upright. Deferred because its callers compare
  renders, which carry no orientation. Unlock: a caller diffing camera
  files, then the orientation applied in `raster.Load`, with a rotated
  fixture in diff's tests.
- **impose fits only A5 on A4.** Other two-up or n-up layouts (A6 four to
  an A4, DL three to an A4) and other sheets are refused. Deferred because
  the one caller prints A5 flyers. Unlock: a caller with another size, then
  the sheet and grid as flags and as build options beside `2up`, each
  layout with its own `impose-halves` quality case.
- **marks leaves the bleed empty.** A page inked to its edge gets a bleed
  of bare paper, so a cut a little outside the trim shows a white sliver.
  Deferred because its callers mark office prints, whose white margin is
  the bleed's true content. Unlock: a caller marking a page drawn to its
  edge, then a render at the trim plus the bleed (the page's own artwork
  past the edge) handed to marks in place of the trimmed PDF.
- **clip check accepts `ios` only.** `clip.Adapters` lists the platforms a
  runner adapter exists for; the iOS one lives in a separate tool, not
  plate. Unlock: a runner adapter for macos or android, then the platform
  added to `clip.Adapters`.
- **The desktop cursor travels on an eased path between action points.**
  The event log carries actions, not pointer moves, so the drawn path is
  not the real one. Unlock: an event-log version carrying pointer `move`
  events, read by `clip.ParseEvents` and drawn by `clip.Timeline`.
- **GIF goes through ffmpeg's palette pipeline.** Deferred because no case
  shows it losing to gifski. Unlock: a `quality/` case where it measurably
  does, then gifski pinned.
- **Animated WebP frame times round to whole ms at 60 fps.** Deferred
  because no caller needs exact WebP timing. Unlock: such a caller.
- **Animated WebP loads every frame at once.** `clip.Encode` hands the
  frames to ImageMagick, which holds them all at Q16, so memory grows with
  frame count times frame size (a 10-second full-width clip at 60 fps needs over 12 GB).
  Deferred because MP4 and WebM are the formats a page plays, and WebP is
  an opt-in extra. Unlock: a pinned ffmpeg carrying `libwebp_anim`, which
  streams the frames, or encoding in batches and joining them.
- **Sharpness misses a magenta chroma smear.** Magenta keeps its strokes
  through either smear `clip.MinSharpness` catches on the other colours
  (see `sharpSigma` in internal/clip/measure.go), so a chroma-smearing
  encode of magenta text passes. Deferred because the mp4 settings are
  measured end to end and no colour but magenta slips through. Unlock: a
  per-channel score that separates the magenta smear from a real encode,
  measured on the `clip-text-*` cases.
- **WebKit's video path still differs from the decode reference.** The
  residual after the sRGB tag is recorded at `bt709` in internal/clip/encode.go.
  Deferred because the quality harness measures Chromium only. Unlock: a
  WebKit-side measurement in the harness, then the encode tuned against it.
- **frame-code can miss an early frame.** The `frame-code` quality metric
  reads a frame's code from a pixel strip, and a frame before the strip is
  drawn reads as no code. Deferred because the cases that use it start
  after the strip is up. Unlock: a case that needs the first frame, then
  the metric reading the sidecar's start.
- **Repeated quality fixtures are repeated.** The `clip-*` cases in
  `quality/cases.toml` restate shared inputs. Deferred because only the
  clip cases repeat them. Unlock: a case-include feature in the quality
  runner once a second op needs it.
