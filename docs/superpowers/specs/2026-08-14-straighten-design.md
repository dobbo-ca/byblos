# Straighten a page: the transform contract, and the lossless content matrix

Beads: byb-16j.4 (the contract), byb-16j.2 (the implementation).
Parent: byb-16j (G5). Measured gates: byb-16j.1 (census, closed), byb-16j.3 (keystone, closed).

Written 2026-08-14 against origin/main 870ce63.

## 1. What this builds, and what it deliberately does not

An interactive editor — kleio's, client-side — lets a user straighten a scanned page against a
live preview. The instructions come back to byblos, which executes them once, at full
resolution, against the pristine source.

This spec covers two halves of that:

- **The contract.** An absolute, declarative transform on `PageSource`, plus the shared fixture
  that fails when the client preview and the byblos execution disagree.
- **The implementation.** A lossless arbitrary-angle rotation through the content stream.

It does not build: a keystone or perspective correction (byb-16j.3, closed — no detectable
population, and the pinned sample cannot produce the evidence because every corpus in it is
flatbed or already rectified); a lossy raster resample (priced at 233 pages of 169,376, of
which 132 are one document); a user-driven crop (deferred, see §6); or any change to
`extract.go` (see §7).

### The measurement this rests on

byb-16j.1 measured content skew over 27,910 pages that reduce to one raster and decode:

| percentile | degrees |     | threshold | pages | share |
|---|---|---|---|---|---|
| p50 | 0.100 | | > 0.5 deg | 3,432 | 12.30% |
| p90 | 0.600 | | > 1.0 deg | 960 | 3.44% |
| p99 | 1.825 | | > 2.0 deg | **233** | **0.83%** |
|  |  | | > 5.0 deg | 21 | 0.08% |

"A scanner leaves a page 1-10 degrees off" is 3.44% of the population, not the norm. **Size
this for small angles.** The 15-degree search rail in `internal/skew` was never approached by a
real page except through a bug.

## 2. The parameterisation, and why it is an angle

`byb-16j.4` asked whether to carry four corners or an angle plus two keystone terms. The census
answers it: neither keystone axis has a population above its own noise floor. Four corners
would be carrying a representation, across three codebases, for a transform nothing in the
sample needs. **The transform is an angle and a crop.**

```go
// StraightenSpec is an absolute correction to apply to one page's content.
//
// Deg is the rotation byblos applies, in degrees, positive COUNTER-CLOCKWISE in
// PDF default user space. That is the same signed convention as
// skew.Estimate.Deg (internal/skew/skew.go:70-74), pinned there by
// TestSignIsUserSpace, and it is stated in exactly one place for both sides of
// the repository boundary.
//
// It is ABSOLUTE. It is the angle from the ORIGINAL page, never a delta on
// whatever is already applied. kleio redelivers a job at least once (ocr.go:55-60),
// so a transform that composes with what is already there corrupts the page on a
// retry. This is the same argument byb-yul.4 settled for PageSource.Rotate.
//
// To straighten a page flat: measure the raster's content angle D with
// internal/skew, read the placement angle p from ImageRef.PlacementDeg, and set
// Deg = -(p + D). The two cancel on a page that already reads straight —
// verified over 159 pages of 005393.pdf, mean |p + D| = 0.093 degrees.
type StraightenSpec struct {
    Deg  float64
    Crop *[4]float64 // [llx lly urx ury]; see §6. Refused when non-nil.
}
```

It hangs off the existing row rather than adding a second call:

```go
type PageSource struct {
    Source     io.ReadSeeker
    Page       int
    Rotate     int             // unchanged: ABSOLUTE, one of 0/90/180/270
    Straighten *StraightenSpec // nil for no correction
}
```

`StraightenSpec` is canonical in `internal/pdfdoc` and aliased from the root package, for the
same reason `PageSource` and `EncodedImage` are (`editpages.go:30-33`): internal packages are
unreachable from kleio, and a parallel type would be a second thing to keep in step.

It is a pointer so that "no correction" and "a correction of 0.0 degrees" stay distinguishable.
That is the same argument `PageGeometry` already makes for its own pointer-ness, and a 0.0
degree correction is a real instruction — it is what an editor sends when a user drags the
slider back to centre.

