package byblos

import (
	"image"
	"testing"
)

func TestNewBitmapStride(t *testing.T) {
	for _, tc := range []struct{ w, wantStride int }{
		{1, 1}, {7, 1}, {8, 1}, {9, 2}, {16, 2}, {17, 3},
	} {
		b := NewBitmap(tc.w, 3)
		if b.Stride != tc.wantStride {
			t.Errorf("NewBitmap(%d, 3).Stride = %d; want %d", tc.w, b.Stride, tc.wantStride)
		}
		if len(b.Pix) != tc.wantStride*3 {
			t.Errorf("NewBitmap(%d, 3) len(Pix) = %d; want %d", tc.w, len(b.Pix), tc.wantStride*3)
		}
	}
}

// Rows are packed MSB-first: pixel (x, y) is bit 7-(x%8) of Pix[y*Stride+x/8].
func TestBitmapPackingIsMSBFirst(t *testing.T) {
	b := NewBitmap(9, 2)
	b.Set(0, 0, 1) // bit 7 of byte 0
	b.Set(7, 0, 1) // bit 0 of byte 0
	b.Set(8, 0, 1) // bit 7 of byte 1
	b.Set(1, 1, 1) // bit 6 of byte 2

	want := []byte{0x81, 0x80, 0x40, 0x00}
	for i := range want {
		if b.Pix[i] != want[i] {
			t.Fatalf("Pix = % 02x; want % 02x", b.Pix, want)
		}
	}
}

func TestBitmapAtSetRoundTrip(t *testing.T) {
	b := NewBitmap(13, 5)
	set := [][2]int{{0, 0}, {12, 4}, {6, 2}, {8, 1}}
	for _, p := range set {
		b.Set(p[0], p[1], 1)
	}
	for _, p := range set {
		if got := b.At(p[0], p[1]); got != 1 {
			t.Errorf("At(%d, %d) = %d; want 1", p[0], p[1], got)
		}
	}
	if got := b.At(1, 0); got != 0 {
		t.Errorf("At(1, 0) = %d; want 0", got)
	}
	b.Set(0, 0, 0)
	if got := b.At(0, 0); got != 0 {
		t.Errorf("At(0, 0) after clearing = %d; want 0", got)
	}
}

// Out-of-bounds reads return 0. This is not defensive padding: JBIG2 T.88
// section 6.2.5.2 requires template pixels outside the bitmap to read as 0, so
// the encoder in B2 relies on exactly this behaviour.
func TestBitmapOutOfBoundsReadsZero(t *testing.T) {
	b := NewBitmap(4, 4)
	for x := 0; x < 4; x++ {
		for y := 0; y < 4; y++ {
			b.Set(x, y, 1)
		}
	}
	for _, p := range [][2]int{{-1, 0}, {0, -1}, {4, 0}, {0, 4}, {-3, -3}, {99, 99}} {
		if got := b.At(p[0], p[1]); got != 0 {
			t.Errorf("At(%d, %d) out of bounds = %d; want 0", p[0], p[1], got)
		}
	}
}

func TestBitmapOutOfBoundsWriteIsNoOp(t *testing.T) {
	b := NewBitmap(4, 4)
	for _, p := range [][2]int{{-1, 0}, {0, -1}, {4, 0}, {0, 4}} {
		b.Set(p[0], p[1], 1) // must not panic
	}
	for _, v := range b.Pix {
		if v != 0 {
			t.Fatalf("out-of-bounds Set wrote into Pix: % 02x", b.Pix)
		}
	}
}

// Set must never touch the padding bits past Width in the last byte of a row,
// because Equal compares the packed bytes directly.
func TestBitmapPaddingBitsRemainZero(t *testing.T) {
	b := NewBitmap(9, 1)
	for x := 0; x < 9; x++ {
		b.Set(x, 0, 1)
	}
	if b.Pix[1] != 0x80 {
		t.Errorf("Pix[1] = %02x; want 80 (only bit 7 set, padding clear)", b.Pix[1])
	}
}

func TestBitmapBounds(t *testing.T) {
	b := NewBitmap(11, 7)
	if got, want := b.Bounds(), image.Rect(0, 0, 11, 7); got != want {
		t.Errorf("Bounds() = %v; want %v", got, want)
	}
}

func TestBitmapCloneIsIndependent(t *testing.T) {
	b := NewBitmap(8, 2)
	b.Set(3, 1, 1)
	c := b.Clone()
	if !b.Equal(c) {
		t.Fatal("Clone() is not Equal to its source")
	}
	c.Set(0, 0, 1)
	if b.At(0, 0) != 0 {
		t.Error("mutating the clone changed the source")
	}
	if b.Equal(c) {
		t.Error("Equal() reported equality after the clone diverged")
	}
}

func TestBitmapEqualRejectsDifferentSizes(t *testing.T) {
	if NewBitmap(8, 2).Equal(NewBitmap(8, 3)) {
		t.Error("Equal() = true for 8x2 vs 8x3; want false")
	}
	if NewBitmap(8, 2).Equal(nil) {
		t.Error("Equal(nil) = true; want false")
	}
}
