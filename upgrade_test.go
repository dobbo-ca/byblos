package byblos

import (
	"slices"
	"testing"
)

func TestUpgradeCandidates(t *testing.T) {
	tests := []struct {
		name    string
		prov    *Provenance
		current []string
		want    []string
	}{
		// --- the three cases from design spec section 6 ---
		{
			name: "applied jbig2-generic, new jbig2-symbol: yes, smaller output",
			prov: &Provenance{
				Capabilities: []string{"inspect", "jbig2-generic"},
				Pages:        []PageProvenance{{Applied: []string{"jbig2-generic"}}},
			},
			current: []string{"inspect", "jbig2-generic", "jbig2-symbol"},
			want:    []string{"jbig2-symbol"},
		},
		{
			name: "diverted not-single-raster, new render: yes, now processable",
			prov: &Provenance{
				Capabilities: []string{"extract-raster", "inspect"},
				Pages:        []PageProvenance{{Diverted: "not-single-raster"}},
			},
			current: []string{"extract-raster", "inspect", "render"},
			want:    []string{"render"},
		},
		{
			name: "applied jpeg-recompress only, new jbig2-symbol: no bitonal content",
			prov: &Provenance{
				Capabilities: []string{"inspect", "jpeg-recompress"},
				Pages:        []PageProvenance{{Applied: []string{"jpeg-recompress"}}},
			},
			current: []string{"inspect", "jbig2-symbol", "jpeg-recompress"},
			want:    nil,
		},

		// --- boundary behaviour ---
		{
			name: "no capability gap at all",
			prov: &Provenance{
				Capabilities: []string{"inspect", "jbig2-generic"},
				Pages:        []PageProvenance{{Applied: []string{"jbig2-generic"}}},
			},
			current: []string{"inspect", "jbig2-generic"},
			want:    nil,
		},
		{
			name: "numeric parameter suffixes are stripped before matching",
			prov: &Provenance{
				Capabilities: []string{"downsample", "inspect", "jpeg-recompress"},
				Pages:        []PageProvenance{{Applied: []string{"downsample-150", "jpeg-recompress"}}},
			},
			current: []string{"downsample", "inspect", "jpeg-recompress", "page-cleanup"},
			want:    []string{"page-cleanup"},
		},
		{
			name: "ccitt-g4 is a compatibility fallback, never an improvement",
			prov: &Provenance{
				Capabilities: []string{"inspect", "jbig2-generic"},
				Pages:        []PageProvenance{{Applied: []string{"jbig2-generic"}}},
			},
			current: []string{"ccitt-g4", "inspect", "jbig2-generic"},
			want:    nil,
		},
		{
			name: "jbig2-generic improves a document that only got ccitt-g4",
			prov: &Provenance{
				Capabilities: []string{"ccitt-g4", "inspect"},
				Pages:        []PageProvenance{{Applied: []string{"ccitt-g4"}}},
			},
			current: []string{"ccitt-g4", "inspect", "jbig2-generic"},
			want:    []string{"jbig2-generic"},
		},
		// --- a codec divert is a decoder problem, not a renderer problem ---
		//
		// byb-97q. A renderer cannot read a raster whose codec Byblos cannot
		// decode, so nominating one for these pages recommends the largest
		// piece of work in the Cadmus/Byblos family for a page that needs a
		// decoder. Measured over the survey corpora, the page-covering raster
		// on a scan-shaped page was undecodable on 56.9% of dc_random, 67.2%
		// of commons and 91.3% of ia pages.
		{
			name: "unsupported-codec is NOT a render candidate",
			prov: &Provenance{
				Capabilities: []string{"extract-raster", "inspect"},
				Pages:        []PageProvenance{{Diverted: "unsupported-codec"}},
			},
			current: []string{"extract-raster", "inspect", "render"},
			want:    nil,
		},
		{
			name: "unsupported-codec nominates every decoder",
			prov: &Provenance{
				Capabilities: []string{"extract-raster", "inspect"},
				Pages:        []PageProvenance{{Diverted: "unsupported-codec"}},
			},
			current: []string{"decode-jbig2", "decode-jpx", "decode-tiff", "extract-raster", "inspect", "render"},
			want:    []string{"decode-jbig2", "decode-jpx", "decode-tiff"},
		},
		{
			name: "not-single-raster nominates the renderer and no decoder",
			prov: &Provenance{
				Capabilities: []string{"extract-raster", "inspect"},
				Pages:        []PageProvenance{{Diverted: "not-single-raster"}},
			},
			current: []string{"decode-jbig2", "decode-jpx", "decode-tiff", "extract-raster", "inspect", "render"},
			want:    []string{"render"},
		},
		{
			name: "a document carrying both divert classes wants both",
			prov: &Provenance{
				Capabilities: []string{"extract-raster", "inspect"},
				Pages: []PageProvenance{
					{Diverted: "not-single-raster"},
					{Diverted: "unsupported-codec"},
				},
			},
			current: []string{"decode-jbig2", "decode-jpx", "decode-tiff", "extract-raster", "inspect", "render"},
			want:    []string{"decode-jbig2", "decode-jpx", "decode-tiff", "render"},
		},
		// --- byb-z8j: a codec-specific class narrows which decoder is wanted ---
		{
			name: "jbig2-diverted page wants decode-jbig2 and not decode-jpx or decode-tiff",
			prov: &Provenance{
				Capabilities: []string{"extract-raster", "inspect"},
				Pages:        []PageProvenance{{Diverted: "unsupported-codec-jbig2"}},
			},
			current: []string{"decode-jbig2", "decode-jpx", "decode-tiff", "extract-raster", "inspect"},
			want:    []string{"decode-jbig2"},
		},
		{
			name: "jpx-diverted page wants only decode-jpx",
			prov: &Provenance{
				Capabilities: []string{"extract-raster", "inspect"},
				Pages:        []PageProvenance{{Diverted: "unsupported-codec-jpx"}},
			},
			current: []string{"decode-jbig2", "decode-jpx", "decode-tiff", "extract-raster", "inspect"},
			want:    []string{"decode-jpx"},
		},
		{
			name: "tiff-diverted page wants only decode-tiff",
			prov: &Provenance{
				Capabilities: []string{"extract-raster", "inspect"},
				Pages:        []PageProvenance{{Diverted: "unsupported-codec-tiff"}},
			},
			current: []string{"decode-jbig2", "decode-jpx", "decode-tiff", "extract-raster", "inspect"},
			want:    []string{"decode-tiff"},
		},
		{
			name: "a fully processed document wants no decoder",
			prov: &Provenance{
				Capabilities: []string{"extract-raster", "inspect"},
				Pages:        []PageProvenance{{Applied: []string{"jbig2-generic"}}},
			},
			current: []string{"decode-jbig2", "decode-jpx", "decode-tiff", "extract-raster", "inspect"},
			want:    nil,
		},
		{
			name: "only the diverted page matters, not the handled ones",
			prov: &Provenance{
				Capabilities: []string{"extract-raster", "inspect", "jbig2-generic"},
				Pages: []PageProvenance{
					{Applied: []string{"jbig2-generic"}},
					{Diverted: "not-single-raster"},
				},
			},
			current: []string{"extract-raster", "inspect", "jbig2-generic", "render"},
			want:    []string{"render"},
		},
		{
			name: "an unknown new capability is reported: better a wasted re-run than a missed upgrade",
			prov: &Provenance{
				Capabilities: []string{"inspect"},
				Pages:        []PageProvenance{{Applied: []string{"jbig2-generic"}}},
			},
			current: []string{"inspect", "some-future-thing"},
			want:    []string{"some-future-thing"},
		},
		{
			name:    "nil provenance: nothing is known, so everything is a candidate",
			prov:    nil,
			current: []string{"inspect", "render"},
			want:    []string{"inspect", "render"},
		},
		{
			name: "results are sorted and deduplicated",
			prov: &Provenance{
				Capabilities: nil,
				Pages:        []PageProvenance{{Diverted: "not-single-raster"}},
			},
			current: []string{"render", "pdfa", "render"},
			want:    []string{"pdfa", "render"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := UpgradeCandidates(tc.prov, tc.current)
			if !slices.Equal(got, tc.want) {
				t.Errorf("UpgradeCandidates() = %v; want %v", got, tc.want)
			}
		})
	}
}

