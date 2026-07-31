package jbig2

import (
	"bytes"
	"testing"
)

func TestBitmapSetGetRoundTrip(t *testing.T) {
	b := NewBitmap(13, 5)
	if b.Stride != 2 {
		t.Fatalf("Stride = %d; want 2 for width 13", b.Stride)
	}
	if len(b.Pix) != 10 {
		t.Fatalf("len(Pix) = %d; want 10", len(b.Pix))
	}
	pts := [][2]int{{0, 0}, {12, 4}, {7, 2}, {8, 0}}
	for _, p := range pts {
		b.Set(p[0], p[1], 1)
	}
	for _, p := range pts {
		if got := b.Get(p[0], p[1]); got != 1 {
			t.Errorf("Get(%d,%d) = %d; want 1", p[0], p[1], got)
		}
	}
	if got := b.Get(1, 1); got != 0 {
		t.Errorf("Get(1,1) = %d; want 0", got)
	}
	b.Set(7, 2, 0)
	if got := b.Get(7, 2); got != 0 {
		t.Errorf("Get(7,2) after clear = %d; want 0", got)
	}
}

// T.88 6.2.5.2: pixels outside the bitmap are 0. No replication, no wrap.
func TestBitmapOutOfBoundsIsZero(t *testing.T) {
	b := NewBitmap(4, 4)
	for y := 0; y < 4; y++ {
		for x := 0; x < 4; x++ {
			b.Set(x, y, 1)
		}
	}
	for _, p := range [][2]int{{-1, 0}, {0, -1}, {4, 0}, {0, 4}, {-1, -1}, {99, 99}} {
		if got := b.Get(p[0], p[1]); got != 0 {
			t.Errorf("Get(%d,%d) = %d; want 0 out of bounds", p[0], p[1], got)
		}
	}
}

// MSB-first packing: pixel 0 is bit 0x80 of byte 0.
func TestBitmapPackingIsMSBFirst(t *testing.T) {
	b := NewBitmap(8, 1)
	b.Set(0, 0, 1)
	if b.Pix[0] != 0x80 {
		t.Errorf("Pix[0] = %#02x after Set(0,0,1); want 0x80", b.Pix[0])
	}
	b.Set(7, 0, 1)
	if b.Pix[0] != 0x81 {
		t.Errorf("Pix[0] = %#02x after Set(7,0,1); want 0x81", b.Pix[0])
	}
}

func TestBitmapMaskPaddingClearsBitsPastWidth(t *testing.T) {
	b := NewBitmap(5, 2)
	b.Pix[0] = 0xFF
	b.Pix[1] = 0xFF
	b.MaskPadding()
	if b.Pix[0] != 0xF8 {
		t.Errorf("Pix[0] = %#02x after MaskPadding; want 0xf8 (5 pixels wide)", b.Pix[0])
	}
	if b.Pix[1] != 0xF8 {
		t.Errorf("Pix[1] = %#02x after MaskPadding; want 0xf8", b.Pix[1])
	}
	for x := 0; x < 5; x++ {
		if b.Get(x, 0) != 1 {
			t.Errorf("MaskPadding cleared visible pixel (%d,0)", x)
		}
	}
}

// A Stride larger than the minimal (W+7)/8 leaves whole bytes past the last
// visible pixel. Get never reads them, so they cost no correctness -- but
// RowEqualAbove compares whole strides, so leaving them dirty silently stops
// TPGD from firing. MaskPadding must clear them as well as the padding bits
// inside the byte that holds pixel W-1.
func TestBitmapMaskPaddingHandlesNonMinimalStride(t *testing.T) {
	b := &Bitmap{W: 12, H: 2, Stride: 4, Pix: []byte{
		0xFF, 0xFF, 0xFF, 0xFF,
		0xFF, 0xFF, 0xFF, 0xFF,
	}}
	b.MaskPadding()
	want := []byte{0xFF, 0xF0, 0x00, 0x00, 0xFF, 0xF0, 0x00, 0x00}
	if !bytes.Equal(b.Pix, want) {
		t.Fatalf("MaskPadding on a stride-4 12-pixel-wide bitmap = % 02X; want % 02X", b.Pix, want)
	}
	for x := 0; x < 12; x++ {
		if b.Get(x, 0) != 1 {
			t.Errorf("MaskPadding cleared visible pixel (%d,0)", x)
		}
	}
	if !b.RowEqualAbove(1) {
		t.Error("RowEqualAbove(1) = false; want true once MaskPadding has equalised the strides")
	}
}

// Row 0 is compared against an implicit all-zero row above the bitmap.
func TestBitmapRowEqualAbove(t *testing.T) {
	b := NewBitmap(9, 4)
	// row 0 all zero, row 1 all zero, row 2 has a pixel, row 3 same as row 2.
	b.Set(3, 2, 1)
	b.Set(3, 3, 1)
	want := []bool{true, true, false, true}
	for y, w := range want {
		if got := b.RowEqualAbove(y); got != w {
			t.Errorf("RowEqualAbove(%d) = %v; want %v", y, got, w)
		}
	}
}

func TestNewBitmapRejectsNonPositiveDimensions(t *testing.T) {
	for _, d := range [][2]int{{0, 5}, {5, 0}, {-1, 5}} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("NewBitmap(%d,%d): want panic, got none", d[0], d[1])
				}
			}()
			NewBitmap(d[0], d[1])
		}()
	}
}
