# byb-0gm implementation plan: a page-1 thumbnail capability

Design phase, 2026-08-04. Written on branch `thumbnail-design-8d42`. No Go code
changed by this document; every figure in it was measured on this branch and the
commands are recorded so they can be re-run.

---

## 0. READ THIS FIRST: byblos cannot render, and the thumbnail scope does not make that smaller

**Byblos has no rasterizer, and nothing in its dependency set has one either.**
That is not a gap to be filled by the narrow scope `byb-0gm` decided; it is the
whole of the work, and the "thumbnail fidelity, not archival fidelity" carve-out
in design spec §2's 2026-08-03 amendment removes almost none of it.

### 0.1 The finding, with the evidence

**Byblos only EXTRACTS.** `ExtractPageRaster` (`extract.go:209`) opens the
document, classifies the content stream, and then pulls the bytes of an
*already-embedded image XObject* out of it — `d.RawImage(placement.ID)` at
`extract.go:247`, `image.Decode` at `extract.go:276`. Nothing between those two
lines interprets a painting operator. Its own doc comment says so:

> Vector paint is judged the same way: by what the content stream proves, not by
> rendering it. […] Nothing is filled, scan-converted or composited to decide
> this — only painting order and a bounding box. (`extract.go:187-192`)

`internal/content` is a classifier, not a renderer, and deliberately so. Its path
model is a bounding box: `addPoints`/`addRect` extend a `pathBox`
(`internal/content/walk.go:258,470,478`) and a Bézier is reduced to the bounding
box of its control points. It discards the fill rule on purpose — "the fill rule
they name only matters for a self-intersecting path's interior, which a bounding
box can never see" (`walk.go:365-367`). It has *no* text machinery at all: `Tf`,
`Tm`, `Td`, `TL`, `Tc`, `Tz` are not handled, and `Tj`/`TJ` only increment
`s.TextChars` by `len(operand)` (`walk.go:389-405`). It resolves no colour space:
`cs`/`CS` store the resource *name* and `sc`/`scn` store raw components
(`walk.go:352-359`).

**No dependency supplies one.** `go.mod` has exactly two direct requires:
`pdfcpu v0.13.0` and `golang.org/x/image v0.41.0`.

- pdfcpu does not rasterize. `pkg/pdfcpu/draw/draw.go` *writes content-stream
  operators to an `io.Writer`* (`DrawRect(w io.Writer, …)`, `FillRect(w
  io.Writer, …)`) — it emits PDF, it does not produce pixels. `pkg/api`'s image
  functions (`Images`, `ExtractImages`, `ExtractImagesRaw`) extract embedded
  XObjects, which is what byblos already uses. `model.AnnotationRenderer` is an
  interface for writing annotation dictionaries, not for painting them.
- `golang.org/x/image` has the pieces to *build* one (`vector`, `font/sfnt`,
  `font/opentype`) but byblos imports neither, and **cannot import them without
  amending an architecture test** — see §0.4.
- `internal/glyphless` is not a font byblos could draw with. Its own package
  comment: "a minimal TrueType font whose glyphs carry **no outlines**: every
  character prints nothing" (`internal/glyphless/glyphless.go:1-3`). It exists so
  `StampTextLayer` can write an invisible OCR layer.

### 0.2 A born-digital page has nothing to extract — measured

Run over the whole generated corpus (35 documents, `cmd/byblos-corpus` output),
calling `ExtractPageRaster(f, 1)` on each:

```
born-digital.pdf   ERR  byblos: page is not a single raster: no-image
```

and `Inspect` on the same document reports `Images:[] TextChars:44`. That is
`classify`'s first arm, `extract.go:381-382`, and `classify`'s own comment
already names the case: "A born-digital page has both no image and text;
'no-image' says more" (`extract.go:374-375`).

So for the exact population `byb-0gm` scoped — born-digital page 1 — the current
code path returns an error by construction. There is no partial capability to
build on.

### 0.3 How big that population is — measured on the pinned sample

`~/work/dobbo-ca/.byblos-sample`, all 5,672 documents in `manifest.tsv`,
**page 1 only**, this branch, 2026-08-04:

| page-1 outcome | docs | share |
|---|---:|---:|
| `ExtractPageRaster` succeeds (raster path works today) | 556 | 9.8% |
| diverted, page 1 has **no images and ≥1 text char** | 3,146 | 55.5% |
| diverted, page 1 has **images and text**, or multiple images | 1,946 | 34.3% |
| diverted, page 1 blank (no images, no text) | 6 | 0.1% |
| read failure | 18 | 0.3% |

Per corpus leg, `ExtractPageRaster` on page 1:

| leg | docs | page 1 extracts |
|---|---:|---:|
| `ia` (the scan-shaped leg) | 299 | **299 (100%)** |
| `dc` | 520 | 87 (16.7%) |
| `govdocs1` (Distiller-produced, born-digital) | 4,840 | 167 (3.5%) |
| `anchors` | 13 | 3 |

Two things follow, and they point in opposite directions:

