#!/usr/bin/env bash

set -euo pipefail

readonly SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
readonly PKG="./cel"
readonly BENCH_TIME="2s"
readonly BENCH_COUNT="1"

cd "${SCRIPT_DIR}"

go test "${PKG}" -run '^$' -bench '^BenchmarkJit_Jit$' \
	-benchtime="${BENCH_TIME}" -count="${BENCH_COUNT}" \
	-cpuprofile=jit_cpu_arm64.out

go tool pprof -http=:8080 jit_cpu_arm64.out
