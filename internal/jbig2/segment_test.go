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
