package jbig2

// sltpContextTemplate0 is the fixed context used to code the SLTP bit under
// TPGDON with GBTEMPLATE 0 (T.88 6.2.5.7, Figure 8). It is read in the same
// reading order as contextTemplate0, and it always uses the nominal template
// regardless of where the AT pixels actually sit.
const sltpContextTemplate0 = 0x9B25

// contextTemplate0 forms the GBTEMPLATE 0 context for the pixel at (x, y) with
// the nominal AT pixels of T.88 Table 5:
//
//	A1 = (3, -1)   A2 = (-3, -1)   A3 = (2, -2)   A4 = (-2, -2)
//
// which collapses the 16-pixel template into three contiguous runs:
//
//	row y-2:  x-2 .. x+2      (5 pixels, context bits 15..11)
//	row y-1:  x-3 .. x+3      (7 pixels, context bits 10..4)
//	row y:    x-4 .. x-1      (4 pixels, context bits  3..0)
//
// Bits are gathered in reading order with MSB = top-left. Out-of-bounds pixels
// read as 0 via Bitmap.Get, as T.88 6.2.5.2 requires.
func contextTemplate0(b *Bitmap, x, y int) int {
	cx := 0
	for dx := -2; dx <= 2; dx++ {
		cx = cx<<1 | b.Get(x+dx, y-2)
	}
	for dx := -3; dx <= 3; dx++ {
		cx = cx<<1 | b.Get(x+dx, y-1)
	}
	for dx := -4; dx <= -1; dx++ {
		cx = cx<<1 | b.Get(x+dx, y)
	}
	return cx
}

// EncodeGenericRegion codes b as an arithmetic generic region using
// GBTEMPLATE 0 with nominal AT pixels, and returns the MQ-coded region data.
//
// The coding is lossless: a conforming decoder reconstructs b exactly. That is
// a property of the algorithm, not of the parameters -- there is no setting of
// this function that can substitute one pixel for another.
//
// When tpgdon is true, each row is preceded by an SLTP bit coded in the fixed
// context sltpContextTemplate0; a row identical to the one above is then not
// coded at all (T.88 6.2.5.5, 6.2.5.7 step 3b). This is a large win on scanned
// pages and a small loss (about two bytes) on incompressible noise.
//
// The context array is allocated fresh on every call, which is what T.88
// 7.4.6.4 step 2 requires: arithmetic coding statistics are reset to zero at
// the start of every generic region segment, never carried across segments.
func EncodeGenericRegion(b *Bitmap, tpgdon bool) []byte {
	b.MaskPadding()

	cx := make(contexts, 1<<16)
	e := newEncoder()

	ltp := 0
	for y := 0; y < b.H; y++ {
		if tpgdon {
			next := 0
			if b.RowEqualAbove(y) {
				next = 1
			}
			e.encode(cx, sltpContextTemplate0, next^ltp)
			ltp = next
			if ltp == 1 {
				continue
			}
		}
		for x := 0; x < b.W; x++ {
			e.encode(cx, contextTemplate0(b, x, y), b.Get(x, y))
		}
	}
	return e.flush()
}
