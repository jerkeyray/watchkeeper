#!/bin/sh
set -eu
mode=${1:-run}
config=${2:-benchmarks/smoke.json}
output=${3:-email-$(date -u +%Y%m%dT%H%M%SZ)}
case "$output" in *[!a-zA-Z0-9_-]*|'') echo 'output must contain only letters, digits, underscores and hyphens' >&2; exit 2;; esac
project=${BENCH_PROJECT:-watchkeeper-bench-$output}
export BENCH_SOURCE_REVISION
BENCH_SOURCE_REVISION=$(git rev-parse HEAD)
# Include untracked implementation files in the source fingerprint.
export BENCH_DIRTY_DIFF_HASH
BENCH_DIRTY_DIFF_HASH=$(go run ./cmd/benchmark source-hash)
compose() { docker compose -p "$project" -f deploy/benchmark/compose.yaml "$@"; }
mkdir -p "results/$output"
compose up -d --wait postgres
compose build suite
export BENCH_IMAGE_DIGESTS
BENCH_IMAGE_DIGESTS=$(docker image inspect "$project-suite" postgres:17-alpine --format '{{.Id}} {{json .RepoDigests}}')
# Run as the invoking user so bind-mounted result files remain readable/writable.
compose run --rm --user "$(id -u):$(id -g)" -e BENCH_SOURCE_REVISION -e BENCH_DIRTY_DIFF_HASH -e BENCH_IMAGE_DIGESTS suite /app/benchmark "$mode" -config "/app/$config" -output "/results/$output"
echo "Results: results/$output; retained Compose project: $project"
