package jbig2

import (
	"encoding/binary"
	"fmt"
	"math"
)

// Segment types used by this package. T.88 7.3 gives the allowed types as a
// plain numbered list, not a table -- everything else in it belongs to the
// symbol, text, halftone and refinement machinery this package deliberately
// does not implement.
const (
	segTypeImmediateLosslessGenericRegion = 39
	segTypePageInformation                = 48
)

// nominalATTemplate0 is the AT pixel field for GBTEMPLATE 0 (T.88 7.4.6.3),
// eight signed bytes carrying the nominal positions of T.88 Table 5:
// A1 (3,-1), A2 (-3,-1), A3 (2,-2), A4 (-2,-2).
var nominalATTemplate0 = []byte{0x03, 0xFF, 0xFD, 0xFF, 0x02, 0xFE, 0xFE, 0xFE}

// segmentHeader builds a T.88 7.2 segment header with a one-byte page
// association and no referred-to segments: 11 bytes total.
//
// The flags byte is the segment type in bits 0-5, bit 6 clear for a one-byte
// page association, bit 7 clear for "not deferred, non-retain". The
// referred-to-segment-count byte is 0x00: zero references, no retain bits.
//
// The 0xFFFFFFFF unknown-data-length form is deliberately not used -- the
// length is always known here, and the unknown form is only decodable by
// scanning for a terminator.
func segmentHeader(segNum uint32, segType byte, pageAssoc byte, dataLen int) []byte {
	h := make([]byte, 0, 11)
	h = binary.BigEndian.AppendUint32(h, segNum)
	h = append(h, segType, 0x00, pageAssoc)
	return binary.BigEndian.AppendUint32(h, uint32(dataLen))
}

// pageInfoSegmentData builds the 19-byte page information segment body
// (T.88 7.4.8).
//
// Resolutions are written as 0 ("unknown"): the JBIG2 page carries no DPI of
// its own in PDF, where the image XObject's placement matrix determines scale.
// The flags byte is 0x01 -- bit 0 "page is eventually lossless" set, because it
// is; every other bit clear, which means no refinements, default pixel value 0,
// default combination operator OR, no auxiliary buffers, and the combination
// operator not overridable. Striping information is 0x0000: not striped.
func pageInfoSegmentData(width, height int) []byte {
	d := make([]byte, 0, 19)
	d = binary.BigEndian.AppendUint32(d, uint32(width))
	d = binary.BigEndian.AppendUint32(d, uint32(height))
	d = binary.BigEndian.AppendUint32(d, 0)
	d = binary.BigEndian.AppendUint32(d, 0)
	d = append(d, 0x01)
	return append(d, 0x00, 0x00)
}

// genericRegionSegmentData builds the body of a generic region segment: the
// 17-byte region segment information field (T.88 7.4.1), the one-byte generic
// region flags (7.4.6.2), the eight-byte AT field (7.4.6.3) and the MQ-coded
// region data.
//
// The region flags byte is MMR in bit 0, GBTEMPLATE in bits 1-2, TPGDON in
// bit 3, and reserved zeros above: template 0 with TPGDON is 0x08.
func genericRegionSegmentData(b *Bitmap, x, y int, tpgdon bool) []byte {
	d := make([]byte, 0, 26+b.H)
	d = binary.BigEndian.AppendUint32(d, uint32(b.W))
	d = binary.BigEndian.AppendUint32(d, uint32(b.H))
	d = binary.BigEndian.AppendUint32(d, uint32(x))
	d = binary.BigEndian.AppendUint32(d, uint32(y))
	d = append(d, 0x00) // region segment flags: external combination operator OR

	flags := byte(0x00) // MMR = 0, GBTEMPLATE = 0
	if tpgdon {
		flags |= 0x08
	}
	d = append(d, flags)
	d = append(d, nominalATTemplate0...)
	return append(d, EncodeGenericRegion(b, tpgdon)...)
}

// EmbeddedStream encodes b as a complete JBIG2 bitstream in the embedded file
// organization required by ISO 32000-1:2008 7.4.7 for the PDF JBIG2Decode
// filter: no file header, no end-of-page segment, no end-of-file segment, and
// every segment associated with page 1.
//
// The result is exactly two segments -- a page information segment and an
// immediate lossless generic region segment covering the whole page. It carries
// no page-0 (global) segments, so the image XObject needs no /DecodeParms and
// no /JBIG2Globals stream.
func EmbeddedStream(b *Bitmap) ([]byte, error) {
	if b.W <= 0 || b.H <= 0 {
		return nil, fmt.Errorf("jbig2: bitmap is %dx%d; dimensions must be positive", b.W, b.H)
	}
	// uint64 conversion so the comparison also compiles on a 32-bit int platform.
	if uint64(b.W) > math.MaxUint32 || uint64(b.H) > math.MaxUint32 {
		return nil, fmt.Errorf("jbig2: bitmap is %dx%d; JBIG2 region dimensions are 32-bit", b.W, b.H)
	}

	pi := pageInfoSegmentData(b.W, b.H)
	gr := genericRegionSegmentData(b, 0, 0, true)

	out := make([]byte, 0, 22+len(pi)+len(gr))
	out = append(out, segmentHeader(0, segTypePageInformation, 1, len(pi))...)
	out = append(out, pi...)
	out = append(out, segmentHeader(1, segTypeImmediateLosslessGenericRegion, 1, len(gr))...)
	return append(out, gr...), nil
}
