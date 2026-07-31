package jbig2

import (
	"bytes"
	"testing"
)

// Each template position must map to exactly one context bit, in reading order
// with MSB = top-left. A single set pixel at that position must produce exactly
// that bit and nothing else.
func TestContextTemplate0BitPositions(t *testing.T) {
	cases := []struct {
		dx, dy int
		bit    uint
	}{
		{-2, -2, 15}, {-1, -2, 14}, {0, -2, 13}, {1, -2, 12}, {2, -2, 11},
		{-3, -1, 10}, {-2, -1, 9}, {-1, -1, 8}, {0, -1, 7},
		{1, -1, 6}, {2, -1, 5}, {3, -1, 4},
		{-4, 0, 3}, {-3, 0, 2}, {-2, 0, 1}, {-1, 0, 0},
	}
	for _, c := range cases {
		b := NewBitmap(12, 8)
		b.Set(5+c.dx, 4+c.dy, 1)
		got := contextTemplate0(b, 5, 4)
		want := 1 << c.bit
		if got != want {
			t.Errorf("template pixel (%d,%d): context = %#04x; want %#04x (bit %d)",
				c.dx, c.dy, got, want, c.bit)
		}
	}
}

func TestContextTemplate0AllOnesInterior(t *testing.T) {
	b := NewBitmap(9, 5)
	for y := 0; y < 5; y++ {
		for x := 0; x < 9; x++ {
			b.Set(x, y, 1)
		}
	}
	if got := contextTemplate0(b, 4, 3); got != 0xFFFF {
		t.Errorf("interior context on an all-ink bitmap = %#04x; want 0xffff", got)
	}
}

// At the top-left corner every template pixel is out of bounds, so the context
// is 0 even on an all-ink bitmap (T.88 6.2.5.2).
func TestContextTemplate0CornerIsZero(t *testing.T) {
	b := NewBitmap(9, 5)
	for y := 0; y < 5; y++ {
		for x := 0; x < 9; x++ {
			b.Set(x, y, 1)
		}
	}
	if got := contextTemplate0(b, 0, 0); got != 0 {
		t.Errorf("corner context = %#04x; want 0x0000", got)
	}
}

// T.88 Figure 8 gives the SLTP context for GBTEMPLATE 0 as a picture in reading
// order. Decomposed into the three template runs it reads 10011 / 0110010 /
// 0101, which is 0x9B25.
func TestSLTPContextDecomposition(t *testing.T) {
	if sltpContextTemplate0 != 0x9B25 {
		t.Fatalf("sltpContextTemplate0 = %#04x; want 0x9b25", sltpContextTemplate0)
	}
	row2 := (sltpContextTemplate0 >> 11) & 0x1F
	row1 := (sltpContextTemplate0 >> 4) & 0x7F
	row0 := sltpContextTemplate0 & 0x0F
	if row2 != 0b10011 {
		t.Errorf("SLTP row y-2 = %05b; want 10011", row2)
	}
	if row1 != 0b0110010 {
		t.Errorf("SLTP row y-1 = %07b; want 0110010", row1)
	}
	if row0 != 0b0101 {
		t.Errorf("SLTP row y = %04b; want 0101", row0)
	}
}

// TestEncodeGenericRegionAnnexH1 is the T.88 Annex H.1 segment 11 conformance
// vector (doc p. 135): a 54x44 bitmap coded with GBTEMPLATE 0, TPGDON = 1 and
// nominal AT pixels produces exactly these nine bytes.
//
// This single assertion pins the context bit order, the SLTP context value, the
// TPGD state machine, the out-of-bounds rule and the MQ flush convention all at
// once. If it fails, one of those five is wrong -- and the MQ coder is already
// proven by Task 2, so it is one of the other four.
func TestEncodeGenericRegionAnnexH1(t *testing.T) {
	want := []byte{0x04, 0xEE, 0xED, 0x87, 0xFB, 0xCB, 0x2B, 0xFF, 0xAC}
	got := EncodeGenericRegion(figureH6(), true)
	if !bytes.Equal(got, want) {
		t.Fatalf("region data mismatch\ngot  (%d): % 02X\nwant (%d): % 02X",
			len(got), got, len(want), want)
	}
}