`PageSource.Rotate` keeps its refusal exactly as it is. `/Rotate` cannot carry an arbitrary
angle: ISO 32000-1 table 30 allows only a multiple of 90, and pdfcpu writes `/Rotate 45` with a
nil error and then refuses the file it wrote. That reasoning is already recorded at
`internal/pdfdoc/buildpages.go:126-137` and does not move.

### Absolute is ENFORCED, not assumed

Stating that `Deg` is absolute does not make it so. `WrapContent` cannot see a wrap that is
already there, so applying `Deg` to an already-straightened document composes to `2·Deg` while
the provenance record, being replaced rather than accumulated, reports `Deg`. That is a silent
double rotation with a record that under-states it — worse than a delta, because it looks right.

Citing kleio's redelivery does not close this, and validating the spec against kleio showed why:
`ocr.go:55-57` describes a stage that re-runs **over its own output**, and `ocr.go:209-218`
records that as a deliberate cost, safe only because OCR is idempotent from its own output.
Kleio has both patterns in production — the compress stage re-reads the original each attempt
(`compress.go:525`), the OCR stage does not. "The caller will send the original" is a hope.

So byblos enforces it:

> **The applied rotation is `Deg` minus whatever the source page's provenance already records as
> `Straightened.Deg`, defaulting to zero.** `Deg` therefore means "the total angle from the
> ORIGINAL", whatever document the caller hands over, and the record always holds that same
> total rather than the increment just applied.

This costs nothing to implement. `BuildFromPages` already reads the source's provenance before
building (`editpages.go:100-103`) — the old record is in hand at exactly the moment the
difference is needed.

It makes redelivery idempotent under both of kleio's patterns. Re-running `Deg = 3.2` against an
export already carrying `Straightened{Deg: 3.2}` applies `0.0` and changes nothing; running it
against one carrying `1.0` applies `2.2` and lands on `3.2` from the original either way. The
"one generation of loss" property becomes a fact about the operation rather than a rule the
caller has to remember.

A source whose provenance is absent or unreadable is treated as unstraightened, which is the
only safe default: it is what every document byblos has never seen looks like.

### The premise this still rests on, and it is kleio's to keep

Byblos applies the correction to the bytes it is given. It cannot verify that those bytes are
the original scan rather than a compressed or OCR'd rendition, and the rule above only aligns
successive corrections — it does not recover resolution already lost upstream.

Kleio's retention policy can remove that original. `ApplyRetention` (`validate.go:62-77`, called
at `validate.go:429`) DELETES it under the `discard` policy and tags it for a Glacier lifecycle
transition under `archive_glacier`, after which it is not readable without a restore. Only
`keep_standard` — the default (`0001_tables.sql:89-91`) — keeps it hot.

A straighten of a `discard`-policy document is therefore a correction of a second-generation
rendition. That is a legitimate thing to do and it is not this library's call, but it is not
what "applied once, to the pristine source" describes, and the kleio-side design must decide it
rather than inherit it silently.

### What validate rejects

`validate` (`internal/pdfdoc/buildpages.go:138`) already rejects what cannot be resolved without
opening a document, and gains two rows:

- **A non-finite `Deg`.** NaN or an infinity produces a `cm` of non-finite numbers, which pdfcpu
  writes and `api.Validate` then refuses — the same shape of failure as `/Rotate 45`, which is
  exactly what this function exists to catch at the call rather than at the next reader.
- **A non-nil `Crop`.** Not implemented in this version (§6). Refusing is what lets the field
  exist in the contract now without a caller silently getting a page that ignored it.

An angle outside `(-180, 180]` is **not** rejected. The arithmetic takes it modulo 360 by
construction, so 370 and 10 produce the same matrix and the same page; there is nothing to
refuse and normalising would only hide a caller's own bug. This is unlike `Rotate`, where the
refusal exists because the value is written into the file verbatim.

## 3. The mechanism

Wrap the page's whole existing content in a rotation, and touch nothing else.

```
  /Contents  before            /Contents  after
  [ s1 s2 s3 ]        ===>     [ NEW  s1 s2 s3  NEW ]
                                  |              |
                                  v              v
                        "q a b c d e f cm"      "Q"
```

`s1 s2 s3` are never decoded and never rewritten. The raster is carried byte for byte, so the
correction is lossless; text stays text and stays selectable. It therefore works on a
born-digital page, and it composes with a JBIG2 or JPX raster byblos cannot decode at all.

