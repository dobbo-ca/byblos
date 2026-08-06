package jbig2

import (
	"fmt"
	"sync/atomic"
)

// decodedPixels is the running, process-lifetime total of MQ decisions this
// package's generic region decoder has actually run: one per coded pixel, plus
// one per row for the SLTP bit when TPGDON is on.
//
// It exists so that a test can bound DECODE COST directly. Neither of the two
// obvious proxies can see the thing that matters. Wall clock varies by an order
// of magnitude on a loaded runner, so any threshold tight enough to catch
// wasted work is flaky and any threshold loose enough to be stable catches
// nothing. TotalAlloc is blind by construction here: a decoder that fills a
// 8192x4095 region and then discards every pixel of it in composite() allocates
// one bitmap either way. The wasted work is CPU that produces no output, and
// counting it is the only way to assert it is not being done.
//
// One atomic add per region, accumulated locally in between, so this costs
// nothing per pixel.
var decodedPixels atomic.Int64

// DecodedPixels reports that running total. It only ever increases; take a
// delta around the call under test.
func DecodedPixels() int64 { return decodedPixels.Load() }

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
//
// The dimension check below is UNREACHABLE from the only production caller, and
// is kept rather than deleted. parseRegionInfo refuses w == 0 || h == 0 out of
// the region segment information field, so decodeGenericRegionSegment can never
// arrive here with either at zero, and no test can fail without this check --
// which is why it has none. What it buys is that the next caller of this
// unexported function gets an error rather than NewBitmap's panic, and that is
// not a distinction worth leaving to whoever adds one. Documented rather than
// pinned, because a test that cannot fail is worse than no test.
func decodeGenericRegion(data []byte, w, h int, tpgdon bool) (*Bitmap, error) {
	if w <= 0 || h <= 0 {
		return nil, fmt.Errorf("jbig2: generic region is %dx%d; dimensions must be positive", w, h)
	}
	b := NewBitmap(w, h)
	cx := make(contexts, 1<<16)
	d := newDecoder(data)

	var work int64
	defer func() { decodedPixels.Add(work) }()

	ltp := 0
	for y := 0; y < h; y++ {
		if tpgdon {
			work++
			ltp ^= d.decode(cx, sltpContextTemplate0)
			if ltp == 1 {
				if y > 0 {
					copy(b.Pix[y*b.Stride:(y+1)*b.Stride], b.Pix[(y-1)*b.Stride:y*b.Stride])
				}
				continue
			}
		}
		work += int64(w)
		for x := 0; x < w; x++ {
			if d.decode(cx, contextTemplate0(b, x, y)) != 0 {
				b.Set(x, y, 1)
			}
		}
	}
	return b, nil
}
