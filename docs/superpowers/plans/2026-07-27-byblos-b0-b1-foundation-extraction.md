# Byblos B0 + B1 Implementation Plan — foundation and extraction

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Ship beads epics `byb-b0` (module scaffolding, the byblos-owned 1bpp `Bitmap`, provenance types, `Capabilities()`, `UpgradeCandidates()`) and `byb-b1` (`Inspect`, `ExtractPageRaster`, `ErrNotSingleRaster`, and day-one instrumentation of the divert rate).

**Architecture:** Four layers, bottom-up. `internal/corpus` generates every test PDF from committed Go code, so no binary fixture of uncertain provenance enters the repository. `internal/content` is a PDF content-stream lexer and graphics-state walker — pdfcpu has no operator parser, and this is the single largest build cost of using it as substrate (design spec §2 depends on telling a clean page-covering scan from a page with an overlay, and that decision can only be made from the content stream). `internal/pdfdoc` is the **only** package that imports pdfcpu; everything above it speaks in Byblos types, which is what design spec §3 means by "wraps pdfcpu behind its own interfaces". The root `byblos` package holds the public API from design spec §4.

**Tech Stack:** Go 1.26. One non-test dependency: `github.com/pdfcpu/pdfcpu` v0.13.0 (Apache-2.0, pure Go). Test-only external oracles: poppler's `pdfinfo`, `pdfimages`, `pdftotext`, used to generate a committed JSON golden — never linked, never shipped, never required to run `go test`.

## Global Constraints

