package byblos

import (
	"bytes"
	"runtime/debug"
	"slices"
	"strings"
	"testing"
)

// borderBitmap is T.88 Figure H.6 built through the public Bitmap type: a 54x44
// rectangle with a 2-pixel ink border.
func borderBitmap() *Bitmap {
	const w, h = 54, 44
	stride := (w + 7) / 8
	b := &Bitmap{Width: w, Height: h, Stride: stride, Pix: make([]byte, stride*h)}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			if y < 2 || y >= h-2 || x < 2 || x >= w-2 {
				b.Pix[y*stride+x/8] |= 0x80 >> (uint(x) % 8)
			}
		}
	}
	return b
}

// The public API must reach the T.88 Annex H.1 region data through the adapter.
// This is what catches a bit-order or ink-convention mismatch between the root
// Bitmap and internal/jbig2: any such mismatch changes these bytes.
func TestEncodeJBIG2GenericReachesSpecVector(t *testing.T) {
	got, err := EncodeJBIG2Generic(borderBitmap())
	if err != nil {
		t.Fatalf("EncodeJBIG2Generic() error = %v", err)
	}
	want := []byte{0x04, 0xEE, 0xED, 0x87, 0xFB, 0xCB, 0x2B, 0xFF, 0xAC}
	if !bytes.HasSuffix(got, want) {
		t.Fatalf("stream does not end with the T.88 Annex H.1 region data\ngot tail: % 02X\nwant:     % 02X",
			got[max(0, len(got)-len(want)):], want)
	}
}

func TestEncodeJBIG2GenericRejectsEmptyBitmap(t *testing.T) {
	if _, err := EncodeJBIG2Generic(&Bitmap{Width: 0, Height: 0}); err == nil {
		t.Fatal("EncodeJBIG2Generic() on a 0x0 bitmap: want error, got nil")
	}
}

func TestEncodeJBIG2GenericRejectsShortPixSlice(t *testing.T) {
	b := &Bitmap{Width: 16, Height: 4, Stride: 2, Pix: make([]byte, 3)}
	if _, err := EncodeJBIG2Generic(b); err == nil {
		t.Fatal("EncodeJBIG2Generic() with a truncated Pix: want error, got nil")
	}
}

// The capability string is what UpgradeCandidates keys on (design spec section
// 6), so it is API surface and must not drift.
func TestCapabilityStringIsStable(t *testing.T) {
	if CapabilityJBIG2Generic != "jbig2-generic" {
		t.Errorf("CapabilityJBIG2Generic = %q; want %q", CapabilityJBIG2Generic, "jbig2-generic")
	}
	if slices.Contains(Capabilities(), "jbig2-symbol") {
		t.Error("Capabilities() advertises jbig2-symbol, which this build does not implement")
	}
	if !slices.Contains(Capabilities(), CapabilityJBIG2Generic) {
		t.Errorf("Capabilities() = %v; want it to contain %q", Capabilities(), CapabilityJBIG2Generic)
	}
}

// encodeJBIG2GenericNoPanic calls EncodeJBIG2Generic and turns a panic into a
// failure of the calling test rather than a crashed test binary, the same way
// decodeJBIG2GenericNoPanic does for the read side.
//
// It is needed here for the same reason: the guards below are the difference
// between an error and a runtime panic, so a test that only checked "err != nil"
// would not run at all once the guard is gone -- the process dies first, taking
// every other test in the package with it and hiding which one was under test.
func encodeJBIG2GenericNoPanic(t *testing.T, b *Bitmap) (out []byte, err error) {
	t.Helper()
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("EncodeJBIG2Generic PANICKED: %v\n\n%s", r, debug.Stack())
		}
	}()
	return EncodeJBIG2Generic(b)
}

// TestEncodeJBIG2GenericRejectsANilBitmapInsteadOfPanicking pins the first
// statement of the exported encoder, which no test in the tree could detect the
// deletion of.
//
// EncodeJBIG2Generic is API surface: it took a *Bitmap in v0.1.0 and callers pin
// that tag, so a nil is a value a caller can hand it -- from a map lookup that
// missed, a decode that returned (nil, err) whose error was logged and dropped,
// or a slice element never filled in. Delete "if b == nil" and the very next
// line dereferences it:
//
//	panic: runtime error: invalid memory address or nil pointer dereference
//
// A panic is the one failure mode this API cannot have. A caller can route
// around an error and cannot route around a crashed process, and byblos runs
// inside batch archival tooling where one bad page must not take the run down.
func TestEncodeJBIG2GenericRejectsANilBitmapInsteadOfPanicking(t *testing.T) {
	got, err := encodeJBIG2GenericNoPanic(t, nil)
	if err == nil {
		t.Fatalf("EncodeJBIG2Generic(nil) = %d bytes, err = nil; want an error", len(got))
	}
	if !strings.Contains(err.Error(), "nil bitmap") {
		t.Errorf("error = %v; want it to name the nil bitmap. The message is what tells a "+
			"caller the argument was missing rather than malformed", err)
	}
}