// A capability with no rule is silently treated as "always improves", which
// would make the reprocess job scan the whole archive. Catch it here instead.
func TestEveryCapabilityHasARule(t *testing.T) {
	for _, c := range Capabilities() {
		if _, ok := capabilityRules[c]; !ok {
			t.Errorf("capability %q has no rule in capabilityRules", c)
		}
	}
}

// Every capability string named in FUTURE.md must already have a rule, so that
// shipping one of them needs no change here.
func TestFutureCapabilitiesHaveRules(t *testing.T) {
	for _, c := range []string{
		"jbig2-symbol", "ccitt-g4", "render", "pdfa", "page-cleanup",
		"decode-jbig2", "decode-jpx", "decode-tiff",
	} {
		if _, ok := capabilityRules[c]; !ok {
			t.Errorf("FUTURE.md capability %q has no rule in capabilityRules", c)
		}
	}
}

func TestAppliedCapability(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"downsample-150", "downsample"},
		{"jbig2-generic", "jbig2-generic"},
		{"jpeg-recompress", "jpeg-recompress"},
		{"quantize-png-64", "quantize-png"},
		{"trailing-", "trailing-"},
		{"noseparator", "noseparator"},
	} {
		if got := appliedCapability(tc.in); got != tc.want {
			t.Errorf("appliedCapability(%q) = %q; want %q", tc.in, got, tc.want)
		}
	}
}