// TPGD is the whole reason this encoder is worth building for scanned pages.
// On Figure H.6, whose 40 interior rows are all identical, it is a better than
// 2x saving.
func TestTPGDShrinksRepeatedRows(t *testing.T) {
	b := figureH6()
	with := len(EncodeGenericRegion(b, true))
	without := len(EncodeGenericRegion(b, false))
	if with != 9 {
		t.Errorf("TPGDON size = %d; want 9", with)
	}
	if without != 20 {
		t.Errorf("TPGDOFF size = %d; want 20", without)
	}
	if with >= without {
		t.Errorf("TPGDON (%d bytes) did not beat TPGDOFF (%d bytes)", with, without)
	}
}

// An all-background bitmap is every row equal to the one above, so TPGD codes
// one bit per row and nothing else.
func TestEncodeGenericRegionAllBackground(t *testing.T) {
	got := EncodeGenericRegion(NewBitmap(16, 4), true)
	want := []byte{0xB3, 0xFF, 0xAC}
	if !bytes.Equal(got, want) {
		t.Fatalf("all-background 16x4 = % 02X; want % 02X", got, want)
	}
}

// Stray padding bits past the row width must not change the output. They are
// invisible to Get but visible to the whole-byte row comparison behind TPGD.
func TestEncodeGenericRegionIgnoresPaddingBits(t *testing.T) {
	clean := noiseBitmap(13, 11, 99)
	dirty := noiseBitmap(13, 11, 99)
	for y := 0; y < dirty.H; y++ {
		dirty.Pix[y*dirty.Stride+dirty.Stride-1] |= 0x07 // bits 13,14,15
	}
	if bytes.Equal(clean.Pix, dirty.Pix) {
		t.Fatal("test setup failed: padding bits were not actually set")
	}
	if a, b := EncodeGenericRegion(clean, true), EncodeGenericRegion(dirty, true); !bytes.Equal(a, b) {
		t.Errorf("padding bits changed the encoding:\nclean % 02X\ndirty % 02X", a, b)
	}
}

// A Stride larger than the minimal (W+7)/8 is accepted (EncodeJBIG2Generic
// rejects only a Stride that is too small). The trailing bytes it leaves are
// invisible to Get but visible to the whole-stride comparison behind TPGD, so
// leaving them dirty silently costs compression while the round trip stays
// lossless -- the worst kind of bug. Measured before MaskPadding was taught to
// clear them: 28 bytes here instead of 12.
func TestEncodeGenericRegionIgnoresNonMinimalStride(t *testing.T) {
	const w, h = 100, 200
	build := func(stride int, junk bool) *Bitmap {
		b := &Bitmap{W: w, H: h, Stride: stride, Pix: make([]byte, stride*h)}
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				if y < 3 || y >= h-3 || x < 3 || x >= w-3 {
					b.Set(x, y, 1)
				}
			}
			if junk {
				for i := (w + 7) / 8; i < stride; i++ {
					b.Pix[y*stride+i] = byte(0xA5 + y)
				}
			}
		}
		return b
	}
	want := EncodeGenericRegion(build((w+7)/8, false), true)
	got := EncodeGenericRegion(build(16, true), true)
	if !bytes.Equal(got, want) {
		t.Errorf("a non-minimal stride changed the encoding: %d bytes vs %d bytes\ngot  % 02X\nwant % 02X",
			len(got), len(want), got, want)
	}
}

// Every fixture must encode without panicking and produce a well-formed stream.
func TestEncodeGenericRegionCorpusWellFormed(t *testing.T) {
	for name, b := range fixtureBitmaps() {
		got := EncodeGenericRegion(b, true)
		if len(got) < 2 {
			t.Errorf("%s: encoded to %d bytes; want at least 2", name, len(got))
			continue
		}
		if got[len(got)-2] != 0xFF || got[len(got)-1] != 0xAC {
			t.Errorf("%s: stream tail = % 02X; want ... FF AC", name, got[len(got)-2:])
		}
	}
}
