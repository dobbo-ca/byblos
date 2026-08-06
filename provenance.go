package byblos

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"time"

	"github.com/dobbo-ca/byblos/internal/pdfdoc"
)

// Version is the Byblos semver recorded in every Provenance. It exists for
// humans and bug reports; upgrade decisions are driven by Capabilities, not by
// comparing versions (design spec section 6).
const Version = "0.2.0"

// Provenance is the record Byblos writes into a processed PDF, as JSON under a
// custom Info-dictionary key. The PDF is authoritative; any mirror of these
// fields in a caller's database is a cache.
type Provenance struct {
	Version      string           `json:"version"`
	Capabilities []string         `json:"capabilities"`
	ProcessedAt  time.Time        `json:"processed_at"`
	Pages        []PageProvenance `json:"pages"`

	// Optimized records which branch Optimize (byb-b5) took, because that
	// choice is a whole-document property, not a per-page one. Absent a
	// linearization request, Optimize returns min(input,
	// pdfcpu-rewritten-output): both are lossless structural rewrites of the
	// same document, so there is no quality tradeoff to record, only a
	// size-vs-linearization one. OptimizeOptions.Linearize:true suspends that
	// rule outright and always returns the linearized bytes, which are always
	// larger; see "rewritten-linearized" below. pdfcpu's rewrite
	// pass strips linearization rather than adding it (see
	// OptimizeOptions.Linearize's measurement), so the rewritten branch loses
	// whatever linearization the INPUT already had, silently -- the field
	// records that the rewrite ran, not that linearization was lost, but it is
	// the caller's only signal that this document did not simply pass
	// through untouched. The pass-through branch, by contrast, preserves
	// whatever linearization the input had, verbatim, because it does not
	// touch the bytes at all.
	//
	// "" is the zero value written by every build before byb-b5, AND what a
	// pass-through emits -- a pass-through returns the input's bytes
	// verbatim, so it cannot write anything into it without growing it past
	// that input and breaking Optimize's "never larger" guarantee. "" must
	// therefore be read as "not known to have been rewritten by Optimize",
	// never as "confirmed pass-through".
	//
	// "rewritten" means pdfcpu's optimized bytes were kept and the input was
	// not linearized, so nothing was lost to get them.
	//
	// "rewritten-delinearized" means the same, EXCEPT that the input carried a
	// linearization parameter dictionary and the output does not. That is a
	// real property traded for bytes, and it is the one case where the "no
	// quality tradeoff" claim above does not hold. It is recorded separately
	// rather than folded into "rewritten" because the two need different
	// answers: a caller that re-linearizes downstream can ignore the first and
	// must act on the second. Kleio's born-digital path exists precisely to
	// produce linearized files (its compress stage runs ocrmypdf in
	// linearize-only mode and does nothing else), so a later Optimize pass
	// quietly undoing that is a regression byblos must not hide.
	//
	// "rewritten-linearized" means Optimize was asked to linearize and did:
	// the output carries Annex F structure byblos itself wrote (byb-1y7,
	// internal/linearize). It is the only value that asserts a POSITIVE
	// property of the output rather than merely naming a branch, which is why
	// capabilityRules keys its upgrade rule on it (upgrade.go).
	//
	// "rewritten-delinearized" SURVIVES byb-1y7 and stays reachable; it is not
	// superseded by "rewritten-linearized". The two are mutually exclusive by
	// construction and describe different calls, not different eras: they are
	// the Linearize:false and Linearize:true branches over the same linearized
	// input. Optimize cannot infer that a caller who did not ask to linearize
	// wanted it, so the delinearizing branch still exists and must still say so.
	// Deleting the value would silence the exact regression it was added to
	// make visible, on the exact documents Kleio cares about.
	//
	// Reserve, do not yet emit, "passed-through".
	//
	// The converse also holds and is just as important: a pass-through run
	// carries whatever record the input already had forward untouched, so
	// "rewritten" on a document that has since had a pass-through Optimize
	// run on it is stale -- it describes the most recent REWRITE, not the
	// most recent call to Optimize. Nothing clears it, for the same reason
	// "" cannot be written by a pass-through: doing so would require an
	// in-band write to bytes that must stay byte-identical to the input.
	Optimized string `json:"optimized,omitempty"`
}

