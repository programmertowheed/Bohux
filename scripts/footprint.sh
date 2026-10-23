#!/usr/bin/env bash
# Measures memory of the Bohux stack for the ADR-0020 footprint gate.
# Run on the target box (2GB VPS) after `make run-dev`:
#   scripts/footprint.sh [build-context-dir]
# Prints idle memory, then memory while BuildKit builds the given context
# (default: this repo's Dockerfile-less sample in scripts/footprint/).
set -euo pipefail

compose=(docker compose -f "$(dirname "$0")/../deploy/dev/compose.yaml")
context="${1:-$(dirname "$0")/footprint}"

snapshot() {
  echo "== $1 =="
  docker stats --no-stream --format '{{.Name}}\t{{.MemUsage}}' \
    $("${compose[@]}" ps -q) | sort
  free -m | awk 'NR==2 {printf "host: used %sMB, available %sMB of %sMB\n", $3, $7, $2}'
  echo
}

"${compose[@]}" ps --status running --quiet | grep -q . || {
  echo "stack is not running; run 'make run-dev' first" >&2
  exit 1
}

echo "waiting 60s for the stack to settle"
sleep 60
snapshot "idle"

echo "building $context with BuildKit"
docker run --rm --network host -v "$(realpath "$context")":/ctx:ro \
  --entrypoint buildctl moby/buildkit:v0.18.2 \
  --addr tcp://127.0.0.1:1234 build \
  --frontend dockerfile.v0 --local context=/ctx --local dockerfile=/ctx &
build_pid=$!
sleep 20
snapshot "mid-build"
wait "$build_pid"

cat <<'NOTE'
Gate (ADR-0011 / ADR-0020): "available" must stay >= 1024MB with the stack idle.
Record both snapshots, the VPS provider and plan, and the date in docs/footprint.md.
NOTE
