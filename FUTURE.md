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

## A PDF renderer

**Capability string:** `render`

Would let Byblos handle pages that `ExtractPageRaster` currently rejects with
`ErrNotSingleRaster`: tiled rasters, image-plus-vector-overlay, and genuinely
mixed content.

**Why deferred:** this is the single largest piece of work anywhere in the Cadmus
/ Byblos family — a content-stream interpreter plus Type1/CFF/TrueType/CID font
rasterization, colour spaces, shadings, and transparency groups. Plausibly larger
than the OCR engine.

**Do not start this on principle.** Start it only if the instrumented divert rate
(design spec §2) shows the case is actually common. If it is rare, diverting to
`needs_review` is the correct permanent answer, and a renderer would be an
enormous amount of code serving a handful of documents.

**Upgrade path:** documents whose provenance records `diverted: not-single-raster`.

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

**Why deferred:** Cadmus L0 already provides the primitives (morphology,
connected components, deskew), so this is mostly a matter of assembling them with
sensible defaults. Not urgent, because Kleio's current pipeline gets acceptable
results without it — and aggressive cleanup risks removing real content, which
needs the same scrutiny as lossy compression.
