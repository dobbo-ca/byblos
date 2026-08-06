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

Kleio needs it in exactly one place, and it is not compression:

- **Born-digital PDFs are never rasterized *for compression*.** Kleio's own
  design says rasterizing them produces something larger and worse. They are
  linearized and their existing text is kept.
- **Scanned PDFs are, overwhelmingly, one page-covering image per page.** That
  does not require rendering. It requires *extracting* — `pdfcpu` decodes the
  stream (except JBIG2 and JPX, which it cannot), and Byblos supplies the
  content-stream walk that decides which placement is the page.
  Measured (`byb-divert`, 2026-07-28): genuinely composite pages are **1.03% of
  29,779 scan-shaped pages**, ~1.5% counting blank-base MRC the geometric test
  cannot see. Do not confuse this with Byblos's own divert rate, which is
  measured on a *different sample*: 85.73% over all 156,872 pages of a
  four-corpus mix that is 78.8% `govdocs1` — which `byb-divert` records is not a
  scanned corpus at all (3.7% scan-shaped, Acrobat Distiller end to end). That
  rate is dominated by pages holding no image at all (`no-image`, 105,049 of
  134,483 diverts); excluding `govdocs1` it is 43.71%. It does not measure this
  premise.
- **The one exception is the thumbnail.** (File:line references in this bullet
  are `kleio/internal/pipeline/` at `e63ffa2`.) `validate.go:338` calls `Thumbnail`
  inside `finalize` with no born-digital guard, so every finalized document has
  page 1 rasterized to a 400px long edge by `pdftoppm`. Nothing else in Kleio
  rasterizes a born-digital page: `Decide` returns `VerdictPass` for born-digital
  before any other test (`gate.go:601-602`), so the 150 dpi gate render
  (`validate.go:295`, guarded by `if !j.BornDigital` at `:219`) and the 1024px
  judge render (`validate.go:243`, reached only on `VerdictAmbiguous`) never see
  one. Both of those rasterize only scanned pages — the population
  `ExtractPageRaster` targets, though it still diverts the JBIG2 and JPX rasters
  it cannot decode (`byb-riy`), which no renderer would fix.

So two of `poppler`'s four roles in Kleio collapse to "list embedded image
geometry to detect born-digital" (`pdfimages -list`, its only invocation —
`tools.go:341`, inside `HasFullPageImage`) and "read page count and page
geometry" (`pdfinfo`). The other two — `pdftotext` word extraction and
`pdftoppm` thumbnail rendering — are open scope decisions; see `byb-lez` and
`byb-0gm`.

### Amendment 2026-08-03: Byblos renders (byb-0gm), at thumbnail fidelity

`byb-0gm` decided option (c), **Byblos renders**, against this section's argument
and against that bead's own recommendation. This section previously asserted
"Kleio never needs it", which is false; the text above is the correction.

The scope is **one consumer**: the 400px page-1 thumbnail above. That is narrower
than `byb-0gm` records — its notes name the gate render as a second consumer, but
the gate render never runs on a born-digital document, so it requires no
renderer. `byb-0gm` also cites `gate.go:248` for it, which is a comment; the call
is `validate.go:295`.

**The fidelity target is thumbnail fidelity**, decided 2026-08-04 and recorded on
`byb-0gm`. The two candidates were different projects, so the choice was made
explicitly rather than inherited from a spec edit:

| target | what it must render | status |
|---|---|---|
| **Thumbnail fidelity** | page 1 only, 400px long edge; recognisable rather than faithful — no colour management, no transparency groups | **Chosen.** It is the only thing Kleio calls today, and it is bounded by a consumer that already exists. |
| **Archival fidelity** | any page at any DPI, faithful enough to store | Not chosen, and not a later phase of `byb-0gm`. Only this justifies the "larger than Cadmus" cost argument above. Nothing in Kleio asks for it. |

The "larger than Cadmus" argument applies to the second row, not the first, so it
is **not** an objection to the renderer `byb-0gm` will build — and equally, that
renderer is not a commitment to an archival one. The §1 non-goal and `FUTURE.md`'s
"A PDF renderer" entry both stand as written: each describes the archival
renderer, which remains deferred on the measured evidence above.

The residual case — a page with tiled images, a vector overlay, or mixed
content — is *detected*, not rendered. `ExtractPageRaster` returns
`ErrNotSingleRaster` and Kleio diverts the document to `needs_review`, exactly as
it already does for other hard failures.

**This divert rate must be instrumented from day one.** The entire scope of this
project rests on the premise that it is rare. If it turns out common, the premise
is wrong and the design needs revisiting — better to learn that from a counter
than from a user complaint.