// PageProvenance records what one page actually received.
//
// Applied entries are capability names, optionally carrying a numeric
// parameter as a "-N" suffix, e.g. "downsample-150".
//
// Diverted is the reason a page was not processed, e.g. "not-single-raster",
// and is empty when the page was handled normally. It is coarser than
// classify's full reason vocabulary (extract.go) — the record only needs to
// be precise enough to answer "would re-processing help, and with what
// capability?" — but not maximally coarse: byb-z8j split the codec case into
// "unsupported-codec-jbig2"/"-jpx"/"-tiff" so a decoder capability can be
// nominated for only the pages that want it, rather than every codec-diverted
// page. The full classify vocabulary still goes to the divert counters, not
// here.
//
// A document written before byb-z8j, or one whose codec this build could not
// name, carries the older coarse "unsupported-codec" instead of one of the
// three above. That string must keep matching every decode-* rule
// indefinitely, because nothing can tell after the fact which codec it held.
//
// divertClass in extract.go is the one place that maps the fine reason to the
// value stored here, and capabilityRules in upgrade.go is what matches on it.
// The two must agree; TestDivertClassCoversEveryReason is the tripwire.
//
// Placement, when present, is the affine the page's raster was painted with, in
// PDF matrix order [a b c d e f] — the same six numbers as ImageRef.Placement.
// It is recorded, not applied: a scanner's deskew rotation stays in the
// placement matrix and the raster is kept as stored, because resampling a
// bilevel raster to straighten it would break the lossless guarantee this
// library exists for (byb-b1.2). It is empty for an axis-aligned placement,
// which is almost every page.
//
// DroppedAnnots is how many annotations painted on this page and are not in the
// raster that was stored. Like Placement it is recorded, not applied: the
// appearance streams are still in the PDF, and drawing them into the raster
// would be a renderer (design spec section 2). What the record carries is the
// fact that the stored image is not the whole of what a reader would see, so a
// later pass can find those pages without re-measuring the archive. It is zero
// for almost every page — byb-b1.11 measured 6 in 18,610 extracted — and
// omitted when zero, so the ordinary record does not grow.
//
// Geometry, when present, is the raster and page boxes measured at write time
// (byb-b5.1). Placement deliberately stays a separate top-level field rather
// than moving inside PageGeometry: lifting an old record's top-level placement
// into the container would have to invent boxes for it, and a Geometry
// carrying a [0 0 0 0] page box is a wrong geometry stated confidently. The two
// fields have two separate presence bits because they have two separate
// histories. It is nil for every record this build did not measure and for
// every record written before byb-b5.1 -- see PageGeometry's doc comment for
// why that nilness must survive.
type PageProvenance struct {
	Applied       []string      `json:"applied,omitempty"`
	Diverted      string        `json:"diverted,omitempty"`
	Placement     []float64     `json:"placement,omitempty"`
	DroppedAnnots int           `json:"dropped_annots,omitempty"`
	Geometry      *PageGeometry `json:"geometry,omitempty"`
}