1. **The extraction path already serves every scanned document's page 1** in this
   sample — 299 of 299 on the `ia` leg. A thumbnail built on extraction alone is
   not worthless; it is complete for scans.
2. **89.8% of documents need a renderer for page 1**, and 52.6% of all documents
   (2,983) have a page 1 with zero images and ≥200 characters of text. For those,
   a "thumbnail" is a picture of text and nothing else. A renderer that draws
   paths and images but not glyphs returns a blank white rectangle for them.

### 0.4 The font stack is the cost, and thumbnail fidelity does not shrink it

Design spec §2 names the cost as "a content-stream interpreter, Type1/CFF/
TrueType/CID font rasterization, color spaces, shadings, transparency groups"
and the amendment carves away colour management, transparency groups and
shadings. It does not carve away fonts, and fonts are the bulk.

Measured with `pdffonts -f 1 -l 1` (poppler, used as an oracle, not a runtime
dependency) over 840 sampled documents whose page 1 diverts and carries text —
3,161 page-1 font uses:

| font program | uses | share | can `x/image/font/sfnt` parse it? |
|---|---:|---:|---|
| TrueType, **not embedded** | 913 | 28.9% | no font program exists in the file |
| Type 1C (bare CFF), embedded | 686 | 21.7% | no — `sfnt` decodes TTF/OTF *containers* |
| TrueType, embedded | 654 | 20.7% | yes |
| Type 1 (PFB), **not embedded** | 516 | 16.3% | no program in the file |
| Type 1 (PFB), embedded | 147 | 4.7% | no — needs eexec + Type 1 charstrings |
| CID TrueType, embedded | 133 | 4.2% | yes, plus an Identity-H CMap |
| everything else | 112 | 3.5% | mixed |

- **47.8% of page-1 font uses are not embedded.** Rendering those requires byblos
  to *ship substitute outline fonts* — the standard-14 metrics plus metrically
  compatible programs, plus a fallback for arbitrary named fonts. That is a
  licensing and binary-size decision, not a coding task, and nobody has taken it.