Note what the counter does *not* measure. `DivertRate` counts born-digital and
unreadable pages too, so it cannot answer the premise on its own — `byb-divert`
had to select a scan-shaped population separately to do that, and `byb-5kk`
showed the failure mode directly: a page-tree bug made 1,266 real pages
unreadable and pushed the measured divert rate *down*. Watch `UnhandledRate` for
regressions (`stats.go:50-57`), and measure the premise itself over scan-shaped
pages.

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
defines its own `TextLayer` input type (§4). The fan-out this paragraph
describes — Kleio as the only component that knows both libraries exist,
each staying independently useful and independently testable — is the right
shape and stays. But the conversion this paragraph used to name,
`cadmus.Page` → `byblos.TextLayer`, does not exist anywhere, on either side.

`cadmus.Page` is not a compilable type: `go list ./...` in Cadmus exports no
library package at all (one `cmd` and six `internal` packages), and `type
Page` appears nowhere in Cadmus's Go source — only in Cadmus's own design
doc. `byblos.TextLayer` is real (`stamp.go`), but Kleio does not produce one:
Kleio's `go.mod` now requires `github.com/dobbo-ca/byblos v0.1.0`, but the
only calls it makes are `byblos.Inspect` for page metadata
(`internal/pipeline/inspect.go`); nothing in Kleio calls `StampTextLayer` or
constructs a `TextLayer`. Kleio still adds its text layer by shelling out to
`ocrmypdf` (`internal/pipeline/ocr.go:104-105`), and its own OCR-confidence path
reads tesseract TSV directly (`TesseractTSV`, `internal/pipeline/tools.go`)
without turning it into a `byblos.TextLayer` either. So `TextLayer` and
`PositionedWord` currently have no producer anywhere outside Byblos's own
tests — the same zero-callers shape as G1's audit (byb-js5). Whether the real
path is Kleio parsing tesseract TSV directly into a `TextLayer`, or something
else, is a Kleio-side decision this document does not make on Kleio's
behalf.

---

## 4. Public API

```go
package byblos

const Version = "0.2.0"                          // recorded on every Provenance
const CapabilityJBIG2Generic = "jbig2-generic"    // provenance capability string

// --- inspection ---

type ImageRef struct {
    Bounds        image.Rectangle // placement on the page, in points
    Placement     [6]float64      // paint matrix, [a b c d e f] (ISO 32000-1 8.3.3)
    Width, Height int             // pixel dimensions
    Bitonal       bool
    Filter        string          // declared codec, e.g. "JBIG2Decode"; "" when none
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

// Sauvola (byb-jj5) binarizes by local adaptive thresholding, producing the
// Bitmap EncodeJBIG2Generic takes. Adaptive because a scan with a shadowed
// gutter has no single global cutoff that works across the page.
func Sauvola(img image.Image) (*Bitmap, error)

// ErrUnsupportedJBIG2Feature reports a JBIG2 stream that parsed correctly and
// uses a coding feature byblos does not implement: symbol dictionaries and text
// regions, refinement, halftones, MMR, or a generic region coded with anything
// other than GBTEMPLATE 0 and the nominal AT pixels. Distinct from a decode
// failure: this one says the bytes are fine and byblos is not enough.
var ErrUnsupportedJBIG2Feature = errors.New("byblos: JBIG2 stream uses a feature byblos does not decode")

// DecodeJBIG2Generic inverts EncodeJBIG2Generic UP TO A SIZE and nothing wider
// (byb-riy): immediate generic regions only, and only pages inside the decoder's
// resource budget -- 67,108,864 pixels packing into 16 MiB, which covers every
// 600-dpi master and 800-dpi A4 but not 600-dpi A3. The encoder has no size
// budget and never did, so byblos can write a page it will not read back; the
// asymmetry is documented on DecodeJBIG2Generic and pinned by
// TestEncodeDecodeSizeBoundary. Below that size it is what makes a
// byblos-compressed page re-openable by byblos, and it is the decoder
// ExtractPageRaster runs on an inbound /JBIG2Decode image. See §5.
func DecodeJBIG2Generic(data []byte) (*Bitmap, error)

func QuantizePNG(img image.Image, colors int) ([]byte, error)
func Downsample(img image.Image, srcDPI, dstDPI float64) (image.Image, error)
func DownsampleDeclaredBPC(img image.Image, declaredBPC int, srcDPI, dstDPI float64) (image.Image, error)

// QuantizeIndexed (byb-96p) is QuantizePNG's embeddable variant: same core,
// but it returns a PDF /Indexed /FlateDecode image rather than a PNG file.
// NOTE: BuildPDF does not accept it — internal/pdfbuild rejects the Indexed
// colour space — so it has no exported route into a PDF today. See byb-5jy and byb-fp6.
func QuantizeIndexed(img image.Image, colors int) (EncodedImage, error)

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

// ReplaceImages (byb-fp6) substitutes image streams in an EXISTING document,
// keyed by 1-based page, leaving everything else as it was. It refuses an image
// carrying /SMask, /Mask or /ImageMask, and writes no provenance: the caller
// records what it applied, through RecordExtraction and WriteProvenance.
func ReplaceImages(w io.Writer, r io.ReadSeeker, subs map[int]EncodedImage) error

// --- capability errors ---

// ErrNotImplemented reports that a caller asked for something Byblos does
// not do YET, as distinct from a broken or ineligible document. Test with
// errors.Is; use errors.As with *NotImplemented to find which capability.
var ErrNotImplemented = errors.New("byblos: not implemented")

type NotImplemented struct {
    Capability string // e.g. "linearize"
    Why        string // one sentence, in terms of what is missing, not what to do
    Issue      string // e.g. "byb-k48"
}

func (e *NotImplemented) Error() string
func (e *NotImplemented) Unwrap() error

type OptimizeOptions struct {
    Linearize      bool // implemented: our own Annex F writer, see §3
    RecompressJPEG bool // implemented: re-encodes eligible DCTDecode images at JPEGQuality (1..100), byb-b3
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
    Geometry      *PageGeometry   // raster/page boxes measured at write time (byb-b5.1)
}

type PageGeometry struct {
    RasterBox [4]float64 // [llx lly urx ury], PDF default user space, points
    PageBox   [4]float64 // same order; NOT Placement's [a b c d e f] matrix order
}

func (g PageGeometry) CoversPage() bool

func ReadProvenance(r io.ReadSeeker) (*Provenance, error)
func WriteProvenance(r io.ReadSeeker, w io.Writer, p Provenance) error
func RecordExtraction(r io.ReadSeeker) (Provenance, error) // runs extraction over every page, ready for WriteProvenance (byb-b5)
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
    Applied       []string      // e.g. ["downsample-150", "jbig2-generic"]
    Diverted      string        // e.g. "not-single-raster"; "" when handled normally
    Placement     []float64     // paint matrix recorded at write time (byb-b1.3)
    DroppedAnnots int           // annotations that painted but are not in the extracted raster
    Geometry      *PageGeometry // raster/page boxes measured at write time (byb-b5.1)
}
```

