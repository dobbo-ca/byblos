package jbig2

import (
	"encoding/binary"
	"errors"
	"fmt"
)

// Segment types this decoder recognises beyond the two the encoder emits.
// T.88 7.3 gives these as a numbered list, not a table.
const (
	segTypeIntermediateGenericRegion = 36
	segTypeImmediateGenericRegion    = 38
	segTypeEndOfPage                 = 49
	segTypeEndOfStripe               = 50
	segTypeEndOfFile                 = 51
	segTypeProfiles                  = 52
	segTypeTables                    = 53
	segTypeExtension                 = 62
)

// ErrUnsupportedFeature reports a JBIG2 stream this package parsed correctly
// and refuses to decode: a symbol dictionary, a text region, a refinement or
// halftone region, MMR coding, or a generic region using anything other than
// GBTEMPLATE 0 with the nominal AT pixels.
//
// It is a distinct sentinel from a parse failure on purpose. Everything it
// covers is a legal stream some other producer wrote; the honest answer is "not
// implemented here", and the caller's correct response is to divert the page,
// never to present a partial raster. Nothing in this package guesses.
var ErrUnsupportedFeature = errors.New("jbig2: unsupported feature")

// maxRegionPixels bounds what a region header may claim before this decoder
// refuses to allocate for it. Region dimensions are 32-bit (T.88 7.4.1), so a
// corrupt or hostile header can ask for 2^64 pixels; the bitmap is allocated
// from the header, before a single coded bit is read, and arithmetic coding
// puts no lower bound on the bytes a large region needs, so there is no
// data-derived bound to use instead.
//
// 2^29 pixels is 67 MB packed. A 1200-dpi A4 page is 279 million pixels, so
// this admits every plausible scan and rejects the absurd.
//
// It is applied THREE times, and the third is the one that is easy to leave
// out. One header driving one allocation is bounded by the per-region check in
// parseRegionInfo, and the page those regions resolve to is bounded by the
// check in DecodeEmbeddedStream -- but a stream may carry any number of
// regions, each individually legal, every one of them decoded and RETAINED
// before the page is composed. So the same constant also bounds their SUM, read
// from the headers, before the first region is decoded. Without that third use
// the cost is linear in segment count at 37 bytes per region: 326 bytes of
// input measured 38.7 seconds and 512 MiB before erroring, and 40 KB would ask
// for 64 GiB. A decoder reached from an untrusted PDF (extract.go) cannot leave
// that open.
const maxRegionPixels = 1 << 29

// segment is one parsed T.88 7.2 segment header together with the slice of the
// input holding its data. The data is not copied.
type segment struct {
	number uint32
	typ    byte
	data   []byte
}

// parseSegments walks the sequence of segment headers in the embedded file
// organization (T.88 7.2, and ISO 32000-1:2008 7.4.7 for what PDF strips: no
// file header, and the segment headers are not separated from their data).
//
// The unknown-data-length form (0xFFFFFFFF, T.88 7.2.7) is rejected. It is only
// decodable by scanning the coded data for a terminating sequence, which means
// running the arithmetic decoder to find out where the segment ends; this
// package's own encoder never emits it.
func parseSegments(s []byte) ([]segment, error) {
	var out []segment
	for off := 0; off < len(s); {
		rest := s[off:]
		if len(rest) < 11 {
			return nil, fmt.Errorf("jbig2: segment header at offset %d is %d bytes; the minimum is 11", off, len(rest))
		}
		num := binary.BigEndian.Uint32(rest[0:4])
		flags := rest[4]
		typ := flags & 0x3F
		i := 5

		// Referred-to segment count and retain flags (T.88 7.2.4). Counts up to
		// 4 fit in the top 3 bits of a single byte alongside the retain bits; a
		// value of 7 there selects the long form, a 4-byte count whose low 29
		// bits are the count, followed by ceil((count+1)/8) retain bytes.
		count := int(rest[i] >> 5)
		if count == 7 {
			if i+4 > len(rest) {
				return nil, fmt.Errorf("jbig2: segment %d: truncated long-form referred-to count", num)
			}
			n := binary.BigEndian.Uint32(rest[i:i+4]) & 0x1FFFFFFF
			if uint64(n) > uint64(len(s)) {
				return nil, fmt.Errorf("jbig2: segment %d: refers to %d segments in a %d-byte stream", num, n, len(s))
			}
			count = int(n)
			i += 4 + (count+8)/8
		} else {
			i++
		}

		// Referred-to segment numbers (T.88 7.2.5): sized by THIS segment's
		// number, not by the numbers being referred to.
		refSize := 1
		switch {
		case num > 65536:
			refSize = 4
		case num > 256:
			refSize = 2
		}
		i += count * refSize

		// Page association (T.88 7.2.6): four bytes when bit 6 of the flags is
		// set, one otherwise.
		if flags&0x40 != 0 {
			i += 4
		} else {
			i++
		}

		if i < 0 || i+4 > len(rest) {
			return nil, fmt.Errorf("jbig2: segment %d: header runs past the end of the stream", num)
		}
		dataLen := binary.BigEndian.Uint32(rest[i : i+4])
		i += 4
		if dataLen == 0xFFFFFFFF {
			return nil, fmt.Errorf("%w: segment %d has an unknown data length", ErrUnsupportedFeature, num)
		}
		if uint64(dataLen) > uint64(len(rest)-i) {
			return nil, fmt.Errorf("jbig2: segment %d declares %d bytes of data but only %d remain",
				num, dataLen, len(rest)-i)
		}
		out = append(out, segment{number: num, typ: typ, data: rest[i : i+int(dataLen)]})
		off += i + int(dataLen)
	}
	if len(out) == 0 {
		return nil, errors.New("jbig2: stream contains no segments")
	}
	return out, nil
}

