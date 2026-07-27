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

	// A document that already got jbig2-generic cannot benefit from gaining it.
	// A document that got ccitt-g4 can: same losslessness, better ratio.
	"jbig2-generic": anyPageApplied("ccitt-g4"),

	// The intended next capability. Its upgrade set is exactly the pages that
	// recorded jbig2-generic (FUTURE.md).
	"jbig2-symbol": anyPageApplied("jbig2-generic"),

	// A compatibility fallback with strictly worse compression. Gaining it never
	// improves a document; it is only ever chosen deliberately (FUTURE.md).
	"ccitt-g4": never,

	// A renderer turns diverted pages into processable ones, and nothing else.
	"render": anyPageDiverted("not-single-raster", "unsupported-codec"),

	// Codec capabilities: gaining one does not improve a document that was
	// already processed with it, and we cannot tell from the record whether a
	// page that missed it had content it would have helped. Conservatively
	// never - a false positive here means re-processing the whole archive.
	"quantize-png":    never,
	"downsample":      never,
	"jpeg-recompress": never,
	"text-layer":      never,
	"linearize":       never,

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
