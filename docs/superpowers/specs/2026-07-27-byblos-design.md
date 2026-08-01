# Byblos — a pure-Go PDF pipeline

**Status:** approved design
**Date:** 2026-07-27
**Repo:** `dobbo-ca/byblos`
**Primary consumer:** `dobbo-ca/kleio`
**Sibling:** `dobbo-ca/cadmus` (OCR engine)

Byblos was the port that traded papyrus to Greece; "biblion", and eventually
"book", comes from its name. The library handles the paper.

---

## 1. Problem

Kleio's document pipeline shells out to a container image bundling `ocrmypdf`,
`tesseract`, `ghostscript`, `jbig2enc`, `pngquant`, `poppler`, `unpaper`, and
`img2pdf`. [Cadmus](https://github.com/dobbo-ca/cadmus) removes `tesseract` and
the OCR half of `ocrmypdf`. Byblos removes the rest except `unpaper`, deferred
(see FUTURE.md).

### Goals

| # | Goal |
|---|------|
| G1 | Eliminate Kleio's PDF-side binary dependencies: `ghostscript`, `jbig2enc`, `pngquant`, `poppler`, `img2pdf` |
| G2 | Keep compression quality competitive with the `ocrmypdf` pipeline it replaces |
| G3 | Make processed documents **upgradeable**: when Byblos gains a capability, identify precisely which stored documents would benefit |

"No binary dependencies" means no executables to invoke and no shared libraries
to link — not "no Go modules". A Go module compiles into the binary and does not
affect static linking, cross-compilation, or `CGO_ENABLED=0`. Byblos therefore
takes pure-Go module dependencies where a mature one exists.

### Non-goals

- **OCR.** Cadmus does that. Byblos never recognizes text; it only places text it
  is handed.
- **General PDF rendering.** See §2 — the reason this project is tractable.
- **Orchestration policy.** The compression preset ladder, born-digital rules,
  validation gate, and retry policy stay in Kleio.
- **PDF/A conversion.** Deferred; see `FUTURE.md`.

---

## 2. The key reframing: extract, do not render

Pure-Go PDF *rasterization* is enormous — a content-stream interpreter, Type1/CFF/
TrueType/CID font rasterization, color spaces, shadings, transparency groups. It
would be larger than Cadmus.

Kleio never needs it:

- **Born-digital PDFs are explicitly never rasterized.** Kleio's own design says
  rasterizing them produces something larger and worse. They are linearized and
  their existing text is kept.
- **Scanned PDFs are, overwhelmingly, one page-covering image per page.** That
  does not require rendering. It requires *extracting* — which `pdfcpu` already
  does.

So `poppler`'s role collapses from "render arbitrary PDF" to "get the embedded
raster off a page".

The residual case — a page with tiled images, a vector overlay, or mixed
content — is *detected*, not rendered. `ExtractPageRaster` returns
`ErrNotSingleRaster` and Kleio diverts the document to `needs_review`, exactly as
it already does for other hard failures.

**This divert rate must be instrumented from day one.** The entire scope of this
project rests on the premise that it is rare. If it turns out common, the premise
is wrong and the design needs revisiting — better to learn that from a counter
than from a user complaint.

---

## 3. Dependencies

| Dependency | Why |
|---|---|
| [`pdfcpu`](https://github.com/pdfcpu/pdfcpu) (Apache-2.0, pure Go) | PDF parsing, writing, image extraction, optimization. Actively developed since 2018, no cgo. Reimplementing a PDF parser to avoid it would be ideology, not engineering. **Not linearization** — see below. |
| `golang.org/x/image` | Resampling filters for downsampling. |

Everything else — JBIG2 encoding, color quantization, the text-layer assembly —
is ours, because nothing suitable and permissively licensed exists in pure Go.

**Linearization is ours too, and was not planned for.** This table originally
credited `pdfcpu` with it. That is wrong, and it was wrong in the direction that
matters: `pdfcpu` v0.13.0 has no linearizer (`model/xreftable.go`:
"Linearization section (not yet supported)"), never emits `/Linearized`, and its
write path *frees* linearization hint tables (`write.go`, `deleteRedundantObject`
via `IsLinearizationObject`). Measured on `pdfcpu`'s own fixtures, a round trip
through its optimize pass takes `bookletTest.pdf` from 50,308 bytes linearized to
34,531 bytes **not** linearized.

That mattered more than a missing option, because §4's `Optimize` is what
replaces Kleio's born-digital treatment — and that treatment is linearization
and nothing else (`kleio/internal/pipeline/compress.go`, `linearizeArgs`:
`ocrmypdf --skip-text --tesseract-timeout 0 --optimize 1`, then return). So G1
could not be met for born-digital documents until Byblos linearized on its
own, which meant implementing ISO 32000-1:2008 Annex F — object ordering, the
linearization parameter dictionary, page-offset and shared-object hint tables,
and a split cross-reference. Byblos now does this itself (`internal/linearize`,
`byb-1y7`): `Optimize` accepts `Linearize: true` and returns Annex F output,
at a measured +649 to +1007 bytes against the same document rewritten without
linearizing.

`Optimize` still records `rewritten-delinearized` on the provenance when its
non-linearizing rewrite drops linearization the input arrived with, so that
loss stays visible rather than silent.

**Byblos wraps `pdfcpu` behind its own interfaces** so that replacing it later is
a swap rather than a rewrite.

**Byblos does not import Cadmus, and Cadmus does not import Byblos.** Byblos
defines its own `TextLayer` input type; Kleio converts `cadmus.Page` →
`byblos.TextLayer`. Kleio is the only component that knows both exist. Each
library stays independently useful and independently testable.

---

## 4. Public API

```go
package byblos

const Version = "0.1.0"                          // recorded on every Provenance
const CapabilityJBIG2Generic = "jbig2-generic"    // provenance capability string

// --- inspection ---

type ImageRef struct {
    Bounds        image.Rectangle // placement on the page, in points
    Placement     [6]float64      // paint matrix, [a b c d e f] (ISO 32000-1 8.3.3)
    Width, Height int             // pixel dimensions
    Bitonal       bool
}

type PageInfo struct {
    Index     int
    Bounds    image.Rectangle // page box, in points
    Images    []ImageRef
    TextChars int             // feeds Kleio's born-digital signal
}

func Inspect(r io.ReadSeeker) ([]PageInfo, error)

// --- extraction (no renderer) ---

// ErrNotSingleRaster reports a page that is not one visible image: tiled
// rasters, visible vector content, or image-plus-overlay.
var ErrNotSingleRaster = errors.New("byblos: page is not a single raster")

// ErrUnsupportedImageCodec reports a page raster stored in a codec Byblos
// cannot decode: JBIG2, JPEG 2000, or CMYK re-rendered as TIFF by pdfcpu.
var ErrUnsupportedImageCodec = errors.New("byblos: page raster uses an image codec byblos cannot decode")

// PageRaster is the raster and where it sits. byb-b1.3 measured 132 pages
// whose raster is placed at its own resolution on a nominal page box and does
// not fill it; those pages extract, so the caller has to be able to tell.
type PageRaster struct {
    Image         image.Image
    Bounds        image.Rectangle // where the raster lands, in points
    Page          image.Rectangle // the page box, in points
    DroppedAnnots int             // annotations that paint and are not in Image
}

func (p PageRaster) CoversPage() bool

func ExtractPageRaster(r io.ReadSeeker, page int) (*PageRaster, error)

// --- extraction telemetry ---
// Instruments the premise §2 rests on: that a page which is not a single
// raster is rare. See ExtractCounters.UnhandledRate.

type ExtractCounters struct {
    Attempted, Extracted, Partial, Diverted, Failed uint64
    Reasons map[string]uint64 // divert reason -> count
}

func ExtractStats() ExtractCounters
func ResetExtractStats()
func (c ExtractCounters) DivertRate() float64
func (c ExtractCounters) UnhandledRate() float64

// --- codecs ---

type Bitmap struct { // 1-bpp bilevel image; see §5
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

func EncodeJBIG2Generic(b *Bitmap) ([]byte, error)   // lossless; see §5

// NOT YET IMPLEMENTED (B3, image codecs). Kept here as the intended shape,
// not as shipped API.
func QuantizePNG(img image.Image, colors int) ([]byte, error)
func Downsample(img image.Image, srcDPI, dstDPI int) image.Image

// --- assembly ---

type PositionedWord struct {
    Text   string
    Bounds image.Rectangle
}

type TextLayer struct {
    Pages [][]PositionedWord
}

// ErrUnstampableRune reports a rune outside the glyphless font's coverage
// (printable ASCII). StampTextLayer errors rather than substituting a glyph.
var ErrUnstampableRune = errors.New("byblos: rune is outside the glyphless font's coverage")

func StampTextLayer(w io.Writer, r io.ReadSeeker, tl TextLayer) error

type ColorSpace = pdfdoc.ColorSpace   // §4 write-seam vocabulary, shared with
type DecodeParms = pdfdoc.DecodeParms // ReplaceImage so BuildPDF isn't a second
type EncodedImage = pdfdoc.EncodedImage // thing to keep in step

type BuildPage struct {
    Image             EncodedImage
    WidthPt, HeightPt float64 // MediaBox in points; both zero derives from DPI
    DPI               float64
}

func BuildPDF(w io.Writer, pages []BuildPage) error

// --- capability errors ---

// ErrNotImplemented reports that a caller asked for something Byblos does
// not do YET, as distinct from a broken or ineligible document. Test with
// errors.Is; use errors.As with *NotImplemented to find which capability.
var ErrNotImplemented = errors.New("byblos: not implemented")

type NotImplemented struct {
    Capability string // e.g. "jpeg-recompress"
    Why        string
    Issue      string // e.g. "byb-b3"
}

func (e *NotImplemented) Error() string
func (e *NotImplemented) Unwrap() error

type OptimizeOptions struct {
    Linearize      bool // implemented: our own Annex F writer, see §3
    RecompressJPEG bool // still refused with *NotImplemented{Capability: "jpeg-recompress"} -- B3
    JPEGQuality    int
}

func Optimize(w io.Writer, r io.ReadSeeker, opts OptimizeOptions) error

// --- provenance (§6) ---

type Provenance struct {
    Version      string
    Capabilities []string
    ProcessedAt  time.Time
    Pages        []PageProvenance

    // Optimized records which branch Optimize took: "" (not known to have
    // been rewritten), "rewritten", "rewritten-delinearized" (rewrite
    // dropped linearization the input had), or "rewritten-linearized" (Annex
    // F output, byb-1y7). See §6.
    Optimized string
}

type PageProvenance struct {
    Applied       []string        // e.g. ["downsample-150", "jbig2-generic"]
    Diverted      string          // e.g. "not-single-raster"; "" when handled normally
    Placement     []float64       // paint matrix recorded at write time; see PageRaster
    DroppedAnnots int             // annotations that painted but are not in the extracted raster
}

func ReadProvenance(r io.ReadSeeker) (*Provenance, error)
func WriteProvenance(r io.ReadSeeker, w io.Writer, p Provenance) error
func UpgradeCandidates(p *Provenance, current []string) []string
func Capabilities() []string // what this build can do
```

---

## 5. JBIG2: lossless generic region only

**Decision: generic region coding (MQ arithmetic coder + template prediction),
lossless. No symbol dictionary.**

JBIG2's headline 100:1 ratios come from *symbol matching*: segment the page into
glyphs, store each distinct glyph once, and reference it thereafter. In **lossy**
mode, near-identical glyphs are unified — and that has a documented history of
silently altering scanned documents. The 2013 Xerox scanner defect, in which
scanned digits were substituted for other digits with no visual artifact, was
exactly this mechanism.

Kleio is a document archive. It may hold financial and medical records. A
compression mode that can silently change a 6 into an 8, leaving an image that
looks perfectly clean, is not an acceptable default — the failure is undetectable
by the very validation gate that is supposed to catch degradation, because the
output looks *better*, not worse.

`ocrmypdf --jbig2-lossy`, which Kleio's current aggressive preset uses, is this
mode. **Byblos will not reproduce it by default.**

Generic region coding is lossless: the decoded bitmap is bit-identical to the
input, so no substitution is possible, ever. It gives roughly 2-4× better
compression than CCITT G4 for around 2-3k LOC of well-specified work.

Lossless symbol-dictionary coding — better ratios, still zero substitution risk,
substantially more complex — is recorded in `FUTURE.md` as the intended next step.

---

## 6. Provenance and upgradeability (G3)

A version number alone cannot answer the question that matters: *would
re-processing this document actually improve it?* A scan of colour photographs
gains nothing from better bitonal compression, and re-processing an entire
archive to discover that defeats Kleio's cost directive.

Byblos therefore records **capabilities and what each page received**:

```go
type Provenance struct {
    Version      string    // byblos semver, for humans and bug reports
    Capabilities []string  // what this build could do at processing time
    ProcessedAt  time.Time
    Pages        []PageProvenance

    // Optimized records which branch Optimize (byb-b5) took: "" (not known
    // to have been rewritten), "rewritten", "rewritten-delinearized", or
    // "rewritten-linearized" (byb-1y7). See §4 and §8.
    Optimized string
}

type PageProvenance struct {
    Applied       []string  // e.g. ["downsample-150", "jbig2-generic"]
    Diverted      string    // e.g. "not-single-raster"; "" when handled normally
    Placement     []float64 // paint matrix recorded at write time (byb-b1.3)
    DroppedAnnots int       // annotations that painted but are not in the extracted raster
}
```

`UpgradeCandidates` returns only the capability gaps that would change *this*
document's output:

| recorded | new capability | upgrade? |
|---|---|---|
| `applied: jbig2-generic` | `jbig2-symbol` | yes — smaller output |
| `diverted: not-single-raster` | `render` | yes — now processable |
| `applied: jpeg-recompress` only | `jbig2-symbol` | no — no bitonal content |

An empty result means re-processing is wasted work.

**Stored twice, deliberately.** The PDF carries the record as JSON under a custom
Info-dictionary key, so the file is self-describing and survives leaving Kleio.
Kleio mirrors `byblos_version` and `byblos_capabilities` into its `documents`
table so the upgrade job is one indexed query rather than an S3 scan. **The PDF is
authoritative; the column is a cache** — a mismatch is resolved by re-reading the
PDF.

`// ponytail: Info-dict JSON; move to XMP if PDF 2.0 conformance ever matters.`

### Shared with Cadmus

Cadmus L3 has the same problem: shipping a fine-tuned model means some documents
should be re-OCR'd. Same shape, same query. Kleio should therefore build **one**
generic reprocess job driven by two provenance columns — `ocr_model_version` and
`byblos_capabilities` — rather than two bespoke ones.

---

## 7. The invisible text layer

Searchable-but-invisible text uses PDF text rendering mode 3. Mode 3 still
requires a font resource, because the viewer must position glyphs even though it
draws nothing.

The standard solution, and `ocrmypdf`'s, is a **glyphless font**: a minimal
TrueType whose glyphs are all empty, with a width table that places text
correctly. Byblos generates this font once and commits it as a small asset.

This is a known-solved problem, but it is not free, and it is the piece most
easily missed when estimating this project.

---

## 8. Testing strategy

As with Cadmus, the tools being replaced serve as **test-only differential
oracles**. They never ship and Kleio never depends on them.

| area | oracle |
|---|---|
| `Inspect`, `ExtractPageRaster` | `pdfinfo`, `pdfimages` output on a fixed PDF corpus |
| `EncodeJBIG2Generic` | round-trip through a JBIG2 *decoder*, asserting bit-identical output — this is the definitive lossless check, and it does not need `jbig2enc` |
| `QuantizePNG`, `Downsample` | size and PSNR bounds against `pngquant`/`ghostscript` output |
| `StampTextLayer` | `pdftotext` on the result must return the text that was stamped, in reading order |
| `Optimize` | output must remain valid per `pdfcpu validate`, and no larger than input — suspended for `Linearize: true`: over the 27 readable corpus documents it runs +7 to +1007 B against the input (usually but not always larger; the rewrite alone can shrink a document more than linearizing costs), and +649 to +1007 B with no exceptions against the same document rewritten and not linearized (see §4/§6 `Provenance`) |

The JBIG2 oracle deserves emphasis: because we encode losslessly, correctness is
verifiable by decoding our own output and comparing bitmaps. No reference encoder
is required, and the test is exact rather than statistical.

Corpus: born-digital PDFs, single-image scans, multi-image/tiled pages,
image-plus-vector-overlay pages, and at least one deliberately malformed file.

---

## 9. Kleio integration

Kleio's `compress` worker replaces `exec.Command` calls with Byblos calls. **Its
preset ladder, born-digital detection, validation gate, and retry policy are
unchanged** — this is a substitution beneath an existing boundary, not a redesign.
Work currently in flight on Kleio Plan 2 is unaffected and is not wasted.

New in Kleio: `byblos_version` and `byblos_capabilities` columns, and the generic
reprocess job described in §6.

---

## 10. Licensing

Apache-2.0, matching `pdfcpu` and Cadmus.

Byblos is **not** a port of OCRmyPDF. OCRmyPDF is MPL-2.0, which is file-level
copyleft: translated files would have to remain MPL-2.0. Byblos reimplements from
format specifications and documented behaviour, which does not create a derivative
work. OCRmyPDF remains a valuable *design reference* for decision logic, and that
distinction must be respected in practice — **do not translate its source**.

Note also that porting OCRmyPDF would not have served G1 anyway: it is
orchestration over the very binaries this project exists to remove, so a Go
translation would still shell out to all of them.

---

## 11. Decisions taken

- **Separate repo** from Cadmus and Kleio. Neither library imports the other.
- **Extract, never render.** Undetectable cases divert to `needs_review`; the
  divert rate is instrumented.
- **`pdfcpu` as a dependency**, wrapped behind our own interfaces.
- **JBIG2 lossless generic region only.** Lossy symbol matching is rejected on
  data-integrity grounds, not deferred for effort.
- **Capability-based provenance**, stored in the PDF and mirrored in Kleio.
- **Apache-2.0**, reimplemented from specifications rather than ported.
