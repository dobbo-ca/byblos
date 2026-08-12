package jbig2

// byb-9v0 left this package with TWO implementations of one context definition:
// the incremental three-run loop decodeGenericRegion has always used, and the
// table walk decodeGenericRegionGeneral needs because a symbol dictionary codes
// templates 1-3 and moves its AT pixels.
//
// A DISAGREEMENT BETWEEN THEM WOULD NOT FAIL, WHICH IS WHY THIS EXISTS. The MQ
// decoder yields a decision for any context number, so a table with two entries
// transposed decodes without error into a bitmap that is merely wrong. Nothing
// downstream can tell: the symbol is a glyph shape either way. So the two are
// pinned against each other on every fixture bitmap and at every pixel,
// including the out-of-bounds ones at the edges where the template hangs off the
// bitmap and T.88 6.2.5.2's read-as-zero rule is what makes them agree.

import (
	"bytes"
	"testing"
)

// TestGeneralTemplateAgreesWithTheIncrementalLoop compares the CONTEXTS the two
// implementations form, not the bitmaps they decode.
//
// Comparing decoded bitmaps would be the weaker test by a wide margin: the
// arithmetic decoder is adaptive, so a context that disagrees on a rare
// combination can still decode the same pixels on a fixture that never reaches
// it. The context number is the thing the two implementations independently
// compute, so it is the thing to compare, at every pixel of every fixture --
// including the three rows and four columns of margin where most of the template
// is off the bitmap.
func TestGeneralTemplateAgreesWithTheIncrementalLoop(t *testing.T) {
	tmpl := genericTemplates[0]
	general := func(b *Bitmap, x, y int) int {
		cx := 0
		for _, p := range tmpl {
			dx, dy := p.dx, p.dy
			if p.at != atFixed {
				dx, dy = int(nominalAT[p.at][0]), int(nominalAT[p.at][1])
			}
			cx = cx<<1 | b.Get(x+dx, y+dy)
		}
		return cx
	}

	for name, b := range fixtureBitmaps() {
		t.Run(name, func(t *testing.T) {
			// Past the edges on every side, so the out-of-bounds rule is
			// exercised rather than assumed.
			for y := -3; y < b.H+1; y++ {
				for x := -5; x < b.W+5; x++ {
					want := contextTemplate0(b, x, y)
					if got := general(b, x, y); got != want {
						t.Fatalf("at (%d,%d): the template table forms context %#06x and the "+
							"incremental loop forms %#06x. One of genericTemplates[0] and "+
							"contextTemplate0 has the template pixels in a different order, and "+
							"neither would report an error for it -- the symbol would simply "+
							"decode wrong.", x, y, got, want)
					}
				}
			}
		})
	}
}

// And the SLTP context of each template, which is a constant per template and is
// the one number a typo cannot be caught on by the test above.
func TestSLTPContextsMatchTheTemplateThatUsesThem(t *testing.T) {
	if sltpContexts[0] != sltpContextTemplate0 {
		t.Errorf("sltpContexts[0] = %#06x; want %#06x, the constant the encoder codes with",
			sltpContexts[0], sltpContextTemplate0)
	}
	// T.88 6.2.5.7, the four values in the order the templates are numbered.
	for i, want := range [4]int{0x9B25, 0x0795, 0x00E5, 0x0195} {
		if sltpContexts[i] != want {
			t.Errorf("sltpContexts[%d] = %#06x; want %#06x (T.88 6.2.5.7)", i, sltpContexts[i], want)
		}
	}
}

// The general path must decode what the incremental path decodes, end to end, on
// the parameters they share. This is weaker than the context comparison above
// and catches a different thing: that the two agree about TPGDON, about the row
// copy, and about where the coded data starts.
func TestGeneralPathDecodesWhatTheIncrementalPathDecodes(t *testing.T) {
	for name, want := range fixtureBitmaps() {
		for _, tpgdon := range []bool{false, true} {
			t.Run(name, func(t *testing.T) {
				// A copy: EncodeGenericRegion masks padding in place, which is a
				// no-op on a well-formed fixture but is still a write.
				src := &Bitmap{W: want.W, H: want.H, Stride: want.Stride, Pix: bytes.Clone(want.Pix)}
				coded := EncodeGenericRegion(src, tpgdon)
				got, err := decodeGenericRegionGeneral(newDecoder(coded), make(contexts, 1<<16),
					want.W, want.H, 0, nominalAT, tpgdon)
				if err != nil {
					t.Fatalf("decodeGenericRegionGeneral: %v", err)
				}
				if !bytes.Equal(got.Pix, want.Pix) {
					t.Errorf("the general path decoded this package's own generic region wrongly "+
						"(tpgdon=%v)", tpgdon)
				}
			})
		}
	}
}
