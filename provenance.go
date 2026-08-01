package byblos

import (
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
const Version = "0.1.0"

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
//
// ADDING A THIRD BOX LATER: it must carry its OWN presence bit -- *[4]float64,
// or a sibling bool -- and must not be a bare [4]float64 like these two.
// Geometry's nil-ness is the presence bit for the pair above and for nothing
// else, so a value-typed box added now would zero-fill on every record written
// before it existed, and by the rule two paragraphs up a zero box inside a
// non-nil Geometry reads as a real degenerate measurement rather than as an
// absent one. The pair above escape this only because they are the reason
// Geometry becomes non-nil in the first place. byb-b1.12 is the live case:
// once content.Walk honours form /BBox and clip paths it will want to record
// the clip box here, and every byb-b5.1-era record would otherwise claim it
// measured a [0 0 0 0] clip.
type PageGeometry struct {
	RasterBox [4]float64 `json:"raster_box"`
	PageBox   [4]float64 `json:"page_box"`
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
func WriteProvenance(r io.ReadSeeker, w io.Writer, p Provenance) error {
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
func RecordExtraction(r io.ReadSeeker) (Provenance, error) {
	old, err := ReadProvenance(r)
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
func ReadProvenance(r io.ReadSeeker) (*Provenance, error) {
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
