package jbig2

import (
	"bytes"
	"encoding/binary"
	"math"
	"testing"
)

// T.88 Annex H.1, twelfth segment header (file offset 0x0200): segment number
// 11, type 39 (immediate lossless generic region), short page association,
// no referred-to segments, page 2, data length 35.
func TestSegmentHeaderMatchesAnnexH1(t *testing.T) {
	got := segmentHeader(11, segTypeImmediateLosslessGenericRegion, 2, 35)
	want := []byte{0x00, 0x00, 0x00, 0x0B, 0x27, 0x00, 0x02, 0x00, 0x00, 0x00, 0x23}
	if !bytes.Equal(got, want) {
		t.Fatalf("segment header = % 02X; want % 02X", got, want)
	}
}

// T.88 Annex H.1, ninth segment header (file offset 0x0190): segment number 8,
// type 48 (page information), page 2, data length 19.
func TestPageInfoSegmentHeaderMatchesAnnexH1(t *testing.T) {
	got := segmentHeader(8, segTypePageInformation, 2, 19)
	want := []byte{0x00, 0x00, 0x00, 0x08, 0x30, 0x00, 0x02, 0x00, 0x00, 0x00, 0x13}
	if !bytes.Equal(got, want) {
		t.Fatalf("page info header = % 02X; want % 02X", got, want)
	}
}

// T.88 Annex H.1, ninth segment data part (file offset 0x019B): a 64x56 page
// with unknown resolutions, "eventually lossless" set, not striped.
func TestPageInfoSegmentDataMatchesAnnexH1(t *testing.T) {
	got := pageInfoSegmentData(64, 56)
	want := []byte{
		0x00, 0x00, 0x00, 0x40, // width 64
		0x00, 0x00, 0x00, 0x38, // height 56
		0x00, 0x00, 0x00, 0x00, // X resolution unknown
		0x00, 0x00, 0x00, 0x00, // Y resolution unknown
		0x01,       // flags: page is eventually lossless
		0x00, 0x00, // striping information: not striped
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("page info data = % 02X; want % 02X", got, want)
	}
	if len(got) != 19 {
		t.Fatalf("page info data = %d bytes; want 19", len(got))
	}
}

// T.88 Annex H.1, twelfth segment data part (file offset 0x020B): region info
// for a 54x44 region at (4, 11), then generic region flags 0x08 (arithmetic,
// GBTEMPLATE 0, TPGDON), then the eight nominal AT bytes, then the region data.
func TestGenericRegionSegmentDataMatchesAnnexH1(t *testing.T) {
	got := genericRegionSegmentData(figureH6(), 4, 11, true)
	want := []byte{
		0x00, 0x00, 0x00, 0x36, // region width 54
		0x00, 0x00, 0x00, 0x2C, // region height 44
		0x00, 0x00, 0x00, 0x04, // region X 4
		0x00, 0x00, 0x00, 0x0B, // region Y 11
		0x00,                                           // external combination operator OR
		0x08,                                           // MMR=0, GBTEMPLATE=0, TPGDON=1
		0x03, 0xFF, 0xFD, 0xFF, 0x02, 0xFE, 0xFE, 0xFE, // nominal AT pixels
		0x04, 0xEE, 0xED, 0x87, 0xFB, 0xCB, 0x2B, 0xFF, 0xAC,
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("generic region segment data mismatch\ngot  (%d): % 02X\nwant (%d): % 02X",
			len(got), got, len(want), want)
	}
	if len(got) != 35 {
		t.Fatalf("segment data = %d bytes; want 35 (matching the header's length field)", len(got))
	}
}

// The embedded stream is exactly two segments, both associated with page 1, and
// carries no JBIG2 file header (ISO 32000-1 7.4.7 forbids it in PDF).
func TestEmbeddedStreamStructure(t *testing.T) {
	b := figureH6()
	got, err := EmbeddedStream(b)
	if err != nil {
		t.Fatalf("EmbeddedStream() error = %v", err)
	}

	if bytes.HasPrefix(got, []byte{0x97, 0x4A, 0x42, 0x32, 0x0D, 0x0A, 0x1A, 0x0A}) {
		t.Fatal("stream begins with the JBIG2 file header, which ISO 32000-1 7.4.7 forbids in PDF")
	}

	// Segment 0: page information.
	if n := binary.BigEndian.Uint32(got[0:4]); n != 0 {
		t.Errorf("first segment number = %d; want 0", n)
	}
	if got[4] != segTypePageInformation {
		t.Errorf("first segment type = %d; want %d", got[4], segTypePageInformation)
	}
	if got[5] != 0x00 {
		t.Errorf("first segment referred-to byte = %#02x; want 0x00 (no references)", got[5])
	}
	if got[6] != 1 {
		t.Errorf("first segment page association = %d; want 1", got[6])
	}
	if n := binary.BigEndian.Uint32(got[7:11]); n != 19 {
		t.Errorf("page info data length = %d; want 19", n)
	}

	// Segment 1 starts immediately after the 11-byte header and 19-byte body.
	const s1 = 11 + 19
	if n := binary.BigEndian.Uint32(got[s1 : s1+4]); n != 1 {
		t.Errorf("second segment number = %d; want 1", n)
	}
	if got[s1+4] != segTypeImmediateLosslessGenericRegion {
		t.Errorf("second segment type = %d; want %d", got[s1+4], segTypeImmediateLosslessGenericRegion)
	}
	if got[s1+6] != 1 {
		t.Errorf("second segment page association = %d; want 1", got[s1+6])
	}
	bodyLen := int(binary.BigEndian.Uint32(got[s1+7 : s1+11]))
	if want := len(got) - s1 - 11; bodyLen != want {
		t.Errorf("generic region data length field = %d; want %d", bodyLen, want)
	}

	// No end-of-page (49) or end-of-file (51) segment: 7.4.7 forbids both.
	if len(got) != s1+11+bodyLen {
		t.Errorf("stream is %d bytes; two segments account for %d -- a trailing segment was emitted",
			len(got), s1+11+bodyLen)
	}
}

