# Byblos B2 — Lossless JBIG2 Generic Region Encoder

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Implement beads epic `byb-b2` — a lossless JBIG2 generic-region encoder (MQ arithmetic coder + GBTEMPLATE 0 template prediction + TPGD row prediction), wrapped in the embedded-file organization that the PDF `JBIG2Decode` filter requires.

**Architecture:** Four layers, each fully verified before the next is built.

1. **MQ arithmetic coder** (`internal/jbig2/mq.go`) — ITU-T T.88 Annex E.2. Built and tested in complete isolation against the spec's own 257-decision conformance vector. A subtly wrong arithmetic coder is close to undebuggable once it is buried under region coding, so it gets its own task, its own file, and a published test vector before a single pixel is touched.
2. **Generic region coder** (`internal/jbig2/generic.go`) — T.88 §6.2, GBTEMPLATE 0 with nominal AT pixels and TPGDON. Tested against a second published vector: T.88 Annex H.1 segment 11, whose input bitmap is recoverable from the *same annex's* MMR encoding of the identical figure.
3. **Segment writer** (`internal/jbig2/segment.go`) — T.88 §7.2, §7.4.1, §7.4.6, §7.4.8. Emits exactly two segments: page information (type 48) and immediate lossless generic region (type 39). Header and field bytes are checked against Annex H.1's hex dump.
4. **Public API and oracles** (`jbig2.go`, `*_test.go`) — `byblos.EncodeJBIG2Generic`, plus the acceptance oracle: encode → decode with an independent decoder → assert bit-identical.

**What this epic explicitly does NOT build:** no symbol dictionary, no text region, no refinement region, no halftone region, no MMR/CCITT fallback, no AT-pixel search, no GBTEMPLATE 1/2/3. **Lossy symbol matching is rejected on data-integrity grounds, not deferred** (`FUTURE.md`, design spec §5). If any step of the implementation appears to require glyph segmentation, symbol matching, or "near-identical" bitmap unification, **stop and raise it** — that is the failure mode this design exists to prevent, and no compression benchmark justifies reintroducing it.

**Tech Stack:** Go 1.26. `golang.org/x/image/ccitt` (decode only) is used in **tests** to recover a spec fixture. Test-only external oracles: `jbig2dec` (Artifex, AGPL-3.0 — invoked as a subprocess by golden *generation* and by opt-in round-trip tests, never linked and never shipped), `pdfimages` (poppler), `magick` + `tiffdump` (ImageMagick/libtiff).

---

## Global Constraints

- **Go 1.26.** `go.mod` declares `go 1.26`.
- **Module path:** `github.com/dobbo-ca/byblos`
- **No cgo.** Every package builds with `CGO_ENABLED=0`. CI enforces this.
- **Permitted dependencies: `github.com/pdfcpu/pdfcpu` and `golang.org/x/image` ONLY.** Anything else must be **raised, not added** — including test-only dependencies, which still land in `go.mod`. In particular: two pure-Go JBIG2 *decoder* modules exist (`github.com/xiaoqidun/jbig2`, `github.com/dkrisman/gobig2`, both Apache-2.0, both near-zero-star and unvalidated). Either would be a convenient round-trip oracle. **Neither may be added under this plan.** Task 8 uses external binaries instead, which is why the allow-list is not a problem. If a future task genuinely needs one, raise it as a separate decision.
- **byblos does NOT import cadmus and cadmus does NOT import byblos.** byblos owns its own 1bpp `Bitmap` type.
- **Apache-2.0.** Byblos is reimplemented from format specifications. It is **NOT** a port of OCRmyPDF (MPL-2.0) — never translate OCRmyPDF source. It is likewise not a translation of `jbig2enc` (Apache-2.0) or `jbig2dec` (AGPL-3.0): every file in `internal/jbig2` is original work written from ITU-T T.88 and ISO 32000-1, and carries no derivation header.
- **Test-only oracles** (`jbig2dec`, `pdfimages`, `magick`, `tiffdump`) generate committed fixtures/goldens and back opt-in cross-checks. **`go test ./...` must pass with none of them installed**: every test that needs one calls `exec.LookPath` and `t.Skipf`s when it is absent.
- **Bitonal convention, fixed here and relied on by every task:** in a byblos 1bpp bitmap, **1 means ink (black)**. This is also JBIG2's convention (T.88: 1 = black), so the encoder is a straight pass-through with no inversion anywhere. Task 9 proves this through poppler's own JBIG2 implementation reading a real PDF: `pdfimages` applies the `JBIG2Decode` filter and the `/Decode` array, which is what the polarity question turns on. It does **not** execute the content stream or the graphics pipeline; Task 9 Step 3 adds a short optional `pdftoppm` rasterisation on top for anyone who wants the full renderer path.
- Rows are packed **MSB-first**, `Stride` bytes per row, and **padding bits past the last pixel in a row are zero**.

---

## File Structure

| File | Responsibility |
|---|---|
| `go.mod`, `Makefile`, `.github/workflows/ci.yml` | Module scaffolding (Task 1; may already exist from `byb-b0`) |
| `internal/jbig2/doc.go` | Package doc, spec citations, scope boundary, licence note |
| `internal/jbig2/bitmap.go` | `Bitmap`: 1bpp MSB-first packed bitmap, the encoder's input |
| `internal/jbig2/mq.go` | Table E.1 and the MQ arithmetic encoder (T.88 Annex E.2) |
| `internal/jbig2/mq_test.go` | T.88 Annex H.2 conformance vector |
| `internal/jbig2/generic.go` | GBTEMPLATE 0 context, TPGD, `EncodeGenericRegion` |
| `internal/jbig2/generic_test.go` | Context unit tests; T.88 Annex H.1 segment 11 vector |
| `internal/jbig2/fixtures_test.go` | Deterministic test bitmaps shared across test files |
| `internal/jbig2/segment.go` | Segment headers, page info segment, generic region segment, `EmbeddedStream` |
| `internal/jbig2/segment_test.go` | Header and field goldens taken from T.88 Annex H.1 |
| `internal/jbig2/roundtrip_test.go` | `jbig2dec` round-trip oracle + committed byte goldens |
| `internal/jbig2/pdfembed_test.go` | Minimal PDF writer + poppler polarity proof |
| `internal/jbig2/compare_test.go` | CCITT G4 size comparison (bead acceptance criterion) |
| `jbig2.go` | Root API: `EncodeJBIG2Generic`, capability string |
| `jbig2_test.go` | Root API test |
| `testdata/jbig2/*.jb2` | Committed encoder byte goldens |

**Task dependency:** strictly sequential. Task N+1 assumes Task N is green and committed.

---

## Task 1: Package scaffolding, the `Bitmap` type, and the oracle install

`byb-b2` depends on `byb-b0`, which owns the module scaffolding and the root
`byblos.Bitmap`. At the time this plan was written **`byblos` had no `go.mod`**,
so B0 may not have landed. This task handles both cases explicitly rather than
assuming one.

`internal/jbig2` defines its **own** minimal bitmap struct rather than importing
the root package — the root package imports `internal/jbig2`, so the reverse
import would be a cycle. Task 7 adapts between the two.

**Files:**
- Create (if absent): `go.mod`, `Makefile`, `.github/workflows/ci.yml`
- Create: `internal/jbig2/doc.go`, `internal/jbig2/bitmap.go`
- Test: `internal/jbig2/bitmap_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces:

```go
// Bitmap is a 1-bit-per-pixel bitmap with MSB-first packed rows.
// A set bit (1) is ink (black), matching both byblos and JBIG2 convention.
type Bitmap struct {
	W, H   int
	Stride int    // bytes per row; (W+7)/8
	Pix    []byte // Stride*H bytes
}

func NewBitmap(w, h int) *Bitmap
func (b *Bitmap) Get(x, y int) int   // out of bounds returns 0 (T.88 6.2.5.2)
func (b *Bitmap) Set(x, y, v int)
func (b *Bitmap) MaskPadding()       // zeroes bits past W in every row
func (b *Bitmap) RowEqualAbove(y int) bool
```

- [ ] **Step 1: Establish the module and the build scaffolding**

```bash
cd /Users/christopherdobbyn/work/dobbo-ca/byblos
ls go.mod 2>/dev/null || go mod init github.com/dobbo-ca/byblos
cat go.mod
```

Then confirm `go.mod` declares the module path `github.com/dobbo-ca/byblos` and
a `go` line of `1.26` **or a `1.26.x` patch line** — `go mod init` on Go 1.26.4
writes `go 1.26.4`, which is correct and must not be edited down to `go 1.26`.

If `byb-b0` already created it with a `require` block, **leave that alone** —
just verify every entry is `github.com/pdfcpu/pdfcpu` or `golang.org/x/image`.
Anything else: stop and raise it.

Now the two scaffolding files. **Create each only if it is absent**; if `byb-b0`
already landed one, leave it and only check the property named below.

`Makefile` — check that `lint` runs both `gofmt` and `go vet`, and that the
Go invocations set `CGO_ENABLED=0`:

```make
.PHONY: test lint build

test:
	CGO_ENABLED=0 go test ./...

# gofmt is part of lint on purpose: `go vet` does not check formatting, and a
# gofmt-dirty file otherwise reaches a commit unnoticed.
lint:
	@test -z "$$(gofmt -l .)" || { echo "gofmt needed in:"; gofmt -l .; exit 1; }
	CGO_ENABLED=0 go vet ./...

build:
	CGO_ENABLED=0 go build ./...
```

`.github/workflows/ci.yml` — check that `CGO_ENABLED` is `"0"`, since the
Global Constraints say CI enforces the no-cgo rule and this file is what does
it. Task 8 Step 5 appends a second job to this file.

```yaml
name: ci

on:
  push:
    branches: [main]
  pull_request:

env:
  CGO_ENABLED: "0"

jobs:
  test:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with:
          go-version: '1.26'
      - name: gofmt
        run: test -z "$(gofmt -l .)" || { gofmt -l .; exit 1; }
      - run: go vet ./...
      - run: go build ./...
      # No decoder oracle is installed in this job, deliberately: it is what
      # proves every oracle-backed test skips cleanly instead of failing.
      - run: go test ./... -v
```

- [ ] **Step 2: Check the root `Bitmap` contract against `byb-b0`**

```bash
grep -rn "type Bitmap struct" --include=*.go /Users/christopherdobbyn/work/dobbo-ca/byblos
```

Two outcomes, both fine:

- **No match** — `byb-b0` has not landed. Proceed; Task 7 will define the
  adapter against whatever B0 eventually ships, and its test will fail loudly if
  the layouts disagree.
- **A match in the repo root** — read it. Record in the Task 7 notes whether its
  fields are `(Width, Height, Stride, Pix)` or `(W, H, Stride, Pix)`, whether
  `Pix` is MSB-first packed, and whether 1 means ink. **Do not edit B0's type to
  suit this task**, and do not assume it matches; Task 7 converts.

- [ ] **Step 3: Add the test-only decoder oracle**

`jbig2dec` is the acceptance oracle for the whole epic. It is AGPL-3.0, which is
fine for a subprocess invoked by tests and never linked or shipped — the same
arrangement the design spec §8 already describes for `pdfinfo`/`pdftotext`.

```bash
HOMEBREW_NO_AUTO_UPDATE=1 brew install jbig2dec
jbig2dec --version
jbig2dec --help | grep -- --embedded
```

Expected: a version line, and `-e --embedded   expect embedded bit stream without file header`.

The other oracles are probably already present; confirm:

```bash
which pdfimages magick tiffdump || echo "some oracles missing - tests will skip"
```

- [ ] **Step 4: Add `golang.org/x/image`**

Needed by `generic_test.go` in Task 4 to MMR-decode a fixture out of the spec.
It is on the permitted list.

```bash
go get golang.org/x/image@v0.44.0
go list -m golang.org/x/image
```

Expected: `golang.org/x/image v0.44.0`. (v0.44.0 is the version this plan's
fixtures were verified against. A later version is acceptable if Task 4's test
still passes; if it does not, pin back to v0.44.0 and raise the difference.)

- [ ] **Step 5: Write the failing test**

Create `internal/jbig2/bitmap_test.go`:

```go
package jbig2

import (
	"bytes"
	"testing"
)

func TestBitmapSetGetRoundTrip(t *testing.T) {
	b := NewBitmap(13, 5)
	if b.Stride != 2 {
		t.Fatalf("Stride = %d; want 2 for width 13", b.Stride)
	}
	if len(b.Pix) != 10 {
		t.Fatalf("len(Pix) = %d; want 10", len(b.Pix))
	}
	pts := [][2]int{{0, 0}, {12, 4}, {7, 2}, {8, 0}}
	for _, p := range pts {
		b.Set(p[0], p[1], 1)
	}
	for _, p := range pts {
		if got := b.Get(p[0], p[1]); got != 1 {
			t.Errorf("Get(%d,%d) = %d; want 1", p[0], p[1], got)
		}
	}
	if got := b.Get(1, 1); got != 0 {
		t.Errorf("Get(1,1) = %d; want 0", got)
	}
	b.Set(7, 2, 0)
	if got := b.Get(7, 2); got != 0 {
		t.Errorf("Get(7,2) after clear = %d; want 0", got)
	}
}

// T.88 6.2.5.2: pixels outside the bitmap are 0. No replication, no wrap.
func TestBitmapOutOfBoundsIsZero(t *testing.T) {
	b := NewBitmap(4, 4)
	for y := 0; y < 4; y++ {
		for x := 0; x < 4; x++ {
			b.Set(x, y, 1)
		}
	}
	for _, p := range [][2]int{{-1, 0}, {0, -1}, {4, 0}, {0, 4}, {-1, -1}, {99, 99}} {
		if got := b.Get(p[0], p[1]); got != 0 {
			t.Errorf("Get(%d,%d) = %d; want 0 out of bounds", p[0], p[1], got)
		}
	}
}

// MSB-first packing: pixel 0 is bit 0x80 of byte 0.
func TestBitmapPackingIsMSBFirst(t *testing.T) {
	b := NewBitmap(8, 1)
	b.Set(0, 0, 1)
	if b.Pix[0] != 0x80 {
		t.Errorf("Pix[0] = %#02x after Set(0,0,1); want 0x80", b.Pix[0])
	}
	b.Set(7, 0, 1)
	if b.Pix[0] != 0x81 {
		t.Errorf("Pix[0] = %#02x after Set(7,0,1); want 0x81", b.Pix[0])
	}
}

func TestBitmapMaskPaddingClearsBitsPastWidth(t *testing.T) {
	b := NewBitmap(5, 2)
	b.Pix[0] = 0xFF
	b.Pix[1] = 0xFF
	b.MaskPadding()
	if b.Pix[0] != 0xF8 {
		t.Errorf("Pix[0] = %#02x after MaskPadding; want 0xf8 (5 pixels wide)", b.Pix[0])
	}
	if b.Pix[1] != 0xF8 {
		t.Errorf("Pix[1] = %#02x after MaskPadding; want 0xf8", b.Pix[1])
	}
	for x := 0; x < 5; x++ {
		if b.Get(x, 0) != 1 {
			t.Errorf("MaskPadding cleared visible pixel (%d,0)", x)
		}
	}
}

// A Stride larger than the minimal (W+7)/8 leaves whole bytes past the last
// visible pixel. Get never reads them, so they cost no correctness -- but
// RowEqualAbove compares whole strides, so leaving them dirty silently stops
// TPGD from firing. MaskPadding must clear them as well as the padding bits
// inside the byte that holds pixel W-1.
func TestBitmapMaskPaddingHandlesNonMinimalStride(t *testing.T) {
	b := &Bitmap{W: 12, H: 2, Stride: 4, Pix: []byte{
		0xFF, 0xFF, 0xFF, 0xFF,
		0xFF, 0xFF, 0xFF, 0xFF,
	}}
	b.MaskPadding()
	want := []byte{0xFF, 0xF0, 0x00, 0x00, 0xFF, 0xF0, 0x00, 0x00}
	if !bytes.Equal(b.Pix, want) {
		t.Fatalf("MaskPadding on a stride-4 12-pixel-wide bitmap = % 02X; want % 02X", b.Pix, want)
	}
	for x := 0; x < 12; x++ {
		if b.Get(x, 0) != 1 {
			t.Errorf("MaskPadding cleared visible pixel (%d,0)", x)
		}
	}
	if !b.RowEqualAbove(1) {
		t.Error("RowEqualAbove(1) = false; want true once MaskPadding has equalised the strides")
	}
}

// Row 0 is compared against an implicit all-zero row above the bitmap.
func TestBitmapRowEqualAbove(t *testing.T) {
	b := NewBitmap(9, 4)
	// row 0 all zero, row 1 all zero, row 2 has a pixel, row 3 same as row 2.
	b.Set(3, 2, 1)
	b.Set(3, 3, 1)
	want := []bool{true, true, false, true}
	for y, w := range want {
		if got := b.RowEqualAbove(y); got != w {
			t.Errorf("RowEqualAbove(%d) = %v; want %v", y, got, w)
		}
	}
}