// regionInfo is the region segment information field of T.88 7.4.1: 17 bytes
// giving the region's size, its position on the page, and the operator by which
// it combines with what is already there.
type regionInfo struct {
	w, h int
	x, y int
	op   byte
}

func parseRegionInfo(d []byte) (regionInfo, error) {
	if len(d) < 17 {
		return regionInfo{}, fmt.Errorf("jbig2: region segment info is %d bytes; want 17", len(d))
	}
	w := binary.BigEndian.Uint32(d[0:4])
	h := binary.BigEndian.Uint32(d[4:8])
	if w == 0 || h == 0 {
		return regionInfo{}, fmt.Errorf("jbig2: region is %dx%d; dimensions must be positive", w, h)
	}
	if uint64(w)*uint64(h) > maxRegionPixels {
		return regionInfo{}, fmt.Errorf("jbig2: region is %dx%d, %d pixels; the limit is %d",
			w, h, uint64(w)*uint64(h), uint64(maxRegionPixels))
	}
	// Bits 0-2 of the flags byte are the external combination operator; bits
	// 3-7 are reserved. T.88 7.4.1.5 allows only 0-4 there, so 5-7 is a
	// malformed field rather than an operator this package has not implemented.
	op := d[16] & 0x07
	if op > 4 {
		return regionInfo{}, fmt.Errorf("jbig2: external combination operator %d; T.88 7.4.1.5 allows 0-4", op)
	}
	return regionInfo{
		w: int(w), h: int(h),
		x:  int(binary.BigEndian.Uint32(d[8:12])),
		y:  int(binary.BigEndian.Uint32(d[12:16])),
		op: op,
	}, nil
}