// TestEncodeJBIG2GenericRejectsAStrideTooSmallForTheWidthInsteadOfPanicking
// pins the stride check, which is a MEMORY SAFETY guard and not a tidiness one.
//
// Bitmap.Set indexes Pix[y*Stride + x/8] with no bounds check of its own, by
// design -- it is the inner loop of every raster operation in this package. The
// only thing standing between a caller's Stride and an out-of-range write is
// this check. Delete it and a 16-pixel-wide bitmap declaring Stride 1, whose Pix
// is exactly Stride*Height and so passes the length check below it, reaches the
// encoder's padding mask and panics:
//
//	panic: runtime error: index out of range [1] with length 1
//
// The shape is not exotic. Stride 1 for width 16 is the arithmetic slip of
// writing Width/8 instead of (Width+7)/8 and then getting the width wrong too,
// and it is exactly the field a caller building a Bitmap by hand fills in
// themselves -- Bitmap's own fields are exported.
//
// Note that the existing short-Pix test cannot reach this: it declares Stride 2,
// which is correct for width 16, and is refused one check later on Pix's length.
// A bitmap whose Pix is long enough for the stride it declares gets past that
// one entirely.
//
// TWO WIDTHS, and the second one is the point. A width of 16 cannot tell
// (Width+7)/8 from Width/8 -- they are both 2 -- so a check rewritten as the
// second form passes a test built only on multiples of 8 and then admits every
// width that is not one. Width 17 separates them: (17+7)/8 is 3 and 17/8 is 2,
// so Stride 2 is a byte short of row 17's last pixel and MaskPadding indexes
// row[2] of a two-byte row.
func TestEncodeJBIG2GenericRejectsAStrideTooSmallForTheWidthInsteadOfPanicking(t *testing.T) {
	for _, b := range []*Bitmap{
		// Stride 1 for a 16-pixel width; (16+7)/8 is 2. Pix is Stride*Height
		// bytes, so every check other than the stride one is satisfied.
		{Width: 16, Height: 4, Stride: 1, Pix: make([]byte, 4)},
		// Stride 2 for a 17-pixel width: correct under Width/8, one byte short
		// under the (Width+7)/8 this format actually needs.
		{Width: 17, Height: 4, Stride: 2, Pix: make([]byte, 8)},
	} {
		got, err := encodeJBIG2GenericNoPanic(t, b)
		if err == nil {
			t.Fatalf("EncodeJBIG2Generic on a %dx%d bitmap with stride %d = %d bytes, err = nil; "+
				"want an error. (%d+7)/8 is %d, so row 0 alone runs past its own stride.",
				b.Width, b.Height, b.Stride, len(got), b.Width, (b.Width+7)/8)
		}
		if !strings.Contains(err.Error(), "stride") {
			t.Errorf("%dx%d stride %d: error = %v; want it to name the stride. Every field of "+
				"Bitmap is exported and a caller who filled this one in wrongly needs to be "+
				"told which one it was", b.Width, b.Height, b.Stride, err)
		}
	}
}

// TestEncodeJBIG2GenericRejectsAPixSliceOneByteShort pins the Pix length check
// at its BOUNDARY, which is the direction the existing short-Pix test leaves
// open.
//
// TestEncodeJBIG2GenericRejectsShortPixSlice hands it 3 bytes where 8 are
// wanted. That refuses under "len < want" and under "len < want-1" and under
// anything else in between, so it pins that a check exists and not where it
// sits. One byte short is the only length that separates them, and it is also
// the only one a caller reaches by accident -- an off-by-one in their own
// (Width+7)/8, or a slice built for Height-1 rows.
//
// Off by one, the encoder walks off the end of the last row: MaskPadding takes
// Pix[y*Stride:(y+1)*Stride] for every row, and on the last one that is a slice
// bound past the allocation.
//
//	panic: runtime error: slice bounds out of range [:8] with capacity 7
func TestEncodeJBIG2GenericRejectsAPixSliceOneByteShort(t *testing.T) {
	// Stride 2 is correct for width 16, so the stride check above passes and
	// this is the only guard left between the caller and that slice bound.
	b := &Bitmap{Width: 16, Height: 4, Stride: 2, Pix: make([]byte, 2*4-1)}
	got, err := encodeJBIG2GenericNoPanic(t, b)
	if err == nil {
		t.Fatalf("EncodeJBIG2Generic on a %dx%d bitmap with stride %d and %d bytes of Pix = "+
			"%d bytes, err = nil; want an error. %d*%d is %d, so the last row is a byte short.",
			b.Width, b.Height, b.Stride, len(b.Pix), len(got), b.Stride, b.Height, b.Stride*b.Height)
	}
	if !strings.Contains(err.Error(), "want at least") {
		t.Errorf("error = %v; want it to name the length Pix should have had", err)
	}
}