func TestNewBitmapRejectsNonPositiveDimensions(t *testing.T) {
	for _, d := range [][2]int{{0, 5}, {5, 0}, {-1, 5}} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("NewBitmap(%d,%d): want panic, got none", d[0], d[1])
				}
			}()
			NewBitmap(d[0], d[1])
		}()
	}
}
```

- [ ] **Step 6: Run the test to verify it fails**

Run: `go test ./internal/jbig2/ -v`
Expected: FAIL — `undefined: NewBitmap`.

- [ ] **Step 7: Implement the package doc and the bitmap**

Create `internal/jbig2/doc.go`:

```go
// Package jbig2 implements a lossless JBIG2 generic-region encoder.
//
// Scope is deliberately narrow. It implements only:
//
//   - the MQ arithmetic coder of ITU-T T.88 (02/2000) | ISO/IEC 14492:2001,
//     Annex E.2;
//   - the generic region coding procedure of T.88 6.2 with GBTEMPLATE 0,
//     nominal AT pixels, and TPGDON, run in the encoding direction;
//   - the segment syntax of T.88 7.2, 7.4.1, 7.4.6 and 7.4.8, emitted in the
//     embedded file organization that ISO 32000-1:2008 7.4.7 requires of the
//     PDF JBIG2Decode filter.
//
// It implements NO symbol dictionary, NO text region, NO refinement region, NO
// halftone region and NO MMR fallback. Generic-region coding is bit-exact by
// construction: the decoded bitmap is always identical to the encoded one, so
// no character can ever be substituted for another. Lossy symbol matching --
// the mechanism behind the 2013 Xerox scanner defect -- is rejected outright
// for this reason, not merely deferred. See FUTURE.md.
//
// This package is original work written from the published specifications. It
// is not a translation of jbig2enc, jbig2dec, OCRmyPDF, or any other
// implementation.
package jbig2
```

Create `internal/jbig2/bitmap.go`:

```go
package jbig2

import "fmt"

// Bitmap is a 1-bit-per-pixel bitmap with MSB-first packed rows: pixel x of a
// row lives in bit 0x80>>(x%8) of byte x/8. A set bit is ink (black), which is
// also JBIG2's convention, so no inversion happens anywhere in this package.
//
// Bits past W in the final byte of a row are padding and must be zero; use
// MaskPadding to enforce that on a bitmap built by other means.
type Bitmap struct {
	W, H   int
	Stride int
	Pix    []byte
}

// NewBitmap returns an all-background bitmap. It panics on non-positive
// dimensions: a zero-pixel region is not representable in a JBIG2 region
// segment, and silently producing one would emit an undecodable stream.
func NewBitmap(w, h int) *Bitmap {
	if w <= 0 || h <= 0 {
		panic(fmt.Sprintf("jbig2: NewBitmap(%d, %d): dimensions must be positive", w, h))
	}
	s := (w + 7) / 8
	return &Bitmap{W: w, H: h, Stride: s, Pix: make([]byte, s*h)}
}

// Get returns the pixel at (x, y), or 0 if (x, y) lies outside the bitmap.
// T.88 6.2.5.2 requires out-of-bounds template pixels to read as 0, with no
// edge replication and no wrapping; returning 0 here is what implements that.
func (b *Bitmap) Get(x, y int) int {
	if x < 0 || y < 0 || x >= b.W || y >= b.H {
		return 0
	}
	return int(b.Pix[y*b.Stride+x/8]>>(7-uint(x)%8)) & 1
}

// Set writes the pixel at (x, y). Coordinates must be in bounds.
func (b *Bitmap) Set(x, y, v int) {
	i := y*b.Stride + x/8
	m := byte(0x80 >> (uint(x) % 8))
	if v != 0 {
		b.Pix[i] |= m
	} else {
		b.Pix[i] &^= m
	}
}

// MaskPadding zeroes everything in a row past the last visible pixel: the
// padding bits inside the byte that holds pixel W-1, and every whole byte
// between there and Stride. Those trailing bytes exist whenever Stride is
// larger than the minimal (W+7)/8, which this package accepts.
//
// Get never reads any of it, so none of it can cost correctness. RowEqualAbove
// compares whole strides, though, so stray bits there make two visually
// identical rows compare unequal and TPGD stops firing. Measured on a 100x200
// bordered bitmap with Stride 16: 12 bytes with the trailing bytes cleared,
// 28 bytes with them left dirty.
func (b *Bitmap) MaskPadding() {
	last := (b.W - 1) / 8 // the byte holding the last visible pixel
	mask := byte(0xFF)
	if rem := b.W % 8; rem != 0 {
		mask = byte(0xFF << (8 - uint(rem)))
	}
	for y := 0; y < b.H; y++ {
		row := b.Pix[y*b.Stride : (y+1)*b.Stride]
		row[last] &= mask
		clear(row[last+1:])
	}
}

// RowEqualAbove reports whether row y is identical to row y-1. Row 0 is
// compared against the implicit all-zero row above the bitmap, matching the
// out-of-bounds rule. This is the predicate that drives TPGD.
func (b *Bitmap) RowEqualAbove(y int) bool {
	cur := b.Pix[y*b.Stride : (y+1)*b.Stride]
	if y == 0 {
		for _, v := range cur {
			if v != 0 {
				return false
			}
		}
		return true
	}
	prev := b.Pix[(y-1)*b.Stride : y*b.Stride]
	for i := range prev {
		if prev[i] != cur[i] {
			return false
		}
	}
	return true
}
```

- [ ] **Step 8: Run the test to verify it passes**

Run: `go test ./internal/jbig2/ -v`
Expected: PASS, all seven tests.

- [ ] **Step 9: Verify the no-cgo constraint and the format gate**

```bash
make build lint
```

Expected: exit status 0, with no gofmt findings and no vet findings. This also
proves the Makefile written in Step 1 works, since Task 8 extends it.

- [ ] **Step 10: Commit**

```bash
git add go.mod go.sum Makefile .github/workflows/ci.yml internal/jbig2
git commit -m "feat(jbig2): add the 1bpp Bitmap substrate for generic region coding"
```

Drop `Makefile` and `.github/workflows/ci.yml` from the `git add` if `byb-b0`
already owned them and Step 1 left them untouched.

---

## Task 2: MQ arithmetic coder

**This task is deliberately isolated.** The MQ coder is a 16-bit interval
subdivider with carry propagation, byte stuffing and a two-stage flush. Every
one of those is a place where an off-by-one produces output that is *plausible*
— right length, right shape — and wrong. Debugging that through a 65536-entry
context array and a template predictor is close to impossible. T.88 Annex H.2
publishes a 256-decision vector with its exact 30-byte output, and Table H.1
traces all 257 register states; that vector is the entire acceptance criterion
for this task, and nothing downstream is written until it passes.

The lineage is the same MQ coder as JPEG 2000 (T.800 Annex C) and descends from
the QM coder of JBIG1/JPEG. **Do not lift an implementation from a J2K codebase:**
T.800 permits termination variants (predictable termination, per-pass flush)
that T.88 does not use. Take BYTEOUT and FLUSH from T.88 E.2.7/E.2.9 as written.

**Files:**
- Create: `internal/jbig2/mq.go`
- Test: `internal/jbig2/mq_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces:

```go
// contexts is one byte per context: index<<1 | mps.
type contexts []byte

type encoder struct{ /* ... */ }

func newEncoder() *encoder
func (e *encoder) encode(cx contexts, i, d int)  // T.88 E.2.3 CODE0/CODE1
func (e *encoder) flush() []byte                 // T.88 E.2.9 FLUSH
```

- [ ] **Step 1: Write the failing test**

Both byte strings below were read directly out of the spec text (T.88 Annex H.2,
doc p. 142) and the vector was verified end-to-end against a working prototype
while this plan was written — **the expected output is known-correct, so if your
implementation disagrees, your implementation is wrong.**

Create `internal/jbig2/mq_test.go`:

```go
package jbig2

import (
	"bytes"
	"testing"
)

// Table E.1 has exactly 47 rows. Index 46 is the fixed 0.5 estimate: it is its
// own NMPS and NLPS, so once reached the state never leaves.
func TestQeTableShape(t *testing.T) {
	if len(qeTable) != 47 {
		t.Fatalf("len(qeTable) = %d; want 47", len(qeTable))
	}
	for i, e := range qeTable {
		if e.qe == 0 || e.qe > 0x5601 {
			t.Errorf("qeTable[%d].qe = %#04x; want a value in (0, 0x5601]", i, e.qe)
		}
		if int(e.nmps) >= len(qeTable) {
			t.Errorf("qeTable[%d].nmps = %d; out of range", i, e.nmps)
		}
		if int(e.nlps) >= len(qeTable) {
			t.Errorf("qeTable[%d].nlps = %d; out of range", i, e.nlps)
		}
		if e.swch > 1 {
			t.Errorf("qeTable[%d].swch = %d; want 0 or 1", i, e.swch)
		}
	}
	// SWITCH is set on exactly the three rows T.88 marks: 0, 6 and 14.
	for _, i := range []int{0, 6, 14} {
		if qeTable[i].swch != 1 {
			t.Errorf("qeTable[%d].swch = 0; want 1", i)
		}
	}
	if qeTable[46].nmps != 46 || qeTable[46].nlps != 46 {
		t.Errorf("qeTable[46] transitions = (%d,%d); want (46,46)", qeTable[46].nmps, qeTable[46].nlps)
	}
}

// TestMQConformanceVector is the T.88 Annex H.2 test sequence: 32 bytes of
// decisions (256 decisions, MSB first) coded through a single context starting
// at I=0, MPS=0, producing exactly 30 bytes.
//
// On failure, do NOT adjust the expected bytes. Instrument encode() to log
// (I, MPS, Qe, A, C, CT, B) after every decision and diff against Table H.1
// (T.88 doc p. 143-146), which traces all 257 events. The first row that
// differs is the bug.
func TestMQConformanceVector(t *testing.T) {
	in := []byte{
		0x00, 0x02, 0x00, 0x51, 0x00, 0x00, 0x00, 0xC0,
		0x03, 0x52, 0x87, 0x2A, 0xAA, 0xAA, 0xAA, 0xAA,
		0x82, 0xC0, 0x20, 0x00, 0xFC, 0xD7, 0x9E, 0xF6,
		0xBF, 0x7F, 0xED, 0x90, 0x4F, 0x46, 0xA3, 0xBF,
	}
	want := []byte{
		0x84, 0xC7, 0x3B, 0xFC, 0xE1, 0xA1, 0x43, 0x04,
		0x02, 0x20, 0x00, 0x00, 0x41, 0x0D, 0xBB, 0x86,
		0xF4, 0x31, 0x7F, 0xFF, 0x88, 0xFF, 0x37, 0x47,
		0x1A, 0xDB, 0x6A, 0xDF, 0xFF, 0xAC,
	}

	cx := make(contexts, 1)
	e := newEncoder()
	for _, b := range in {
		for i := 7; i >= 0; i-- {
			e.encode(cx, 0, int(b>>uint(i))&1)
		}
	}
	got := e.flush()

	if !bytes.Equal(got, want) {
		t.Fatalf("MQ output mismatch\ngot  (%d): % 02X\nwant (%d): % 02X",
			len(got), got, len(want), want)
	}
}

// Every MQ stream terminates with the 0xFF 0xAC marker written by FLUSH.
func TestMQFlushTerminator(t *testing.T) {
	cx := make(contexts, 1)
	e := newEncoder()
	for i := 0; i < 40; i++ {
		e.encode(cx, 0, i&1)
	}
	got := e.flush()
	if len(got) < 2 || got[len(got)-2] != 0xFF || got[len(got)-1] != 0xAC {
		t.Fatalf("stream tail = % 02X; want ... FF AC", got)
	}
}

// Byte stuffing after 0xFF emits only 7 bits, so no 0xFF byte may ever be
// followed by a byte above 0x8F inside the coded data. Violating this would
// create a marker sequence a decoder must treat as terminating the stream.
func TestMQNoMarkerSequenceInData(t *testing.T) {
	cx := make(contexts, 1<<8)
	e := newEncoder()
	s := uint32(1)
	for i := 0; i < 20000; i++ {
		s = s*1664525 + 1013904223
		e.encode(cx, int(s>>24)&0xFF, int(s>>16)&1)
	}
	got := e.flush()
	body := got[:len(got)-2] // exclude the FF AC terminator
	for i := 0; i+1 < len(body); i++ {
		if body[i] == 0xFF && body[i+1] > 0x8F {
			t.Fatalf("marker sequence FF %02X at offset %d", body[i+1], i)
		}
	}
}

// An encoder that has coded nothing still flushes a well-formed terminator.
func TestMQFlushWithNoDecisions(t *testing.T) {
	got := newEncoder().flush()
	if len(got) == 0 {
		t.Fatal("flush() with no decisions returned no bytes")
	}
	if got[len(got)-2] != 0xFF || got[len(got)-1] != 0xAC {
		t.Fatalf("flush() = % 02X; want a stream ending FF AC", got)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/jbig2/ -run TestMQ -v`
Expected: FAIL — `undefined: qeTable`, `undefined: newEncoder`.

- [ ] **Step 3: Implement the MQ encoder**

Create `internal/jbig2/mq.go`. The awkward part of the port is `INITENC`'s
`BP = BPST - 1` (E.2.8): the byte register `B` starts one *before* the output
buffer, so the first `BYTEOUT` writes at the buffer start, and the carry path in
`BYTEOUT` increments the *previous* byte. Model this as a one-byte leading
sentinel that is sliced off in `flush`, not as a negative index or a special
case — the special case is where this goes wrong.

```go
package jbig2

// qeEntry is one row of T.88 Table E.1: the LPS probability estimate and the
// index transitions taken after an MPS or an LPS renormalisation.
type qeEntry struct {
	qe               uint32
	nmps, nlps, swch uint8
}

// qeTable is T.88 Table E.1 in full. Index 46 is the fixed 0.5 estimate.
var qeTable = [47]qeEntry{
	{0x5601, 1, 1, 1}, {0x3401, 2, 6, 0}, {0x1801, 3, 9, 0}, {0x0AC1, 4, 12, 0},
	{0x0521, 5, 29, 0}, {0x0221, 38, 33, 0}, {0x5601, 7, 6, 1}, {0x5401, 8, 14, 0},
	{0x4801, 9, 14, 0}, {0x3801, 10, 14, 0}, {0x3001, 11, 17, 0}, {0x2401, 12, 18, 0},
	{0x1C01, 13, 20, 0}, {0x1601, 29, 21, 0}, {0x5601, 15, 14, 1}, {0x5401, 16, 14, 0},
	{0x5101, 17, 15, 0}, {0x4801, 18, 16, 0}, {0x3801, 19, 17, 0}, {0x3401, 20, 18, 0},
	{0x3001, 21, 19, 0}, {0x2801, 22, 19, 0}, {0x2401, 23, 20, 0}, {0x2201, 24, 21, 0},
	{0x1C01, 25, 22, 0}, {0x1801, 26, 23, 0}, {0x1601, 27, 24, 0}, {0x1401, 28, 25, 0},
	{0x1201, 29, 26, 0}, {0x1101, 30, 27, 0}, {0x0AC1, 31, 28, 0}, {0x09C1, 32, 29, 0},
	{0x08A1, 33, 30, 0}, {0x0521, 34, 31, 0}, {0x0441, 35, 32, 0}, {0x02A1, 36, 33, 0},
	{0x0221, 37, 34, 0}, {0x0141, 38, 35, 0}, {0x0111, 39, 36, 0}, {0x0085, 40, 37, 0},
	{0x0049, 41, 38, 0}, {0x0025, 42, 39, 0}, {0x0015, 43, 40, 0}, {0x0009, 44, 41, 0},
	{0x0005, 45, 42, 0}, {0x0001, 45, 43, 0}, {0x5601, 46, 46, 0},
}

// contexts holds the adaptive state for a set of coding contexts, one byte
// each, packed as index<<1 | mps. T.88 7.4.6.4 step 2 requires these to be
// zeroed at the start of every generic region segment, so callers allocate a
// fresh slice per segment rather than reusing one.
type contexts []byte

// encoder is the MQ arithmetic encoder of T.88 Annex E.2.
//
// out carries a one-byte leading sentinel so that bp can start at the E.2.8
// "BPST - 1" position without a negative index; flush slices it off. The carry
// path in byteout increments out[bp] in place, which is exactly the "propagate
// the carry into the previously written byte" behaviour of Figure E.9.
type encoder struct {
	c   uint32 // code register
	a   uint32 // interval register, 16-bit
	ct  int    // bits remaining before the next byte is emitted
	out []byte
	bp  int
}

// newEncoder performs INITENC (T.88 E.2.8, Figure E.10).
func newEncoder() *encoder {
	// The sentinel byte is 0, never 0xFF, so CT starts at 12 rather than 13.
	return &encoder{a: 0x8000, c: 0, ct: 12, out: []byte{0}, bp: 0}
}

func (e *encoder) b() byte     { return e.out[e.bp] }
func (e *encoder) setB(v byte) { e.out[e.bp] = v }

func (e *encoder) incBP() {
	e.bp++
	if e.bp == len(e.out) {
		e.out = append(e.out, 0)
	}
}

// byteout is T.88 E.2.7, Figure E.9.
func (e *encoder) byteout() {
	if e.b() == 0xFF {
		e.stuff()
		return
	}
	if e.c < 0x8000000 {
		e.normal()
		return
	}
	// Carry out of the code register: propagate into the previous byte.
	e.setB(e.b() + 1)
	if e.b() == 0xFF {
		e.c &= 0x7FFFFFF
		e.stuff()
		return
	}
	e.normal()
}

// stuff emits 7 bits after a 0xFF byte, which is what prevents a 0xFF 0x90..0xFF
// marker sequence from ever appearing in the coded data.
func (e *encoder) stuff() {
	e.incBP()
	e.setB(byte(e.c >> 20))
	e.c &= 0xFFFFF
	e.ct = 7
}

func (e *encoder) normal() {
	e.incBP()
	e.setB(byte(e.c >> 19))
	e.c &= 0x7FFFF
	e.ct = 8
}

// renorm is RENORME (T.88 E.2.6, Figure E.8).
func (e *encoder) renorm() {
	for {
		e.a <<= 1
		e.c <<= 1
		e.ct--
		if e.ct == 0 {
			e.byteout()
		}
		if e.a&0x8000 != 0 {
			return
		}
	}
}

// encode codes decision d (0 or 1) in context i. This is CODE0/CODE1
// (T.88 E.2.3) dispatching to CODEMPS (Figure E.6) or CODELPS (Figure E.7).
func (e *encoder) encode(cx contexts, i, d int) {
	st := cx[i]
	q := qeTable[st>>1]
	mps := st & 1

	if uint8(d) == mps {
		// CODEMPS. The conditional exchange can only matter when the interval
		// needs renormalising, which is why it sits inside this branch.
		e.a -= q.qe
		if e.a&0x8000 == 0 {
			if e.a < q.qe {
				e.a = q.qe
			} else {
				e.c += q.qe
			}
			cx[i] = q.nmps<<1 | mps
			e.renorm()
			return
		}
		e.c += q.qe
		return
	}

	// CODELPS.
	e.a -= q.qe
	if e.a < q.qe {
		e.c += q.qe
	} else {
		e.a = q.qe
	}
	if q.swch == 1 {
		mps = 1 - mps
	}
	cx[i] = q.nlps<<1 | mps
	e.renorm()
}

// flush is FLUSH (T.88 E.2.9, Figure E.11) including SETBITS (Figure E.12).
// It returns the complete coded segment, always terminated by 0xFF 0xAC.
//
// The optional trailing-0x7FFF removal of E.2.10 is deliberately NOT applied.
// It saves at most two bytes per region and is the only legitimate source of
// byte-level variation between two correct encoders; omitting it is what makes
// this encoder reproduce the Annex H.1 and H.2 vectors byte for byte.
func (e *encoder) flush() []byte {
	tempc := e.c + e.a
	e.c |= 0xFFFF
	if e.c >= tempc {
		e.c -= 0x8000
	}
	e.c <<= uint(e.ct)
	e.byteout()
	e.c <<= uint(e.ct)
	e.byteout()
	if e.b() != 0xFF {
		e.incBP()
		e.setB(0xFF)
	}
	e.incBP()
	e.setB(0xAC)
	return e.out[1 : e.bp+1]
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/jbig2/ -run TestMQ -v`
Expected: PASS, all five tests. `TestMQConformanceVector` in particular must
pass; **do not proceed to Task 3 until it does.** If it fails, add the Table H.1
register trace instrumentation described in the test's doc comment rather than
guessing at the arithmetic.