// decodeGenericRegionSegment decodes the body of an immediate generic region
// segment (T.88 7.4.6) and returns both the region's bitmap and where it goes.
//
// Everything this package cannot code for is rejected here, before any bit of
// the arithmetic stream is read, because the MQ decoder returns a decision for
// any input whatsoever: decoding an MMR region or a template-1 region as if it
// were template 0 does not fail, it produces noise. The rejections are the only
// thing standing between a stream from another producer and a wrong raster.
func decodeGenericRegionSegment(d []byte) (*Bitmap, regionInfo, error) {
	info, err := parseRegionInfo(d)
	if err != nil {
		return nil, regionInfo{}, err
	}
	if len(d) < 18 {
		return nil, regionInfo{}, fmt.Errorf("jbig2: generic region segment is %d bytes; want at least 18", len(d))
	}
	flags := d[17]
	mmr := flags&0x01 != 0
	template := (flags >> 1) & 0x03
	tpgdon := flags&0x08 != 0
	extTemplate := flags&0x10 != 0

	if mmr {
		return nil, regionInfo{}, fmt.Errorf("%w: MMR-coded generic region", ErrUnsupportedFeature)
	}
	if template != 0 {
		return nil, regionInfo{}, fmt.Errorf("%w: generic region GBTEMPLATE %d (only 0 is implemented)",
			ErrUnsupportedFeature, template)
	}
	if extTemplate {
		return nil, regionInfo{}, fmt.Errorf("%w: generic region EXTTEMPLATE", ErrUnsupportedFeature)
	}

	// AT pixels (T.88 7.4.6.3): four signed byte pairs for GBTEMPLATE 0.
	if len(d) < 26 {
		return nil, regionInfo{}, fmt.Errorf("jbig2: generic region segment is %d bytes; want at least 26 for the AT field", len(d))
	}
	at := d[18:26]
	for i := range at {
		if at[i] != nominalATTemplate0[i] {
			return nil, regionInfo{}, fmt.Errorf("%w: generic region AT pixels % 02X are not the nominal % 02X",
				ErrUnsupportedFeature, at, nominalATTemplate0)
		}
	}

	b, err := decodeGenericRegion(d[26:], info.w, info.h, tpgdon)
	if err != nil {
		return nil, regionInfo{}, err
	}
	return b, info, nil
}

// composite draws region onto page at (x0, y0) under one of the external
// combination operators of T.88 7.4.1 and Table 12. Pixels falling outside the
// page are dropped, which is what T.88 6.2.4 requires of a region that overruns
// its page. op has already been validated as 0-4 by parseRegionInfo.
func composite(page, region *Bitmap, x0, y0 int, op byte) {
	for y := 0; y < region.H; y++ {
		py := y0 + y
		if py < 0 || py >= page.H {
			continue
		}
		for x := 0; x < region.W; x++ {
			px := x0 + x
			if px < 0 || px >= page.W {
				continue
			}
			s, dst := region.Get(x, y), page.Get(px, py)
			var v int
			switch op {
			case 0: // OR
				v = dst | s
			case 1: // AND
				v = dst & s
			case 2: // XOR
				v = dst ^ s
			case 3: // XNOR
				v = 1 - (dst ^ s)
			default: // 4, REPLACE
				v = s
			}
			page.Set(px, py, v)
		}
	}
}

