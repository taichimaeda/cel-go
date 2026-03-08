#!/usr/bin/env bash

set -euo pipefail

readonly SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
readonly PKG="./cel"
readonly BENCH_TIME="2s"
readonly BENCH_COUNT="1"
readonly DOCKER_IMAGE="golang:1.26"
readonly DOCKER_PLATFORM="linux/amd64"

cd "${SCRIPT_DIR}"

docker run --rm --platform "${DOCKER_PLATFORM}" \
	-v "${SCRIPT_DIR}:/work" \
	-w /work \
	"${DOCKER_IMAGE}" \
	/usr/local/go/bin/go test "${PKG}" -run '^$' -bench '^BenchmarkJit_Jit$' \
	-benchtime="${BENCH_TIME}" -count="${BENCH_COUNT}" \
	-cpuprofile=jit_cpu_amd64.out

go tool pprof -http=:8080 jit_cpu_amd64.out
