# Future work

Capabilities considered during design and deliberately not built yet. Each entry
records **why** it was deferred, so the reasoning is available to whoever picks it
up rather than being rediscovered.

Anything added here should also add a **capability string**, so that documents
processed before it existed can be identified via `UpgradeCandidates` (see the
design spec §6). That is the whole point of capability-based provenance: shipping
one of these should not require re-processing the archive, only the documents it
would actually improve.

---

## JBIG2 symbol dictionary — lossless mode

**Capability string:** `jbig2-symbol`

Match repeated glyphs across a page, store each distinct bitmap once, and
reference it thereafter. Substituting only when bitmaps are **exactly equal**
keeps this lossless, so it carries none of the substitution risk of lossy mode
below.

Compression is substantially better than generic-region coding on text-heavy
scans, which is most of Kleio's corpus. This is the intended next capability.

**Why deferred:** roughly 3-4× the work of generic region coding — glyph
segmentation, dictionary construction, and refinement coding, on top of the MQ
coder that v1 already needs. Generic region delivers most of the practical benefit
for a fraction of the effort, and shipping it first means the MQ coder and the
bitstream plumbing are already proven when this lands.

**Upgrade path:** documents whose provenance records `applied: jbig2-generic` are
exactly the upgrade set.

---

## JBIG2 lossy symbol matching — REJECTED, not merely deferred

**Capability string:** none. Do not assign one.

This is what `ocrmypdf --jbig2-lossy` does, and what Kleio's current aggressive
preset uses today. It achieves the headline ~100:1 ratios by unifying glyphs that
are *nearly* identical rather than exactly identical.

**Why rejected:** it can silently alter document content. The 2013 Xerox scanner
defect — in which scanned digits were replaced with different digits, producing a
clean-looking image that was simply wrong — was this mechanism. Kleio is a
document archive that may hold financial and medical records.

The failure mode is particularly bad here because it is invisible to Kleio's
validation gate. That gate detects *degradation* — blur, noise, low OCR
confidence. Lossy symbol substitution produces output that scores **better** on
every one of those signals while being factually incorrect. The safeguard cannot
see it.

**If someone revisits this,** the burden is to show how a substituted character
would be detected before the original is discarded under the retention policy. In
the absence of such a mechanism, the ratio is not worth the risk. Do not enable it
because a benchmark looked good.

---

## CCITT G4 encoding

**Capability string:** `ccitt-g4`

Lossless bitonal compression, universally supported by every PDF reader ever
written, and much simpler than any JBIG2 mode.

**Why deferred:** compression is meaningfully worse than JBIG2 generic region, so
it does not serve Kleio's minimum-size directive. Worth adding only as a
*compatibility* fallback if a consumer is found that cannot read JBIG2 — which is
rare, since JBIG2 has been in the PDF specification since 1.4.

---

## Raster decoders — JBIG2, JPEG 2000, CMYK TIFF

**Capability strings:** `decode-jbig2`, `decode-jpx`, `decode-tiff`

Would let Byblos handle pages that `ExtractPageRaster` currently rejects with
`ErrUnsupportedImageCodec`. The page is understood — one page-covering raster —
and only its codec is out of reach. `decode-tiff` is the smallest of the three
and arrives with B3, which already pulls in `golang.org/x/image/tiff` for the
CMYK rasters pdfcpu re-renders as TIFF.

**Why deferred:** B0/B1 targeted the extraction path, and the corpus Byblos was
built against is consumer-scanner output, which is entirely PNG and JPEG.

**Why they are separate capability strings:** the codec mix is wildly
corpus-dependent, so "would a decoder help?" has no single answer. Codec of the
page-covering raster on scan-shaped pages, measured on `byb-divert`:

| corpus | n | codec mix | undecodable |
|---|---|---|---|
| localscans | 110 | png 100% | 0.0% |
| personal | 30 | png 66.7%, jpg 33.3% | 0.0% |
| govdocs1 | 875 | png 82.4%, jbig2 8.9%, jpg 5.6% | 12.0% |
| dc_random | 520 | jbig2 54.6%, png 20.6%, jpg 20.0% | 56.9% |
| commons | 8048 | jpx 54.8%, png 22.2%, jbig2 12.4%, jpg 10.7% | 67.2% |
| ia | 19590 | jpx 84.0%, jbig2 7.4%, png 5.8%, jpg 2.8% | 91.3% |