// PageGeometry is a page's raster and page boxes as measured at write time,
// each [llx lly urx ury] in PDF default user space: points, origin at the
// lower-left corner, y increasing upward (ISO 32000-1 8.3). This is NOT the
// [a b c d e f] affine matrix order PageProvenance.Placement uses -- a box is
// two corners, a placement is a transform, and conflating their orderings
// would silently misread one as the other.
//
// Geometry is a pointer, not a value, and must stay one. nil means "this
// build recorded no geometry" -- what every pre-byb-b5.1 record deserializes
// to -- and must never be read as "the raster covers the page". A non-nil
// Geometry with a zero box is a real, if degenerate, measurement, and JSON can
// tell the two apart because a pointer either marshals as the object or is
// omitted entirely.
//
// Go 1.24's `omitzero` is deliberately refused here even though go.mod (go
// 1.26.4) has it available: omitzero on a value-typed PageGeometry would make
// a measured all-zero box and a never-measured field serialize identically,
// destroying exactly the distinction the paragraph above depends on. The
// pointer is the backward-compatibility story; do not "tidy" it away.
// TestPageProvenanceGeometryZeroValueIsNotOmitted is the tripwire.
//
// This exists for the case byb-b1.3 measured: a raster placed at its own
// resolution on a nominal page box, axis-aligned (so Placement is empty for
// it), that does not fill the box. 132 such pages were measured; one of them
// is 2384x3321 px at 302 DPI, which is 568.37 x 791.76 pt on a 612x792
// MediaBox -- 92.84% of the box (92.87% by width), a 43.6 pt blank strip the
// raster does not cover. Without Geometry, a later re-processing run has no
// way to tell that page apart from a full-page scan.
//
// Sourcing note for whoever writes this record: PageRaster.Bounds and
// PageRaster.Page are image.Rectangle, built through round() (inspect.go:92-
// 100) -- i.e. points ROUNDED TO INTEGERS. A writer populating PageGeometry
// must source the unrounded floats -- content.Placement.Box and pdfdoc
// Page.CropBox -- not PageRaster. 568.3708 is not 568.
//
// CoversPage below applies coverTolerancePt (extract.go:49, == 1.0) over these
// exact floats. The live PageRaster.CoversPage applies NO tolerance of its own
// -- it is a plain image.Rectangle.In test on rounded integer rectangles; the
// 1.0pt allowance belongs to contains() (extract.go), a different
// function. That means the two CAN disagree, for a shortfall in the (0.5,
// 1.0] pt band: PageRaster.CoversPage sees no cover (rounding already ate the
// sub-pixel slack, containment fails outright), while this one's tolerance
// still calls it covered. That fork is deliberate -- documented here, not
// left to be discovered later.
//
// RasterBox and PageBox are both mandatory whenever Geometry is non-nil: a
// writer that measures one must measure the other, and a decoder that gets a
// short JSON array for either silently zero-fills the missing elements rather
// than erroring (a wrongly-typed value, e.g. a string, does still error),
// which would misrepresent a partial record as a real (if
// degenerate) measurement. Byblos's own writer always sets both together.
// The same short-array hazard applies to ClipBox, once its presence bit says
// it is there at all: a short clip_box array still zero-fills rather than
// erroring, reading as a real (if degenerate) measured clip.
//
// ClipBox is the third box, added by byb-b1.12 once content.Walk started
// honouring form /BBox and clip paths. It carries its OWN presence bit --
// *[4]float64, not a bare [4]float64 -- for exactly the reason the paragraph
// above warns about: a value-typed box would zero-fill on every record
// written before it existed, and a zero box inside a non-nil Geometry already
// means "measured, and the measurement was degenerate" (the paragraph above
// this one), so it would make every byb-b5.1-era record lie that it measured
// a [0 0 0 0] clip.
//
// It is populated only when a clip actually narrowed the placement below its
// unclipped raster box (extract.go compares Placement.Box against
// Placement.CTM.UnitSquareBox(), the placement's own extent before any clip)
// -- not whenever a clip merely happened to be in effect. A clip landing
// exactly on the raster's own edge (a form BBox sized to match its image,
// say) narrows nothing, so it records no ClipBox, the same as a page with no
// clip at all: this field answers "did a clip change what shows", not "was a
// clip present". A page with no narrowing clip in effect leaves ClipBox nil,
// the same way Geometry itself is nil for a page a build never measured.
//
// There is no honest value for "unbounded, nothing clipped this" that fits
// inside a plain [4]float64 the way RasterBox and PageBox do -- any box-shaped
// value collides with a real, if degenerate, measurement (the paragraph
// above). That is the reason ClipBox needs its own presence bit rather than
// being inferred by comparing RasterBox to something.
type PageGeometry struct {
	RasterBox [4]float64  `json:"raster_box"`
	PageBox   [4]float64  `json:"page_box"`
	ClipBox   *[4]float64 `json:"clip_box,omitempty"`
}

