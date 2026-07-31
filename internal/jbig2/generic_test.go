package jbig2

import "testing"

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
