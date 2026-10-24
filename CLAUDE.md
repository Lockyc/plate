---
type: architecture
links:
  - rel: see-also
    to: README.md
    note: the command set and install, for a human
  - rel: see-also
    to: docs/design.md
    note: the architecture every command is built to
  - rel: see-also
    to: docs/roadmap.md
    note: start here for what's next
---

# plate — notes for the next agent

A Go CLI that makes or inspects a visual artefact from the shell: rasters,
rendered pages, PDFs, for print and web pipelines. Generic operations only;
a consuming project's look or values come in as flags.
The command set is in [README.md](README.md), the architecture in
[docs/design.md](docs/design.md), and **what is built and what comes next in
[docs/roadmap.md](docs/roadmap.md)**.

## When plate falls short, improve plate

This is the rule the repo exists for. If an operation does not give a result
good enough for the job, the fix goes into plate: a new engine, flag or
preset. Work it in this order:

1. Add a `quality/` case that reproduces the shortfall, with its metric.
2. Try the candidate approaches and measure each one on that case.
3. Land the winner. The commit body records every candidate and its number.

Thresholds in `quality/cases.toml` only ever get stricter. Declaring a result
impossible after one attempt, or hand-patching a consuming project's recipe
around plate, is the failure this rule exists to stop.

## Conventions

- **Generic only.** A crop box, path, colour or threshold tuned to one image
  belongs in the consuming project's recipe, passed as a flag.
- **One engine runner, one pin table.** Every external call goes through
  `internal/engine`, every provider call through `internal/genai`, and every
  version and model ID lives in `internal/pins`. Never call `exec.Command`
  or a provider's API from an op package, and never restate a version or
  model ID in a doc or error string.
- **Python only for ML**, as embedded PEP 723 scripts with `exclude-newer`
  and a committed lockfile run `--locked`, model revisions pinned in
  `internal/pins` (or, where a library fetches the weights itself, by that
  library's locked version); upstream CLIs (LaMa) through `uv tool run`.
- **Portable.** No GNU-only or BSD-only flags in anything plate runs.
- **Mark synthesis.** A command that creates pixels the source never had
  sets `synthesises` in main.go's command table, which applies the
  `plate.toml` policy (docs/design.md → Synthesis policy).
- **Public repo, self-contained tree.** No machine paths, no private
  hostnames, no consuming project's assets. `quality/` holds only openly
  licensed or synthetic images, each with its source and licence recorded.
- Working notes go in the gitignored `.superpowers/` and `docs/superpowers/`.
  Tracked docs never reference them.

## Branch and release

Main-only. Semver, with `VERSION` as the single source, embedded in the
binary. A release is a `vX.Y.Z` tag plus a GitHub release with notes, and
the README is curated at each release.

## Commands

```bash
just build | just test | just gate | just quality | just lock-ml | just install
```
