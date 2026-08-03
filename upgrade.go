package byblos

import (
	"slices"
	"strings"
)

// capabilityRules maps a capability name to the condition under which *gaining*
// it would change the output for a document with the given recorded provenance.
//
// This is the heart of goal G3. A capability may have a rule long before it has
// an implementation — the rules for the FUTURE.md capabilities are here so that
// shipping one of them requires no change to this table.
var capabilityRules = map[string]func(*Provenance) bool{
	// Inspection and extraction do not alter output.
	"inspect":        never,
	"extract-raster": never,

	// Construction happens before a document exists. No recorded provenance can
	// describe a document that would be improved by gaining it — the file was
	// already built by something, and rebuilding it from images Byblos no
	// longer holds is not an upgrade path.
	"build-pdf": never,

	// A document that already got jbig2-generic cannot benefit from gaining it.
	// A document that got ccitt-g4 can: same losslessness, better ratio.
	"jbig2-generic": anyPageApplied("ccitt-g4"),

	// The intended next capability. Its upgrade set is exactly the pages that
	// recorded jbig2-generic (FUTURE.md).
	"jbig2-symbol": anyPageApplied("jbig2-generic"),

	// A compatibility fallback with strictly worse compression. Gaining it never
	// improves a document; it is only ever chosen deliberately (FUTURE.md).
	"ccitt-g4": never,

	// A renderer turns pages Byblos understood but could not reduce to one
	// page-covering raster into processable ones, and nothing else: tiled
	// rasters, image-plus-vector-overlay, mixed content (FUTURE.md).
	//
	// "unsupported-codec" is deliberately NOT here (byb-97q). That class means
	// the page was understood and its raster could not be *decoded*. A renderer
	// cannot read those bytes either; a decoder can. Including it recommended
	// the largest piece of work in the Cadmus/Byblos family for up to 91% of
	// pages on an archive-shaped corpus.
	//
	// byb-b5.1: a partial placement (PageGeometry.CoversPage false, or a
	// non-empty Placement) is deliberately NOT a term in this rule either,
	// decided explicitly rather than by omission:
	//
	//   - Old records keep nominating render, because divertClass still maps
	//     "not-page-covering" to "not-single-raster" (divertClass, extract.go, pinned
	//     by extract_test.go:284-287) -- new records already stopped doing
	//     that at byb-b1.3, which retired the "not-page-covering" divert
	//     reason entirely (extract_test.go:283-286), before Geometry existed.
	//     A future record carrying Geometry still gets no coverage term here.
	//   - The volume argument: 63.7% of extracted pages carry
	//     covers_page=false -- 14,046 of the 22,050 extracted across the same
	//     166,423-page sample run tools/sample/README.md documents (that file
	//     records page and extracted counts, not a covers_page breakdown; the
	//     14,046/13,439 figures below are this rule's own measurement over
	//     that run, not a quote from README), and concentrated in the ia leg,
	//     which is 13,439 of 14,943 (89.9%) because those are the natural-DPI
	//     scans byb-b1.3 was built for; govdocs1 is 14.3% and dc 2.4%. Against
	//     the ~91% of pages that got byb-97q filed for exactly this reason, a
	//     coverage-keyed rule repeats byb-97q at the same magnitude.
	//     (byb-b1.11's close text quotes 13,963 of 18,610 = 75% for this
	//     ratio; that is the earlier, smaller run. The figures here are
	//     recomputed from the same 166,423-page results the 7 below comes
	//     from, so both sides of this comparison share a denominator.)
	//   - The gain is zero: extract.go's ExtractPageRaster doc comment records
	//     that every other arm of classify has already established no
	//     text/path/shading/inline-image/unresolved-XObject marks the page, and
	//     that on all 132 measured pages the region outside the placement held
	//     ZERO content operators. A renderer emits the same pixels plus
	//     synthesized white -- and extract.go explicitly refuses to synthesize.
	//   - byb-b1.12 narrowed, but did not remove, the argument against a
	//     coverage rule: content.Walk now honours W/W* clip paths and form
	//     /BBox, so a placement narrowed by either already reports
	//     CoversPage false, not true. What Walk still does not fold into
	//     gs.clip is a text clipping mode (Tr 4-7, ISO 32000-1 9.3.6, Walk's
	//     doc comment) -- a residual case where the error still runs toward
	//     CoversPage reporting TRUE over ink a reader could not actually see.
	//     Pages hidden that way are still ones a coverage rule would SKIP
	//     nominating. Coverage remains unsound as a proxy for loss in exactly
	//     the direction that matters; byb-b1.12 shrank the gap, it did not
	//     close it.
	//   - DroppedAnnots, by contrast, IS included below: extract.go says
	//     outright that rendering appearance streams into the raster would be
	//     a renderer, so gaining render demonstrably changes the output for
	//     those pages -- the literal condition capabilityRules tests. Its cost
	//     profile is the inverse of coverage's, on one denominator: 7 of
	//     22,050 extracted pages (~0.03%) versus 14,046 of the same 22,050
	//     (63.7%). The conservative bias is affordable here, unaffordable
	//     there.
	//   - DroppedAnnots is only counted on ExtractPageRaster's success path
	//     (the d.Annots(page) loop in extractPage, extract.go): a diverted
	//     page's raster was never read, so
	//     its annotations were never counted either, and DroppedAnnots stays
	//     0. A page carrying both a non-empty Diverted and DroppedAnnots > 0
	//     is not a shape this build's writer produces; the arm below does not
	//     guard against it separately.
	"render": func(p *Provenance) bool {
		for _, pg := range p.Pages {
			if pg.DroppedAnnots > 0 {
				return true
			}
		}
		return anyPageDiverted("not-single-raster")(p)
	},

	// Raster decoders. One capability per codec, because "which documents want
	// a JPX decoder" and "which want a JBIG2 decoder" are different questions
	// with very different answers by corpus — jpx dominates web-archive scans,
	// jbig2 dominates document-conversion output.
	//
	// byb-z8j gave divertClass (extract.go) a codec-specific class per codec,
	// so each rule now also keys on its own "unsupported-codec-<codec>" class
	// and nominates only the pages that actually want that decoder. The coarse
	// "unsupported-codec" stays in every rule below: a document a pre-byb-z8j
	// build wrote, or one whose codec this build could not name (RawImage's
	// ErrUnsupportedCodec path — pdfcpu gives up before naming a file type),
	// carries only the coarse class, and nothing can tell after the fact which
	// codec it held. Conservative bias unchanged — a wasted re-run beats a
	// hidden upgrade — it now only costs that on the pages the fine reason
	// could not reach.
	//
	// The names are "decode-<codec>" and not "<codec>-decode" on purpose:
	// "jbig2-decode" would match the "jbig2-" prefix that page-cleanup below
	// tests for.
	"decode-jbig2": anyPageDiverted("unsupported-codec", "unsupported-codec-jbig2"),
	"decode-jpx":   anyPageDiverted("unsupported-codec", "unsupported-codec-jpx"),
	"decode-tiff":  anyPageDiverted("unsupported-codec", "unsupported-codec-tiff"),

	// Output-side capabilities: gaining one does not improve a document that was
	// already processed with it, and we cannot tell from the record whether a
	// page that missed it had content it would have helped. Conservatively
	// never - a false positive here means re-processing the whole archive.
	"quantize-png":    never,
	"downsample":      never,
	"jpeg-recompress": never,
	"text-layer":      never,

	// Linearization is a whole-file property, so its rule reads
	// Provenance.Optimized rather than any page's record. "never" was right
	// while byblos could not linearize at all; now that it can, a document
	// whose record does not show linearization WOULD come out different if it
	// were reprocessed, and saying otherwise would hide a real upgrade.
	//
	// The judgement call, stated rather than buried: provenance records no
	// "the caller asked for linearization" flag, so this also nominates
	// documents whose caller deliberately chose not to linearize. That is the
	// conservative direction and the one the rest of this table takes -- a
	// wasted re-run beats a hidden upgrade.
	"linearize": func(p *Provenance) bool { return p.Optimized != "rewritten-linearized" },

	// Despeckling and border removal apply to any page whose raster Byblos
	// actually handled. Every prefix ends in "-" so that a future capability
	// whose name merely starts with one of these words does not match.
	"page-cleanup": anyPageAppliedPrefix("jbig2-", "ccitt-", "quantize-", "downsample-", "jpeg-"),

	// PDF/A conformance is a property of the whole file, so any document can be
	// converted.
	"pdfa": always,
}