This follows `AppendContent`'s posture (`internal/pdfdoc/text.go:181-184`): appending a new
stream is byte-preserving for everything already there, where rewriting means decoding and
re-encoding bytes that are not this call's business, and fails outright on a filter pdfcpu
cannot decode.

### The matrix

For `Deg = t` about a centre `(cx, cy)`:

```
a =  cos t     c = -sin t     e = cx(1 - cos t) + cy·sin t
b =  sin t     d =  cos t     f = cy(1 - cos t) - cx·sin t
```

### The rotation centre is the CropBox centre

Not the origin. Rotating about the origin was measured to push the raster off the right page
edge at every angle: a unit-square placement on a 612 pt page reaches URX 611.998 at 0.13
degrees and 607.44 at 7. The CropBox is also the box byblos itself treats as the page
(`inspect.go:247`, `extract.go:369`, `extract.go:523`, `extract.go:560`); `MediaBox` reaches
byblos only as CropBox's fallback (`internal/pdfdoc/pdfdoc.go:411-414`).

### No net-CTM correction, unlike AppendContent

`AppendContent` computes `netCTM(cur)` and applies its inverse, because appended ops must run
in default user space whatever the existing content left in effect (`text.go:166-178`). A
prepend needs no such correction: it is the outermost transform by construction. The trailing
`Q` restores the state saved by our own `q`, so it works even when the existing content leaves
a stray `cm` outside any q/Q pair.

### It composes with /Rotate; it does not replace it

`/Rotate` is a display attribute the viewer applies after rendering the content; the `cm` is
inside the content. They compose, in that order, and `/Rotate` is left untouched.

The **angle** passes through `/Rotate` unchanged, because rotations commute. The **crop** does
not — a rectangle stated in the displayed frame must be mapped back through `/Rotate` to the
content frame, and getting that backwards produces an export cropped a quarter turn away from
the preview with no error anywhere. That is why §6 states the crop's frame now, before anything
uses it.

## 4. The page box does not change

Both `MediaBox` and `CropBox` are left as they are.

The consequence, stated rather than discovered: the rotated content's corners fall outside the
box, and no viewer draws them. At the measured p90 of 0.6 degrees these are slivers. The page
box becomes the user's crop when §6 lands, which is the decision that actually determines it.

A rotated rectangle cannot cover an axis-aligned one — the four corner triangles are empty.
**`CoversPage()` used to report `true` anyway; byb-2mt (below, §13) has since fixed it.** Measured
on this branch before byb-2mt landed:

```
  deg=0     CoversPage=true   page=(0,0)-(612,792)  bounds=(0,0)-(612,792)
  deg=0.6   CoversPage=true   page=(0,0)-(612,792)  bounds=(-4,-3)-(616,795)
  deg=1.0   CoversPage=true   page=(0,0)-(612,792)  bounds=(-7,-5)-(619,797)
  deg=1.9   CoversPage=true   page=(0,0)-(612,792)  bounds=(-13,-10)-(625,802)
```

`CoversPage` was a plain `p.Page.In(p.Bounds)` (`extract.go:241`) and `Bounds` is the
*axis-aligned bounding box* of the placed raster. Rotation grows that box by `|cos t| + |sin t|`,
so the box swallows the page while the raster itself no longer reaches the corners. byblos
therefore reported a straightened page as fully covered with more confidence than before, not
less. The genuinely uncovered share is 3.26% of the page at 2 degrees.

This was byb-2mt exception 3 — every geometry consumer modelled a rotated footprint as its
bounding box — reachable at any placement within `maxSkewDeg`, with or without this feature.
byb-2mt replaced the bounding-box test with a true-quadrilateral test (`content.Quad`,
`internal/content/walk.go`) at the four sites that needed it: `inkHidden`, the `contains` check
inside `classify`'s stacked-page arm, `PageRaster.CoversPage`, and `PageGeometry.CoversPage`. The
`covers` check beside `contains` was deliberately left AABB-only — a rotated placement's true
quad falls short of covering the page by several points at even a fraction of a degree, past any
tolerance already in the package, which would divert every rotated *stack* (including `internal/corpus`'s `stacked` fixture, the
16,241-page identical-matrix dedup shape) the moment it carries any rotation at all; see
`extract.go`'s comment on `covers` in `classify` for the full argument.

