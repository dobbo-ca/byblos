package byblos

import (
	"bytes"
	"image"
)

// Bitmap is a 1-bit-per-pixel bilevel image owned by Byblos.
//
// Byblos deliberately does not share this type with Cadmus: neither library
// imports the other (design spec section 3), so each owns its own substrate.
//
// A set bit (1) is BLACK ink. That matches JBIG2, where 1 is black, and is the
// inverse of PDF /DeviceGray, where 1 is white; any conversion across that
// boundary inverts.
//
// The origin is top-left and y increases downward, matching image.Rectangle.
// Note that PageInfo.Bounds uses the opposite (PDF) convention.
//
// Rows are packed MSB-first: pixel (x, y) is bit 7-(x%8) of
// Pix[y*Stride + x/8]. Bits past Width in the final byte of a row are always
// zero, and Set preserves that, so Equal may compare Pix directly.
type Bitmap struct {
	Width, Height int
	Stride        int // bytes per row
	Pix           []byte
}

// NewBitmap returns a w x h bitmap with every pixel clear. It panics on a
// negative dimension.
func NewBitmap(w, h int) *Bitmap {
	if w < 0 || h < 0 {
		panic("byblos: NewBitmap with a negative dimension")
	}
	stride := (w + 7) / 8
	return &Bitmap{Width: w, Height: h, Stride: stride, Pix: make([]byte, stride*h)}
}

// At returns 1 or 0. Pixels outside the bitmap read as 0, as required by
// ITU-T T.88 section 6.2.5.2 for JBIG2 template gathering.
func (b *Bitmap) At(x, y int) uint8 {
	if x < 0 || y < 0 || x >= b.Width || y >= b.Height {
		return 0
	}
	return (b.Pix[y*b.Stride+x/8] >> (7 - uint(x)%8)) & 1
}

// Set writes 1 when v is non-zero and 0 otherwise. Coordinates outside the
// bitmap are ignored.
func (b *Bitmap) Set(x, y int, v uint8) {
	if x < 0 || y < 0 || x >= b.Width || y >= b.Height {
		return
	}
	i := y*b.Stride + x/8
	mask := byte(1) << (7 - uint(x)%8)
	if v != 0 {
		b.Pix[i] |= mask
	} else {
		b.Pix[i] &^= mask
	}
}

// Bounds returns the bitmap's extent with the origin at (0, 0).
func (b *Bitmap) Bounds() image.Rectangle { return image.Rect(0, 0, b.Width, b.Height) }

// Clone returns a deep copy.
func (b *Bitmap) Clone() *Bitmap {
	c := &Bitmap{Width: b.Width, Height: b.Height, Stride: b.Stride, Pix: make([]byte, len(b.Pix))}
	copy(c.Pix, b.Pix)
	return c
}

// Equal reports whether o has the same dimensions and the same pixels. It is
// the lossless check the JBIG2 round-trip test in B2 is built on.
func (b *Bitmap) Equal(o *Bitmap) bool {
	if o == nil || b.Width != o.Width || b.Height != o.Height {
		return false
	}
	return bytes.Equal(b.Pix, o.Pix)
}
