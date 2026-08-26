#!/usr/bin/env bash
# Measures and pins internal/bench/baseline.json. byb-om7.8 steps 5 and 6.
#
# Usage, from a clean checkout with the extracted bench set to hand:
#
#     tools/bench/baseline.sh <bench-set-dir> <commit-sha> <out-dir>
#
# WHY DOCKER. The baseline must be measured on Linux. On macOS /proc/self/io is
# absent, so disk_bytes, write_iops and read_iops are recorded as missing and
# three of the six metrics are lost. Verified rig: linux/arm64, golang:1.26.4.
#
# WHY THE SOURCE IS COPIED AND NOT BENCHMARKED IN PLACE. A bind mount on macOS
# is virtiofs. Reads through it are neither the speed nor the syscall shape of
# a real Linux filesystem, and read_iops is one of the six metrics.
#
# CAUTION: latency is not measurable on a loaded box. inspect's run-to-run
# spread was 5.10% quiet and 52.87% while three agents saturated the same
# machine. Stop every other build, container and agent before running this, and
# check the load average first.
set -euo pipefail

CAPS=inspect,extract-raster,build-pdf,jbig2-generic,quantize-png,downsample,jpeg-recompress,linearize,text-layer
IMAGE=golang:1.26.4
PLATFORM=linux/arm64

if [ "${1:-}" = "--in-container" ]; then
	commit=$2
	benchset=$3

	cp -r /src /work
	cp -r /set /workset
	cd /work
	CGO_ENABLED=0 go build -o /work/byblos-bench ./cmd/byblos-bench

	# THE SINGLE DEFINITION OF THE HARNESS FINGERPRINT IS
	# .github/workflows/bench.yml, reproduced here character for character. CI
	# computes the value this way, so a baseline whose harness sha was computed
	# any other way is stale the moment it lands.
	harness=$(cat cmd/byblos-bench/*.go internal/bench/map.go | sha256sum | cut -d' ' -f1)

	# THREE RUNS MINIMUM. One run records a spread of zero, and Score reads a
	# zero spread as zero tolerance, so a one-run baseline fails every candidate
	# on jitter. The spreads these repetitions produce are the noise floor.
	for n in 1 2 3; do
		echo "=== run $n"
		/work/byblos-bench run -set /workset -out "/out/run-$n.json" -reps 3 -time "$CAPS"
	done

	/work/byblos-bench baseline \
		-runs /out/run-1.json,/out/run-2.json,/out/run-3.json \
		-out /out/baseline.json \
		-commit "$commit" -benchset "$benchset" -harness "$harness"

	# THE CONTROL. A fourth run of the same unchanged code, scored against the
	# baseline just built. It MUST fail and exit 1. A control that passes means
	# the scorer read jitter as improvement, which is the hole NoiseMargin
	# exists to close, so the baseline is not fit to commit and is deleted.
	echo "=== control run 4"
	/work/byblos-bench run -set /workset -out /out/run-4.json -reps 3 -time "$CAPS"
	set +e
	/work/byblos-bench score \
		-baseline /out/baseline.json -head /out/run-4.json -base-run /out/run-1.json |
		tee /out/control.md
	rc=${PIPESTATUS[0]}
	set -e
	echo "control exit code: $rc" | tee /out/control-exit
	if [ "$rc" -eq 0 ]; then
		rm -f /out/baseline.json
		echo "REFUSING TO PIN: the control run PASSED. An unchanged run must fail." >&2
		exit 1
	fi
	echo "baseline written to /out/baseline.json; control failed as required"
	exit 0
fi

set=${1:?bench set directory}
commit=${2:?the commit these runs measure}
out=${3:?output directory}
mkdir -p "$out"

# The archive sha256 is the -benchset fingerprint. Read it from the release's
# published checksum rather than retyping it.
benchset=996e9df965c0bfb1b8a63e2945ad7591a72e8bb041300187010cd030867649d6
n=$(find "$set" -maxdepth 1 -name '*.pdf' | wc -l | tr -d ' ')
test "$n" -eq 12 || { echo "bench set holds $n documents, expected 12" >&2; exit 1; }

echo "load average before measuring: $(uptime)"
exec docker run --rm --platform "$PLATFORM" --cpus 4 \
	-v "$(git rev-parse --show-toplevel)":/src:ro \
	-v "$(cd "$set" && pwd)":/set:ro \
	-v "$(cd "$out" && pwd)":/out \
	-e GOMODCACHE=/gomod -v byblos-gomod:/gomod \
	"$IMAGE" bash /src/tools/bench/baseline.sh --in-container "$commit" "$benchset"
