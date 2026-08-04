package jbig2

import "fmt"

// decodeGenericRegion is the generic region decoding procedure of T.88 6.2.5.7
// for GBTEMPLATE 0 with nominal AT pixels: the exact inverse of
// EncodeGenericRegion, run against the same context template and the same fixed
// SLTP context.
//
// It gathers each pixel's context with contextTemplate0 -- the encoder's own
// helper, deliberately shared rather than re-derived, because a decoder that
// disagreed with the encoder about the bit order of the template would produce
// a plausible-looking wrong raster instead of an error.
//
// Under TPGDON each row is preceded by an SLTP bit; when the running LTP flag
// is 1 the row is not coded at all and repeats the row above (T.88 6.2.5.5,
// 6.2.5.7 step 3b). Row 0 repeats the implicit all-zero row above the bitmap,
// which NewBitmap already provides, so that case needs no copy.
//
// There is no way for this to detect a stream it has mis-parsed: the MQ decoder
// yields a decision for any input, so garbage in gives a bitmap out. The
// callers' business is to have rejected everything it cannot code for BEFORE
// reaching here -- see decodeGenericRegionSegment.
func decodeGenericRegion(data []byte, w, h int, tpgdon bool) (*Bitmap, error) {
	if w <= 0 || h <= 0 {
		return nil, fmt.Errorf("jbig2: generic region is %dx%d; dimensions must be positive", w, h)
	}
	b := NewBitmap(w, h)
	cx := make(contexts, 1<<16)
	d := newDecoder(data)

	ltp := 0
	for y := 0; y < h; y++ {
		if tpgdon {
			ltp ^= d.decode(cx, sltpContextTemplate0)
			if ltp == 1 {
				if y > 0 {
					copy(b.Pix[y*b.Stride:(y+1)*b.Stride], b.Pix[(y-1)*b.Stride:y*b.Stride])
				}
				continue
			}
		}
		for x := 0; x < w; x++ {
			if d.decode(cx, contextTemplate0(b, x, y)) != 0 {
				b.Set(x, y, 1)
			}
		}
	}
	return b, nil
}
