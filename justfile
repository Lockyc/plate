# plate — task runner
#
# `default` is the house self-contained clip-to-width `just --list` (same in every repo,
# public or private). Canonical text + rationale: lockyc-config skill, references/just-pretty.md.

# `default` pipes `just --list` through a small stock-perl filter that clips long recipe
# docs to your terminal width (…) instead of wrapping. Self-contained — no external files;
# falls back to plain `just --list` where perl is absent. Edit the recipes below, not this.
# List available recipes
default:
    @if command -v perl >/dev/null 2>&1; then just --color always --list | perl -CS -Mutf8 -lpe 'BEGIN{($w)=`stty size 2>/dev/null </dev/tty`=~/ (\d+)/; $w||=100; $col=(-t STDOUT && !exists $ENV{NO_COLOR})} s/\e\[[0-9;]*m//g unless $col; (my $v=$_)=~s/\e\[[0-9;]*m//g; if(length($v)>$w){my($o,$n)=("",0); while(length && $n<$w-1){ if($col && s/^(\e\[[0-9;]*m)//){$o.=$1}else{s/^(.)//;$o.=$1;$n++} } $_=$o."…".($col?"\e[0m":"")}'; else just --list; fi

# Build the binary
[group("build")]
build:
    go build -o plate .

# Run the test suite
[group("check")]
test:
    go test ./...

# Run the quality cases over quality/ (needs every engine and model; local only)
[group("check")]
quality:
    go test -tags quality -count=1 -timeout 0 -v ./quality/

# Provider bake-off for `plate generate`: every openrouter model over quality/bakeoff/poses.txt; spends real money (about US$10-20) and needs OPENROUTER_API_KEY; not part of gate or quality
[group("check")]
bakeoff:
    #!/usr/bin/env bash
    set -uo pipefail
    if [ -z "${OPENROUTER_API_KEY:-}" ]; then
      echo "skipped every model: OPENROUTER_API_KEY is not set"
      echo "grand total \$0.00"
      exit 0
    fi
    tmp="$(mktemp -d)"
    trap 'rm -rf "$tmp"' EXIT
    bin="$tmp/plate"
    if ! go build -o "$bin" .; then
      echo "bakeoff: go build failed" >&2
      exit 1
    fi
    # The model IDs live in internal/pins; an unknown --model lists them. The
    # probe runs keyless, so it can never reach the network.
    models="$(env -u OPENROUTER_API_KEY "$bin" generate --provider openrouter --model '?' --pose "$tmp/probe" --prompt p --ref "$tmp/probe.png" </dev/null 2>&1 \
      | sed -n -E 's/.*; known: (.*)$/\1/p' | tr ',' '\n' | tr -d ' ')"
    if [ -z "$models" ]; then
      echo "bakeoff: could not list the openrouter models" >&2
      exit 1
    fi
    declare -a sheets=()
    total=0.00
    for model in $models; do
      n=0
      while IFS= read -r line || [ -n "$line" ]; do
        [ -n "$line" ] || continue
        n=$((n + 1))
        pose="quality/bakeoff/out/${model//\//_}/$n"
        out="$("$bin" generate --pose "$pose" --ref quality/assets/bunny-reference.png --n 4 --provider openrouter --model "$model" --prompt "$line" </dev/null)"
        code=$?
        echo "$out"
        cost="$(printf '%s\n' "$out" | sed -n -E 's/^pose total \$([0-9.]+)$/\1/p' | tail -n 1)"
        total="$(awk -v a="$total" -v b="${cost:-0}" 'BEGIN { printf "%.2f", a + b }')"
        if printf '%s\n' "$out" | grep -qxF "$pose/sheet.png"; then sheets+=("$pose/sheet.png"); fi
        if [ "$code" -ne 0 ]; then
          echo "failed (exit $code): $model pose $n: $line" >&2
        fi
      done < quality/bakeoff/poses.txt
    done
    if [ "${#sheets[@]}" -gt 0 ]; then printf '%s\n' "${sheets[@]}"; fi
    echo "grand total \$$total"

# go vet static checks
[group("check")]
vet:
    go vet ./...

# Format all Go files in place
[group("check")]
fmt:
    gofmt -w .

# Non-mutating pre-merge gate: gofmt check + vet + tests
[group("check")]
gate:
    #!/usr/bin/env bash
    set -euo pipefail
    unformatted="$(gofmt -l .)"
    if [ -n "$unformatted" ]; then
      echo "✗ gofmt: these files need formatting:" >&2
      echo "$unformatted" >&2
      exit 1
    fi
    go vet ./...
    go vet -tags quality ./quality/
    go test ./...
    echo "✓ gate passed"

# Install plate onto GOBIN and print its version
[group("build")]
install:
    #!/usr/bin/env bash
    set -euo pipefail
    go install .
    bin="$(go env GOBIN)"; [ -n "$bin" ] || bin="$(go env GOPATH)/bin"
    "$bin/plate" version

# Re-resolve every embedded ML script's lockfile (after editing a PEP 723 header)
[group("build")]
lock-ml:
    for f in internal/ml/scripts/*.py; do uv lock --script "$f"; done