`UpgradeCandidates` returns only the capability gaps that would change *this*
document's output:

| recorded | new capability | upgrade? |
|---|---|---|
| `applied: jbig2-generic` | `jbig2-symbol` | yes — smaller output |
| `diverted: not-single-raster` | `render` | yes — now processable |
| `dropped_annots > 0` (any page) | `render` | yes — appearance streams would render differently (byb-b5.1) |
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
| `Optimize` | output must remain valid per `pdfcpu validate`, and no larger than input — suspended for `Linearize: true`: over the 34 readable corpus documents it runs +7 to +1007 B against the input, every one of them larger (the +7 is `dup-raster`, the one document pdfcpu's rewrite shrinks enough to nearly pay for the linearization), and +649 to +2071 B with no exceptions against the same document rewritten and not linearized (the +2071 is `booklet`, the eight-page document: linearization's per-page hint columns are the one cost that scales with page count — see §4/§6 `Provenance`) |

The JBIG2 oracle deserves emphasis: because we encode losslessly, correctness is
verifiable by decoding our own output and comparing bitmaps. No reference encoder
is required, and the test is exact rather than statistical.

Corpus: born-digital PDFs, single-image scans, multi-image/tiled pages,
image-plus-vector-overlay pages, and at least one deliberately malformed file.

---

## 9. Kleio integration

Kleio's `compress` worker will replace its `exec.Command` calls with Byblos
calls. **Its preset ladder, born-digital detection, validation gate, and retry
policy are unchanged** — this is a substitution beneath an existing boundary, not
a redesign. Work currently in flight on Kleio Plan 2 is unaffected.

New in Kleio: `byblos_version` and `byblos_capabilities` columns, and the generic
reprocess job described in §6.

**Not started, but no longer unimported.** Kleio's `go.mod` now requires
`github.com/dobbo-ca/byblos v0.1.0` (`go.mod:16`, recorded in `go.sum:76-77`), and
`internal/pipeline/inspect.go` imports it and calls `byblos.Inspect` from
`pageCountViaByblos`. That call is reached only when `PageCount` runs with
`KLEIO_BYBLOS_INSPECT=1` set; the flag defaults to unset, so in production
`PageCount` still takes the `pdfinfo` path exactly as it did before Byblos
existed, and the `compress` worker itself makes no Byblos call at all. The one
place the flag is ever set is a test, `TestPageCountUnderTheByblosFlagNeedsNoPoppler`
(`internal/pipeline/inspect_spike_test.go`), which does call `byblos.Inspect`
through Kleio's real `PageCount` function against a real (ghostscript-generated)
PDF — so Byblos code has executed once, inside Kleio's test suite, but never
inside Kleio's production pipeline, and no Byblos function other than `Inspect`
has been called from Kleio at all. This is still the largest gap between this
document and the world, and it is what makes every parity claim here
theoretical. Everything above is the intended design, not a description of
running code.

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