// CoversPage reports whether RasterBox fills PageBox, within coverTolerancePt.
// It mirrors PageRaster.CoversPage's guard against a degenerate box: a PageBox
// with zero or negative width or height -- including one with its corners
// swapped -- is covered by nothing, because a plain containment check would
// otherwise answer true for an empty or inverted box (image.Rectangle.In does,
// for an empty receiver), which would make an unmeasured or degenerate record
// report itself as a full-page scan.
func (g PageGeometry) CoversPage() bool {
	if g.PageBox[2] <= g.PageBox[0] || g.PageBox[3] <= g.PageBox[1] {
		return false
	}
	return g.RasterBox[0] <= g.PageBox[0]+coverTolerancePt &&
		g.RasterBox[1] <= g.PageBox[1]+coverTolerancePt &&
		g.RasterBox[2] >= g.PageBox[2]-coverTolerancePt &&
		g.RasterBox[3] >= g.PageBox[3]-coverTolerancePt
}

// buildCapabilities is what this build of Byblos can do. Every entry MUST also
// have a rule in capabilityRules (upgrade.go); TestEveryCapabilityHasARule
// enforces that.
//
// Append to this list as each epic lands. Do not remove entries: a capability
// string is a permanent identifier that older documents' provenance refers to.
var buildCapabilities = []string{
	"build-pdf",
	"downsample",
	"extract-raster",
	"inspect",
	"jbig2-generic",
	"jpeg-recompress",
	"linearize",
	"quantize-png",
	"text-layer",
}

// Capabilities returns, sorted, what this build can do.
func Capabilities() []string {
	out := slices.Clone(buildCapabilities)
	slices.Sort(out)
	return out
}

// provenanceKey is the Info-dictionary key a Provenance record is stored
// under, as JSON (design spec section 6). It is a constant, not a literal at
// each call site, because ReadProvenance must keep reading it indefinitely --
// even after some future release moves the write side to XMP, an old document
// written under this key has to keep giving back its record.
const provenanceKey = "byblos-provenance"

// errCorruptProvenance marks a value under provenanceKey that failed to
// unmarshal as JSON, distinct from a pdfdoc/pdfcpu-level read failure.
// Optimize (byb-b5) treats this as "no provenance" rather than fatal: an
// otherwise-valid PDF should not fail to optimize merely because whatever
// wrote this key put garbage under it.
var errCorruptProvenance = errors.New("byblos: corrupt provenance value")

// WriteProvenance marshals p to JSON and stores it under provenanceKey in
// r's Info dictionary, via pdfdoc.WriteProperties (pdfcpu's api.AddProperties
// underneath). It needs only an Info dictionary to exist, not an extraction
// outcome or a text layer -- see byb-0dz, which split this half of B5 off
// byb-b1/byb-b4 for exactly that reason.
//
// It cannot be cancelled. Use WriteProvenanceContext when the caller has a
// deadline.
func WriteProvenance(r io.ReadSeeker, w io.Writer, p Provenance) error {
	return WriteProvenanceContext(context.Background(), r, w, p)
}