`TestStraightenCoversPageIsFalseUnderRotation` (renamed from
`TestStraightenCoversPageReportsTheAABBLie`) now pins the corrected behaviour: `CoversPage()` is
`false` under rotation and `true` only for an axis-aligned placement.

`classify` does not require a single-image page to cover the page box (`extract.go:712-745`,
byb-b1.3's argument), so extraction itself is unaffected either way.

Growing the MediaBox was considered and rejected for now: it changes the page size every
downstream consumer sees, and `internal/pdfdoc/pdfdoc.go:411-414` does not intersect CropBox
with MediaBox as ISO 32000-1 7.7.3.3 requires, so the two diverging is silent inside byblos and
visible everywhere else.

## 5. Where it lives

Two new helpers in `internal/pdfdoc`, beside `AppendContent`.

### WrapContent

```go
// WrapContent brackets page n's whole content with before and after, as two new
// streams. No existing stream is decoded or rewritten.
func (d *doc) WrapContent(n int, before, after []byte) error
```

`WrapContent` joins the `Doc` interface (`internal/pdfdoc/pdfdoc.go:203`) beside
`AppendContent`, for the reason recorded there: it needs the context `Open` normalised, so there
is no way to reach it from a document byblos did not read. `contentDepth` stays unexported — it
is this package's own guard, not a question a caller asks.

`/Contents` has four legal shapes and they are not interchangeable:

| shape | what WrapContent does |
|---|---|
| `types.Array` | `Array{beforeRef, old..., afterRef}` |
| `IndirectRef` to a stream | `pd["Contents"] = Array{beforeRef, ref, afterRef}` |
| `IndirectRef` to an array | append at both ends, written back through `xt.FindTableEntryForIndRef` |
| `nil` | no content to rotate; the wrapper is still written, so the record and the file agree |

A direct `types.StreamDict` is malformed (ISO 32000-1 7.3.8.1) and is refused, matching
`AppendContent` and `ReplaceImage`.

**The indirect-ref-to-array case is a known trap** and the reason it needs the explicit
write-back is already recorded at `text.go:266-281`: wrapping an indirect reference to an array
in a new outer array produces `/Contents` pointing at an array containing that array, which no
reader accepts as page content — silently discarding the whole page. And `xt.Dereference` hands
back the table entry's own `Object`, so assigning through a local variable after `append` (which
may reallocate) does not reach back into the map.

### contentDepth, and the q/Q refusal

```go
// contentDepth returns the net q/Q nesting page n's content leaves behind.
func (d *doc) contentDepth(n int) (int, error)
```

`netCTM` (`text.go:349`) already lexes the whole stream tracking q/Q/cm and throws the depth
away; `text.go:170-178` records a previous `qDepth` being deliberately removed. This restores it
for a different question.

Any imbalance returns `ErrUnbalancedContent` and the page is refused. The fatal direction is a
**surplus `Q`**: it pops our own `q` mid-stream, so every mark after that point is silently not
rotated — the page comes out half-corrected, with no error. A surplus `q` is merely malformed.
Both are refused, because a wrapper that only half-applies is worse than a refusal.

**This refusal is unpriced.** Nothing in the repository can currently detect an unbalanced
content stream, and nobody has counted them. byb-16j.2 carries a measurement pass over the
pinned sample — content streams decompress, no image decodes, so it is far cheaper than the
skew census. If the population is material it becomes its own bead.

## 6. The crop is declared and not built

`StraightenSpec.Crop` is refused when non-nil. Its frame is fixed now anyway, because the frame
is the part that gets silently mismatched and it is cheaper to state before a consumer exists
than after:

> **The crop is stated in the SOURCE page's unrotated PDF user space** — `[llx lly urx ury]`,
> origin lower-left, y increasing upward, the same convention as `PageGeometry`. That is the
> only frame that is a property of the file rather than of a viewer's interpretation of
> `/Rotate`, and it is the frame byblos writes in.

The editor does the mapping from its canvas. That mapping is **four steps, not one**, and naming
only the axis flip would understate it:

1. **pixels to points**, by the client's own render scale.
2. **y-flip**, against the page height. The canvas is origin top-left with y running **down**;
   PDF user space is origin lower-left with y running **up**. byb-16j.1 hit exactly this flip
   while building its own instrument, and `internal/skew/skew.go:660` is where byblos does the
   conversion — once, deliberately, rather than leaving it to a caller.
