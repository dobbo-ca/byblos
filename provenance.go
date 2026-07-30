package byblos

import (
	"slices"
	"time"
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
}

// PageProvenance records what one page actually received.
//
// Applied entries are capability names, optionally carrying a numeric
// parameter as a "-N" suffix, e.g. "downsample-150".
//
// Diverted is the coarse reason a page was not processed, e.g.
// "not-single-raster", and is empty when the page was handled normally. The
// fine-grained reason (see classify in extract.go) goes to the divert counters,
// not into the stored record: the record only needs to be precise enough to
// answer "would re-processing help?".
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
}

// Capabilities returns, sorted, what this build can do.
func Capabilities() []string {
	out := slices.Clone(buildCapabilities)
	slices.Sort(out)
	return out
}