// DecodeEmbeddedStream decodes a JBIG2 bitstream in the embedded file
// organization -- the form ISO 32000-1:2008 7.4.7 requires of the PDF
// JBIG2Decode filter, and the form EmbeddedStream produces -- and returns the
// composed page bitmap. A set bit is ink, as everywhere else in this package.
//
// It is the inverse of EmbeddedStream and nothing more. It decodes immediate
// generic regions coded with GBTEMPLATE 0 and the nominal AT pixels, composed
// onto the page named by a page information segment. Everything else in JBIG2 --
// symbol dictionaries and text regions, refinement, halftones, MMR, the other
// three templates, non-nominal AT pixels, intermediate regions destined for an
// auxiliary buffer -- is reported as ErrUnsupportedFeature and decoded by
// nobody. That is deliberate: a stream this package half-understands would
// yield a raster that is wrong without being detectably wrong, which is a
// strictly worse outcome for a caller than an error it can route around.
//
// Page-0 (global) segments carried in a PDF /JBIG2Globals stream are not
// consulted. They only ever hold symbol dictionaries and pattern dictionaries,
// which nothing here can use.
func DecodeEmbeddedStream(s []byte) (*Bitmap, error) {
	segs, err := parseSegments(s)
	if err != nil {
		return nil, err
	}

	// Region segments are decoded in a second pass so that a page information
	// segment declaring the unknown height 0xFFFFFFFF (T.88 7.4.8.2, a striped
	// page) can be sized from the regions that will land on it. Their absolute
	// Y coordinates make that exact, not a guess.
	var pageW, pageH int
	var pageDefault int
	sawPageInfo := false
	pageKnownHeight := true
	regions := make([]segment, 0, len(segs))

	for _, sg := range segs {
		switch sg.typ {
		case segTypePageInformation:
			if sawPageInfo {
				return nil, fmt.Errorf("%w: more than one page information segment", ErrUnsupportedFeature)
			}
			sawPageInfo = true
			if len(sg.data) < 19 {
				return nil, fmt.Errorf("jbig2: page information segment is %d bytes; want 19", len(sg.data))
			}
			w := binary.BigEndian.Uint32(sg.data[0:4])
			h := binary.BigEndian.Uint32(sg.data[4:8])
			// Bit 2 of the flags byte is the page's default pixel value.
			pageDefault = int(sg.data[16]>>2) & 1
			if h == 0xFFFFFFFF {
				pageKnownHeight = false
				h = 0
			}
			if w == 0 {
				return nil, errors.New("jbig2: page information segment declares a width of 0")
			}
			if uint64(w) > maxRegionPixels {
				return nil, fmt.Errorf("jbig2: page is %d pixels wide; the limit is %d", w, uint64(maxRegionPixels))
			}
			pageW, pageH = int(w), int(h)

		case segTypeImmediateGenericRegion, segTypeImmediateLosslessGenericRegion:
			regions = append(regions, sg)

		case segTypeEndOfPage, segTypeEndOfStripe, segTypeEndOfFile,
			segTypeProfiles, segTypeTables, segTypeExtension:
			// Carry no page content this decoder needs. End-of-stripe would
			// matter to a striped page, but region segments carry absolute Y
			// coordinates, so the page extent is already recoverable without it.

		case segTypeIntermediateGenericRegion:
			// Composed into an auxiliary buffer by a later refinement or text
			// region, never onto the page. Nothing here consumes one, so
			// treating it as immediate would put a region on the page that the
			// producer did not intend to be there.
			return nil, fmt.Errorf("%w: intermediate generic region (segment %d)", ErrUnsupportedFeature, sg.number)

		default:
			return nil, fmt.Errorf("%w: segment type %d (segment %d)", ErrUnsupportedFeature, sg.typ, sg.number)
		}
	}

	if !sawPageInfo {
		return nil, errors.New("jbig2: stream has no page information segment")
	}
	if len(regions) == 0 {
		return nil, errors.New("jbig2: stream has no immediate generic region segment")
	}

	// A running pixel budget over every region in the stream, spent from the
	// HEADERS before any region is decoded. Each region and the resolved page
	// are already capped one at a time; nothing caps how many regions a stream
	// may carry, and every one of them is decoded and held until the page below
	// is composed, so the per-region cap times the segment count is what the
	// caller actually pays. See maxRegionPixels.
	//
	// Region info that does not parse is passed over rather than reported here.
	// The decode loop below reaches it in stream order and says what is actually
	// wrong with it, which keeps a malformed stream's error the one it was
	// before this budget existed.
	budget := int64(maxRegionPixels)
	for _, sg := range regions {
		info, err := parseRegionInfo(sg.data)
		if err != nil {
			continue
		}
		budget -= int64(info.w) * int64(info.h)
		if budget < 0 {
			return nil, fmt.Errorf("jbig2: segment %d: the stream's %d regions exceed the "+
				"%d-pixel budget for one page", sg.number, len(regions), uint64(maxRegionPixels))
		}
	}

	// Decode first, then allocate: an unknown page height is only known once
	// every region header has been read.
	type placed struct {
		b    *Bitmap
		info regionInfo
	}
	decoded := make([]placed, 0, len(regions))
	for _, sg := range regions {
		b, info, err := decodeGenericRegionSegment(sg.data)
		if err != nil {
			return nil, fmt.Errorf("jbig2: segment %d: %w", sg.number, err)
		}
		decoded = append(decoded, placed{b, info})
		if !pageKnownHeight {
			if bottom := info.y + info.h; bottom > pageH {
				pageH = bottom
			}
		}
	}
	if pageH <= 0 {
		return nil, fmt.Errorf("jbig2: page height resolves to %d", pageH)
	}
	if uint64(pageW)*uint64(pageH) > maxRegionPixels {
		return nil, fmt.Errorf("jbig2: page is %dx%d; the limit is %d pixels", pageW, pageH, uint64(maxRegionPixels))
	}

	out := NewBitmap(pageW, pageH)
	if pageDefault == 1 {
		for i := range out.Pix {
			out.Pix[i] = 0xFF
		}
		out.MaskPadding()
	}
	for _, p := range decoded {
		composite(out, p.b, p.info.x, p.info.y, p.info.op)
	}
	return out, nil
}