3. **inverse `/Rotate`**, for a page declaring 90, 180 or 270. Every raster a client is likely to
   hold has already had `/Rotate` applied to it — `pdftoppm` does — so its frame is the displayed
   one and this step is not optional. This is the silent quarter-turn failure §3 names.
4. **CropBox origin offset.** A renderer renders the CropBox, which need not sit at `(0, 0)`.

Steps 3 and 4 are the ones that produce a plausible-looking crop in the wrong place, because
they are no-ops on the common page and only bite on the page that carries an unusual box or a
declared rotation.

When the crop lands it also determines the page box, which §4 leaves alone, and it restores the
covering-raster property that §4 gives up: a rectangle inscribed in the rotated content is
fully covered, where the original box is not.

## 7. What is recorded

Both routes, for different reasons. Neither is sufficient alone.

### Applied gets the bare capability name

`Applied` gains `"straighten"`, with no parameter. It is union-idempotent, so it survives
`unionSorted` (`provenance.go:444-449`) unchanged however many times a page is reprocessed, and
`anyPageApplied("straighten")` works.

It carries no angle, and it must not try to. `appliedCapability` (`upgrade.go:255-266`) strips
only an **all-digit** final segment: `"straighten-3.2"` aborts on the `.` and returns unchanged,
so the capability would read as `"straighten-3.2"` forever and never match. A negative angle
fails the same way, on the `-` delimiter. Encoding the angle as an integer was rejected: p50 is
0.100 degrees and p90 is 0.600, so integer degrees discard the whole distribution.

### The angle needs a typed field, because Applied unions

```go
type PageProvenance struct {
    Applied       []string
    Diverted      string
    Placement     []float64
    DroppedAnnots int
    Geometry      *PageGeometry
    Straightened  *PageStraighten // NEW
}

// PageStraighten is the correction byblos applied to this page's content, in
// StraightenSpec.Deg's signed convention.
//
// It is ABSOLUTE and is REPLACED, never accumulated — which is why it cannot
// live in Applied. unionSorted UNIONS, so two corrections of one page would
// leave two contradictory angles in the record, and a union cannot express an
// absolute value.
//
// It is a pointer for the reason PageGeometry is one (see that type): a 0.0
// degree correction is a real measurement, and `omitzero` on a value type would
// make it serialize identically to "never straightened".
type PageStraighten struct {
    Deg float64 `json:"deg"`
}
```

### Three fixes it needs to survive at all

1. **`RecordExtractionContext` rebuilds every page record and merges only `Applied`**
   (`provenance.go:436`). Every other field is taken fresh from `extractPage` and the old value
   is discarded. `Placement`, `DroppedAnnots` and `Geometry` survive only because `extractPage`
   re-derives them from the document; an applied angle is not derivable from the document. The
   carry-forward must add `Straightened`.

2. **That merge is gated on `rec.Diverted == ""`.** A page that diverts returns a
   `PageProvenance` with every other field zero, so the marker is destroyed by exactly the
   divert it exists to explain. The carry-forward must sit outside that guard.

3. **`clonePageProvenance` (`editpages.go:205-217`) is a shallow `out := in`** with explicit
   deep copies per field. It needs one for the new pointer, or `BuildFromPages` drops the record
   on export. It has no test named for it, and nothing in the tree enumerates `PageProvenance`
   fields reflectively — a missed carry-forward fails silently.

### One asymmetry, left in on purpose

`clonePageProvenance` deep-copies the `Straightened` pointer; the carry-forward in
`RecordExtractionContext` assigns it directly. The two paths disagree, and that is deliberate
rather than an oversight.

The carry-forward's source, `old`, is read inside `RecordExtractionContext` and dropped when it
returns, so exactly one holder of the pointer survives the call and the aliasing cannot be
observed. A defensive copy there would be unreachable code that no test could exercise, which is
worse than the asymmetry. `clonePageProvenance` genuinely needs its copy, because
`BuildFromPages` can name one source page twice in a sequence and both output records would
otherwise share it — which is the reason that function's doc comment already gives for every
other field it copies.

If `old` ever arrives from a caller rather than from a local read, this becomes a real aliasing
bug and the copy must be added with it.

## 8. Letting a caller see the refusal coming

`ImageRef` gains one field:

```go
// PlacementDeg is Placement's rotation about the page's axes, in degrees,
// positive COUNTER-CLOCKWISE — atan2(b, a). It is the SIGNED angle, where
// skewDegrees (extract.go:71-75) is unsigned and cannot express a direction.
//
// It exists so a caller can see a straighten's consequence before asking for it:
// a correction of Deg leaves the placement at PlacementDeg + Deg, and
// placementReason (extract.go:800) diverts a page past maxSkewDeg = 2.0. The
// precedent is Substitutable — a caller that cannot see a refusal coming cannot
// drive the primitive (byb-js5.2).
PlacementDeg float64
```

This also closes a real gap. `internal/skew/skew.go:71-72` justifies its whole sign convention
by saying it matches "`ImageRef.Placement`'s `atan2(b, a)`" — a computation that **exists
nowhere in production**, only in `skew_probe_test.go`. Nothing would fail today if
`Placement`'s sign meaning drifted. Adding the field and pinning both sides against one fixture
makes the claimed agreement load-bearing, which is what byb-16j.4 asks for.

### The envelope rule, stated exactly

Straightening a page to read flat leaves the placement at exactly `-D`, where `D` is the
content skew — **independent of what the placement was before**. Verified by running
`content.Walk` on a wrapped page: the composed image CTM has `atan2(b, a) = t` exactly, at
t in {0.13, 1.7, 2.0, 2.0001, 2.5, 7.0}.

So the rule is not about the correction's magnitude:

> A page whose content skew exceeds `maxSkewDeg` leaves byblos's extraction envelope when it is
> straightened flat. That is 233 pages of 169,376 (0.83%), and 132 of them are one document.
> The other 99.17% stay inside it by construction, with no change to `extract.go`.

**byblos does not refuse the transform.** The rotation is valid PDF that every viewer draws
correctly, and it applies to pages that never extracted anyway — born-digital, JPX, multi-image.
Refusing would make the primitive's contract depend on an extractability it does not need. The
caller sees the consequence coming through `PlacementDeg`, and `Diverted` records it afterwards
through the existing mechanism.

`extract.go` is not touched: no classifier change, no constant moved. Two findings that came out
of this work are filed rather than fixed — see §11.

## 9. The shared fixture

byb-16j.4's real obligation: two implementations of one transform, in two languages, across two
repositories. A disagreement reads to a user as "it looked right in the editor", which is the
hardest class of bug to chase.

`testdata/straighten/contract.json` is the contract. Neither side owns it.

```json
{
  "convention": "PDF default user space: origin lower-left, y up, angles positive CCW",
  "tol": 1e-6,
  "cases": [
    {
      "name": "flat-page-1.7deg",
      "page":   {"cropbox": [0, 0, 612, 792], "rotate": 0},
      "apply":  {"deg": -1.7},
      "centre": [306, 396],
      "expect": {"cm": [0.9995599, -0.0296662, 0.0296662, 0.9995599, -11.6131499, 9.2521661]}
    },
    {
      "name": "rotate90-page-0.6deg",
      "page":   {"cropbox": [0, 0, 612, 792], "rotate": 90},
      "apply":  {"deg": -0.6},
      "centre": [306, 396],
      "expect": {"cm": [0.9999452, -0.0104718, 0.0104718, 0.9999452, -4.1300483, 3.2260789]}
    },
    {
      "name": "identity",
      "page":   {"cropbox": [0, 0, 612, 792], "rotate": 0},
      "apply":  {"deg": 0},
      "centre": [306, 396],
      "expect": {"cm": [1, 0, 0, 1, 0, 0]}
    }
  ]
}
```

The Go test reads it, applies the transform, and asserts the emitted `cm` term by term. A sign
flip then reports as `cm[1] expected -0.0296662, got +0.0296662` rather than as a pixel diff.

**The other side of this fixture does not exist yet, and the spec should not pretend otherwise.**
kleio is a headless JSON API with three queue workers and no frontend at all — no JavaScript, no
canvas, no client-side image code, and no editor in any of its design documents. The client the
editor will live in is outside that repository and outside this one.

So this is a commitment, not a description. Two things make it a cheap one to keep:

- Because kleio imports byblos as a Go module, a kleio Go test can read `contract.json` straight
  out of the module cache — byblos testdata ships in the module zip — so that half stays in sync
  for free with the version bump, with nothing to vendor.
- For the eventual JS client there is no sync mechanism today. Whoever builds it must copy this
  file and pin the byblos version it came from, and the file carries its own `convention` string
  for exactly that reason.