- **44.2% of uses are Type 1 or bare CFF**, neither of which `x/image/font/sfnt`
  parses (`go doc golang.org/x/image/font/sfnt`: "a decoder for TTF […] and OTF
  […] Such fonts are also known as SFNT fonts"). Each needs its own charstring
  interpreter.
- Only **268 of the 840 sampled documents (31.9%)** have a page 1 whose fonts are
  *all* embedded and *all* SFNT-parsable — the best case for a `sfnt`-only build.

Two further gaps in the same direction:

- `golang.org/x/image/vector` does not implement PDF's fill rules. `vector.go:324`
  and `:360` both carry the comment `// TODO: non-zero vs even-odd winding?`. PDF
  needs `f` (non-zero) and `f*` (even-odd) to be exactly right.
- **Adding either import is gated.** `imagecodecs_arch_test.go:59-62` is an
  *allowlist* of `golang.org/x/image` subpackages containing exactly `ccitt` and
  `draw`, and its failure message demands the new import be justified in writing
  and added to `ci.yml` too. (Checked: neither `x/image/vector` nor
  `x/image/font/sfnt` calls `image.RegisterFormat`, so the justification is
  available — but it has to be written.) Separately `arch_test.go:12` confines
  every `pdfcpu` import to `internal/pdfdoc`, so a renderer's font- and
  resource-dictionary access has to be routed through `pdfdoc`, not reached for
  directly.

### 0.5 What this changes about the shape of the work

`byb-0gm`'s decision — option (c), byblos renders — was taken believing that
"one consumer, page 1, 400px, born-digital only, presentation-not-archival is a
materially smaller project". The first three clauses are true and do shrink it.
**The fourth does not**, because the fidelity a thumbnail can drop (colour
management, transparency, shadings) is not where the work is, and the fidelity it
cannot drop (glyphs on the page) is.

The recommendation in §1 is built around that, not against it: it delivers the
thumbnail *API* and the extraction-backed path now — which is worth having, since
it is complete for scans — and makes the renderer a sequence of independently
shippable stages behind an honest `*NotImplemented`, each of which moves a named
share of documents from "byblos declines" to "byblos renders". It does not
pretend the renderer is a fortnight of work.

**What should be reopened with Chris before stage 4 starts** (not before stage 1,
which is worth doing either way):

- The non-embedded-font decision (§0.4, 47.8% of uses). Without it, stages 4c-4e
  still leave roughly half the text on the page invisible, so the renderer never
  reaches the point where `pdftoppm` can be removed. **This should be decided
  before any font code is written, because if the answer is "we will not ship
  substitute fonts" then stages 4c-4e have no completion state and should not be
  started.**
- Whether "recognisable rather than faithful" was meant to license a *greeked*
  thumbnail — text drawn as grey bars at its measured advance width rather than as
  glyphs. At 400px on a 792pt page the scale is 0.505 px/pt ≈ 36 dpi, so 12pt body
  text is ~6px tall and is already illegible mush in the incumbent's output. A
  greeked thumbnail needs `/Widths` and metrics but **no outlines at all**, which
  deletes §0.4 entirely and is perhaps 10% of the cost. It is a materially
  different product and is not this plan's to choose.

---

## 1. Recommendation

**Ship `Thumbnail` in stage 1 as an API with an extraction-backed path and an
explicit `*NotImplemented{Capability: "render"}` for every page that needs a
rasterizer. Do not start the renderer until the non-embedded-font question in
§0.5 is answered.**

Why this and not the alternatives:

- *Build the renderer first, ship the API when it works.* Loses because "when it
  works" is not a date anyone can name, and because the extraction path is
  already complete for 100% of the `ia` leg and would sit unshipped behind it.
- *Ship `Thumbnail` returning a blank page when it cannot render.* Loses badly.
  Kleio would write blank thumbnails into the document list and nothing would
  fail. `notimplemented.go:16-18` already names this exact distinction and says
  the correct caller response to "byblos cannot do this at all" is to "fall back
  to the old tool, for EVERY document" — which kleio can only do if byblos says
  so in the error.
- *Leave `pdftoppm` alone and close `byb-0gm` as option (a).* This is the cheapest
  answer and §0 is an argument for it, but it is a decision that was already taken
  the other way and is not this lane's to retake. Stage 1 is compatible with it:
  if the renderer is never built, stage 1 still removes `pdftoppm` for scans and
  the `*NotImplemented` is a permanent, honest statement rather than a placeholder.

---

## 2. The exported API

```go
// ThumbnailOptions carries the knobs. It is a struct with one field on purpose:
// byblos is tagged and kleio pins it, so the rule is ADD NEVER CHANGE, and a
// bare int parameter would force a second exported function the first time a
// second knob (a page number, a fidelity level) is wanted.
type ThumbnailOptions struct {
    LongEdgePx int
}

func Thumbnail(ctx context.Context, r io.ReadSeeker, opts ThumbnailOptions) (image.Image, error)
```

Purely additive: no existing signature, field or sentinel changes. `ErrNotSingleRaster`,
`ErrUnsupportedImageCodec`, `ErrNotImplemented`, `NotImplemented` and the `"render"`
capability string all already exist and are reused as-is.

### 2.1 Why `ctx` is a plain first parameter and there is no `ThumbnailContext`

`byb-xyn`'s note records that the `XxxContext(ctx, …)` naming is **forced** for the
nine existing document-scale entry points, because they shipped in v0.1.0 without a
context and kleio pins that tag. `Thumbnail` has never shipped. There is nothing to
preserve, so it takes `ctx` directly and there is deliberately **no** non-context
twin to keep in step. This asymmetry is intentional and must be stated in the doc
comment, or a later reader will "fix" it by adding a `ThumbnailContext` that means
the same thing.

### 2.2 Dependency on byb-xyn — hard, and in three places

1. **Semantics.** `byb-xyn` is deciding what cancellation *means* partway through,
   and its acceptance criterion is a measured worst-case elapsed-until-return per
   entry point. `Thumbnail` inherits that contract; it must not invent a second one.
2. **Plumbing.** `Thumbnail` calls `extractPage` (`extract.go:226`) internally. If
   `byb-xyn` threads `ctx` into that layer, `Thumbnail` must pass its own `ctx`
   through rather than `context.Background()`, or it silently opts out of the very
   guarantee it was designed around.
3. **Merge order.** `byb-xyn` adds nine `XxxContext` declarations to design spec §4.
   This lane adds two. Both branches edit the same fenced block, so they *will*
   conflict, and `TestDesignSpecPublicAPIBlockMatchesThePackage` fails on whichever
   merges second until it rebases. **Land `byb-xyn` first.**

### 2.3 What `Thumbnail` returns and why

`image.Image`, not PNG bytes. Every other image-producing entry point in the package
returns an `image.Image` (`ExtractPageRaster` via `PageRaster.Image`, `Downsample`,
`DownsampleDeclaredBPC`) or a `*Bitmap` (`Sauvola`). Returning encoded PNG here would
put encoding policy — bit depth, palette, compression — inside a function whose job is
geometry, and `QuantizePNG` already owns that policy for callers who want it. Kleio's
call site writes a file and can `png.Encode` in one line.

---

## 3. Design spec §4 block, verbatim and paste-ready

**Do not paste this into the spec in this lane.** Verified by experiment on this
branch: adding `func Thumbnail(…)` to §4 with no corresponding Go declaration makes
`TestDesignSpecPublicAPIBlockMatchesThePackage` fail immediately with

```
designspec_pin_test.go:230: the design spec's section 4 block declares func Thumbnail,
which the package does not export
```

The pin is bidirectional (`designspec_pin_test.go:222-239`), so the block below and
the Go code must land in the **same commit**. It goes after the `func Inspect(…)` line
and before the `// --- extraction (no renderer) ---` header — and that header's text
must change too, since the section stops being renderer-free the day stage 4a lands
(leave it alone until then).

```go
// --- thumbnails (byb-0gm) ---

// ThumbnailOptions carries Thumbnail's knobs. LongEdgePx bounds the LONG edge of
// the output, whichever axis that is; 0 means 400, the value kleio's document
// list uses (tools.go:472). It is a struct rather than a bare int because byblos
// is tagged and the rule is add-never-change.
type ThumbnailOptions struct {
    LongEdgePx int
}

// Thumbnail renders page 1 of r to a preview bounded to opts.LongEdgePx on its
// long edge. It applies the page's /Rotate, because a thumbnail is a display
// artifact -- unlike ExtractPageRaster, which deliberately does not.
//
// ctx is a plain first parameter and there is no non-context twin: unlike the
// entry points byb-xyn had to give XxxContext names, Thumbnail never shipped
// without one, so there is no v0.1.0 signature to preserve.
//
// It returns *NotImplemented with Capability "render" for a page that is not a
// single embedded raster -- a born-digital page above all. Byblos has no
// rasterizer; see §2's 2026-08-03 amendment and byb-0gm. That is a property of
// the build, not of the document, so a caller should fall back to its old tool
// for every such document rather than quarantining them (see ErrNotImplemented).
func Thumbnail(ctx context.Context, r io.ReadSeeker, opts ThumbnailOptions) (image.Image, error)
```

The pin compares declaration **names and kinds only** — it parses the block with
`go/parser` and diffs the exported identifier sets (`designspec_pin_test.go:89-133`).
A signature that drifts from the code is *not* caught. Keep them in step by hand.

---

## 4. "Born-digital only" as a runtime predicate: there isn't one, and there must not be

**There is no born-digital classifier in byblos, and `Thumbnail` must not gain one.**

- Nothing in the package computes such a predicate. The closest fact is
  `PageInfo.TextChars`, and `inspect.go:56-58` states its limits outright: "It is a
  born-digital signal, not a text extractor: it counts stored code units, not Unicode
  code points, and it does not decode fonts."
- The boolean lives in kleio as `j.BornDigital` (`validate.go:219`), derived from
  `pdfimages -list` via `HasFullPageImage` (`tools.go:341`). Byblos feeds that
  decision; it does not make it.
- **The only call site has no born-digital guard.** `kleio/internal/pipeline/validate.go:338`
  calls `Thumbnail` inside `finalize` for every document. A byblos `Thumbnail` that
  rejected non-born-digital input would be unusable at the one place it is wanted.

"Born-digital only" in `byb-0gm` scopes *which pages force a renderer*. It is not a
restriction on the API's domain, and reading it as one inverts the capability: the
scanned document is the case byblos can serve **today**, and the born-digital one is
the case it cannot.

So the runtime predicate is not "is this born-digital" but "**can this page be served
from an embedded raster**", and that predicate already exists: `classify`
(`extract.go:379-529`). Dispatch:

| page 1 is… | `Thumbnail` does |
|---|---|
| a single decodable embedded raster | rotate, scale, return the image. This is 100% of the `ia` leg. |
| anything `classify` diverts (`no-image`, `has-text`, `vector-paint`, `multiple-images`, `mrc-layers`, …) | `&NotImplemented{Capability: "render", Why: …, Issue: "byb-0gm"}` |
| a raster in a codec byblos cannot decode (`jbig2`, `jpx`) | the same `*NotImplemented`, but `Capability: "decode-jbig2"` / `"decode-jpx"` — the page *is* a single raster and a renderer would not fix it (`byb-riy`) |
| unreadable (`pdfdoc.Open` fails) | the underlying error, unwrapped. "This document is broken" is a third thing. |

### 4.1 Which sentinel, exactly, and which not

Return **only** the `*NotImplemented`. Do **not** also wrap `ErrNotSingleRaster`.

`notimplemented.go:16-18` draws the line and it is the right one here:

```
this document is broken        -> park it, review it
this document is not eligible  -> divert it, record why (ErrNotSingleRaster)
Byblos cannot do this at all   -> fall back to the old tool, for EVERY document
```

A born-digital page is not "ineligible for a thumbnail" — every document deserves
one, and `pdftoppm` produces one. Byblos declining is a property of the build. If
`Thumbnail` leaked `ErrNotSingleRaster`, a caller matching on it would quarantine
89.8% of documents as bad input.

`"render"`, `"decode-jbig2"` and `"decode-jpx"` are already keys in `capabilityRules`
(`upgrade.go:103,131-133`), which is exactly the vocabulary `NotImplemented.Capability`
is documented to draw from (`notimplemented.go:33-40`), so a caller can hand the string
straight to `UpgradeCandidates` later. **No new capability string is needed, and
`"render"` must NOT be added to `buildCapabilities` until the renderer exists** —
`TestEveryCapabilityHasARule` (`upgrade_test.go:247`) checks that direction only, so
adding it early would pass the test and lie in every provenance record byblos writes.

### 4.2 Telemetry

`Thumbnail` routes through `extractPage`, so `countAttempt`/`countDivert` fire and the
existing `ExtractCounters` already instrument it — a thumbnail of a born-digital page
lands in `Reasons["no-image"]`. **Do not add a second counter family.** If it later
matters to split thumbnail calls from extraction calls, that is its own bead.

---

## 5. The 400px contract, pinned against the incumbent

Every number below was measured on this machine with poppler 25.x, against
hand-built single-page PDFs, using the exact incumbent invocation
(`pdftoppm -png -singlefile -f 1 -l 1 -scale-to 400`, `kleio/internal/pipeline/tools.go:460-464`).

| MediaBox (pt) | /Rotate | pdftoppm output (px) |
|---|---:|---|
| 612 x 792 (portrait) | 0 | 310 x 400 |
| 792 x 612 (landscape) | 0 | 400 x 310 |
| 500 x 500 (square) | 0 | 400 x 400 |
| 200 x 1000 (tall) | 0 | 80 x 400 |
| 601 x 1000 | 0 | **241** x 400 |
| 612 x 792 | 90 | **400 x 310** |
| 612 x 792 | 180 | 310 x 400 |
| 612 x 792 | 270 | **400 x 310** |
| 100 x 150 (smaller than the target) | 0 | 267 x 400 (**upsampled**) |
| 612 x 792, CropBox [0 0 306 792] | 0 | 310 x 400 (**CropBox ignored**) |

### 5.1 The rules that follow

1. **The long edge, whichever axis it is.** `scale = float64(LongEdgePx) / max(wPt, hPt)`.
   There is no portrait/landscape branch and there must not be one: `max` covers
   portrait, landscape and square identically, and a square page (500x500 → 400x400)
   needs no tie-break because both edges *are* the long edge.
2. **Ceil, not round.** 601 x 1000 → 241 x 400: `601 * 0.4 = 240.4`, and poppler
   emits 241. 612 x 792 → 310 confirms it independently (`612 * 400/792 = 309.09`;
   round gives 309). 200 x 1000 → 80 confirms an exact value is not inflated.
   So `outShort = ceil(shortPt * scale)`, and the long edge lands on `LongEdgePx`
   exactly. **This is the single likeliest thing to ship wrong**, because `round` is
   what `DownsampleDeclaredBPC` uses internally (`downsample.go:110-111`) and looks
   right until someone compares against poppler.
3. **/Rotate is applied.** 90 and 270 both give 400 x 310 from a portrait MediaBox;
   180 gives 310 x 400. `ExtractPageRaster` explicitly does *not* apply it — "a
   page's /Rotate is a display attribute and does not affect content space […]
   applying /Rotate is the caller's business" (`extract.go:174-176`) — so `Thumbnail`
   is that caller and must do it itself. Two steps, both required:
   - swap `wPt`/`hPt` **before** computing the scale, for 90 and 270;
   - rotate the pixels. Getting the first without the second yields a correctly-sized
     image containing a sideways page, which §7's T1 cannot catch. That is what T3 is for.
   There is **no existing rotate helper in the package** (grepped: the only `rotate`
   identifiers are `internal/corpus`'s fixture builders at `corpus.go:347,354`), so one
   must be added — as an unexported helper in the thumbnail file, ~30 lines.
4. **Byblos does not upsample. This is a deliberate divergence from poppler.**
   `downsample.go:18-19` already states the policy: "upsampling a scan invents detail
   byblos will not produce." The contract is therefore a **bound**:
   `max(outW, outH) <= LongEdgePx`, with equality except when the source raster has
   fewer pixels than the target, in which case the source is returned at its own size.
   Note the trigger is the *raster's pixel dimensions*, not the page's points: a
   2550 x 3300 scan on a 612 x 792 page downsamples to 310 x 400 normally.
5. **CropBox, not MediaBox. Also a deliberate divergence.** Measured, `pdftoppm`
   without `-cropbox` ignores a CropBox and renders the MediaBox. Byblos's entire
   geometry vocabulary is CropBox-preferring already — `PageInfo.Bounds` is "the
   page's CropBox, or its MediaBox when it declares no CropBox" (`inspect.go:50-51`),
   `PageRaster.Page` is `rectOf(p.CropBox)` (`extract.go:301`), and `classify` takes
   the CropBox. Introducing a second page-box convention inside one package is how
   the reversed-CropBox class of bug happens (`normalizedBox`, `extract.go:347-357`).
   CropBox is also what every viewer shows, which is what makes a thumbnail
   recognisable; poppler ships a `-cropbox` flag precisely because MediaBox is the
   wrong default for display, and kleio simply never passes it.

   **This is the one recommendation in this plan I would accept being overruled on**,
   because "do not visibly change the product" is a real argument for MediaBox parity.
   Either way it must be pinned by T7 so it is a decision with a name rather than a
   drift.

---

## 6. Which primitive scales — and the bit-depth trap, inverted

**Neither `Downsample` nor `DownsampleDeclaredBPC`. Use `draw.CatmullRom.Scale`
directly, through a small unexported helper. No new exported API.**

Two independent reasons, and the second is the one that matters.

### 6.1 The signatures do not fit, and forcing them breaks §5.2

Both take `srcDPI`/`dstDPI`, not a target size. Hitting exactly 310 x 400 would mean
back-solving a `dstDPI` and then letting `int(math.Round(dx * ratio))`
(`downsample.go:110-111`) decide the output — `round`, where §5.2 measured `ceil`.
Two roundings in series cannot be made to land on `ceil` reliably. And
`DownsampleDeclaredBPC` returns the source unchanged when `srcDPI <= dstDPI`
(`downsample.go:105-107`), which is right for its job and wrong as a way to express
"bound the long edge".

The helper is `draw.CatmullRom.Scale(dst, image.Rect(0,0,w,h), src, src.Bounds(), draw.Src, nil)`
with `w`,`h` computed per §5.1 — about ten lines, using `golang.org/x/image/draw`,
which is **already on the arch-test allowlist** (`imagecodecs_arch_test.go:61`) and
already imported by `downsample.go:8`. No allowlist change, no new dependency.
Checked before proposing it: no "resize to N pixels" helper exists anywhere in the
package today.

### 6.2 The bilevel rule is CORRECT for archival output and WRONG here

`byb-05w` and `byb-xcx` are about `declaredBPC == 1` needing point subsampling. That
rule exists to match Ghostscript's `/MonoImageDownsampleType /Subsample` so that a
bitonal raster **re-encoded back into a PDF** still holds two values — `downsample.go:56-63`
is explicit that this is about what a 1-bpc stream can store.

**A thumbnail is never re-encoded into a PDF. It is displayed.** Point-subsampling a
2550 x 3300 bitonal scan down to 310 x 400 discards ~98% of the pixels and produces
aliased, illegible speckle. Anti-aliased area/Catmull-Rom downsampling to grey is what
every thumbnailer does, and what the incumbent `pdftoppm` does.

So the trap here is **not** repeating `byb-plj`; it is the inverse. The likely future
regression is somebody landing `byb-xcx` (which adds `PageRaster.Bitonal`), noticing
that `Thumbnail` "ignores the declared depth", and helpfully routing it through
`DownsampleDeclaredBPC(img, 1, …)` — making every bitonal scan's thumbnail alias.
T5 exists to fail loudly when that happens, and the doc comment on the helper must say
why in one sentence: *the declared-depth rule governs image data going back into a PDF;
this path produces pixels for a screen.*

**Corollary:** `Thumbnail` does not need `byb-xcx` and must not be blocked on it.

---

## 7. Test plan

Every test below states what defect class it catches and whether it fails against the
**do-nothing null**, which for this feature is:

```go
func Thumbnail(ctx context.Context, r io.ReadSeeker, opts ThumbnailOptions) (image.Image, error) {
    return image.NewRGBA(image.Rect(0, 0, 310, 400)), nil
}
```

— a plausible stub: right type, right shape, right size for the commonest page,
blank pixels, no error. A test that this passes has no kill power.

### T1 — geometry table against the measured poppler numbers

Table-driven over all ten rows of §5's table (with the `tiny` and `cropped` rows
carrying byblos's *divergent* expected values per §5.1.4-5, and a comment naming
poppler's number). Assert `img.Bounds().Dx()/.Dy()`.

- **Catches:** wrong axis; round-vs-ceil; a missing or wrong `/Rotate`; a
  portrait/landscape branch that mishandles square.
- **Null:** FAILS on 5 of 10 rows (landscape 400x310, square 400x400, tall 80x400,
  ceil 241x400, rot90 400x310). **Strong.**
- **The trap to avoid:** a single-document version of this test has near-zero kill
  power — the null passes it. The table's value is entirely in the rows whose
  expected size differs from 310x400. If a reviewer sees this test with fewer than
  four distinct expected sizes, it has been gutted.
- Expected values are literals measured today, so this runs in the oracle-free CI
  pass. A second, poppler-backed variant may re-derive them, guarded by
  `exec.LookPath` + `t.Skip` as `arch_test.go:45-48` does.

### T2 — the thumbnail is a picture of the page

On a corpus scan whose raster has known structure, assert (a) the output is not a
constant colour, and (b) per-quadrant mean luminance correlates with the source
raster's per-quadrant means within a tolerance.

- **Catches:** returning a blank canvas; returning the wrong page; scaling from the
  wrong source; a horizontal or vertical flip.
- **Null:** FAILS on (a) immediately — a zero `image.RGBA` is one constant colour.
  **Strong.**
- Quadrant means rather than PSNR on purpose: a resampler change should not fail
  this, only a *structural* error should.

### T3 — a rotated page comes back rotated, not merely re-shaped

Build a page carrying a page-covering raster that is dark in one quadrant only, with
`/Rotate 90`. Assert the dark quadrant lands where poppler puts it.

- **Catches:** the specific bug §5.1.3 warns about — swapping `w`/`h` in the size
  computation and never rotating the pixels. **T1 cannot catch this**, because T1
  only reads bounds, and the buggy implementation's bounds are correct.
- **Null:** FAILS (blank, no dark quadrant). **Strong**, and it is the only test that
  discriminates a *correct* implementation from a *plausibly-wrong* one rather than
  from a stub. Highest value in the plan.

### T4 — the NotImplemented contract on a born-digital page

`Thumbnail(ctx, corpus born-digital, {})` must return an error with
`errors.Is(err, ErrNotImplemented)`, an `errors.As` target whose `Capability == "render"`,
that capability present in `capabilityRules`, and `!errors.Is(err, ErrNotSingleRaster)`.

- **Catches:** the worst available failure mode — silently returning a blank white
  page for 89.8% of documents. Kleio would write blank thumbnails and nothing would
  fail. Also catches leaking the divert sentinel (§4.1).
- **Null:** FAILS — the stub returns `nil` error. **Strong.**
- Add a second row for `jbig2.pdf` asserting `Capability == "decode-jbig2"`, which
  pins the §4 distinction that a renderer would not fix that page.

### T5 — bilevel scans are anti-aliased, not point-subsampled

Thumbnail a page whose raster is declared `/BitsPerComponent 1` and decodable, and
assert the output holds **more than two** distinct grey levels.

- **Catches:** the §6.2 regression — a future change routing `Thumbnail` through
  `DownsampleDeclaredBPC` with the declared depth.
- **Null:** FAILS (one level). A `draw.NearestNeighbor` implementation has two and
  also FAILS. **Strong.**
- **Acceptance is mutation-proof, and must be demonstrated:** swapping
  `draw.CatmullRom` → `draw.NearestNeighbor` in the implementation must make this test
  fail. Record the observed level counts in the test's comment, in the style of
  `byb-05w`'s note about `TestDownsampleDoesNotInferBilevelFromPixels`.
- **Cost, stated up front:** no such fixture exists. `internal/corpus`'s only 1-bpc
  rasters are JBIG2 (`corpus.go:841,937` — byblos cannot decode them, they divert) or
  inside `mrc` (diverts as `mrc-layers`), and `corpus.go:826` records that no document
  sets `/ImageMask true`. So T5 needs a **new corpus document**, taking `corpus.Count`
  35 → 36, which then requires updating every `"N corpus documents"` claim in the tree
  or `TestCorpusCountClaimsMatchTheCorpus` fails. That is why T5 is its own build stage.

### T6 — no upsampling

A page whose raster is smaller than the target: assert `max(w,h) <= LongEdgePx` **and**
that the dimensions equal the source raster's, not the target's.

- **Catches:** silently upsampling to match poppler, contradicting `downsample.go:18-19`.
- **Null:** FAILS (fixed 310x400). **Strong.**

### T7 — the CropBox convention

MediaBox 612 x 792 with CropBox [0 0 306 792] → assert 155 x 400, with a comment
recording that `pdftoppm` gives 310 x 400 and why byblos differs (§5.1.5).

- **Catches:** drift back to MediaBox; and more usefully, it makes the divergence a
  decision someone has to consciously reverse.
- **Null:** FAILS. **Strong.**

### T8 — cancellation (blocked on byb-xyn)

An already-cancelled `ctx` must yield `errors.Is(err, context.Canceled)` and no image.
A `ctx` cancelled mid-call on the most expensive available document must return within
the bound `byb-xyn` sets, measured.

- **Catches:** a `Thumbnail` that accepts `ctx` and ignores it — which is the default
  outcome, since `pdfcpu` is not context-aware.
- **Null:** FAILS the pre-cancelled case. **Strong** for that half; the latency half is
  only as strong as `byb-xyn`'s bound.

### Tests deliberately NOT written, and why

Named explicitly because each looks reasonable and all four have **zero** kill power
against the null:

- *"returns a non-nil image for `scan.pdf`"* — the stub passes.
- *"the output is at most 400px on every side"* — the stub passes; so does a 1x1
  image. Only meaningful folded into T1's table with exact expected sizes.
- *"returns an error for a malformed PDF"* — the error is `pdfdoc.Open`'s and is
  already covered; the thumbnail code contributes nothing to it. Keep it as one row
  of T1, never as a standalone test.
- *"the returned image is an `*image.RGBA`"* — pins an implementation detail, blocks a
  legitimate change, corresponds to no defect class.

---

## 8. Staged build order

Every stage ends with `make test` green and the tree shippable. Stages 2 onward can be
abandoned at any point without leaving the tree broken or the API half-formed.

### Stage 0 — prerequisites (not this lane's code)
- `byb-xyn` lands its `context.Context` convention (§2.2). **Blocking**, for merge
  order alone.
- Chris answers the non-embedded-font question (§0.5). Blocking for stage 4 only.
- **Checkpoint:** unchanged tree, suite green.

### Stage 1 — the API and the extraction path
Adds `thumbnail.go` (`ThumbnailOptions`, `Thumbnail`, unexported `scaleToBox` and
`rotate90/180/270` helpers), the §3 spec block **in the same commit**, and tests
T1, T2, T3, T4, T6, T7.

- **Verify:** `env CGO_ENABLED=0 /opt/homebrew/bin/go test ./...` exits 0 — run the
  plain command and check the status, not just `-json`; and
  `TestDesignSpecPublicAPIBlockMatchesThePackage` passes, which it cannot unless the
  spec block and the code agree.
- **Value at this checkpoint:** kleio can call `Thumbnail` and fall back to `pdftoppm`
  on `errors.Is(err, ErrNotImplemented)`. Removes the subprocess for 100% of the `ia`
  leg and 9.8% of the mixed sample. `pdftoppm` stays in the container.
- **Stopping here is a legitimate end state**, and is what §1 recommends until §0.5 is
  answered.

### Stage 2 — the bilevel presentation guard
New 1-bpc corpus fixture, `corpus.Count` 35 → 36, the tree-wide `"N corpus documents"`
sweep, and T5.

- **Verify:** `TestCorpusCountClaimsMatchTheCorpus` and `TestCorpusReadableCountIsWhatTheCorpusDeclares`
  pass; and the mutation check — `draw.CatmullRom` → `draw.NearestNeighbor` must fail T5.
- Separate from stage 1 because its cost is a corpus bump, not thumbnail code.

### Stage 3 — cancellation
Thread `ctx` per `byb-xyn`'s decided semantics; add T8; record the measured worst-case
latency in the doc comment.

- **Verify:** T8 passes; the measured number is written down, not asserted vaguely.

### Stage 4 — the renderer, only after §0.5 is answered
Each sub-stage is independently shippable and moves a **named** share of documents from
the `*NotImplemented` branch to the rendered branch. The signature never changes.

| | what it adds | population it unblocks | new gate |
|---|---|---|---|
| 4a | real path model (segments, Bézier flattening), fill/stroke, rect clip, DeviceGray/RGB/CMYK | blank and vector-only page 1s (~0.8%) | **`x/image/vector` must be added to `imagecodecs_arch_test.go:59-62` and `ci.yml`**, with a written note that it calls no `image.RegisterFormat` (verified). Also its `// TODO: non-zero vs even-odd winding?` (`vector/vector.go:324,360`) must be resolved or worked around. |
| 4b | image XObjects drawn under a CTM | partial for the 34.3% mixed class — images appear, text does not | none |
| 4c | embedded TrueType text (`sfnt` + `vector`, encodings, `/Widths`, text matrices) | ~26% of font uses; ~32% of documents fully | `x/image/font/sfnt` allowlist entry; `pdfcpu` font access must go through `internal/pdfdoc` (`arch_test.go:12`) |
| 4d | bare CFF / Type 1C charstrings | +21.7% of font uses (the embedded ones; the 1.5% non-embedded fall to 4f) | none |
| 4e | Type 1 PFB: eexec + Type 1 charstrings | +4.7% of font uses -- the embedded Type 1s only. The other 16.3% are non-embedded and belong to 4f. | none |
| 4f | **substitute fonts for the 47.8% of uses that embed nothing** | the remainder — and the only stage after which `pdftoppm` can actually be removed | a licensing and binary-size decision |

The ordering makes the honest point visible: **4a-4e do not add up to a replacement for
`pdftoppm`.** Only 4f does, and 4f is the decision in §0.5 that has not been taken.

---

## 9. What was not checked

- **Kleio's side of the swap.** The call site (`validate.go:338`) and the incumbent
  helper (`tools.go:454-479`) were read; no change to kleio was designed or attempted,
  and kleio still does not import byblos.
- **Rendering quality.** No renderer exists, so nothing was compared pixel-for-pixel
  against `pdftoppm` beyond output *dimensions*. The §5 table pins geometry only.
- **The `pdffonts` sample is 840 of the 5,038 render-needing documents** (every 6th),
  not all of them, and `pdffonts` reports poppler's classification, not the raw
  `/FontFile*` keys. The shares in §0.4 are indicative to a few percent, not exact.
- **`x/image/font/opentype`'s rasterizer** was not evaluated as an alternative to
  `sfnt` + `vector` for stage 4c. It may be a better starting point; nobody looked.
- **Multi-page and non-page-1 thumbnails** are out of scope by `byb-0gm`'s decision
  and were not designed for; `ThumbnailOptions.Page` is the additive escape hatch.
- **The 18 read failures** in §0.3 were not diagnosed. They are `pdfdoc.Open` failures
  and are a separate concern from thumbnails.

## 10. How to re-run the measurements

- Corpus extraction sweep: `go run ./cmd/byblos-corpus <dir>`, then call
  `ExtractPageRaster(f, 1)` on each file from a module that `replace`s byblos.
- Pinned-sample page-1 sweep: same probe, fed `cut -f1 ~/work/dobbo-ca/.byblos-sample/manifest.tsv`.
  Record `Inspect`'s page-1 `len(Images)` and `TextChars` alongside the verdict —
  that pair is what splits "needs a renderer" from "extracts".
- Geometry table: `pdftoppm -png -singlefile -f 1 -l 1 -scale-to 400 <pdf> <prefix>`,
  then read the PNG IHDR width/height.
- Font shares: `pdffonts -f 1 -l 1 <pdf>` over the diverted-with-text subset.