// WriteProvenanceContext is WriteProvenance, cancellable only before its work
// begins (byb-xyn).
//
// CANCELLATION LATENCY: A WHOLE PDFCPU ROUND TRIP. Writing provenance is one
// indivisible read-validate-optimize-write pass through pdfcpu, which is not
// context-aware; there is no loop boundary byblos owns inside it, so the
// context is consulted once on entry and not again. The parameter exists so a
// caller can decline to START the work, and so this primitive composes with
// the other eight rather than being the one that silently takes no context.
// A cancelled call writes nothing to w. See context.go.
func WriteProvenanceContext(ctx context.Context, r io.ReadSeeker, w io.Writer, p Provenance) error {
	if err := checkContext(ctx); err != nil {
		return err
	}
	data, err := json.Marshal(p)
	if err != nil {
		return fmt.Errorf("byblos: marshal provenance: %w", err)
	}
	if err := pdfdoc.WriteProperties(r, w, map[string]string{provenanceKey: string(data)}); err != nil {
		return fmt.Errorf("byblos: write provenance: %w", err)
	}
	return nil
}

// RecordExtraction runs extraction over every page of r and returns the record
// of what happened, ready for WriteProvenance. Pages is exactly one entry per
// page, in page order, so index i describes page i+1 -- PageProvenance carries
// no page number of its own.
//
// It is the only producer of PageGeometry: the unrounded placement and CropBox
// floats live inside the extraction path and nothing else can see them.
//
// A page Byblos could not READ at all aborts the whole call. There is no
// "failed" value in PageProvenance's vocabulary, and the two ways to carry on
// are both worse: skipping the page shifts every later page's index, and
// appending a zero PageProvenance is indistinguishable to any reader from a
// page that was handled and had nothing applied. A caller that needs per-page
// resilience has ExtractPageRaster.
//
// Capabilities claims only "extract-raster", for the reason Optimize's fresh
// record claims nothing (optimize.go): this call did not build, encode, stamp
// or linearize anything, and a record saying otherwise would suppress those
// capabilities in UpgradeCandidates. A caller that goes on to do more appends
// to both Capabilities and the pages' Applied.
//
// It reads back whatever record r already carries, the same way Optimize does
// (optimize.go), and merges into it rather than overwriting it: Optimized is
// preserved verbatim (extraction is not a rewrite and has no opinion on it),
// Capabilities is the union of the old record's and this call's, and each
// page's Applied is the union of the old page's and this call's. Without this,
// running RecordExtraction over a document Optimize (or an earlier
// RecordExtraction) already processed would silently erase real capabilities
// -- including "rewritten-linearized", which a later re-run without
// Linearize:true would then falsely nominate for reprocessing. A record under
// provenanceKey that fails to parse as JSON is treated as no record at all,
// matching ReadProvenance/Optimize's errCorruptProvenance handling.
//
// It cannot be cancelled. Use RecordExtractionContext when the caller has a
// deadline.
func RecordExtraction(r io.ReadSeeker) (Provenance, error) {
	return RecordExtractionContext(context.Background(), r)
}