Then the whole package:

Run: `go test ./internal/jbig2/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/jbig2/mq.go internal/jbig2/mq_test.go
git commit -m "feat(jbig2): add the MQ arithmetic encoder, verified against T.88 Annex H.2"
```

---

## Task 3: GBTEMPLATE 0 context formation

The context is 16 pixels, so 65536 contexts and a 64 KiB state array. With the
nominal AT pixels (T.88 Table 5: `A1=(3,-1) A2=(-3,-1) A3=(2,-2) A4=(-2,-2)`)
the template collapses into three contiguous runs relative to the current pixel
at (0,0):

```
row y-2:  x-2 .. x+2      (5 pixels)
row y-1:  x-3 .. x+3      (7 pixels)
row y  :  x-4 .. x-1      (4 pixels)
```

**Bit order.** T.88 6.2.5.7 says the gathering order "is not standardised, but
shall be consistent and independent of the location of the AT pixels". That is
true in the abstract — any bijection relabels context buckets identically on
both sides — but it is only self-consistent because encoder and decoder both
read the same figures. **Use reading order: rows top to bottom, left to right
within a row, MSB = top-left.** Every real decoder assumes it, the spec's own
worked example gathers "in reading order", and Task 4's Annex H.1 vector fails
immediately under any other order, which is the real proof.

**AT pixels are fixed at nominal and no AT search is implemented.** Moving them
is a compression optimisation for periodic and halftone content; for scanned
text it buys little and costs a search. `jbig2enc` hardcodes the nominal values
and Annex H.1's own arithmetic generic region uses them.

**Files:**
- Create: `internal/jbig2/generic.go` (context function only; the encoder lands in Task 4)
- Test: `internal/jbig2/generic_test.go`

**Interfaces:**
- Consumes: `Bitmap` (Task 1).
- Produces:

```go
const sltpContextTemplate0 = 0x9B25

func contextTemplate0(b *Bitmap, x, y int) int
```

- [ ] **Step 1: Write the failing test**

Every expected value below was computed from a working prototype while this plan
was written; they are facts, not sketches.

Create `internal/jbig2/generic_test.go`:

```go
package jbig2

import "testing"

// Each template position must map to exactly one context bit, in reading order
// with MSB = top-left. A single set pixel at that position must produce exactly
// that bit and nothing else.
func TestContextTemplate0BitPositions(t *testing.T) {
	cases := []struct {
		dx, dy int
		bit    uint
	}{
		{-2, -2, 15}, {-1, -2, 14}, {0, -2, 13}, {1, -2, 12}, {2, -2, 11},
		{-3, -1, 10}, {-2, -1, 9}, {-1, -1, 8}, {0, -1, 7},
		{1, -1, 6}, {2, -1, 5}, {3, -1, 4},
		{-4, 0, 3}, {-3, 0, 2}, {-2, 0, 1}, {-1, 0, 0},
	}
	for _, c := range cases {
		b := NewBitmap(12, 8)
		b.Set(5+c.dx, 4+c.dy, 1)
		got := contextTemplate0(b, 5, 4)
		want := 1 << c.bit
		if got != want {
			t.Errorf("template pixel (%d,%d): context = %#04x; want %#04x (bit %d)",
				c.dx, c.dy, got, want, c.bit)
		}
	}
}

func TestContextTemplate0AllOnesInterior(t *testing.T) {
	b := NewBitmap(9, 5)
	for y := 0; y < 5; y++ {
		for x := 0; x < 9; x++ {
			b.Set(x, y, 1)
		}
	}
	if got := contextTemplate0(b, 4, 3); got != 0xFFFF {
		t.Errorf("interior context on an all-ink bitmap = %#04x; want 0xffff", got)
	}
}

// At the top-left corner every template pixel is out of bounds, so the context
// is 0 even on an all-ink bitmap (T.88 6.2.5.2).
func TestContextTemplate0CornerIsZero(t *testing.T) {
	b := NewBitmap(9, 5)
	for y := 0; y < 5; y++ {
		for x := 0; x < 9; x++ {
			b.Set(x, y, 1)
		}
	}
	if got := contextTemplate0(b, 0, 0); got != 0 {
		t.Errorf("corner context = %#04x; want 0x0000", got)
	}
}

// T.88 Figure 8 gives the SLTP context for GBTEMPLATE 0 as a picture in reading
// order. Decomposed into the three template runs it reads 10011 / 0110010 /
// 0101, which is 0x9B25.
func TestSLTPContextDecomposition(t *testing.T) {
	if sltpContextTemplate0 != 0x9B25 {
		t.Fatalf("sltpContextTemplate0 = %#04x; want 0x9b25", sltpContextTemplate0)
	}
	row2 := (sltpContextTemplate0 >> 11) & 0x1F
	row1 := (sltpContextTemplate0 >> 4) & 0x7F
	row0 := sltpContextTemplate0 & 0x0F
	if row2 != 0b10011 {
		t.Errorf("SLTP row y-2 = %05b; want 10011", row2)
	}
	if row1 != 0b0110010 {
		t.Errorf("SLTP row y-1 = %07b; want 0110010", row1)
	}
	if row0 != 0b0101 {
		t.Errorf("SLTP row y = %04b; want 0101", row0)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/jbig2/ -run "TestContext|TestSLTP" -v`
Expected: FAIL — `undefined: contextTemplate0`.

- [ ] **Step 3: Implement context formation**

Create `internal/jbig2/generic.go`:

```go
package jbig2

// sltpContextTemplate0 is the fixed context used to code the SLTP bit under
// TPGDON with GBTEMPLATE 0 (T.88 6.2.5.7, Figure 8). It is read in the same
// reading order as contextTemplate0, and it always uses the nominal template
// regardless of where the AT pixels actually sit.
const sltpContextTemplate0 = 0x9B25

// contextTemplate0 forms the GBTEMPLATE 0 context for the pixel at (x, y) with
// the nominal AT pixels of T.88 Table 5:
//
//	A1 = (3, -1)   A2 = (-3, -1)   A3 = (2, -2)   A4 = (-2, -2)
//
// which collapses the 16-pixel template into three contiguous runs:
//
//	row y-2:  x-2 .. x+2      (5 pixels, context bits 15..11)
//	row y-1:  x-3 .. x+3      (7 pixels, context bits 10..4)
//	row y:    x-4 .. x-1      (4 pixels, context bits  3..0)
//
// Bits are gathered in reading order with MSB = top-left. Out-of-bounds pixels
// read as 0 via Bitmap.Get, as T.88 6.2.5.2 requires.
func contextTemplate0(b *Bitmap, x, y int) int {
	cx := 0
	for dx := -2; dx <= 2; dx++ {
		cx = cx<<1 | b.Get(x+dx, y-2)
	}
	for dx := -3; dx <= 3; dx++ {
		cx = cx<<1 | b.Get(x+dx, y-1)
	}
	for dx := -4; dx <= -1; dx++ {
		cx = cx<<1 | b.Get(x+dx, y)
	}
	return cx
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/jbig2/ -run "TestContext|TestSLTP" -v`
Expected: PASS, all four tests.

- [ ] **Step 5: Commit**

```bash
git add internal/jbig2/generic.go internal/jbig2/generic_test.go
git commit -m "feat(jbig2): add GBTEMPLATE 0 context formation with nominal AT pixels"
```

---

## Task 4: Generic region encoding with TPGD

TPGD (typical prediction for generic direction) is fifteen lines. Before each
row the encoder codes one extra bit `SLTP` in the fixed context `0x9B25`;
`LTP ^= SLTP`; when `LTP == 1` the row is a byte-for-byte copy of the row above
and **no pixels are coded at all**. Rows that repeat exactly collapse to one bit
each.

**How much that is worth depends entirely on how many rows repeat exactly, and
it is not always large. Measure, do not assume.** On T.88's own Figure H.6,
whose 40 interior rows are identical, it takes the region from 20 bytes to 9.
On this plan's `textPageBitmap(640, 480)` it is 870 bytes with TPGD against 891
without — 2.4%, because that fixture's inter-line gaps are short and its glyph
rows never repeat. Task 10's failure guidance depends on knowing this.

It is not free on incompressible data — on uniform noise it costs about two
bytes — which is why it stays a parameter rather than being hardcoded, even
though every caller in byblos passes `true`.

**The acceptance oracle for this task is T.88 Annex H.1 segment 11**, whose
region data is the nine bytes `04 EE ED 87 FB CB 2B FF AC` for a 54x44 bitmap
coded with template 0, TPGDON and nominal AT. The input bitmap for that vector
is Figure H.6, which is a *picture* in the spec — but Annex H.1 segment 4 encodes
the identical figure with **MMR**, and `golang.org/x/image/ccitt` decodes MMR.
That gives an independent, in-repo, zero-external-tool path to the exact fixture.

**Files:**
- Modify: `internal/jbig2/generic.go`
- Modify: `internal/jbig2/generic_test.go`
- Create: `internal/jbig2/fixtures_test.go`

**Interfaces:**
- Consumes: `Bitmap` (Task 1), `encoder`/`contexts` (Task 2), `contextTemplate0` (Task 3).
- Produces:

```go
// EncodeGenericRegion codes b as a GBTEMPLATE 0 arithmetic generic region.
func EncodeGenericRegion(b *Bitmap, tpgdon bool) []byte
```

- [ ] **Step 1: Verify the MMR fixture path before relying on it**

This is the one place where an external package's option semantics decide
whether the test fixture is correct. Verify it, do not assume it.

Create `internal/jbig2/fixtures_test.go`:

```go
package jbig2

import (
	"bytes"
	"io"
	"testing"

	"golang.org/x/image/ccitt"
)

// figureH6MMR is the MMR-coded region data of T.88 Annex H.1 segment 4 (file
// offset 0x00D0, doc p. 130), which encodes the same 54x44 bitmap that Annex
// H.1 segment 11 encodes arithmetically. Decoding it recovers the exact fixture
// the arithmetic conformance vector expects, straight from the spec.
var figureH6MMR = []byte{
	0x26, 0xA0, 0x71, 0xCE, 0xA7,
	0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF,
	0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF,
	0xF8, 0xF0,
}

// figureH6 returns T.88 Figure H.6: a 54x44 rectangle with a 2-pixel border.
func figureH6() *Bitmap {
	b := NewBitmap(54, 44)
	for y := 0; y < 44; y++ {
		for x := 0; x < 54; x++ {
			if y < 2 || y >= 42 || x < 2 || x >= 52 {
				b.Set(x, y, 1)
			}
		}
	}
	return b
}

// TestFigureH6MatchesSpecMMR cross-checks the hand-written figureH6 against the
// spec's own MMR encoding of the same figure. If x/image/ccitt ever changes the
// meaning of Invert or the Group4 sub-format this test fails rather than
// silently validating the wrong fixture; in that case fix the decode options,
// and only if that is impossible fall back to figureH6 alone and note in the
// commit message that the cross-check was lost.
func TestFigureH6MatchesSpecMMR(t *testing.T) {
	// ccitt yields 1 = white by default; Invert makes 1 = black, matching both
	// JBIG2 and this package's convention.
	r := ccitt.NewReader(bytes.NewReader(figureH6MMR), ccitt.MSB, ccitt.Group4,
		54, 44, &ccitt.Options{Invert: true})
	got, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("MMR decode of T.88 H.1 segment 4 failed: %v", err)
	}
	want := figureH6()
	if len(got) != len(want.Pix) {
		t.Fatalf("MMR decode produced %d bytes; want %d", len(got), len(want.Pix))
	}
	if !bytes.Equal(got, want.Pix) {
		t.Fatalf("MMR-decoded Figure H.6 differs from the hand-built fixture\ngot  % 02X\nwant % 02X",
			got, want.Pix)
	}
}

// noiseBitmap is a deterministic pseudo-random bitmap: the worst case for an
// adaptive arithmetic coder, and the case most likely to expose a carry or
// byte-stuffing bug.
func noiseBitmap(w, h int, seed uint32) *Bitmap {
	b := NewBitmap(w, h)
	s := seed
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			s = s*1664525 + 1013904223
			if s>>29&1 == 1 {
				b.Set(x, y, 1)
			}
		}
	}
	return b
}

// textPageBitmap synthesises a page that behaves like a scan of text: wide
// white margins, blank inter-line gaps, and rows of small ink blocks standing
// in for glyphs. Deterministic, so sizes are comparable across runs.
func textPageBitmap(w, h int) *Bitmap {
	b := NewBitmap(w, h)
	s := uint32(20260727)
	next := func(n int) int {
		s = s*1664525 + 1013904223
		return int(s>>16) % n
	}
	const (
		marginX  = 40
		marginY  = 60
		lineStep = 26
		glyphW   = 6
		glyphH   = 11
		glyphGap = 2
		wordGap  = 7
	)
	for top := marginY; top+glyphH < h-marginY; top += lineStep {
		x := marginX
		for x < w-marginX-glyphW {
			word := 2 + next(7)
			for i := 0; i < word && x < w-marginX-glyphW; i++ {
				for gy := 0; gy < glyphH; gy++ {
					for gx := 0; gx < glyphW; gx++ {
						// Hollow the middle so glyphs are strokes, not blocks.
						if gy == 0 || gy == glyphH-1 || gx == 0 || gx == glyphW-1 || (gy+gx)%5 == 0 {
							b.Set(x+gx, top+gy, 1)
						}
					}
				}
				x += glyphW + glyphGap
			}
			x += wordGap
		}
	}
	return b
}

// fixtureBitmaps is the corpus every downstream test iterates over. It spans
// the structural cases that break naive implementations: non-byte-aligned
// widths, single-pixel dimensions, all-background, all-ink, and pure noise.
func fixtureBitmaps() map[string]*Bitmap {
	all := NewBitmap(200, 120)
	for y := 0; y < 120; y++ {
		for x := 0; x < 200; x++ {
			all.Set(x, y, 1)
		}
	}
	return map[string]*Bitmap{
		"border": figureH6(),
		"empty":  NewBitmap(200, 120),
		"full":   all,
		"noise":  noiseBitmap(101, 73, 12345),
		"odd":    noiseBitmap(13, 11, 99),
		"single": NewBitmap(1, 1),
		"column": noiseBitmap(1, 500, 7),
		"row":    noiseBitmap(500, 1, 11),
		"text":   textPageBitmap(640, 480),
	}
}
```

Run: `go test ./internal/jbig2/ -run TestFigureH6MatchesSpecMMR -v`

Expected: PASS. This proves the fixture before anything depends on it. **If it
fails**, the problem is one of: `ccitt.Options.Invert` polarity, `ccitt.MSB` vs
`ccitt.LSB`, or the byte transcription of `figureH6MMR`. Fix in that order;
`figureH6MMR` was transcribed from the spec at offsets 0x00D0-0x00E9 and is 26
bytes long.

- [ ] **Step 2: Write the failing encoder test**

Append to `internal/jbig2/generic_test.go`:

```go
// TestEncodeGenericRegionAnnexH1 is the T.88 Annex H.1 segment 11 conformance
// vector (doc p. 135): a 54x44 bitmap coded with GBTEMPLATE 0, TPGDON = 1 and
// nominal AT pixels produces exactly these nine bytes.
//
// This single assertion pins the context bit order, the SLTP context value, the
// TPGD state machine, the out-of-bounds rule and the MQ flush convention all at
// once. If it fails, one of those five is wrong -- and the MQ coder is already
// proven by Task 2, so it is one of the other four.
func TestEncodeGenericRegionAnnexH1(t *testing.T) {
	want := []byte{0x04, 0xEE, 0xED, 0x87, 0xFB, 0xCB, 0x2B, 0xFF, 0xAC}
	got := EncodeGenericRegion(figureH6(), true)
	if !bytes.Equal(got, want) {
		t.Fatalf("region data mismatch\ngot  (%d): % 02X\nwant (%d): % 02X",
			len(got), got, len(want), want)
	}
}

// TPGD is the whole reason this encoder is worth building for scanned pages.
// On Figure H.6, whose 40 interior rows are all identical, it is a better than
// 2x saving.
func TestTPGDShrinksRepeatedRows(t *testing.T) {
	b := figureH6()
	with := len(EncodeGenericRegion(b, true))
	without := len(EncodeGenericRegion(b, false))
	if with != 9 {
		t.Errorf("TPGDON size = %d; want 9", with)
	}
	if without != 20 {
		t.Errorf("TPGDOFF size = %d; want 20", without)
	}
	if with >= without {
		t.Errorf("TPGDON (%d bytes) did not beat TPGDOFF (%d bytes)", with, without)
	}
}

// An all-background bitmap is every row equal to the one above, so TPGD codes
// one bit per row and nothing else.
func TestEncodeGenericRegionAllBackground(t *testing.T) {
	got := EncodeGenericRegion(NewBitmap(16, 4), true)
	want := []byte{0xB3, 0xFF, 0xAC}
	if !bytes.Equal(got, want) {
		t.Fatalf("all-background 16x4 = % 02X; want % 02X", got, want)
	}
}

// Stray padding bits past the row width must not change the output. They are
// invisible to Get but visible to the whole-byte row comparison behind TPGD.
func TestEncodeGenericRegionIgnoresPaddingBits(t *testing.T) {
	clean := noiseBitmap(13, 11, 99)
	dirty := noiseBitmap(13, 11, 99)
	for y := 0; y < dirty.H; y++ {
		dirty.Pix[y*dirty.Stride+dirty.Stride-1] |= 0x07 // bits 13,14,15
	}
	if bytes.Equal(clean.Pix, dirty.Pix) {
		t.Fatal("test setup failed: padding bits were not actually set")
	}
	if a, b := EncodeGenericRegion(clean, true), EncodeGenericRegion(dirty, true); !bytes.Equal(a, b) {
		t.Errorf("padding bits changed the encoding:\nclean % 02X\ndirty % 02X", a, b)
	}
}

// A Stride larger than the minimal (W+7)/8 is accepted (EncodeJBIG2Generic
// rejects only a Stride that is too small). The trailing bytes it leaves are
// invisible to Get but visible to the whole-stride comparison behind TPGD, so
// leaving them dirty silently costs compression while the round trip stays
// lossless -- the worst kind of bug. Measured before MaskPadding was taught to
// clear them: 28 bytes here instead of 12.
func TestEncodeGenericRegionIgnoresNonMinimalStride(t *testing.T) {
	const w, h = 100, 200
	build := func(stride int, junk bool) *Bitmap {
		b := &Bitmap{W: w, H: h, Stride: stride, Pix: make([]byte, stride*h)}
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				if y < 3 || y >= h-3 || x < 3 || x >= w-3 {
					b.Set(x, y, 1)
				}
			}
			if junk {
				for i := (w + 7) / 8; i < stride; i++ {
					b.Pix[y*stride+i] = byte(0xA5 + y)
				}
			}
		}
		return b
	}
	want := EncodeGenericRegion(build((w+7)/8, false), true)
	got := EncodeGenericRegion(build(16, true), true)
	if !bytes.Equal(got, want) {
		t.Errorf("a non-minimal stride changed the encoding: %d bytes vs %d bytes\ngot  % 02X\nwant % 02X",
			len(got), len(want), got, want)
	}
}

// Every fixture must encode without panicking and produce a well-formed stream.
func TestEncodeGenericRegionCorpusWellFormed(t *testing.T) {
	for name, b := range fixtureBitmaps() {
		got := EncodeGenericRegion(b, true)
		if len(got) < 2 {
			t.Errorf("%s: encoded to %d bytes; want at least 2", name, len(got))
			continue
		}
		if got[len(got)-2] != 0xFF || got[len(got)-1] != 0xAC {
			t.Errorf("%s: stream tail = % 02X; want ... FF AC", name, got[len(got)-2:])
		}
	}
}
```

Add `"bytes"` to the imports of `generic_test.go`.

- [ ] **Step 3: Run the test to verify it fails**

Run: `go test ./internal/jbig2/ -run TestEncodeGenericRegion -v`
Expected: FAIL — `undefined: EncodeGenericRegion`.

- [ ] **Step 4: Implement the region encoder**

Append to `internal/jbig2/generic.go`:

```go
// EncodeGenericRegion codes b as an arithmetic generic region using
// GBTEMPLATE 0 with nominal AT pixels, and returns the MQ-coded region data.
//
// The coding is lossless: a conforming decoder reconstructs b exactly. That is
// a property of the algorithm, not of the parameters -- there is no setting of
// this function that can substitute one pixel for another.
//
// When tpgdon is true, each row is preceded by an SLTP bit coded in the fixed
// context sltpContextTemplate0; a row identical to the one above is then not
// coded at all (T.88 6.2.5.5, 6.2.5.7 step 3b). This is a large win on scanned
// pages and a small loss (about two bytes) on incompressible noise.
//
// The context array is allocated fresh on every call, which is what T.88
// 7.4.6.4 step 2 requires: arithmetic coding statistics are reset to zero at
// the start of every generic region segment, never carried across segments.
func EncodeGenericRegion(b *Bitmap, tpgdon bool) []byte {
	b.MaskPadding()

	cx := make(contexts, 1<<16)
	e := newEncoder()

	ltp := 0
	for y := 0; y < b.H; y++ {
		if tpgdon {
			next := 0
			if b.RowEqualAbove(y) {
				next = 1
			}
			e.encode(cx, sltpContextTemplate0, next^ltp)
			ltp = next
			if ltp == 1 {
				continue
			}
		}
		for x := 0; x < b.W; x++ {
			e.encode(cx, contextTemplate0(b, x, y), b.Get(x, y))
		}
	}
	return e.flush()
}
```

- [ ] **Step 5: Run the tests to verify they pass**

Run: `go test ./internal/jbig2/ -v`
Expected: PASS. `TestEncodeGenericRegionAnnexH1` reporting
`04 EE ED 87 FB CB 2B FF AC` is the moment the coder is proven end to end.

- [ ] **Step 6: Commit**

```bash
git add internal/jbig2/generic.go internal/jbig2/generic_test.go internal/jbig2/fixtures_test.go
git commit -m "feat(jbig2): add generic region coding with TPGD, verified against T.88 Annex H.1"
```

---

## Task 5: Segment writer and the embedded stream

ISO 32000-1:2008 §7.4.7 constrains the bitstream tightly:

- **Embedded file organization only.** The optional `0xFF 0xAA` / `0xFF 0xAB`
  markers "shall not be used in PDF".
- **"The JBIG2 file header, end-of-page segments, and end-of-file segment shall
  not be used in PDF."** So no 8-byte `97 4A 42 32 0D 0A 1A 0A` header, no type
  49, no type 51.
- The XObject stream carries all segments for its page, and "the segment's page
  number should always be 1".
- Global (page-0) segments go in a separate `/JBIG2Globals` stream.

**No `/JBIG2Globals` is needed here.** That stream carries symbol dictionaries,
pattern dictionaries and custom Huffman tables — page-0 segments. A
generic-region-only encoder produces none of them, so `/DecodeParms` is omitted
entirely. (Table 12's "shall be placed in this stream even if only a single
image XObject refers to it" forbids *inlining* globals; it does not mandate a
stream when there are none.)

So the stream is exactly two segments:

| # | Type | Page assoc | Payload |
|---|---|---|---|
| 0 | **48** page information | 1 | 19 bytes: width(4) height(4) xres(4) yres(4) flags(1) striping(2) |
| 1 | **39** immediate lossless generic region | 1 | region info(17) + flags(1) + AT(8) + MQ data |

The page information segment is **mandatory**: T.88 §7.4.8, "The first segment
that is associated with any page must be a page information segment."

Type **39** (immediate *lossless* generic region) rather than 38, because it
advertises the losslessness this whole design rests on.

**Files:**
- Create: `internal/jbig2/segment.go`
- Test: `internal/jbig2/segment_test.go`

**Interfaces:**
- Consumes: `Bitmap` (Task 1), `EncodeGenericRegion` (Task 4).
- Produces:

```go
func EmbeddedStream(b *Bitmap) ([]byte, error)
```

- [ ] **Step 1: Write the failing test**

The goldens below are transcribed from T.88 Annex H.1's hex dump (doc p. 121,
133, 135). Segment numbers and page associations differ from what byblos emits,
which is exactly why the low-level writers are tested directly.

Create `internal/jbig2/segment_test.go`:

```go
package jbig2

import (
	"bytes"
	"encoding/binary"
	"math"
	"testing"
)

// T.88 Annex H.1, twelfth segment header (file offset 0x0200): segment number
// 11, type 39 (immediate lossless generic region), short page association,
// no referred-to segments, page 2, data length 35.
func TestSegmentHeaderMatchesAnnexH1(t *testing.T) {
	got := segmentHeader(11, segTypeImmediateLosslessGenericRegion, 2, 35)
	want := []byte{0x00, 0x00, 0x00, 0x0B, 0x27, 0x00, 0x02, 0x00, 0x00, 0x00, 0x23}
	if !bytes.Equal(got, want) {
		t.Fatalf("segment header = % 02X; want % 02X", got, want)
	}
}

// T.88 Annex H.1, ninth segment header (file offset 0x0190): segment number 8,
// type 48 (page information), page 2, data length 19.
func TestPageInfoSegmentHeaderMatchesAnnexH1(t *testing.T) {
	got := segmentHeader(8, segTypePageInformation, 2, 19)
	want := []byte{0x00, 0x00, 0x00, 0x08, 0x30, 0x00, 0x02, 0x00, 0x00, 0x00, 0x13}
	if !bytes.Equal(got, want) {
		t.Fatalf("page info header = % 02X; want % 02X", got, want)
	}
}

// T.88 Annex H.1, ninth segment data part (file offset 0x019B): a 64x56 page
// with unknown resolutions, "eventually lossless" set, not striped.
func TestPageInfoSegmentDataMatchesAnnexH1(t *testing.T) {
	got := pageInfoSegmentData(64, 56)
	want := []byte{
		0x00, 0x00, 0x00, 0x40, // width 64
		0x00, 0x00, 0x00, 0x38, // height 56
		0x00, 0x00, 0x00, 0x00, // X resolution unknown
		0x00, 0x00, 0x00, 0x00, // Y resolution unknown
		0x01,       // flags: page is eventually lossless
		0x00, 0x00, // striping information: not striped
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("page info data = % 02X; want % 02X", got, want)
	}
	if len(got) != 19 {
		t.Fatalf("page info data = %d bytes; want 19", len(got))
	}
}

// T.88 Annex H.1, twelfth segment data part (file offset 0x020B): region info
// for a 54x44 region at (4, 11), then generic region flags 0x08 (arithmetic,
// GBTEMPLATE 0, TPGDON), then the eight nominal AT bytes, then the region data.
func TestGenericRegionSegmentDataMatchesAnnexH1(t *testing.T) {
	got := genericRegionSegmentData(figureH6(), 4, 11, true)
	want := []byte{
		0x00, 0x00, 0x00, 0x36, // region width 54
		0x00, 0x00, 0x00, 0x2C, // region height 44
		0x00, 0x00, 0x00, 0x04, // region X 4
		0x00, 0x00, 0x00, 0x0B, // region Y 11
		0x00,                                           // external combination operator OR
		0x08,                                           // MMR=0, GBTEMPLATE=0, TPGDON=1
		0x03, 0xFF, 0xFD, 0xFF, 0x02, 0xFE, 0xFE, 0xFE, // nominal AT pixels
		0x04, 0xEE, 0xED, 0x87, 0xFB, 0xCB, 0x2B, 0xFF, 0xAC,
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("generic region segment data mismatch\ngot  (%d): % 02X\nwant (%d): % 02X",
			len(got), got, len(want), want)
	}
	if len(got) != 35 {
		t.Fatalf("segment data = %d bytes; want 35 (matching the header's length field)", len(got))
	}
}

// The embedded stream is exactly two segments, both associated with page 1, and
// carries no JBIG2 file header (ISO 32000-1 7.4.7 forbids it in PDF).
func TestEmbeddedStreamStructure(t *testing.T) {
	b := figureH6()
	got, err := EmbeddedStream(b)
	if err != nil {
		t.Fatalf("EmbeddedStream() error = %v", err)
	}

	if bytes.HasPrefix(got, []byte{0x97, 0x4A, 0x42, 0x32, 0x0D, 0x0A, 0x1A, 0x0A}) {
		t.Fatal("stream begins with the JBIG2 file header, which ISO 32000-1 7.4.7 forbids in PDF")
	}

	// Segment 0: page information.
	if n := binary.BigEndian.Uint32(got[0:4]); n != 0 {
		t.Errorf("first segment number = %d; want 0", n)
	}
	if got[4] != segTypePageInformation {
		t.Errorf("first segment type = %d; want %d", got[4], segTypePageInformation)
	}
	if got[5] != 0x00 {
		t.Errorf("first segment referred-to byte = %#02x; want 0x00 (no references)", got[5])
	}
	if got[6] != 1 {
		t.Errorf("first segment page association = %d; want 1", got[6])
	}
	if n := binary.BigEndian.Uint32(got[7:11]); n != 19 {
		t.Errorf("page info data length = %d; want 19", n)
	}

	// Segment 1 starts immediately after the 11-byte header and 19-byte body.
	const s1 = 11 + 19
	if n := binary.BigEndian.Uint32(got[s1 : s1+4]); n != 1 {
		t.Errorf("second segment number = %d; want 1", n)
	}
	if got[s1+4] != segTypeImmediateLosslessGenericRegion {
		t.Errorf("second segment type = %d; want %d", got[s1+4], segTypeImmediateLosslessGenericRegion)
	}
	if got[s1+6] != 1 {
		t.Errorf("second segment page association = %d; want 1", got[s1+6])
	}
	bodyLen := int(binary.BigEndian.Uint32(got[s1+7 : s1+11]))
	if want := len(got) - s1 - 11; bodyLen != want {
		t.Errorf("generic region data length field = %d; want %d", bodyLen, want)
	}

	// No end-of-page (49) or end-of-file (51) segment: 7.4.7 forbids both.
	if len(got) != s1+11+bodyLen {
		t.Errorf("stream is %d bytes; two segments account for %d -- a trailing segment was emitted",
			len(got), s1+11+bodyLen)
	}
}

func TestEmbeddedStreamRejectsOversizeBitmap(t *testing.T) {
	// A bitmap whose dimensions do not fit the 4-byte region fields cannot be
	// represented; the writer must say so rather than truncate silently.
	//
	// The width goes through an int64 *variable* so this file still compiles
	// where int is 32 bits. Both `W: 1 << 33` and `int(someConstant)` would be
	// compile-time overflows there; a runtime conversion is not. On such a
	// platform the guard is unreachable, so skip.
	w := int64(1) << 33
	if w > int64(math.MaxInt) {
		t.Skip("int is 32 bits on this platform; the 32-bit region dimension guard is unreachable")
	}
	b := &Bitmap{W: int(w), H: 1, Stride: 1, Pix: make([]byte, 1)}
	if _, err := EmbeddedStream(b); err == nil {
		t.Fatal("EmbeddedStream() on a 2^33-wide bitmap: want error, got nil")
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/jbig2/ -run "TestSegment|TestPageInfo|TestGenericRegionSegment|TestEmbeddedStream" -v`
Expected: FAIL — `undefined: segmentHeader`.

- [ ] **Step 3: Implement the segment writer**

Create `internal/jbig2/segment.go`:

```go
package jbig2

import (
	"encoding/binary"
	"fmt"
	"math"
)

// Segment types used by this package. T.88 7.3 gives the allowed types as a
// plain numbered list, not a table -- everything else in it belongs to the
// symbol, text, halftone and refinement machinery this package deliberately
// does not implement.
const (
	segTypeImmediateLosslessGenericRegion = 39
	segTypePageInformation                = 48
)

// nominalATTemplate0 is the AT pixel field for GBTEMPLATE 0 (T.88 7.4.6.3),
// eight signed bytes carrying the nominal positions of T.88 Table 5:
// A1 (3,-1), A2 (-3,-1), A3 (2,-2), A4 (-2,-2).
var nominalATTemplate0 = []byte{0x03, 0xFF, 0xFD, 0xFF, 0x02, 0xFE, 0xFE, 0xFE}

// segmentHeader builds a T.88 7.2 segment header with a one-byte page
// association and no referred-to segments: 11 bytes total.
//
// The flags byte is the segment type in bits 0-5, bit 6 clear for a one-byte
// page association, bit 7 clear for "not deferred, non-retain". The
// referred-to-segment-count byte is 0x00: zero references, no retain bits.
//
// The 0xFFFFFFFF unknown-data-length form is deliberately not used -- the
// length is always known here, and the unknown form is only decodable by
// scanning for a terminator.
func segmentHeader(segNum uint32, segType byte, pageAssoc byte, dataLen int) []byte {
	h := make([]byte, 0, 11)
	h = binary.BigEndian.AppendUint32(h, segNum)
	h = append(h, segType, 0x00, pageAssoc)
	return binary.BigEndian.AppendUint32(h, uint32(dataLen))
}

// pageInfoSegmentData builds the 19-byte page information segment body
// (T.88 7.4.8).
//
// Resolutions are written as 0 ("unknown"): the JBIG2 page carries no DPI of
// its own in PDF, where the image XObject's placement matrix determines scale.
// The flags byte is 0x01 -- bit 0 "page is eventually lossless" set, because it
// is; every other bit clear, which means no refinements, default pixel value 0,
// default combination operator OR, no auxiliary buffers, and the combination
// operator not overridable. Striping information is 0x0000: not striped.
func pageInfoSegmentData(width, height int) []byte {
	d := make([]byte, 0, 19)
	d = binary.BigEndian.AppendUint32(d, uint32(width))
	d = binary.BigEndian.AppendUint32(d, uint32(height))
	d = binary.BigEndian.AppendUint32(d, 0)
	d = binary.BigEndian.AppendUint32(d, 0)
	d = append(d, 0x01)
	return append(d, 0x00, 0x00)
}

// genericRegionSegmentData builds the body of a generic region segment: the
// 17-byte region segment information field (T.88 7.4.1), the one-byte generic
// region flags (7.4.6.2), the eight-byte AT field (7.4.6.3) and the MQ-coded
// region data.
//
// The region flags byte is MMR in bit 0, GBTEMPLATE in bits 1-2, TPGDON in
// bit 3, and reserved zeros above: template 0 with TPGDON is 0x08.
func genericRegionSegmentData(b *Bitmap, x, y int, tpgdon bool) []byte {
	d := make([]byte, 0, 26+b.H)
	d = binary.BigEndian.AppendUint32(d, uint32(b.W))
	d = binary.BigEndian.AppendUint32(d, uint32(b.H))
	d = binary.BigEndian.AppendUint32(d, uint32(x))
	d = binary.BigEndian.AppendUint32(d, uint32(y))
	d = append(d, 0x00) // region segment flags: external combination operator OR

	flags := byte(0x00) // MMR = 0, GBTEMPLATE = 0
	if tpgdon {
		flags |= 0x08
	}
	d = append(d, flags)
	d = append(d, nominalATTemplate0...)
	return append(d, EncodeGenericRegion(b, tpgdon)...)
}

// EmbeddedStream encodes b as a complete JBIG2 bitstream in the embedded file
// organization required by ISO 32000-1:2008 7.4.7 for the PDF JBIG2Decode
// filter: no file header, no end-of-page segment, no end-of-file segment, and
// every segment associated with page 1.
//
// The result is exactly two segments -- a page information segment and an
// immediate lossless generic region segment covering the whole page. It carries
// no page-0 (global) segments, so the image XObject needs no /DecodeParms and
// no /JBIG2Globals stream.
func EmbeddedStream(b *Bitmap) ([]byte, error) {
	if b.W <= 0 || b.H <= 0 {
		return nil, fmt.Errorf("jbig2: bitmap is %dx%d; dimensions must be positive", b.W, b.H)
	}
	// uint64 conversion so the comparison also compiles on a 32-bit int platform.
	if uint64(b.W) > math.MaxUint32 || uint64(b.H) > math.MaxUint32 {
		return nil, fmt.Errorf("jbig2: bitmap is %dx%d; JBIG2 region dimensions are 32-bit", b.W, b.H)
	}

	pi := pageInfoSegmentData(b.W, b.H)
	gr := genericRegionSegmentData(b, 0, 0, true)

	out := make([]byte, 0, 22+len(pi)+len(gr))
	out = append(out, segmentHeader(0, segTypePageInformation, 1, len(pi))...)
	out = append(out, pi...)
	out = append(out, segmentHeader(1, segTypeImmediateLosslessGenericRegion, 1, len(gr))...)
	return append(out, gr...), nil
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/jbig2/ -v`
Expected: PASS, every test in the package.

- [ ] **Step 5: Commit**

```bash
git add internal/jbig2/segment.go internal/jbig2/segment_test.go
git commit -m "feat(jbig2): add the segment writer and PDF embedded-organization stream"
```

---

## Task 6: Incremental context update

`contextTemplate0` costs 16 `Get` calls per pixel. A 300 DPI A4 page is about
8.4 million pixels, so 134 million bounds-checked bit extractions per page.
Because the template is three contiguous horizontal runs under nominal AT, each
one is a shift register that takes exactly one new pixel per step:

```
A' = ((A << 1) | pixel(x+3, y-2)) & 0x1F
B' = ((B << 1) | pixel(x+4, y-1)) & 0x7F
C' = ((C << 1) | pixel(x,   y  )) & 0x0F      <- the pixel just coded
cx = A<<11 | B<<4 | C
```

This is an optimisation, so it gets an equivalence test rather than a new
conformance test: the naive implementation stays in the tree as a test-only
reference and the two must agree byte for byte on the whole fixture corpus.

**Files:**
- Modify: `internal/jbig2/generic.go`
- Modify: `internal/jbig2/generic_test.go`

**Interfaces:** unchanged. `EncodeGenericRegion` keeps its signature.

- [ ] **Step 1: Write the failing equivalence test**

Append to `internal/jbig2/generic_test.go`:

```go
// encodeGenericRegionReference is the straightforward implementation: it calls
// contextTemplate0 for every pixel. It exists only so the optimised encoder has
// something to be proven equal to.
func encodeGenericRegionReference(b *Bitmap, tpgdon bool) []byte {
	b.MaskPadding()
	cx := make(contexts, 1<<16)
	e := newEncoder()
	ltp := 0
	for y := 0; y < b.H; y++ {
		if tpgdon {
			next := 0
			if b.RowEqualAbove(y) {
				next = 1
			}
			e.encode(cx, sltpContextTemplate0, next^ltp)
			ltp = next
			if ltp == 1 {
				continue
			}
		}
		for x := 0; x < b.W; x++ {
			e.encode(cx, contextTemplate0(b, x, y), b.Get(x, y))
		}
	}
	return e.flush()
}

// The sliding-window context update must be bit-for-bit equivalent to forming
// each context from scratch, on every fixture and with TPGD both ways.
func TestEncodeGenericRegionMatchesReference(t *testing.T) {
	for name, b := range fixtureBitmaps() {
		for _, tpgdon := range []bool{true, false} {
			want := encodeGenericRegionReference(b, tpgdon)
			got := EncodeGenericRegion(b, tpgdon)
			if !bytes.Equal(got, want) {
				t.Errorf("%s (tpgdon=%v): optimised encoder differs from reference\ngot  (%d): % 02X\nwant (%d): % 02X",
					name, tpgdon, len(got), got, len(want), want)
			}
		}
	}
}

func BenchmarkEncodeGenericRegionTextPage(bench *testing.B) {
	b := textPageBitmap(2550, 3300) // A4 at 300 DPI
	bench.ResetTimer()
	for i := 0; i < bench.N; i++ {
		EncodeGenericRegion(b, true)
	}
}

func BenchmarkEncodeGenericRegionReferenceTextPage(bench *testing.B) {
	b := textPageBitmap(2550, 3300)
	bench.ResetTimer()
	for i := 0; i < bench.N; i++ {
		encodeGenericRegionReference(b, true)
	}
}
```

- [ ] **Step 2: Run the test and confirm it passes trivially**

Run: `go test ./internal/jbig2/ -run TestEncodeGenericRegionMatchesReference -v`
Expected: PASS — at this point `EncodeGenericRegion` *is* the reference, so the
test is currently tautological. That is fine and expected: it becomes a real
test the moment Step 4 changes the implementation, and running it now proves the
harness itself works.

- [ ] **Step 3: Record the baseline**

```bash
go test ./internal/jbig2/ -run XXX -bench BenchmarkEncodeGenericRegion -benchtime 3x
```

Write both `ns/op` figures down; Step 5 compares against them.

- [ ] **Step 4: Replace the inner loop with the sliding window**

In `internal/jbig2/generic.go`, replace the body of the `for y` loop's pixel
pass in `EncodeGenericRegion` so the function reads:

```go
func EncodeGenericRegion(b *Bitmap, tpgdon bool) []byte {
	b.MaskPadding()

	cx := make(contexts, 1<<16)
	e := newEncoder()

	ltp := 0
	for y := 0; y < b.H; y++ {
		if tpgdon {
			next := 0
			if b.RowEqualAbove(y) {
				next = 1
			}
			e.encode(cx, sltpContextTemplate0, next^ltp)
			ltp = next
			if ltp == 1 {
				continue
			}
		}

		// Seed the three template runs at x = 0. Under nominal AT each run is
		// contiguous, so from here on each advances by one pixel per step.
		var runAbove2, runAbove1, runLeft int
		for dx := -2; dx <= 2; dx++ {
			runAbove2 = runAbove2<<1 | b.Get(dx, y-2)
		}
		for dx := -3; dx <= 3; dx++ {
			runAbove1 = runAbove1<<1 | b.Get(dx, y-1)
		}
		for dx := -4; dx <= -1; dx++ {
			runLeft = runLeft<<1 | b.Get(dx, y)
		}

		for x := 0; x < b.W; x++ {
			pix := b.Get(x, y)
			e.encode(cx, runAbove2<<11|runAbove1<<4|runLeft, pix)
			runAbove2 = (runAbove2<<1 | b.Get(x+3, y-2)) & 0x1F
			runAbove1 = (runAbove1<<1 | b.Get(x+4, y-1)) & 0x7F
			runLeft = (runLeft<<1 | pix) & 0x0F
		}
	}
	return e.flush()
}
```

Leave `contextTemplate0` in place — it is still the definition of the bit order,
it is still directly unit-tested in Task 3, and it is what the reference encoder
uses.

- [ ] **Step 5: Run the tests and the benchmark**

```bash
go test ./internal/jbig2/ -v
go test ./internal/jbig2/ -run XXX -bench BenchmarkEncodeGenericRegion -benchtime 3x
```

Expected: all tests PASS, including
`TestEncodeGenericRegionMatchesReference` (now non-trivial) and
`TestEncodeGenericRegionAnnexH1` (still producing the nine spec bytes).
`BenchmarkEncodeGenericRegionTextPage` should be faster than
`BenchmarkEncodeGenericRegionReferenceTextPage`.

**Acceptance:** the optimised encoder must be at least 1.5x faster on the
300 DPI page benchmark. If it is not, the win is not worth the extra code.
Revert it completely — a half-revert leaves a test that compares a function to a
copy of itself and looks green forever. Delete, in `generic_test.go`:

- `encodeGenericRegionReference`
- `TestEncodeGenericRegionMatchesReference`
- `BenchmarkEncodeGenericRegionReferenceTextPage`

Keep `BenchmarkEncodeGenericRegionTextPage`, revert Step 4's change to
`generic.go`, and note the measured `ns/op` figures in the commit message. Do
not keep two implementations for a rounding error.

- [ ] **Step 6: Commit**

```bash
git add internal/jbig2/generic.go internal/jbig2/generic_test.go
git commit -m "perf(jbig2): form template contexts with a sliding window"
```

---

## Task 7: Public API

The design spec §4 fixes the signature as
`func EncodeJBIG2Generic(b *Bitmap) ([]byte, error)` in package `byblos`, over
the root `Bitmap` that `byb-b0` owns. `internal/jbig2` cannot import the root
package (the root imports it), so this task writes the adapter.

**The adapter is the one place in this epic where a layout mismatch produces
silently wrong output rather than a compile error** — if B0's `Pix` were
LSB-first, or 1 meant paper instead of ink, everything would still compile and
every page would come out inverted or scrambled. The test below is what catches
that.

**Files:**
- Create: `jbig2.go`, `jbig2_test.go`
- Create (only if `byb-b0` has not landed): the root `Bitmap` type

**Interfaces:**
- Consumes: `internal/jbig2.EmbeddedStream`.
- Produces:

```go
package byblos

const CapabilityJBIG2Generic = "jbig2-generic"

func EncodeJBIG2Generic(b *Bitmap) ([]byte, error)
```

- [ ] **Step 1: Establish the root `Bitmap`**

```bash
grep -rn "type Bitmap struct" --include=*.go /Users/christopherdobbyn/work/dobbo-ca/byblos --exclude-dir=internal
```

**If it exists** (B0 landed): read the declaration. Note its field names, its
bit packing, and its ink convention. Write the adapter in Step 3 against *that*,
converting bit by bit if the packing differs rather than reinterpreting `Pix`.

**If it does not exist** (B0 has not landed): create `bitmap.go` in the repo root
with exactly this, which is the layout `internal/jbig2` already uses so the
adapter is a field copy:

```go
package byblos

// Bitmap is a 1-bit-per-pixel bitmap. Rows are packed MSB-first with Stride
// bytes per row; a set bit is ink (black). Bits past Width in the last byte of
// a row are padding and must be zero.
//
// Byblos owns this type. It is deliberately not imported from cadmus: neither
// library depends on the other.
type Bitmap struct {
	Width, Height int
	Stride        int
	Pix           []byte
}
```

Record which branch you took. **A commit message is not enough** — `byb-b0` is
still open and is titled "scaffolding, 1bpp Bitmap, provenance types", so if B2
lands first, B0 will collide on exactly these files. Step 6 below writes the
reconciliation note onto the bead itself.

- [ ] **Step 2: Write the failing test**

Create `jbig2_test.go`:

```go
package byblos

import (
	"bytes"
	"slices"
	"testing"
)

// borderBitmap is T.88 Figure H.6 built through the public Bitmap type: a 54x44
// rectangle with a 2-pixel ink border.
func borderBitmap() *Bitmap {
	const w, h = 54, 44
	stride := (w + 7) / 8
	b := &Bitmap{Width: w, Height: h, Stride: stride, Pix: make([]byte, stride*h)}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			if y < 2 || y >= h-2 || x < 2 || x >= w-2 {
				b.Pix[y*stride+x/8] |= 0x80 >> (uint(x) % 8)
			}
		}
	}
	return b
}

// The public API must reach the T.88 Annex H.1 region data through the adapter.
// This is what catches a bit-order or ink-convention mismatch between the root
// Bitmap and internal/jbig2: any such mismatch changes these bytes.
func TestEncodeJBIG2GenericReachesSpecVector(t *testing.T) {
	got, err := EncodeJBIG2Generic(borderBitmap())
	if err != nil {
		t.Fatalf("EncodeJBIG2Generic() error = %v", err)
	}
	want := []byte{0x04, 0xEE, 0xED, 0x87, 0xFB, 0xCB, 0x2B, 0xFF, 0xAC}
	if !bytes.HasSuffix(got, want) {
		t.Fatalf("stream does not end with the T.88 Annex H.1 region data\ngot tail: % 02X\nwant:     % 02X",
			got[max(0, len(got)-len(want)):], want)
	}
}

func TestEncodeJBIG2GenericRejectsEmptyBitmap(t *testing.T) {
	if _, err := EncodeJBIG2Generic(&Bitmap{Width: 0, Height: 0}); err == nil {
		t.Fatal("EncodeJBIG2Generic() on a 0x0 bitmap: want error, got nil")
	}
}

func TestEncodeJBIG2GenericRejectsShortPixSlice(t *testing.T) {
	b := &Bitmap{Width: 16, Height: 4, Stride: 2, Pix: make([]byte, 3)}
	if _, err := EncodeJBIG2Generic(b); err == nil {
		t.Fatal("EncodeJBIG2Generic() with a truncated Pix: want error, got nil")
	}
}

// The capability string is what UpgradeCandidates keys on (design spec section
// 6), so it is API surface and must not drift.
func TestCapabilityStringIsStable(t *testing.T) {
	if CapabilityJBIG2Generic != "jbig2-generic" {
		t.Errorf("CapabilityJBIG2Generic = %q; want %q", CapabilityJBIG2Generic, "jbig2-generic")
	}
	if slices.Contains(Capabilities(), "jbig2-symbol") {
		t.Error("Capabilities() advertises jbig2-symbol, which this build does not implement")
	}
	if !slices.Contains(Capabilities(), CapabilityJBIG2Generic) {
		t.Errorf("Capabilities() = %v; want it to contain %q", Capabilities(), CapabilityJBIG2Generic)
	}
}
```

- [ ] **Step 3: Implement the public API**

Create `jbig2.go`:

