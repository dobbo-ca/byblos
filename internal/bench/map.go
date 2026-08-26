// Package bench measures what each shipped byblos capability costs, so that a
// change can be scored against a committed baseline rather than argued about.
//
// See docs/superpowers/specs/2026-08-11-bench-map-design.md.
package bench

// Target names the benchmark that measures one capability's cost.
//
// Weights are NOT stored here. A capability's weight is its measured share of
// the objective (spec section 5.2), so storing one would be storing a stale
// copy of something the harness recomputes. What is stored is which slice of
// the bench set exercises the capability, and an optional hand multiplier that
// must state its reason.
type Target struct {
	// Capability must appear in byblos.Capabilities().
	Capability string

	// Entry names the exported call exercised, for the reader. It is
	// documentation, not dispatch: dispatch lives in cases (case.go), keyed on
	// Capability.
	Entry string

	// Corpus names the slice of the bench set this capability is fed. "all"
	// means every document; a case that cannot use a given document reports
	// ErrIneligible and is skipped for it.
	Corpus string

	// Override multiplies the measured weight. 1.0 means no override.
	Override float64

	// Why is required when Override != 1.0 and forbidden otherwise.
	Why string
}

// Targets is one entry per capability in byblos.Capabilities().
// TestEveryCapabilityHasATarget enforces that.
var Targets = []Target{
	{Capability: "inspect", Entry: "Inspect", Corpus: "all", Override: 1.0},
	{Capability: "extract-raster", Entry: "ExtractPageRaster", Corpus: "all", Override: 1.0},
	{Capability: "build-pdf", Entry: "BuildPDF", Corpus: "all", Override: 1.0},
	{Capability: "jbig2-generic", Entry: "EncodeJBIG2Generic", Corpus: "all", Override: 1.0},
	{Capability: "quantize-png", Entry: "QuantizePNG", Corpus: "all", Override: 1.0},
	{Capability: "downsample", Entry: "Downsample", Corpus: "all", Override: 1.0},
	{Capability: "jpeg-recompress", Entry: "Optimize(RecompressJPEG)", Corpus: "all", Override: 1.0},
	{Capability: "linearize", Entry: "Optimize(Linearize)", Corpus: "all", Override: 1.0},
	{Capability: "text-layer", Entry: "StampTextLayer", Corpus: "all", Override: 1.0},
}

// TargetFor returns the target for a capability string.
func TargetFor(capability string) (Target, bool) {
	for _, t := range Targets {
		if t.Capability == capability {
			return t, true
		}
	}
	return Target{}, false
}
