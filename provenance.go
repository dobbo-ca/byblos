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
type PageProvenance struct {
	Applied       []string  `json:"applied,omitempty"`
	Diverted      string    `json:"diverted,omitempty"`
	Placement     []float64 `json:"placement,omitempty"`
	DroppedAnnots int       `json:"dropped_annots,omitempty"`
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
	"jbig2-generic",
	"linearize",
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