```go
package byblos

import (
	"fmt"

	"github.com/dobbo-ca/byblos/internal/jbig2"
)

// CapabilityJBIG2Generic is the provenance capability string recorded for a
// page compressed with lossless JBIG2 generic region coding. A document whose
// provenance carries it is exactly the upgrade set for a future jbig2-symbol
// capability (see FUTURE.md).
const CapabilityJBIG2Generic = "jbig2-generic"

// EncodeJBIG2Generic compresses a bitonal bitmap with lossless JBIG2 generic
// region coding and returns a JBIG2 bitstream in the embedded file organization
// required by the PDF JBIG2Decode filter.
//
// The coding is lossless: a decoder reconstructs b exactly, so no character can
// be substituted for another. Byblos does not implement lossy symbol matching
// and will not -- see FUTURE.md.
//
// To embed the result as a PDF image XObject, use these dictionary entries and
// nothing else:
//
//	/Type             /XObject
//	/Subtype          /Image
//	/Width            b.Width
//	/Height           b.Height
//	/ColorSpace       /DeviceGray
//	/BitsPerComponent 1
//	/Filter           /JBIG2Decode
//
// No /Decode array: the JBIG2Decode filter already presents a JBIG2 black pixel
// as the DeviceGray sample that renders black, so adding /Decode [1 0] inverts
// the page. No /DecodeParms and no /JBIG2Globals stream either: generic region
// coding produces no page-0 segments. The filter must not be used with inline
// images (ISO 32000-1:2008 7.4.7).
//
// EncodeJBIG2Generic does not copy b.Pix. It zeroes everything in each row past
// pixel b.Width-1: the padding bits inside that pixel's byte, and any whole
// bytes between there and b.Stride when the stride is larger than the minimal
// (b.Width+7)/8. On a well-formed bitmap that is a no-op, since all of it is
// required to be zero already. Pass a copy if that matters.
func EncodeJBIG2Generic(b *Bitmap) ([]byte, error) {
	if b == nil {
		return nil, fmt.Errorf("byblos: EncodeJBIG2Generic: nil bitmap")
	}
	if b.Width <= 0 || b.Height <= 0 {
		return nil, fmt.Errorf("byblos: EncodeJBIG2Generic: bitmap is %dx%d; dimensions must be positive",
			b.Width, b.Height)
	}
	if b.Stride < (b.Width+7)/8 {
		return nil, fmt.Errorf("byblos: EncodeJBIG2Generic: stride %d is too small for width %d",
			b.Stride, b.Width)
	}
	if len(b.Pix) < b.Stride*b.Height {
		return nil, fmt.Errorf("byblos: EncodeJBIG2Generic: Pix is %d bytes; want at least %d",
			len(b.Pix), b.Stride*b.Height)
	}

	// Same packing, same ink convention, so this shares the pixel buffer rather
	// than copying it. EncodeGenericRegion masks padding bits in place, which is
	// a no-op on a well-formed bitmap.
	return jbig2.EmbeddedStream(&jbig2.Bitmap{
		W:      b.Width,
		H:      b.Height,
		Stride: b.Stride,
		Pix:    b.Pix,
	})
}
```

If `byb-b0` has already defined `Capabilities()`, add `CapabilityJBIG2Generic`
to the list it returns. If it has not, create `capabilities.go`:

```go
package byblos

// Capabilities reports what this build of byblos can do. It is recorded in a
// processed document's provenance so that UpgradeCandidates can later identify
// exactly which stored documents a new capability would improve.
func Capabilities() []string {
	return []string{CapabilityJBIG2Generic}
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./... -v`
Expected: PASS.

If `TestEncodeJBIG2GenericReachesSpecVector` fails while
`TestEncodeGenericRegionAnnexH1` passes, the adapter is the bug: the root
`Bitmap`'s packing or ink convention does not match what the adapter assumes.
Fix the adapter (convert explicitly), not the internal package.

- [ ] **Step 5: Commit**

```bash
git add jbig2.go jbig2_test.go bitmap.go capabilities.go
git commit -m "feat: add EncodeJBIG2Generic and the jbig2-generic capability"
```

Drop `bitmap.go` and `capabilities.go` from the `git add` if `byb-b0` already
owned them and Steps 1 and 3 left them untouched.

- [ ] **Step 6: Reconcile with `byb-b0`**

`byb-b2` depends on `byb-b0`, which is still open and owns the module
scaffolding, the root `Bitmap` and the provenance types. Every file this epic
created that B0 also claims has to be recorded on B0's bead, or whoever picks up
B0 will re-create it and collide.

Four steps in this plan branched on whether the file already existed: Task 1
Step 1 (`go.mod`, `Makefile`, `.github/workflows/ci.yml`), and Steps 1 and 3
above (the root `bitmap.go`, `capabilities.go`). Go back and check which branch
each one took — `git log --diff-filter=A --name-only --format=` over this
epic's commits lists exactly the files it added, if that is easier than
remembering.

```bash
bd show byb-b0
```

Then note only the files B2 created rather than found:

```bash
bd update byb-b0 --append-notes "$(cat <<'NOTES'
byb-b2 landed first and already created the following B0-owned files. Do not
re-create them; extend or reconcile instead.

<list only the files B2 actually created, one per line, with a one-line
description of what shape each one has>

The root Bitmap, if B2 created it, is
{Width, Height, Stride int; Pix []byte}, MSB-first packed, 1 = ink. Changing
that layout will fail jbig2_test.go's TestEncodeJBIG2GenericReachesSpecVector,
which is the intended alarm -- it is a silent-corruption seam, not a nuisance.
NOTES
)"
```

If B2 created **none** of them (B0 landed first), skip this step and say so.

---

## Task 8: The round-trip acceptance oracle

This is the `byb-b2` acceptance criterion: **encode a bitmap, decode it with an
independent decoder, assert the result is bit-identical.** Because the encoding
is lossless the check is exact rather than statistical, and it needs no
reference encoder.

**Which decoder.** No pure-Go JBIG2 decoder is available under this plan's
dependency allow-list. Two exist (`github.com/xiaoqidun/jbig2` and
`github.com/dkrisman/gobig2`, both Apache-2.0), but both are very recent,
near-zero-star and unvalidated, and adding either would violate the allow-list
even as a test dependency. **They must be raised as a separate decision, not
added here.** Instead the oracle is `jbig2dec` (Artifex), invoked as an external
test-only binary — the same arrangement the design spec §8 already uses for
`pdfinfo` and `pdftotext`. It is AGPL-3.0, which is irrelevant to a subprocess
that is never linked and never shipped.

`go test ./...` must still pass on a machine without it, so the test does two
things: it runs the live round trip when `jbig2dec` is on `PATH`, and it always
compares the encoder output against committed byte goldens. The goldens are
regenerated with `-update`, which refuses to write unless the live round trip
passed — so a golden can only ever be a stream an independent decoder confirmed.

**Files:**
- Create: `internal/jbig2/roundtrip_test.go`
- Create: `testdata/jbig2/*.jb2` (generated in Step 3)
- Modify: `Makefile`, `.github/workflows/ci.yml`

**Interfaces:**
- Consumes: `EmbeddedStream` (Task 5), `fixtureBitmaps` (Task 4).
- Produces: committed goldens and a `make jbig2-goldens` target.

- [ ] **Step 1: Write the failing test**

Create `internal/jbig2/roundtrip_test.go`:

```go
package jbig2

import (
	"bytes"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
)

var updateGoldens = flag.Bool("update", false,
	"regenerate testdata/jbig2 goldens; requires jbig2dec and verifies the round trip first")

func goldenPath(name string) string {
	return filepath.Join("..", "..", "testdata", "jbig2", name+".jb2")
}

// decodePBM parses a binary PBM (P4): the magic, width and height as
// whitespace-separated tokens, a single whitespace byte, then MSB-first packed
// rows with 1 = black -- the same packing and polarity as Bitmap.
func decodePBM(raw []byte) (*Bitmap, error) {
	isSpace := func(c byte) bool { return c == ' ' || c == '\t' || c == '\r' || c == '\n' }

	i := 0
	// nextToken skips runs of whitespace and '#' comments, then returns the
	// following token. The PBM header ends at the single whitespace byte after
	// the height, which the caller consumes.
	nextToken := func() (string, error) {
		for i < len(raw) {
			if isSpace(raw[i]) {
				i++
				continue
			}
			if raw[i] == '#' {
				for i < len(raw) && raw[i] != '\n' {
					i++
				}
				continue
			}
			break
		}
		start := i
		for i < len(raw) && !isSpace(raw[i]) && raw[i] != '#' {
			i++
		}
		if start == i {
			return "", fmt.Errorf("pbm: unexpected end of header at offset %d", start)
		}
		return string(raw[start:i]), nil
	}

	magic, err := nextToken()
	if err != nil {
		return nil, err
	}
	if magic != "P4" {
		return nil, fmt.Errorf("pbm: magic = %q; want P4", magic)
	}
	var dims [2]int
	for d := range dims {
		tok, err := nextToken()
		if err != nil {
			return nil, err
		}
		n, err := strconv.Atoi(tok)
		if err != nil {
			return nil, fmt.Errorf("pbm: dimension %q: %w", tok, err)
		}
		dims[d] = n
	}
	if i >= len(raw) || !isSpace(raw[i]) {
		return nil, fmt.Errorf("pbm: header is not terminated by whitespace at offset %d", i)
	}
	i++ // exactly one whitespace byte separates the header from the raster

	w, h := dims[0], dims[1]
	if w <= 0 || h <= 0 {
		return nil, fmt.Errorf("pbm: bad dimensions %dx%d", w, h)
	}
	b := NewBitmap(w, h)
	if len(raw[i:]) < len(b.Pix) {
		return nil, fmt.Errorf("pbm: body is %d bytes; want %d", len(raw[i:]), len(b.Pix))
	}
	copy(b.Pix, raw[i:])
	return b, nil
}

// decodeWithJBIG2Dec runs the external decoder over an embedded-organization
// stream and returns the bitmap it produced.
func decodeWithJBIG2Dec(t *testing.T, bin string, stream []byte) *Bitmap {
	t.Helper()
	dir := t.TempDir()
	in := filepath.Join(dir, "in.jb2")
	out := filepath.Join(dir, "out.pbm")
	if err := os.WriteFile(in, stream, 0o644); err != nil {
		t.Fatalf("writing %s: %v", in, err)
	}
	cmd := exec.Command(bin, "-e", "-t", "pbm", "-o", out, in)
	if combined, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("jbig2dec failed: %v\n%s", err, combined)
	}
	raw, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("reading %s: %v", out, err)
	}
	got, err := decodePBM(raw)
	if err != nil {
		t.Fatalf("parsing jbig2dec output: %v", err)
	}
	return got
}

func assertBitmapsIdentical(t *testing.T, name string, got, want *Bitmap) {
	t.Helper()
	if got.W != want.W || got.H != want.H {
		t.Errorf("%s: decoded %dx%d; want %dx%d", name, got.W, got.H, want.W, want.H)
		return
	}
	var diff int
	var firstX, firstY int
	for y := 0; y < want.H; y++ {
		for x := 0; x < want.W; x++ {
			if got.Get(x, y) != want.Get(x, y) {
				if diff == 0 {
					firstX, firstY = x, y
				}
				diff++
			}
		}
	}
	if diff != 0 {
		t.Errorf("%s: round trip is NOT lossless -- %d of %d pixels differ, first at (%d,%d)",
			name, diff, want.W*want.H, firstX, firstY)
	}
}

// TestRoundTripBitIdentical is the byb-b2 acceptance criterion. Encoding is
// lossless, so this is an exact check: every pixel must survive.
func TestRoundTripBitIdentical(t *testing.T) {
	bin, err := exec.LookPath("jbig2dec")
	if err != nil {
		t.Skipf("jbig2dec not installed (brew install jbig2dec): %v", err)
	}
	for name, want := range fixtureBitmaps() {
		t.Run(name, func(t *testing.T) {
			want.MaskPadding()
			stream, err := EmbeddedStream(want)
			if err != nil {
				t.Fatalf("EmbeddedStream() error = %v", err)
			}
			assertBitmapsIdentical(t, name, decodeWithJBIG2Dec(t, bin, stream), want)
		})
	}
}

// TestEncoderGoldens keeps CI honest on a machine with no decoder installed: it
// pins the exact bytes the encoder produces for every fixture. A golden is only
// ever written by -update, which refuses to run without a successful live round
// trip, so every committed golden is a stream jbig2dec confirmed lossless.
func TestEncoderGoldens(t *testing.T) {
	if *updateGoldens {
		bin, err := exec.LookPath("jbig2dec")
		if err != nil {
			t.Fatalf("-update requires jbig2dec: %v", err)
		}
		if err := os.MkdirAll(filepath.Dir(goldenPath("x")), 0o755); err != nil {
			t.Fatalf("creating golden directory: %v", err)
		}
		for name, b := range fixtureBitmaps() {
			// A sub-test per fixture so t.Failed() reflects only this fixture:
			// a golden is written if and only if its own round trip passed.
			t.Run(name, func(t *testing.T) {
				b.MaskPadding()
				stream, err := EmbeddedStream(b)
				if err != nil {
					t.Fatalf("EmbeddedStream() error = %v", err)
				}
				assertBitmapsIdentical(t, name, decodeWithJBIG2Dec(t, bin, stream), b)
				if t.Failed() {
					t.Fatal("refusing to write a golden that does not round-trip")
				}
				if err := os.WriteFile(goldenPath(name), stream, 0o644); err != nil {
					t.Fatalf("writing golden: %v", err)
				}
				t.Logf("wrote %s (%d bytes)", goldenPath(name), len(stream))
			})
		}
		return
	}

	for name, b := range fixtureBitmaps() {
		b.MaskPadding()
		want, err := os.ReadFile(goldenPath(name))
		if err != nil {
			t.Errorf("%s: golden missing (regenerate with: go test ./internal/jbig2/ -update): %v", name, err)
			continue
		}
		got, err := EmbeddedStream(b)
		if err != nil {
			t.Errorf("%s: EmbeddedStream() error = %v", name, err)
			continue
		}
		if !bytes.Equal(got, want) {
			t.Errorf("%s: encoder output changed: %d bytes now, %d in the golden. "+
				"If this is intentional, re-run with -update on a machine with jbig2dec.",
				name, len(got), len(want))
		}
	}
}
```

- [ ] **Step 2: Run the tests — the round trip must pass, the goldens must be missing**

This step is not the usual "verify the new test fails": only half of it is
expected to fail, and which half matters.

Run: `go test ./internal/jbig2/ -run "TestRoundTrip|TestEncoderGoldens" -v`
Expected: `TestRoundTripBitIdentical` **PASSES** (jbig2dec was installed in
Task 1) and `TestEncoderGoldens` **FAILS** with `golden missing` for all nine
fixtures, because Step 3 has not written them yet.

If `TestRoundTripBitIdentical` fails, **stop** — the encoder is not lossless and
nothing else in this task matters. The first differing pixel coordinate in the
failure message localises it: a mismatch at row 0 points at the segment headers
or the region info field, a mismatch at the start of an interior row points at
TPGD, and a scattered mismatch points at the context formation.

- [ ] **Step 3: Generate and commit the goldens**

```bash
go test ./internal/jbig2/ -run TestEncoderGoldens -update -v
ls -l testdata/jbig2/
```

Expected: nine `.jb2` files written, each logged with its size. Sanity-check the
two you can predict:

```bash
xxd testdata/jbig2/border.jb2 | tail -3
```

Expected: the last nine bytes are `04 ee ed 87 fb cb 2b ff ac`, and the file is
76 bytes total (11 + 19 header/page-info, 11 + 35 for the region segment).

- [ ] **Step 4: Verify the tests pass without the oracle**

The no-oracle run needs a `PATH` that contains the Go toolchain **and nothing
else**. Do not write `PATH=/usr/bin:/bin`: `go` is not in either directory on a
Homebrew Mac (so the command simply fails with `go: command not found` and
proves nothing), and on the Linux CI box `apt` installs `jbig2dec` *into*
`/usr/bin` (so where it does work it proves the opposite of what is wanted).
A throwaway directory holding one symlink is the portable answer — and it is
also why `$(dirname "$(command -v go)")` is wrong here: on this machine that is
`/opt/homebrew/bin`, which holds every oracle too.

```bash
go test ./internal/jbig2/ -run "TestRoundTrip|TestEncoderGoldens" -v

NOORACLE=$(mktemp -d) && ln -s "$(command -v go)" "$NOORACLE/go"
env PATH="$NOORACLE" CGO_ENABLED=0 go test ./internal/jbig2/ -run "TestRoundTrip|TestEncoderGoldens" -v
```

(`env` resolves `go` using the `PATH` it was handed, which is why the symlink is
enough. Everything else the toolchain needs — `HOME`, the build cache, `GOROOT`
— is untouched.)

Expected: first command — both PASS. Second command (with `jbig2dec` off
`PATH`) — `TestRoundTripBitIdentical` SKIPS with the install hint,
`TestEncoderGoldens` PASSES. That is the "tests pass with no oracles installed"
constraint demonstrated, not assumed. If the second command reports
`go: command not found`, the symlink did not get made — fix that before reading
anything into the result.

- [ ] **Step 5: Wire the golden target and CI**

Both files were created in Task 1 Step 1, so this step **extends** them.

Append the goldens target to the `Makefile` and add it to the `.PHONY` line:

```make
.PHONY: test lint build jbig2-goldens

# Regenerates the committed JBIG2 encoder goldens. Requires jbig2dec, which
# verifies each stream round-trips losslessly before the golden is written.
# Manual step -- never run in CI. Commit the results.
jbig2-goldens:
	go test ./internal/jbig2/ -run TestEncoderGoldens -update -v
```

Append a second job to `.github/workflows/ci.yml` so the two `byb-b2` acceptance
criteria — the bit-identical round trip **and** beating CCITT G4 on the corpus —
actually execute in CI rather than skipping forever. Both are oracle-backed, so
without this job neither is ever checked anywhere but a laptop. The workflow-level
`env: CGO_ENABLED: "0"` from Task 1 already covers this job.

```yaml
  roundtrip:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with:
          go-version: '1.26'
      - name: install the test-only oracles
        run: sudo apt-get update && sudo apt-get install -y jbig2dec imagemagick libtiff-tools
      - name: confirm the oracles are on PATH
        # A missing binary makes the test t.Skip, which would turn this whole
        # job into a silent no-op. Fail here instead, loudly.
        run: |
          command -v jbig2dec
          command -v magick || command -v convert
          command -v tiffdump
      - run: go test ./internal/jbig2/ -run "TestRoundTrip|TestBeats" -v
```

Leave the `test` job alone: it runs without any oracle installed and must stay
that way, because that is what proves the skip paths work.

The `TestBeats` half of the pattern matches nothing yet — those tests arrive in
Task 10, and `-run` with no match is a silent no-op until then. The oracles they
need are installed here so the job does not have to change again.

