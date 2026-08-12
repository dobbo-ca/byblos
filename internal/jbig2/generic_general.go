package jbig2

import "fmt"

// The generic region decoding procedure of T.88 6.2.5.7 for ALL FOUR TEMPLATES
// and ARBITRARY AT PIXELS, over a decoder and a context array supplied by the
// caller.
//
// WHY THIS EXISTS BESIDE decodeGenericRegion, WHICH DECODES THE SAME THING. The
// two differ in what they are handed, and neither can be the other:
//
//   - decodeGenericRegion owns its decoder and its contexts, because T.88
//     7.4.6.4 resets the statistics at the start of every generic region
//     SEGMENT. It codes template 0 with the nominal AT pixels and it is the only
//     path a page-sized region ever takes, so it keeps the incremental
//     three-run context loop that makes 51 Mpx/s possible and that every figure
//     in the budget comment was measured on.
//   - this one is handed both, because a symbol dictionary decodes every symbol
//     it holds from ONE arithmetic decoder and ONE shared context array (T.88
//     6.5.8.1): the statistics learned on the first glyph are what compress the
//     second. Constructing a decoder per symbol would restart the code register
//     and decode noise from the second symbol onwards.
//
// THE COST OF GENERALITY IS PAID WHERE IT DOES NOT MATTER. This gathers each
// pixel's context by walking a template table -- up to sixteen Bitmap.Get calls
// against three shifts and three Gets in the incremental loop. On a symbol that
// is nothing: a glyph is a few hundred pixels and a dictionary a few hundred
// thousand. On a 67-million-pixel page it would be several seconds, which is why
// a page-sized region does not come here.
//
// TestGeneralTemplateAgreesWithTheIncrementalLoop is what keeps the two from
// drifting. Two implementations of one context definition is exactly the shape
// this package warns about in decodeGenericRegion's own comment -- a
// disagreement would not fail, it would produce a plausible wrong raster -- so
// they are pinned against each other on every fixture bitmap rather than trusted
// to stay equal.

// templatePixel is one entry of a generic region template: a fixed offset from
// the pixel being coded, or, when at is not atFixed, the AT pixel of that index.
type templatePixel struct {
	dx, dy int
	at     int
}

const atFixed = -1

// genericTemplates are the four templates of T.88 figures 4 to 7, listed MOST
// SIGNIFICANT CONTEXT BIT FIRST.
//
// The bit ORDER is not derivable from the figures alone -- they draw positions,
// not a numbering -- and getting it wrong produces a decoder that runs happily
// and returns noise. It is the order of T.88 6.2.5.7's context formation, and it
// agrees pixel for pixel with contextTemplate0 on template 0 with nominal AT,
// which is the property the agreement test checks.
var genericTemplates = [4][]templatePixel{
	{ // template 0: 16 pixels, four AT
		{at: 3}, {-1, -2, atFixed}, {0, -2, atFixed}, {1, -2, atFixed}, {at: 2},
		{at: 1}, {-2, -1, atFixed}, {-1, -1, atFixed}, {0, -1, atFixed}, {1, -1, atFixed}, {2, -1, atFixed}, {at: 0},
		{-4, 0, atFixed}, {-3, 0, atFixed}, {-2, 0, atFixed}, {-1, 0, atFixed},
	},
	{ // template 1: 13 pixels, one AT
		{-1, -2, atFixed}, {0, -2, atFixed}, {1, -2, atFixed}, {2, -2, atFixed},
		{-2, -1, atFixed}, {-1, -1, atFixed}, {0, -1, atFixed}, {1, -1, atFixed}, {2, -1, atFixed},
		{at: 0}, {-3, 0, atFixed}, {-2, 0, atFixed}, {-1, 0, atFixed},
	},
	{ // template 2: 10 pixels, one AT
		{-1, -2, atFixed}, {0, -2, atFixed}, {1, -2, atFixed},
		{-2, -1, atFixed}, {-1, -1, atFixed}, {0, -1, atFixed}, {1, -1, atFixed},
		{at: 0}, {-2, 0, atFixed}, {-1, 0, atFixed},
	},
	{ // template 3: 10 pixels, one AT, and only two rows
		{-3, -1, atFixed}, {-2, -1, atFixed}, {-1, -1, atFixed}, {0, -1, atFixed}, {1, -1, atFixed},
		{at: 0}, {-4, 0, atFixed}, {-3, 0, atFixed}, {-2, 0, atFixed}, {-1, 0, atFixed},
	},
}

// sltpContexts are the fixed contexts the SLTP bit is coded in under TPGDON,
// one per template (T.88 6.2.5.7). Template 0's is the constant the encoder
// already uses.
var sltpContexts = [4]int{sltpContextTemplate0, 0x0795, 0x00E5, 0x0195}

// nominalAT is the AT field of T.88 Table 5 as signed pairs, the same eight
// bytes nominalATTemplate0 holds.
var nominalAT = [4][2]int8{{3, -1}, {-3, -1}, {2, -2}, {-2, -2}}

// decodeGenericRegionGeneral decodes w x h pixels into a fresh bitmap using
// template and at, taking its decisions from d and its statistics from cx.
//
// It charges its pixels to the package's decode counter exactly as
// decodeGenericRegion does, so a symbol dictionary's work is visible to the
// same cost tests that bound a generic region's.
func decodeGenericRegionGeneral(d *decoder, cx contexts, w, h, template int, at [4][2]int8, tpgdon bool) (*Bitmap, error) {
	if w <= 0 || h <= 0 {
		return nil, fmt.Errorf("jbig2: generic region is %dx%d; dimensions must be positive", w, h)
	}
	if template < 0 || template > 3 {
		return nil, fmt.Errorf("jbig2: generic region template %d; T.88 6.2.5.3 defines 0-3", template)
	}
	tmpl := genericTemplates[template]
	b := NewBitmap(w, h)

	var work int64
	defer func() { decodedPixels.Add(work) }()

	ltp := 0
	for y := 0; y < h; y++ {
		if tpgdon {
			work++
			ltp ^= d.decode(cx, sltpContexts[template])
			if ltp == 1 {
				if y > 0 {
					copy(b.Pix[y*b.Stride:(y+1)*b.Stride], b.Pix[(y-1)*b.Stride:y*b.Stride])
				}
				continue
			}
		}
		work += int64(w)
		for x := 0; x < w; x++ {
			ctx := 0
			for _, p := range tmpl {
				dx, dy := p.dx, p.dy
				if p.at != atFixed {
					dx, dy = int(at[p.at][0]), int(at[p.at][1])
				}
				ctx = ctx<<1 | b.Get(x+dx, y+dy)
			}
			if d.decode(cx, ctx) != 0 {
				b.Set(x, y, 1)
			}
		}
	}
	return b, nil
}