// RecordExtractionContext is RecordExtraction, cancellable at each page
// boundary (byb-xyn).
//
// This is the primitive the context convention is really for. It runs
// extraction over EVERY page, so it is both the most expensive entry point in
// the package and the one where a per-page check buys the most: on a long
// document it is the difference between a worker returning in one page's time
// and a worker held until the SQS visibility timeout redelivers the same file.
//
// CANCELLATION LATENCY: one page's extraction -- open, walk, classify, decode
// -- which is the same indivisible unit ExtractPageRasterContext documents,
// bounded by byb-riy's decoder budget and not by this context. The initial
// ReadProvenance and pdfdoc.Open are single uninterruptible pdfcpu passes; a
// context cancelled during those is not noticed until the page loop is
// reached.
//
// WHAT ONE PAGE COSTS, measured, because this is the number a caller has to
// budget for and the reassuring one is misleading:
//
//	ordinary 300-dpi JPEG scans, 120 pages:  12 ms   (3.2% of the call)
//	hostile JBIG2 admitted by byb-riy:        seconds per page
//
// The second number is the contract. byb-riy's budget
// admits a page of 67,092,481 pixels, and decoding one costs seconds, so
// "cancellation stops this within a page" is only a useful promise to a caller
// who has budgeted SECONDS for that page -- not the milliseconds an ordinary
// document suggests. A kleio worker whose SQS visibility timeout is shorter
// than that will still be redelivered onto the same document, which is the
// exact failure this bead exists to prevent, so the timeout has to be set
// against the hostile number. See TestCancellationLatencyOnAHostilePage and
// context.go.
func RecordExtractionContext(ctx context.Context, r io.ReadSeeker) (Provenance, error) {
	if err := checkContext(ctx); err != nil {
		return Provenance{}, err
	}
	old, err := ReadProvenanceContext(ctx, r)
	if err != nil {
		if !errors.Is(err, errCorruptProvenance) {
			return Provenance{}, err
		}
		old = nil
	}
	if _, err := r.Seek(0, io.SeekStart); err != nil {
		return Provenance{}, fmt.Errorf("byblos: provenance: seek: %w", err)
	}
	d, err := pdfdoc.Open(r)
	if err != nil {
		return Provenance{}, err
	}
	p := Provenance{
		Version:      Version,
		Capabilities: []string{"extract-raster"},
		ProcessedAt:  time.Now(),
		Pages:        make([]PageProvenance, 0, d.PageCount()),
	}
	if old != nil {
		p.Optimized = old.Optimized
		p.Capabilities = unionSorted(old.Capabilities, p.Capabilities)
	}
	for n := 1; n <= d.PageCount(); n++ {
		if err := checkContext(ctx); err != nil {
			return Provenance{}, err
		}
		_, rec, err := extractPage(d, n)
		if err != nil && rec.Diverted == "" {
			return Provenance{}, fmt.Errorf("byblos: provenance: page %d: %w", n, err)
		}
		if old != nil && rec.Diverted == "" && n-1 < len(old.Pages) {
			rec.Applied = unionSorted(old.Pages[n-1].Applied, rec.Applied)
		}
		p.Pages = append(p.Pages, rec)
	}
	return p, nil
}

// unionSorted returns the sorted, deduplicated union of a and b.
func unionSorted(a, b []string) []string {
	out := slices.Clone(a)
	out = append(out, b...)
	slices.Sort(out)
	return slices.Compact(out)
}

// ReadProvenance reads back the record WriteProvenance stored under
// provenanceKey, via pdfdoc.ReadProperties (pdfcpu's api.Properties
// underneath). It returns (nil, nil) for a document no Byblos build has
// processed -- absence is not an error, and UpgradeCandidates already treats a
// nil *Provenance as "every capability is a candidate", so callers need no
// special case for a never-seen file.
//
// It cannot be cancelled. Use ReadProvenanceContext when the caller has a
// deadline.
func ReadProvenance(r io.ReadSeeker) (*Provenance, error) {
	return ReadProvenanceContext(context.Background(), r)
}

// ReadProvenanceContext is ReadProvenance, cancellable only before its work
// begins (byb-xyn).
//
// CANCELLATION LATENCY: A WHOLE PDFCPU READ. Reading the Info dictionary means
// pdfcpu parsing the document, which is not interruptible and has no
// byblos-owned loop inside it, so the context is consulted once on entry and
// not again. It is the cheapest of the nine, but "cheapest" is a property of
// the document, not a bound this context provides. See context.go.
func ReadProvenanceContext(ctx context.Context, r io.ReadSeeker) (*Provenance, error) {
	if err := checkContext(ctx); err != nil {
		return nil, err
	}
	props, err := pdfdoc.ReadProperties(r)
	if err != nil {
		return nil, fmt.Errorf("byblos: read provenance: %w", err)
	}
	raw, ok := props[provenanceKey]
	if !ok {
		return nil, nil
	}
	var p Provenance
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		return nil, fmt.Errorf("%w: %w", errCorruptProvenance, err)
	}
	return &p, nil
}