**Then read the first CI run's log for this job and confirm every
`TestRoundTripBitIdentical` subtest says `--- PASS`, not `--- SKIP`.** A green
job proves nothing on its own; the whole point of the job is that these tests
really ran. If anything skipped, find out which binary is missing and raise it
rather than leaving the job as decoration. Task 10 Step 3 re-checks the same log
once `TestBeatsCCITTG4OnCorpus` exists.

- [ ] **Step 6: Verify the whole suite**

```bash
CGO_ENABLED=0 go build ./... && CGO_ENABLED=0 go vet ./... && go test ./... -v
```

Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/jbig2/roundtrip_test.go testdata/jbig2 Makefile .github/workflows/ci.yml
git commit -m "test(jbig2): add the jbig2dec round-trip oracle and encoder goldens"
```

---

## Task 9: PDF `JBIG2Decode` embedding and the polarity proof

JBIG2 encodes 1 as black; PDF `DeviceGray` encodes 1 as white. That asymmetry is
the classic inverted-output bug in every JBIG2-in-PDF implementation, and it has
exactly two possible resolutions — the `JBIG2Decode` filter inverts internally
(so no `/Decode` array is needed) or it does not (so `/Decode [1 0]` is
required). Reading the spec does not settle it: ISO 32000-1 §7.4.7 says nothing
about polarity.

**It was settled empirically while this plan was written, against poppler: no
`/Decode` array is correct.** With `/Decode [1 0]` the page comes out inverted.
This task re-proves that in the repo so it stays proven, and it is why
`EncodeJBIG2Generic`'s doc comment names the exact dictionary entries.

The PDF writer here is **test-only and hand-rolled**, deliberately. `pdfcpu` is
permitted, but B2 has no business taking a PDF-writing dependency to prove a
polarity fact — real PDF assembly is `byb-b1` and `byb-b5`.

**Files:**
- Create: `internal/jbig2/pdfembed_test.go`

**Interfaces:**
- Consumes: `EmbeddedStream` (Task 5), `figureH6` (Task 4).
- Produces: nothing exported.

- [ ] **Step 1: Write the failing test**

Create `internal/jbig2/pdfembed_test.go`:

```go
package jbig2

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// buildTestPDF writes a one-page PDF whose only content is a JBIG2 image
// XObject covering the page. decodeEntry is spliced into the image dictionary
// so the test can compare the presence and absence of a /Decode array.
//
// This writer exists only to feed poppler. Real PDF assembly is byb-b1/byb-b5.
func buildTestPDF(b *Bitmap, jb2 []byte, decodeEntry string) []byte {
	var buf bytes.Buffer
	var offsets []int
	obj := func(head string, stream []byte) {
		offsets = append(offsets, buf.Len())
		buf.WriteString(head)
		if stream != nil {
			buf.Write(stream)
			buf.WriteString("\nendstream\nendobj\n")
		}
	}

	buf.WriteString("%PDF-1.4\n")
	obj("1 0 obj\n<< /Type /Catalog /Pages 2 0 R >>\nendobj\n", nil)
	obj("2 0 obj\n<< /Type /Pages /Kids [3 0 R] /Count 1 >>\nendobj\n", nil)
	obj(fmt.Sprintf("3 0 obj\n<< /Type /Page /Parent 2 0 R /MediaBox [0 0 %d %d] "+
		"/Resources << /XObject << /Im0 5 0 R >> >> /Contents 4 0 R >>\nendobj\n", b.W, b.H), nil)
	content := fmt.Sprintf("q %d 0 0 %d 0 0 cm /Im0 Do Q", b.W, b.H)
	obj(fmt.Sprintf("4 0 obj\n<< /Length %d >>\nstream\n", len(content)), []byte(content))
	obj(fmt.Sprintf("5 0 obj\n<< /Type /XObject /Subtype /Image /Width %d /Height %d "+
		"/ColorSpace /DeviceGray /BitsPerComponent 1 %s/Filter /JBIG2Decode /Length %d >>\nstream\n",
		b.W, b.H, decodeEntry, len(jb2)), jb2)

	xref := buf.Len()
	fmt.Fprintf(&buf, "xref\n0 %d\n0000000000 65535 f \n", len(offsets)+1)
	for _, o := range offsets {
		fmt.Fprintf(&buf, "%010d 00000 n \n", o)
	}
	fmt.Fprintf(&buf, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n",
		len(offsets)+1, xref)
	return buf.Bytes()
}

// extractWithPdfimages runs poppler's pdfimages over a PDF and returns the
// single image it extracts, as a bitmap.
func extractWithPdfimages(t *testing.T, bin string, pdf []byte) *Bitmap {
	t.Helper()
	dir := t.TempDir()
	in := filepath.Join(dir, "in.pdf")
	if err := os.WriteFile(in, pdf, 0o644); err != nil {
		t.Fatalf("writing %s: %v", in, err)
	}
	cmd := exec.Command(bin, in, filepath.Join(dir, "img"))
	if combined, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("pdfimages failed: %v\n%s", err, combined)
	}
	matches, err := filepath.Glob(filepath.Join(dir, "img-*.pbm"))
	if err != nil || len(matches) != 1 {
		t.Fatalf("pdfimages produced %v (err %v); want exactly one .pbm", matches, err)
	}
	raw, err := os.ReadFile(matches[0])
	if err != nil {
		t.Fatalf("reading %s: %v", matches[0], err)
	}
	got, err := decodePBM(raw)
	if err != nil {
		t.Fatalf("parsing pdfimages output: %v", err)
	}
	return got
}

// TestPDFEmbeddingIsBitIdentical is the second, independent decoder oracle: the
// stream goes through a real PDF and poppler's own JBIG2 implementation, not
// jbig2dec. Two independent decoders agreeing is far stronger evidence than one.
func TestPDFEmbeddingIsBitIdentical(t *testing.T) {
	bin, err := exec.LookPath("pdfimages")
	if err != nil {
		t.Skipf("pdfimages not installed (brew install poppler): %v", err)
	}
	for name, want := range fixtureBitmaps() {
		t.Run(name, func(t *testing.T) {
			want.MaskPadding()
			jb2, err := EmbeddedStream(want)
			if err != nil {
				t.Fatalf("EmbeddedStream() error = %v", err)
			}
			pdf := buildTestPDF(want, jb2, "")
			assertBitmapsIdentical(t, name, extractWithPdfimages(t, bin, pdf), want)
		})
	}
}

// TestPDFDecodeArrayWouldInvert pins the polarity decision. JBIG2 1 = black and
// DeviceGray 1 = white, so it is not obvious which way round the filter works;
// ISO 32000-1 7.4.7 does not say. This asserts both directions: without /Decode
// the image is correct, and with /Decode [1 0] it is exactly inverted.
//
// If this ever starts failing, the fix is to change EncodeJBIG2Generic's
// documented dictionary entries -- not to "adjust" the encoder, which is pinned
// by the Annex H.1 vector.
func TestPDFDecodeArrayWouldInvert(t *testing.T) {
	bin, err := exec.LookPath("pdfimages")
	if err != nil {
		t.Skipf("pdfimages not installed (brew install poppler): %v", err)
	}
	want := figureH6()
	jb2, err := EmbeddedStream(want)
	if err != nil {
		t.Fatalf("EmbeddedStream() error = %v", err)
	}

	plain := extractWithPdfimages(t, bin, buildTestPDF(want, jb2, ""))
	assertBitmapsIdentical(t, "no /Decode", plain, want)

	inverted := extractWithPdfimages(t, bin, buildTestPDF(want, jb2, "/Decode [1 0] "))
	if inverted.W != want.W || inverted.H != want.H {
		t.Fatalf("/Decode [1 0] produced %dx%d; want %dx%d",
			inverted.W, inverted.H, want.W, want.H)
	}
	var same int
	for y := 0; y < want.H; y++ {
		for x := 0; x < want.W; x++ {
			if inverted.Get(x, y) == want.Get(x, y) {
				same++
			}
		}
	}
	if same != 0 {
		t.Errorf("/Decode [1 0] matched the source in %d of %d pixels; expected a total inversion. "+
			"If the filter's polarity has changed, update EncodeJBIG2Generic's documented image dictionary.",
			same, want.W*want.H)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/jbig2/ -run TestPDF -v`
Expected: FAIL — `undefined: buildTestPDF` on the first compile, then, once the
file is in place, PASS. (Both tests and their helpers ship in the same file, so
this step is really "run it and confirm the assertions hold".)

- [ ] **Step 3: Run and inspect**

```bash
go test ./internal/jbig2/ -run TestPDF -v
```

Expected: PASS. `TestPDFEmbeddingIsBitIdentical` runs nine subtests, all
bit-identical through poppler; `TestPDFDecodeArrayWouldInvert` confirms the
`/Decode [1 0]` variant matches in **zero** pixels.

If `pdfimages` reports the image as something other than JBIG2, check the filter
name and the `/Length` field:

```bash
pdfimages -list /tmp/anything.pdf
```

Expected column values: `image  54  44  gray  1  1  jbig2`.

**Optional, and worth doing once: put it through an actual rasteriser.**
`pdfimages` applies the `JBIG2Decode` filter and the `/Decode` array — which is
exactly what the polarity question turns on — but it never executes the content
stream or the graphics pipeline, so on its own it is slightly less than "a real
renderer said so". `pdftoppm` closes that gap in four lines. Dump the two PDFs
the test already builds (add a throwaway `os.WriteFile(..., buildTestPDF(want,
jb2, ""), 0o644)` and one with `"/Decode [1 0] "`, then delete it), and:

```bash
pdftoppm -r 72 -gray -png /tmp/polarity-plain.pdf  /tmp/pp-plain
pdftoppm -r 72 -gray -png /tmp/polarity-decode.pdf /tmp/pp-decode
magick /tmp/pp-plain-1.png  -format "corner=%[pixel:p{0,0}] centre=%[pixel:p{27,22}]" info:; echo
magick /tmp/pp-decode-1.png -format "corner=%[pixel:p{0,0}] centre=%[pixel:p{27,22}]" info:; echo
```

Expected, measured while this plan was written: plain → `corner=srgb(0,0,0)`
(the 2-pixel ink border, black) and `centre=srgb(255,255,255)` (paper, white);
`/Decode [1 0]` → exactly the reverse. That is the rendered confirmation of what
`TestPDFDecodeArrayWouldInvert` asserts at the filter level.

- [ ] **Step 4: Verify the skip path**

Same minimal-`PATH` construction as Task 8 Step 4, and for the same reason —
`/usr/bin:/bin` neither contains `go` on a Homebrew Mac nor excludes the oracles
on Linux.

```bash
NOORACLE=$(mktemp -d) && ln -s "$(command -v go)" "$NOORACLE/go"
env PATH="$NOORACLE" CGO_ENABLED=0 go test ./internal/jbig2/ -run TestPDF -v
```

Expected: both tests SKIP with the `brew install poppler` hint. No failure.

- [ ] **Step 5: Commit**

```bash
git add internal/jbig2/pdfembed_test.go
git commit -m "test(jbig2): prove PDF JBIG2Decode embedding and polarity through poppler"
```

---

## Task 10: Compression comparison against CCITT G4, and closing the epic

The `byb-b2` acceptance criteria are two: the round trip is bit-identical
(Task 8, done) and **compression beats CCITT G4 on the corpus**. This task
measures the second.

There is no G4 *encoder* available — `golang.org/x/image/ccitt` decodes only —
so the baseline comes from ImageMagick writing a Group 4 TIFF, with the payload
size read out of the TIFF's `StripByteCounts` tag so the container overhead is
excluded. That is approximate: ImageMagick's G4 writer may emit an EOFB and may
byte-align rows, worth a handful of bytes. The comparison therefore asserts a
ratio with margin, not an exact figure.

A second, exact, tool-free data point is available from the spec itself: T.88
Annex H.1 encodes the *same* 54x44 figure both ways, as 26 bytes of MMR
(segment 4) and 9 bytes of arithmetic generic region (segment 11). That is a
2.9x win published by the standard, and it needs no ImageMagick at all.

**Files:**
- Create: `internal/jbig2/compare_test.go`

- [ ] **Step 1: Write the failing test**

Create `internal/jbig2/compare_test.go`:

```go
package jbig2

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"testing"
)

// TestBeatsMMROnSpecFigure is the tool-free half of the comparison. T.88 Annex
// H.1 encodes Figure H.6 twice: as MMR in segment 4 (26 bytes of region data)
// and as an arithmetic generic region in segment 11 (9 bytes). Both numbers
// come from the standard, so this assertion needs nothing installed.
func TestBeatsMMROnSpecFigure(t *testing.T) {
	const specMMRBytes = 26
	got := len(EncodeGenericRegion(figureH6(), true))
	if got != 9 {
		t.Fatalf("Figure H.6 encoded to %d bytes; want 9 (T.88 Annex H.1 segment 11)", got)
	}
	if got >= specMMRBytes {
		t.Errorf("generic region (%d bytes) did not beat the spec's own MMR encoding (%d bytes)",
			got, specMMRBytes)
	}
	t.Logf("Figure H.6: generic region %d bytes vs MMR %d bytes (%.2fx)",
		got, specMMRBytes, float64(specMMRBytes)/float64(got))
}

// writePBM writes b as a binary PBM, which is what ImageMagick reads.
func writePBM(path string, b *Bitmap) error {
	out := append([]byte(fmt.Sprintf("P4\n%d %d\n", b.W, b.H)), b.Pix...)
	return os.WriteFile(path, out, 0o644)
}

// stripByteCountsRE matches tiffdump's single-strip form, `... 1<139>`. A
// multi-strip TIFF prints `... 3<100 200 300>`, which this deliberately does
// NOT match: g4PayloadBytes then fails loudly rather than silently reporting
// the first strip and inflating the ratio.
var stripByteCountsRE = regexp.MustCompile(`StripByteCounts \(279\)[^<]*<(\d+)>`)

// lookImageMagick finds the ImageMagick CLI. ImageMagick 7 installs it as
// `magick`; ImageMagick 6, which is still what Debian and Ubuntu package,
// installs it as `convert`. Both take the arguments used below unchanged.
//
// Only the `magick` (IM7) path has been measured. If `convert` produces a
// materially different StripByteCounts, log both and raise it -- do not relax
// the bound to accommodate it.
func lookImageMagick() (string, error) {
	for _, name := range []string{"magick", "convert"} {
		if p, err := exec.LookPath(name); err == nil {
			return p, nil
		}
	}
	return "", fmt.Errorf("neither magick nor convert is installed (brew install imagemagick)")
}

// g4PayloadBytes compresses b as a CCITT Group 4 TIFF and returns the size of
// the compressed strip, excluding TIFF container overhead.
func g4PayloadBytes(t *testing.T, magick, tiffdump string, b *Bitmap) int {
	t.Helper()
	dir := t.TempDir()
	pbm := filepath.Join(dir, "in.pbm")
	tif := filepath.Join(dir, "out.tif")
	if err := writePBM(pbm, b); err != nil {
		t.Fatalf("writing %s: %v", pbm, err)
	}
	if out, err := exec.Command(magick, pbm, "-compress", "Group4", tif).CombinedOutput(); err != nil {
		t.Fatalf("magick failed: %v\n%s", err, out)
	}
	out, err := exec.Command(tiffdump, tif).CombinedOutput()
	if err != nil {
		t.Fatalf("tiffdump failed: %v\n%s", err, out)
	}
	m := stripByteCountsRE.FindSubmatch(out)
	if m == nil {
		t.Fatalf("no single-strip StripByteCounts in tiffdump output:\n%s", out)
	}
	n, err := strconv.Atoi(string(m[1]))
	if err != nil {
		t.Fatalf("parsing StripByteCounts %q: %v", m[1], err)
	}
	return n
}

// TestBeatsCCITTG4OnCorpus is the byb-b2 compression acceptance criterion.
//
// Every fixture must beat G4 at all. The text page carries a much tighter bound
// because it is the case that matters for Kleio and the case the design spec
// makes a promise about. Measured while this plan was written, on the fixtures
// below: text 14.10x (jbig2 870 B, G4 12268 B), empty 9.25x, full 3.00x,
// border 2.44x, noise 2.11x.
func TestBeatsCCITTG4OnCorpus(t *testing.T) {
	magick, err := lookImageMagick()
	if err != nil {
		t.Skipf("%v", err)
	}
	tiffdump, err := exec.LookPath("tiffdump")
	if err != nil {
		t.Skipf("tiffdump not installed (brew install libtiff): %v", err)
	}

	// Fixtures too small for the comparison to mean anything: at these sizes the
	// MQ flush terminator and the G4 EOFB dominate the measurement.
	skip := map[string]bool{"single": true, "odd": true, "row": true, "column": true}

	for name, b := range fixtureBitmaps() {
		if skip[name] {
			continue
		}
		t.Run(name, func(t *testing.T) {
			b.MaskPadding()
			ours := len(EncodeGenericRegion(b, true))
			g4 := g4PayloadBytes(t, magick, tiffdump, b)
			ratio := float64(g4) / float64(ours)
			t.Logf("%s (%dx%d): jbig2 %d bytes, ccitt-g4 %d bytes, %.2fx", name, b.W, b.H, ours, g4, ratio)
			if ratio <= 1.0 {
				t.Errorf("%s: jbig2 generic region (%d bytes) did not beat CCITT G4 (%d bytes)",
					name, ours, g4)
			}
			// 14.10x was measured on this exact fixture. The bound is set at 8x:
			// low enough that ordinary encoder churn cannot trip it, high enough
			// that passing it actually establishes the design spec section 5
			// claim of "roughly 2-4x better compression than CCITT G4" instead of
			// sitting below it. A bound of 1.5x here would be unfalsifiable.
			if name == "text" && ratio < 8 {
				t.Errorf("text page: only %.2fx better than CCITT G4; expected at least 8x "+
					"(14.10x was measured while this plan was written, jbig2 870 B vs G4 12268 B). "+
					"Do not widen this bound -- work through the order in Task 10 Step 2 first, "+
					"and re-measure against a real scan before relaxing anything.", ratio)
			}
		})
	}
}
```

- [ ] **Step 2: Run the tests**

Run: `go test ./internal/jbig2/ -run "TestBeatsMMR|TestBeatsCCITT" -v`

Expected: `TestBeatsMMROnSpecFigure` PASSES and logs `9 bytes vs MMR 26 bytes
(2.89x)`. `TestBeatsCCITTG4OnCorpus` PASSES with a logged ratio per fixture,
matching the figures measured while this plan was written:

| fixture | jbig2 | ccitt-g4 | ratio |
|---|---|---|---|
| `text` (640x480) | 870 | 12268 | 14.10x |
| `empty` (200x120) | 4 | 37 | 9.25x |
| `full` (200x120) | 6 | 18 | 3.00x |
| `border` (54x44) | 9 | 22 | 2.44x |
| `noise` (101x73) | 939 | 1981 | 2.11x |

Record every logged ratio — they go into the bead in Step 5.

**If the `text` bound fails**, do not just widen it. Work in this order:

1. **Look at the jbig2 byte count, not the ratio.** 870 bytes is the measured
   figure. If it is still near 870, the encoder is fine and the G4 baseline
   moved — check `g4PayloadBytes`: is `tiffdump` still reporting a single strip,
   and is the ImageMagick on `PATH` really writing Group 4? (An ImageMagick 6
   `convert` was never measured; see `lookImageMagick`.)
2. **If the jbig2 count is far from 870, the encoder changed.** Run
   `TestEncodeGenericRegionAnnexH1` — it pins the coder to the spec's own nine
   bytes and localises this immediately.
3. **Do not start with TPGD.** On this fixture TPGD is worth almost nothing:
   `EncodeGenericRegion(textPageBitmap(640,480), true)` is **870** bytes and
   `..., false)` is **891** — 2.4%. Its blank rows are few and its glyph rows
   never repeat. Comparing those two numbers will tell you nothing, and reading
   2.4% as "TPGD is broken" would send you after a non-bug. TPGD's leverage is
   already pinned where it exists, by `TestTPGDShrinksRepeatedRows` on Figure
   H.6 (20 bytes → 9).
4. Is `textPageBitmap` still producing anything resembling text? Dump it with
   `writePBM` and look at it.
5. Only after all of that, consider whether the bound is wrong — and re-measure
   against a real scan before relaxing it.

- [ ] **Step 3: Verify the full suite, clean, with no oracles**

```bash
CGO_ENABLED=0 go build ./...
CGO_ENABLED=0 go vet ./...
test -z "$(gofmt -l .)" || { gofmt -l .; false; }
go test ./... -v

