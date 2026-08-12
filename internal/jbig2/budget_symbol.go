package jbig2

import "fmt"

// The resource budget for symbol mode, and the one place in this package where a
// bound is enforced DURING decoding rather than from the headers.
//
// EVERY RULE IN segment_decode.go'S BUDGET COMMENT RESTS ON ONE PROPERTY: a
// region segment's header states its size, so what it will cost is knowable
// before a coded bit is read, and an unaffordable stream is refused having
// allocated nothing but its headers. A SYMBOL DICTIONARY BREAKS THAT PROPERTY,
// and not incidentally -- structurally. T.88 6.5.5 codes symbol sizes as
// arithmetic deltas inside the coded data (IADH, IADW). The header carries
// SDNUMNEWSYMS and nothing about how big any of them is. There is no arithmetic
// over the header fields that bounds the work, because the header does not
// contain the terms.
//
// A TEXT REGION BREAKS IT A SECOND WAY, in the other direction. Its header does
// state its size and its instance count, but its cost is neither: it is the sum
// of the AREAS of the symbols it places, and those come from a dictionary in
// another segment -- or, when the dictionary is in a PDF /JBIG2Globals object,
// from another STREAM. A small dictionary and a short region can place a very
// large number of instances of a large symbol: 65,536 instances of a 4096x4096
// symbol is a hundred and ten billion pixel writes out of a region header that
// declares 100x100, and every one of the five header rules is satisfied.
//
// So the budget is split in two by WHEN it can be evaluated, and both halves are
// live:
//
//	From the headers, in planStream, as before:
//	  - the symbols a stream may decode at all (maxStreamSymbols)
//	  - the instances it may place at all (maxStreamInstances)
//
//	During decoding, here, checked BEFORE each allocation and each placement:
//	  - symbol bitmap pixels, charged into the SAME budget rule 2 gives region
//	    decoding, because they are the same thing: one MQ decision per pixel
//	  - symbol bitmap bytes, charged into the same budget as rule 4
//	  - placement pixels, which are not MQ decisions and get their own rule
//
// CHARGING BEFORE THE ALLOCATION IS WHAT MAKES THE DECODE-TIME HALF WORTH
// ANYTHING. A check after the fact bounds nothing: the 32-bit height and width
// deltas of one height class can ask for a 2-billion-pixel symbol, and by the
// time a post-hoc check saw it the allocation had already happened. Every charge
// below takes the dimensions and refuses them; NewBitmap is called only on what
// the charge admitted.
//
// PLACEMENT IS NOT CHARGED AT THE MQ RATE, and the ratio comes from the
// measurements already in segment_decode.go rather than from a guess. An MQ
// decision runs at 51 Mpx/s on the shape a real page has. A placement pixel is a
// read, a boolean operation and a masked write -- the same work composite()
// does, and composite's cost is the difference between two rows of that table:
// an 8192x8192 region alone decodes 67,108,864 pixels in 1.238-1.323s, and an
// 8191x8191 page-covering stream through DecodeJBIG2Generic costs 1.552s for
// 67,092,481. The shapes differ by one row on a side, so the difference is
// approximate, and it puts compositing at roughly 290 Mpx/s -- about a sixth of
// an MQ decision either way one rounds it.
//
// Charging placement against rule 2's pixel budget would therefore price it at
// about six times its cost, and what pays for that is a legitimate page. A dense
// 600-dpi A4 scan is 34.8 million page pixels; ESTIMATED rather than measured,
// five thousand glyphs at an 80x80 box is another 32 million pixels of drawing,
// which a shared budget would refuse outright. The estimate is coarse and it
// does not need to be better than coarse: it is within a factor of two of the
// whole budget, which is enough to show that sharing one is the wrong shape.
//
// So placement gets rule 3's SHAPE rather than rule 2's number: at most
// maxRegionOverdraw times the page plus overdrawFloorPixels, which is the same
// argument one level down. A region that draws far more symbol area than the
// page can show is doing work with no output, exactly as a region that overhangs
// its page is. At the cap on the largest admissible page that is 268 million
// placement pixels, about 0.9s -- against the 1.55s residual the pixel budget
// already concedes unconditionally, and reached only by a stream that draws four
// times over every pixel of a maximal page.
const (
	// maxStreamSymbols bounds the symbols one stream may decode, over every
	// dictionary in it. It is a HEADER rule: SDNUMNEWSYMS is four bytes, so a
	// 14-byte segment can ask for four billion *Bitmap slots -- 32 GB of
	// pointers -- before any of them is decoded.
	//
	// 1<<16 is derived the same way maxStreamSegments is, from what a document
	// legitimately holds rather than from a fixture. A symbol dictionary is an
	// alphabet: jbig2enc emits twelve for a page of Latin text, a whole document
	// dictionary runs to a few thousand, and the largest alphabet any script
	// puts on a page is CJK, whose common set is about seven thousand
	// characters. 65,536 is an order of magnitude past that, and it costs 2.6 MB
	// of Bitmap headers at the cap, inside the 16 MiB rules 1-4 concede.
	maxStreamSymbols = 1 << 16

	// maxStreamInstances bounds the symbol instances one stream may place, over
	// every text region in it. It is a HEADER rule for the same reason:
	// SBNUMINSTANCES is four bytes.
	//
	// It is not bounded by the placement rule below, and that is why it exists
	// separately: an instance costs about thirty MQ decisions in coordinate and
	// symbol-ID decoding BEFORE anything is drawn, so four billion instances of
	// a 1x1 symbol is 120 billion decisions against a placement charge of four
	// billion pixels. 1<<21 = 2,097,152 keeps that per-instance work at about
	// one maximal page's worth of MQ decisions, which is the bar the whole
	// budget is set to. A dense 600-dpi page carries a few thousand glyphs, so
	// the headroom over anything real is two orders of magnitude.
	maxStreamInstances = 1 << 21
)

