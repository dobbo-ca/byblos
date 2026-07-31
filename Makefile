.PHONY: build test lint corpus oracle glyphless jbig2-goldens

build:
	CGO_ENABLED=0 go build ./...

test:
	CGO_ENABLED=0 go test ./...

# go vet is the gate CI enforces. golangci-lint is a local convenience and is
# skipped when it is not installed, so `make lint` is runnable on a clean
# machine instead of failing with "command not found".
lint:
	CGO_ENABLED=0 go vet ./...
	@if command -v golangci-lint >/dev/null 2>&1; then \
		golangci-lint run; \
	else \
		echo "golangci-lint not installed; skipping (go vet above is the enforced gate)"; \
	fi

# Writes the generated PDF corpus to testdata/corpus/ (gitignored). Tests build
# the same corpus in memory; this target exists only to feed the oracle tooling.
corpus:
	CGO_ENABLED=0 go run ./cmd/byblos-corpus testdata/corpus

# Regenerates testdata/oracle/poppler.json. Requires poppler. Manual step,
# never run in CI. Commit the result.
oracle: corpus
	CGO_ENABLED=0 go run testdata/oracle/gen.go

# Regenerates internal/glyphless/glyphless.ttf, the invisible-text font byb-b4
# stamps with. Manual step, never run in CI. Commit the result.
glyphless:
	CGO_ENABLED=0 go run internal/glyphless/gen.go

# Regenerates the committed JBIG2 encoder goldens. Requires jbig2dec, which
# verifies each stream round-trips losslessly before the golden is written.
# Manual step -- never run in CI. Commit the results.
jbig2-goldens:
	go test ./internal/jbig2/ -run TestEncoderGoldens -update -v
