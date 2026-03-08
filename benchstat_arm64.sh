#!/usr/bin/env bash

set -euo pipefail

readonly SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
readonly PKG="./cel"
readonly BENCH_TIME="100ms"
readonly BENCH_COUNT="8"
readonly DOCKER_IMAGE="golang:1.26"
readonly DOCKER_PLATFORM="linux/arm64"
readonly TARGET_ARCH="arm64"

cd "${SCRIPT_DIR}"

if [ "$(go env GOARCH)" = "${TARGET_ARCH}" ]; then
	go test "${PKG}" -run '^$' -bench '^BenchmarkJit_Interpreter$' -benchmem -benchtime="${BENCH_TIME}" -count="${BENCH_COUNT}" > interpreter_arm64.txt
	go test "${PKG}" -run '^$' -bench '^BenchmarkJit_Jit$' -benchmem -benchtime="${BENCH_TIME}" -count="${BENCH_COUNT}" > jit_arm64.txt
else
	docker run --rm --platform "${DOCKER_PLATFORM}" \
		-v "${SCRIPT_DIR}:/work" \
		-w /work \
		"${DOCKER_IMAGE}" \
		/usr/local/go/bin/go test "${PKG}" -run '^$' -bench '^BenchmarkJit_Interpreter$' -benchmem -benchtime="${BENCH_TIME}" -count="${BENCH_COUNT}" > interpreter_arm64.txt
	docker run --rm --platform "${DOCKER_PLATFORM}" \
		-v "${SCRIPT_DIR}:/work" \
		-w /work \
		"${DOCKER_IMAGE}" \
		/usr/local/go/bin/go test "${PKG}" -run '^$' -bench '^BenchmarkJit_Jit$' -benchmem -benchtime="${BENCH_TIME}" -count="${BENCH_COUNT}" > jit_arm64.txt
fi

benchstat -ignore .name interpreter_arm64.txt jit_arm64.txt > benchstat_arm64.txt
