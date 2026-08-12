package jbig2

import "fmt"

// The text region decoding procedure of T.88 6.4.5, arithmetic variant.
//
// A text region places symbols from a dictionary onto a bitmap. It decodes no
// pixels of its own: every pixel it produces was decoded once, in a symbol
// dictionary, and is stamped here as many times as it occurs. That is where
// symbol mode's compression comes from and it is also the whole of its cost
// model -- see streamBudget for why placement needs a budget that region
// decoding did not.
//
// THE COORDINATE SYSTEM IS THE PART TO GET RIGHT. Instances are grouped into
// STRIPS of SBSTRIPS rows. T is the coordinate across strips and S the one along
// them, and TRANSPOSED swaps which of those is x and which is y. Both are coded
// as deltas from the previous instance, so a single sign error does not shift
// one glyph -- it shifts every glyph after it, and the page still looks like a
// page.

// decodeTextRegion decodes one text region segment against syms, the symbol list
// its referred-to dictionaries exported.
func decodeTextRegion(sg segment, syms []*Bitmap, budget *streamBudget) (*Bitmap, regionInfo, error) {
	h, err := parseTextRegionHeader(sg.data)
	if err != nil {
		return nil, regionInfo{}, err
	}
	if err := h.unsupported(); err != nil {
		return nil, regionInfo{}, err
	}
	if len(syms) == 0 {
		return nil, regionInfo{}, fmt.Errorf("jbig2: text region refers to no symbol dictionary, so it has "+
			"no symbols to place; segment %d", sg.number)
	}
	if err := budget.chargeInstances(int64(h.numInstances)); err != nil {
		return nil, regionInfo{}, err
	}

	strips := 1 << h.logStrips
	codeLen := symCodeLen(len(syms))
	b := NewBitmap(h.info.w, h.info.h)
	if h.defPixel == 1 {
		for i := range b.Pix {
			b.Pix[i] = 0xFF
		}
		b.MaskPadding()
	}

	d := newDecoder(sg.data[h.dataOff:])
	iadt, iafs, iads, iait := newIntContexts(), newIntContexts(), newIntContexts(), newIntContexts()
	iaid := make(contexts, 1<<(codeLen+1))

	// T.88 6.4.5 step 1: the first strip's T is coded as a negative offset.
	stripT, ok := decodeInt(d, iadt)
	if !ok {
		return nil, regionInfo{}, fmt.Errorf("jbig2: text region: OOB where the initial strip T was expected")
	}
	stripT = -stripT * strips
	firstS, placed := 0, 0

	for placed < int(h.numInstances) {
		dt, ok := decodeInt(d, iadt)
		if !ok {
			return nil, regionInfo{}, fmt.Errorf("jbig2: text region: OOB where a strip T delta was expected")
		}
		stripT += dt * strips

		dfs, ok := decodeInt(d, iafs)
		if !ok {
			return nil, regionInfo{}, fmt.Errorf("jbig2: text region: OOB where a strip's first S was expected")
		}
		firstS += dfs
		curS := firstS

		for first := true; ; first = false {
			if !first {
				ids, ok := decodeInt(d, iads)
				if !ok {
					break // OOB ends the strip (T.88 6.4.5 step 3c ii).
				}
				curS += ids + h.dsOffset
			}
			if placed == int(h.numInstances) {
				return nil, regionInfo{}, fmt.Errorf("jbig2: text region declares %d instances and codes more",
					h.numInstances)
			}
			curT := 0
			if strips != 1 {
				v, ok := decodeInt(d, iait)
				if !ok {
					return nil, regionInfo{}, fmt.Errorf("jbig2: text region: OOB where a curT was expected")
				}
				curT = v
			}
			id := decodeIAID(d, iaid, codeLen)
			if id < 0 || id >= len(syms) {
				return nil, regionInfo{}, fmt.Errorf("jbig2: text region places symbol %d of a %d-symbol list",
					id, len(syms))
			}
			sym := syms[id]
			if err := budget.chargePlacement(int64(sym.W), int64(sym.H)); err != nil {
				return nil, regionInfo{}, err
			}
			curS = placeSymbol(b, sym, curS, stripT+curT, h.refCorner, h.transposed, byte(h.combOp))
			placed++
		}
	}
	return b, h.info, nil
}

// placeSymbol draws sym onto b with its reference corner at (S, T) -- or at
// (T, S) when transposed -- and returns the advanced S.
//
// The pre-advance and post-advance of T.88 6.4.5 steps 3c vi, vii, x and xi are
// what make S mean the LEADING edge for a left or top corner and the TRAILING
// edge for a right or bottom one. The net effect either way is that the symbol
// occupies the same run of coordinates starting where S was on entry, which is
// why both branches below compute the same origin from different arithmetic
// rather than placing the symbol in two different places.
func placeSymbol(b, sym *Bitmap, curS, t, refCorner int, transposed bool, op byte) int {
	w, h := sym.W, sym.H
	right := refCorner == refCornerTopRight || refCorner == refCornerBottomRight
	bottom := refCorner == refCornerBottomLeft || refCorner == refCornerBottomRight

	if !transposed && right {
		curS += w - 1
	}
	if transposed && bottom {
		curS += h - 1
	}

	var x0, y0 int
	if !transposed {
		x0, y0 = curS, t
		if right {
			x0 = curS - w + 1
		}
		if bottom {
			y0 = t - h + 1
		}
	} else {
		x0, y0 = t, curS
		if right {
			x0 = t - w + 1
		}
		if bottom {
			y0 = curS - h + 1
		}
	}
	composite(b, sym, x0, y0, op)

	if !transposed && !right {
		curS += w - 1
	}
	if transposed && !bottom {
		curS += h - 1
	}
	return curS
}