Two properties hold for every case and must be asserted separately from the literal numbers,
because they catch a whole class of error that a copied constant does not:

- **The centre is a fixed point.** Mapping `centre` through `cm` returns `centre` exactly. This
  fails immediately if the translation terms are derived for the wrong rotation centre.
- **`atan2(b, a)` reads back `deg`.** This is the same computation `ImageRef.PlacementDeg`
  performs (§8), so it ties the fixture to the sign convention rather than restating it.

The `rotate90` case exists because that is where the angle and the crop behave differently, and
where the frame confusion lives. Its `cm` is identical in form to the flat case — the angle
passes through `/Rotate` unchanged, which is the claim §3 makes and this pins.

`testdata/straighten/contract.pdf` is what byblos writes for the first case, so the fixture also
pins the file byblos produces and not only the arithmetic.

The cases must include a page carrying `/Rotate 90`, because that is where the angle and the
crop behave differently and where the frame confusion lives.

## 10. Tests

Red first, per case.

| test | pins |
|---|---|
| `TestStraightenIsLossless` | raster bytes byte-identical before and after |
| `TestStraightenWritesTheContractsMatrix` | the `cm` against `contract.json`, term by term |
| `TestStraightenRotatesAboutThePageCentre` | content stays centred; refutes origin-rotation |
| `TestStraightenComposesWithRotate` | a `/Rotate 90` page plus an angle |
| `TestStraightenIsAbsolute` | applying twice from the original is idempotent (byb-yul.4's redelivery) |
| `TestStraightenOnAlreadyStraightenedSourceAppliesTheDifference` | the enforced-absolute rule: `Deg` 3.2 over a source recording 1.0 applies 2.2 and records 3.2; over one recording 3.2 it applies 0.0 and changes the geometry not at all |
| `TestStraightenTreatsUnreadableProvenanceAsUnstraightened` | the safe default; a document byblos has never seen must not be treated as partly corrected |
| `TestStraightenRefusesUnbalancedContent` | the q/Q guard, both directions |
| `TestStraightenWrapsAContentsArray` | all four `/Contents` shapes |
| `TestStraightenValidates` | `api.Validate` accepts the output, and byblos reads back the geometry it wrote |
| `TestStraightenBoundsUnderARotatedCTM` | `content.Walk`'s `Bounds` on a rotated parent CTM |
| `TestStraightenRecordsTheAngle` | provenance, absolute, replaced not unioned |
| `TestStraightenSurvivesRecordExtraction` | the carry-forward, including the diverted page |
| `TestStraightenRefusesACrop` | non-nil `Crop` is refused in this version |
| `TestPlacementDegMatchesSkewConvention` | the two sides of the sign convention against one fixture |

**Watch the clip trap.** A page with an existing `W`/`W*` clip path or a form `/BBox` now has a
rotated parent CTM. byblos computed a clip early once before and it was harmless until something
read it, at which point it ate a stroke's spread and extracted a bad page. Assert `Bounds` on a
rotated page against a fixture; do not assume the walk follows.

## 11. The main spec must be edited, or the build breaks

`designspec_pin_test.go` compares the exported surface of the root package against
`docs/superpowers/specs/2026-07-27-byblos-design.md`. It fails loudly for a new exported struct
field or type, and **silently** for the rest. Every edit:

| main-spec edit | enforced by |
|---|---|
| `PageProvenance` gains `Straightened *PageStraighten` (spec §4, around line 581) | `designspec_pin_test.go:505` — **fails the build** |
| The `PageStraighten` type is declared beside `PageGeometry` | same — **fails the build** |
| The `StraightenSpec` type is declared beside `PageSource` (around line 528) | same — **fails the build** |
| `ImageRef` gains `PlacementDeg float64` | same — **fails the build** |
| The `PageSource` field-list comment at line 528-529 gains `Straighten` | **nothing.** See below |

The last row is the trap. Both sides of `PageSource` are type aliases
(`editpages.go:33`, spec line 528), and `exportedStructFields` skips any `TypeSpec` whose type
is not an `*ast.StructType` — so the pin never sees the fields at all. The spec records them in
a trailing *comment*, and comments are explicitly not compared. `packageGoFiles` also reads the
root directory only, so `internal/pdfdoc` is invisible to the pin entirely.

Adding a field to `pdfdoc.PageSource` therefore breaks no test and leaves the spec quietly
wrong. It must be updated by hand, and this row is here so that the next person does not have to
rediscover why.

## 12. Nothing here can reach kleio without a release

Validated 2026-08-14 against kleio at `2f67417`.

Kleio depends on byblos as a plain Go module, pinned at **v0.1.0** (`go.mod:16`), which is 42
commits behind this branch. `editpages.go` — `PageSource`, `BuildFromPages`, the whole seam this
spec extends — exists at neither `v0.1.0` nor `v0.2.0`. The contract is invisible to kleio until
byblos cuts a tag and kleio bumps to it.

That bump carries a small tax worth knowing about in advance: kleio's byblos spike documents
that a malformed last page makes `Inspect` error (`internal/pipeline/inspect.go:32-35`), and
`35fc1aa` made per-page failures non-fatal after `v0.2.0`. The comment goes stale on the same
bump. The spike is behind `KLEIO_BYBLOS_INSPECT`, default off, so this is contained.

Kleio imports exactly three byblos symbols — `Inspect`, `PageInfo.Index`, `PageInfo.Bounds` —
across two files, and **constructs no byblos struct anywhere**, keyed or unkeyed. Adding fields
to `PageSource`, `PageProvenance` or `ImageRef` therefore cannot break its build. The four
`cmd/byblos-*` binaries are measurement-only (byb-vv4) and reference none of these types either,
so no CLI contract moves.

## 13. Filed, not built

Both came out of testing whether a rotated placement survives the pipeline. It does — extract,
`Downsample`, `EncodeJBIG2Generic`, `ReplaceImages`, `Validate` and re-extract all pass at 0,
1.9, 3.2, 7 and 45 degrees, with extracted bytes byte-identical across every placement and an
identity re-encode rendering pixel-identical under poppler. What does not survive is byblos's
*description* of the page.

**AABB blindness — byb-2mt (fixed).** Every geometry consumer used to model a rotated footprint
as its axis-aligned bounding box. `inkHidden` (`extract.go:1004-1017`) judged ink in the corner
triangles hidden, so the page passed as clean and the ink was dropped from the returned raster —
measured at 2152 of 2500 dark pixels omitted at 10 degrees, with no divert and no error. `contains`
on a stack (`extract.go:750-768`) let a rotated top discard an under-layer whose own bounding box
(not its true shape) happened to sit inside the top's. Both were reachable at 2 degrees or less,
and under any sheared placement.

byb-2mt closed both, plus `PageRaster.CoversPage` and `PageGeometry.CoversPage`, by adding
`content.Quad` (`internal/content/walk.go`) — the placement's true quadrilateral, tested with the
same half-plane algorithm at every site, conjoined with the existing AABB test rather than
replacing it, and skipped entirely for an axis-aligned placement. The `covers` check inside the
same stacked-page arm was deliberately left AABB-only (see §4's revision above): unlike
`contains`, which asks whether one placement's ink sits inside another's, `covers` asks whether a
rotated placement's true shape reaches an axis-aligned page's corners, which byb-2mt's own fix for
`CoversPage` establishes it structurally cannot once rotated past roughly a tenth of a degree.
Fixing `covers` the same way would have diverted every rotated stack, including the
`stacked` corpus fixture this section's first paragraph measured surviving the pipeline.

**Shear and rotation are conflated — byb-06n (P2, no longer blocked now byb-2mt has landed).** `skewDegrees` returns
bit-identical `5.000000` for a
rigid 5-degree turn and for a 10-degree shear; `math.Abs` at `extract.go:72-73` destroys the
sign of `b` versus `c`, which is the only information separating them. They are separable by
column orthogonality — real rigid placements measure 1.86e-07 and 3.99e-05 degrees off
perpendicular, against 5.77 for the pinned shear fixture. Splitting them is **not**
admission-neutral, which is why it is gated: a matrix like `[1, 0, tan(1deg), 1]` has
`skewDegrees` 1.0 and shear 1.0, so it extracts today and would newly divert. That needs a
census over the full CTMs first — one `Inspect` pass, no image decoding — because the byb-16j.1
census stored `top_skew` and not the six numbers.

`maxSkewDeg` stays at 2.0. byb-b1.2 sized it against a measured 1.09-degree worst-case scanner
deskew, and that justification is about scanner deskew, not about correction headroom.