- **Go 1.26.** `go.mod` declares `go 1.26`. **Module path:** `github.com/dobbo-ca/byblos`.
- **No cgo.** Every package builds with `CGO_ENABLED=0`, including a `GOOS=linux GOARCH=arm64` cross-build. CI enforces both.
- **Permitted dependencies: `github.com/pdfcpu/pdfcpu` and `golang.org/x/image` ONLY.** Anything else must be raised with the plan author, not added. CI fails on any other external import. B0/B1 needs only pdfcpu; `golang.org/x/image` arrives with B3 and must not be added speculatively here.
- **Byblos does not import Cadmus and Cadmus does not import Byblos.** Byblos owns its own 1bpp `Bitmap` type (Task 2). If you find yourself wanting to share code with Cadmus, stop and raise it.
- **Apache-2.0. Byblos is reimplemented from format specifications.** It is **not** a port of OCRmyPDF (MPL-2.0) — never open or translate OCRmyPDF source. Format references are ISO 32000-1:2008 (PDF) and the pdfcpu API surface.
- **Test-only oracles.** `pdfinfo`, `pdfimages`, `pdftotext`, and later `pngquant` may be invoked by golden-*generation* tooling, never by tests themselves. `go test ./...` must pass on a machine with none of them installed: tests `t.Skipf` when a committed golden is absent.
- **Only `internal/pdfdoc` may import pdfcpu.** Task 8 adds a test that enforces this.
- **Never pass a nil `*model.Configuration` to a pdfcpu `api.*` function.** `api.ReadAndValidate` dereferences `conf.Cmd` with no nil check and pdfcpu's `fault.Catch` only recovers its own panic type, so a nil config is an unrecoverable process kill, not an error. Every call passes `model.NewDefaultConfiguration()`.
- **Coordinate convention.** `PageInfo.Bounds` and `ImageRef.Bounds` are PDF default user space: points (1/72"), origin lower-left, **y increasing upward**. `image.Rectangle` is used only as a convenient integer rectangle; do not assume screen orientation. `Bitmap` is the opposite: origin top-left, y down, matching `image.Rectangle`'s usual reading. Both conventions are documented at their declaration and must not be mixed silently.
- **Bitonal convention.** In a `Bitmap`, a set bit (1) is **black ink**. This matches JBIG2, and is the inverse of PDF `/DeviceGray` where 1 is white.

---

## File Structure

| File | Responsibility |
|---|---|
| `go.mod`, `Makefile`, `.golangci.yml`, `.github/workflows/ci.yml`, `NOTICE`, `doc.go` | Scaffolding (Task 1) |
| `bitmap.go` | The byblos-owned 1bpp `Bitmap` (Task 2) |
| `provenance.go` | `Provenance`, `PageProvenance`, `Capabilities()` (Task 3) |
| `upgrade.go` | `UpgradeCandidates()` and the capability rule table (Task 4) |
| `internal/corpus/corpus.go` | Deterministic in-memory PDF corpus + minimal PDF writer (Task 5) |
| `cmd/byblos-corpus/main.go` | Writes the corpus to disk for the oracle tooling (Task 5) |
| `internal/content/lexer.go` | PDF content-stream tokenizer (Task 6) |
| `internal/content/walk.go` | `Matrix`, `Box`, graphics-state walker, `Scan` (Task 7) |
| `internal/pdfdoc/pdfdoc.go` | The only pdfcpu importer: `Doc`, `Page`, `ImageInfo`, `content.Env` (Task 8) |
| `inspect.go` | `Inspect`, `PageInfo`, `ImageRef` (Task 9) |
| `extract.go` | `ExtractPageRaster`, `ErrNotSingleRaster`, `ErrUnsupportedImageCodec`, `classify` (Task 10) |
| `stats.go`, `cmd/byblos-divert/main.go` | Divert-rate counters and the corpus-scanning CLI (Task 11) |
| `testdata/oracle/gen.go`, `testdata/oracle/poppler.json` | Poppler golden generator (manual) and its committed output (Task 12) |

`testdata/corpus/` is already in `.gitignore` and stays there: the corpus is generated, never committed.

**Dependency order:** 1 → 2, 3 → 4; 1 → 5 → 6 → 7 → 8 → 9 → 10 → 11 → 12. Tasks 2/3/4 (B0) and Tasks 5–12 (B1) share only Task 1 and can be worked concurrently after it.

---

## Task 1: Repository scaffolding and the dependency guard

**Files:**
- Create: `go.mod`, `Makefile`, `.golangci.yml`, `.github/workflows/ci.yml`, `NOTICE`, `doc.go`

**Interfaces:**
- Consumes: nothing.
- Produces: a module named `github.com/dobbo-ca/byblos` that builds and tests clean with `CGO_ENABLED=0`; `make build`, `make test`, `make lint`.

- [ ] **Step 1: Verify the pdfcpu version and its purity before depending on it**

The research pinned v0.13.0 as latest on 2026-06-09 and confirmed it is pure Go. Confirm both, now, rather than inheriting a stale fact:

```bash
cd /Users/christopherdobbyn/work/dobbo-ca/byblos
go list -m -versions github.com/pdfcpu/pdfcpu | tr ' ' '\n' | tail -5
```

Expected: `v0.13.0` is present. **If a newer version exists, use it, and re-run Steps 8–9 of Task 8 against it** — the API facts in this plan were verified against v0.13.0 specifically.

```bash
go mod download github.com/pdfcpu/pdfcpu@v0.13.0 2>/dev/null || true
grep -rl 'import "C"' "$(go env GOMODCACHE)/github.com/pdfcpu/pdfcpu@v0.13.0" | head
```

Expected: no output. Any hit means pdfcpu is not cgo-free and the whole dependency decision must be raised before continuing.

- [ ] **Step 2: Create the module**

```bash
cd /Users/christopherdobbyn/work/dobbo-ca/byblos
go mod init github.com/dobbo-ca/byblos
go get github.com/pdfcpu/pdfcpu@v0.13.0
```

Then confirm `go.mod` looks like this — verified on Go 1.26.4:

```
module github.com/dobbo-ca/byblos

go 1.26.4

require github.com/pdfcpu/pdfcpu v0.13.0 // indirect
```

Two things that are easy to misread as errors and are not:

- `go mod init` writes the **full toolchain version** (`go 1.26.4`, not `go 1.26`). Anything `>= 1.26` satisfies the Global Constraint; do not hand-edit it down.
- pdfcpu is marked `// indirect` and its own dependencies are absent, because nothing imports pdfcpu until Task 8. Both resolve themselves the first time `go build ./...` runs with `internal/pdfdoc` present. Do not add a blank import to "fix" it.

Do **not** add `golang.org/x/image` — it belongs to B3.

- [ ] **Step 3: Add the package doc and the licensing statement**

Create `doc.go`:

```go
// Package byblos is a pure-Go PDF pipeline for scanned documents: no cgo, no
// shared libraries, no subprocesses.
//
// Byblos does not render PDFs. Scanned pages are overwhelmingly one
// page-covering image per page, which requires extraction rather than
// rendering; pages that are not are detected and reported with
// ErrNotSingleRaster rather than guessed at. See docs/superpowers/specs.
//
// Licensing: Byblos is Apache-2.0 and is reimplemented from format
// specifications — principally ISO 32000-1:2008 — and from the documented
// behaviour of the tools it replaces. It is NOT a port of OCRmyPDF, which is
// MPL-2.0 file-level copyleft; no OCRmyPDF source is consulted or translated.
// See NOTICE.
package byblos
```

Create `NOTICE`:

```
Byblos
Copyright 2026 Chris Dobbyn

This product includes software developed at pdfcpu (https://pdfcpu.io/),
licensed under the Apache License, Version 2.0.

Byblos is an original implementation written from format specifications
(ISO 32000-1:2008; ITU-T T.88). It is not derived from OCRmyPDF, Ghostscript,
jbig2enc, pngquant, poppler, or img2pdf.
```

- [ ] **Step 4: Add the Makefile**

Create `Makefile`:

```make
.PHONY: build test lint corpus oracle

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
```

- [ ] **Step 5: Add the linter config**

Create `.golangci.yml`:

```yaml
version: "2"
linters:
  enable:
    - errcheck
    - govet
    - ineffassign
    - staticcheck
    - unused
  exclusions:
    presets:
      # golangci-lint v2 has no implicit default exclusions. Without this,
      # errcheck flags every fmt.Printf/fmt.Fprintln in cmd/byblos-corpus and
      # cmd/byblos-divert and the `defer f.Close()` in byblos-divert.
      - std-error-handling
```

**Verify this config rather than assuming it.** `golangci-lint` is not installed on the development machine this plan was written on, and CI (Step 6) runs `go vet` only, so nothing else exercises this file. If you have golangci-lint, run once now:

```bash
golangci-lint config verify && golangci-lint run
```

Expected: the config validates and the run is clean once Tasks 5 and 11 have added the two commands. If your version rejects `exclusions.presets`, run `golangci-lint config verify` and adapt the key to what it reports — **do not "fix" it by removing `errcheck` from the enabled list.** If you do not have golangci-lint, `make lint` skips it and `go vet` is the gate; that is an accepted, stated limitation, not something to work around.

- [ ] **Step 6: Add CI, including the dependency allowlist**

Create `.github/workflows/ci.yml`:

```yaml
name: ci
on:
  push:
    branches: [main]
  pull_request:

jobs:
  test:
    runs-on: ubuntu-latest
    env:
      CGO_ENABLED: 0
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with:
          go-version: '1.26'
      - run: go build ./...
      - run: go vet ./...
      - run: go test ./...
      - name: cross-build without cgo
        run: GOOS=linux GOARCH=arm64 go build ./...
      - name: assert only permitted dependencies
        run: |
          go list -f '{{range .Imports}}{{.}}{{"\n"}}{{end}}{{range .TestImports}}{{.}}{{"\n"}}{{end}}{{range .XTestImports}}{{.}}{{"\n"}}{{end}}' ./... \
            | sort -u | grep '\.' | grep -v '^github.com/dobbo-ca/byblos' > ext.txt || true
          echo "external imports:"; cat ext.txt
          if grep -vE '^(github\.com/pdfcpu/pdfcpu|golang\.org/x/image)(/|$)' ext.txt; then
            echo "FORBIDDEN dependency listed above. Permitted: pdfcpu, golang.org/x/image."
            exit 1
          fi
```

The `grep '\.'` filter keeps only import paths whose first element contains a dot — i.e. module paths, never standard-library packages.

- [ ] **Step 7: Verify the scaffolding**

```bash
make build && make test && CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build ./...
```

Expected: all three succeed. `go test ./...` reports `no test files` for the root package — that is a pass at this point.

- [ ] **Step 8: Commit**

```bash
git add go.mod go.sum Makefile .golangci.yml .github NOTICE doc.go
git commit -m "chore: scaffold the Go module, CI, and the dependency allowlist"
```

---

## Task 2: The byblos-owned 1bpp Bitmap

Design spec §3 is explicit: neither Byblos nor Cadmus imports the other. Cadmus has an `internal/imaging.Bitmap` with 1bpp and 8bpp depths; Byblos needs only 1bpp, for `EncodeJBIG2Generic` in B2. This is a deliberate, small duplication.

**Files:**
- Create: `bitmap.go`
- Test: `bitmap_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces:

```go
type Bitmap struct {
    Width, Height int
    Stride        int
    Pix           []byte
}

func NewBitmap(w, h int) *Bitmap
func (b *Bitmap) At(x, y int) uint8
func (b *Bitmap) Set(x, y int, v uint8)
func (b *Bitmap) Bounds() image.Rectangle
func (b *Bitmap) Clone() *Bitmap
func (b *Bitmap) Equal(o *Bitmap) bool
```

- [ ] **Step 1: Write the failing test**

Create `bitmap_test.go`:

```go
package byblos

import (
	"image"
	"testing"
)

func TestNewBitmapStride(t *testing.T) {
	for _, tc := range []struct{ w, wantStride int }{
		{1, 1}, {7, 1}, {8, 1}, {9, 2}, {16, 2}, {17, 3},
	} {
		b := NewBitmap(tc.w, 3)
		if b.Stride != tc.wantStride {
			t.Errorf("NewBitmap(%d, 3).Stride = %d; want %d", tc.w, b.Stride, tc.wantStride)
		}
		if len(b.Pix) != tc.wantStride*3 {
			t.Errorf("NewBitmap(%d, 3) len(Pix) = %d; want %d", tc.w, len(b.Pix), tc.wantStride*3)
		}
	}
}

// Rows are packed MSB-first: pixel (x, y) is bit 7-(x%8) of Pix[y*Stride+x/8].
func TestBitmapPackingIsMSBFirst(t *testing.T) {
	b := NewBitmap(9, 2)
	b.Set(0, 0, 1) // bit 7 of byte 0
	b.Set(7, 0, 1) // bit 0 of byte 0
	b.Set(8, 0, 1) // bit 7 of byte 1
	b.Set(1, 1, 1) // bit 6 of byte 2

	want := []byte{0x81, 0x80, 0x40, 0x00}
	for i := range want {
		if b.Pix[i] != want[i] {
			t.Fatalf("Pix = % 02x; want % 02x", b.Pix, want)
		}
	}
}

func TestBitmapAtSetRoundTrip(t *testing.T) {
	b := NewBitmap(13, 5)
	set := [][2]int{{0, 0}, {12, 4}, {6, 2}, {8, 1}}
	for _, p := range set {
		b.Set(p[0], p[1], 1)
	}
	for _, p := range set {
		if got := b.At(p[0], p[1]); got != 1 {
			t.Errorf("At(%d, %d) = %d; want 1", p[0], p[1], got)
		}
	}
	if got := b.At(1, 0); got != 0 {
		t.Errorf("At(1, 0) = %d; want 0", got)
	}
	b.Set(0, 0, 0)
	if got := b.At(0, 0); got != 0 {
		t.Errorf("At(0, 0) after clearing = %d; want 0", got)
	}
}

// Out-of-bounds reads return 0. This is not defensive padding: JBIG2 T.88
// section 6.2.5.2 requires template pixels outside the bitmap to read as 0, so
// the encoder in B2 relies on exactly this behaviour.
func TestBitmapOutOfBoundsReadsZero(t *testing.T) {
	b := NewBitmap(4, 4)
	for x := 0; x < 4; x++ {
		for y := 0; y < 4; y++ {
			b.Set(x, y, 1)
		}
	}
	for _, p := range [][2]int{{-1, 0}, {0, -1}, {4, 0}, {0, 4}, {-3, -3}, {99, 99}} {
		if got := b.At(p[0], p[1]); got != 0 {
			t.Errorf("At(%d, %d) out of bounds = %d; want 0", p[0], p[1], got)
		}
	}
}

func TestBitmapOutOfBoundsWriteIsNoOp(t *testing.T) {
	b := NewBitmap(4, 4)
	for _, p := range [][2]int{{-1, 0}, {0, -1}, {4, 0}, {0, 4}} {
		b.Set(p[0], p[1], 1) // must not panic
	}
	for _, v := range b.Pix {
		if v != 0 {
			t.Fatalf("out-of-bounds Set wrote into Pix: % 02x", b.Pix)
		}
	}
}

// Set must never touch the padding bits past Width in the last byte of a row,
// because Equal compares the packed bytes directly.
func TestBitmapPaddingBitsRemainZero(t *testing.T) {
	b := NewBitmap(9, 1)
	for x := 0; x < 9; x++ {
		b.Set(x, 0, 1)
	}
	if b.Pix[1] != 0x80 {
		t.Errorf("Pix[1] = %02x; want 80 (only bit 7 set, padding clear)", b.Pix[1])
	}
}

func TestBitmapBounds(t *testing.T) {
	b := NewBitmap(11, 7)
	if got, want := b.Bounds(), image.Rect(0, 0, 11, 7); got != want {
		t.Errorf("Bounds() = %v; want %v", got, want)
	}
}

func TestBitmapCloneIsIndependent(t *testing.T) {
	b := NewBitmap(8, 2)
	b.Set(3, 1, 1)
	c := b.Clone()
	if !b.Equal(c) {
		t.Fatal("Clone() is not Equal to its source")
	}
	c.Set(0, 0, 1)
	if b.At(0, 0) != 0 {
		t.Error("mutating the clone changed the source")
	}
	if b.Equal(c) {
		t.Error("Equal() reported equality after the clone diverged")
	}
}

func TestBitmapEqualRejectsDifferentSizes(t *testing.T) {
	if NewBitmap(8, 2).Equal(NewBitmap(8, 3)) {
		t.Error("Equal() = true for 8x2 vs 8x3; want false")
	}
	if NewBitmap(8, 2).Equal(nil) {
		t.Error("Equal(nil) = true; want false")
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test . -run TestBitmap -v`
Expected: FAIL — `undefined: NewBitmap`.

- [ ] **Step 3: Implement Bitmap**

Create `bitmap.go`:

```go
package byblos

import (
	"bytes"
	"image"
)

// Bitmap is a 1-bit-per-pixel bilevel image owned by Byblos.
//
// Byblos deliberately does not share this type with Cadmus: neither library
// imports the other (design spec section 3), so each owns its own substrate.
//
// A set bit (1) is BLACK ink. That matches JBIG2, where 1 is black, and is the
// inverse of PDF /DeviceGray, where 1 is white; any conversion across that
// boundary inverts.
//
// The origin is top-left and y increases downward, matching image.Rectangle.
// Note that PageInfo.Bounds uses the opposite (PDF) convention.
//
// Rows are packed MSB-first: pixel (x, y) is bit 7-(x%8) of
// Pix[y*Stride + x/8]. Bits past Width in the final byte of a row are always
// zero, and Set preserves that, so Equal may compare Pix directly.
type Bitmap struct {
	Width, Height int
	Stride        int // bytes per row
	Pix           []byte
}

// NewBitmap returns a w x h bitmap with every pixel clear. It panics on a
// negative dimension.
func NewBitmap(w, h int) *Bitmap {
	if w < 0 || h < 0 {
		panic("byblos: NewBitmap with a negative dimension")
	}
	stride := (w + 7) / 8
	return &Bitmap{Width: w, Height: h, Stride: stride, Pix: make([]byte, stride*h)}
}

// At returns 1 or 0. Pixels outside the bitmap read as 0, as required by
// ITU-T T.88 section 6.2.5.2 for JBIG2 template gathering.
func (b *Bitmap) At(x, y int) uint8 {
	if x < 0 || y < 0 || x >= b.Width || y >= b.Height {
		return 0
	}
	return (b.Pix[y*b.Stride+x/8] >> (7 - uint(x)%8)) & 1
}

// Set writes 1 when v is non-zero and 0 otherwise. Coordinates outside the
// bitmap are ignored.
func (b *Bitmap) Set(x, y int, v uint8) {
	if x < 0 || y < 0 || x >= b.Width || y >= b.Height {
		return
	}
	i := y*b.Stride + x/8
	mask := byte(1) << (7 - uint(x)%8)
	if v != 0 {
		b.Pix[i] |= mask
	} else {
		b.Pix[i] &^= mask
	}
}

// Bounds returns the bitmap's extent with the origin at (0, 0).
func (b *Bitmap) Bounds() image.Rectangle { return image.Rect(0, 0, b.Width, b.Height) }

// Clone returns a deep copy.
func (b *Bitmap) Clone() *Bitmap {
	c := &Bitmap{Width: b.Width, Height: b.Height, Stride: b.Stride, Pix: make([]byte, len(b.Pix))}
	copy(c.Pix, b.Pix)
	return c
}

// Equal reports whether o has the same dimensions and the same pixels. It is
// the lossless check the JBIG2 round-trip test in B2 is built on.
func (b *Bitmap) Equal(o *Bitmap) bool {
	if o == nil || b.Width != o.Width || b.Height != o.Height {
		return false
	}
	return bytes.Equal(b.Pix, o.Pix)
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test . -run TestBitmap -v`
Expected: PASS, all nine tests.

- [ ] **Step 5: Commit**

```bash
git add bitmap.go bitmap_test.go
git commit -m "feat: add the byblos-owned 1bpp Bitmap type"
```

---

## Task 3: Provenance types and Capabilities()

**Files:**
- Create: `provenance.go`
- Test: `provenance_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces:

```go
const Version = "0.1.0"

type Provenance struct {
    Version      string           `json:"version"`
    Capabilities []string         `json:"capabilities"`
    ProcessedAt  time.Time        `json:"processed_at"`
    Pages        []PageProvenance `json:"pages"`
}

type PageProvenance struct {
    Applied  []string `json:"applied,omitempty"`
    Diverted string   `json:"diverted,omitempty"`
}

func Capabilities() []string
```

- [ ] **Step 1: Write the failing test**

Create `provenance_test.go`:

```go
package byblos

import (
	"encoding/json"
	"slices"
	"testing"
	"time"
)

func TestCapabilitiesIsSortedAndStable(t *testing.T) {
	got := Capabilities()
	if len(got) == 0 {
		t.Fatal("Capabilities() is empty")
	}
	if !slices.IsSorted(got) {
		t.Errorf("Capabilities() = %v; want sorted", got)
	}
	// The caller must not be able to corrupt the build's capability list.
	got[0] = "tampered"
	if Capabilities()[0] == "tampered" {
		t.Error("Capabilities() returns the package's own slice; want a copy")
	}
}

// B0+B1 delivers exactly these two. Later epics append; this assertion is the
// tripwire that a capability was added without a rule (see upgrade_test.go).
func TestCapabilitiesContainsB1Set(t *testing.T) {
	got := Capabilities()
	for _, want := range []string{"extract-raster", "inspect"} {
		if !slices.Contains(got, want) {
			t.Errorf("Capabilities() = %v; missing %q", got, want)
		}
	}
}

// The PDF carries provenance as JSON under a custom Info-dictionary key
// (design spec section 6), so the round trip must be exact.
func TestProvenanceJSONRoundTrip(t *testing.T) {
	in := &Provenance{
		Version:      Version,
		Capabilities: Capabilities(),
		ProcessedAt:  time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC),
		Pages: []PageProvenance{
			{Applied: []string{"jbig2-generic", "downsample-150"}},
			{Diverted: "not-single-raster"},
		},
	}
	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	var out Provenance
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if out.Version != in.Version || !slices.Equal(out.Capabilities, in.Capabilities) {
		t.Errorf("round trip = %+v; want %+v", out, in)
	}
	if !out.ProcessedAt.Equal(in.ProcessedAt) {
		t.Errorf("ProcessedAt = %v; want %v", out.ProcessedAt, in.ProcessedAt)
	}
	if len(out.Pages) != 2 || !slices.Equal(out.Pages[0].Applied, in.Pages[0].Applied) {
		t.Errorf("Pages = %+v; want %+v", out.Pages, in.Pages)
	}
	if out.Pages[1].Diverted != "not-single-raster" {
		t.Errorf("Pages[1].Diverted = %q; want \"not-single-raster\"", out.Pages[1].Diverted)
	}
}

// A page that was handled normally must not emit noise into the record.
func TestPageProvenanceOmitsEmptyFields(t *testing.T) {
	raw, err := json.Marshal(PageProvenance{})
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	if string(raw) != "{}" {
		t.Errorf("Marshal(PageProvenance{}) = %s; want {}", raw)
	}
}

func TestVersionIsSemver(t *testing.T) {
	if Version == "" {
		t.Fatal("Version is empty")
	}
	var maj, min, pat int
	if n, err := fmtSscan(Version, &maj, &min, &pat); err != nil || n != 3 {
		t.Errorf("Version = %q; want a MAJOR.MINOR.PATCH semver", Version)
	}
}
```

Add the tiny helper at the bottom of the same file (keeping the test self-contained):

```go
func fmtSscan(s string, a, b, c *int) (int, error) {
	return fmt.Sscanf(s, "%d.%d.%d", a, b, c)
}
```

and add `"fmt"` to the test file's imports.

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test . -run 'TestCapabilities|TestProvenance|TestPageProvenance|TestVersion' -v`
Expected: FAIL — `undefined: Capabilities`.

- [ ] **Step 3: Implement the provenance types**

Create `provenance.go`:

```go
package byblos

import (
	"slices"
	"time"
)

// Version is the Byblos semver recorded in every Provenance. It exists for
// humans and bug reports; upgrade decisions are driven by Capabilities, not by
// comparing versions (design spec section 6).
const Version = "0.1.0"

// Provenance is the record Byblos writes into a processed PDF, as JSON under a
// custom Info-dictionary key. The PDF is authoritative; any mirror of these
// fields in a caller's database is a cache.
type Provenance struct {
	Version      string           `json:"version"`
	Capabilities []string         `json:"capabilities"`
	ProcessedAt  time.Time        `json:"processed_at"`
	Pages        []PageProvenance `json:"pages"`
}

// PageProvenance records what one page actually received.
//
// Applied entries are capability names, optionally carrying a numeric
// parameter as a "-N" suffix, e.g. "downsample-150".
//
// Diverted is the coarse reason a page was not processed, e.g.
// "not-single-raster", and is empty when the page was handled normally. The
// fine-grained reason (see classify in extract.go) goes to the divert counters,
// not into the stored record: the record only needs to be precise enough to
// answer "would re-processing help?".
//
// divertClass in extract.go is the one place that maps the fine reason to the
// value stored here, and capabilityRules in upgrade.go is what matches on it.
// The two must agree; TestDivertClassCoversEveryReason is the tripwire.
type PageProvenance struct {
	Applied  []string `json:"applied,omitempty"`
	Diverted string   `json:"diverted,omitempty"`
}

// buildCapabilities is what this build of Byblos can do. Every entry MUST also
// have a rule in capabilityRules (upgrade.go); TestEveryCapabilityHasARule
// enforces that.
//
// Append to this list as each epic lands. Do not remove entries: a capability
// string is a permanent identifier that older documents' provenance refers to.
var buildCapabilities = []string{
	"extract-raster",
	"inspect",
}

// Capabilities returns, sorted, what this build can do.
func Capabilities() []string {
	out := slices.Clone(buildCapabilities)
	slices.Sort(out)
	return out
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test . -run 'TestCapabilities|TestProvenance|TestPageProvenance|TestVersion' -v`
Expected: PASS, all five tests.

- [ ] **Step 5: Commit**

```bash
git add provenance.go provenance_test.go
git commit -m "feat: add Provenance, PageProvenance, and Capabilities"
```

---

## Task 4: UpgradeCandidates and the capability rule table

This is the whole of goal G3. `UpgradeCandidates` answers *"would re-processing this document actually improve it?"* — an empty result means re-processing is wasted work.

**Files:**
- Create: `upgrade.go`
- Test: `upgrade_test.go`

**Interfaces:**
- Consumes: `Provenance`, `PageProvenance` (Task 3).
- Produces:

```go
func UpgradeCandidates(p *Provenance, current []string) []string
```

- [ ] **Step 1: Write the failing test, driven by the design spec section 6 table**

Create `upgrade_test.go`:

```go
package byblos

import (
	"slices"
	"testing"
)

func TestUpgradeCandidates(t *testing.T) {
	tests := []struct {
		name    string
		prov    *Provenance
		current []string
		want    []string
	}{
		// --- the three cases from design spec section 6 ---
		{
			name: "applied jbig2-generic, new jbig2-symbol: yes, smaller output",
			prov: &Provenance{
				Capabilities: []string{"inspect", "jbig2-generic"},
				Pages:        []PageProvenance{{Applied: []string{"jbig2-generic"}}},
			},
			current: []string{"inspect", "jbig2-generic", "jbig2-symbol"},
			want:    []string{"jbig2-symbol"},
		},
		{
			name: "diverted not-single-raster, new render: yes, now processable",
			prov: &Provenance{
				Capabilities: []string{"extract-raster", "inspect"},
				Pages:        []PageProvenance{{Diverted: "not-single-raster"}},
			},
			current: []string{"extract-raster", "inspect", "render"},
			want:    []string{"render"},
		},
		{
			name: "applied jpeg-recompress only, new jbig2-symbol: no bitonal content",
			prov: &Provenance{
				Capabilities: []string{"inspect", "jpeg-recompress"},
				Pages:        []PageProvenance{{Applied: []string{"jpeg-recompress"}}},
			},
			current: []string{"inspect", "jbig2-symbol", "jpeg-recompress"},
			want:    nil,
		},

		// --- boundary behaviour ---
		{
			name: "no capability gap at all",
			prov: &Provenance{
				Capabilities: []string{"inspect", "jbig2-generic"},
				Pages:        []PageProvenance{{Applied: []string{"jbig2-generic"}}},
			},
			current: []string{"inspect", "jbig2-generic"},
			want:    nil,
		},
		{
			name: "numeric parameter suffixes are stripped before matching",
			prov: &Provenance{
				Capabilities: []string{"downsample", "inspect", "jpeg-recompress"},
				Pages:        []PageProvenance{{Applied: []string{"downsample-150", "jpeg-recompress"}}},
			},
			current: []string{"downsample", "inspect", "jpeg-recompress", "page-cleanup"},
			want:    []string{"page-cleanup"},
		},
		{
			name: "ccitt-g4 is a compatibility fallback, never an improvement",
			prov: &Provenance{
				Capabilities: []string{"inspect", "jbig2-generic"},
				Pages:        []PageProvenance{{Applied: []string{"jbig2-generic"}}},
			},
			current: []string{"ccitt-g4", "inspect", "jbig2-generic"},
			want:    nil,
		},
		{
			name: "jbig2-generic improves a document that only got ccitt-g4",
			prov: &Provenance{
				Capabilities: []string{"ccitt-g4", "inspect"},
				Pages:        []PageProvenance{{Applied: []string{"ccitt-g4"}}},
			},
			current: []string{"ccitt-g4", "inspect", "jbig2-generic"},
			want:    []string{"jbig2-generic"},
		},
		{
			name: "unsupported-codec diverts are also render candidates",
			prov: &Provenance{
				Capabilities: []string{"extract-raster", "inspect"},
				Pages:        []PageProvenance{{Diverted: "unsupported-codec"}},
			},
			current: []string{"extract-raster", "inspect", "render"},
			want:    []string{"render"},
		},
		{
			name: "only the diverted page matters, not the handled ones",
			prov: &Provenance{
				Capabilities: []string{"extract-raster", "inspect", "jbig2-generic"},
				Pages: []PageProvenance{
					{Applied: []string{"jbig2-generic"}},
					{Diverted: "not-single-raster"},
				},
			},
			current: []string{"extract-raster", "inspect", "jbig2-generic", "render"},
			want:    []string{"render"},
		},
		{
			name: "an unknown new capability is reported: better a wasted re-run than a missed upgrade",
			prov: &Provenance{
				Capabilities: []string{"inspect"},
				Pages:        []PageProvenance{{Applied: []string{"jbig2-generic"}}},
			},
			current: []string{"inspect", "some-future-thing"},
			want:    []string{"some-future-thing"},
		},
		{
			name:    "nil provenance: nothing is known, so everything is a candidate",
			prov:    nil,
			current: []string{"inspect", "render"},
			want:    []string{"inspect", "render"},
		},
		{
			name: "results are sorted and deduplicated",
			prov: &Provenance{
				Capabilities: nil,
				Pages:        []PageProvenance{{Diverted: "not-single-raster"}},
			},
			current: []string{"render", "pdfa", "render"},
			want:    []string{"pdfa", "render"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := UpgradeCandidates(tc.prov, tc.current)
			if !slices.Equal(got, tc.want) {
				t.Errorf("UpgradeCandidates() = %v; want %v", got, tc.want)
			}
		})
	}
}

// A capability with no rule is silently treated as "always improves", which
// would make the reprocess job scan the whole archive. Catch it here instead.
func TestEveryCapabilityHasARule(t *testing.T) {
	for _, c := range Capabilities() {
		if _, ok := capabilityRules[c]; !ok {
			t.Errorf("capability %q has no rule in capabilityRules", c)
		}
	}
}

// Every capability string named in FUTURE.md must already have a rule, so that
// shipping one of them needs no change here.
func TestFutureCapabilitiesHaveRules(t *testing.T) {
	for _, c := range []string{"jbig2-symbol", "ccitt-g4", "render", "pdfa", "page-cleanup"} {
		if _, ok := capabilityRules[c]; !ok {
			t.Errorf("FUTURE.md capability %q has no rule in capabilityRules", c)
		}
	}
}

func TestAppliedCapability(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"downsample-150", "downsample"},
		{"jbig2-generic", "jbig2-generic"},
		{"jpeg-recompress", "jpeg-recompress"},
		{"quantize-png-64", "quantize-png"},
		{"trailing-", "trailing-"},
		{"noseparator", "noseparator"},
	} {
		if got := appliedCapability(tc.in); got != tc.want {
			t.Errorf("appliedCapability(%q) = %q; want %q", tc.in, got, tc.want)
		}
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test . -run 'TestUpgrade|TestEveryCapability|TestFutureCapabilities|TestAppliedCapability' -v`
Expected: FAIL — `undefined: UpgradeCandidates`.

- [ ] **Step 3: Implement the rule table and UpgradeCandidates**

Create `upgrade.go`:

```go
package byblos

import (
	"slices"
	"strings"
)

// capabilityRules maps a capability name to the condition under which *gaining*
// it would change the output for a document with the given recorded provenance.
//
// This is the heart of goal G3. A capability may have a rule long before it has
// an implementation — the rules for the FUTURE.md capabilities are here so that
// shipping one of them requires no change to this table.
var capabilityRules = map[string]func(*Provenance) bool{
	// Inspection and extraction do not alter output.
	"inspect":        never,
	"extract-raster": never,

	// A document that already got jbig2-generic cannot benefit from gaining it.
	// A document that got ccitt-g4 can: same losslessness, better ratio.
	"jbig2-generic": anyPageApplied("ccitt-g4"),

	// The intended next capability. Its upgrade set is exactly the pages that
	// recorded jbig2-generic (FUTURE.md).
	"jbig2-symbol": anyPageApplied("jbig2-generic"),

	// A compatibility fallback with strictly worse compression. Gaining it never
	// improves a document; it is only ever chosen deliberately (FUTURE.md).
	"ccitt-g4": never,

	// A renderer turns diverted pages into processable ones, and nothing else.
	"render": anyPageDiverted("not-single-raster", "unsupported-codec"),

	// Codec capabilities: gaining one does not improve a document that was
	// already processed with it, and we cannot tell from the record whether a
	// page that missed it had content it would have helped. Conservatively
	// never - a false positive here means re-processing the whole archive.
	"quantize-png":    never,
	"downsample":      never,
	"jpeg-recompress": never,
	"text-layer":      never,
	"linearize":       never,

	// Despeckling and border removal apply to any page whose raster Byblos
	// actually handled. Every prefix ends in "-" so that a future capability
	// whose name merely starts with one of these words does not match.
	"page-cleanup": anyPageAppliedPrefix("jbig2-", "ccitt-", "quantize-", "downsample-", "jpeg-"),

	// PDF/A conformance is a property of the whole file, so any document can be
	// converted.
	"pdfa": always,
}

func never(*Provenance) bool  { return false }
func always(*Provenance) bool { return true }

func anyPageApplied(want string) func(*Provenance) bool {
	return func(p *Provenance) bool {
		for _, pg := range p.Pages {
			for _, a := range pg.Applied {
				if appliedCapability(a) == want {
					return true
				}
			}
		}
		return false
	}
}

func anyPageAppliedPrefix(prefixes ...string) func(*Provenance) bool {
	return func(p *Provenance) bool {
		for _, pg := range p.Pages {
			for _, a := range pg.Applied {
				for _, pre := range prefixes {
					if strings.HasPrefix(a, pre) {
						return true
					}
				}
			}
		}
		return false
	}
}

func anyPageDiverted(reasons ...string) func(*Provenance) bool {
	return func(p *Provenance) bool {
		for _, pg := range p.Pages {
			if slices.Contains(reasons, pg.Diverted) {
				return true
			}
		}
		return false
	}
}

// appliedCapability strips a trailing numeric parameter from an Applied entry:
// "downsample-150" is the capability "downsample" applied at 150 DPI, while
// "jbig2-generic" is a capability name in its own right. Only an all-digit
// final segment is treated as a parameter.
func appliedCapability(s string) string {
	i := strings.LastIndexByte(s, '-')
	if i < 0 || i == len(s)-1 {
		return s
	}
	for _, r := range s[i+1:] {
		if r < '0' || r > '9' {
			return s
		}
	}
	return s[:i]
}

// UpgradeCandidates returns, sorted, the capabilities in current that the
// document described by p does not already have AND that would actually change
// its output. An empty result means re-processing the document is wasted work.
//
// A capability with no rule is reported: missing a real upgrade is worse than
// one wasted re-run, and TestEveryCapabilityHasARule keeps the gap from
// persisting. A nil p means nothing is known about the document, so every
// capability is a candidate.
func UpgradeCandidates(p *Provenance, current []string) []string {
	if p == nil {
		out := slices.Clone(current)
		slices.Sort(out)
		return slices.Compact(out)
	}
	var out []string
	for _, c := range current {
		if slices.Contains(p.Capabilities, c) {
			continue
		}
		rule, ok := capabilityRules[c]
		if !ok || rule(p) {
			out = append(out, c)
		}
	}
	slices.Sort(out)
	return slices.Compact(out)
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test . -run 'TestUpgrade|TestEveryCapability|TestFutureCapabilities|TestAppliedCapability' -v`
Expected: PASS — 12 `TestUpgradeCandidates` subtests plus the three guards.

- [ ] **Step 5: Verify the whole B0 surface and commit**

```bash
make test && make build
git add upgrade.go upgrade_test.go
git commit -m "feat: add UpgradeCandidates and the capability rule table"
```

- [ ] **Step 6: Close the B0 epic**

```bash
bd update byb-b0 --append-notes "B0 complete: module + CI with a dependency allowlist, byblos-owned 1bpp Bitmap, Provenance/PageProvenance, Capabilities(), UpgradeCandidates() with a rule table covering every FUTURE.md capability. The design spec section 6 table is a table-driven test."
bd close byb-b0
```

---

## Task 5: The generated test corpus

Design spec §8 requires a corpus of born-digital PDFs, single-image scans, tiled pages, image-plus-overlay pages, and at least one malformed file. **Every one is generated by committed Go code** — following the Cadmus precedent for its golden input — so no binary fixture of uncertain provenance enters the repository and anyone can reproduce it byte-for-byte.

Four of the documents deserve emphasis.

`overlay-text` puts its text inside a **Form XObject**, not in the page content stream: the research proved that a page with a form-borne overlay still reports exactly one image from `pdfcpu.Images()`, so an image count alone cannot detect it. `scan-in-form` is the mirror image — a clean scan wrapped in a form, which must **not** divert. Together they are the regression pair for the whole classification design.

`dup-raster` is two pages carrying a **byte-identical** raster as two distinct objects. This is the duplex-scanner shape: blank back-pages, separator sheets, any reused raster XObject. It exists because pdfcpu **deduplicates** byte-identical image XObjects during its optimize pass, so any extraction path keyed on "the object number pdfcpu's page-image map came back with" silently returns page 1's object for page 2. Task 8 does not use such a path; this document is what keeps it that way.

`jbig2` carries a page-covering image whose `/Filter` is `/JBIG2Decode` at 1 bit per component. It exists for two reasons: it is the only corpus document that makes `ImageRef.Bitonal` true (the field B2's JBIG2 path selects on), and it is the only one that exercises `ErrUnsupportedImageCodec` — pdfcpu returns JBIG2 payloads as **raw opaque bytes** with no error, which is exactly the trap that sentinel exists for.

**Files:**
- Create: `internal/corpus/corpus.go`, `cmd/byblos-corpus/main.go`
- Test: `internal/corpus/corpus_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces:

```go
type Doc struct {
    Name string
    Desc string
    Data []byte
}

func All() []Doc
func ByName(name string) ([]byte, bool)

const (
    PageWidthPt, PageHeightPt      = 612, 792
    ScanImageW, ScanImageH         = 306, 396
    TileImageW, TileImageH         = 153, 396
    BornDigitalTextChars           = 44
    OverlayTextChars               = 18
)
```

- [ ] **Step 1: Write the failing test**

Create `internal/corpus/corpus_test.go`:

```go
package corpus

import (
	"bytes"
	"fmt"
	"testing"
)

func wantNames() []string {
	return []string{
		"born-digital", "scan", "scan-rotated", "scan-in-form",
		"tiled", "overlay-text", "overlay-vector", "mixed",
		"dup-raster", "jbig2", "malformed",
	}
}

func TestAllReturnsTheExpectedCorpus(t *testing.T) {
	got := All()
	if len(got) != len(wantNames()) {
		t.Fatalf("All() returned %d documents; want %d", len(got), len(wantNames()))
	}
	seen := map[string]bool{}
	for _, d := range got {
		if seen[d.Name] {
			t.Errorf("duplicate document name %q", d.Name)
		}
		seen[d.Name] = true
		if d.Desc == "" {
			t.Errorf("document %q has no description", d.Name)
		}
		if len(d.Data) == 0 {
			t.Errorf("document %q is empty", d.Name)
		}
		if !bytes.HasPrefix(d.Data, []byte("%PDF-")) {
			t.Errorf("document %q does not start with %%PDF-", d.Name)
		}
	}
	for _, n := range wantNames() {
		if !seen[n] {
			t.Errorf("All() is missing %q", n)
		}
	}
}

// The corpus is a fixture. If it is not byte-stable, the committed poppler
// goldens in Task 12 stop meaning anything.
func TestGenerationIsDeterministic(t *testing.T) {
	a, b := All(), All()
	for i := range a {
		if !bytes.Equal(a[i].Data, b[i].Data) {
			t.Errorf("document %q differs between two calls to All()", a[i].Name)
		}
	}
}

func TestByName(t *testing.T) {
	if _, ok := ByName("scan"); !ok {
		t.Error("ByName(\"scan\") not found")
	}
	if _, ok := ByName("nope"); ok {
		t.Error("ByName(\"nope\") reported found")
	}
}

func TestMalformedIsATruncatedScan(t *testing.T) {
	scan, _ := ByName("scan")
	bad, _ := ByName("malformed")
	if len(bad) >= len(scan) {
		t.Fatalf("malformed is %d bytes, scan is %d; want strictly shorter", len(bad), len(scan))
	}
	if !bytes.HasPrefix(scan, bad) {
		t.Error("malformed is not a prefix of scan; it should be a plain truncation")
	}
	if bytes.Contains(bad, []byte("startxref")) {
		t.Error("malformed still contains startxref; truncate harder")
	}
}

// A self-check on the hand-rolled PDF writer: every xref offset must land on
// its own "N 0 obj" header. A writer bug here would surface much later as an
// unexplained pdfcpu parse failure.
func TestXrefOffsetsPointAtTheirObjects(t *testing.T) {
	for _, d := range All() {
		if d.Name == "malformed" {
			continue
		}
		t.Run(d.Name, func(t *testing.T) {
			data := d.Data
			i := bytes.LastIndex(data, []byte("startxref"))
			if i < 0 {
				t.Fatal("no startxref")
			}
			var start int
			// The EOL after "startxref" must be trimmed before Sscanf:
			// fmt.Sscanf requires newlines in the input to be matched by
			// newlines in the format, so "%d" against "\n123\n" fails with
			// "unexpected newline" on every document.
			tail := bytes.TrimLeft(data[i+len("startxref"):], " \r\n")
			if _, err := fmt.Sscanf(string(tail), "%d", &start); err != nil {
				t.Fatalf("parsing startxref: %v", err)
			}
			if start <= 0 || start >= len(data) || !bytes.HasPrefix(data[start:], []byte("xref\n")) {
				t.Fatalf("startxref %d does not point at an xref table", start)
			}
			hdr := data[start+len("xref\n"):]
			var count int
			if _, err := fmt.Sscanf(string(hdr), "0 %d", &count); err != nil {
				t.Fatalf("parsing xref subsection header: %v", err)
			}
			entries := hdr[bytes.IndexByte(hdr, '\n')+1:]
			if len(entries) < count*20 {
				t.Fatalf("xref table has %d bytes; want at least %d for %d entries", len(entries), count*20, count)
			}
			for n := 1; n < count; n++ {
				var off int
				if _, err := fmt.Sscanf(string(entries[n*20:n*20+10]), "%d", &off); err != nil {
					t.Fatalf("object %d: parsing xref entry: %v", n, err)
				}
				want := fmt.Sprintf("%d 0 obj", n)
				if off <= 0 || off >= len(data) || !bytes.HasPrefix(data[off:], []byte(want)) {
					end := min(off+16, len(data))
					t.Errorf("object %d: xref offset %d points at %q; want %q", n, off, data[off:end], want)
				}
			}
		})
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/corpus/ -v`
Expected: FAIL — `undefined: All`.

- [ ] **Step 3: Implement the corpus**

Create `internal/corpus/corpus.go`:

```go
// Package corpus builds the Byblos test corpus in memory.
//
// Every document here is produced by this code, deterministically. No binary
// PDF fixture of uncertain provenance enters the repository, and `make corpus`
// reproduces the exact bytes the committed poppler goldens were made from.
//
// The PDF writer below is deliberately minimal and hand-rolled rather than
// built on pdfcpu: the corpus must be able to express structures pdfcpu would
// never emit, including a truncated file.
package corpus

import (
	"bytes"
	"compress/zlib"
	"fmt"
)

// Geometry shared by every generated document. US Letter at 72 points/inch.
const (
	PageWidthPt  = 612
	PageHeightPt = 792

	ScanImageW, ScanImageH = 306, 396 // the full-page raster, 36 DPI
	TileImageW, TileImageH = 153, 396 // each half of the tiled page
)

// Text content, kept as constants so tests assert against a named value rather
// than a magic number.
const (
	bornDigitalContent = "BT /F1 12 Tf 1 0 0 1 72 720 Tm (Byblos born-digital page one.) Tj ET\n" +
		"BT /F1 12 Tf 1 0 0 1 72 700 Tm [ (Second) -250 (line) -250 (here.) ] TJ ET\n"
	overlayTextContent = "BT /F1 10 Tf 1 0 0 1 72 40 Tm (Scanned 2026-07-27) Tj ET\n"

	// BornDigitalTextChars is len("Byblos born-digital page one.") +
	// len("Second") + len("line") + len("here.") = 29 + 6 + 4 + 5.
	BornDigitalTextChars = 44
	// OverlayTextChars is len("Scanned 2026-07-27").
	OverlayTextChars = 18
)

const helveticaFont = "<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>"

// Doc is one generated test document.
type Doc struct {
	Name string
	Desc string
	Data []byte
}

// All returns the corpus, in a stable order.
func All() []Doc {
	return []Doc{
		{"born-digital", "text only, no images: must never be rasterized", bornDigital()},
		{"scan", "one page-covering image: the case the whole design targets", scan(0)},
		{"scan-rotated", "one page-covering image on a /Rotate 90 page", scan(90)},
		{"scan-in-form", "one page-covering image inside a Form XObject: must NOT divert", scanInForm()},
		{"tiled", "two half-page images: must divert", tiled()},
		{"overlay-text", "page-covering image plus text inside a Form XObject: must divert", overlayText()},
		{"overlay-vector", "page-covering image plus a stroked rectangle: must divert", overlayVector()},
		{"mixed", "two pages: born-digital then scan", mixed()},
		{"dup-raster", "two pages holding a byte-identical raster as two objects: both must extract", dupRaster()},
		{"jbig2", "one page-covering JBIG2 raster: 1 bpc, and a codec byblos cannot decode", jbig2()},
		{"malformed", "the scan document truncated mid-body", malformed()},
	}
}

// ByName returns one document's bytes.
func ByName(name string) ([]byte, bool) {
	for _, d := range All() {
		if d.Name == name {
			return d.Data, true
		}
	}
	return nil, false
}

// --- the minimal PDF writer -------------------------------------------------

type writer struct {
	buf     bytes.Buffer
	offsets []int // offsets[i-1] is the byte offset of object i; -1 until filled
}

func newWriter() *writer {
	w := &writer{}
	// The binary comment line marks the file as containing binary data, per
	// ISO 32000-1 section 7.5.2.
	w.buf.WriteString("%PDF-1.7\n%\xE2\xE3\xCF\xD3\n")
	return w
}

// reserve allocates an object number to be filled later, so parents can refer
// to children that have not been written yet.
func (w *writer) reserve() int {
	w.offsets = append(w.offsets, -1)
	return len(w.offsets)
}

func (w *writer) fill(n int, body string) {
	w.offsets[n-1] = w.buf.Len()
	fmt.Fprintf(&w.buf, "%d 0 obj\n%s\nendobj\n", n, body)
}

// fillStream writes a Flate-compressed stream object. PDF /FlateDecode is the
// zlib format of RFC 1950, which is what compress/zlib produces.
func (w *writer) fillStream(n int, dict string, payload []byte) {
	var z bytes.Buffer
	zw := zlib.NewWriter(&z)
	if _, err := zw.Write(payload); err != nil {
		panic(err)
	}
	if err := zw.Close(); err != nil {
		panic(err)
	}
	w.offsets[n-1] = w.buf.Len()
	fmt.Fprintf(&w.buf, "%d 0 obj\n<< %s /Filter /FlateDecode /Length %d >>\nstream\n", n, dict, z.Len())
	w.buf.Write(z.Bytes())
	w.buf.WriteString("\nendstream\nendobj\n")
}

// fillRawStream writes a stream object whose payload is stored verbatim. The
// dictionary declares whatever /Filter the caller wants; nothing is applied
// here. This exists so the corpus can carry a codec Go has no encoder for —
// see jbig2() below.
func (w *writer) fillRawStream(n int, dict string, payload []byte) {
	w.offsets[n-1] = w.buf.Len()
	fmt.Fprintf(&w.buf, "%d 0 obj\n<< %s /Length %d >>\nstream\n", n, dict, len(payload))
	w.buf.Write(payload)
	w.buf.WriteString("\nendstream\nendobj\n")
}

// finish writes the cross-reference table and trailer. Each xref entry is
// exactly 20 bytes, as ISO 32000-1 section 7.5.4 requires.
func (w *writer) finish(root int) []byte {
	start := w.buf.Len()
	fmt.Fprintf(&w.buf, "xref\n0 %d\n0000000000 65535 f \n", len(w.offsets)+1)
	for i, off := range w.offsets {
		if off < 0 {
			panic(fmt.Sprintf("corpus: object %d was reserved but never filled", i+1))
		}
		fmt.Fprintf(&w.buf, "%010d 00000 n \n", off)
	}
	fmt.Fprintf(&w.buf,
		"trailer\n<< /Size %d /Root %d 0 R >>\nstartxref\n%d\n%%%%EOF\n",
		len(w.offsets)+1, root, start)
	return w.buf.Bytes()
}

func imageDict(w, h int) string {
	return fmt.Sprintf("/Type /XObject /Subtype /Image /Width %d /Height %d"+
		" /ColorSpace /DeviceGray /BitsPerComponent 8", w, h)
}

// grayPixels returns deterministic 8-bit grey samples. The pattern is
// arbitrary but must be stable and must compress imperfectly, so that a
// truncation is genuinely damaging.
func grayPixels(w, h, seed int) []byte {
	px := make([]byte, w*h)
	for i := range px {
		px[i] = byte((i*7 + seed*31) % 251)
	}
	return px
}

// --- the documents ----------------------------------------------------------

func bornDigital() []byte {
	w := newWriter()
	cat, pages, page, cont, font := w.reserve(), w.reserve(), w.reserve(), w.reserve(), w.reserve()
	w.fill(cat, fmt.Sprintf("<< /Type /Catalog /Pages %d 0 R >>", pages))
	w.fill(pages, fmt.Sprintf("<< /Type /Pages /Kids [%d 0 R] /Count 1 >>", page))
	w.fill(page, fmt.Sprintf("<< /Type /Page /Parent %d 0 R /MediaBox [0 0 %d %d]"+
		" /Resources << /Font << /F1 %d 0 R >> >> /Contents %d 0 R >>",
		pages, PageWidthPt, PageHeightPt, font, cont))
	w.fillStream(cont, "", []byte(bornDigitalContent))
	w.fill(font, helveticaFont)
	return w.finish(cat)
}

func scan(rotate int) []byte {
	w := newWriter()
	cat, pages, page, cont, img := w.reserve(), w.reserve(), w.reserve(), w.reserve(), w.reserve()
	rot := ""
	if rotate != 0 {
		rot = fmt.Sprintf(" /Rotate %d", rotate)
	}
	w.fill(cat, fmt.Sprintf("<< /Type /Catalog /Pages %d 0 R >>", pages))
	w.fill(pages, fmt.Sprintf("<< /Type /Pages /Kids [%d 0 R] /Count 1 >>", page))
	w.fill(page, fmt.Sprintf("<< /Type /Page /Parent %d 0 R /MediaBox [0 0 %d %d]%s"+
		" /Resources << /XObject << /Im0 %d 0 R >> >> /Contents %d 0 R >>",
		pages, PageWidthPt, PageHeightPt, rot, img, cont))
	w.fillStream(cont, "", []byte(fmt.Sprintf("q %d 0 0 %d 0 0 cm /Im0 Do Q\n", PageWidthPt, PageHeightPt)))
	w.fillStream(img, imageDict(ScanImageW, ScanImageH), grayPixels(ScanImageW, ScanImageH, 1))
	return w.finish(cat)
}

func scanInForm() []byte {
	w := newWriter()
	cat, pages, page, cont, form, img := w.reserve(), w.reserve(), w.reserve(), w.reserve(), w.reserve(), w.reserve()
	w.fill(cat, fmt.Sprintf("<< /Type /Catalog /Pages %d 0 R >>", pages))
	w.fill(pages, fmt.Sprintf("<< /Type /Pages /Kids [%d 0 R] /Count 1 >>", page))
	w.fill(page, fmt.Sprintf("<< /Type /Page /Parent %d 0 R /MediaBox [0 0 %d %d]"+
		" /Resources << /XObject << /Fm0 %d 0 R >> >> /Contents %d 0 R >>",
		pages, PageWidthPt, PageHeightPt, form, cont))
	w.fillStream(cont, "", []byte("q 1 0 0 1 0 0 cm /Fm0 Do Q\n"))
	w.fillStream(form, fmt.Sprintf("/Type /XObject /Subtype /Form /BBox [0 0 %d %d]"+
		" /Matrix [1 0 0 1 0 0] /Resources << /XObject << /Im0 %d 0 R >> >>",
		PageWidthPt, PageHeightPt, img),
		[]byte(fmt.Sprintf("q %d 0 0 %d 0 0 cm /Im0 Do Q\n", PageWidthPt, PageHeightPt)))
	w.fillStream(img, imageDict(ScanImageW, ScanImageH), grayPixels(ScanImageW, ScanImageH, 1))
	return w.finish(cat)
}

func tiled() []byte {
	w := newWriter()
	cat, pages, page, cont := w.reserve(), w.reserve(), w.reserve(), w.reserve()
	img0, img1 := w.reserve(), w.reserve()
	w.fill(cat, fmt.Sprintf("<< /Type /Catalog /Pages %d 0 R >>", pages))
	w.fill(pages, fmt.Sprintf("<< /Type /Pages /Kids [%d 0 R] /Count 1 >>", page))
	w.fill(page, fmt.Sprintf("<< /Type /Page /Parent %d 0 R /MediaBox [0 0 %d %d]"+
		" /Resources << /XObject << /Im0 %d 0 R /Im1 %d 0 R >> >> /Contents %d 0 R >>",
		pages, PageWidthPt, PageHeightPt, img0, img1, cont))
	half := PageWidthPt / 2
	w.fillStream(cont, "", []byte(fmt.Sprintf(
		"q %d 0 0 %d 0 0 cm /Im0 Do Q\nq %d 0 0 %d %d 0 cm /Im1 Do Q\n",
		half, PageHeightPt, half, PageHeightPt, half)))
	w.fillStream(img0, imageDict(TileImageW, TileImageH), grayPixels(TileImageW, TileImageH, 2))
	w.fillStream(img1, imageDict(TileImageW, TileImageH), grayPixels(TileImageW, TileImageH, 3))
	return w.finish(cat)
}

// overlayText hides its text inside a Form XObject. pdfcpu's Images() still
// reports exactly one image for this page, which is why classification has to
// walk the content stream and recurse into forms.
func overlayText() []byte {
	w := newWriter()
	cat, pages, page, cont := w.reserve(), w.reserve(), w.reserve(), w.reserve()
	form, font, img := w.reserve(), w.reserve(), w.reserve()
	w.fill(cat, fmt.Sprintf("<< /Type /Catalog /Pages %d 0 R >>", pages))
	w.fill(pages, fmt.Sprintf("<< /Type /Pages /Kids [%d 0 R] /Count 1 >>", page))
	w.fill(page, fmt.Sprintf("<< /Type /Page /Parent %d 0 R /MediaBox [0 0 %d %d]"+
		" /Resources << /XObject << /Im0 %d 0 R /Fm0 %d 0 R >> >> /Contents %d 0 R >>",
		pages, PageWidthPt, PageHeightPt, img, form, cont))
	w.fillStream(cont, "", []byte(fmt.Sprintf(
		"q %d 0 0 %d 0 0 cm /Im0 Do Q\nq /Fm0 Do Q\n", PageWidthPt, PageHeightPt)))
	w.fillStream(form, fmt.Sprintf("/Type /XObject /Subtype /Form /BBox [0 0 %d %d]"+
		" /Resources << /Font << /F1 %d 0 R >> >>", PageWidthPt, PageHeightPt, font),
		[]byte(overlayTextContent))
	w.fill(font, helveticaFont)
	w.fillStream(img, imageDict(ScanImageW, ScanImageH), grayPixels(ScanImageW, ScanImageH, 1))
	return w.finish(cat)
}

func overlayVector() []byte {
	w := newWriter()
	cat, pages, page, cont, img := w.reserve(), w.reserve(), w.reserve(), w.reserve(), w.reserve()
	w.fill(cat, fmt.Sprintf("<< /Type /Catalog /Pages %d 0 R >>", pages))
	w.fill(pages, fmt.Sprintf("<< /Type /Pages /Kids [%d 0 R] /Count 1 >>", page))
	w.fill(page, fmt.Sprintf("<< /Type /Page /Parent %d 0 R /MediaBox [0 0 %d %d]"+
		" /Resources << /XObject << /Im0 %d 0 R >> >> /Contents %d 0 R >>",
		pages, PageWidthPt, PageHeightPt, img, cont))
	w.fillStream(cont, "", []byte(fmt.Sprintf(
		"q %d 0 0 %d 0 0 cm /Im0 Do Q\nq 0 0 0 RG 2 w 72 72 468 648 re S Q\n",
		PageWidthPt, PageHeightPt)))
	w.fillStream(img, imageDict(ScanImageW, ScanImageH), grayPixels(ScanImageW, ScanImageH, 1))
	return w.finish(cat)
}

func mixed() []byte {
	w := newWriter()
	cat, pages := w.reserve(), w.reserve()
	p1, c1, font := w.reserve(), w.reserve(), w.reserve()
	p2, c2, img := w.reserve(), w.reserve(), w.reserve()
	w.fill(cat, fmt.Sprintf("<< /Type /Catalog /Pages %d 0 R >>", pages))
	w.fill(pages, fmt.Sprintf("<< /Type /Pages /Kids [%d 0 R %d 0 R] /Count 2 >>", p1, p2))
	w.fill(p1, fmt.Sprintf("<< /Type /Page /Parent %d 0 R /MediaBox [0 0 %d %d]"+
		" /Resources << /Font << /F1 %d 0 R >> >> /Contents %d 0 R >>",
		pages, PageWidthPt, PageHeightPt, font, c1))
	w.fillStream(c1, "", []byte(bornDigitalContent))
	w.fill(font, helveticaFont)
	w.fill(p2, fmt.Sprintf("<< /Type /Page /Parent %d 0 R /MediaBox [0 0 %d %d]"+
		" /Resources << /XObject << /Im0 %d 0 R >> >> /Contents %d 0 R >>",
		pages, PageWidthPt, PageHeightPt, img, c2))
	w.fillStream(c2, "", []byte(fmt.Sprintf("q %d 0 0 %d 0 0 cm /Im0 Do Q\n", PageWidthPt, PageHeightPt)))
	w.fillStream(img, imageDict(ScanImageW, ScanImageH), grayPixels(ScanImageW, ScanImageH, 1))
	return w.finish(cat)
}

// dupRaster gives two pages the same raster bytes as two distinct objects.
// pdfcpu deduplicates byte-identical image XObjects in its optimize pass, so
// an extraction path that asks pdfcpu "which image objects are on page 2?"
// gets page 1's object number back and then cannot find it in the page's own
// resource dictionary. Task 8 resolves the image through the page's resources
// instead; this document is the regression guard for that decision, and the
// duplex-scanner blank-back-page case in its own right.
func dupRaster() []byte {
	w := newWriter()
	cat, pages := w.reserve(), w.reserve()
	p1, c1, i1 := w.reserve(), w.reserve(), w.reserve()
	p2, c2, i2 := w.reserve(), w.reserve(), w.reserve()
	body := fmt.Sprintf("q %d 0 0 %d 0 0 cm /Im0 Do Q\n", PageWidthPt, PageHeightPt)
	px := grayPixels(ScanImageW, ScanImageH, 1)
	w.fill(cat, fmt.Sprintf("<< /Type /Catalog /Pages %d 0 R >>", pages))
	w.fill(pages, fmt.Sprintf("<< /Type /Pages /Kids [%d 0 R %d 0 R] /Count 2 >>", p1, p2))
	w.fill(p1, fmt.Sprintf("<< /Type /Page /Parent %d 0 R /MediaBox [0 0 %d %d]"+
		" /Resources << /XObject << /Im0 %d 0 R >> >> /Contents %d 0 R >>",
		pages, PageWidthPt, PageHeightPt, i1, c1))
	w.fillStream(c1, "", []byte(body))
	w.fillStream(i1, imageDict(ScanImageW, ScanImageH), px)
	w.fill(p2, fmt.Sprintf("<< /Type /Page /Parent %d 0 R /MediaBox [0 0 %d %d]"+
		" /Resources << /XObject << /Im0 %d 0 R >> >> /Contents %d 0 R >>",
		pages, PageWidthPt, PageHeightPt, i2, c2))
	w.fillStream(c2, "", []byte(body))
	w.fillStream(i2, imageDict(ScanImageW, ScanImageH), px)
	return w.finish(cat)
}

// jbig2Payload is 64 bytes of deterministic filler. It is NOT a valid JBIG2
// segment stream and does not need to be: nothing in Byblos or in poppler
// decodes it. What matters is that the /Filter says JBIG2Decode, because that
// is what drives pdfcpu to hand back opaque bytes instead of an error.
func jbig2Payload() []byte {
	p := make([]byte, 64)
	for i := range p {
		p[i] = byte((i*13 + 7) % 251)
	}
	return p
}

// jbig2 is a page-covering 1-bpc raster stored with /Filter /JBIG2Decode. It
// is the corpus's only bitonal image (ImageRef.Bitonal true) and its only
// undecodable codec (ErrUnsupportedImageCodec).
//
// Known gap: no corpus document sets /ImageMask true, the other disjunct of
// ImageRef.Bitonal. A stencil mask is not extractable at all — pdfcpu's
// ExtractImage rejects it with "invalid components/bpc 0/1" — so covering that
// disjunct would mean adding a document that can only ever produce a failure.
// It belongs with the real-world-scans follow-up, not here.
func jbig2() []byte {
	w := newWriter()
	cat, pages, page, cont, img := w.reserve(), w.reserve(), w.reserve(), w.reserve(), w.reserve()
	w.fill(cat, fmt.Sprintf("<< /Type /Catalog /Pages %d 0 R >>", pages))
	w.fill(pages, fmt.Sprintf("<< /Type /Pages /Kids [%d 0 R] /Count 1 >>", page))
	w.fill(page, fmt.Sprintf("<< /Type /Page /Parent %d 0 R /MediaBox [0 0 %d %d]"+
		" /Resources << /XObject << /Im0 %d 0 R >> >> /Contents %d 0 R >>",
		pages, PageWidthPt, PageHeightPt, img, cont))
	w.fillStream(cont, "", []byte(fmt.Sprintf("q %d 0 0 %d 0 0 cm /Im0 Do Q\n", PageWidthPt, PageHeightPt)))
	w.fillRawStream(img, fmt.Sprintf("/Type /XObject /Subtype /Image /Width %d /Height %d"+
		" /ColorSpace /DeviceGray /BitsPerComponent 1 /Filter /JBIG2Decode", ScanImageW, ScanImageH),
		jbig2Payload())
	return w.finish(cat)
}

// malformed truncates the scan document mid-body, which is what a partial
// upload or a truncated S3 object looks like: a plausible header, a broken
// stream, and no cross-reference table.
func malformed() []byte {
	full := scan(0)
	return full[:len(full)*6/10]
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/corpus/ -v`
Expected: PASS, including all ten `TestXrefOffsetsPointAtTheirObjects` subtests (eleven documents, `malformed` skipped).

- [ ] **Step 5: Add the corpus-writing command**

Create `cmd/byblos-corpus/main.go`:

```go
// Command byblos-corpus writes the generated test corpus to a directory. The
// tests build the same documents in memory; this exists so the poppler oracle
// tooling has files to run against. The output directory is gitignored.
package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/dobbo-ca/byblos/internal/corpus"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: byblos-corpus <outdir>")
		os.Exit(2)
	}
	dir := os.Args[1]
	if err := os.MkdirAll(dir, 0o755); err != nil {
		fmt.Fprintln(os.Stderr, "byblos-corpus:", err)
		os.Exit(1)
	}
	for _, d := range corpus.All() {
		path := filepath.Join(dir, d.Name+".pdf")
		if err := os.WriteFile(path, d.Data, 0o644); err != nil {
			fmt.Fprintln(os.Stderr, "byblos-corpus:", err)
			os.Exit(1)
		}
		fmt.Printf("%-16s %7d bytes  %s\n", d.Name, len(d.Data), d.Desc)
	}
}
```

- [ ] **Step 6: Generate the corpus and eyeball it against an external reader**

```bash
make corpus
ls -l testdata/corpus/
```

Expected: eleven `.pdf` files. If poppler is installed, confirm the generator produces PDFs a real reader accepts:

```bash
for f in testdata/corpus/*.pdf; do echo "== $f"; pdfinfo "$f" 2>&1 | grep -E '^(Pages|Page size)'; done
pdfimages -list testdata/corpus/jbig2.pdf
```

Expected: every file except `malformed.pdf` reports a page count and a `612 x 792 pts` page size (`mixed.pdf` and `dup-raster.pdf` report 2 pages, the rest 1). `pdfimages -list` on `jbig2.pdf` reports one `306 x 396` image with `bpc` **1** and `enc` **jbig2** — verified against poppler 26.06.0. **`malformed.pdf` must produce an error.** If poppler *accepts* `malformed.pdf`, truncate harder — change the `6/10` in `malformed()` to `3/10` and re-run until poppler rejects it, then record the fraction in the function comment. If poppler is not installed, skip this step; Task 8 verifies the same thing through pdfcpu.

- [ ] **Step 7: Commit**

```bash
git add internal/corpus cmd/byblos-corpus
git commit -m "feat(corpus): generate the PDF test corpus from committed Go code"
```

---

## Task 6: PDF content-stream lexer

pdfcpu decodes content streams but does **not** tokenize them — its `scan` package is a line scanner, nothing more. Verified against v0.13.0. This lexer is the missing piece, and it is the single largest build cost of using pdfcpu as substrate.

**Files:**
- Create: `internal/content/lexer.go`
- Test: `internal/content/lexer_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces:

```go
type Kind uint8

const (
    KindNumber Kind = iota
    KindString
    KindName
    KindArrayOpen
    KindArrayClose
    KindDictOpen
    KindDictClose
    KindKeyword
    KindInlineImage
)

type Token struct {
    Kind Kind
    Num  float64
    Text []byte
}

type Lexer struct{ /* ... */ }
func NewLexer(src []byte) *Lexer
func (l *Lexer) Next() (Token, error) // io.EOF at end of input
```

- [ ] **Step 1: Write the failing test**

Create `internal/content/lexer_test.go`:

```go
package content

import (
	"errors"
	"io"
	"testing"
)

// lex drains a lexer, failing the test on any error other than io.EOF.
func lex(t *testing.T, src string) []Token {
	t.Helper()
	l := NewLexer([]byte(src))
	var out []Token
	for {
		tok, err := l.Next()
		if errors.Is(err, io.EOF) {
			return out
		}
		if err != nil {
			t.Fatalf("Next() error = %v after %d tokens", err, len(out))
		}
		out = append(out, tok)
	}
}

func TestLexerOperatorSequence(t *testing.T) {
	got := lex(t, "q 612 0 0 792 0 0 cm /Im0 Do Q")
	want := []struct {
		kind Kind
		num  float64
		text string
	}{
		{KindKeyword, 0, "q"},
		{KindNumber, 612, ""},
		{KindNumber, 0, ""},
		{KindNumber, 0, ""},
		{KindNumber, 792, ""},
		{KindNumber, 0, ""},
		{KindNumber, 0, ""},
		{KindKeyword, 0, "cm"},
		{KindName, 0, "Im0"},
		{KindKeyword, 0, "Do"},
		{KindKeyword, 0, "Q"},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d tokens, want %d: %+v", len(got), len(want), got)
	}
	for i, w := range want {
		if got[i].Kind != w.kind || got[i].Num != w.num || string(got[i].Text) != w.text {
			t.Errorf("token %d = {%v %v %q}; want {%v %v %q}",
				i, got[i].Kind, got[i].Num, got[i].Text, w.kind, w.num, w.text)
		}
	}
}

func TestLexerNumbers(t *testing.T) {
	got := lex(t, "-3 .5 4. +2 0.000")
	want := []float64{-3, 0.5, 4, 2, 0}
	if len(got) != len(want) {
		t.Fatalf("got %d tokens, want %d", len(got), len(want))
	}
	for i, w := range want {
		if got[i].Kind != KindNumber || got[i].Num != w {
			t.Errorf("token %d = {%v %v}; want number %v", i, got[i].Kind, got[i].Num, w)
		}
	}
}

// Real-world content streams contain malformed reals. Viewers take the longest
// valid prefix rather than abandoning the page; so do we.
func TestLexerMalformedRealTakesLongestValidPrefix(t *testing.T) {
	got := lex(t, "1.-2 cm")
	if len(got) != 2 || got[0].Kind != KindNumber || got[0].Num != 1 {
		t.Fatalf("got %+v; want [number 1, keyword cm]", got)
	}
}

func TestLexerLiteralStringEscapes(t *testing.T) {
	got := lex(t, `(a\(b\)c\n\101\\z) Tj`)
	if len(got) != 2 || got[0].Kind != KindString {
		t.Fatalf("got %+v; want [string, keyword]", got)
	}
	if want := "a(b)c\nA\\z"; string(got[0].Text) != want {
		t.Errorf("string = %q; want %q", got[0].Text, want)
	}
}

func TestLexerLiteralStringNestedParens(t *testing.T) {
	got := lex(t, "((nested) ok) Tj")
	if len(got) != 2 || string(got[0].Text) != "(nested) ok" {
		t.Fatalf("got %+v; want the nested parens preserved", got)
	}
}

// A backslash before a newline is a line continuation and contributes nothing.
func TestLexerLiteralStringLineContinuation(t *testing.T) {
	got := lex(t, "(ab\\\ncd) Tj")
	if len(got) != 2 || string(got[0].Text) != "abcd" {
		t.Fatalf("string = %q; want \"abcd\"", got[0].Text)
	}
}

// An odd number of hex digits is padded with a trailing zero (ISO 32000-1
// section 7.3.4.3).
func TestLexerHexStringOddDigitCount(t *testing.T) {
	got := lex(t, "<4869 7> Tj")
	if len(got) != 2 || got[0].Kind != KindString {
		t.Fatalf("got %+v; want [string, keyword]", got)
	}
	if want := []byte{0x48, 0x69, 0x70}; string(got[0].Text) != string(want) {
		t.Errorf("string = % 02x; want % 02x", got[0].Text, want)
	}
}

func TestLexerDictAndArrayDelimiters(t *testing.T) {
	got := lex(t, "<< /A [1 2] >> BDC")
	kinds := []Kind{KindDictOpen, KindName, KindArrayOpen, KindNumber, KindNumber, KindArrayClose, KindDictClose, KindKeyword}
	if len(got) != len(kinds) {
		t.Fatalf("got %d tokens, want %d: %+v", len(got), len(kinds), got)
	}
	for i, k := range kinds {
		if got[i].Kind != k {
			t.Errorf("token %d kind = %v; want %v", i, got[i].Kind, k)
		}
	}
}

func TestLexerNameHexEscape(t *testing.T) {
	got := lex(t, "/A#20B Do")
	if len(got) != 2 || got[0].Kind != KindName || string(got[0].Text) != "A B" {
		t.Fatalf("got %+v; want name \"A B\"", got)
	}
}

func TestLexerSkipsComments(t *testing.T) {
	got := lex(t, "% a comment with ( unbalanced\nq % trailing\nQ")
	if len(got) != 2 || string(got[0].Text) != "q" || string(got[1].Text) != "Q" {
		t.Fatalf("got %+v; want [q, Q]", got)
	}
}

// An inline image's binary payload must never be tokenized: it can contain any
// byte sequence, including things that look like operators.
func TestLexerInlineImageIsOneToken(t *testing.T) {
	src := "BI /W 2 /H 2 /BPC 8 /CS /G ID \x00q Q(\xff EI Q"
	got := lex(t, src)
	if len(got) != 2 {
		t.Fatalf("got %d tokens, want 2: %+v", len(got), got)
	}
	if got[0].Kind != KindInlineImage {
		t.Errorf("token 0 kind = %v; want KindInlineImage", got[0].Kind)
	}
	if got[1].Kind != KindKeyword || string(got[1].Text) != "Q" {
		t.Errorf("token 1 = {%v %q}; want keyword Q", got[1].Kind, got[1].Text)
	}
}

func TestLexerErrors(t *testing.T) {
	for _, tc := range []struct{ name, src string }{
		{"unterminated literal string", "(abc"},
		{"unterminated hex string", "<4869"},
		{"stray close paren", ") Tj"},
		{"inline image without EI", "BI /W 1 ID \x00\x01"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			l := NewLexer([]byte(tc.src))
			for {
				_, err := l.Next()
				if errors.Is(err, io.EOF) {
					t.Fatal("reached EOF without an error")
				}
				if err != nil {
					return
				}
			}
		})
	}
}

// The lexer must always make progress, or a malformed stream becomes a hang.
func TestLexerAlwaysAdvances(t *testing.T) {
	l := NewLexer([]byte("q\x00\x00 Q"))
	prev := -1
	for i := 0; i < 100; i++ {
		_, err := l.Next()
		if err != nil {
			return
		}
		if l.pos <= prev {
			t.Fatalf("lexer did not advance past offset %d", prev)
		}
		prev = l.pos
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/content/ -v`
Expected: FAIL — `undefined: NewLexer`.

- [ ] **Step 3: Implement the lexer**

Create `internal/content/lexer.go`:

```go
// Package content lexes and walks PDF content streams.
//
// pdfcpu decodes content streams but does not tokenize them, so Byblos needs
// its own operator-level parser. It is the only way to tell a clean
// page-covering scan from a page carrying an overlay (design spec section 2):
// an image count alone cannot, because an overlay commonly lives inside a Form
// XObject that the page's image count never sees.
//
// Syntax follows ISO 32000-1:2008 section 7.2 (lexical conventions) and
// section 8.2 (content streams).
package content

import (
	"fmt"
	"io"
	"strconv"
)

// Kind classifies a content-stream token.
type Kind uint8

const (
	KindNumber Kind = iota
	KindString
	KindName
	KindArrayOpen
	KindArrayClose
	KindDictOpen
	KindDictClose
	KindKeyword     // an operator, or true/false/null
	KindInlineImage // an entire BI ... ID ... EI sequence
)

// Token is one lexed item. Num is meaningful for KindNumber; Text holds the
// name text (without the leading slash), the decoded string bytes, or the
// keyword text. Text aliases the source buffer for keywords, and is freshly
// allocated for names and strings.
type Token struct {
	Kind Kind
	Num  float64
	Text []byte
}

// Lexer tokenizes a decoded content stream.
type Lexer struct {
	src []byte
	pos int
}

func NewLexer(src []byte) *Lexer { return &Lexer{src: src} }

func isWhite(c byte) bool {
	return c == 0 || c == '\t' || c == '\n' || c == '\f' || c == '\r' || c == ' '
}

func isDelim(c byte) bool {
	switch c {
	case '(', ')', '<', '>', '[', ']', '{', '}', '/', '%':
		return true
	}
	return false
}

func hexVal(c byte) (byte, bool) {
	switch {
	case c >= '0' && c <= '9':
		return c - '0', true
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10, true
	case c >= 'A' && c <= 'F':
		return c - 'A' + 10, true
	}
	return 0, false
}

func (l *Lexer) skipSpace() {
	for l.pos < len(l.src) {
		c := l.src[l.pos]
		if isWhite(c) {
			l.pos++
			continue
		}
		if c == '%' {
			for l.pos < len(l.src) && l.src[l.pos] != '\n' && l.src[l.pos] != '\r' {
				l.pos++
			}
			continue
		}
		return
	}
}

// Next returns the next token, or io.EOF when the stream is exhausted. Every
// successful call advances the read position, so a caller cannot loop forever.
func (l *Lexer) Next() (Token, error) {
	l.skipSpace()
	if l.pos >= len(l.src) {
		return Token{}, io.EOF
	}
	c := l.src[l.pos]
	switch {
	case c == '[':
		l.pos++
		return Token{Kind: KindArrayOpen}, nil
	case c == ']':
		l.pos++
		return Token{Kind: KindArrayClose}, nil
	case c == '<':
		if l.pos+1 < len(l.src) && l.src[l.pos+1] == '<' {
			l.pos += 2
			return Token{Kind: KindDictOpen}, nil
		}
		return l.hexString()
	case c == '>':
		if l.pos+1 < len(l.src) && l.src[l.pos+1] == '>' {
			l.pos += 2
			return Token{Kind: KindDictClose}, nil
		}
		return Token{}, fmt.Errorf("content: stray '>' at offset %d", l.pos)
	case c == '(':
		return l.literalString()
	case c == ')':
		return Token{}, fmt.Errorf("content: stray ')' at offset %d", l.pos)
	case c == '/':
		return l.name()
	case c == '+' || c == '-' || c == '.' || (c >= '0' && c <= '9'):
		return l.number()
	case c == '{' || c == '}':
		// Type 4 function braces never occur in a page content stream; report
		// them as keywords so the walker can treat the page as unclassifiable.
		l.pos++
		return Token{Kind: KindKeyword, Text: l.src[l.pos-1 : l.pos]}, nil
	default:
		return l.keyword()
	}
}

func (l *Lexer) keyword() (Token, error) {
	start := l.pos
	for l.pos < len(l.src) && !isWhite(l.src[l.pos]) && !isDelim(l.src[l.pos]) {
		l.pos++
	}
	if l.pos == start {
		l.pos++ // guarantee progress
		return Token{}, fmt.Errorf("content: unexpected byte %q at offset %d", l.src[start], start)
	}
	kw := l.src[start:l.pos]
	if string(kw) == "BI" {
		return l.inlineImage(start)
	}
	return Token{Kind: KindKeyword, Text: kw}, nil
}

func (l *Lexer) number() (Token, error) {
	start := l.pos
	if c := l.src[l.pos]; c == '+' || c == '-' {
		l.pos++
	}
	for l.pos < len(l.src) {
		c := l.src[l.pos]
		if (c >= '0' && c <= '9') || c == '.' || c == '-' || c == '+' {
			l.pos++
			continue
		}
		break
	}
	text := string(l.src[start:l.pos])
	v, err := strconv.ParseFloat(text, 64)
	if err != nil {
		v = parsePrefixFloat(text)
	}
	return Token{Kind: KindNumber, Num: v}, nil
}

// parsePrefixFloat returns the value of the longest parseable prefix of s, or
// 0. Real content streams carry malformed reals such as "1.-2"; viewers keep
// rendering, and a page is not worth abandoning over one bad operand.
func parsePrefixFloat(s string) float64 {
	for i := len(s); i > 0; i-- {
		if v, err := strconv.ParseFloat(s[:i], 64); err == nil {
			return v
		}
	}
	return 0
}

func (l *Lexer) name() (Token, error) {
	l.pos++ // consume '/'
	var out []byte
	for l.pos < len(l.src) {
		c := l.src[l.pos]
		if isWhite(c) || isDelim(c) {
			break
		}
		if c == '#' && l.pos+2 < len(l.src) {
			hi, ok1 := hexVal(l.src[l.pos+1])
			lo, ok2 := hexVal(l.src[l.pos+2])
			if ok1 && ok2 {
				out = append(out, hi<<4|lo)
				l.pos += 3
				continue
			}
		}
		out = append(out, c)
		l.pos++
	}
	return Token{Kind: KindName, Text: out}, nil
}

func (l *Lexer) literalString() (Token, error) {
	start := l.pos
	l.pos++ // consume '('
	depth := 1
	var out []byte
	for l.pos < len(l.src) {
		c := l.src[l.pos]
		l.pos++
		switch c {
		case '\\':
			if l.pos >= len(l.src) {
				return Token{}, fmt.Errorf("content: string at offset %d ends in a backslash", start)
			}
			e := l.src[l.pos]
			l.pos++
			switch e {
			case 'n':
				out = append(out, '\n')
			case 'r':
				out = append(out, '\r')
			case 't':
				out = append(out, '\t')
			case 'b':
				out = append(out, '\b')
			case 'f':
				out = append(out, '\f')
			case '(', ')', '\\':
				out = append(out, e)
			case '\r':
				if l.pos < len(l.src) && l.src[l.pos] == '\n' {
					l.pos++
				}
			case '\n':
				// line continuation: contributes nothing
			default:
				if e >= '0' && e <= '7' {
					v := int(e - '0')
					for k := 0; k < 2 && l.pos < len(l.src); k++ {
						d := l.src[l.pos]
						if d < '0' || d > '7' {
							break
						}
						v = v*8 + int(d-'0')
						l.pos++
					}
					out = append(out, byte(v))
				} else {
					out = append(out, e)
				}
			}
		case '(':
			depth++
			out = append(out, c)
		case ')':
			depth--
			if depth == 0 {
				return Token{Kind: KindString, Text: out}, nil
			}
			out = append(out, c)
		default:
			out = append(out, c)
		}
	}
	return Token{}, fmt.Errorf("content: unterminated literal string at offset %d", start)
}

func (l *Lexer) hexString() (Token, error) {
	start := l.pos
	l.pos++ // consume '<'
	var out []byte
	var cur byte
	var half bool
	for l.pos < len(l.src) {
		c := l.src[l.pos]
		l.pos++
		if c == '>' {
			if half {
				out = append(out, cur<<4) // an odd digit count pads with zero
			}
			return Token{Kind: KindString, Text: out}, nil
		}
		v, ok := hexVal(c)
		if !ok {
			continue // whitespace and stray bytes are ignored
		}
		if half {
			out = append(out, cur<<4|v)
			half = false
		} else {
			cur = v
			half = true
		}
	}
	return Token{}, fmt.Errorf("content: unterminated hex string at offset %d", start)
}

// inlineImage consumes BI ... ID <binary> EI as a single token. The dictionary
// and sample data are not decoded: the walker only needs to know that an inline
// image is present. The payload is skipped verbatim because it may contain any
// byte sequence, including text that looks like operators.
func (l *Lexer) inlineImage(start int) (Token, error) {
	if !l.seekKeyword("ID") {
		return Token{}, fmt.Errorf("content: inline image at offset %d has no ID", start)
	}
	if l.pos < len(l.src) && isWhite(l.src[l.pos]) {
		l.pos++ // exactly one whitespace byte separates ID from the samples
	}
	for l.pos+1 < len(l.src) {
		if l.src[l.pos] == 'E' && l.src[l.pos+1] == 'I' &&
			l.pos > 0 && isWhite(l.src[l.pos-1]) &&
			(l.pos+2 == len(l.src) || isWhite(l.src[l.pos+2]) || isDelim(l.src[l.pos+2])) {
			l.pos += 2
			return Token{Kind: KindInlineImage}, nil
		}
		l.pos++
	}
	l.pos = len(l.src)
	return Token{}, fmt.Errorf("content: inline image at offset %d has no EI", start)
}

// seekKeyword advances past the next standalone occurrence of kw.
func (l *Lexer) seekKeyword(kw string) bool {
	for l.pos+len(kw) <= len(l.src) {
		if string(l.src[l.pos:l.pos+len(kw)]) == kw &&
			(l.pos == 0 || isWhite(l.src[l.pos-1]) || isDelim(l.src[l.pos-1])) &&
			(l.pos+len(kw) == len(l.src) || isWhite(l.src[l.pos+len(kw)]) || isDelim(l.src[l.pos+len(kw)])) {
			l.pos += len(kw)
			return true
		}
		l.pos++
	}
	return false
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/content/ -v`
Expected: PASS, all fourteen tests including the four `TestLexerErrors` subtests.

- [ ] **Step 5: Fuzz it briefly — a lexer that hangs or panics on hostile input is a denial of service**

Add to `internal/content/lexer_test.go`:

```go
func FuzzLexer(f *testing.F) {
	f.Add("q 612 0 0 792 0 0 cm /Im0 Do Q")
	f.Add("BT (a) Tj ET")
	f.Add("BI /W 1 ID \x00 EI")
	f.Add("<< /A [1 2] >> BDC")
	f.Fuzz(func(t *testing.T, s string) {
		l := NewLexer([]byte(s))
		for i := 0; i <= len(s)+1; i++ {
			if _, err := l.Next(); err != nil {
				return
			}
		}
		t.Fatalf("lexer produced more tokens than input bytes for %q", s)
	})
}
```

```bash
go test ./internal/content/ -run FuzzLexer -fuzz FuzzLexer -fuzztime 30s
```

Expected: no crashers. Any failure is a real bug — fix it and add the crasher to `testdata/fuzz/` (which `go test` commits automatically).

- [ ] **Step 6: Commit**

```bash
git add internal/content
git commit -m "feat(content): add the PDF content-stream lexer"
```

---

## Task 7: Graphics-state walker

Turns a token stream into the facts classification needs: where each image XObject was painted, how many characters were shown, and whether anything other than an image was drawn. It recurses into Form XObjects, because that is where overlays hide.

**Files:**
- Create: `internal/content/walk.go`
- Test: `internal/content/walk_test.go`

**Interfaces:**
- Consumes: `Lexer` (Task 6).
- Produces:

```go
type Matrix [6]float64
var Identity = Matrix{1, 0, 0, 1, 0, 0}
func (m Matrix) Mul(n Matrix) Matrix
func (m Matrix) Apply(x, y float64) (float64, float64)
func (m Matrix) UnitSquareBox() Box

type Box struct{ LLX, LLY, URX, URY float64 }

type XObject struct {
    Image   bool
    ID      int
    Content []byte
    Matrix  Matrix
    Scope   int
}

// Env resolves XObject resource names. Scopes are opaque handles into the
// caller's resource tree.
type Env interface {
    XObject(scope int, name string) (XObject, bool)
}

type Placement struct {
    Name string
    ID   int
    CTM  Matrix
    Box  Box
}

type Scan struct {
    Images     []Placement
    TextChars  int
    TextOps    int
    PaintOps   int
    ShadingOps int
    InlineImgs int
    Unresolved []string
}

func Walk(src []byte, scope int, env Env) (*Scan, error)
```

- [ ] **Step 1: Write the failing test**

Create `internal/content/walk_test.go`:

```go
package content

import (
	"math"
	"strings"
	"testing"
)

// mapEnv is a fake resource tree: one map of name to XObject per scope.
type mapEnv []map[string]XObject

func (e mapEnv) XObject(scope int, name string) (XObject, bool) {
	if scope < 0 || scope >= len(e) {
		return XObject{}, false
	}
	xo, ok := e[scope][name]
	return xo, ok
}

func imageEnv(id int) mapEnv {
	return mapEnv{{"Im0": {Image: true, ID: id}}}
}

func boxEq(t *testing.T, got Box, llx, lly, urx, ury float64) {
	t.Helper()
	const eps = 1e-9
	if math.Abs(got.LLX-llx) > eps || math.Abs(got.LLY-lly) > eps ||
		math.Abs(got.URX-urx) > eps || math.Abs(got.URY-ury) > eps {
		t.Errorf("box = %+v; want {%g %g %g %g}", got, llx, lly, urx, ury)
	}
}

func TestMatrixMulIsApplyThenApply(t *testing.T) {
	scale := Matrix{2, 0, 0, 2, 0, 0}
	move := Matrix{1, 0, 0, 1, 5, 5}
	// move first, then scale: (1,1) -> (6,6) -> (12,12)
	got := move.Mul(scale)
	x, y := got.Apply(1, 1)
	if x != 12 || y != 12 {
		t.Errorf("move.Mul(scale).Apply(1,1) = (%g, %g); want (12, 12)", x, y)
	}
}

func TestMatrixUnitSquareBoxHandlesNegativeScale(t *testing.T) {
	// A y-flip: the unit square maps to y in [-1, 0].
	boxEq(t, Matrix{1, 0, 0, -1, 0, 0}.UnitSquareBox(), 0, -1, 1, 0)
}

func TestWalkSingleImagePlacement(t *testing.T) {
	s, err := Walk([]byte("q 612 0 0 792 0 0 cm /Im0 Do Q"), 0, imageEnv(7))
	if err != nil {
		t.Fatalf("Walk() error = %v", err)
	}
	if len(s.Images) != 1 {
		t.Fatalf("Images = %+v; want exactly one", s.Images)
	}
	if s.Images[0].ID != 7 || s.Images[0].Name != "Im0" {
		t.Errorf("placement = %+v; want ID 7, name Im0", s.Images[0])
	}
	boxEq(t, s.Images[0].Box, 0, 0, 612, 792)
}

func TestWalkNestedCTM(t *testing.T) {
	s, err := Walk([]byte("q 2 0 0 2 10 10 cm q 1 0 0 1 5 5 cm /Im0 Do Q Q"), 0, imageEnv(1))
	if err != nil {
		t.Fatalf("Walk() error = %v", err)
	}
	if len(s.Images) != 1 {
		t.Fatalf("Images = %+v; want exactly one", s.Images)
	}
	// inner translate(5,5) composed under outer scale(2) translate(10,10)
	boxEq(t, s.Images[0].Box, 20, 20, 22, 22)
}

func TestWalkQRestoresTheCTM(t *testing.T) {
	src := "q 10 0 0 10 0 0 cm /Im0 Do Q /Im0 Do"
	s, err := Walk([]byte(src), 0, imageEnv(1))
	if err != nil {
		t.Fatalf("Walk() error = %v", err)
	}
	if len(s.Images) != 2 {
		t.Fatalf("Images = %+v; want two", s.Images)
	}
	boxEq(t, s.Images[0].Box, 0, 0, 10, 10)
	boxEq(t, s.Images[1].Box, 0, 0, 1, 1)
}

// An unbalanced Q must not panic or corrupt the CTM stack.
func TestWalkUnbalancedRestore(t *testing.T) {
	s, err := Walk([]byte("Q Q q 5 0 0 5 0 0 cm /Im0 Do Q Q Q"), 0, imageEnv(1))
	if err != nil {
		t.Fatalf("Walk() error = %v", err)
	}
	if len(s.Images) != 1 {
		t.Fatalf("Images = %+v; want one", s.Images)
	}
	boxEq(t, s.Images[0].Box, 0, 0, 5, 5)
}

func TestWalkCountsShownCharacters(t *testing.T) {
	src := "BT /F1 12 Tf (Hello) Tj [ (wor) -120 (ld) ] TJ ET"
	s, err := Walk([]byte(src), 0, mapEnv{{}})
	if err != nil {
		t.Fatalf("Walk() error = %v", err)
	}
	if s.TextChars != 10 {
		t.Errorf("TextChars = %d; want 10", s.TextChars)
	}
	if s.TextOps != 2 {
		t.Errorf("TextOps = %d; want 2", s.TextOps)
	}
}

// The quote operators show text too, and the double-quote form takes two
// numeric operands before the string.
func TestWalkCountsQuoteOperators(t *testing.T) {
	s, err := Walk([]byte("BT (ab) ' 1 2 (cde) \" ET"), 0, mapEnv{{}})
	if err != nil {
		t.Fatalf("Walk() error = %v", err)
	}
	if s.TextChars != 5 || s.TextOps != 2 {
		t.Errorf("TextChars = %d, TextOps = %d; want 5, 2", s.TextChars, s.TextOps)
	}
}

func TestWalkRecursesIntoFormXObjects(t *testing.T) {
	env := mapEnv{
		{"Fm0": {Content: []byte("q 100 0 0 100 0 0 cm /Im0 Do Q"), Matrix: Matrix{2, 0, 0, 2, 0, 0}, Scope: 1}},
		{"Im0": {Image: true, ID: 42}},
	}
	s, err := Walk([]byte("q 0.5 0 0 0.5 0 0 cm /Fm0 Do Q"), 0, env)
	if err != nil {
		t.Fatalf("Walk() error = %v", err)
	}
	if len(s.Images) != 1 || s.Images[0].ID != 42 {
		t.Fatalf("Images = %+v; want one placement of ID 42", s.Images)
	}
	// form /Matrix scale(2) under page CTM scale(0.5) is the identity.
	boxEq(t, s.Images[0].Box, 0, 0, 100, 100)
}

// This is the regression the whole classification design rests on: text inside
// a form must be seen, because the page's own image count cannot see it.
func TestWalkSeesTextInsideAForm(t *testing.T) {
	env := mapEnv{
		{
			"Im0": {Image: true, ID: 1},
			"Fm0": {Content: []byte("BT (Scanned 2026-07-27) Tj ET"), Matrix: Identity, Scope: 1},
		},
		{},
	}
	s, err := Walk([]byte("q 612 0 0 792 0 0 cm /Im0 Do Q q /Fm0 Do Q"), 0, env)
	if err != nil {
		t.Fatalf("Walk() error = %v", err)
	}
	if len(s.Images) != 1 {
		t.Errorf("Images = %+v; want one", s.Images)
	}
	if s.TextChars != 18 || s.TextOps != 1 {
		t.Errorf("TextChars = %d, TextOps = %d; want 18, 1", s.TextChars, s.TextOps)
	}
}

func TestWalkRejectsUnboundedFormRecursion(t *testing.T) {
	env := mapEnv{{"Fm0": {Content: []byte("/Fm0 Do"), Matrix: Identity, Scope: 0}}}
	_, err := Walk([]byte("/Fm0 Do"), 0, env)
	if err == nil {
		t.Fatal("Walk() on a self-referencing form: want an error, got nil")
	}
	if !strings.Contains(err.Error(), "nesting") {
		t.Errorf("error = %v; want it to mention nesting", err)
	}
}

func TestWalkCountsPaintingOperators(t *testing.T) {
	for _, tc := range []struct {
		name string
		src  string
		want int
	}{
		{"stroke", "72 72 468 648 re S", 1},
		{"fill", "0 0 m 10 10 l f", 1},
		{"even-odd fill", "0 0 10 10 re f*", 1},
		{"close and stroke", "0 0 m 10 10 l b", 1},
		{"clip then no-op paint is not painting", "0 0 10 10 re W n", 0},
		{"construction alone is not painting", "0 0 m 10 10 l 20 20 l h", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, err := Walk([]byte(tc.src), 0, mapEnv{{}})
			if err != nil {
				t.Fatalf("Walk() error = %v", err)
			}
			if s.PaintOps != tc.want {
				t.Errorf("PaintOps = %d; want %d", s.PaintOps, tc.want)
			}
		})
	}
}

func TestWalkCountsShadingAndInlineImages(t *testing.T) {
	s, err := Walk([]byte("/Sh0 sh BI /W 1 /H 1 /BPC 8 /CS /G ID \x00 EI"), 0, mapEnv{{}})
	if err != nil {
		t.Fatalf("Walk() error = %v", err)
	}
	if s.ShadingOps != 1 {
		t.Errorf("ShadingOps = %d; want 1", s.ShadingOps)
	}
	if s.InlineImgs != 1 {
		t.Errorf("InlineImgs = %d; want 1", s.InlineImgs)
	}
}

func TestWalkRecordsUnresolvedXObjectNames(t *testing.T) {
	s, err := Walk([]byte("/Missing Do"), 0, mapEnv{{}})
	if err != nil {
		t.Fatalf("Walk() error = %v", err)
	}
	if len(s.Unresolved) != 1 || s.Unresolved[0] != "Missing" {
		t.Errorf("Unresolved = %v; want [Missing]", s.Unresolved)
	}
}

func TestWalkPropagatesLexerErrors(t *testing.T) {
	if _, err := Walk([]byte("(unterminated"), 0, mapEnv{{}}); err == nil {
		t.Fatal("Walk() on an unterminated string: want an error, got nil")
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/content/ -run 'TestWalk|TestMatrix' -v`
Expected: FAIL — `undefined: Walk`.

- [ ] **Step 3: Implement the walker**

Create `internal/content/walk.go`:

```go
package content

import (
	"errors"
	"fmt"
	"io"
)

// Matrix is a PDF transformation matrix [a b c d e f] in the row-vector
// convention of ISO 32000-1 section 8.3.3:
//
//	[ a b 0 ]
//	[ c d 0 ]
//	[ e f 1 ]
type Matrix [6]float64

// Identity is the identity transform.
var Identity = Matrix{1, 0, 0, 1, 0, 0}

// Mul returns the matrix that applies m first and then n.
func (m Matrix) Mul(n Matrix) Matrix {
	return Matrix{
		m[0]*n[0] + m[1]*n[2],
		m[0]*n[1] + m[1]*n[3],
		m[2]*n[0] + m[3]*n[2],
		m[2]*n[1] + m[3]*n[3],
		m[4]*n[0] + m[5]*n[2] + n[4],
		m[4]*n[1] + m[5]*n[3] + n[5],
	}
}

// Apply maps a point through m.
func (m Matrix) Apply(x, y float64) (float64, float64) {
	return x*m[0] + y*m[2] + m[4], x*m[1] + y*m[3] + m[5]
}

// Box is an axis-aligned rectangle in PDF user space: points, origin
// lower-left, y increasing upward.
type Box struct{ LLX, LLY, URX, URY float64 }

// UnitSquareBox returns the bounding box of the unit square mapped through m.
// An image XObject always occupies the unit square in its own space
// (ISO 32000-1 section 8.9.5.2), so this is exactly where the image lands.
func (m Matrix) UnitSquareBox() Box {
	x0, y0 := m.Apply(0, 0)
	x1, y1 := m.Apply(1, 0)
	x2, y2 := m.Apply(0, 1)
	x3, y3 := m.Apply(1, 1)
	return Box{
		LLX: min(min(x0, x1), min(x2, x3)),
		LLY: min(min(y0, y1), min(y2, y3)),
		URX: max(max(x0, x1), max(x2, x3)),
		URY: max(max(y0, y1), max(y2, y3)),
	}
}

// XObject is a resolved /XObject resource. ID is caller-assigned identity
// echoed back in Placement.ID; pdfdoc uses the PDF object number, so that an
// image named Im0 inside a form is never confused with the page's own Im0.
type XObject struct {
	Image   bool
	ID      int
	Content []byte // form only: the decoded content stream
	Matrix  Matrix // form only: its /Matrix, Identity when absent
	Scope   int    // form only: the scope handle for its own resources
}

// Env resolves XObject resource names encountered during a walk. Scopes are
// opaque handles into the caller's resource tree; the caller chooses the
// numbering and Walk only passes them back.
type Env interface {
	XObject(scope int, name string) (XObject, bool)
}

// Placement is one painting of an image XObject.
type Placement struct {
	Name string // resource name at the point of use, for diagnostics
	ID   int
	CTM  Matrix
	Box  Box
}

// Scan is what a content-stream walk observed, including everything reached
// through Form XObjects.
type Scan struct {
	Images     []Placement
	TextChars  int      // bytes shown by Tj, TJ, ' and "
	TextOps    int      // number of text-showing operators
	PaintOps   int      // path-painting operators; clipping alone does not count
	ShadingOps int      // sh
	InlineImgs int      // BI ... EI
	Unresolved []string // Do operands that did not resolve
}

const (
	// maxFormDepth bounds Form XObject recursion. Real documents nest two or
	// three deep; anything beyond this is malformed or hostile.
	maxFormDepth = 8
	// maxOperands bounds the pending-operand buffer. Only a TJ array comes
	// close; truncating one costs a little TextChars precision on absurd input
	// and nothing else.
	maxOperands = 8192
)

// Walk interprets a decoded content stream, resolving resource names in scope
// through env.
//
// Known simplification: a Form XObject's /BBox clips its content, and Walk
// ignores that clip. A form whose BBox crops an oversized image will therefore
// report an oversized placement. This errs toward accepting a page as
// page-covering; revisit if the divert-rate instrumentation shows it matters.
func Walk(src []byte, scope int, env Env) (*Scan, error) {
	s := &Scan{}
	if err := walk(src, scope, env, Identity, 0, s); err != nil {
		return nil, err
	}
	return s, nil
}

func walk(src []byte, scope int, env Env, ctm Matrix, depth int, s *Scan) error {
	if depth > maxFormDepth {
		return fmt.Errorf("content: form XObject nesting deeper than %d", maxFormDepth)
	}
	l := NewLexer(src)
	var stack []Matrix
	var ops []Token
	for {
		tok, err := l.Next()
		if err != nil {
			// End of stream is the normal exit. Match the sentinel, never the
			// error text.
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		if tok.Kind == KindInlineImage {
			s.InlineImgs++
			ops = ops[:0]
			continue
		}
		if tok.Kind != KindKeyword {
			if len(ops) < maxOperands {
				ops = append(ops, tok)
			}
			continue
		}

		switch string(tok.Text) {
		case "q":
			stack = append(stack, ctm)
		case "Q":
			if n := len(stack); n > 0 {
				ctm = stack[n-1]
				stack = stack[:n-1]
			}
		case "cm":
			if m, ok := matrixOperands(ops); ok {
				ctm = m.Mul(ctm)
			}
		case "Do":
			if err := doXObject(ops, scope, env, ctm, depth, s); err != nil {
				return err
			}
		case "Tj", "'", "\"":
			s.TextOps++
			for i := len(ops) - 1; i >= 0; i-- {
				if ops[i].Kind == KindString {
					s.TextChars += len(ops[i].Text)
					break
				}
			}
		case "TJ":
			s.TextOps++
			for _, o := range ops {
				if o.Kind == KindString {
					s.TextChars += len(o.Text)
				}
			}
		case "S", "s", "f", "F", "f*", "B", "B*", "b", "b*":
			s.PaintOps++
		case "sh":
			s.ShadingOps++
		}
		ops = ops[:0]
	}
}

func doXObject(ops []Token, scope int, env Env, ctm Matrix, depth int, s *Scan) error {
	if len(ops) == 0 || ops[len(ops)-1].Kind != KindName {
		return nil
	}
	name := string(ops[len(ops)-1].Text)
	xo, ok := env.XObject(scope, name)
	if !ok {
		s.Unresolved = append(s.Unresolved, name)
		return nil
	}
	if xo.Image {
		s.Images = append(s.Images, Placement{Name: name, ID: xo.ID, CTM: ctm, Box: ctm.UnitSquareBox()})
		return nil
	}
	return walk(xo.Content, xo.Scope, env, xo.Matrix.Mul(ctm), depth+1, s)
}

// matrixOperands reads the six numbers a cm operator takes.
func matrixOperands(ops []Token) (Matrix, bool) {
	if len(ops) < 6 {
		return Identity, false
	}
	var m Matrix
	for i := 0; i < 6; i++ {
		t := ops[len(ops)-6+i]
		if t.Kind != KindNumber {
			return Identity, false
		}
		m[i] = t.Num
	}
	return m, true
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/content/ -v`
Expected: PASS, including all six `TestWalkCountsPaintingOperators` subtests.

- [ ] **Step 5: Commit**

```bash
git add internal/content/walk.go internal/content/walk_test.go
git commit -m "feat(content): add the graphics-state walker with form recursion"
```

---

## Task 8: The pdfcpu bridge

The only package in Byblos that imports pdfcpu. Design spec §3: "Byblos wraps pdfcpu behind its own interfaces so that replacing it later is a swap rather than a rewrite." Task 8 Step 9 adds the test that keeps that true.

**Files:**
- Create: `internal/pdfdoc/pdfdoc.go`
- Test: `internal/pdfdoc/pdfdoc_test.go`, `arch_test.go` (repo root)

**Interfaces:**
- Consumes: `internal/content` (Task 7), `internal/corpus` (Task 5, tests only).
- Produces:

```go
type Rect struct{ LLX, LLY, URX, URY float64 }

type ImageInfo struct {
    Name          string
    ObjNr         int
    Width, Height int
    BPC           int
    ImageMask     bool
}

type Page struct {
    Index    int
    MediaBox Rect
    CropBox  Rect
    Rotate   int
    Content  []byte
    Scope    int
}

type Doc interface {
    PageCount() int
    Page(n int) (*Page, error)
    XObject(scope int, name string) (content.XObject, bool) // implements content.Env
    ImageInfo(id int) (ImageInfo, bool)
    RawImage(id int) (data []byte, fileType string, err error)
}

var ErrUnsupportedCodec = errors.New("byblos/pdfdoc: image codec cannot be rendered")

func Open(rs io.ReadSeeker) (Doc, error)
```

**`RawImage` takes the id `XObject` returned, not a page and an object number.** That is deliberate and load-bearing: the id identifies an image the caller already resolved through the page's own resource dictionary, so `RawImage` renders the stream dictionary it already holds. Asking pdfcpu "which image objects are on page N?" instead would go through pdfcpu's optimize pass, which **deduplicates byte-identical image XObjects** and therefore reports page 1's object number for page 2 of `dup-raster`. It would also re-read, re-validate and re-optimize the whole file per call.

- [ ] **Step 1: Confirm the pdfcpu facts this task is built on**

These were verified empirically against v0.13.0 by building and running each call. Re-verify only if Task 1 Step 1 pinned a different version.

```bash
cd /Users/christopherdobbyn/work/dobbo-ca/byblos
go doc github.com/pdfcpu/pdfcpu/pkg/pdfcpu/types.Dict | grep -E 'IntEntry|NameEntry|BooleanEntry|ArrayEntry|DictEntry|Subtype'
go doc github.com/pdfcpu/pdfcpu/pkg/pdfcpu/types.StreamDict | head -20
go doc github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model.XRefTable | grep -E 'PageDict|PageContent|EnsurePageCount|DereferenceStreamDict|DereferenceDict|Dereference\('
go doc github.com/pdfcpu/pdfcpu/pkg/pdfcpu.ExtractImage
go doc github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model.Image
```

Expected, all confirmed on v0.13.0:

- `types.Dict` has `IntEntry(string) *int`, `NameEntry(string) *string`, `BooleanEntry(string) *bool`, `ArrayEntry(string) types.Array`, `DictEntry(string) types.Dict`, `Subtype() *string`. **None of these dereference an indirect reference** — see Step 5, which routes the ones that matter through `XRefTable.Dereference`.
- `types.StreamDict` has fields `Raw []byte` (encoded) and `Content []byte` (decoded), and method `Decode() error`. **`Content` is empty until `Decode()` is called** — verified.
- `types.Rectangle` is `{LL, UR types.Point}` with `Point{X, Y float64}`.
- `types.IndirectRef` is `{ObjectNumber, GenerationNumber types.Integer}`; `types.Integer` has `Value() int`. A resource-dict entry is a **value** `types.IndirectRef`, not a pointer — assert on the value type.
- `types.Integer` is `int`, `types.Float` is `float64`, `types.Boolean` is `bool`; each has a `Value()` method.
- `xt.PageDict(n, true)` returns `(types.Dict, *types.IndirectRef, *model.InheritedPageAttrs, error)` with `InheritedPageAttrs{Resources types.Dict; MediaBox, CropBox *types.Rectangle; Rotate int}`. `CropBox` is nil when the page has none.
- `xt.PageContent(d, n)` returns fully decoded, concatenated content bytes.
- `pdfcpu.ExtractImage(ctx *model.Context, sd *types.StreamDict, thumb bool, resourceID string, objNr int, stub bool) (*model.Image, error)` — note this is `pkg/pdfcpu`, **not** `pkg/api`. `model.Image` embeds an `io.Reader` and carries `FileType string`.

**If any signature differs on your version, fix the code below to match and note the difference in the commit message. Do not guess at a replacement.**

- [ ] **Step 2: Confirm the three behaviours that are not visible from signatures**

The probe must live **inside the module** — a `command-line-arguments` file under `/tmp` cannot import `github.com/dobbo-ca/byblos/internal/corpus` ("use of internal package not allowed"). Put it in the package it is probing for, where `arch_test.go` (Step 7) permits the pdfcpu import.

Create `internal/pdfdoc/probe_test.go` (the package has no other file yet; `go test` builds it from this alone):

```go
package pdfdoc

import (
	"bytes"
	"fmt"
	"io"
	"testing"

	"github.com/dobbo-ca/byblos/internal/corpus"
	"github.com/pdfcpu/pdfcpu/pkg/api"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/types"
)

// TestProbe is a throwaway: run it, read the output, delete the file.
func TestProbe(t *testing.T) {
	for _, name := range []string{"scan", "jbig2"} {
		data, ok := corpus.ByName(name)
		if !ok {
			t.Fatalf("corpus document %q not found", name)
		}
		ctx, err := api.ReadContext(bytes.NewReader(data), model.NewDefaultConfiguration())
		if err != nil {
			t.Fatalf("%s: ReadContext: %v", name, err)
		}
		fmt.Printf("%s: PageCount before EnsurePageCount: %d\n", name, ctx.PageCount)
		if err := ctx.EnsurePageCount(); err != nil {
			t.Fatalf("%s: EnsurePageCount: %v", name, err)
		}
		fmt.Printf("%s: PageCount after: %d\n", name, ctx.PageCount)

		// Object 5 is the image XObject in both documents.
		sd, _, err := ctx.XRefTable.DereferenceStreamDict(*types.NewIndirectRef(5, 0))
		if err != nil || sd == nil {
			t.Fatalf("%s: DereferenceStreamDict: %v", name, err)
		}
		im, err := pdfcpu.ExtractImage(ctx, sd, false, "Im0", 5, false)
		if err != nil {
			t.Fatalf("%s: ExtractImage: %v", name, err)
		}
		fmt.Printf("%s: FileType=%q Reader==nil:%v Width=%d Height=%d Bpc=%d\n",
			name, im.FileType, im.Reader == nil, im.Width, im.Height, im.Bpc)
		if im.Reader != nil {
			b, err := io.ReadAll(im)
			fmt.Printf("%s: bytes=%d readErr=%v\n", name, len(b), err)
		}
	}
}
```

```bash
go test ./internal/pdfdoc/ -run TestProbe -v
```

Expected output, all verified on v0.13.0:

```
scan: PageCount before EnsurePageCount: 0
scan: PageCount after: 1
scan: FileType="png" Reader==nil:false Width=0 Height=0 Bpc=0
scan: bytes=5374 readErr=<nil>
jbig2: PageCount before EnsurePageCount: 0
jbig2: PageCount after: 1
jbig2: FileType="jbig2" Reader==nil:false Width=0 Height=0 Bpc=0
jbig2: bytes=64 readErr=<nil>
```

(The exact `scan` byte count depends on the zlib encoder; anything in the low thousands is right. `jbig2: bytes=64` is exact — it is `len(corpus.jbig2Payload())` handed back untouched.)

Three facts to internalise, all of which the code below depends on:

1. **`ctx.PageCount` is zero after `ReadContext`.** You must call `ctx.EnsurePageCount()`. Without it every page lookup silently fails.
2. **`pdfcpu.ExtractImage` works on a context that was never validated or optimized** — the one `api.ReadContext` returns. This is what lets `Open` keep its promise not to run the validator, and it is why `RawImage` takes a resolved id rather than a page number.
3. **`model.Image.Width`, `Height`, and `Bpc` are ZERO** on this path; only `FileType` is populated. (`ExtractImage`'s `stub` argument is what fills them in, and a stub carries no pixels.) Pixel dimensions must come from the image XObject's own stream dictionary, which is what `ImageInfo` below does. **If your probe shows non-zero dimensions, prefer them and simplify — but do not assume it without seeing the output.**

A fourth behaviour is not worth a corpus document but is worth knowing, because Step 5 guards against it: for an image whose last filter is none of Flate / LZW / CCITTFax / RunLength / DCT / JPX / JBIG2 (an `/ASCIIHexDecode` image, say), `pdfcpu.RenderImage` returns `(nil, "", nil)` and `ExtractImage` hands back a `model.Image` whose embedded `io.Reader` is **nil**. `io.ReadAll` on it panics with a nil pointer dereference — an unrecoverable process kill, which the Global Constraints forbid.

Delete `internal/pdfdoc/probe_test.go` before Step 3.

- [ ] **Step 3: Write the failing test**

Create `internal/pdfdoc/pdfdoc_test.go`:

```go
package pdfdoc

import (
	"bytes"
	"errors"
	"image"
	"strings"
	"testing"

	_ "image/png"

	"github.com/dobbo-ca/byblos/internal/content"
	"github.com/dobbo-ca/byblos/internal/corpus"
)

// corpusDoc, not doc: `doc` is already the unexported struct type in
// pdfdoc.go, and shadowing it here is a compile error.
func corpusDoc(t *testing.T, name string) []byte {
	t.Helper()
	data, ok := corpus.ByName(name)
	if !ok {
		t.Fatalf("corpus document %q not found", name)
	}
	return data
}

func open(t *testing.T, name string) Doc {
	t.Helper()
	d, err := Open(bytes.NewReader(corpusDoc(t, name)))
	if err != nil {
		t.Fatalf("Open(%q) error = %v", name, err)
	}
	return d
}

// image0 resolves /Im0 on a page and returns its id, so the RawImage tests do
// not each repeat the four-line dance.
func image0(t *testing.T, d Doc, page int) int {
	t.Helper()
	p, err := d.Page(page)
	if err != nil {
		t.Fatalf("Page(%d) error = %v", page, err)
	}
	xo, ok := d.XObject(p.Scope, "Im0")
	if !ok {
		t.Fatalf("page %d: XObject(scope, \"Im0\") not found", page)
	}
	if !xo.Image {
		t.Fatalf("page %d: Im0 did not resolve as an image", page)
	}
	return xo.ID
}

func TestOpenReadsPageGeometry(t *testing.T) {
	d := open(t, "scan")
	if got := d.PageCount(); got != 1 {
		t.Fatalf("PageCount() = %d; want 1", got)
	}
	p, err := d.Page(1)
	if err != nil {
		t.Fatalf("Page(1) error = %v", err)
	}
	want := Rect{0, 0, corpus.PageWidthPt, corpus.PageHeightPt}
	if p.MediaBox != want {
		t.Errorf("MediaBox = %+v; want %+v", p.MediaBox, want)
	}
	// A page with no /CropBox reports the MediaBox, so callers never special-case it.
	if p.CropBox != want {
		t.Errorf("CropBox = %+v; want %+v (the MediaBox)", p.CropBox, want)
	}
	if p.Rotate != 0 {
		t.Errorf("Rotate = %d; want 0", p.Rotate)
	}
	if !strings.Contains(string(p.Content), "/Im0 Do") {
		t.Errorf("Content = %q; want it to contain \"/Im0 Do\"", p.Content)
	}
}

func TestOpenReadsRotate(t *testing.T) {
	p, err := open(t, "scan-rotated").Page(1)
	if err != nil {
		t.Fatalf("Page(1) error = %v", err)
	}
	if p.Rotate != 90 {
		t.Errorf("Rotate = %d; want 90", p.Rotate)
	}
}

func TestOpenMultiPage(t *testing.T) {
	d := open(t, "mixed")
	if got := d.PageCount(); got != 2 {
		t.Fatalf("PageCount() = %d; want 2", got)
	}
	p1, err := d.Page(1)
	if err != nil {
		t.Fatalf("Page(1) error = %v", err)
	}
	if !strings.Contains(string(p1.Content), "Tj") {
		t.Errorf("page 1 content = %q; want the born-digital text", p1.Content)
	}
	p2, err := d.Page(2)
	if err != nil {
		t.Fatalf("Page(2) error = %v", err)
	}
	if !strings.Contains(string(p2.Content), "/Im0 Do") {
		t.Errorf("page 2 content = %q; want the scan", p2.Content)
	}
}

func TestPageOutOfRange(t *testing.T) {
	d := open(t, "scan")
	for _, n := range []int{0, 2, -1} {
		if _, err := d.Page(n); err == nil {
			t.Errorf("Page(%d) on a 1-page document: want an error, got nil", n)
		}
	}
}

func TestXObjectResolvesAnImage(t *testing.T) {
	d := open(t, "scan")
	p, err := d.Page(1)
	if err != nil {
		t.Fatalf("Page(1) error = %v", err)
	}
	xo, ok := d.XObject(p.Scope, "Im0")
	if !ok {
		t.Fatal("XObject(scope, \"Im0\") not found")
	}
	if !xo.Image {
		t.Fatal("Im0 did not resolve as an image")
	}
	info, ok := d.ImageInfo(xo.ID)
	if !ok {
		t.Fatalf("ImageInfo(%d) not found", xo.ID)
	}
	if info.Width != corpus.ScanImageW || info.Height != corpus.ScanImageH {
		t.Errorf("image dims = %dx%d; want %dx%d",
			info.Width, info.Height, corpus.ScanImageW, corpus.ScanImageH)
	}
	if info.BPC != 8 {
		t.Errorf("BPC = %d; want 8", info.BPC)
	}
	if info.ImageMask {
		t.Error("ImageMask = true; want false")
	}
}

// A Form XObject must come back decoded, with its own /Matrix and a scope
// handle that resolves the form's own resources.
func TestXObjectResolvesAFormWithItsOwnScope(t *testing.T) {
	d := open(t, "scan-in-form")
	p, err := d.Page(1)
	if err != nil {
		t.Fatalf("Page(1) error = %v", err)
	}
	fm, ok := d.XObject(p.Scope, "Fm0")
	if !ok {
		t.Fatal("XObject(scope, \"Fm0\") not found")
	}
	if fm.Image {
		t.Fatal("Fm0 resolved as an image; want a form")
	}
	if !strings.Contains(string(fm.Content), "/Im0 Do") {
		t.Errorf("form content = %q; want it decoded and containing \"/Im0 Do\"", fm.Content)
	}
	if fm.Matrix != content.Identity {
		t.Errorf("form Matrix = %v; want identity", fm.Matrix)
	}
	if fm.Scope == p.Scope {
		t.Error("the form reused the page's scope; it declares its own /Resources")
	}
	if _, ok := d.XObject(fm.Scope, "Im0"); !ok {
		t.Error("Im0 does not resolve in the form's scope")
	}
	// The page's own scope must NOT see the form's Im0.
	if _, ok := d.XObject(p.Scope, "Im0"); ok {
		t.Error("Im0 leaked into the page scope")
	}
}

// A form without its own /Resources inherits the enclosing scope
// (ISO 32000-1 section 8.10.2). overlay-text's form declares only /Font, so
// the image must still resolve through the page's resources.
func TestFormWithoutXObjectResourcesFallsBack(t *testing.T) {
	d := open(t, "overlay-text")
	p, err := d.Page(1)
	if err != nil {
		t.Fatalf("Page(1) error = %v", err)
	}
	fm, ok := d.XObject(p.Scope, "Fm0")
	if !ok {
		t.Fatal("Fm0 not found")
	}
	if _, ok := d.XObject(fm.Scope, "Im0"); !ok {
		t.Error("Im0 does not resolve from the form's scope; resource fallback is missing")
	}
}

func TestXObjectMissingName(t *testing.T) {
	d := open(t, "scan")
	p, _ := d.Page(1)
	if _, ok := d.XObject(p.Scope, "Nope"); ok {
		t.Error("XObject(scope, \"Nope\") reported found")
	}
	if _, ok := d.XObject(9999, "Im0"); ok {
		t.Error("XObject with an out-of-range scope reported found")
	}
}

func TestRawImageReturnsDecodableBytes(t *testing.T) {
	d := open(t, "scan")
	data, ft, err := d.RawImage(image0(t, d, 1))
	if err != nil {
		t.Fatalf("RawImage() error = %v", err)
	}
	// pdfcpu re-renders a Flate-compressed image to PNG.
	if ft != "png" {
		t.Errorf("fileType = %q; want \"png\"", ft)
	}
	// Assert the bytes actually decode. Checking only len(data) != 0 would pass
	// on five kilobytes of garbage.
	img, format, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("image.Decode() error = %v", err)
	}
	if format != "png" {
		t.Errorf("decoded format = %q; want \"png\"", format)
	}
	if b := img.Bounds(); b.Dx() != corpus.ScanImageW || b.Dy() != corpus.ScanImageH {
		t.Errorf("decoded %dx%d; want %dx%d", b.Dx(), b.Dy(), corpus.ScanImageW, corpus.ScanImageH)
	}
}

// The regression guard for the dedup trap. Both pages of dup-raster hold the
// same raster bytes as two distinct objects; pdfcpu's optimize pass collapses
// them, so any path that asks pdfcpu which objects are on page 2 gets page 1's
// answer. Resolving through the page's own resources must not.
func TestRawImageIsPerPageWhenRastersAreIdentical(t *testing.T) {
	d := open(t, "dup-raster")
	id1, id2 := image0(t, d, 1), image0(t, d, 2)
	if id1 == id2 {
		t.Fatalf("both pages resolved to id %d; the corpus document is meant to use two objects", id1)
	}
	for page, id := range map[int]int{1: id1, 2: id2} {
		data, ft, err := d.RawImage(id)
		if err != nil {
			t.Fatalf("page %d: RawImage(%d) error = %v", page, id, err)
		}
		if ft != "png" {
			t.Errorf("page %d: fileType = %q; want \"png\"", page, ft)
		}
		img, _, err := image.Decode(bytes.NewReader(data))
		if err != nil {
			t.Fatalf("page %d: image.Decode() error = %v", page, err)
		}
		if b := img.Bounds(); b.Dx() != corpus.ScanImageW || b.Dy() != corpus.ScanImageH {
			t.Errorf("page %d: decoded %dx%d; want %dx%d",
				page, b.Dx(), b.Dy(), corpus.ScanImageW, corpus.ScanImageH)
		}
	}
}

// pdfcpu hands JBIG2 back as opaque bytes with FileType "jbig2" and no error.
// That is not a failure at this layer — it is a real payload in a codec this
// package does not claim to render — so RawImage reports it faithfully and
// extract.go decides what to do.
func TestRawImageReportsJBIG2AsIs(t *testing.T) {
	d := open(t, "jbig2")
	data, ft, err := d.RawImage(image0(t, d, 1))
	if err != nil {
		t.Fatalf("RawImage() error = %v", err)
	}
	if ft != "jbig2" {
		t.Errorf("fileType = %q; want \"jbig2\"", ft)
	}
	if len(data) == 0 {
		t.Error("RawImage() returned no bytes for the JBIG2 stream")
	}
}

// The bitonal flag B2 selects on has to come from the dictionary, because
// pdfcpu reports zero for Bpc on this path.
func TestImageInfoReadsOneBitPerComponent(t *testing.T) {
	d := open(t, "jbig2")
	info, ok := d.ImageInfo(image0(t, d, 1))
	if !ok {
		t.Fatal("ImageInfo() not found")
	}
	if info.BPC != 1 {
		t.Errorf("BPC = %d; want 1", info.BPC)
	}
	if info.Width != corpus.ScanImageW || info.Height != corpus.ScanImageH {
		t.Errorf("dims = %dx%d; want %dx%d",
			info.Width, info.Height, corpus.ScanImageW, corpus.ScanImageH)
	}
}

func TestRawImageUnknownID(t *testing.T) {
	d := open(t, "scan")
	if _, _, err := d.RawImage(99999); err == nil {
		t.Fatal("RawImage() for an id that was never resolved: want an error, got nil")
	}
	// ErrUnsupportedCodec is reserved for a real image in a codec pdfcpu will
	// not render; an unknown id is a caller mistake and must not be confused
	// with it.
	_, _, err := d.RawImage(99999)
	if errors.Is(err, ErrUnsupportedCodec) {
		t.Errorf("error = %v; want a plain lookup error, not ErrUnsupportedCodec", err)
	}
}

// A truncated file must produce a clean error, never a panic and never a
// plausible-looking parse.
func TestOpenMalformedReturnsAnError(t *testing.T) {
	if _, err := Open(bytes.NewReader(corpusDoc(t, "malformed"))); err == nil {
		t.Fatal("Open(malformed): want an error, got nil")
	}
}

// A page that carries no /Contents is legal and empty, not an error.
func TestPageWithoutContentsIsEmptyNotAnError(t *testing.T) {
	// A minimal one-page document with no /Contents entry.
	src := "%PDF-1.7\n" +
		"1 0 obj\n<< /Type /Catalog /Pages 2 0 R >>\nendobj\n" +
		"2 0 obj\n<< /Type /Pages /Kids [3 0 R] /Count 1 >>\nendobj\n" +
		"3 0 obj\n<< /Type /Page /Parent 2 0 R /MediaBox [0 0 200 300] >>\nendobj\n"
	var xref strings.Builder
	offs := []int{
		strings.Index(src, "1 0 obj"),
		strings.Index(src, "2 0 obj"),
		strings.Index(src, "3 0 obj"),
	}
	start := len(src)
	xref.WriteString("xref\n0 4\n0000000000 65535 f \n")
	for _, o := range offs {
		xref.WriteString(pad10(o) + " 00000 n \n")
	}
	xref.WriteString("trailer\n<< /Size 4 /Root 1 0 R >>\nstartxref\n" + itoa(start) + "\n%%EOF\n")

	d, err := Open(strings.NewReader(src + xref.String()))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	p, err := d.Page(1)
	if err != nil {
		t.Fatalf("Page(1) on a contentless page: want no error, got %v", err)
	}
	if len(p.Content) != 0 {
		t.Errorf("Content = %q; want empty", p.Content)
	}
	if want := (Rect{0, 0, 200, 300}); p.MediaBox != want {
		t.Errorf("MediaBox = %+v; want %+v", p.MediaBox, want)
	}
}

func pad10(n int) string {
	s := itoa(n)
	for len(s) < 10 {
		s = "0" + s
	}
	return s
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}
```

- [ ] **Step 4: Run the test to verify it fails**

Run: `go test ./internal/pdfdoc/ -v`
Expected: FAIL — `undefined: Open`.

- [ ] **Step 5: Implement the bridge**

Create `internal/pdfdoc/pdfdoc.go`:

```go
// Package pdfdoc is the only package in Byblos that imports pdfcpu.
//
// Everything above it speaks in the types declared here, so replacing the
// underlying PDF library is a change to this package alone (design spec
// section 3). arch_test.go in the repository root enforces that.
//
// pdfcpu API notes, verified against v0.13.0:
//
//   - api.ReadAndValidate dereferences conf.Cmd with no nil check and pdfcpu's
//     fault.Catch only recovers its own panic type, so passing a nil
//     *model.Configuration kills the process rather than returning an error.
//     Every call here passes model.NewDefaultConfiguration().
//   - ctx.PageCount is zero after ReadContext; ctx.EnsurePageCount() populates it.
//   - types.StreamDict.Content is empty until Decode() is called.
//   - model.Image.Width/Height/Bpc are zero unless ExtractImage is called with
//     stub=true, and a stub carries no pixels. Pixel dimensions therefore come
//     from the image XObject's own stream dictionary.
//   - pdfcpu cannot decode JBIG2Decode or JPXDecode and returns the raw opaque
//     bytes with FileType "jbig2"/"jpx" rather than erroring. Callers must check
//     the file type; extract.go does.
//   - pdfcpu.RenderImage returns (nil, "", nil) for any other unhandled filter,
//     and ExtractImage passes that straight through as a model.Image with a nil
//     embedded Reader. Reading it panics, so RawImage guards and returns
//     ErrUnsupportedCodec instead.
//   - types.Dict's typed accessors (IntEntry, DictEntry, ArrayEntry, ...) do NOT
//     dereference an indirect reference; they return zero. Real documents do use
//     an indirect /Resources on a Form XObject, so every dictionary read that
//     feeds a Byblos type goes through the deref helpers at the bottom of this
//     file.
package pdfdoc

import (
	"errors"
	"fmt"
	"io"

	"github.com/dobbo-ca/byblos/internal/content"
	"github.com/pdfcpu/pdfcpu/pkg/api"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/types"
)

// ErrUnsupportedCodec reports an image stream whose compression filter pdfcpu
// will not render. It exists so that pdfcpu's nil-reader return becomes an
// error at this seam instead of a nil dereference in the caller.
//
// It is NOT returned for JBIG2 or JPX: those come back as real bytes with a
// file type naming the codec, and deciding what to do about them is extract.go's
// job, not this package's.
var ErrUnsupportedCodec = errors.New("byblos/pdfdoc: image codec cannot be rendered")

// Rect is a rectangle in PDF default user space: points, origin lower-left,
// y increasing upward.
type Rect struct{ LLX, LLY, URX, URY float64 }

// ImageInfo describes an image XObject as its own dictionary declares it.
type ImageInfo struct {
	Name          string
	ObjNr         int
	Width, Height int // pixels
	BPC           int // /BitsPerComponent; 0 when absent
	ImageMask     bool
}

// Page is one page's geometry, content, and resource scope.
type Page struct {
	Index    int // 1-based
	MediaBox Rect
	CropBox  Rect // equals MediaBox when the page declares none
	Rotate   int
	Content  []byte // decoded, concatenated
	Scope    int    // resource scope handle for content.Env
}

// Doc is a parsed PDF and the seam Byblos keeps between itself and pdfcpu.
type Doc interface {
	PageCount() int
	Page(n int) (*Page, error)
	// XObject implements content.Env.
	XObject(scope int, name string) (content.XObject, bool)
	// ImageInfo returns the dictionary facts for an image resolved by XObject,
	// keyed by the ID that XObject returned.
	ImageInfo(id int) (ImageInfo, bool)
	// RawImage renders an image previously resolved by XObject and returns its
	// bytes and the file type pdfcpu inferred. The id is the one XObject
	// returned; an id this document has not resolved is an error.
	RawImage(id int) (data []byte, fileType string, err error)
}

type doc struct {
	ctx     *model.Context
	scopes  []scope
	images  map[int]ImageInfo
	streams map[int]*types.StreamDict // image stream dicts, keyed like images
	nextID  int                       // synthetic ids for direct (non-indirect) image objects
}

type scope struct {
	res    types.Dict
	parent int // -1 for a page scope
}

// Open parses rs. It does not run pdfcpu's validator: real scanner output
// exercises relaxed paths, and rejecting a readable file helps nobody. The
// validation gate belongs to Optimize (B5) and to the caller's policy.
//
// rs is read once, here. Nothing below re-reads it, so a file this function
// accepts cannot later be rejected by a validator Byblos opted out of.
func Open(rs io.ReadSeeker) (Doc, error) {
	if _, err := rs.Seek(0, io.SeekStart); err != nil {
		return nil, fmt.Errorf("byblos/pdfdoc: seek: %w", err)
	}
	ctx, err := api.ReadContext(rs, model.NewDefaultConfiguration())
	if err != nil {
		return nil, fmt.Errorf("byblos/pdfdoc: read: %w", err)
	}
	if err := ctx.EnsurePageCount(); err != nil {
		return nil, fmt.Errorf("byblos/pdfdoc: page count: %w", err)
	}
	return &doc{
		ctx:     ctx,
		images:  map[int]ImageInfo{},
		streams: map[int]*types.StreamDict{},
		nextID:  -1,
	}, nil
}

func (d *doc) PageCount() int { return d.ctx.PageCount }

func (d *doc) Page(n int) (*Page, error) {
	if n < 1 || n > d.ctx.PageCount {
		return nil, fmt.Errorf("byblos/pdfdoc: page %d out of range 1..%d", n, d.ctx.PageCount)
	}
	xt := d.ctx.XRefTable
	pd, _, inh, err := xt.PageDict(n, true)
	if err != nil {
		return nil, fmt.Errorf("byblos/pdfdoc: page %d dict: %w", n, err)
	}
	if pd == nil || inh == nil || inh.MediaBox == nil {
		return nil, fmt.Errorf("byblos/pdfdoc: page %d has no dictionary or no MediaBox", n)
	}

	p := &Page{
		Index:    n,
		MediaBox: rectOf(inh.MediaBox),
		Rotate:   ((inh.Rotate % 360) + 360) % 360,
		Scope:    d.addScope(inh.Resources, -1),
	}
	p.CropBox = p.MediaBox
	if inh.CropBox != nil {
		p.CropBox = rectOf(inh.CropBox)
	}

	// A page with no /Contents is legal and empty. Check the dictionary rather
	// than matching on pdfcpu's error text: PageContent returns a
	// github.com/pkg/errors value that errors.Is cannot match against a sentinel.
	if _, ok := pd.Find("Contents"); ok {
		c, err := xt.PageContent(pd, n)
		if err != nil {
			return nil, fmt.Errorf("byblos/pdfdoc: page %d content: %w", n, err)
		}
		p.Content = c
	}
	return p, nil
}

// addScope appends a resource scope and returns its handle.
//
// Known simplification: scopes are never reused, so calling Page twice or
// resolving the same form twice appends duplicate entries. They are equivalent,
// so this is a small waste rather than a bug, and a document has at most a few
// thousand of them. Memoize per page index and per (scope, name) if a real
// archive ever shows it matters.
func (d *doc) addScope(res types.Dict, parent int) int {
	d.scopes = append(d.scopes, scope{res: res, parent: parent})
	return len(d.scopes) - 1
}

func (d *doc) XObject(sc int, name string) (content.XObject, bool) {
	obj, ok := d.lookupXObject(sc, name)
	if !ok {
		return content.XObject{}, false
	}
	id := d.identify(obj)
	sd, _, err := d.ctx.XRefTable.DereferenceStreamDict(obj)
	if err != nil || sd == nil {
		return content.XObject{}, false
	}
	sub := sd.Dict.Subtype()
	if sub == nil {
		return content.XObject{}, false
	}

	switch *sub {
	case "Image":
		d.images[id] = ImageInfo{
			Name:      name,
			ObjNr:     id,
			Width:     d.intEntry(sd.Dict, "Width"),
			Height:    d.intEntry(sd.Dict, "Height"),
			BPC:       d.intEntry(sd.Dict, "BitsPerComponent"),
			ImageMask: d.boolEntry(sd.Dict, "ImageMask"),
		}
		// Keep the stream dictionary. RawImage renders from this rather than
		// asking pdfcpu which objects a page uses, because that answer comes
		// from the optimize pass, which deduplicates identical rasters.
		d.streams[id] = sd
		return content.XObject{Image: true, ID: id}, true

	case "Form":
		if err := sd.Decode(); err != nil {
			return content.XObject{}, false
		}
		m := content.Identity
		if arr := d.arrayEntry(sd.Dict, "Matrix"); len(arr) == 6 {
			for i := 0; i < 6; i++ {
				v, ok := d.number(arr[i])
				if !ok {
					m = content.Identity
					break
				}
				m[i] = v
			}
		}
		// A form without its own /Resources inherits the enclosing resource
		// dictionary (ISO 32000-1 section 8.10.2), which the scope's parent
		// chain provides.
		formScope := d.addScope(d.dictEntry(sd.Dict, "Resources"), sc)
		return content.XObject{Content: sd.Content, Matrix: m, Scope: formScope}, true
	}
	return content.XObject{}, false
}

// lookupXObject walks the scope chain so a form that declares only /Font still
// resolves images through its parent.
func (d *doc) lookupXObject(sc int, name string) (types.Object, bool) {
	for i := sc; i >= 0 && i < len(d.scopes); {
		res := d.scopes[i].res
		if res != nil {
			if xod, err := d.ctx.XRefTable.DereferenceDict(res["XObject"]); err == nil && xod != nil {
				if o, ok := xod.Find(name); ok {
					return o, true
				}
			}
		}
		i = d.scopes[i].parent
		if i < 0 {
			break
		}
	}
	return nil, false
}

// identify returns a stable id for an XObject: its PDF object number when it is
// an indirect reference, and a negative synthetic id otherwise.
func (d *doc) identify(o types.Object) int {
	switch v := o.(type) {
	case types.IndirectRef:
		return v.ObjectNumber.Value()
	case *types.IndirectRef:
		return v.ObjectNumber.Value()
	}
	id := d.nextID
	d.nextID--
	return id
}

func (d *doc) ImageInfo(id int) (ImageInfo, bool) {
	info, ok := d.images[id]
	return info, ok
}

// RawImage renders the image XObject that XObject previously resolved as id.
//
// It renders from the stream dictionary this document already holds, on the
// already-parsed context. Two consequences worth stating, because the obvious
// alternative (api.ExtractImagesRaw with a page number) gets both wrong:
//
//   - It is per-image, not per-file. ExtractImagesRaw runs
//     ReadValidateAndOptimize on every call, so extracting an N-page document
//     would re-read, re-validate and re-optimize it N times — and could reject
//     on the validator that Open deliberately skipped.
//   - It is per-object, not per-deduplicated-object. pdfcpu's optimize pass
//     collapses byte-identical image XObjects, so the map ExtractImagesRaw
//     returns for page 2 of a duplex scan is keyed by page 1's object number.
//     See the dup-raster corpus document.
func (d *doc) RawImage(id int) ([]byte, string, error) {
	sd, ok := d.streams[id]
	if !ok {
		return nil, "", fmt.Errorf("byblos/pdfdoc: image %d has not been resolved on this document", id)
	}
	im, err := pdfcpu.ExtractImage(d.ctx, sd, false, d.images[id].Name, id, false)
	if err != nil {
		return nil, "", fmt.Errorf("byblos/pdfdoc: rendering image %d: %w", id, err)
	}
	// pdfcpu signals "I will not render this filter" by returning a nil reader
	// and an empty file type, with a nil error. Reading that panics.
	if im == nil || im.Reader == nil || im.FileType == "" {
		return nil, "", fmt.Errorf("byblos/pdfdoc: image %d: %w", id, ErrUnsupportedCodec)
	}
	b, err := io.ReadAll(im)
	if err != nil {
		return nil, "", fmt.Errorf("byblos/pdfdoc: reading image %d: %w", id, err)
	}
	return b, im.FileType, nil
}

func (d *doc) number(o types.Object) (float64, bool) {
	o, err := d.ctx.XRefTable.Dereference(o)
	if err != nil {
		return 0, false
	}
	switch v := o.(type) {
	case types.Integer:
		return float64(v.Value()), true
	case types.Float:
		return v.Value(), true
	}
	return 0, false
}

func rectOf(r *types.Rectangle) Rect {
	return Rect{LLX: r.LL.X, LLY: r.LL.Y, URX: r.UR.X, URY: r.UR.Y}
}

// --- dereferencing dictionary readers ---------------------------------------
//
// types.Dict's own IntEntry / BooleanEntry / DictEntry / ArrayEntry return the
// zero value when the entry is an types.IndirectRef instead of a direct object.
// An indirect /Resources on a Form XObject is common in real documents and an
// indirect /Width is legal, and in both cases the failure would be silent wrong
// data — an empty form scope, or Width 0 flowing into PageInfo — rather than an
// error. These read through the reference.

func (d *doc) deref(o types.Object) types.Object {
	v, err := d.ctx.XRefTable.Dereference(o)
	if err != nil {
		return nil
	}
	return v
}

func (d *doc) intEntry(dict types.Dict, key string) int {
	if v, ok := d.deref(dict[key]).(types.Integer); ok {
		return v.Value()
	}
	return 0
}

func (d *doc) boolEntry(dict types.Dict, key string) bool {
	if v, ok := d.deref(dict[key]).(types.Boolean); ok {
		return v.Value()
	}
	return false
}

func (d *doc) dictEntry(dict types.Dict, key string) types.Dict {
	if v, ok := d.deref(dict[key]).(types.Dict); ok {
		return v
	}
	return nil
}

func (d *doc) arrayEntry(dict types.Dict, key string) types.Array {
	if v, ok := d.deref(dict[key]).(types.Array); ok {
		return v
	}
	return nil
}
```

- [ ] **Step 6: Run the tests to verify they pass**

Run: `go test ./internal/pdfdoc/ -v`
Expected: PASS, all fifteen tests. (If `internal/pdfdoc/probe_test.go` is still present, delete it — Step 2 says so, and `arch_test.go` in Step 7 will not complain about it, so nothing else will remind you.)

If `TestOpenMalformedReturnsAnError` fails because pdfcpu repaired the file, **do not weaken the assertion.** Truncate harder: change the `6/10` in `corpus.malformed()` to `3/10`, re-run `go test ./internal/corpus/ ./internal/pdfdoc/`, and record the fraction you settled on in the function comment. The point of the case is that Byblos returns a clean error instead of panicking or producing a plausible-but-wrong parse.

- [ ] **Step 7: Add the architecture guard**

Create `arch_test.go` in the repository root:

```go
package byblos

import (
	"os/exec"
	"strings"
	"testing"
)

// Design spec section 3: pdfcpu is wrapped behind Byblos's own interfaces so
// that replacing it later is a swap rather than a rewrite. That is only true
// while exactly one package imports it.
func TestOnlyPdfdocImportsPdfcpu(t *testing.T) {
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skipf("go toolchain not on PATH: %v", err)
	}
	// XTestImports covers a `package byblos_test` file, which would otherwise
	// slip past both this guard and the CI allowlist.
	out, err := exec.Command(goBin, "list",
		"-f", `{{.ImportPath}} {{join .Imports ","}} {{join .TestImports ","}} {{join .XTestImports ","}}`,
		"./...").Output()
	if err != nil {
		t.Fatalf("go list: %v", err)
	}
	const allowed = "github.com/dobbo-ca/byblos/internal/pdfdoc"
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		pkg, imports, _ := strings.Cut(line, " ")
		if pkg == allowed {
			continue
		}
		if strings.Contains(imports, "github.com/pdfcpu/pdfcpu") {
			t.Errorf("package %s imports pdfcpu; only %s may", pkg, allowed)
		}
	}
}
```

Run: `go test . -run TestOnlyPdfdocImportsPdfcpu -v`
Expected: PASS.

- [ ] **Step 8: Commit**

```bash
git add internal/pdfdoc arch_test.go
git commit -m "feat(pdfdoc): wrap pdfcpu behind the Byblos document interface"
```

---

## Task 9: Inspect

**Files:**
- Create: `inspect.go`
- Test: `inspect_test.go`

**Interfaces:**
- Consumes: `internal/pdfdoc` (Task 8), `internal/content` (Task 7).
- Produces:

```go
type ImageRef struct {
    Bounds        image.Rectangle
    Width, Height int
    Bitonal       bool
}

type PageInfo struct {
    Index     int
    Bounds    image.Rectangle
    Images    []ImageRef
    TextChars int
}

func Inspect(r io.ReadSeeker) ([]PageInfo, error)
```

- [ ] **Step 1: Write the failing test**

Create `inspect_test.go`:

```go
package byblos

import (
	"bytes"
	"image"
	"testing"

	"github.com/dobbo-ca/byblos/internal/corpus"
)

// corpusDoc is used by every test in this package that needs corpus bytes.
// Discarding the ok from corpus.ByName would turn a typo'd name into nil data,
// and a test that expects an error would then pass vacuously.
func corpusDoc(t *testing.T, name string) []byte {
	t.Helper()
	data, ok := corpus.ByName(name)
	if !ok {
		t.Fatalf("corpus document %q not found", name)
	}
	return data
}

func inspect(t *testing.T, name string) []PageInfo {
	t.Helper()
	pages, err := Inspect(bytes.NewReader(corpusDoc(t, name)))
	if err != nil {
		t.Fatalf("Inspect(%q) error = %v", name, err)
	}
	return pages
}

var fullPage = image.Rect(0, 0, corpus.PageWidthPt, corpus.PageHeightPt)

func TestInspectBornDigital(t *testing.T) {
	pages := inspect(t, "born-digital")
	if len(pages) != 1 {
		t.Fatalf("got %d pages; want 1", len(pages))
	}
	p := pages[0]
	if p.Index != 1 {
		t.Errorf("Index = %d; want 1", p.Index)
	}
	if p.Bounds != fullPage {
		t.Errorf("Bounds = %v; want %v", p.Bounds, fullPage)
	}
	if len(p.Images) != 0 {
		t.Errorf("Images = %+v; want none", p.Images)
	}
	if p.TextChars != corpus.BornDigitalTextChars {
		t.Errorf("TextChars = %d; want %d", p.TextChars, corpus.BornDigitalTextChars)
	}
}

func TestInspectSingleImageScan(t *testing.T) {
	pages := inspect(t, "scan")
	if len(pages) != 1 {
		t.Fatalf("got %d pages; want 1", len(pages))
	}
	p := pages[0]
	if p.TextChars != 0 {
		t.Errorf("TextChars = %d; want 0", p.TextChars)
	}
	if len(p.Images) != 1 {
		t.Fatalf("Images = %+v; want exactly one", p.Images)
	}
	img := p.Images[0]
	if img.Bounds != fullPage {
		t.Errorf("image Bounds = %v; want %v (page-covering)", img.Bounds, fullPage)
	}
	if img.Width != corpus.ScanImageW || img.Height != corpus.ScanImageH {
		t.Errorf("image pixels = %dx%d; want %dx%d",
			img.Width, img.Height, corpus.ScanImageW, corpus.ScanImageH)
	}
	if img.Bitonal {
		t.Error("Bitonal = true; the corpus scan is 8-bit grey")
	}
}

func TestInspectTiledReportsBothHalves(t *testing.T) {
	p := inspect(t, "tiled")[0]
	if len(p.Images) != 2 {
		t.Fatalf("Images = %+v; want two", p.Images)
	}
	half := corpus.PageWidthPt / 2
	wantLeft := image.Rect(0, 0, half, corpus.PageHeightPt)
	wantRight := image.Rect(half, 0, corpus.PageWidthPt, corpus.PageHeightPt)
	if p.Images[0].Bounds != wantLeft {
		t.Errorf("left image Bounds = %v; want %v", p.Images[0].Bounds, wantLeft)
	}
	if p.Images[1].Bounds != wantRight {
		t.Errorf("right image Bounds = %v; want %v", p.Images[1].Bounds, wantRight)
	}
	for i, img := range p.Images {
		if img.Width != corpus.TileImageW || img.Height != corpus.TileImageH {
			t.Errorf("tile %d pixels = %dx%d; want %dx%d",
				i, img.Width, img.Height, corpus.TileImageW, corpus.TileImageH)
		}
	}
}

// The image lives inside a Form XObject, so its placement can only be found by
// composing the form's /Matrix with the page CTM.
func TestInspectSeesThroughAForm(t *testing.T) {
	p := inspect(t, "scan-in-form")[0]
	if len(p.Images) != 1 {
		t.Fatalf("Images = %+v; want one", p.Images)
	}
	if p.Images[0].Bounds != fullPage {
		t.Errorf("image Bounds = %v; want %v", p.Images[0].Bounds, fullPage)
	}
}

// The regression the research demands: a form-borne text overlay is invisible
// to an image count, so TextChars must come from the walk, not from pdfcpu.
func TestInspectCountsTextInsideAForm(t *testing.T) {
	p := inspect(t, "overlay-text")[0]
	if len(p.Images) != 1 {
		t.Errorf("Images = %+v; want one", p.Images)
	}
	if p.TextChars != corpus.OverlayTextChars {
		t.Errorf("TextChars = %d; want %d", p.TextChars, corpus.OverlayTextChars)
	}
}

func TestInspectVectorOverlayStillReportsTheImage(t *testing.T) {
	p := inspect(t, "overlay-vector")[0]
	if len(p.Images) != 1 {
		t.Errorf("Images = %+v; want one", p.Images)
	}
	if p.TextChars != 0 {
		t.Errorf("TextChars = %d; want 0", p.TextChars)
	}
}

func TestInspectMultiPage(t *testing.T) {
	pages := inspect(t, "mixed")
	if len(pages) != 2 {
		t.Fatalf("got %d pages; want 2", len(pages))
	}
	if pages[0].Index != 1 || pages[1].Index != 2 {
		t.Errorf("indices = %d, %d; want 1, 2", pages[0].Index, pages[1].Index)
	}
	if pages[0].TextChars != corpus.BornDigitalTextChars || len(pages[0].Images) != 0 {
		t.Errorf("page 1 = %+v; want the born-digital page", pages[0])
	}
	if pages[1].TextChars != 0 || len(pages[1].Images) != 1 {
		t.Errorf("page 2 = %+v; want the scan page", pages[1])
	}
}

func TestInspectRotatedPageReportsUnrotatedBounds(t *testing.T) {
	p := inspect(t, "scan-rotated")[0]
	// /Rotate is a display attribute. Content space is unaffected, so Bounds
	// stays the MediaBox and the placement still covers it.
	if p.Bounds != fullPage {
		t.Errorf("Bounds = %v; want %v", p.Bounds, fullPage)
	}
	if len(p.Images) != 1 || p.Images[0].Bounds != fullPage {
		t.Errorf("Images = %+v; want one page-covering placement", p.Images)
	}
}

func TestInspectMalformedReturnsAnError(t *testing.T) {
	if _, err := Inspect(bytes.NewReader(corpusDoc(t, "malformed"))); err == nil {
		t.Fatal("Inspect(malformed): want an error, got nil")
	}
}

// Bitonal is the field B2's JBIG2 path selects on, so it needs a document that
// makes it true, not only ones that make it false.
func TestInspectReportsBitonalForOneBitImages(t *testing.T) {
	p := inspect(t, "jbig2")[0]
	if len(p.Images) != 1 {
		t.Fatalf("Images = %+v; want one", p.Images)
	}
	if !p.Images[0].Bitonal {
		t.Error("Bitonal = false; the jbig2 document is /BitsPerComponent 1")
	}
	if p.Images[0].Width != corpus.ScanImageW || p.Images[0].Height != corpus.ScanImageH {
		t.Errorf("image pixels = %dx%d; want %dx%d",
			p.Images[0].Width, p.Images[0].Height, corpus.ScanImageW, corpus.ScanImageH)
	}
}

// Both pages of dup-raster are page-covering scans, and Inspect must say so for
// each independently.
func TestInspectDupRasterReportsBothPages(t *testing.T) {
	pages := inspect(t, "dup-raster")
	if len(pages) != 2 {
		t.Fatalf("got %d pages; want 2", len(pages))
	}
	for _, p := range pages {
		if len(p.Images) != 1 || p.Images[0].Bounds != fullPage {
			t.Errorf("page %d = %+v; want one page-covering placement", p.Index, p.Images)
		}
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test . -run TestInspect -v`
Expected: FAIL — `undefined: Inspect`.

- [ ] **Step 3: Implement Inspect**

Create `inspect.go`:

```go
package byblos

import (
	"fmt"
	"image"
	"io"
	"math"

	"github.com/dobbo-ca/byblos/internal/content"
	"github.com/dobbo-ca/byblos/internal/pdfdoc"
)

// ImageRef is one painting of an image on a page.
//
// Bounds is where the image lands, in PDF default user space: points, origin
// lower-left, y increasing upward. image.Rectangle is used only as a convenient
// integer rectangle — do not read it as screen coordinates.
type ImageRef struct {
	Bounds        image.Rectangle
	Width, Height int  // pixel dimensions of the stored raster
	Bitonal       bool // 1 bit per component, or an image mask
}

// PageInfo describes one page.
//
// Bounds is the page's CropBox, or its MediaBox when it declares no CropBox,
// in the same user-space convention as ImageRef.Bounds.
//
// TextChars counts the bytes shown by the page's text operators, including
// text reached through Form XObjects. It is a born-digital signal, not a text
// extractor: it counts stored code units, not Unicode code points, and it does
// not decode fonts. Byblos never recognizes text (design spec section 3).
type PageInfo struct {
	Index     int
	Bounds    image.Rectangle
	Images    []ImageRef
	TextChars int
}

// Inspect reports what every page of r contains. It does not render anything.
func Inspect(r io.ReadSeeker) ([]PageInfo, error) {
	d, err := pdfdoc.Open(r)
	if err != nil {
		return nil, err
	}
	out := make([]PageInfo, 0, d.PageCount())
	for n := 1; n <= d.PageCount(); n++ {
		pi, _, err := inspectPage(d, n)
		if err != nil {
			return nil, err
		}
		out = append(out, *pi)
	}
	return out, nil
}

// inspectPage returns the page's PageInfo alongside the raw walk, which
// ExtractPageRaster needs for classification.
func inspectPage(d pdfdoc.Doc, n int) (*PageInfo, *content.Scan, error) {
	p, err := d.Page(n)
	if err != nil {
		return nil, nil, err
	}
	s, err := content.Walk(p.Content, p.Scope, d)
	if err != nil {
		return nil, nil, fmt.Errorf("byblos: page %d: %w", n, err)
	}
	pi := &PageInfo{
		Index:     n,
		Bounds:    rectOf(p.CropBox),
		TextChars: s.TextChars,
	}
	for _, pl := range s.Images {
		ref := ImageRef{Bounds: boxRect(pl.Box)}
		if info, ok := d.ImageInfo(pl.ID); ok {
			ref.Width = info.Width
			ref.Height = info.Height
			ref.Bitonal = info.BPC == 1 || info.ImageMask
		}
		pi.Images = append(pi.Images, ref)
	}
	return pi, s, nil
}

func rectOf(r pdfdoc.Rect) image.Rectangle {
	return image.Rect(round(r.LLX), round(r.LLY), round(r.URX), round(r.URY))
}

func boxRect(b content.Box) image.Rectangle {
	return image.Rect(round(b.LLX), round(b.LLY), round(b.URX), round(b.URY))
}

func round(v float64) int { return int(math.Round(v)) }
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test . -run TestInspect -v`
Expected: PASS, all eleven tests.

- [ ] **Step 5: Commit**

```bash
git add inspect.go inspect_test.go
git commit -m "feat: add Inspect returning per-page geometry, images, and text length"
```

---

## Task 10: ExtractPageRaster and ErrNotSingleRaster

The design's central bet: a scanned page is one page-covering image, so extraction replaces rendering. Everything else is **detected**, not guessed at.

Classification order matters, because the first matching reason is the one reported and it should be the most informative. A born-digital page has both no image and text; `no-image` says more.

**Files:**
- Create: `extract.go`
- Test: `extract_test.go`

**Interfaces:**
- Consumes: `internal/pdfdoc`, `internal/content`, `inspectPage` (Task 9).
- Produces:

```go
var ErrNotSingleRaster = errors.New("byblos: page is not a single page-covering raster")
var ErrUnsupportedImageCodec = errors.New("byblos: page raster uses an image codec byblos cannot decode")

func ExtractPageRaster(r io.ReadSeeker, page int) (image.Image, error)
```

- [ ] **Step 1: Write the failing test**

Create `extract_test.go`:

```go
package byblos

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/dobbo-ca/byblos/internal/corpus"
)

func TestExtractPageRasterSucceeds(t *testing.T) {
	for _, name := range []string{"scan", "scan-rotated", "scan-in-form"} {
		t.Run(name, func(t *testing.T) {
			data := corpusDoc(t, name)
			img, err := ExtractPageRaster(bytes.NewReader(data), 1)
			if err != nil {
				t.Fatalf("ExtractPageRaster() error = %v", err)
			}
			b := img.Bounds()
			if b.Dx() != corpus.ScanImageW || b.Dy() != corpus.ScanImageH {
				t.Errorf("raster = %dx%d; want %dx%d",
					b.Dx(), b.Dy(), corpus.ScanImageW, corpus.ScanImageH)
			}
		})
	}
}

func TestExtractPageRasterDiverts(t *testing.T) {
	for _, tc := range []struct{ doc, reason string }{
		{"born-digital", "no-image"},
		{"overlay-text", "has-text"},
		{"tiled", "multiple-images"},
		{"overlay-vector", "vector-paint"},
	} {
		t.Run(tc.doc, func(t *testing.T) {
			data := corpusDoc(t, tc.doc)
			_, err := ExtractPageRaster(bytes.NewReader(data), 1)
			if !errors.Is(err, ErrNotSingleRaster) {
				t.Fatalf("error = %v; want ErrNotSingleRaster", err)
			}
			if !strings.Contains(err.Error(), tc.reason) {
				t.Errorf("error = %q; want it to name the reason %q", err, tc.reason)
			}
		})
	}
}

// The trap ErrUnsupportedImageCodec exists for: pdfcpu returns a JBIG2 payload
// as opaque bytes with no error, so without this check the bytes would reach an
// image decoder and either fail obscurely or appear to work.
func TestExtractPageRasterRejectsJBIG2(t *testing.T) {
	data := corpusDoc(t, "jbig2")
	_, err := ExtractPageRaster(bytes.NewReader(data), 1)
	if !errors.Is(err, ErrUnsupportedImageCodec) {
		t.Fatalf("error = %v; want ErrUnsupportedImageCodec", err)
	}
	if errors.Is(err, ErrNotSingleRaster) {
		t.Error("a JBIG2 page-covering scan IS a single raster; it must not also report ErrNotSingleRaster")
	}
	if !strings.Contains(err.Error(), "jbig2") {
		t.Errorf("error = %q; want it to name the codec", err)
	}
}

// Page 2 of the mixed document is a clean scan even though page 1 is not.
// Classification must be per-page.
func TestExtractPageRasterIsPerPage(t *testing.T) {
	data := corpusDoc(t, "mixed")
	if _, err := ExtractPageRaster(bytes.NewReader(data), 1); !errors.Is(err, ErrNotSingleRaster) {
		t.Errorf("page 1: error = %v; want ErrNotSingleRaster", err)
	}
	if _, err := ExtractPageRaster(bytes.NewReader(data), 2); err != nil {
		t.Errorf("page 2: error = %v; want success", err)
	}
}

// Extraction must be per-object too, not merely per-page. Both pages of
// dup-raster hold the same raster bytes as two distinct objects, which pdfcpu's
// optimize pass deduplicates.
func TestExtractPageRasterHandlesDeduplicatedRasters(t *testing.T) {
	data := corpusDoc(t, "dup-raster")
	for _, page := range []int{1, 2} {
		img, err := ExtractPageRaster(bytes.NewReader(data), page)
		if err != nil {
			t.Fatalf("page %d: error = %v; want success", page, err)
		}
		if b := img.Bounds(); b.Dx() != corpus.ScanImageW || b.Dy() != corpus.ScanImageH {
			t.Errorf("page %d: raster = %dx%d; want %dx%d",
				page, b.Dx(), b.Dy(), corpus.ScanImageW, corpus.ScanImageH)
		}
	}
}

func TestExtractPageRasterOutOfRange(t *testing.T) {
	data := corpusDoc(t, "scan")
	for _, n := range []int{0, 2, -1} {
		if _, err := ExtractPageRaster(bytes.NewReader(data), n); err == nil {
			t.Errorf("ExtractPageRaster(page %d): want an error, got nil", n)
		}
	}
}

func TestExtractPageRasterMalformed(t *testing.T) {
	data := corpusDoc(t, "malformed")
	if _, err := ExtractPageRaster(bytes.NewReader(data), 1); err == nil {
		t.Fatal("ExtractPageRaster(malformed): want an error, got nil")
	}
}

// Every reason classify can return, plus the codec reason, must map to a coarse
// class. PageProvenance.Diverted stores the coarse form and capabilityRules
// matches on it (upgrade.go), so an unmapped reason would silently become an
// upgrade blind spot.
func TestDivertClassCoversEveryReason(t *testing.T) {
	want := map[string]string{
		"no-image":           "not-single-raster",
		"has-text":           "not-single-raster",
		"multiple-images":    "not-single-raster",
		"inline-image":       "not-single-raster",
		"vector-paint":       "not-single-raster",
		"shading":            "not-single-raster",
		"unresolved-xobject": "not-single-raster",
		"rotated-placement":  "not-single-raster",
		"not-page-covering":  "not-single-raster",
		"unsupported-codec":  "unsupported-codec",
	}
	for reason, class := range want {
		if got := divertClass(reason); got != class {
			t.Errorf("divertClass(%q) = %q; want %q", reason, got, class)
		}
	}
	if got := divertClass("something-new"); got != "not-single-raster" {
		t.Errorf("divertClass(unknown) = %q; want the conservative default", got)
	}
}

func TestClassify(t *testing.T) {
	page := pdfdocRect(0, 0, 612, 792)
	full := contentBox(0, 0, 612, 792)

	tests := []struct {
		name string
		scan *contentScan
		want string
	}{
		{"clean scan", &contentScan{Images: onePlacement(full)}, ""},
		{"no image at all", &contentScan{}, "no-image"},
		{"text present", &contentScan{Images: onePlacement(full), TextOps: 1}, "has-text"},
		{"two images", &contentScan{Images: twoPlacements(full)}, "multiple-images"},
		{"inline image", &contentScan{Images: onePlacement(full), InlineImgs: 1}, "inline-image"},
		{"painted path", &contentScan{Images: onePlacement(full), PaintOps: 1}, "vector-paint"},
		{"shading", &contentScan{Images: onePlacement(full), ShadingOps: 1}, "shading"},
		{"unresolved name", &contentScan{Images: onePlacement(full), Unresolved: []string{"X"}}, "unresolved-xobject"},
		{"rotated placement", &contentScan{Images: rotatedPlacement()}, "rotated-placement"},
		{"image covers only half the page", &contentScan{Images: onePlacement(contentBox(0, 0, 306, 792))}, "not-page-covering"},
		{"half a point of slack is tolerated", &contentScan{Images: onePlacement(contentBox(0.5, 0.5, 611.5, 791.5))}, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := classify(page, tc.scan); got != tc.want {
				t.Errorf("classify() = %q; want %q", got, tc.want)
			}
		})
	}
}
```

Add these test helpers at the bottom of `extract_test.go`, so the table above reads cleanly:

```go
type contentScan = content.Scan

func pdfdocRect(llx, lly, urx, ury float64) pdfdoc.Rect {
	return pdfdoc.Rect{LLX: llx, LLY: lly, URX: urx, URY: ury}
}

func contentBox(llx, lly, urx, ury float64) content.Box {
	return content.Box{LLX: llx, LLY: lly, URX: urx, URY: ury}
}

func onePlacement(b content.Box) []content.Placement {
	return []content.Placement{{
		Name: "Im0", ID: 1, Box: b,
		CTM: content.Matrix{b.URX - b.LLX, 0, 0, b.URY - b.LLY, b.LLX, b.LLY},
	}}
}

func twoPlacements(b content.Box) []content.Placement {
	return append(onePlacement(b), onePlacement(b)...)
}

func rotatedPlacement() []content.Placement {
	// A 90-degree rotation: a and d are zero, b and c are not.
	return []content.Placement{{
		Name: "Im0", ID: 1,
		CTM: content.Matrix{0, 792, -612, 0, 612, 0},
		Box: contentBox(0, 0, 612, 792),
	}}
}
```

and add `"github.com/dobbo-ca/byblos/internal/content"` and `"github.com/dobbo-ca/byblos/internal/pdfdoc"` to the test file's imports.

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test . -run 'TestExtract|TestClassify' -v`
Expected: FAIL — `undefined: ExtractPageRaster`.

- [ ] **Step 3: Implement extraction and classification**

Create `extract.go`:

```go
package byblos

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	"io"
	"math"

	_ "image/jpeg"
	_ "image/png"

	"github.com/dobbo-ca/byblos/internal/content"
	"github.com/dobbo-ca/byblos/internal/pdfdoc"
)

// ErrNotSingleRaster reports a page that is not one page-covering image:
// tiled rasters, vector content, or image-plus-overlay. The wrapped message
// names the specific reason. Callers divert such documents for review; design
// spec section 2 explains why detecting rather than rendering is the whole
// reason this project is tractable.
var ErrNotSingleRaster = errors.New("byblos: page is not a single page-covering raster")

// ErrUnsupportedImageCodec reports a page raster stored in a codec Byblos
// cannot decode: JBIG2, JPEG 2000, or CMYK images pdfcpu re-renders as TIFF.
//
// This error exists because of a specific correctness trap: pdfcpu does not
// error on JBIG2Decode or JPXDecode, it returns the raw opaque bytes. Handing
// those to an image decoder would either fail obscurely or, worse, appear to
// work. Byblos names the case instead.
var ErrUnsupportedImageCodec = errors.New("byblos: page raster uses an image codec byblos cannot decode")

// coverTolerancePt is how far a placement may fall short of the page box and
// still count as page-covering. One point at 300 DPI is about four pixels.
//
// This constant is an engineering choice made against a synthetic corpus. If
// the divert-rate instrumentation shows "not-page-covering" is a common reason
// on real scans, revisit it here before revisiting the design.
const coverTolerancePt = 1.0

// skewTolerance is how far a placement matrix's off-diagonal terms may stray
// from zero before the image is treated as rotated or sheared.
const skewTolerance = 1e-6

// ExtractPageRaster returns the single page-covering raster of the given
// 1-based page.
//
// It returns an error wrapping ErrNotSingleRaster when the page is anything
// else, and ErrUnsupportedImageCodec when the raster's codec is not decodable.
//
// Note on rotation: a page's /Rotate is a display attribute and does not affect
// content space, so a rotated page still extracts cleanly. The returned image
// is the raster as stored; applying /Rotate is the caller's business.
func ExtractPageRaster(r io.ReadSeeker, page int) (image.Image, error) {
	countAttempt()

	d, err := pdfdoc.Open(r)
	if err != nil {
		countFailure()
		return nil, err
	}
	p, err := d.Page(page)
	if err != nil {
		countFailure()
		return nil, err
	}
	_, scan, err := inspectPage(d, page)
	if err != nil {
		countFailure()
		return nil, err
	}

	if reason := classify(p.CropBox, scan); reason != "" {
		countDivert(reason)
		return nil, fmt.Errorf("%w: %s", ErrNotSingleRaster, reason)
	}

	id := scan.Images[0].ID
	data, fileType, err := d.RawImage(id)
	if err != nil {
		// pdfcpu declines to render some filters by returning a nil reader;
		// pdfdoc turns that into ErrUnsupportedCodec. That is a divert (the page
		// is understood, its codec is not), never a read failure.
		if errors.Is(err, pdfdoc.ErrUnsupportedCodec) {
			countDivert("unsupported-codec")
			return nil, fmt.Errorf("%w: %v", ErrUnsupportedImageCodec, err)
		}
		countFailure()
		return nil, err
	}
	switch fileType {
	case "jbig2", "jpx":
		// pdfcpu returns these as opaque bytes rather than erroring, so the
		// check has to happen here or the bytes look like a valid image.
		countDivert("unsupported-codec")
		return nil, fmt.Errorf("%w: %s", ErrUnsupportedImageCodec, fileType)
	}
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		// TIFF is what pdfcpu emits for CMYK rasters; golang.org/x/image/tiff
		// support arrives with B3.
		countDivert("unsupported-codec")
		return nil, fmt.Errorf("%w: %s: %v", ErrUnsupportedImageCodec, fileType, err)
	}
	countExtracted()
	return img, nil
}

// classify returns the reason a page cannot be treated as a single
// page-covering raster, or "" when it can.
//
// The order is deliberate: the first matching reason is the one reported, and
// it should be the most informative. A born-digital page has both no image and
// text; "no-image" says more.
//
// The returned strings are the keys of the divert counters, so changing one
// changes an operational metric. Do not rename them casually.
func classify(page pdfdoc.Rect, s *content.Scan) string {
	switch {
	case len(s.Images) == 0:
		return "no-image"
	case s.TextOps > 0:
		return "has-text"
	case len(s.Images) > 1:
		return "multiple-images"
	case s.InlineImgs > 0:
		return "inline-image"
	case s.PaintOps > 0:
		return "vector-paint"
	case s.ShadingOps > 0:
		return "shading"
	case len(s.Unresolved) > 0:
		return "unresolved-xobject"
	}
	m := s.Images[0].CTM
	if math.Abs(m[1]) > skewTolerance || math.Abs(m[2]) > skewTolerance {
		return "rotated-placement"
	}
	if !covers(s.Images[0].Box, page) {
		return "not-page-covering"
	}
	return ""
}

// covers reports whether box contains the page box, within tolerance. An image
// larger than the page is fine: it is simply cropped on display.
func covers(b content.Box, page pdfdoc.Rect) bool {
	return b.LLX <= page.LLX+coverTolerancePt &&
		b.LLY <= page.LLY+coverTolerancePt &&
		b.URX >= page.URX-coverTolerancePt &&
		b.URY >= page.URY-coverTolerancePt
}

// divertClass maps a fine-grained classify reason to the coarse class stored in
// PageProvenance.Diverted.
//
// Two vocabularies exist on purpose. The counters want detail, because their
// whole job is to say *why* the divert rate is what it is. The stored record
// wants only enough to answer "would re-processing help?", which is what
// capabilityRules in upgrade.go matches on — and a record written today has to
// stay meaningful when a later release renames a counter key.
//
// B5 writes the record; this is the single place that decides the mapping. An
// unrecognised reason falls back to the class that makes a renderer a candidate,
// because reporting a wasted re-run is cheaper than hiding a real upgrade —
// the same bias UpgradeCandidates takes for a capability with no rule.
func divertClass(reason string) string {
	if reason == "unsupported-codec" {
		return "unsupported-codec"
	}
	return "not-single-raster"
}
```

`divertClass` has no production caller until B5 writes provenance; its test is its only caller in B0/B1. That is intended — the point is that B5 has one place to change, not zero. `go vet` does not object. If `golangci-lint`'s `unused` linter flags it on your setup, confirm the linter is running with tests included rather than deleting the function.

The four `count*` helpers do not exist yet. Add temporary no-op stubs at the bottom of `extract.go` so this task compiles and its tests pass; Task 11 replaces them with the real counters:

```go
// Replaced by the real counters in stats.go (Task 11).
func countAttempt()             {}
func countExtracted()           {}
func countFailure()             {}
func countDivert(reason string) { _ = reason }
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test . -run 'TestExtract|TestClassify|TestDivertClass' -v`
Expected: PASS — three `TestExtractPageRasterSucceeds` subtests, four `TestExtractPageRasterDiverts` subtests, all eleven `TestClassify` subtests, and the JBIG2, dup-raster, per-page, out-of-range, malformed and `TestDivertClassCoversEveryReason` tests.

- [ ] **Step 5: Commit**

```bash
git add extract.go extract_test.go
git commit -m "feat: add ExtractPageRaster with page classification and divert reasons"
```

---

## Task 11: Divert-rate instrumentation

Design spec §2 is unambiguous: *"This divert rate must be instrumented from day one. The entire scope of this project rests on the premise that it is rare. If it turns out common, the premise is wrong and the design needs revisiting — better to learn that from a counter than from a user complaint."* FUTURE.md repeats it: a PDF renderer is started **only** if the measured rate says the case is common.

So this task ships two things: counters the embedding application can export, and a command that measures a directory of real PDFs.

**Files:**
- Create: `stats.go`, `cmd/byblos-divert/main.go`
- Modify: `extract.go` (delete the stub counters)
- Test: `stats_test.go`

**Interfaces:**
- Consumes: `ExtractPageRaster` (Task 10).
- Produces:

```go
type ExtractCounters struct {
    Attempted uint64
    Extracted uint64
    Diverted  uint64
    Failed    uint64
    Reasons   map[string]uint64
}

func ExtractStats() ExtractCounters
func ResetExtractStats()
func (c ExtractCounters) DivertRate() float64
```

- [ ] **Step 1: Write the failing test**

Create `stats_test.go`:

```go
package byblos

import (
	"bytes"
	"math"
	"testing"
)

// corpusDoc is declared in inspect_test.go (Task 9); this file needs no direct
// import of internal/corpus.

// These tests mutate package-level counters, so they must not run in parallel
// with anything that calls ExtractPageRaster.
func TestExtractStatsCountsOutcomes(t *testing.T) {
	ResetExtractStats()

	scan := corpusDoc(t, "scan")
	tiled := corpusDoc(t, "tiled")
	born := corpusDoc(t, "born-digital")
	bad := corpusDoc(t, "malformed")

	if _, err := ExtractPageRaster(bytes.NewReader(scan), 1); err != nil {
		t.Fatalf("scan: %v", err)
	}
	if _, err := ExtractPageRaster(bytes.NewReader(tiled), 1); err == nil {
		t.Fatal("tiled: want an error")
	}
	if _, err := ExtractPageRaster(bytes.NewReader(born), 1); err == nil {
		t.Fatal("born-digital: want an error")
	}
	if _, err := ExtractPageRaster(bytes.NewReader(bad), 1); err == nil {
		t.Fatal("malformed: want an error")
	}

	c := ExtractStats()
	if c.Attempted != 4 {
		t.Errorf("Attempted = %d; want 4", c.Attempted)
	}
	if c.Extracted != 1 {
		t.Errorf("Extracted = %d; want 1", c.Extracted)
	}
	if c.Diverted != 2 {
		t.Errorf("Diverted = %d; want 2", c.Diverted)
	}
	if c.Failed != 1 {
		t.Errorf("Failed = %d; want 1 (the malformed file is a failure, not a divert)", c.Failed)
	}
	if c.Reasons["multiple-images"] != 1 {
		t.Errorf("Reasons[multiple-images] = %d; want 1", c.Reasons["multiple-images"])
	}
	if c.Reasons["no-image"] != 1 {
		t.Errorf("Reasons[no-image] = %d; want 1", c.Reasons["no-image"])
	}
	if got, want := c.DivertRate(), 0.5; math.Abs(got-want) > 1e-9 {
		t.Errorf("DivertRate() = %v; want %v", got, want)
	}
}

func TestExtractStatsSnapshotIsACopy(t *testing.T) {
	ResetExtractStats()
	data := corpusDoc(t, "tiled")
	if _, err := ExtractPageRaster(bytes.NewReader(data), 1); err == nil {
		t.Fatal("want an error")
	}
	c := ExtractStats()
	c.Reasons["multiple-images"] = 999
	c.Attempted = 999
	if got := ExtractStats(); got.Reasons["multiple-images"] != 1 || got.Attempted != 1 {
		t.Error("mutating a snapshot changed the package counters")
	}
}

func TestDivertRateWithNoAttempts(t *testing.T) {
	ResetExtractStats()
	if got := ExtractStats().DivertRate(); got != 0 {
		t.Errorf("DivertRate() with no attempts = %v; want 0", got)
	}
}

func TestResetExtractStats(t *testing.T) {
	ResetExtractStats()
	data := corpusDoc(t, "scan")
	if _, err := ExtractPageRaster(bytes.NewReader(data), 1); err != nil {
		t.Fatal(err)
	}
	ResetExtractStats()
	c := ExtractStats()
	if c.Attempted != 0 || c.Extracted != 0 || len(c.Reasons) != 0 {
		t.Errorf("after ResetExtractStats: %+v; want zero", c)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test . -run 'TestExtractStats|TestDivertRate|TestResetExtractStats' -v`
Expected: FAIL — `undefined: ResetExtractStats`.

- [ ] **Step 3: Implement the counters**

Create `stats.go`:

```go
package byblos

import "sync"

// ExtractCounters is a snapshot of ExtractPageRaster outcomes since process
// start, or since the last ResetExtractStats.
//
// The design of Byblos rests on the premise that a page which is not a single
// page-covering raster is rare (design spec section 2). These counters are how
// that premise is checked against reality rather than assumed. Export
// DivertRate from your application; if it is not small, the premise is wrong
// and the design needs revisiting.
type ExtractCounters struct {
	Attempted uint64
	Extracted uint64
	Diverted  uint64            // page understood, but not a single page-covering raster
	Failed    uint64            // could not be read at all: damaged file, missing page
	Reasons   map[string]uint64 // divert reason to count; see classify in extract.go
}

// DivertRate is the fraction of attempted pages that diverted. Failures are
// excluded from the numerator but not the denominator: a document Byblos could
// not read is a different problem from one it read and declined.
func (c ExtractCounters) DivertRate() float64 {
	if c.Attempted == 0 {
		return 0
	}
	return float64(c.Diverted) / float64(c.Attempted)
}

var (
	statsMu sync.Mutex
	stats   = ExtractCounters{Reasons: map[string]uint64{}}
)

// ExtractStats returns a snapshot. The returned Reasons map is a copy, so the
// caller may keep or mutate it freely.
func ExtractStats() ExtractCounters {
	statsMu.Lock()
	defer statsMu.Unlock()
	out := stats
	out.Reasons = make(map[string]uint64, len(stats.Reasons))
	for k, v := range stats.Reasons {
		out.Reasons[k] = v
	}
	return out
}

// ResetExtractStats zeroes every counter. Intended for tests and for a
// long-lived process that reports per-batch rather than cumulative rates.
func ResetExtractStats() {
	statsMu.Lock()
	defer statsMu.Unlock()
	stats = ExtractCounters{Reasons: map[string]uint64{}}
}

func countAttempt() {
	statsMu.Lock()
	stats.Attempted++
	statsMu.Unlock()
}

func countExtracted() {
	statsMu.Lock()
	stats.Extracted++
	statsMu.Unlock()
}

func countFailure() {
	statsMu.Lock()
	stats.Failed++
	statsMu.Unlock()
}

func countDivert(reason string) {
	statsMu.Lock()
	stats.Diverted++
	stats.Reasons[reason]++
	statsMu.Unlock()
}
```

Delete the four stub functions at the bottom of `extract.go`.

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test . -v`
Expected: PASS. Note that `TestExtractStatsCountsOutcomes` and the extract tests share global counters; each stats test calls `ResetExtractStats` first, and none of them uses `t.Parallel()`. **Do not add `t.Parallel()` to any test in this package** — say so in a comment if you are tempted.

- [ ] **Step 5: Add the divert-rate command**

Create `cmd/byblos-divert/main.go`:

```go
// Command byblos-divert measures how often ExtractPageRaster declines a page.
//
// Design spec section 2 makes the entire scope of Byblos conditional on that
// case being rare, and FUTURE.md makes a PDF renderer conditional on this
// number. Run it over a real archive sample before anyone argues from
// intuition.
//
//	byblos-divert /path/to/pdfs
package main

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/dobbo-ca/byblos"
)

func main() {
	jsonOut := false
	args := os.Args[1:]
	if len(args) > 0 && args[0] == "-json" {
		jsonOut = true
		args = args[1:]
	}
	if len(args) != 1 {
		fmt.Fprintln(os.Stderr, "usage: byblos-divert [-json] <dir>")
		os.Exit(2)
	}

	var files, pages, unreadable int
	err := filepath.WalkDir(args[0], func(path string, e fs.DirEntry, err error) error {
		if err != nil || e.IsDir() || !strings.EqualFold(filepath.Ext(path), ".pdf") {
			return nil
		}
		files++
		f, err := os.Open(path)
		if err != nil {
			unreadable++
			return nil
		}
		defer f.Close()
		infos, err := byblos.Inspect(f)
		if err != nil {
			unreadable++
			fmt.Fprintf(os.Stderr, "inspect %s: %v\n", path, err)
			return nil
		}
		for _, pi := range infos {
			pages++
			if _, err := f.Seek(0, 0); err != nil {
				return nil
			}
			if _, err := byblos.ExtractPageRaster(f, pi.Index); err != nil {
				fmt.Fprintf(os.Stderr, "%s page %d: %v\n", path, pi.Index, err)
			}
		}
		return nil
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "byblos-divert:", err)
		os.Exit(1)
	}

	c := byblos.ExtractStats()
	if jsonOut {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(struct {
			Files, Pages, Unreadable int
			byblos.ExtractCounters
			DivertRate float64
		}{files, pages, unreadable, c, c.DivertRate()})
		return
	}

	fmt.Printf("files       %d\n", files)
	fmt.Printf("unreadable  %d\n", unreadable)
	fmt.Printf("pages       %d\n", c.Attempted)
	fmt.Printf("extracted   %d\n", c.Extracted)
	fmt.Printf("diverted    %d  (%.2f%%)\n", c.Diverted, 100*c.DivertRate())
	fmt.Printf("failed      %d\n", c.Failed)
	if len(c.Reasons) > 0 {
		fmt.Println("reasons:")
		keys := make([]string, 0, len(c.Reasons))
		for k := range c.Reasons {
			keys = append(keys, k)
		}
		sort.Slice(keys, func(i, j int) bool {
			if c.Reasons[keys[i]] != c.Reasons[keys[j]] {
				return c.Reasons[keys[i]] > c.Reasons[keys[j]]
			}
			return keys[i] < keys[j]
		})
		for _, k := range keys {
			fmt.Printf("  %-20s %d\n", k, c.Reasons[k])
		}
	}
}
```

- [ ] **Step 6: Run it against the corpus and record the baseline**

```bash
make corpus
go run ./cmd/byblos-divert testdata/corpus 2>/dev/null
```

Expected, exactly — every number below was produced by running this code over this corpus, not predicted:

```
files       11
unreadable  1
pages       12
extracted   6
diverted    6  (50.00%)
failed      0
reasons:
  no-image             2
  has-text             1
  multiple-images      1
  unsupported-codec    1
  vector-paint         1
```

Read it against the corpus before believing it:

- **files 11, unreadable 1.** Eleven `.pdf` files; `malformed.pdf` fails `Inspect`, so it is counted unreadable and never reaches `ExtractPageRaster`.
- **pages 12.** The ten readable documents contribute 12 pages, because `mixed` and `dup-raster` have two each.
- **extracted 6.** `scan`, `scan-rotated`, `scan-in-form`, `mixed` page 2, and both pages of `dup-raster`.
- **diverted 6.** `born-digital` and `mixed` page 1 as `no-image`; `tiled`, `overlay-text`, `overlay-vector`; and `jbig2` as `unsupported-codec`.
- **failed 0.** A file that cannot be read never gets counted as an attempt, so `unreadable` and `failed` measure different things and both are needed.

The corpus is deliberately adversarial — half its pages divert by construction — so this number says nothing about real documents. It is here to prove the instrument works.

Record that distinction on the epic, and record the measurement that actually matters as an explicit follow-up:

```bash
bd update byb-b1 --append-notes "Divert instrumentation live. Corpus baseline (adversarial by construction): 11 files, 1 unreadable, 12 pages, 6 extracted, 6 diverted 50.00% (no-image 2, has-text 1, multiple-images 1, unsupported-codec 1, vector-paint 1), 0 failed. REAL divert rate is still unmeasured: run 'byblos-divert' over a Kleio archive sample and record the result here before anyone argues for a renderer (FUTURE.md makes that work conditional on this number)."
```

- [ ] **Step 7: Commit**

```bash
git add stats.go stats_test.go extract.go cmd/byblos-divert
git commit -m "feat: instrument the ExtractPageRaster divert rate and add byblos-divert"
```

---

## Task 12: Poppler differential oracle

Design spec §8 pairs `Inspect` **and** `ExtractPageRaster` with `pdfinfo` and `pdfimages` on a fixed corpus. Poppler generates a **committed JSON golden**; the test compares against the golden, so `go test ./...` still passes on a machine with no poppler installed.

Both halves get an oracle, and they are different in kind:

- **`Inspect`** is checked against `pdfinfo` (page count, page size) and `pdfimages -list` (each stored image's pixel dimensions) and `pdftotext` (is there text at all).
- **`ExtractPageRaster`** is checked against `pdfimages -png`, pixel for pixel. This is the only independent evidence that pdfcpu's PNG re-render is *faithful* rather than merely well-formed; without it the sole assertion on an extracted raster is its width and height. Verified: for every page of this corpus that extracts, pdfcpu's render and poppler's are byte-identical after normalising both to RGBA.

What still has **no** oracle is **classification**. No poppler tool answers "is this page a single page-covering raster", so a page Byblos diverts is not a disagreement with poppler — it is a judgement poppler does not make. The raster test skips those pages explicitly. Task 10's corpus expectations are the oracle for classification, and this task does not pretend otherwise.

**Files:**
- Create: `testdata/oracle/gen.go`, `testdata/oracle/poppler.json`
- Test: `oracle_test.go`

**Interfaces:**
- Consumes: `Inspect` (Task 9), `ExtractPageRaster` (Task 10), `internal/corpus` (Task 5).
- Produces: `testdata/oracle/poppler.json`, committed.

- [ ] **Step 1: Check what is installed, and read the actual output format before parsing it**

```bash
pdfinfo -v 2>&1 | head -2; pdfimages -v 2>&1 | head -2; pdftotext -v 2>&1 | head -2
```

If poppler is absent: `brew install poppler`.

**Do not assume the column layout of `pdfimages -list`.** Read it:

```bash
make corpus
pdfimages -list testdata/corpus/scan.pdf
pdfinfo testdata/corpus/scan.pdf
```

`pdfimages -list` prints a two-line header followed by one row per image; the columns are whitespace-separated with `page`, `num`, `type`, `width`, `height`, `color`, `comp`, `bpc` among the first eight. **Write down the actual header you see and index by position from it.** If the header differs from what the generator below assumes, fix the generator — do not fix the expectation.

On poppler 26.06.0 the header has sixteen fields and so does every non-inline data row, which is what makes the generator's `len(f) < len(hdr)` guard safe; only an `[inline]` row would be dropped, and no corpus document has one.

The tool versions go into the golden so a regeneration mismatch is visible in the diff. **`pdfinfo -v` writes to stderr, not stdout** — verified:

```bash
pdfinfo -v 2>/dev/null   # prints nothing
pdfinfo -v 2>&1          # prints "pdfinfo version 26.06.0"
```

`exec.Command(...).Output()` captures stdout only, so the generator below uses `CombinedOutput` for the version probe and `Output` for everything it parses (mixing stderr into `pdfinfo`'s or `pdfimages -list`'s output would corrupt the parse).

- [ ] **Step 2: Write the golden generator**

Create `testdata/oracle/gen.go`:

```go
//go:build ignore

// Command gen regenerates testdata/oracle/poppler.json from the corpus in
// testdata/corpus. Manual step; run `make oracle`. Requires poppler. Never run
// in CI: the committed JSON is what CI compares against.
package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"image"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	_ "image/png"

	"github.com/dobbo-ca/byblos/internal/corpus"
)

type ImageRow struct {
	Page   int `json:"page"`
	Width  int `json:"width"`
	Height int `json:"height"`
	BPC    int `json:"bpc"`
}

// RasterRow is poppler's own rendering of the one image on a page, reduced to a
// hash of its pixels. This is what makes ExtractPageRaster differentially
// testable: a PNG that decodes is not the same claim as a PNG with the right
// pixels in it.
type RasterRow struct {
	Page   int    `json:"page"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
	Pixels string `json:"pixels_sha256"`
}

type DocOracle struct {
	Pages      int         `json:"pages"`
	PageWidth  float64     `json:"page_width_pt"`
	PageHeight float64     `json:"page_height_pt"`
	Images     []ImageRow  `json:"images"`
	Rasters    []RasterRow `json:"rasters,omitempty"`
	HasText    bool        `json:"has_text"`
	Error      string      `json:"error,omitempty"`
}

type Oracle struct {
	Tools     string               `json:"tools"`
	Documents map[string]DocOracle `json:"documents"`
}

func run(name string, args ...string) (string, error) {
	out, err := exec.Command(name, args...).Output()
	return string(out), err
}

// version reads a poppler tool's banner. It uses CombinedOutput because every
// poppler tool prints -v to STDERR; Output() would return an empty string and
// the golden would silently record "".
func version(name string) string {
	out, _ := exec.Command(name, "-v").CombinedOutput()
	return strings.TrimSpace(strings.SplitN(string(out), "\n", 2)[0])
}

// pixelHash normalises an image to 8-bit RGBA and hashes it. Normalising is
// what makes the comparison meaningful: poppler writes grey PNGs that decode to
// image.Gray while pdfcpu's decode to image.RGBA, and the pixels are identical.
//
// This function is duplicated in oracle_test.go on purpose. gen.go is a
// //go:build ignore program under testdata/ and shares no code with the
// package; the alternative is exporting an oracle helper from the shipped API.
// If you change one, change both — the golden is worthless otherwise.
func pixelHash(im image.Image) string {
	b := im.Bounds()
	h := sha256.New()
	fmt.Fprintf(h, "%dx%d;", b.Dx(), b.Dy())
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			r, g, bl, a := im.At(x, y).RGBA()
			h.Write([]byte{byte(r >> 8), byte(g >> 8), byte(bl >> 8), byte(a >> 8)})
		}
	}
	return hex.EncodeToString(h.Sum(nil))
}

func main() {
	o := Oracle{
		Tools:     strings.Join([]string{version("pdfinfo"), version("pdfimages"), version("pdftotext")}, "; "),
		Documents: map[string]DocOracle{},
	}
	tmp, err := os.MkdirTemp("", "byblos-oracle")
	if err != nil {
		fmt.Fprintln(os.Stderr, "gen:", err)
		os.Exit(1)
	}
	defer os.RemoveAll(tmp)

	for _, d := range corpus.All() {
		path := filepath.Join("testdata", "corpus", d.Name+".pdf")
		doc := DocOracle{}

		info, err := run("pdfinfo", path)
		if err != nil {
			doc.Error = "pdfinfo failed"
			o.Documents[d.Name] = doc
			continue
		}
		for _, line := range strings.Split(info, "\n") {
			key, val, ok := strings.Cut(line, ":")
			if !ok {
				continue
			}
			val = strings.TrimSpace(val)
			switch strings.TrimSpace(key) {
			case "Pages":
				doc.Pages, _ = strconv.Atoi(val)
			case "Page size":
				// "612 x 792 pts (letter)"
				f := strings.Fields(val)
				if len(f) >= 3 {
					doc.PageWidth, _ = strconv.ParseFloat(f[0], 64)
					doc.PageHeight, _ = strconv.ParseFloat(f[2], 64)
				}
			}
		}

		list, err := run("pdfimages", "-list", path)
		if err == nil {
			lines := strings.Split(strings.TrimRight(list, "\n"), "\n")
			if len(lines) > 2 {
				hdr := strings.Fields(lines[0])
				col := map[string]int{}
				for i, h := range hdr {
					col[h] = i
				}
				for _, line := range lines[2:] {
					f := strings.Fields(line)
					if len(f) < len(hdr) {
						continue
					}
					at := func(k string) int {
						i, ok := col[k]
						if !ok || i >= len(f) {
							return 0
						}
						v, _ := strconv.Atoi(f[i])
						return v
					}
					doc.Images = append(doc.Images, ImageRow{
						Page: at("page"), Width: at("width"), Height: at("height"), BPC: at("bpc"),
					})
				}
			}
		}

		// Poppler's own rendering of the page's raster, when the page has
		// exactly one. Pages with zero or several are skipped: there is nothing
		// for ExtractPageRaster to be compared against on those, and inventing
		// a rule here would duplicate classify badly.
		for page := 1; page <= doc.Pages; page++ {
			prefix := filepath.Join(tmp, fmt.Sprintf("%s-%d", d.Name, page))
			n := strconv.Itoa(page)
			if _, err := run("pdfimages", "-png", "-f", n, "-l", n, path, prefix); err != nil {
				continue
			}
			pngs, err := filepath.Glob(prefix + "-*.png")
			if err != nil || len(pngs) != 1 {
				continue
			}
			raw, err := os.ReadFile(pngs[0])
			if err != nil {
				continue
			}
			im, _, err := image.Decode(bytes.NewReader(raw))
			if err != nil {
				continue
			}
			doc.Rasters = append(doc.Rasters, RasterRow{
				Page:   page,
				Width:  im.Bounds().Dx(),
				Height: im.Bounds().Dy(),
				Pixels: pixelHash(im),
			})
		}

		if text, err := run("pdftotext", path, "-"); err == nil {
			doc.HasText = strings.TrimSpace(text) != ""
		}
		o.Documents[d.Name] = doc
	}

	buf, err := json.MarshalIndent(o, "", "  ")
	if err != nil {
		fmt.Fprintln(os.Stderr, "gen:", err)
		os.Exit(1)
	}
	out := filepath.Join("testdata", "oracle", "poppler.json")
	if err := os.WriteFile(out, append(buf, '\n'), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "gen:", err)
		os.Exit(1)
	}
	fmt.Println("wrote", out)
}
```

- [ ] **Step 3: Generate the golden and read it**

```bash
make oracle
cat testdata/oracle/poppler.json
```

Expected, verified against poppler 26.06.0:

- `"tools"` is three non-empty banners joined by `"; "`, e.g. `"pdfinfo version 26.06.0; pdfimages version 26.06.0; pdftotext version 26.06.0"`. **An empty string here means the version capture regressed to `Output()`** — that is the bug this field exists to make visible.
- Every document except `malformed` reports `612` x `792` and `"pages": 1`, except `mixed` and `dup-raster` which report 2. `malformed` reports `"error": "pdfinfo failed"` and `"pages": 0`.
- Image rows: `scan`, `scan-rotated`, `scan-in-form`, `overlay-text`, `overlay-vector` and `mixed` one 306x396 8-bpc image each; `dup-raster` two (one per page); `tiled` two 153x396; `jbig2` one 306x396 with `"bpc": 1`; `born-digital` none.
- Raster rows: `scan`, `scan-rotated`, `scan-in-form`, `overlay-text`, `overlay-vector`, `mixed` (page 2) and `dup-raster` (pages 1 and 2) each carry a 306x396 `pixels_sha256`. `born-digital`, `tiled`, `jbig2` and `malformed` carry none — no image, two images, a codec poppler will not write as PNG, and an unreadable file respectively. **`overlay-text` and `overlay-vector` having raster rows is correct**: poppler happily renders the image on those pages; Byblos diverts them for reasons poppler has no opinion about, and the test in Step 4 skips them for exactly that reason.
- `"has_text"` is true for `born-digital`, `mixed`, and `overlay-text`, false for the rest.

**If any field is empty or obviously wrong, fix the generator before writing the test.** A golden that encodes a parsing bug is worse than no golden.

- [ ] **Step 4: Write the differential test**

Create `oracle_test.go`:

```go
package byblos

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"math"
	"os"
	"path/filepath"
	"testing"

	"github.com/dobbo-ca/byblos/internal/corpus"
)

type oracleImage struct {
	Page   int `json:"page"`
	Width  int `json:"width"`
	Height int `json:"height"`
	BPC    int `json:"bpc"`
}

type oracleRaster struct {
	Page   int    `json:"page"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
	Pixels string `json:"pixels_sha256"`
}

type oracleDoc struct {
	Pages      int            `json:"pages"`
	PageWidth  float64        `json:"page_width_pt"`
	PageHeight float64        `json:"page_height_pt"`
	Images     []oracleImage  `json:"images"`
	Rasters    []oracleRaster `json:"rasters"`
	HasText    bool           `json:"has_text"`
	Error      string         `json:"error,omitempty"`
}

type oracleFile struct {
	Tools     string               `json:"tools"`
	Documents map[string]oracleDoc `json:"documents"`
}

func loadOracle(t *testing.T) oracleFile {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "oracle", "poppler.json"))
	if err != nil {
		t.Skipf("poppler golden not present (run make oracle): %v", err)
	}
	var o oracleFile
	if err := json.Unmarshal(raw, &o); err != nil {
		t.Fatalf("parsing the poppler golden: %v", err)
	}
	if o.Tools == "" {
		t.Fatal("the golden records no tool versions; regenerate with make oracle")
	}
	return o
}

// pixelHash must stay byte-for-byte identical to the one in
// testdata/oracle/gen.go. See the note there for why it is duplicated.
func pixelHash(im image.Image) string {
	b := im.Bounds()
	h := sha256.New()
	fmt.Fprintf(h, "%dx%d;", b.Dx(), b.Dy())
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			r, g, bl, a := im.At(x, y).RGBA()
			h.Write([]byte{byte(r >> 8), byte(g >> 8), byte(bl >> 8), byte(a >> 8)})
		}
	}
	return hex.EncodeToString(h.Sum(nil))
}

// Inspect must agree with poppler on page count, page size, and every image's
// pixel dimensions. Those are facts about the file, not judgements, so the
// comparison is exact.
func TestInspectAgreesWithPoppler(t *testing.T) {
	o := loadOracle(t)
	t.Logf("golden generated with %s", o.Tools)

	for _, d := range corpus.All() {
		want, ok := o.Documents[d.Name]
		if !ok {
			t.Errorf("golden has no entry for %q; regenerate with make oracle", d.Name)
			continue
		}
		t.Run(d.Name, func(t *testing.T) {
			got, err := Inspect(bytes.NewReader(d.Data))
			if want.Error != "" || want.Pages == 0 {
				if err == nil {
					t.Fatalf("poppler rejected %q but Inspect succeeded", d.Name)
				}
				return
			}
			if err != nil {
				t.Fatalf("Inspect() error = %v, but poppler read %d pages", err, want.Pages)
			}
			if len(got) != want.Pages {
				t.Fatalf("page count = %d; poppler says %d", len(got), want.Pages)
			}
			for _, pi := range got {
				if w := float64(pi.Bounds.Dx()); math.Abs(w-want.PageWidth) > 1 {
					t.Errorf("page %d width = %v pt; poppler says %v", pi.Index, w, want.PageWidth)
				}
				if h := float64(pi.Bounds.Dy()); math.Abs(h-want.PageHeight) > 1 {
					t.Errorf("page %d height = %v pt; poppler says %v", pi.Index, h, want.PageHeight)
				}
			}

			// Compare image pixel dimensions as multisets: poppler lists stored
			// image objects, Byblos lists paintings of them, and the two orders
			// need not agree.
			gotDims := map[[2]int]int{}
			for _, pi := range got {
				for _, im := range pi.Images {
					gotDims[[2]int{im.Width, im.Height}]++
				}
			}
			wantDims := map[[2]int]int{}
			for _, im := range want.Images {
				wantDims[[2]int{im.Width, im.Height}]++
			}
			for k, n := range wantDims {
				if gotDims[k] != n {
					t.Errorf("images %dx%d: Inspect found %d, pdfimages found %d",
						k[0], k[1], gotDims[k], n)
				}
			}
			for k, n := range gotDims {
				if wantDims[k] == 0 {
					t.Errorf("Inspect reported %d images of %dx%d that pdfimages did not list",
						n, k[0], k[1])
				}
			}

			// TextChars is a born-digital signal, so the oracle assertion is the
			// bi-conditional, not a character count: pdftotext normalises
			// whitespace and reading order, and matching its exact length would
			// assert poppler's formatting rather than our extraction.
			var chars int
			for _, pi := range got {
				chars += pi.TextChars
			}
			if (chars > 0) != want.HasText {
				t.Errorf("TextChars total = %d (text present: %v); pdftotext says text present: %v",
					chars, chars > 0, want.HasText)
			}
		})
	}
}

// ExtractPageRaster must return the same pixels poppler does. Dimensions alone
// would not catch a faithless re-render.
//
// A page Byblos diverts is skipped, not failed: poppler has no notion of "single
// page-covering raster", so `pdfimages -png` writing a file for overlay-text
// says nothing about whether that page should have been extracted.
func TestExtractedRasterMatchesPdfimages(t *testing.T) {
	o := loadOracle(t)
	compared := 0
	for _, d := range corpus.All() {
		want, ok := o.Documents[d.Name]
		if !ok {
			t.Errorf("golden has no entry for %q; regenerate with make oracle", d.Name)
			continue
		}
		for _, rr := range want.Rasters {
			img, err := ExtractPageRaster(bytes.NewReader(d.Data), rr.Page)
			if errors.Is(err, ErrNotSingleRaster) || errors.Is(err, ErrUnsupportedImageCodec) {
				t.Logf("%s page %d: diverted, no disagreement with poppler (%v)", d.Name, rr.Page, err)
				continue
			}
			if err != nil {
				t.Errorf("%s page %d: ExtractPageRaster error = %v, but pdfimages wrote a %dx%d PNG",
					d.Name, rr.Page, err, rr.Width, rr.Height)
				continue
			}
			if b := img.Bounds(); b.Dx() != rr.Width || b.Dy() != rr.Height {
				t.Errorf("%s page %d: raster %dx%d; pdfimages %dx%d",
					d.Name, rr.Page, b.Dx(), b.Dy(), rr.Width, rr.Height)
				continue
			}
			if got := pixelHash(img); got != rr.Pixels {
				t.Errorf("%s page %d: pixels %s; pdfimages %s", d.Name, rr.Page, got, rr.Pixels)
			}
			compared++
		}
	}
	// Without this, a regression that diverted every page would pass silently.
	if compared == 0 {
		t.Error("no page was compared against pdfimages; the oracle is vacuous")
	}
	t.Logf("compared %d pages against pdfimages", compared)
}

// Bitonal detection has no poppler equivalent worth parsing beyond the bpc
// column, so assert against that directly, in both directions: the corpus scan
// is 8-bit grey and must not be reported bitonal, the jbig2 document is 1-bit
// and must be.
func TestInspectBitonalFlagMatchesBitsPerComponent(t *testing.T) {
	o := loadOracle(t)
	for _, name := range []string{"scan", "jbig2"} {
		t.Run(name, func(t *testing.T) {
			want := o.Documents[name]
			// A golden with the wrong shape is a bug in the generator, not a
			// reason to skip: skipping here is how an empty image table goes
			// unnoticed.
			if len(want.Images) != 1 {
				t.Fatalf("golden for %s has %d image rows; want exactly 1 — regenerate with make oracle",
					name, len(want.Images))
			}
			pages, err := Inspect(bytes.NewReader(corpusDoc(t, name)))
			if err != nil {
				t.Fatalf("Inspect() error = %v", err)
			}
			if len(pages) != 1 || len(pages[0].Images) != 1 {
				t.Fatalf("Inspect() = %+v; want one page with one image", pages)
			}
			if got := pages[0].Images[0].Bitonal; got != (want.Images[0].BPC == 1) {
				t.Errorf("Bitonal = %v; pdfimages reports bpc %d", got, want.Images[0].BPC)
			}
		})
	}
}
```

- [ ] **Step 5: Run the tests, then prove they skip cleanly without the golden**

```bash
go test . -run 'TestInspectAgreesWithPoppler|TestInspectBitonalFlag|TestExtractedRasterMatchesPdfimages' -v
```

Expected: PASS, with one `TestInspectAgreesWithPoppler` subtest per corpus document, both `TestInspectBitonalFlagMatchesBitsPerComponent` subtests, and `TestExtractedRasterMatchesPdfimages` logging `compared 6 pages against pdfimages` plus two skipped diverts (`overlay-text`, `overlay-vector`).

```bash
mv testdata/oracle/poppler.json /tmp/poppler.json
go test ./... 2>&1 | tail -5
mv /tmp/poppler.json testdata/oracle/poppler.json
```

Expected: the whole suite still passes; the oracle tests report SKIP. This is the constraint that `go test ./...` must pass with no oracle installed.

- [ ] **Step 6: Commit**

```bash
git add testdata/oracle oracle_test.go
git commit -m "test: add the poppler differential oracle for Inspect"
```

---

## Final: verify the whole plan and close the epics

- [ ] **Step 1: Full verification**

```bash
cd /Users/christopherdobbyn/work/dobbo-ca/byblos
make build && make test && make lint
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build ./...
go list -f '{{range .Imports}}{{.}}{{"\n"}}{{end}}{{range .TestImports}}{{.}}{{"\n"}}{{end}}{{range .XTestImports}}{{.}}{{"\n"}}{{end}}' ./... \
  | sort -u | grep '\.' | grep -v '^github.com/dobbo-ca/byblos'
```

Expected: all builds and tests pass; the final command prints exactly these four lines and nothing else:

```
github.com/pdfcpu/pdfcpu/pkg/api
github.com/pdfcpu/pdfcpu/pkg/pdfcpu
github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model
github.com/pdfcpu/pdfcpu/pkg/pdfcpu/types
```

Any other module is a constraint violation — stop and raise it.

`make lint` runs `go vet` and, only if it is installed, `golangci-lint`. `go vet` is what CI enforces; a machine without golangci-lint prints a skip line and the chain still succeeds. That is the stated limitation from Task 1 Step 5, not a failure to investigate.

- [ ] **Step 2: Close byb-b1**

```bash
bd update byb-b1 --append-notes "B1 complete: Inspect returns per-page bounds, image placements (pixel dims + bitonal), and a text-character count that sees through Form XObjects. ExtractPageRaster returns ErrNotSingleRaster with a named reason for no-image / has-text / multiple-images / inline-image / vector-paint / shading / unresolved-xobject / rotated-placement / not-page-covering, and ErrUnsupportedImageCodec when the raster's codec is not decodable. TESTED: JBIG2, via a corpus document that carries a /JBIG2Decode raster, which also gives ImageRef.Bitonal its only positive case. NOT tested, asserted from pdfcpu source only: JPX and the CMYK-TIFF path, plus the nil-reader guard in pdfdoc for other unhandled filters; /ImageMask true is likewise uncovered, since a stencil mask is not extractable at all. pdfcpu is confined to internal/pdfdoc and arch_test.go enforces it (Imports, TestImports and XTestImports). Extraction renders from the stream dictionary resolved through the page's own resources, NOT via api.ExtractImagesRaw, which deduplicates identical rasters and would return page 1's object for page 2 — the dup-raster corpus document is the regression guard. Divert counters plus cmd/byblos-divert ship with the epic. Corpus of 11 documents generated from committed Go code; poppler goldens committed (including per-page pixel hashes from pdfimages -png, the differential oracle for ExtractPageRaster) and tests skip without them."
bd close byb-b1
```

- [ ] **Step 3: File the follow-ups this plan deliberately deferred**

```bash
bd create "Measure the real divert rate over a Kleio archive sample" -p 1 \
  -d "Run cmd/byblos-divert over a representative sample of real scanned PDFs and record the rate and reason histogram. Design spec section 2 makes the whole Byblos scope conditional on this being rare, and FUTURE.md makes the PDF renderer conditional on this number. Everything measured so far is on a synthetic corpus that is adversarial by construction."
bd create "Validate byblos against real-world damaged scans" -p 2 \
  -d "Every pdfcpu behaviour Byblos relies on was verified against synthetic, pdfcpu-clean or hand-written PDFs. Real scanner output exercises pdfcpu's relaxed-validation paths that were never hit. Collect a sample of genuinely messy PDFs and confirm Inspect and ExtractPageRaster behave: clean errors, no panics, no plausible-but-wrong parses."
bd create "Decide whether coverTolerancePt and the CropBox choice hold on real scans" -p 2 \
  -d "extract.go picks CropBox-else-MediaBox as the page box and allows 1.0 pt of slack before reporting not-page-covering. Both are engineering choices made against a synthetic corpus. If the divert histogram shows not-page-covering is common, retune here before concluding the design is wrong."
```

---

## Self-Review

**Scope coverage.** `byb-b0` asks for module scaffolding and CI (Task 1), a byblos-owned 1bpp Bitmap (Task 2), Provenance/PageProvenance plus `Capabilities()` (Task 3), and `UpgradeCandidates()` with the design spec §6 three-case table driving a table-driven test (Task 4 — the three rows are the first three subtests, verbatim, with nine more covering boundaries). `byb-b1` asks for `Inspect` returning `PageInfo` (Task 9), `ExtractPageRaster` returning `ErrNotSingleRaster` for tiled/vector/overlay pages (Task 10), both wrapping pdfcpu behind Byblos interfaces (Task 8, enforced by `arch_test.go`), and divert-rate instrumentation from day one (Task 11, plus a CLI so the number can actually be measured). The corpus (Task 5) covers born-digital, single-image scan, tiled, overlay, and malformed, with `scan-in-form` and `overlay-text` added as the form-recursion regression pair the research showed is necessary, `dup-raster` as the guard against pdfcpu's image deduplication, and `jbig2` as the only document that makes `ImageRef.Bitonal` true and the only one that reaches `ErrUnsupportedImageCodec`. Tasks 6 and 7 exist because pdfcpu has no content-stream parser and none of the above is possible without one.

**Deliberately out of scope, and why.** `EncodeJBIG2Generic` (B2), `QuantizePNG`/`Downsample`/JPEG (B3), `StampTextLayer` and the glyphless font (B4), `Optimize`/`ReadProvenance`/`WriteProvenance` (B5). `golang.org/x/image` is permitted but not added here, because nothing in B0/B1 needs it and an unused dependency in `go.mod` weakens the CI allowlist's signal.

**Placeholder scan.** Every code step contains real code — compilable as written, with no named-but-undefined helper — and every test step contains real assertions. Four places instruct verification instead of asserting a fact: Task 1 Step 1 (pdfcpu version and cgo-freeness — re-verify rather than trust a research note), Task 1 Step 5 (the `.golangci.yml` exclusions preset, which was never executed because golangci-lint is not installed on the authoring machine), Task 8 Steps 1–2 (pdfcpu signatures and the three behaviours not visible from signatures, each with the exact command and expected output, and an instruction to fix the code rather than guess if they differ), and Task 12 Step 1 (`pdfimages -list` column layout — read the header, index by position, and fix the generator rather than the expectation). Two places carry an explicit fallback if reality differs: Task 8 Step 6 (truncate `malformed()` harder rather than weakening the assertion) and Task 5 Step 6 (same, verified through poppler).

**Known coverage gaps, stated rather than papered over.** `ErrUnsupportedImageCodec` is exercised for JBIG2 only; the JPX and CMYK-TIFF branches, and `pdfdoc.ErrUnsupportedCodec`'s nil-reader guard, are argued from pdfcpu's source and not executed by any test. `ImageRef.Bitonal`'s `ImageMask` disjunct has no corpus case, because a stencil mask cannot be extracted at all. `content.Walk` ignores a Form XObject's `/BBox` clip. All four are documented at the code they affect and belong with the "validate against real-world damaged scans" follow-up.

**Type consistency.** `content.Env` is declared in Task 7 and implemented by `*pdfdoc.doc` in Task 8. `content.XObject.ID` is set by `pdfdoc.identify` (object number) and read back through `Placement.ID` into `pdfdoc.ImageInfo` in Task 9 — this is what keeps a form's `Im0` distinct from the page's `Im0`. `content.Scan` is produced by `Walk` in Task 7 and consumed by `inspectPage` (Task 9) and `classify` (Task 10). `pdfdoc.Rect` flows into `PageInfo.Bounds` through `rectOf` and into `covers` unchanged. `ExtractCounters` is written by the `count*` helpers Task 10 stubs and Task 11 implements — Task 10's step list says explicitly that the stubs are temporary and Task 11's step list says to delete them. The divert reason strings in `classify` are simultaneously the error message suffix asserted in Task 10 and the counter keys asserted in Task 11; a comment on `classify` says so. Those fine-grained strings reach `PageProvenance.Diverted` (Task 3) only through `divertClass` (Task 10), which is the one place B5 will need to touch and whose table test asserts every reason `classify` can return maps to a class `capabilityRules` (Task 4) matches. `pdfdoc.RawImage` takes the id that `content.Placement.ID` carries — not a page number — and `pdfdoc.ErrUnsupportedCodec` is translated into the public `ErrUnsupportedImageCodec` at exactly one site in `ExtractPageRaster`.

**Convention consistency.** PDF user space (y up, origin lower-left) is stated in Global Constraints, restated on `PageInfo`, `ImageRef`, `pdfdoc.Rect`, and `content.Box`, and is the frame every box comparison in `covers` and `classify` happens in. `Bitmap` uses the opposite convention, which is stated at its declaration and nowhere mixed with the first — `Bitmap` has no consumer in B0/B1 at all, which is itself the reason the two cannot collide yet. The 1-is-black bitonal convention is fixed in Task 2 and will be relied on by B2's JBIG2 encoder.

> **Correction (cross-plan review, 2026-07-27).** An earlier draft of this paragraph said the convention would be relied on "by whatever writes `/Decode [1 0]`". That is wrong, and following it would invert every scanned page. The B2 plan established empirically that a JBIG2-encoded image XObject must carry **no `/Decode` array at all**: the `JBIG2Decode` filter already yields 1-is-black, so an explicit `/Decode [1 0]` inverts it. Verified three ways against poppler — `pdfimages` matched in **zero** pixels with `/Decode [1 0]` present, `pdftoppm -r 72 -gray` rasterisation showed the border/centre polarity flipped, and jbig2dec's own PBM output agreed with the no-`/Decode` case. See "Verified facts" in `2026-07-27-byblos-b2-jbig2.md`. Whoever implements B5's PDF writing must not add a `/Decode` entry for JBIG2 images.