func TestEmbeddedStreamRejectsOversizeBitmap(t *testing.T) {
	// A bitmap whose dimensions do not fit the 4-byte region fields cannot be
	// represented; the writer must say so rather than truncate silently.
	//
	// The width goes through an int64 *variable* so this file still compiles
	// where int is 32 bits. Both `W: 1 << 33` and `int(someConstant)` would be
	// compile-time overflows there; a runtime conversion is not. On such a
	// platform the guard is unreachable, so skip.
	w := int64(1) << 33
	if w > int64(math.MaxInt) {
		t.Skip("int is 32 bits on this platform; the 32-bit region dimension guard is unreachable")
	}
	b := &Bitmap{W: int(w), H: 1, Stride: 1, Pix: make([]byte, 1)}
	if _, err := EmbeddedStream(b); err == nil {
		t.Fatal("EmbeddedStream() on a 2^33-wide bitmap: want error, got nil")
	}
}

// TestEmbeddedStreamRejectsAWidthOnePastTheField pins the same guard AT ITS
// BOUNDARY, which the 2^33 case above cannot reach.
//
// 2^33 is TWICE the width the guard is about, and that slack is the whole
// problem: loosen the comparison to "> math.MaxUint32*2" and 2^33 is still
// refused -- 8,589,934,592 is two past 8,589,934,590 -- so the mutant passes
// this file and the entire suite while a bitmap of any width between 2^32 and
// 2^33 goes out as a stream whose region header declares uint32(W), a number
// that is not the width of anything. A guard on a 32-bit FIELD has to be pinned
// at the first value that does not fit the field, which is math.MaxUint32 + 1
// and nothing else.
//
// What the mutant does with this bitmap is not a wrong stream but a panic, and
// it is worth saying where: EncodeGenericRegion masks the padding of every row
// first, and the byte holding pixel W-1 of a 2^32-wide row is index 536,870,911
// of a one-byte Pix. The recover is what keeps that from taking the package's
// other tests down with it; the panic is still a failure of this test.
//
// The HEIGHT half of the same comparison is deliberately left at presence only.
// The bitmap that would pin it has H = 2^32, and genericRegionSegmentData's
// first statement is make([]byte, 0, 26+b.H) -- so a mutant that accepts it
// reserves four gibibytes before it can fail, which makes the test a resource
// bomb rather than an assertion. There is no cheap boundary case for that half.
func TestEmbeddedStreamRejectsAWidthOnePastTheField(t *testing.T) {
	// Through an int64 variable for the same reason as the case above: on a
	// 32-bit platform int(1<<32) is a compile-time overflow and the guard is
	// unreachable anyway.
	w := int64(math.MaxUint32) + 1
	if w > int64(math.MaxInt) {
		t.Skip("int is 32 bits on this platform; the 32-bit region dimension guard is unreachable")
	}
	b := &Bitmap{W: int(w), H: 1, Stride: 1, Pix: make([]byte, 1)}

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("EmbeddedStream() PANICKED on a %d-wide bitmap instead of refusing it: %v",
				w, r)
		}
	}()
	if _, err := EmbeddedStream(b); err == nil {
		t.Fatalf("EmbeddedStream() on a bitmap %d wide -- math.MaxUint32 + 1, the first width "+
			"that does not fit the 32-bit region dimension field of T.88 7.4.1 -- returned no "+
			"error. The header would carry uint32(%d) = %d, which is not the width of this or "+
			"any other bitmap.", w, w, uint32(w))
	}
}

// TestEmbeddedStreamRejectsAZeroDimension pins EmbeddedStream's own dimension
// guard, which is the inner half of a defence-in-depth pair and is currently
// pinned by nothing.
//
// The outer half is byblos.EncodeJBIG2Generic's "b.Width <= 0 || b.Height <= 0",
// and while it stands, no caller can reach this one with a zero dimension. That
// is the argument for calling this check dead, and it is wrong for a reason the
// pair makes obvious: DELETE EITHER HALF ALONE and the other still refuses, so
// neither half can be tested through the public API and BOTH are free to be
// deleted one at a time by two people who each checked that the suite was green.
// Delete both and EncodeJBIG2Generic returns 71 bytes and a nil error for a 16x0
// bitmap -- a JBIG2 stream declaring a page with no rows, which every decoder
// including this one then refuses. A writer that emits an unreadable stream and
// calls it success is the silent accept this package's doc treats as worse than
// an error.
//
// EmbeddedStream is reachable directly from inside the package, so unlike the
// dead policy documented in generic_decode.go and parseSegments this check CAN
// be made to fail, which is the whole test for whether it should be pinned.
func TestEmbeddedStreamRejectsAZeroDimension(t *testing.T) {
	for name, b := range map[string]*Bitmap{
		"zero-height": {W: 16, H: 0, Stride: 2, Pix: []byte{}},
		"zero-width":  {W: 0, H: 4, Stride: 2, Pix: make([]byte, 8)},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := EmbeddedStream(b)
			if err == nil {
				t.Fatalf("EmbeddedStream() on a %dx%d bitmap wrote %d bytes with err = nil; "+
					"a page with a zero dimension has no raster to carry and no decoder "+
					"accepts one back", b.W, b.H, len(got))
			}
		})
	}
}
