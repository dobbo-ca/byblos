.PHONY: build test lint corpus oracle glyphless jbig2-goldens fontmeasure

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

# Builds the four synthetic box fonts the byb-8b9.6 measurement compares
# against a real face. Output goes to tools/fontmeasure/faces/, which is
# gitignored: these are reproducible from this target, unlike the oracle and
# glyphless assets above. Manual step, never run in CI.
fontmeasure:
	mkdir -p tools/fontmeasure/faces
	CGO_ENABLED=0 go run tools/fontmeasure/boxfont.go -style=filled -family="Byblos Box" -out=tools/fontmeasure/faces/box-filled.ttf
	CGO_ENABLED=0 go run tools/fontmeasure/boxfont.go -style=hollow -family="Byblos Box Hollow" -out=tools/fontmeasure/faces/box-hollow.ttf
	CGO_ENABLED=0 go run tools/fontmeasure/boxfont.go -style=filled -inset=0.324 -family="Byblos Box Narrow" -out=tools/fontmeasure/faces/box-narrow.ttf
	CGO_ENABLED=0 go run tools/fontmeasure/boxfont.go -style=filled -top=230 -family="Byblos Box Short" -out=tools/fontmeasure/faces/box-short.ttf

# Regenerates the committed JBIG2 encoder goldens. Requires jbig2dec, which
# verifies each stream round-trips losslessly before the golden is written.
# Manual step -- never run in CI. Commit the results.
jbig2-goldens:
	go test ./internal/jbig2/ -run TestEncoderGoldens -update -v
