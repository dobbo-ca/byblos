package main

import (
	"testing"

	"github.com/dobbo-ca/byblos/internal/pdfdoc"
)

// TestClassifyPrecedence pins section 4.1's ordering. The order is not
// cosmetic: it decides the ambiguous cases, and the published columns depend on
// it. A symbolic font that ALSO carries /Differences counts as C, not E,
// because the /Differences test runs first — get that backwards and dc's C
// column moves.
func TestClassifyPrecedence(t *testing.T) {
	cases := []struct {
		desc      string
		font      pdfdoc.FontFacts
		want      string
		wantNamed bool
	}{
		{"Type3 beats everything, even /ToUnicode",
			pdfdoc.FontFacts{Subtype: "Type3", ToUnicode: true}, classG, false},
		{"Type0 with /ToUnicode is A",
			pdfdoc.FontFacts{Subtype: "Type0", ToUnicode: true}, classA, false},
		{"Type0 without /ToUnicode is F",
			pdfdoc.FontFacts{Subtype: "Type0"}, classF, false},
		{"simple with /ToUnicode is B, whatever its encoding",
			pdfdoc.FontFacts{Subtype: "Type1", ToUnicode: true, Encoding: pdfdoc.EncodingDifferences}, classB, false},
		{"simple with /Differences is C",
			pdfdoc.FontFacts{Subtype: "Type1", Encoding: pdfdoc.EncodingDifferences}, classC, false},
		{"SYMBOLIC with /Differences is C, not E: /Differences runs first",
			pdfdoc.FontFacts{Subtype: "Type1", Encoding: pdfdoc.EncodingDifferences, Symbolic: true}, classC, false},
		{"simple, symbolic, no /ToUnicode, no /Differences is E",
			pdfdoc.FontFacts{Subtype: "Type1", Symbolic: true}, classE, false},
		{"a symbolic font with a NAMED encoding is still E",
			pdfdoc.FontFacts{Subtype: "Type1", Encoding: pdfdoc.EncodingNamed, Symbolic: true}, classE, false},
		{"a named encoding is D, and named",
			pdfdoc.FontFacts{Subtype: "Type1", Encoding: pdfdoc.EncodingNamed}, classD, true},
		{"no encoding is D, and ABSENT",
			pdfdoc.FontFacts{Subtype: "Type1", Encoding: pdfdoc.EncodingAbsent}, classD, false},
		{"a TrueType is a simple font, same as Type1",
			pdfdoc.FontFacts{Subtype: "TrueType", Encoding: pdfdoc.EncodingNamed}, classD, true},
	}

	for _, c := range cases {
		t.Run(c.desc, func(t *testing.T) {
			got, named := classify(c.font)
			if got != c.want {
				t.Errorf("class = %s, want %s", got, c.want)
			}
			if got == classD && named != c.wantNamed {
				t.Errorf("D named = %v, want %v", named, c.wantNamed)
			}
		})
	}
}

// TestShownExcludesCIDDescendants pins the exclusion section 4.1 states.
// CIDFontType0 and CIDFontType2 are never handed to a decoder directly — their
// Type0 parent is — so counting them would double every composite font and
// inflate the published totals.
func TestShownExcludesCIDDescendants(t *testing.T) {
	for _, sub := range []string{"CIDFontType0", "CIDFontType2"} {
		if shown(pdfdoc.FontFacts{Subtype: sub}) {
			t.Errorf("%s counts as shown; it would double-count its Type0 parent", sub)
		}
	}
	for _, sub := range []string{"Type0", "Type1", "TrueType", "Type3", "MMType1", ""} {
		if !shown(pdfdoc.FontFacts{Subtype: sub}) {
			t.Errorf("subtype %q is not counted as shown, but a decoder is handed it", sub)
		}
	}
}