// TestEncodeJBIG2GenericAttributesItsOwnRefusals pins the positive-dimension
// guard, which is the one guard in this function that NOTHING could detect the
// deletion of -- neither half of it, nor both halves together.
//
// jbig2.EmbeddedStream carries the same check one layer down, and round 7 pinned
// it there (TestEmbeddedStreamRejectsAZeroDimension). So with this one deleted a
// zero or negative dimension is still refused, still with no panic and no silent
// accept, and the ONLY thing that changes is who the error says it came from:
//
//	tip:    byblos: EncodeJBIG2Generic: bitmap is 0x4; dimensions must be positive
//	mutant: jbig2: bitmap is 0x4; dimensions must be positive
//
// That is worth a test rather than a shrug. "jbig2:" is the prefix this package
// reserves for errors out of the CODEC, and decodeJBIG2Placement's doc comment
// says so explicitly -- it is what lets extractPage wrap a decode failure
// without naming the codec twice. An error from the exported encoder that
// arrives wearing the codec's prefix tells a caller their bytes were bad when
// what was actually bad was the argument they passed, and the two lead to
// different fixes.
//
// Negative as well as zero, and both axes, because the guard is one expression
// with two halves and either half can go on its own.
func TestEncodeJBIG2GenericAttributesItsOwnRefusals(t *testing.T) {
	for _, b := range []*Bitmap{
		{Width: 0, Height: 4, Stride: 2, Pix: make([]byte, 8)},
		{Width: 16, Height: 0, Stride: 2, Pix: nil},
		{Width: -1, Height: 4, Stride: 2, Pix: make([]byte, 8)},
		{Width: 16, Height: -1, Stride: 2, Pix: make([]byte, 8)},
	} {
		got, err := encodeJBIG2GenericNoPanic(t, b)
		if err == nil {
			t.Fatalf("EncodeJBIG2Generic on a %dx%d bitmap = %d bytes, err = nil; want an error",
				b.Width, b.Height, len(got))
		}
		if !strings.Contains(err.Error(), "EncodeJBIG2Generic") {
			t.Errorf("%dx%d: error = %v; want it to name EncodeJBIG2Generic. A refusal reaching "+
				"the caller under the codec's own \"jbig2:\" prefix says the STREAM was bad; "+
				"what was bad is the bitmap this caller handed in, and this function's own "+
				"guard is the only thing that draws that distinction", b.Width, b.Height, err)
		}
	}
}

// TestEncodeJBIG2GenericRejectsAStrideHeightOverflowInsteadOfPanicking pins the
// third guard in the same class, and it is the one whose deletion is invisible
// at first glance because a downstream check catches the OBVIOUS shape.
//
// The guard exists so that Pix's length can be compared against Stride*Height
// without the product having wrapped. Delete it and the comparison is made
// against a wrapped -- and here negative -- want, which any slice satisfies.
//
// Most overflowing shapes are then still refused one layer down, by
// jbig2.EmbeddedStream's 32-bit dimension check: Stride 1<<24 with Height 1<<40
// comes back as "JBIG2 region dimensions are 32-bit". That is what makes the
// deletion look harmless. It is not. Choose the overflow so that BOTH dimensions
// stay inside 32 bits and only the stride is large -- Height 2^32-1, Stride 2^32,
// whose product is 2^64-2^32 and wraps -- and nothing downstream is looking:
//
//	panic: runtime error: slice bounds out of range [:4294967296] with capacity 8
//
// out of Bitmap.MaskPadding, on row 0, before a single pixel is coded.
func TestEncodeJBIG2GenericRejectsAStrideHeightOverflowInsteadOfPanicking(t *testing.T) {
	// 2^32-1 rows of 2^32 bytes: both dimensions are legal 32-bit JBIG2 values,
	// so the region-dimension check downstream admits them, and the product is
	// 2^64-2^32, which does not fit in the int64 the length check compares.
	b := &Bitmap{Width: 16, Height: (1 << 32) - 1, Stride: 1 << 32, Pix: make([]byte, 8)}
	got, err := encodeJBIG2GenericNoPanic(t, b)
	if err == nil {
		t.Fatalf("EncodeJBIG2Generic on stride %d by height %d = %d bytes, err = nil; want an "+
			"error. Their product does not fit in an int64, so the Pix length check below "+
			"is comparing against a wrapped value and admits any slice at all.",
			b.Stride, b.Height, len(got))
	}
	if !strings.Contains(err.Error(), "overflow") {
		t.Errorf("error = %v; want it to name the overflow. A refusal that blames the 32-bit "+
			"region limit instead would be the downstream check firing, which means this "+
			"guard is gone and only the shapes it does not cover are still safe", err)
	}
}