151 of the 347 files carrying a page-covering scan raster were 100%
undecodable. On an archive-processed corpus the **input** side is a bigger gate
than the output side; on the one raw-consumer-scanner corpus it is not a gate at
all. Settle what Kleio actually ingests before ordering B2 and B3.

**Upgrade path:** documents whose provenance records
`diverted: unsupported-codec-jbig2` / `-jpx` / `-tiff` nominate only the
matching decoder (`byb-z8j`). A document written before `byb-z8j`, or one whose
codec pdfcpu declined to name, still carries the coarse
`diverted: unsupported-codec` and nominates all three, since nothing can tell
after the fact which codec it held.

---

## A PDF renderer

**Capability string:** `render`

Would let Byblos handle pages that `ExtractPageRaster` currently rejects with
`ErrNotSingleRaster`: tiled rasters, image-plus-vector-overlay, and genuinely
mixed content.

It would **not** help a page that diverted with `unsupported-codec`: a renderer
still has to decode the raster it is compositing. Those pages want a decoder
above, and they do not count toward the divert rate that justifies this work.

**Why deferred:** this is the single largest piece of work anywhere in the Cadmus
/ Byblos family — a content-stream interpreter plus Type1/CFF/TrueType/CID font
rasterization, colour spaces, shadings, and transparency groups. Plausibly larger
than the OCR engine.

**Do not start this on principle.** Start it only if the instrumented divert rate
(design spec §2) shows the case is actually common. If it is rare, diverting to
`needs_review` is the correct permanent answer, and a renderer would be an
enormous amount of code serving a handful of documents.

**This gate was evaluated and it stayed shut.** `byb-divert` measured genuinely
composite pages at 1.03% of 29,779 scan-shaped pages (2026-07-28). Rare. So the
renderer described in *this entry* — the one that would rescue
`ErrNotSingleRaster` pages — remains deferred on the evidence.

`byb-0gm` decided Byblos will render anyway, and that decision **bypasses this
gate rather than passing it**: it is driven by Kleio rasterizing every
born-digital document for a 400px thumbnail, which is a different page population
and a different job from anything this entry describes. Do not read `byb-0gm` as
having opened this gate. See design spec §2's 2026-08-03 amendment, where the
fidelity question that separates the two is still open.

**Upgrade path:** documents whose provenance records `diverted: not-single-raster`,
or any page whose provenance records `dropped_annots > 0` (byb-b5.1): rendering
appearance streams into the raster would change the output for those pages.

---

## PDF/A conversion

**Capability string:** `pdfa`

Archival-standard conformance: embedded fonts, defined colour spaces, no external
dependencies, XMP metadata.

**Why deferred:** Kleio has not asked for it. It matters for regulated retention
regimes, so it may become a requirement rather than a nicety — but building it
speculatively would be guessing at which conformance level (PDF/A-1b, -2b, -3b) is
actually needed, and they differ.

---

## XMP metadata for provenance

**Capability string:** none — this is a storage-format change, not a capability.

Provenance currently lives as JSON under a custom Info-dictionary key. The Info
dictionary is deprecated in PDF 2.0 in favour of XMP.

**Why deferred:** the Info dictionary works today, is trivially readable, and
nothing in Kleio requires PDF 2.0 conformance. Revisit alongside PDF/A, which
requires XMP anyway.

**Migration note:** `ReadProvenance` should keep reading the Info-dictionary form
indefinitely, since documents processed by earlier versions will carry it.

---

## `unpaper`-equivalent page cleanup

**Capability string:** `page-cleanup`

Despeckling, border removal, and page-splitting for two-up scans.

**Why deferred:** not urgent, because Kleio's current pipeline gets acceptable
results without it — and aggressive cleanup risks removing real content, which
needs the same scrutiny as lossy compression.

**Byblos cannot assemble Cadmus's primitives, and this entry used to say it
could.** Cadmus has the equivalents (morphology, connected components, deskew,
and a Sauvola binarizer) but they live under `cadmus/internal/imaging/`, which
Go's `internal/` rule puts out of reach — and design spec §3 forbids the import
independently. Whatever Byblos builds here, it writes itself. `byb-jj5` is the
worked example: it needed a binarizer, Cadmus had one, and Byblos is writing its
own regardless.

**Scope note.** This entry covers despeckling, border removal and page-splitting;
its primitive list is morphology, connected components and deskew. Thresholding
is in neither list, so `byb-jj5`'s binarizer does not land here and this entry
did not need amending for it — contrary to the documentation debt that bead
records against it. A binarizer is a JBIG2 input stage, not page cleanup.