func never(*Provenance) bool  { return false }
func always(*Provenance) bool { return true }

func anyPageApplied(want string) func(*Provenance) bool {
	return func(p *Provenance) bool {
		for _, pg := range p.Pages {
			for _, a := range pg.Applied {
				if appliedCapability(a) == want {
					return true
				}
			}
		}
		return false
	}
}

func anyPageAppliedPrefix(prefixes ...string) func(*Provenance) bool {
	return func(p *Provenance) bool {
		for _, pg := range p.Pages {
			for _, a := range pg.Applied {
				for _, pre := range prefixes {
					if strings.HasPrefix(a, pre) {
						return true
					}
				}
			}
		}
		return false
	}
}

func anyPageDiverted(reasons ...string) func(*Provenance) bool {
	return func(p *Provenance) bool {
		for _, pg := range p.Pages {
			if slices.Contains(reasons, pg.Diverted) {
				return true
			}
		}
		return false
	}
}

// appliedCapability strips a trailing numeric parameter from an Applied entry:
// "downsample-150" is the capability "downsample" applied at 150 DPI, while
// "jbig2-generic" is a capability name in its own right. Only an all-digit
// final segment is treated as a parameter.
func appliedCapability(s string) string {
	i := strings.LastIndexByte(s, '-')
	if i < 0 || i == len(s)-1 {
		return s
	}
	for _, r := range s[i+1:] {
		if r < '0' || r > '9' {
			return s
		}
	}
	return s[:i]
}

// UpgradeCandidates returns, sorted, the capabilities in current that the
// document described by p does not already have AND that would actually change
// its output. An empty result means re-processing the document is wasted work.
//
// A capability with no rule is reported: missing a real upgrade is worse than
// one wasted re-run, and TestEveryCapabilityHasARule keeps the gap from
// persisting. A nil p means nothing is known about the document, so every
// capability is a candidate.
func UpgradeCandidates(p *Provenance, current []string) []string {
	if p == nil {
		out := slices.Clone(current)
		slices.Sort(out)
		return slices.Compact(out)
	}
	var out []string
	for _, c := range current {
		if slices.Contains(p.Capabilities, c) {
			continue
		}
		rule, ok := capabilityRules[c]
		if !ok || rule(p) {
			out = append(out, c)
		}
	}
	slices.Sort(out)
	return slices.Compact(out)
}
