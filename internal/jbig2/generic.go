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