// streamBudget carries the running totals across a whole stream: one dictionary
// or region cannot be judged on its own, because a stream may hold any number of
// individually affordable ones.
//
// pixels and bmBytes CONTINUE the totals planStream already charged for the page
// and the region segments, rather than starting from zero. A stream whose
// regions have already spent the pixel budget has none left for its symbols, and
// splitting the two budgets would let it spend each in full.
type streamBudget struct {
	pixels    int64 // MQ decisions, against MaxPagePixels
	bmBytes   int64 // retained packed bitmap bytes, against maxStreamBitmapBytes
	symbols   int64 // symbols decoded, against maxStreamSymbols
	instances int64 // instances placed, against maxStreamInstances
	placement int64 // placement pixels, against placementLimit
	// pagePixels and placementLimit are resolved once the page size is known.
	// The limit is maxRegionOverdraw*pagePixels + overdrawFloorPixels.
	pagePixels     int64
	placementLimit int64
}

func (b *streamBudget) chargeSymbols(n int64) error {
	if n < 0 || b.symbols+n > maxStreamSymbols {
		return fmt.Errorf("jbig2: the stream's symbol dictionaries declare more than %d symbols; the limit "+
			"is an order of magnitude past the largest alphabet a script puts on a page", int64(maxStreamSymbols))
	}
	b.symbols += n
	return nil
}

func (b *streamBudget) chargeInstances(n int64) error {
	if n < 0 || b.instances+n > maxStreamInstances {
		return fmt.Errorf("jbig2: the stream's text regions declare more than %d symbol instances; each one "+
			"costs coordinate decoding before it draws anything", int64(maxStreamInstances))
	}
	b.instances += n
	return nil
}

// chargeBitmap charges one symbol bitmap against the pixel and memory budgets
// rules 2 and 4 set for the whole stream. It is called with the dimensions
// BEFORE the bitmap exists.
func (b *streamBudget) chargeBitmap(w, h int64) error {
	if w <= 0 || h <= 0 || w > MaxPagePixels || h > MaxPagePixels {
		return fmt.Errorf("jbig2: symbol is %dx%d", w, h)
	}
	b.pixels += w * h
	b.bmBytes += (w + 7) / 8 * h
	if b.pixels > MaxPagePixels || b.bmBytes > maxStreamBitmapBytes {
		return fmt.Errorf("jbig2: decoding a %dx%d symbol takes the stream to %d pixels in %d bytes; "+
			"the budget for one stream is %d pixels in %d bytes",
			w, h, b.pixels, b.bmBytes, int64(MaxPagePixels), int64(maxStreamBitmapBytes))
	}
	return nil
}

// chargePlacement charges one symbol instance's area, and counts it into the
// package's decode counter: placing a symbol is per-pixel work the counter
// exists to make visible, even though it is not an MQ decision.
func (b *streamBudget) chargePlacement(w, h int64) error {
	b.placement += w * h
	if b.placement > b.placementLimit {
		return fmt.Errorf("jbig2: the stream's text regions want to draw %d pixels of symbol onto a page "+
			"that can show %d; the budget admits %dx the page plus %d pixels, and everything past that is "+
			"drawn over or off the edge", b.placement, b.pagePixels,
			int64(maxRegionOverdraw), int64(overdrawFloorPixels))
	}
	decodedPixels.Add(w * h)
	return nil
}
