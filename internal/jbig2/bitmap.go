package jbig2

import "fmt"

// Bitmap is a 1-bit-per-pixel bitmap with MSB-first packed rows: pixel x of a
// row lives in bit 0x80>>(x%8) of byte x/8. A set bit is ink (black), which is
// also JBIG2's convention, so no inversion happens anywhere in this package.
//
// Bits past W in the final byte of a row are padding and must be zero; use
// MaskPadding to enforce that on a bitmap built by other means.
type Bitmap struct {
	W, H   int
	Stride int
	Pix    []byte
}

// NewBitmap returns an all-background bitmap. It panics on non-positive
// dimensions: a zero-pixel region is not representable in a JBIG2 region
// segment, and silently producing one would emit an undecodable stream.
func NewBitmap(w, h int) *Bitmap {
	if w <= 0 || h <= 0 {
		panic(fmt.Sprintf("jbig2: NewBitmap(%d, %d): dimensions must be positive", w, h))
	}
	s := (w + 7) / 8
	return &Bitmap{W: w, H: h, Stride: s, Pix: make([]byte, s*h)}
}

// Get returns the pixel at (x, y), or 0 if (x, y) lies outside the bitmap.
// T.88 6.2.5.2 requires out-of-bounds template pixels to read as 0, with no
// edge replication and no wrapping; returning 0 here is what implements that.
func (b *Bitmap) Get(x, y int) int {
	if x < 0 || y < 0 || x >= b.W || y >= b.H {
		return 0
	}
	return int(b.Pix[y*b.Stride+x/8]>>(7-uint(x)%8)) & 1
}

// Set writes the pixel at (x, y). Coordinates must be in bounds.
func (b *Bitmap) Set(x, y, v int) {
	i := y*b.Stride + x/8
	m := byte(0x80 >> (uint(x) % 8))
	if v != 0 {
		b.Pix[i] |= m
	} else {
		b.Pix[i] &^= m
	}
}

// MaskPadding zeroes everything in a row past the last visible pixel: the
// padding bits inside the byte that holds pixel W-1, and every whole byte
// between there and Stride. Those trailing bytes exist whenever Stride is
// larger than the minimal (W+7)/8, which this package accepts.
//
// Get never reads any of it, so none of it can cost correctness. RowEqualAbove
// compares whole strides, though, so stray bits there make two visually
// identical rows compare unequal and TPGD stops firing. Measured on a 100x200
// bordered bitmap with Stride 16: 12 bytes with the trailing bytes cleared,
// 28 bytes with them left dirty.
func (b *Bitmap) MaskPadding() {
	last := (b.W - 1) / 8 // the byte holding the last visible pixel
	mask := byte(0xFF)
	if rem := b.W % 8; rem != 0 {
		mask = byte(0xFF << (8 - uint(rem)))
	}
	for y := 0; y < b.H; y++ {
		row := b.Pix[y*b.Stride : (y+1)*b.Stride]
		row[last] &= mask
		clear(row[last+1:])
	}
}

// RowEqualAbove reports whether row y is identical to row y-1. Row 0 is
// compared against the implicit all-zero row above the bitmap, matching the
// out-of-bounds rule. This is the predicate that drives TPGD.
func (b *Bitmap) RowEqualAbove(y int) bool {
	cur := b.Pix[y*b.Stride : (y+1)*b.Stride]
	if y == 0 {
		for _, v := range cur {
			if v != 0 {
				return false
			}
		}
		return true
	}
	prev := b.Pix[(y-1)*b.Stride : y*b.Stride]
	for i := range prev {
		if prev[i] != cur[i] {
			return false
		}
	}
	return true
}