NOORACLE=$(mktemp -d) && ln -s "$(command -v go)" "$NOORACLE/go"
env PATH="$NOORACLE" CGO_ENABLED=0 go test ./... -v
```

Expected: all succeed. The last one is the constraint check — with no oracle on
`PATH`, every oracle-backed test skips and the suite is still green. It uses the
one-symlink directory from Task 8 Step 4 rather than `PATH=/usr/bin:/bin`, which
would not find `go` on a Homebrew Mac and would not hide `jbig2dec` on Linux.

Then re-read the CI log for the `roundtrip` job added in Task 8 Step 5 and
confirm `TestBeatsCCITTG4OnCorpus/text` now appears as `--- PASS` with its
logged ratio, not `--- SKIP`. This is the point at which the bead's second
acceptance criterion starts being checked by something other than a laptop.

- [ ] **Step 4: Verify the dependency allow-list held**

```bash
go list -m all | grep -v "^github.com/dobbo-ca/byblos"
```

Expected: only `golang.org/x/image` (and its own requirements: `golang.org/x/text`
may appear), plus `github.com/pdfcpu/pdfcpu` and its transitive set if `byb-b0`
or `byb-b1` added it. **Any JBIG2 decoder module in this list is a plan
violation** — remove it and use the external-binary oracle.

- [ ] **Step 5: Commit and close the epic**

```bash
git add internal/jbig2/compare_test.go
git commit -m "test(jbig2): compare generic region compression against CCITT G4"
```

```bash
bd update byb-b2 --append-notes "$(cat <<'NOTES'
B2 complete. Lossless JBIG2 generic region encoder, no symbol dictionary,
no lossy symbol matching.

Conformance:
- MQ arithmetic coder reproduces T.88 Annex H.2 (30 bytes) exactly.
- Generic region reproduces T.88 Annex H.1 segment 11
  (04 EE ED 87 FB CB 2B FF AC) exactly.
- Segment headers and page-info/region-info fields match T.88 Annex H.1.

Acceptance oracle (round trip, bit-identical, exact not statistical):
- jbig2dec -e over 9 fixtures: bit-identical.
- poppler pdfimages through a real PDF over the same 9 fixtures: bit-identical.
- Two independent decoders, so this is a cross-check rather than a self-check.

Compression vs CCITT G4: <paste the per-fixture ratios logged in Step 2>

PDF embedding: embedded file organization, no file header, no end-of-page or
end-of-file segment, page association 1, no /DecodeParms and no /JBIG2Globals
(generic region coding emits no page-0 segments). Image dictionary takes NO
/Decode array -- proven by TestPDFDecodeArrayWouldInvert, which shows
/Decode [1 0] inverts every pixel.

Capability string: jbig2-generic.
NOTES
)"
bd close byb-b2
```

---

## Self-Review

**Scope coverage.** `byb-b2`'s description is "MQ arithmetic coder plus template
prediction, lossless generic region coding, wrapped in the PDF JBIG2Decode
filter." Task 2 is the MQ coder, Tasks 3-4 and 6 are template prediction and
generic region coding, Task 5 and Task 9 are the PDF wrapping. Its acceptance
criteria are "round-trip through an independent JBIG2 decoder returns a
bit-identical bitmap" (Task 8, plus a second independent decoder in Task 9) and
"compression beats CCITT G4 on the corpus" (Task 10).

**The rejected work stays rejected.** No task builds, references, or leaves a
seam for a symbol dictionary or symbol matching. `doc.go` and
`EncodeJBIG2Generic`'s doc comment both state the rejection and point at
`FUTURE.md`; `TestCapabilityStringIsStable` asserts that `Capabilities()` does
not advertise `jbig2-symbol`. The lossless *symbol dictionary* remains a
legitimate future capability, and `jbig2-generic` is exactly the provenance
marker that will identify its upgrade set.

**MQ coder isolation.** Task 2 is a separate file with a separate test file, and
its conformance vector must pass before Task 3 is started. The failure guidance
points at Table H.1's 257-row register trace rather than at guesswork, because
the register trace is what makes an arithmetic coder debuggable at all.

**Placeholder scan.** Every code step contains complete code and every test step
contains real assertions with real expected values. The expected byte strings in
Tasks 2, 4, 5 and 10 were transcribed from the ITU-T T.88 text and then verified
end to end against a working prototype while this plan was written; they are
facts, not sketches. Task 6's acceptance is a numeric speedup bound (1.5x) with
an explicit "revert if not met" instruction rather than "make it faster". Task 10
carries a numeric ratio bound with an explicit "investigate in this order, do
not widen" instruction. Task 8's `-update` path refuses to write a golden that
has not round-tripped, so a golden can never silently encode a bug.

**Type consistency.** `Bitmap` (Task 1) is consumed by Tasks 3-6 and 8-10.
`contexts` and `encoder` (Task 2) are consumed by Tasks 4 and 6.
`contextTemplate0` and `sltpContextTemplate0` (Task 3) are consumed by Task 4 and
by Task 6's reference implementation. `EncodeGenericRegion` (Task 4) is consumed
by Tasks 5, 6 and 10. `EmbeddedStream` (Task 5) is consumed by Tasks 7, 8 and 9.
`fixtureBitmaps` and `figureH6` (Task 4) are consumed by Tasks 6, 8, 9 and 10.
`decodePBM` and `assertBitmapsIdentical` are defined in Task 8's
`roundtrip_test.go` and reused by Task 9's `pdfembed_test.go` — same package, so
no export is needed, but Task 9 cannot be done before Task 8.

**Oracle discipline.** Four external tools are used and all four are optional:
`jbig2dec`, `pdfimages`, `magick` (or ImageMagick 6's `convert`), `tiffdump`.
Every test that touches one calls `exec.LookPath` and `t.Skipf`s. Task 8 Step 4,
Task 9 Step 4 and Task 10 Step 3 demonstrate the no-oracle path explicitly, with
a `PATH` containing one symlink to `go` and nothing else — not `/usr/bin:/bin`,
which contains no `go` on a Homebrew Mac and does contain `jbig2dec` on Linux.
The converse risk — an oracle-backed test skipping forever and being mistaken
for a passing one — is handled by Task 8 Step 5's CI job, which installs all
three Linux-side oracles, fails if any is missing, and is read back at Task 8
Step 5 and Task 10 Step 3 to confirm `PASS` rather than `SKIP`.
`golang.org/x/image/ccitt` is the one in-process oracle, it is on the permitted
dependency list, and Task 4 Step 1 verifies its behaviour before anything relies
on it.

---

## Verified facts, and what is still unverified

Everything in this section was established while writing the plan, not assumed.

**Verified byte-exactly against ITU-T T.88 and against running code:**

- The Table E.1 Qe/NMPS/NLPS/SWITCH values, and the E.2.3-E.2.9 encoder
  (CODEMPS, CODELPS, RENORME, BYTEOUT with carry and stuffing, INITENC, FLUSH
  with SETBITS): reproduce the Annex H.2 30-byte vector exactly.
- **Omitting the optional E.2.10 trailing-`0x7FFF` trim is correct**, and is what
  makes both spec vectors reproduce byte for byte.
- The template-0 context bit order (reading order, MSB = top-left; runs of
  5/7/4 over rows y-2, y-1, y), the nominal AT positions, out-of-bounds = 0, the
  SLTP context `0x9B25`, and the TPGD state machine: together they reproduce
  Annex H.1 segment 11's `04 EE ED 87 FB CB 2B FF AC` exactly.
- Figure H.6 is a 54x44 rectangle with a 2-pixel border, recovered by MMR-decoding
  Annex H.1 segment 4 with `golang.org/x/image/ccitt` v0.44.0 using
  `ccitt.MSB`, `ccitt.Group4`, `&ccitt.Options{Invert: true}`.
- The segment header layout produces Annex H.1's exact header bytes for both the
  page information segment and the generic region segment.
- The sliding-window context update is byte-identical to per-pixel context
  formation across structured, noise, single-pixel, single-row, single-column,
  empty and non-byte-aligned bitmaps, with TPGD both on and off.
- `jbig2dec -e -t pbm -o out.pbm in.jb2` decodes the two-segment embedded stream,
  and `pdfimages` extracts it from a PDF; both return bit-identical bitmaps.
- **No `/Decode` array**: with it absent poppler renders the page correctly; with
  `/Decode [1 0]` it renders inverted. Confirmed twice — at the filter level by
  `pdfimages` (matching in exactly **0** pixels with `/Decode [1 0]`), and by
  actually rasterising with `pdftoppm -r 72 -gray`: no `/Decode` gives a black
  border corner and a white centre, `/Decode [1 0]` gives the reverse.
- TPGD's value depends entirely on how many rows repeat exactly. Figure H.6:
  20 bytes → 9. `textPageBitmap(640, 480)`: **891 bytes → 870**, only 2.4%. On
  uniform noise it costs about two bytes. It is not "the single largest win" on
  an arbitrary page and the plan does not claim it is.
- **Compression against CCITT G4**, measured with ImageMagick 7 `magick` +
  `tiffdump` on this plan's own fixtures: text 870 B vs 12268 B (**14.10x**),
  empty 4 vs 37 (9.25x), full 6 vs 18 (3.00x), border 9 vs 22 (2.44x), noise
  939 vs 1981 (2.11x). The `text` bound in `TestBeatsCCITTG4OnCorpus` is set at
  8x from this measurement.
- **A `Stride` larger than the minimal `(W+7)/8` is accepted and used to cost
  2.2x compression silently.** `MaskPadding` masked the last byte of the stride
  rather than the byte holding pixel `W-1`, and `RowEqualAbove` compares whole
  strides, so junk in the trailing bytes suppressed TPGD while the round trip
  stayed lossless. Measured on a 100x200 bordered bitmap at `Stride 16`: 28
  bytes before the fix, 12 after — the same as at minimal `Stride 13`.
  `TestBitmapMaskPaddingHandlesNonMinimalStride` and
  `TestEncodeGenericRegionIgnoresNonMinimalStride` both fail against the old
  `MaskPadding` and pass against the corrected one.
- `TestEmbeddedStreamRejectsOversizeBitmap` written as `W: 1 << 33` does not
  compile where `int` is 32 bits (`GOOS=linux GOARCH=386 go vet ./internal/jbig2/`
  reports `cannot use 1 << 33 ... as int value`). Routing the width through an
  `int64` variable, with a skip when `int` is too narrow, compiles and vets
  clean on both widths.
- `stripByteCountsRE` does **not** match tiffdump's multi-strip form
  `... 3<100 200 300>` — verified against Go's `regexp`, which returns no match
  and so takes `g4PayloadBytes`'s `t.Fatalf` path. It cannot silently report
  only the first strip.

**Where the plan tells the implementer to verify rather than guess:**

1. **The root `byblos.Bitmap` layout** (Task 1 Step 2, Task 7 Step 1). `byb-b0`
   owns it and had not landed when this plan was written — there was no `go.mod`
   in the repo. The plan gives both branches: adapt to what B0 shipped, or create
   the type with the stated layout. Task 7's `TestEncodeJBIG2GenericReachesSpecVector`
   is what catches a mismatch, because a wrong packing or ink convention still
   compiles.
2. **`golang.org/x/image/ccitt` decode options** (Task 4 Step 1). Verified at
   v0.44.0 only. `TestFigureH6MatchesSpecMMR` proves the fixture before anything
   depends on it, and the plan says to fix the options — or, failing that, drop
   to the hand-built fixture and record the loss — rather than adjust the expected
   bitmap.
3. **`golang.org/x/image` version drift** (Task 1 Step 4). Pin back to v0.44.0
   and raise the difference if a later version breaks Task 4's fixture test.
4. **SLTP contexts for GBTEMPLATE 1, 2 and 3** (`0x0795`, `0x00E5`, `0x0195`).
   Only template 0's `0x9B25` is verified. This plan implements template 0 only,
   so the others are out of scope — but if anyone adds them, **re-read T.88
   Figures 9-11** rather than trusting those three numbers.
5. **The `/Decode` polarity finding beyond poppler** (Task 9). It was verified
   against poppler three ways — `pdfimages`, `pdftoppm` rasterisation, and
   jbig2dec's own PBM output — all of which agree. It has **not** been checked
   in Acrobat, macOS Preview, or pdf.js. Task 9's test is written so that if a
   different renderer ever disagrees, the fix is to change the documented
   dictionary entries — never the encoder, which is pinned by the Annex H.1
   vector.
6. **The CCITT G4 baseline is approximate** (Task 10). ImageMagick's Group 4
   writer may emit an EOFB and may byte-align rows, so `StripByteCounts` is a few
   bytes larger than a minimal G4 payload. The test therefore asserts a ratio
   with margin, and the exact, tool-free comparison against the spec's own MMR
   encoding of Figure H.6 is asserted separately.
7. **Whether the synthetic text page is representative** (Task 10 Step 2). The
   `text >= 8x` bound is calibrated on a synthetic page measured at 14.10x, not
   on a real scan. The plan gives an explicit five-step order to work through
   before touching the bound, starting from the absolute byte count rather than
   the ratio, and requires re-measuring against a real scan before any
   relaxation. A real-scan corpus is out of scope for B2.
8. **ImageMagick 6 (`convert`) has not been measured** (Task 10). Only the
   ImageMagick 7 `magick` binary produced the ratios above. `lookImageMagick`
   falls back to `convert` so the CI job on Debian/Ubuntu is not a silent skip,
   but if `convert` reports a materially different `StripByteCounts` the
   instruction is to log both and raise it, not to relax the bound. Task 8
   Step 5's CI job fails loudly if neither binary is present, and Task 10 Step 3
   requires reading the CI log to confirm the test ran rather than skipped.
9. **Which of `go.mod`, `Makefile`, `.github/workflows/ci.yml`, the root
   `bitmap.go` and `capabilities.go` this epic ends up creating** depends on
   whether `byb-b0` lands first. Task 1 Step 1 and Task 7 Steps 1 and 3 branch
   on it explicitly, and Task 7 Step 6 writes the outcome onto `byb-b0` with
   `bd update --append-notes` so the collision is reconciled on the bead rather
   than left in a commit message.
10. **Two pure-Go JBIG2 decoders exist and are deliberately not used**
    (Task 8 preamble). `github.com/xiaoqidun/jbig2` and `github.com/dkrisman/gobig2`
    are both Apache-2.0 and both would be convenient oracles, but both are recent
    and unvalidated and neither is on the permitted dependency list. They must be
    **raised as a separate decision**, not added under this plan. The external-binary
    oracle makes them unnecessary.
11. **`byb-b0`'s `Capabilities()`** (Task 7 Step 3). If it already exists, extend
    it; if not, create it. Do not assume either.
