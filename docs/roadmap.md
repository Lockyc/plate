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

**Phase 5 changes nothing in a consuming project until its owner approves
that project's cutover.** The check runs plate beside the existing pipeline,
reading the project's assets without writing to its tree.

## Sequencing

Phases run in order, except where one needs nothing from the phase before
it: phase 6 adds commands and cuts no project over, so it runs
independently of phase 5. Within a phase, order is open.

## Deferred

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
- **impose fits only A5 on A4.** Other two-up or n-up layouts (A6 four to
  an A4, DL three to an A4) and other sheets are refused. Deferred because
  the one caller prints A5 flyers. Unlock: a caller with another size, then
  the sheet and grid as flags and as build options beside `2up`, each
  layout with its own `impose-halves` quality case.
