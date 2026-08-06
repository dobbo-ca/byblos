package jbig2

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"
)

// TestMQDecodeConformanceVector runs the T.88 Annex H.2 test sequence
// BACKWARDS: the 30 coded bytes the encoder must produce are fed to the decoder,
// which must give back the 256 original decisions.
//
// This is the only check on the MQ decoder that does not go through this
// package's own encoder, and it is the reason the round trip can be trusted at
// all. An encoder and a decoder that shared a wrong reading of Table E.1 or of
// the conditional exchange would agree with each other perfectly and disagree
// with every other implementation; the round trip cannot see that, and this can.
//
// On failure, do NOT adjust either array. Both are the spec's, and the encoder
// direction is pinned to the same pair in TestMQConformanceVector.
func TestMQDecodeConformanceVector(t *testing.T) {
	coded := []byte{
		0x84, 0xC7, 0x3B, 0xFC, 0xE1, 0xA1, 0x43, 0x04,
		0x02, 0x20, 0x00, 0x00, 0x41, 0x0D, 0xBB, 0x86,
		0xF4, 0x31, 0x7F, 0xFF, 0x88, 0xFF, 0x37, 0x47,
		0x1A, 0xDB, 0x6A, 0xDF, 0xFF, 0xAC,
	}
	want := []byte{
		0x00, 0x02, 0x00, 0x51, 0x00, 0x00, 0x00, 0xC0,
		0x03, 0x52, 0x87, 0x2A, 0xAA, 0xAA, 0xAA, 0xAA,
		0x82, 0xC0, 0x20, 0x00, 0xFC, 0xD7, 0x9E, 0xF6,
		0xBF, 0x7F, 0xED, 0x90, 0x4F, 0x46, 0xA3, 0xBF,
	}

	cx := make(contexts, 1)
	d := newDecoder(coded)
	got := make([]byte, len(want))
	for i := range got {
		var v byte
		for bit := 7; bit >= 0; bit-- {
			v |= byte(d.decode(cx, 0)) << uint(bit)
		}
		got[i] = v
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("MQ decode mismatch\ngot  % 02X\nwant % 02X", got, want)
	}
}

// TestDecodeGenericRegionAnnexH1 decodes the T.88 Annex H.1 segment 11
// conformance vector (doc p. 135) -- nine bytes written down from the standard,
// not produced here -- and requires the 54x44 Figure H.6 back.
//
// Both sides of this are independent of this package's encoder. The bytes are
// the spec's; figureH6 is itself cross-checked against the spec's own MMR
// coding of the same figure by TestFigureH6MatchesSpecMMR. So this pins the
// generic region decoding procedure -- template bit order, TPGD, the SLTP
// context -- against T.88 rather than against EncodeGenericRegion.
func TestDecodeGenericRegionAnnexH1(t *testing.T) {
	data := []byte{0x04, 0xEE, 0xED, 0x87, 0xFB, 0xCB, 0x2B, 0xFF, 0xAC}
	got, err := decodeGenericRegion(data, 54, 44, true)
	if err != nil {
		t.Fatalf("decodeGenericRegion() error = %v", err)
	}
	assertBitmapsIdentical(t, "annex-h1", got, figureH6())
}

// TestDecodeRoundTripBitIdentical is the acceptance criterion for byb-riy, and
// the only lossless check in this package that needs no external binary: encode
// every fixture, decode it back with this package's own decoder, and require
// the pixels to be identical.
//
// It runs in the oracle-free suite by construction -- there is nothing to look
// up on PATH -- which is what makes it the test that has to have kill power.
// The fixture corpus is the structural one from fixtures_test.go: all-ink,
// all-background, a single pixel, a one-pixel column and a one-pixel row, and
// three non-byte-aligned widths (1, 13, 101), because a decoder that only ever
// sees a friendly bitmap proves close to nothing.
func TestDecodeRoundTripBitIdentical(t *testing.T) {
	for name, want := range fixtureBitmaps() {
		t.Run(name, func(t *testing.T) {
			want.MaskPadding()
			stream, err := EmbeddedStream(want)
			if err != nil {
				t.Fatalf("EmbeddedStream() error = %v", err)
			}
			got, err := DecodeEmbeddedStream(stream)
			if err != nil {
				t.Fatalf("DecodeEmbeddedStream() error = %v", err)
			}
			assertBitmapsIdentical(t, name, got, want)
			// assertBitmapsIdentical walks pixels, so it does not see stray
			// bits in a row's padding. Those cost Bitmap.Equal, which the
			// public round trip in the parent package compares directly.
			if !bytes.Equal(got.Pix, want.Pix) {
				t.Errorf("%s: pixels match but the packed bytes do not; padding is dirty", name)
			}
		})
	}
}

// TestDecodeGoldens decodes the committed encoder goldens, which are streams a
// real jbig2dec confirmed lossless at the time they were written (see
// TestEncoderGoldens). It is the one place the decoder is checked against bytes
// this session's encoder did not produce, so an encoder and decoder that agreed
// with each other but not with the standard would still fail here.
func TestDecodeGoldens(t *testing.T) {
	for name, want := range fixtureBitmaps() {
		want.MaskPadding()
		stream, err := os.ReadFile(goldenPath(name))
		if err != nil {
			t.Errorf("%s: golden missing: %v", name, err)
			continue
		}
		got, err := DecodeEmbeddedStream(stream)
		if err != nil {
			t.Errorf("%s: DecodeEmbeddedStream() error = %v", name, err)
			continue
		}
		assertBitmapsIdentical(t, name, got, want)
	}
}

// TestDecodeTPGDOff covers the branch EmbeddedStream never exercises: it always
// sets TPGDON, so without this the untypical-prediction path in the decoder is
// dead code that no round trip would reach.
func TestDecodeTPGDOff(t *testing.T) {
	for name, want := range fixtureBitmaps() {
		t.Run(name, func(t *testing.T) {
			want.MaskPadding()
			data := EncodeGenericRegion(want, false)
			got, err := decodeGenericRegion(data, want.W, want.H, false)
			if err != nil {
				t.Fatalf("decodeGenericRegion() error = %v", err)
			}
			assertBitmapsIdentical(t, name, got, want)
		})
	}
}

// stripedStream rewrites a page information segment's height to the unknown
// value 0xFFFFFFFF. The page information segment is the first one EmbeddedStream
// emits, and its data begins at offset 11 (the fixed-size header this package
// writes), so the height is the second big-endian uint32 of the body.
func stripedStream(t *testing.T, s []byte) []byte {
	t.Helper()
	out := bytes.Clone(s)
	const heightOffset = 11 + 4
	if len(out) < heightOffset+4 {
		t.Fatalf("stream is %d bytes; too short to hold a page information segment", len(out))
	}
	binary.BigEndian.PutUint32(out[heightOffset:heightOffset+4], 0xFFFFFFFF)
	return out
}

// TestDecodeUnknownPageHeight covers the striped-page form of T.88 7.4.8.2. The
// height comes back from the region's own absolute Y plus its height, which is
// exact rather than a guess -- so the decoded page must still equal the original.
func TestDecodeUnknownPageHeight(t *testing.T) {
	want := figureH6()
	want.MaskPadding()
	stream, err := EmbeddedStream(want)
	if err != nil {
		t.Fatalf("EmbeddedStream() error = %v", err)
	}
	got, err := DecodeEmbeddedStream(stripedStream(t, stream))
	if err != nil {
		t.Fatalf("DecodeEmbeddedStream() error = %v", err)
	}
	assertBitmapsIdentical(t, "unknown-height", got, want)
}

// TestDecodeClipsRegionToPage covers the two things composite does beyond
// copying: placing a region at a non-zero offset, and dropping the part of a
// region that falls outside the page (T.88 6.2.4).
//
// Nothing EmbeddedStream writes exercises either -- it always emits one region
// at (0,0) exactly the size of the page -- so without this the offset is
// untested and the clip is a bounds check with no bounds to check. Bitmap.Set
// does not range-check, so an unclipped region does not merely draw in the
// wrong place: it writes past Pix.
func TestDecodeClipsRegionToPage(t *testing.T) {
	full := figureH6()
	full.MaskPadding()
	base, err := EmbeddedStream(full)
	if err != nil {
		t.Fatalf("EmbeddedStream() error = %v", err)
	}
	setU32 := func(s []byte, off int, v uint32) []byte {
		out := bytes.Clone(s)
		binary.BigEndian.PutUint32(out[off:off+4], v)
		return out
	}
	const pageWidthAt, pageHeightAt = 11, 11 + 4
	const regionXAt, regionYAt = regionDataOffset + 8, regionDataOffset + 12

	t.Run("clipped", func(t *testing.T) {
		// A 20x20 page under a 54x44 region: everything past the page edge in
		// both axes has to be dropped.
		s := setU32(setU32(base, pageWidthAt, 20), pageHeightAt, 20)
		got, err := DecodeEmbeddedStream(s)
		if err != nil {
			t.Fatalf("DecodeEmbeddedStream() error = %v", err)
		}
		if got.W != 20 || got.H != 20 {
			t.Fatalf("decoded %dx%d; want 20x20", got.W, got.H)
		}
		want := NewBitmap(20, 20)
		for y := 0; y < 20; y++ {
			for x := 0; x < 20; x++ {
				want.Set(x, y, full.Get(x, y))
			}
		}
		assertBitmapsIdentical(t, "clipped", got, want)
		if !bytes.Equal(got.Pix, want.Pix) {
			t.Errorf("clipped: packed bytes differ: got % 02X want % 02X", got.Pix, want.Pix)
		}
	})

	t.Run("offset", func(t *testing.T) {
		// A 54x44 region at (5,7) on a 64x64 page: placed whole, nothing else
		// touched.
		s := setU32(setU32(base, pageWidthAt, 64), pageHeightAt, 64)
		s = setU32(setU32(s, regionXAt, 5), regionYAt, 7)
		got, err := DecodeEmbeddedStream(s)
		if err != nil {
			t.Fatalf("DecodeEmbeddedStream() error = %v", err)
		}
		want := NewBitmap(64, 64)
		for y := 0; y < full.H; y++ {
			for x := 0; x < full.W; x++ {
				want.Set(x+5, y+7, full.Get(x, y))
			}
		}
		assertBitmapsIdentical(t, "offset", got, want)
	})

	// The five external combination operators of T.88 7.4.1 and Table 12.
	// EmbeddedStream only ever writes op 0, so ops 1-4 are reached by no round
	// trip and by no golden; before this table, AND and XOR could be swapped,
	// and XNOR and REPLACE could each be replaced by OR, and the whole suite
	// stayed green. An inbound single-region page composed with XNOR would then
	// come back as its own photographic negative with no error -- exactly the
	// wrong-without-being-detectably-wrong outcome this package's doc calls the
	// thing worth refusing.
	//
	// Each operator is checked against BOTH page default pixel values, because
	// one is not enough to tell them apart. Over a default-background page AND
	// is constant 0 while OR, XOR and REPLACE all hand back the region; over a
	// default-ink page OR is constant 1 while AND, XNOR and REPLACE all hand
	// back the region. Across the pair every operator has a distinct signature,
	// so this table pins all five and not merely four.
	//
	// The expectations are written as functions of the source pixel, evaluated
	// by hand from Table 12, rather than derived from composite's own switch --
	// which would agree with any mutation of it.
	t.Run("operators", func(t *testing.T) {
		ops := []struct {
			name         string
			op           byte
			onBackground func(s int) int // page default pixel 0
			onInk        func(s int) int // page default pixel 1
		}{
			{"or", 0, func(s int) int { return s }, func(int) int { return 1 }},
			{"and", 1, func(int) int { return 0 }, func(s int) int { return s }},
			{"xor", 2, func(s int) int { return s }, func(s int) int { return 1 - s }},
			{"xnor", 3, func(s int) int { return 1 - s }, func(s int) int { return s }},
			{"replace", 4, func(s int) int { return s }, func(s int) int { return s }},
		}
		defaults := []struct {
			name  string
			flags byte // page information flags: bit 0 lossless, bit 2 default pixel
		}{
			{"page-background", 0x01},
			{"page-ink", 0x01 | 0x04},
		}
		for _, o := range ops {
			for i, d := range defaults {
				want := o.onBackground
				if i == 1 {
					want = o.onInk
				}
				t.Run(o.name+"/"+d.name, func(t *testing.T) {
					s := setByte(setByte(base, regionInfoFlagsAt, o.op), pageFlagsAt, d.flags)
					got, err := DecodeEmbeddedStream(s)
					if err != nil {
						t.Fatalf("DecodeEmbeddedStream() error = %v", err)
					}
					exp := NewBitmap(full.W, full.H)
					for y := 0; y < full.H; y++ {
						for x := 0; x < full.W; x++ {
							exp.Set(x, y, want(full.Get(x, y)))
						}
					}
					assertBitmapsIdentical(t, o.name+"/"+d.name, got, exp)
				})
			}
		}
	})

	// Bits 3-7 of that same byte are RESERVED (T.88 7.4.1.5), and the operator
	// is the low three bits only. Masking them off rather than refusing on them
	// is a decision the code states and nothing pinned: widen the mask by one
	// bit -- d[16]&0x0F instead of &0x07 -- and a stream whose reserved bit 3 is
	// set reads as operator 8, fails the 0-4 range check, and is refused as
	// malformed. That is a legal stream this package would then decline, and a
	// false refusal is as much a defect here as a missed attack.
	//
	// All five reserved bits are set at once, so any widening of the mask fails
	// this, not just a widening by one bit.
	t.Run("reserved-operator-bits-are-ignored", func(t *testing.T) {
		s := setByte(base, regionInfoFlagsAt, 0xF8) // operator 0 (OR), bits 3-7 set
		got, err := DecodeEmbeddedStream(s)
		if err != nil {
			t.Fatalf("a region whose reserved combination-operator bits are set was refused: %v. "+
				"T.88 7.4.1.5 puts the operator in bits 0-2 and reserves 3-7; a decoder that "+
				"reads a reserved bit as part of the operator refuses streams it should "+
				"compose", err)
		}
		assertBitmapsIdentical(t, "reserved-bits-set", got, full)
	})
}

// setByte returns a copy of s with the byte at off replaced. Used to reach the
// rejection paths without hand-assembling a whole stream.
func setByte(s []byte, off int, v byte) []byte {
	out := bytes.Clone(s)
	out[off] = v
	return out
}

// The generic region segment is the second EmbeddedStream writes. Its header is
// 11 bytes and the page information segment before it is 11 + 19; so the region
// segment's data starts at 11+19+11 = 41, its region-info flags byte is at
// 41+16, its generic-region flags byte at 41+17, and its AT field at 41+18.
const (
	regionDataOffset  = 11 + 19 + 11
	regionInfoFlagsAt = regionDataOffset + 16
	regionFlagsAt     = regionDataOffset + 17
	regionATAt        = regionDataOffset + 18
	pageFlagsAt       = 11 + 16
)

// TestDecodeRejectsRatherThanGuesses is the safety half of this decoder. Every
// case here is a legal JBIG2 stream some other producer could write and this
// package cannot decode; each must come back as ErrUnsupportedFeature, because
// the MQ decoder returns a decision for ANY input, so a missing rejection does
// not fail -- it silently yields a wrong raster.
func TestDecodeRejectsRatherThanGuesses(t *testing.T) {
	base, err := EmbeddedStream(figureH6())
	if err != nil {
		t.Fatalf("EmbeddedStream() error = %v", err)
	}
	if got := base[regionFlagsAt]; got != 0x08 {
		t.Fatalf("offset assumption broken: generic region flags = %#02x, want 0x08 (template 0, TPGDON)", got)
	}

	cases := map[string][]byte{
		// Generic region flags: bit 0 MMR, bits 1-2 GBTEMPLATE, bit 4 EXTTEMPLATE.
		"mmr":          setByte(base, regionFlagsAt, 0x09),
		"template1":    setByte(base, regionFlagsAt, 0x0A),
		"template2":    setByte(base, regionFlagsAt, 0x0C),
		"template3":    setByte(base, regionFlagsAt, 0x0E),
		"ext-template": setByte(base, regionFlagsAt, 0x18),
		// A1 moved from (3,-1) to (8,-1): a legal non-nominal AT pixel.
		"non-nominal-at": setByte(base, regionATAt, 0x08),
		// Segment type 36 is an intermediate generic region, destined for an
		// auxiliary buffer rather than the page.
		"intermediate-generic": setByte(base, 11+19+4, 36),
		"symbol-dictionary":    setByte(base, 11+19+4, 0),
		"text-region":          setByte(base, 11+19+4, 6),
		"refinement-region":    setByte(base, 11+19+4, 42),
		"halftone-region":      setByte(base, 11+19+4, 22),
	}
	for name, stream := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := DecodeEmbeddedStream(stream)
			if err == nil {
				t.Fatalf("DecodeEmbeddedStream() returned a %dx%d bitmap; want ErrUnsupportedFeature",
					got.W, got.H)
			}
			if !errors.Is(err, ErrUnsupportedFeature) {
				t.Fatalf("DecodeEmbeddedStream() error = %v; want ErrUnsupportedFeature", err)
			}
		})
	}
}

// TestDecodeRejectsMalformed checks that a damaged stream is an error and never
// a panic. The MQ decoder tolerates truncation by design -- it reads 1 bits past
// the end -- so these are the cases the SEGMENT layer has to catch.
func TestDecodeRejectsMalformed(t *testing.T) {
	base, err := EmbeddedStream(figureH6())
	if err != nil {
		t.Fatalf("EmbeddedStream() error = %v", err)
	}

	cases := map[string][]byte{
		"empty":                    nil,
		"header-only":              base[:8],
		"truncated-page-info":      base[:11+10],
		"truncated-region-header":  base[:regionDataOffset+9],
		"declared-length-past-end": base[:len(base)-1],
		// Region info flags bits 0-2 hold the external combination operator;
		// 7 is outside the 0-4 T.88 7.4.1.5 allows.
		"bad-combination-operator": setByte(base, regionInfoFlagsAt, 0x07),
		// The BOUNDARY of that range, and the only value that pins where it sits:
		// 7 is refused by a check written as "> 4", "> 5" and "> 6" alike. 5 is
		// the first reserved value, and admitting it does not fail loudly -- it
		// falls through composite's switch to the REPLACE default, so a reserved
		// operator silently becomes the one operator that discards what is
		// already on the page.
		"combination-operator-5": setByte(base, regionInfoFlagsAt, 0x05),
		// Unknown data length (0xFFFFFFFF) on the page information segment.
		"unknown-data-length": append(append([]byte{}, base[:7]...),
			append([]byte{0xFF, 0xFF, 0xFF, 0xFF}, base[11:]...)...),
	}
	for name, stream := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := DecodeEmbeddedStream(stream)
			if err == nil {
				t.Fatalf("DecodeEmbeddedStream() returned a %dx%d bitmap; want an error", got.W, got.H)
			}
		})
	}
}

// TestDecodeTruncatedCodedDataDoesNotPanic exercises the 1-bits-past-the-end
// rule of the MQ decoder directly. The segment layer cannot catch this: the
// declared data length is honoured, the bytes are simply wrong. A decoder that
// indexed its input without a guard would panic here on every one of these.
func TestDecodeTruncatedCodedDataDoesNotPanic(t *testing.T) {
	b := figureH6()
	b.MaskPadding()
	data := EncodeGenericRegion(b, true)
	for cut := len(data); cut >= 0; cut-- {
		got, err := decodeGenericRegion(data[:cut], b.W, b.H, true)
		if err != nil {
			t.Fatalf("cut to %d bytes: decodeGenericRegion() error = %v", cut, err)
		}
		if got.W != b.W || got.H != b.H {
			t.Fatalf("cut to %d bytes: got %dx%d; want %dx%d", cut, got.W, got.H, b.W, b.H)
		}
	}
	// The full stream is the only cut that has to be right.
	got, err := decodeGenericRegion(data, b.W, b.H, true)
	if err != nil {
		t.Fatalf("decodeGenericRegion() error = %v", err)
	}
	assertBitmapsIdentical(t, "untruncated", got, b)
}

// TestDecodeRejectsAbsurdRegion guards the allocation the region header drives.
// Region dimensions are 32-bit, so without the cap this is a 2-gigabyte
// allocation from a 26-byte header.
func TestDecodeRejectsAbsurdRegion(t *testing.T) {
	base, err := EmbeddedStream(figureH6())
	if err != nil {
		t.Fatalf("EmbeddedStream() error = %v", err)
	}
	huge := bytes.Clone(base)
	binary.BigEndian.PutUint32(huge[regionDataOffset:regionDataOffset+4], 0xFFFF)
	binary.BigEndian.PutUint32(huge[regionDataOffset+4:regionDataOffset+8], 0xFFFF)
	if got, err := DecodeEmbeddedStream(huge); err == nil {
		t.Fatalf("DecodeEmbeddedStream() returned a %dx%d bitmap for a 65535x65535 region; want an error",
			got.W, got.H)
	}
}

// TestDecodeRejectsAbsurdRegionOffset covers the one route by which a region's
// POSITION, not its size, drives an allocation: on a page of unknown height the
// page is sized from the regions, so a region at y = 0xFFFFFFFF asks for a
// four-billion-row page from a 26-byte header. The size cap has to be applied
// to the resolved page, not only to each region.
func TestDecodeRejectsAbsurdRegionOffset(t *testing.T) {
	base, err := EmbeddedStream(figureH6())
	if err != nil {
		t.Fatalf("EmbeddedStream() error = %v", err)
	}
	s := stripedStream(t, base)
	binary.BigEndian.PutUint32(s[regionDataOffset+12:regionDataOffset+16], 0xFFFFFFFF)
	if got, err := DecodeEmbeddedStream(s); err == nil {
		t.Fatalf("DecodeEmbeddedStream() returned a %dx%d bitmap; want an error", got.W, got.H)
	}
}

// TestDecodeDefaultPixelValue covers page information flags bit 2. Byblos never
// sets it, so nothing else in this package reaches the branch; a page whose
// default pixel is ink and whose region ORs onto it must come back all-ink.
func TestDecodeDefaultPixelValue(t *testing.T) {
	b := NewBitmap(13, 11) // non-byte-aligned, so the padding rule is live
	stream, err := EmbeddedStream(b)
	if err != nil {
		t.Fatalf("EmbeddedStream() error = %v", err)
	}
	got, err := DecodeEmbeddedStream(setByte(stream, pageFlagsAt, 0x01|0x04))
	if err != nil {
		t.Fatalf("DecodeEmbeddedStream() error = %v", err)
	}
	for y := 0; y < got.H; y++ {
		for x := 0; x < got.W; x++ {
			if got.Get(x, y) != 1 {
				t.Fatalf("pixel (%d,%d) = 0; want 1 from the page default", x, y)
			}
		}
	}
	want := NewBitmap(13, 11)
	for i := range want.Pix {
		want.Pix[i] = 0xFF
	}
	want.MaskPadding()
	if !bytes.Equal(got.Pix, want.Pix) {
		t.Errorf("padding bits are set in the default-ink page: got % 02X, want % 02X", got.Pix, want.Pix)
	}
}

// emptyRegionSegments builds a stream of n immediate generic region segments,
// each declaring w x h pixels at (0,0) and carrying NO coded data at all: 37
// bytes apiece, an 11-byte segment header plus the 26-byte region header the
// decoder must read before it can allocate. The MQ decoder reads 1 bits past
// the end of its input, so a region with no data still decodes -- expensively,
// and at exactly the size its header claims.
func emptyRegionSegments(pageW, pageH int, w, h uint32, n int) []byte {
	pi := pageInfoSegmentData(pageW, pageH)
	out := append(segmentHeader(0, segTypePageInformation, 1, len(pi)), pi...)
	for i := 0; i < n; i++ {
		d := make([]byte, 0, 26)
		d = binary.BigEndian.AppendUint32(d, w)
		d = binary.BigEndian.AppendUint32(d, h)
		d = binary.BigEndian.AppendUint32(d, 0) // region X
		d = binary.BigEndian.AppendUint32(d, 0) // region Y
		d = append(d, 0x00)                     // external combination operator OR
		d = append(d, 0x08)                     // MMR=0, GBTEMPLATE=0, TPGDON=1
		d = append(d, nominalATTemplate0...)
		out = append(out, segmentHeader(uint32(i+1), segTypeImmediateLosslessGenericRegion, 1, len(d))...)
		out = append(out, d...)
	}
	return out
}

// TestDecodeBoundsTheWholeStreamNotJustOneRegion is the resource half of the
// safety story, and it is about the SUM rather than any single header.
//
// A per-region cap caps each region on its own and the resolved page on its own.
// Neither caps the total: every region is decoded and RETAINED until the page is
// composed, and the page-size check that refuses the lot does not run until the
// last one has been decoded. Eight regions each at the per-region cap is 326
// bytes of input; measured with no whole-stream budget at all, that is 38.1
// seconds and 512 MiB of TotalAlloc before returning an error -- an
// amplification of about 1.6 million to one, LINEAR in segment count at 37 bytes
// per region, so 40 KB asks for 64 GiB and hours of CPU.
//
// This matters because extract.go now runs this decoder on bytes taken straight
// out of an untrusted PDF (byb-riy). Before that wiring, no exported entry point
// could be made to do it.
//
// The bound asserted is ALLOCATION, not wall clock. It is the sturdier of the
// two -- a busy CI runner changes elapsed time by an order of magnitude and
// changes TotalAlloc by nothing -- and it also stands in for the time, since the
// only expensive thing here is decoding pixels and pixels cannot be decoded
// without a bitmap to put them in.
func TestDecodeBoundsTheWholeStreamNotJustOneRegion(t *testing.T) {
	// 8192 x 4096 is half of MaxPagePixels, so every one of these passes the cap
	// on a single region, and the page is page-covering so the overdraw rule has
	// nothing to say either. Only their sum is absurd.
	s := emptyRegionSegments(8192, 4096, 8192, 4096, 8)
	if len(s) != 30+8*37 {
		t.Fatalf("hostile stream is %d bytes; want %d", len(s), 30+8*37)
	}

	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	start := time.Now()
	got, err := DecodeEmbeddedStream(s)
	elapsed := time.Since(start)
	runtime.ReadMemStats(&after)
	grew := after.TotalAlloc - before.TotalAlloc
	t.Logf("%d bytes in: err = %v, elapsed = %v, TotalAlloc grew by %d bytes", len(s), err, elapsed, grew)

	if err == nil {
		t.Fatalf("DecodeEmbeddedStream() returned a %dx%d bitmap for 8 regions of %d pixels each; want an error",
			got.W, got.H, MaxPagePixels)
	}
	if errors.Is(err, ErrUnsupportedFeature) {
		t.Errorf("error = %v; this is a resource refusal, not a coding feature byblos has "+
			"not implemented, so it must not carry ErrUnsupportedFeature -- a caller reading "+
			"that sentinel would file the page as recoverable by a future decoder", err)
	}
	if !strings.Contains(err.Error(), "budget") {
		t.Errorf("error = %v; want it to name the cumulative pixel budget, so the refusal is "+
			"distinguishable from the per-region and per-page caps", err)
	}
	// 512 MiB was measured with no whole-stream budget. 1 MiB is far above what
	// parsing 326 bytes of headers costs and far below one region's 2 MiB packed
	// bitmap, so nothing can pass this having decoded even one region.
	const bound = 1 << 20
	if grew > bound {
		t.Errorf("refusing 8 oversize regions allocated %d bytes (%.1f MiB); the limit is %d. "+
			"The cost has to be refused from the HEADERS, before any region is decoded.",
			grew, float64(grew)/(1<<20), bound)
	}
}

// TestDecodeAcceptsSeveralRegionsWithinTheBudget is the other side of the
// cumulative budget: a stream whose regions sum to well under the cap must still
// decode, and each region must land where its own header puts it.
//
// Without this the budget could be set to anything at all -- zero included --
// and the test above would still pass.
func TestDecodeAcceptsSeveralRegionsWithinTheBudget(t *testing.T) {
	tile := figureH6()
	tile.MaskPadding()
	pi := pageInfoSegmentData(120, 100)
	s := append(segmentHeader(0, segTypePageInformation, 1, len(pi)), pi...)
	at := [][2]int{{0, 0}, {60, 0}, {0, 50}, {60, 50}}
	for i, p := range at {
		d := genericRegionSegmentData(tile, p[0], p[1], true)
		s = append(s, segmentHeader(uint32(i+1), segTypeImmediateLosslessGenericRegion, 1, len(d))...)
		s = append(s, d...)
	}

	got, err := DecodeEmbeddedStream(s)
	if err != nil {
		t.Fatalf("DecodeEmbeddedStream() on four 54x44 regions of a 120x100 page: %v", err)
	}
	want := NewBitmap(120, 100)
	for _, p := range at {
		for y := 0; y < tile.H; y++ {
			for x := 0; x < tile.W; x++ {
				if tile.Get(x, y) != 0 {
					want.Set(x+p[0], y+p[1], 1)
				}
			}
		}
	}
	assertBitmapsIdentical(t, "four-tiles", got, want)
}

// handHeader assembles one T.88 7.2 segment header byte by byte, in the wide
// forms this package's own encoder never writes: referred-to segment numbers
// two and four bytes wide (7.2.5), the long-form referred-to count (7.2.4), and
// a four-byte page association (7.2.6).
//
// Every width is a literal stated by the case rather than a value derived here,
// so the header is an independent reading of the standard and not a second copy
// of the arithmetic under test. Deriving retainBytes from len(refs) with the
// same expression parseSegments uses would make the long-form case agree with
// any mutation of it.
type handHeader struct {
	num         uint32
	typ         byte // 0-63; goes in bits 0-5 of the flags byte
	deferred    bool // flags bit 7, "deferred non-retain"
	page4       bool // flags bit 6: four-byte page association
	refs        []uint32
	refBytes    int  // bytes per referred-to segment number: 1, 2 or 4
	longCount   bool // select the 7.2.4 long form for the referred-to count
	retainBytes int  // long form only: ceil((count+1)/8), stated per case
	data        []byte
}

func (h handHeader) bytes() []byte {
	out := binary.BigEndian.AppendUint32(nil, h.num)
	flags := h.typ
	if h.page4 {
		flags |= 0x40
	}
	if h.deferred {
		flags |= 0x80
	}
	out = append(out, flags)
	if h.longCount {
		// The long form is a four-byte field whose top three bits are the 111
		// selector and whose low 29 bits are the count, then the retain bytes.
		out = binary.BigEndian.AppendUint32(out, 0xE0000000|uint32(len(h.refs)))
		out = append(out, make([]byte, h.retainBytes)...)
	} else {
		out = append(out, byte(len(h.refs))<<5)
	}
	for _, r := range h.refs {
		switch h.refBytes {
		case 1:
			out = append(out, byte(r))
		case 2:
			out = binary.BigEndian.AppendUint16(out, uint16(r))
		default:
			out = binary.BigEndian.AppendUint32(out, r)
		}
	}
	if h.page4 {
		out = binary.BigEndian.AppendUint32(out, 1)
	} else {
		out = append(out, 1)
	}
	out = binary.BigEndian.AppendUint32(out, uint32(len(h.data)))
	return append(out, h.data...)
}

// TestParseSegmentsHeaderArithmetic pins the variable-width parts of the T.88
// 7.2 segment header: how many bytes a referred-to segment number takes (7.2.5,
// sized by THIS segment's number), the long form of the referred-to count and
// its retain bytes (7.2.4), and the width of the page association (7.2.6).
//
// None of it is reachable through this package's own encoder, which writes
// segment numbers 0 and 1, no references and a one-byte page association --
// eleven fixed bytes every time. Inbound archive JBIG2 is under no such
// restriction, and every one of these widths sits directly in front of the data
// length field, so getting one wrong does not mis-set a flag: it slides the
// parse and turns a good stream into a corrupt one, or worse, into a different
// stream that still parses.
//
// The cases are chosen to sit ON the boundaries. Each carries a distinctive
// three-byte payload, so a header read one byte long or short reads a garbage
// data length and the segment does not come back intact.
func TestParseSegmentsHeaderArithmetic(t *testing.T) {
	payload := []byte{0xAA, 0xBB, 0xCC}
	cases := []struct {
		name string
		h    handHeader
	}{
		// T.88 7.2.5: one byte per referred-to segment number while this
		// segment's number is at most 256, two while it is at most 65536, four
		// above that. Both boundaries are inclusive on the lower side, so 256
		// and 65536 are the numbers that tell "greater than" from "at least".
		{"ref-1-byte-at-256", handHeader{num: 256, typ: segTypeImmediateGenericRegion,
			refs: []uint32{1}, refBytes: 1, data: payload}},
		{"ref-2-byte-at-257", handHeader{num: 257, typ: segTypeImmediateGenericRegion,
			refs: []uint32{1}, refBytes: 2, data: payload}},
		{"ref-2-byte-at-65536", handHeader{num: 65536, typ: segTypeImmediateGenericRegion,
			refs: []uint32{1}, refBytes: 2, data: payload}},
		{"ref-4-byte-at-65537", handHeader{num: 65537, typ: segTypeImmediateGenericRegion,
			refs: []uint32{1}, refBytes: 4, data: payload}},

		// T.88 7.2.4 long form. Eight references need ceil(9/8) = 2 retain
		// bytes; seven would need one, and so would a count of eight computed
		// as ceil(count/8). Eight is the smallest count where the two differ.
		{"long-count-8-refs", handHeader{num: 3, typ: segTypeImmediateGenericRegion,
			refs: []uint32{1, 2, 3, 4, 5, 6, 7, 8}, refBytes: 1,
			longCount: true, retainBytes: 2, data: payload}},

		// The type is bits 0-5 of the flags byte. Bit 7 is "deferred
		// non-retain" and bit 6 selects the page association width, so a
		// segment carrying either of them must still read as its own type --
		// and bit 7 must NOT be what widens the page association.
		{"deferred-bit-set", handHeader{num: 4, typ: segTypeImmediateGenericRegion,
			deferred: true, data: payload}},

		// T.88 7.2.6: bit 6 set means the page association is four bytes.
		{"page-association-4-byte", handHeader{num: 5, typ: segTypeImmediateGenericRegion,
			page4: true, data: payload}},
		{"page-association-4-byte-and-deferred", handHeader{num: 6, typ: segTypeImmediateGenericRegion,
			page4: true, deferred: true, refs: []uint32{1}, refBytes: 1, data: payload}},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := parseSegments(c.h.bytes())
			if err != nil {
				t.Fatalf("parseSegments() error = %v", err)
			}
			if len(got) != 1 {
				t.Fatalf("parseSegments() returned %d segments; want 1", len(got))
			}
			if got[0].number != c.h.num {
				t.Errorf("segment number = %d; want %d", got[0].number, c.h.num)
			}
			if got[0].typ != c.h.typ {
				t.Errorf("segment type = %d; want %d", got[0].typ, c.h.typ)
			}
			if !bytes.Equal(got[0].data, c.h.data) {
				t.Errorf("segment data = % 02X; want % 02X -- the header was read the wrong "+
					"length, so the data length field was taken from the wrong offset",
					got[0].data, c.h.data)
			}
		})
	}

	// Two wide headers back to back. A single segment can be parsed at the
	// wrong length and still yield the right data if the miscount happens to
	// land inside a run of zeros; a second segment has to start exactly where
	// the first ends, so the offset arithmetic is checked as well as the widths.
	t.Run("two-in-sequence", func(t *testing.T) {
		first := handHeader{num: 70000, typ: segTypePageInformation, refs: []uint32{1, 2},
			refBytes: 4, page4: true, data: []byte{0x11, 0x22}}
		second := handHeader{num: 70001, typ: segTypeImmediateGenericRegion,
			refs: []uint32{1, 2, 3, 4, 5, 6, 7, 8, 9}, refBytes: 4,
			longCount: true, retainBytes: 2, deferred: true, data: []byte{0x33}}
		got, err := parseSegments(append(first.bytes(), second.bytes()...))
		if err != nil {
			t.Fatalf("parseSegments() error = %v", err)
		}
		if len(got) != 2 {
			t.Fatalf("parseSegments() returned %d segments; want 2", len(got))
		}
		if got[0].number != 70000 || got[0].typ != segTypePageInformation ||
			!bytes.Equal(got[0].data, first.data) {
			t.Errorf("first segment = {%d, %d, % 02X}; want {70000, %d, % 02X}",
				got[0].number, got[0].typ, got[0].data, segTypePageInformation, first.data)
		}
		if got[1].number != 70001 || got[1].typ != segTypeImmediateGenericRegion ||
			!bytes.Equal(got[1].data, second.data) {
			t.Errorf("second segment = {%d, %d, % 02X}; want {70001, %d, % 02X}",
				got[1].number, got[1].typ, got[1].data, segTypeImmediateGenericRegion, second.data)
		}
	})
}

// TestResourceBudgetsArePinnedFromAbove is the test round 1 and round 2 of this
// work both lacked, and lacking it is why a broken bound looked fixed.
//
// Every budget test above asks "is this hostile stream refused?", which pins the
// budgets only FROM BELOW: multiply MaxPagePixels or maxStreamBitmapBytes by ten
// and every one of them still passes, because their hostile streams are absurd
// by orders of magnitude rather than by one pixel. Measured: nine mutations
// (each constant x2, x3, x10) against the whole suite, nine survivors.
//
// So these streams are exactly ONE unit over the rule they pin, and no other
// rule can be what refuses them -- which is the property that makes each
// subtest name a single constant. The dimensions are LITERALS and must stay
// literals: written in terms of the constants they would move with the mutation
// and stop killing it, which is the whole failure being corrected here.
func TestResourceBudgetsArePinnedFromAbove(t *testing.T) {
	// Rule 1, the OUTPUT budget on the PAGE, and the one this table was missing.
	// Rules 2, 3 and 4 are all pinned below; rule 1 was not, and deleting its
	// clause from segment_decode.go -- leaving "if bmBytes > maxStreamBitmapBytes"
	// alone on that line -- built clean and passed the whole suite. A caller of
	// PageSize, DecodeEmbeddedStream or the exported DecodeJBIG2Generic then gets
	// back a page at TWICE the documented MaxPagePixels ceiling, silently, from a
	// 67-byte stream, and MaxPagePixels is exported precisely because
	// decodeJBIG2Placement applies the same number to an image dictionary's
	// declared /Width and /Height.
	//
	// 8321 x 8065 = 67,108,865 pixels, one over the budget. Those are the two
	// factors of MaxPagePixels+1 (= 5 x 53 x 157 x 1613) that come nearest to
	// square, so the page is a plausible page shape rather than a sliver, and
	// every other rule is left with a wide margin:
	//
	//   page width guard : 8,321, against a limit of 67,108,864.
	//   rule 2 (work)    : the regions sum to 1 pixel, against 67,108,864.
	//   one-region cap   : 1 x 1 = 1 pixel, against 67,108,864.
	//   rule 4 (memory)  : the page packs to ((8321+7)/8) * 8065 = 1041 * 8065 =
	//                      8,395,665 bytes and the region to 1, so 8,395,666 in
	//                      all -- 8,381,550 bytes INSIDE the 16,777,216 budget.
	//   rule 3 (overdraw): 1 pixel, against 4 * 67,108,865 + 65,536 = 268,500,996.
	//   height guard     : the page resolves to 8,065 rows.
	//
	// So rule 1 is the only rule that can fire, and if it stops firing this
	// stream is ACCEPTED rather than refused by something else. The dimensions
	// are LITERALS for the same reason as every other case here.
	t.Run("page-pixels", func(t *testing.T) {
		s := emptyRegionSegments(8321, 8065, 1, 1, 1)
		w, h, err := PageSize(s)
		if err == nil {
			t.Fatalf("PageSize() = %dx%d, err = nil; an 8321x8065 page is 67,108,865 pixels, "+
				"one over the 67,108,864 output budget, and it must be refused. Accepting it "+
				"hands a caller a page past the documented MaxPagePixels ceiling -- which is "+
				"the ceiling the PDF layer sizes its *image.Gray from.", w, h)
		}
		if !strings.Contains(err.Error(), "budget") {
			t.Errorf("error = %v; want the page budget refusal", err)
		}
		if !strings.Contains(err.Error(), "67108865 page pixels") {
			t.Errorf("error = %v; want it to name the 67,108,865 page pixels it refused, so "+
				"the refusal is attributable to the page and not to the regions or the memory "+
				"budget -- neither of which this stream comes close to", err)
		}
	})

	// The page WIDTH guard, which applies MaxPagePixels to a single header field
	// before any region header is read.
	//
	// This one is honest about what it can pin. On a 64-bit int the guard cannot
	// change an ACCEPT into a REJECT: any width past MaxPagePixels makes
	// pageW * pageH past MaxPagePixels too, so rule 1 refuses the same stream one
	// gate later. What the guard uniquely provides is the DIAGNOSIS -- "the width
	// field alone is out of range", produced from the 19-byte page information
	// segment without parsing a single region -- and that is what is pinned here.
	// Deleting the guard leaves rule 1's "a 67108865x1 page under 1 region(s)
	// wants ..." message, which does not name the width, and this fails.
	//
	// 67,108,865 x 1 is one pixel of width over the limit.
	t.Run("page-width", func(t *testing.T) {
		s := emptyRegionSegments(1, 1, 1, 1, 1)
		binary.BigEndian.PutUint32(s[11:15], 67108865)
		_, _, err := PageSize(s)
		if err == nil {
			t.Fatal("a page 67,108,865 pixels wide is one past the limit on the width field " +
				"and must be refused")
		}
		if !strings.Contains(err.Error(), "pixels wide") {
			t.Errorf("error = %v; want the refusal to name the WIDTH field. The width is "+
				"checked on its own so an out-of-range page information segment is diagnosed "+
				"from 19 bytes, before any region header is parsed; without that check the "+
				"stream is still refused, but only by the page-pixel budget and only after "+
				"every region has been walked", err)
		}
	})

	// Rule 2, the WORK budget on the sum of the regions. An 8192x8192 region
	// (67,108,864 pixels, exactly the budget and exactly the cap on one region)
	// plus a 1x1 region is 67,108,865 -- one pixel over.
	//
	// Nothing else can be what refuses it. The page is 8192x8191 = 67,092,481,
	// inside rule 1; 67,108,865 is 0.25x of 4 times that page, nowhere near rule
	// 3; and the three bitmaps pack to 8,387,584 + 8,388,608 + 1 = 16,776,193
	// bytes, 1,023 inside rule 4.
	t.Run("pixels", func(t *testing.T) {
		s := emptyRegionSegments(8192, 8191, 8192, 8192, 1)
		s = append(s, segmentHeader(2, segTypeImmediateLosslessGenericRegion, 1, 26)...)
		s = append(s, regionSegmentData(1, 1)...)
		_, _, err := PageSize(s)
		if err == nil {
			t.Fatal("an 8192x8192 region beside a 1x1 region is 67,108,865 pixels of decoding; " +
				"the work budget is 67,108,864 and it must refuse this. Accepting it means " +
				"MaxPagePixels has been raised without this test being retuned.")
		}
		if !strings.Contains(err.Error(), "budget") {
			t.Errorf("error = %v; want the cumulative budget refusal", err)
		}
	})

	// Rule 4, the MEMORY budget. A 1-pixel-wide region of 14,680,065 rows packs
	// to 14,680,065 bytes; the 4096x4096 page beside it packs to 2,097,152. That
	// is 16,777,217 bytes, one over.
	//
	// Nothing else can be what refuses it. The region is 14,680,065 pixels, less
	// than a quarter of rule 2's budget; the page is 16,777,216, a quarter of
	// rule 1's; and the regions are 0.875x the page, well under rule 3. The page
	// has to be that large precisely so rule 3 is not what fires -- on a 1x1
	// page a 14-million-pixel region is refused as waste, one rule earlier.
	t.Run("bytes", func(t *testing.T) {
		s := emptyRegionSegments(4096, 4096, 1, 14680065, 1)
		_, _, err := PageSize(s)
		if err == nil {
			t.Fatal("a 1x14680065 region beside a 4096x4096 page packs to 16,777,217 bytes, " +
				"one over the 16 MiB memory budget, and neither the work budget nor the " +
				"overdraw budget can be what refuses it. Accepting it means " +
				"maxStreamBitmapBytes has been raised.")
		}
		if !strings.Contains(err.Error(), "budget") {
			t.Errorf("error = %v; want the cumulative budget refusal, not a per-header cap", err)
		}
	})

	// Rule 3, the OVERDRAW budget, and it takes TWO subtests because it is two
	// constants: the regions may sum to maxRegionOverdraw times the page PLUS
	// overdrawFloorPixels. The two terms are equal at a page of
	// overdrawFloorPixels / maxRegionOverdraw = 16,384 pixels -- a 128x128 page.
	// On anything larger the ratio governs and the floor is a rounding error; on
	// anything smaller the floor governs. So one case pins whichever term
	// dominates at the page size it happened to choose, and the other constant
	// can then be raised without a test noticing. Each is pinned below on a page
	// where the other cannot be what refuses the stream.
	//
	// THE RATIO, on a page 256x larger than that crossover. A 2048x2048 page can
	// show 4,194,304 pixels, so 4x the page is 16,777,216 and the floor adds
	// 0.4% to it. The regions sum to 17,039,360 -- 4x the page plus FOUR times
	// the floor, which is past the bound however the floor alone is moved, and
	// inside it as soon as the ratio reaches 5.
	//
	// Nothing else can be what refuses it. 17,039,360 pixels is a quarter of
	// rule 2's budget, the page is a sixteenth of rule 1's, and the three
	// bitmaps pack to 524,288 + 2,097,152 + 32,768 = 2,654,208 bytes, a sixth of
	// rule 4's. Only the ratio between the regions and the page is out of range.
	t.Run("overdraw-ratio", func(t *testing.T) {
		s := emptyRegionSegments(2048, 2048, 4096, 4096, 1)
		s = append(s, segmentHeader(2, segTypeImmediateLosslessGenericRegion, 1, 26)...)
		s = append(s, regionSegmentData(4096, 64)...)
		_, _, err := PageSize(s)
		if err == nil {
			t.Fatal("a 2048x2048 page under regions summing to 17,039,360 pixels asks to " +
				"decode more than 4x what the page can show, and every pixel past the " +
				"page's edges is discarded by composite(). Accepting it means " +
				"maxRegionOverdraw has been raised, and 67 bytes buy decoding that " +
				"produces no output again.")
		}
		if !strings.Contains(err.Error(), "budget") {
			t.Errorf("error = %v; want the overdraw budget refusal", err)
		}
		// The refusal states the ratio it applied, so the ratio is pinned to its
		// exact value and not merely to within the margin this shape leaves.
		if !strings.Contains(err.Error(), "admits 4x the page") {
			t.Errorf("error = %v; want it to name the 4x ratio it applied", err)
		}
	})

	// THE FLOOR, on the smallest page there is, where the ratio contributes four
	// pixels and cannot be what refuses anything. A 1x1 page admits
	// 4 + 65,536 = 65,540 pixels of decoding; one 64x1025 region asks for 65,600.
	//
	// The 60-pixel margin over the bound is deliberate, and it is what makes this
	// case name a SINGLE constant: it is far too small for any change to
	// maxRegionOverdraw to close -- even a ratio of 8 moves the bound to 65,544 --
	// and far too small for any increase in overdrawFloorPixels to survive.
	//
	// Nothing else can be what refuses it. 65,600 pixels is a thousandth of rule
	// 2's budget, the page is one pixel, and the two bitmaps pack to 8,201 bytes.
	t.Run("overdraw-floor", func(t *testing.T) {
		s := emptyRegionSegments(1, 1, 64, 1025, 1)
		_, _, err := PageSize(s)
		if err == nil {
			t.Fatal("a 1x1 page under a 64x1025 region is 65,600 MQ decisions for a " +
				"one-pixel answer, past the 65,540 the overdraw floor allows on a page " +
				"that size. Accepting it means overdrawFloorPixels has been raised, and " +
				"on a small page the floor is the whole of rule 3 -- the 67-byte " +
				"1x1-page/8192x4095-region stream is refused by nothing else.")
		}
		if !strings.Contains(err.Error(), "budget") {
			t.Errorf("error = %v; want the overdraw budget refusal", err)
		}
		// The refusal states the floor it applied. Asserting the 60-pixel margin
		// above cannot pin the floor closer than 60 pixels; this pins it to the
		// pixel, which is what kills a floor moved by one.
		if !strings.Contains(err.Error(), "plus 65536 pixels") {
			t.Errorf("error = %v; want it to name the 65,536-pixel floor it applied", err)
		}
	})

	// One region of 67,108,865 pixels -- 5 x 13,421,773, one over the cap on a
	// single region -- refused from its 26-byte header before the arithmetic
	// decoder is started.
	t.Run("one-region", func(t *testing.T) {
		if _, _, err := decodeGenericRegionSegment(regionSegmentData(5, 13421773)); err == nil {
			t.Fatal("a 5x13421773 region is 67,108,865 pixels, one over the 67,108,864 cap on " +
				"one region, and must be refused from its header")
		}
	})
}

// TestResourceBudgetsArePinnedFromBelow is the other jaw. Without it the pins
// above are satisfied by a budget of zero, which would refuse every real page.
//
// The dimensions are the largest scan in extract.go's measured 150-400 DPI
// envelope, and they are literals for the same reason.
func TestResourceBudgetsArePinnedFromBelow(t *testing.T) {
	// 400-dpi A4, 3307x4677, page-covering: 30,933,678 pixels and 3,872,556
	// packed bytes once the page is charged alongside the region. Both budgets
	// must admit it, or byblos cannot read back a scan it is expected to write.
	for _, c := range []struct {
		name string
		w, h int
	}{
		{"400dpi-A4", 3307, 4677},
		{"400dpi-Letter", 3400, 4400},
		{"300dpi-A4", 2480, 3508},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := emptyRegionSegments(c.w, c.h, uint32(c.w), uint32(c.h), 1)
			w, h, err := PageSize(s)
			if err != nil {
				t.Fatalf("a %dx%d page-covering scan is inside byblos's own 150-400 DPI "+
					"envelope and must be decodable: %v", c.w, c.h, err)
			}
			if w != c.w || h != c.h {
				t.Errorf("PageSize() = %dx%d; want %dx%d", w, h, c.w, c.h)
			}
		})
	}

	// Both budgets at once, on the single shape that touches each of them
	// EXACTLY. An 8192x8192 page under a page-covering region is 67,108,864
	// pixels of page and 67,108,864 of region -- rules 1 and 2 to the pixel --
	// and the two bitmaps pack to 8,388,608 bytes apiece, which is 16,777,216,
	// rule 4 to the byte.
	//
	// Nothing else in this file sits on the boundary from the ADMITTED side. The
	// cases above are all one pixel or one byte over and assert a refusal, so
	// lowering a budget by one leaves every one of them still refused and
	// survives the file; this is what makes MaxPagePixels and
	// maxStreamBitmapBytes pinned to their exact values rather than to within a
	// factor of two.
	t.Run("both-budgets-at-their-exact-boundary", func(t *testing.T) {
		w, h, err := PageSize(emptyRegionSegments(8192, 8192, 8192, 8192, 1))
		if err != nil {
			t.Fatalf("an 8192x8192 page-covering stream is 67,108,864 pixels on each side of "+
				"the page/region split and packs to exactly 16,777,216 bytes -- the pixel "+
				"budget and the memory budget both to the unit, and both budgets are written "+
				"as limits rather than as strict bounds. Refusing it means one of them has "+
				"been lowered: %v", err)
		}
		if w != 8192 || h != 8192 {
			t.Errorf("PageSize() = %dx%d; want 8192x8192", w, h)
		}
	})

	// And the memory budget from below, on the shape only it governs: a
	// 1-pixel-wide page-covering region of 8 million rows is 8,000,000 bytes for
	// the region and as much again for the page, 16,000,000 of the 16,777,216
	// the memory budget allows.
	//
	// It is page-covering because the overdraw rule now governs the shape this
	// used to have. A 1x8000000 region on a 1x1 page is eight million pixels of
	// decoding for a one-pixel answer, which is the byb-riy defect in miniature
	// and is refused on purpose; what the memory budget has to keep admitting is
	// the same skinny bitmap when the page can actually SHOW it.
	t.Run("skinny-within-memory-budget", func(t *testing.T) {
		if _, _, err := PageSize(emptyRegionSegments(1, 8000000, 1, 8000000, 1)); err != nil {
			t.Fatalf("a page-covering 1x8000000 region packs to 16,000,000 bytes with its "+
				"page, inside the 16 MiB memory budget, and must be admitted: %v", err)
		}
	})

	// And the overdraw budget from below, on the boundary it draws. Four regions
	// of 4096x1024 sum to 16,777,216 pixels, exactly four times a 2048x2048
	// page: the most layering rule 3 admits, and it must admit it. Without this
	// the overdraw pin above is satisfied by maxRegionOverdraw = 1, which would
	// refuse T.88's own clip rule -- a region legitimately overhanging its page.
	t.Run("four-layers-over-one-page", func(t *testing.T) {
		if _, _, err := PageSize(emptyRegionSegments(2048, 2048, 4096, 1024, 4)); err != nil {
			t.Fatalf("four 4096x1024 regions over a 2048x2048 page sum to exactly four times "+
				"the page, which the overdraw budget admits, and must be decodable: %v", err)
		}
	})

	// The floor under that ratio, on the shapes a ratio alone gets wrong. T.88
	// 6.2.4's clip rule exists so that a region may legitimately overhang the
	// page it lands on, and on a SMALL page any overhang at all is a large
	// multiple of the page while costing an amount of decoding no caller can
	// perceive. Each of these is a legal document, and refusing one is as much a
	// defect as admitting an attack; each costs under 2 ms.
	//
	// The 300x300-on-100x100 case is the one that FIXES the floor's magnitude
	// from below: 90,000 - 4*10,000 = 50,000 pixels of floor are required, so
	// halving overdrawFloorPixels to 32,768 refuses it. The 54x44-on-20x20 case
	// is not hypothetical either -- it is the stream
	// TestDecodeClipsRegionToPage/clipped decodes. All five shapes are decoded
	// for real, not merely planned, by TestDecodeAdmitsLegalClipCases.
	for _, c := range []struct {
		name                     string
		pageW, pageH, regW, regH int
	}{
		{"20x20-under-54x44", 20, 20, 54, 44},
		{"20x20-under-60x50", 20, 20, 60, 50},
		{"64x64-under-256x256", 64, 64, 256, 256},
		{"100x100-under-300x300", 100, 100, 300, 300},
	} {
		t.Run("clipped/"+c.name, func(t *testing.T) {
			s := emptyRegionSegments(c.pageW, c.pageH, uint32(c.regW), uint32(c.regH), 1)
			if _, _, err := PageSize(s); err != nil {
				t.Fatalf("a %dx%d region overhanging a %dx%d page is %d pixels of decoding, "+
					"which T.88 6.2.4 clips rather than refusing, and which the overdraw "+
					"floor exists to admit: %v",
					c.regW, c.regH, c.pageW, c.pageH, c.regW*c.regH, err)
			}
		})
	}

	// And the floor at its exact boundary, which is what pins it to the pixel
	// rather than to within a factor of two. On a 1x1 page the ratio contributes
	// four pixels, so the bound is 4 + overdrawFloorPixels = 65,540 and a single
	// 4x16385 region asks for exactly that.
	//
	// This is the concession the floor buys, stated plainly: 65,540 MQ decisions
	// for a one-pixel answer, all but one of them discarded by composite(). It
	// is 1.5 ms, a thousandth of the 1.53 s that decoding one maximal legitimate
	// page costs unconditionally, and that ratio is where the constant comes
	// from. Any reduction of overdrawFloorPixels at all fails here.
	t.Run("overdraw-floor-at-its-exact-boundary", func(t *testing.T) {
		if _, _, err := PageSize(emptyRegionSegments(1, 1, 4, 16385, 1)); err != nil {
			t.Fatalf("a 1x1 page under a 4x16385 region is 65,540 pixels of decoding, "+
				"exactly 4x the page plus the 65,536-pixel overdraw floor, and the floor "+
				"means nothing if the shape at its own boundary is refused: %v", err)
		}
	})
}

// regionSegmentData builds a generic region segment body of the declared size
// with no coded data, for tests that only need the header to be read.
func regionSegmentData(w, h uint32) []byte {
	d := make([]byte, 0, 26)
	d = binary.BigEndian.AppendUint32(d, w)
	d = binary.BigEndian.AppendUint32(d, h)
	d = binary.BigEndian.AppendUint32(d, 0)
	d = binary.BigEndian.AppendUint32(d, 0)
	d = append(d, 0x00, 0x08)
	return append(d, nominalATTemplate0...)
}

// segmentsOnPages builds the same two-segment stream emptyRegionSegments does --
// an 8x8 page and one 1x1 region carrying no coded data -- with the T.88 7.2.6
// page association of each segment given explicitly rather than hard-coded to 1.
func segmentsOnPages(pageInfoPage, regionPage byte) []byte {
	pi := pageInfoSegmentData(8, 8)
	out := append(segmentHeader(0, segTypePageInformation, pageInfoPage, len(pi)), pi...)
	d := regionSegmentData(1, 1)
	out = append(out, segmentHeader(1, segTypeImmediateLosslessGenericRegion, regionPage, len(d))...)
	return append(out, d...)
}

// TestDecodeRefusesSegmentsOnDifferentPages records what this package decided to
// do with the page association of T.88 7.2.6, and pins it. Both halves matter:
// the two refusals AND the four shapes that must still be accepted.
//
// The decision is in parseSegments's comment and it is to REFUSE disagreement,
// not to ignore the field. The alternative was defensible -- ISO 32000-1:2008
// 7.4.7 gives a PDF-embedded stream a single page, so nothing needs routing --
// and it is what this decoder did until now. It was found by mutating a test
// FIXTURE rather than the production code: emptyRegionSegments with its region
// on page 2 instead of page 1 passed the entire suite, because nothing anywhere
// looked at the field, and a region declared on a page the stream does not
// describe was composited onto the page it does. That is a raster no decoder
// honouring the field produces, handed back with no error -- the silent accept
// this package's doc calls worse than a refusal.
//
// What is NOT refused is as much of the decision as what is, which is why the
// accept cases are here and not left implicit:
//
//   - Two segments agreeing on page 2 are accepted. The property that makes
//     compositing correct is that the segments AGREE, not that they say 1;
//     PDF's rule is about how many pages a stream carries, not their numbers.
//     A check written as "page != 1" would pass a test that only had the
//     disagreement case, and it would refuse a legal stream.
//   - A region associated with page 0 is accepted. Zero is not a page number in
//     7.2.6 -- it marks a segment as associated with no page, which is what a
//     global or end-of-file segment carries -- so refusing on it would refuse a
//     legal stream that carries one inline.
func TestDecodeRefusesSegmentsOnDifferentPages(t *testing.T) {
	for _, c := range []struct {
		name                     string
		pageInfoPage, regionPage byte
		wantErr                  bool
	}{
		{"both-on-page-1", 1, 1, false},
		{"both-on-page-2", 2, 2, false},
		{"region-on-another-page", 1, 2, true},
		{"page-information-on-another-page", 2, 1, true},
		{"region-associated-with-no-page", 1, 0, false},
		{"page-information-associated-with-no-page", 0, 1, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, err := DecodeEmbeddedStream(segmentsOnPages(c.pageInfoPage, c.regionPage))
			if !c.wantErr {
				if err != nil {
					t.Fatalf("DecodeEmbeddedStream() error = %v; a page information segment on "+
						"page %d under a region on page %d is a stream whose segments do not "+
						"DISAGREE, and refusing it refuses a legal stream", err, c.pageInfoPage, c.regionPage)
				}
				if got.W != 8 || got.H != 8 {
					t.Fatalf("page is %dx%d; want 8x8", got.W, got.H)
				}
				return
			}
			if err == nil {
				t.Fatalf("DecodeEmbeddedStream() returned a %dx%d page for a stream whose page "+
					"information segment is on page %d and whose region is on page %d. The region "+
					"is declared on a page this stream does not describe; compositing it onto the "+
					"page that is described hands back a raster a decoder honouring T.88 7.2.6 "+
					"would not produce, and says nothing.", got.W, got.H, c.pageInfoPage, c.regionPage)
			}
			t.Logf("refused: %v", err)
			// The message has to name BOTH pages, or a caller cannot tell which
			// segment is the odd one out from the error alone.
			for _, want := range []string{
				fmt.Sprintf("page %d", c.pageInfoPage),
				fmt.Sprintf("page %d", c.regionPage),
			} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error = %v; want it to name %q", err, want)
				}
			}
			if errors.Is(err, ErrUnsupportedFeature) {
				t.Errorf("error = %v; a stream whose segments disagree about their page is "+
					"MALFORMED, not a coding feature this package has not implemented. Filing it "+
					"under the sentinel tells an archive a future decoder recovers the page.", err)
			}
		})
	}
}

// TestBothBudgetsAreReachable pins the relationship BETWEEN the two constants,
// which neither of the tests above can see.
//
// A bitmap's pixels never exceed eight times its packed bytes. So a memory
// budget at or below MaxPagePixels/8 refuses everything the pixel budget would
// have refused, one gate earlier, and the pixel budget becomes dead policy that
// no stream can reach; a memory budget at or above MaxPagePixels is dead in the
// same way from the other side. Either mutation is invisible to a test that only
// asks whether a hostile stream is refused, because it still is -- by the other
// budget. Halving maxStreamBitmapBytes survived every other test in this file.
//
// A dead resource bound is exactly how the first version of this went wrong: a
// constant nothing reaches can be changed to anything at all without a test
// noticing, and the next reader trusts it.
func TestBothBudgetsAreReachable(t *testing.T) {
	if maxStreamBitmapBytes*8 <= MaxPagePixels {
		t.Errorf("maxStreamBitmapBytes = %d, so no stream can exceed %d pixels without "+
			"exceeding it first; MaxPagePixels = %d is unreachable and bounds nothing",
			maxStreamBitmapBytes, maxStreamBitmapBytes*8, MaxPagePixels)
	}
	if maxStreamBitmapBytes >= MaxPagePixels {
		t.Errorf("maxStreamBitmapBytes = %d is not below MaxPagePixels = %d, so even a "+
			"1-pixel-wide bitmap hits the pixel budget first and the memory budget is "+
			"unreachable -- which leaves the 8x row-padding amplification unbounded",
			maxStreamBitmapBytes, MaxPagePixels)
	}
}

// TestDecodeRefusesAPageWithNoHeight pins the guard on the resolved page
// height, which is the one check in planStream that stands between a legal-
// looking header and a panic.
//
// A page information segment may declare a height of 0. Nothing else refuses
// it: a zero-height page is 0 pixels, so the output budget is satisfied; it
// packs to 0 bytes, so the memory budget is satisfied; and 0 pixels of page
// makes the overdraw budget's floor of 65,536 the only thing the regions have to
// fit under, which one 1x1 region does. Deleting "pageH <= 0" therefore does
// not merely lose an error message -- planStream RETURNS SUCCESSFULLY, PageSize
// reports a 64x0 page, and DecodeEmbeddedStream then calls NewBitmap(64, 0),
// which panics. The height guard is the only thing making that unreachable.
//
// The assertion is on PageSize rather than on DecodeEmbeddedStream so that the
// failure is an assertion and not a crashed test binary.
func TestDecodeRefusesAPageWithNoHeight(t *testing.T) {
	s := emptyRegionSegments(64, 0, 1, 1, 1)
	w, h, err := PageSize(s)
	if err == nil {
		t.Fatalf("PageSize() = %dx%d, err = nil; a page information segment declaring a "+
			"height of 0 resolves to a page with no rows, which no budget refuses and which "+
			"NewBitmap panics on", w, h)
	}
	if !strings.Contains(err.Error(), "height") {
		t.Errorf("error = %v; want the refusal to name the page height", err)
	}
	// And the same stream through the decoder, which is where the panic would be.
	if _, err := DecodeEmbeddedStream(s); err == nil {
		t.Fatal("DecodeEmbeddedStream() accepted a page of height 0")
	}
}

// TestDecodeRefusesAPageWithNoWidth is the width half of the same guard, and it
// fails in exactly the same way: a page information segment declaring a width of
// 0 satisfies every budget -- 0 page pixels, 0 packed page bytes, and a page
// that can show nothing leaves the overdraw floor as the only bar the regions
// have to clear -- so deleting "w == 0" does not lose an error message, it makes
// PageSize report a 0x64 page with err = nil and DecodeEmbeddedStream PANIC on
// the empty pixel buffer that follows.
//
// It is a separate function from the height case because the two guards are in
// different places for different reasons: the width is checked as the page
// information segment is parsed, from 19 bytes, and the height cannot be
// checked there at all -- an unknown height (0xFFFFFFFF) is not resolved until
// every region header has been read.
func TestDecodeRefusesAPageWithNoWidth(t *testing.T) {
	s := emptyRegionSegments(0, 64, 1, 1, 1)
	w, h, err := PageSize(s)
	if err == nil {
		t.Fatalf("PageSize() = %dx%d, err = nil; a page information segment declaring a "+
			"width of 0 resolves to a page with no columns, which no budget refuses and "+
			"which the decoder panics on", w, h)
	}
	if !strings.Contains(err.Error(), "width") {
		t.Errorf("error = %v; want the refusal to name the page width", err)
	}
	if _, err := DecodeEmbeddedStream(s); err == nil {
		t.Fatal("DecodeEmbeddedStream() accepted a page of width 0")
	}
}

// TestHeaderRefusalsAreAttributedToTheFieldThatBrokeThem pins the two header
// checks that cannot be pinned as an accept/reject at all, because a later gate
// refuses exactly the same set of streams. What each one uniquely buys is the
// DIAGNOSIS -- which field was wrong, reported from the header that carried it
// -- and a refusal that names the wrong thing is a bug report nobody can act on.
//
// This is the same honesty the page-width case in TestResourceBudgetsArePinnedFromAbove
// is written with. Both checks are cheap and both are worth keeping; neither can
// claim more than this.
func TestHeaderRefusalsAreAttributedToTheFieldThatBrokeThem(t *testing.T) {
	// The memory budget inside the region loop. Two 1x8388609 regions pack to
	// 16,777,218 bytes between them -- two past the budget -- before the page is
	// charged at all, and 16,777,218 pixels is a quarter of the pixel budget, so
	// the region loop's memory check is the only thing that can fire there.
	// Delete it and the same stream is still refused, by the page's own check one
	// gate later, but the message blames "a 1x1 page" for 16 MiB the page did not
	// ask for.
	t.Run("regions-alone-over-the-memory-budget", func(t *testing.T) {
		_, _, err := PageSize(emptyRegionSegments(1, 1, 1, 8388609, 2))
		if err == nil {
			t.Fatal("two 1x8388609 regions pack to 16,777,218 bytes, two past the memory " +
				"budget, and must be refused")
		}
		if !strings.Contains(err.Error(), "the stream's 2 regions") {
			t.Errorf("error = %v; want the refusal attributed to the REGIONS. The region "+
				"loop charges memory as it goes so that a stream whose regions alone are "+
				"unaffordable is reported against the region that broke the budget, before "+
				"the page is charged; without that check the stream is still refused, but "+
				"the message blames the page", err)
		}
	})

	// A region dimension of 0. parseRegionInfo refuses it from the 17-byte
	// region segment information field; decodeGenericRegion would refuse it too,
	// after the flags and the AT pixels have been parsed and the coded data
	// reached. Deleting the earlier check moves the message from "region" to
	// "generic region" -- from the field that is wrong to the procedure that
	// tripped over it.
	t.Run("a-region-with-a-zero-dimension", func(t *testing.T) {
		_, _, err := decodeGenericRegionSegment(regionSegmentData(0, 4))
		if err == nil {
			t.Fatal("a 0x4 region has no pixels and must be refused")
		}
		if !strings.HasPrefix(err.Error(), "jbig2: region is 0x4") {
			t.Errorf("error = %v; want it attributed to the region segment information "+
				"field, which is the 17 bytes that are actually wrong", err)
		}
	})

	// The height half of the same guard. regionSegmentData(0, 4) exercises only
	// w == 0: parseRegionInfo's condition is "w == 0 || h == 0", and deleting the
	// h half leaves the case above passing, because its width is what is zero.
	//
	// A region of zero HEIGHT is the more dangerous of the two, and it is why
	// this is not merely symmetry. Its packed size is ((w+7)/8) * 0 = 0 bytes and
	// its pixel count is 0, so it is free under every one of the four budgets;
	// with the guard gone it reaches decodeGenericRegion, which allocates a
	// bitmap with no rows and reports the failure against the coded data rather
	// than against the 17 bytes of header that are wrong.
	t.Run("a-region-with-a-zero-height", func(t *testing.T) {
		_, _, err := decodeGenericRegionSegment(regionSegmentData(4, 0))
		if err == nil {
			t.Fatal("a 4x0 region has no pixels and must be refused")
		}
		if !strings.HasPrefix(err.Error(), "jbig2: region is 4x0") {
			t.Errorf("error = %v; want it attributed to the region segment information "+
				"field. A zero HEIGHT costs zero pixels and zero packed bytes, so no budget "+
				"refuses it and the region header check is the only gate there is", err)
		}
	})

	// A stream with no segments at all. parseSegments refuses a zero-byte input
	// on its own; delete that and it returns (nil, nil) -- no segments and no
	// error -- and planStream reports "no page information segment", which is
	// true of an empty stream in the way that "no windows" is true of a hole in
	// the ground. The nil-and-nil return is the part a later caller would trip
	// over; the message is what a caller reads today.
	t.Run("a-stream-with-no-segments-at-all", func(t *testing.T) {
		_, _, err := PageSize(nil)
		if err == nil {
			t.Fatal("a zero-byte stream must be refused")
		}
		if !strings.Contains(err.Error(), "contains no segments") {
			t.Errorf("error = %v; want the refusal attributed to the empty stream rather "+
				"than to a page information segment that was never going to be there", err)
		}
	})

	// A stream carrying region segments and no page information segment: 37
	// bytes, one immediate lossless generic region for an 8x8 area, and nothing
	// saying what page it lands on.
	//
	// Deleting "!sawPageInfo" does not accept it -- pageW and pageH are left at
	// zero and the height guard catches that one gate later -- but it reports
	// "page height resolves to 0", which describes a page information segment
	// that declared a zero height. There is no page information segment. A
	// producer told its page height is wrong goes looking at a field it never
	// wrote.
	t.Run("regions-with-no-page-information-segment", func(t *testing.T) {
		d := regionSegmentData(8, 8)
		s := append(segmentHeader(1, segTypeImmediateLosslessGenericRegion, 1, len(d)), d...)
		_, _, err := PageSize(s)
		if err == nil {
			t.Fatal("a stream with no page information segment has no page to compose onto " +
				"and must be refused")
		}
		if !strings.Contains(err.Error(), "no page information segment") {
			t.Errorf("error = %v; want the refusal to name the MISSING segment. Without that "+
				"check the stream is still refused, by the resolved page height, and the "+
				"message blames a height field that no segment in this stream ever set", err)
		}
	})

	// THE ORDER OF THE FOUR RULES, which segment_decode.go states is deliberate
	// -- the page is charged LAST inside the region loop's budget, and rule 3
	// runs after both because a page of unknown height is not sized until the
	// loop has finished -- and which nothing pinned. Moving rule 3 above the page
	// check passes the whole suite; all it changes is which refusal the caller
	// reads, and that is exactly what this function exists to hold still.
	//
	// The stream is 67 bytes: a 1x16000000 page under one 8192x8179 region. It is
	// over TWO rules at once, which is what makes it able to tell them apart:
	//
	//   rule 4 (memory)  : a 1-pixel-wide page pads to one byte a row, so the
	//                      page packs to 16,000,000 bytes and the region to
	//                      1024 * 8179 = 8,375,296 -- 24,375,296 in all, against
	//                      a 16,777,216 budget.
	//   rule 3 (overdraw): 8192 * 8179 = 67,002,368 region pixels, against
	//                      4 * 16,000,000 + 65,536 = 64,065,536.
	//
	// And under neither of the other two: the page is 16,000,000 pixels, a
	// quarter of rule 1's budget, and the regions sum to 67,002,368, inside rule
	// 2's 67,108,864.
	//
	// The tip reports the MEMORY the stream asks for. That is the right answer
	// and the reason is not a preference: rule 4 is a fact about the stream's own
	// declared bitmaps, while rule 3 is a judgement about how much of the work is
	// WASTED, and a stream that cannot be held in memory at all was never going
	// to reach the question of how much of it would be discarded. Told "you asked
	// for 24 MiB of bitmap", a producer knows what to change; told "you are
	// drawing 67 million pixels onto a 16 million pixel page", it fixes the
	// overhang and is refused again for the memory it never heard about.
	t.Run("the-memory-budget-is-charged-before-the-overdraw-rule", func(t *testing.T) {
		s := emptyRegionSegments(1, 16000000, 8192, 8179, 1)
		if len(s) != 67 {
			t.Fatalf("stream is %d bytes; want 67", len(s))
		}
		_, _, err := PageSize(s)
		if err == nil {
			t.Fatal("a 1x16000000 page under an 8192x8179 region is 24,375,296 packed bytes " +
				"and 67,002,368 pixels of overdraw, past rule 4 and rule 3 both, and must " +
				"be refused")
		}
		if !strings.Contains(err.Error(), "wants 16000000 page pixels and 24375296 bytes") {
			t.Errorf("error = %v;\nwant the refusal that names the BYTES OF BITMAP the stream "+
				"asks for. The page is charged into the memory budget before the overdraw rule "+
				"runs, on purpose: a stream that will not fit in memory is refused for that, "+
				"not for how much of it composite() would have thrown away. Moving rule 3 above "+
				"the page check leaves this stream refused -- and changes what its producer is "+
				"told to fix.", err)
		}
		if strings.Contains(err.Error(), "onto a page that can show") {
			t.Errorf("error = %v;\nthis is the OVERDRAW refusal. Rule 3 has been moved in front "+
				"of the page's own memory and pixel checks, which reverses the attribution "+
				"segment_decode.go documents as deliberate", err)
		}
	})

	// Regions summing to EXACTLY maxStreamBitmapBytes, which is the boundary of
	// the memory budget rather than a violation of it. One 1x16777216 region is
	// 16,777,216 packed bytes -- a one-pixel row still occupies a whole byte --
	// and 16,777,216 pixels, a quarter of the pixel budget, so the regions on
	// their own are exactly affordable. The 2048x2048 page under them adds
	// 524,288 bytes and is what actually breaks the budget.
	//
	// So the refusal must name the PAGE. Turning the region loop's "bmBytes >
	// maxStreamBitmapBytes" into ">=" leaves the stream refused and moves the
	// blame onto regions that are inside the budget: a producer told its regions
	// are unaffordable shrinks them, and the page it was never told about breaks
	// the budget again. The ">" is also what makes the constant mean "16,777,216
	// bytes are allowed" rather than "16,777,215 are".
	t.Run("regions-exactly-at-the-memory-budget-are-charged-to-the-page", func(t *testing.T) {
		s := emptyRegionSegments(2048, 2048, 1, 16777216, 1)
		_, _, err := PageSize(s)
		if err == nil {
			t.Fatal("16,777,216 bytes of region plus 524,288 bytes of page is past the memory " +
				"budget and must be refused")
		}
		if !strings.Contains(err.Error(), "and 17301504 bytes of bitmap") {
			t.Errorf("error = %v;\nwant the refusal charged to the PAGE, at the full 17,301,504 "+
				"bytes. The regions alone come to exactly the 16,777,216-byte budget, which is "+
				"inside it; charging them as though they were over it blames the affordable "+
				"half of the stream and makes the budget one byte smaller than it says", err)
		}
	})

	// An INTERMEDIATE generic region (T.88 segment type 36) is refused by name.
	// Deleting its case leaves it refused by the default arm instead, still as
	// ErrUnsupportedFeature, reported as "segment type 36" -- so every test that
	// asks only whether the stream is refused, and every test that asks only
	// whether the sentinel is right, stays green.
	//
	// The name is the whole value of that arm. Type 36 is not an unknown segment
	// type; it is a KNOWN one whose content this decoder could read perfectly
	// well and must not put on the page, because an intermediate region is
	// destined for an auxiliary buffer that a later refinement or text region
	// consumes. "Segment type 36" tells a caller byblos does not recognise
	// something; "intermediate generic region" tells them byblos recognised it
	// and declined, which is the difference between filing the page for a fuller
	// decoder and filing a bug against this one.
	t.Run("an-intermediate-generic-region-is-refused-by-name", func(t *testing.T) {
		pi := pageInfoSegmentData(8, 8)
		s := append(segmentHeader(0, segTypePageInformation, 1, len(pi)), pi...)
		d := regionSegmentData(8, 8)
		s = append(s, segmentHeader(1, segTypeIntermediateGenericRegion, 1, len(d))...)
		s = append(s, d...)

		_, _, err := PageSize(s)
		if err == nil {
			t.Fatal("an intermediate generic region is destined for an auxiliary buffer, not " +
				"the page, and must not be composed onto it")
		}
		if !errors.Is(err, ErrUnsupportedFeature) {
			t.Errorf("error = %v; want ErrUnsupportedFeature", err)
		}
		if !strings.Contains(err.Error(), "intermediate generic region") {
			t.Errorf("error = %v; want the refusal to name the segment KIND. Without its own "+
				"case the stream falls to the default arm and is reported as \"segment type "+
				"36\", which reads as a type byblos does not know -- and this is one it knows "+
				"and declines on purpose", err)
		}
	})

	// The long-form referred-to segment count of T.88 7.2.4: eleven bytes, a
	// segment header whose referred-to count byte selects the long form and
	// whose 29-bit count is 16,777,215.
	//
	// Deleting the check leaves the stream refused one gate later, by "header
	// runs past the end of the stream" -- true, and useless: what ran past the
	// end is a referred-to list of sixteen million entries in an eleven-byte
	// file, and the count is the field that is wrong. There is no overflow
	// hiding behind it on this platform -- n is masked to 29 bits and bounded by
	// len(s), so count*refSize cannot wrap a 64-bit int -- so this is
	// attribution and nothing else, which is exactly what this function is for.
	t.Run("a-long-form-referred-to-count-larger-than-the-stream", func(t *testing.T) {
		s, err := hex.DecodeString("0000000030e0ffffff0000")
		if err != nil {
			t.Fatalf("the case's own hex is malformed: %v", err)
		}
		if _, err = parseSegments(s); err == nil {
			t.Fatal("a segment referring to 16,777,215 other segments in an 11-byte stream " +
				"must be refused")
		}
		if !strings.Contains(err.Error(), "refers to 16777215 segments in a 11-byte stream") {
			t.Errorf("error = %v; want the refusal to name the referred-to COUNT and the size "+
				"of the stream it cannot fit in. Without it the header-end check reports that "+
				"the header ran past the end, which is true of every field at once and names "+
				"none of them", err)
		}
	})
}

// TestUnknownSegmentDataLengthIsAFeatureNotDamage pins the CLASSIFICATION of
// T.88 7.2.7's unknown-data-length form, which is the one thing about it that
// can be got wrong without any test noticing.
//
// parseSegments refuses 0xFFFFFFFF as ErrUnsupportedFeature. Delete that and the
// same stream is still refused -- the length check below it reads it as a
// declaration of 4,294,967,295 bytes and says "declares 4294967295 bytes of data
// but only N remain" -- so every test that asks only "is this refused?" stays
// green. TestDecodeRejectsMalformed's own unknown-data-length case is one of
// them.
//
// The sentinel is the whole point. A stream using the unknown-length form is
// INTACT and LEGAL: T.88 7.2.7 defines it, and it is decodable, just not from
// the headers alone -- finding where the segment ends means running the
// arithmetic decoder until it hits the terminating sequence, which this package
// declines to do and which a fuller decoder does. ErrUnsupportedJBIG2Feature's
// own doc says an archive acts on that distinction: an unsupported FEATURE is a
// page a later decoder recovers, and damage is a page that needs re-scanning
// from the original. Reclassifying this one sends a perfectly good page back to
// the scanner, and the original may no longer exist.
func TestUnknownSegmentDataLengthIsAFeatureNotDamage(t *testing.T) {
	base, err := EmbeddedStream(figureH6())
	if err != nil {
		t.Fatalf("EmbeddedStream() error = %v", err)
	}
	// The page information segment's data length field: the last four bytes of
	// the first 11-byte segment header.
	s := bytes.Clone(base)
	binary.BigEndian.PutUint32(s[7:11], 0xFFFFFFFF)

	_, err = parseSegments(s)
	if err == nil {
		t.Fatal("a segment declaring the unknown data length 0xFFFFFFFF must be refused; " +
			"finding where it ends means running the arithmetic decoder to look for a " +
			"terminator, and nothing here does that")
	}
	if !errors.Is(err, ErrUnsupportedFeature) {
		t.Errorf("error = %v; want ErrUnsupportedFeature. The unknown-data-length form is a "+
			"LEGAL T.88 7.2.7 encoding this package declines to implement, not a damaged "+
			"stream. Without the explicit check the length comparison below it reports "+
			"\"declares 4294967295 bytes of data but only N remain\", which reads as "+
			"corruption -- and an archive told a page is corrupt re-scans it rather than "+
			"queueing it for a decoder that can read it", err)
	}
	if !strings.Contains(err.Error(), "unknown data length") {
		t.Errorf("error = %v; want it to name the unknown data length, so the caller can tell "+
			"WHICH unimplemented feature it hit", err)
	}
	// And through the whole decoder, which is where a caller sees the sentinel.
	if _, err := DecodeEmbeddedStream(s); !errors.Is(err, ErrUnsupportedFeature) {
		t.Errorf("DecodeEmbeddedStream() error = %v; want ErrUnsupportedFeature", err)
	}
}

// TestTheBudgetChargesRegionsAfterOneThatDoesNotParse pins the `continue` in
// planStream's budget loop, and it is an ACCEPT/REJECT flip rather than a
// wording change.
//
// The loop deliberately passes over a region whose 17-byte information field
// does not parse, so that the decode loop reaches it in stream order and says
// what is actually wrong with it. What it must NOT do is stop: turning that
// `continue` into a `break` leaves every region AFTER the malformed one
// uncharged, and the whole stream is then planned from a prefix of itself.
//
// The stream below is 141 bytes: a 64x64 page under three region segments --
// a legal 64x64, then a 0x4 whose width field is invalid, then an 8192x8192.
// The three sum to 67,112,960 pixels, 4,096 past the work budget, and the tip
// refuses the stream from its headers having decoded nothing. Under `break` the
// 8192x8192 region is never costed at all: PageSize reports a 64x64 page and no
// error, and DecodeEmbeddedStream goes on to decode 2,112 pixels before the
// malformed region stops it.
//
// The pixels decoded stay bounded either way -- the decode loop aborts at the
// same region the budget loop stopped at -- so this is not the DoS coming back.
// What breaks is the property PageSize's doc states outright, that it makes
// every refusal DecodeEmbeddedStream makes before decoding and at the same cost.
// The PDF layer leans on exactly that: decodeJBIG2Placement calls PageSize FIRST
// so a stream that was never going to be accepted is refused before it is paid
// for. A budget that stops counting at the first bad header hands that gate a
// stream it has not actually costed, and the only thing still holding the line
// is that the decode loop happens to abort at the first error too.
func TestTheBudgetChargesRegionsAfterOneThatDoesNotParse(t *testing.T) {
	pi := pageInfoSegmentData(64, 64)
	s := append(segmentHeader(0, segTypePageInformation, 1, len(pi)), pi...)
	for i, d := range [][]byte{
		regionSegmentData(64, 64),     // legal, and page-covering
		regionSegmentData(0, 4),       // a width of 0: parseRegionInfo refuses it
		regionSegmentData(8192, 8192), // 67,108,864 pixels, the whole work budget
	} {
		s = append(s, segmentHeader(uint32(i+1), segTypeImmediateLosslessGenericRegion, 1, len(d))...)
		s = append(s, d...)
	}
	if len(s) != 141 {
		t.Fatalf("stream is %d bytes; want 141", len(s))
	}

	w, h, err := PageSize(s)
	if err == nil {
		t.Fatalf("PageSize() = %dx%d, err = nil; three regions summing to 67,112,960 pixels "+
			"are 4,096 past the work budget. The 8192x8192 region sits BEHIND one whose "+
			"header does not parse, and skipping a header that does not parse must not also "+
			"skip the cost of everything after it.", w, h)
	}
	if !strings.Contains(err.Error(), "the stream's 3 regions") {
		t.Errorf("error = %v;\nwant the budget refusal, charged against all THREE regions. "+
			"The 0x4 region is passed over so the decode loop can report what is wrong with "+
			"it; the regions after it still have to be paid for.", err)
	}
	// Nothing may be decoded on the way to that refusal.
	before := DecodedPixels()
	if _, err := DecodeEmbeddedStream(s); err == nil {
		t.Fatal("DecodeEmbeddedStream() accepted the stream")
	}
	if spent := DecodedPixels() - before; spent != 0 {
		t.Errorf("%d pixels were decoded before the refusal; the budget is evaluated from "+
			"segment headers alone and must cost nothing", spent)
	}
}

// TestDecodeRefusesAStreamWithNoRegionSegment pins the guard on an EMPTY page.
//
// A stream carrying a page information segment and no region segment at all
// satisfies every rule in planStream trivially -- zero regions sum to zero
// pixels and zero bytes, and zero over anything is inside the overdraw ratio --
// so "len(regions) == 0" is the only thing that refuses it. Deleting it does not
// lose an error message: DecodeEmbeddedStream composes nothing onto a fresh
// bitmap and hands back a clean, all-background 8x8 page from a 30-BYTE stream
// that contains no image data whatsoever.
//
// That is the failure this package's doc singles out as worse than an error: a
// raster that is wrong without being detectably wrong. A caller cannot tell "the
// scanned page really was blank" from "the segment carrying the page was
// truncated away, or stripped by whatever handled the PDF before me", and the
// second is a page that needs re-scanning rather than filing.
//
// The stream is built rather than quoted because every byte of it is a legal
// page information segment; there is nothing malformed to preserve.
func TestDecodeRefusesAStreamWithNoRegionSegment(t *testing.T) {
	pi := pageInfoSegmentData(8, 8)
	s := append(segmentHeader(0, segTypePageInformation, 1, len(pi)), pi...)
	if len(s) != 30 {
		t.Fatalf("stream is %d bytes; want 30", len(s))
	}

	w, h, err := PageSize(s)
	if err == nil {
		t.Fatalf("PageSize() = %dx%d, err = nil; a stream with no region segment declares a "+
			"page and carries nothing to put on it", w, h)
	}
	if !strings.Contains(err.Error(), "no immediate generic region segment") {
		t.Errorf("error = %v; want the refusal to name the missing region segment", err)
	}
	if errors.Is(err, ErrUnsupportedFeature) {
		t.Errorf("error = %v; a stream with no region segment is not a coding feature this "+
			"package has not implemented -- there is nothing in it to implement", err)
	}

	// And through the decoder, which is where the blank page would be handed
	// back. Asserting on PageSize alone would leave that path untested, and it is
	// the path a caller actually takes.
	got, err := DecodeEmbeddedStream(s)
	if err == nil {
		t.Fatalf("DecodeEmbeddedStream() returned a %dx%d bitmap from %d bytes carrying no "+
			"region segment. Every pixel of it is background this decoder invented; nothing "+
			"in the stream said the page was blank.", got.W, got.H, len(s))
	}
}

// TestDecodeRefusesASecondPageInformationSegment pins "sawPageInfo", and the
// reason it matters is the SIZE, not the duplication.
//
// planStream assigns pageW and pageH from each page information segment it
// meets. Delete the guard and the LAST one wins, silently: the 97-byte stream
// below declares an 8x8 page, then a 4096x4096 page, then a single 8x8 region --
// and it decodes, as a 4096x4096 page with an 8x8 region in the corner of it.
//
// That number is not decoration. decodeJBIG2Placement checks the decoded page
// against the image dictionary's /Width and /Height precisely so that a stream
// which is not the one the dictionary describes is refused; with the guard gone,
// an attacker who controls the JBIG2 bytes chooses which of two declared sizes
// that check sees, by appending nineteen bytes. The page the check passes on and
// the page the content belongs to are then different pages, and the size check
// is satisfied by a field nothing else in the stream agrees with.
//
// The two sizes are deliberately far apart so a silent accept cannot be mistaken
// for a rounding difference, and the region belongs to NEITHER page in
// particular -- an 8x8 region is inside both -- so nothing downstream fails and
// nothing else in the suite notices.
func TestDecodeRefusesASecondPageInformationSegment(t *testing.T) {
	first := pageInfoSegmentData(8, 8)
	second := pageInfoSegmentData(4096, 4096)
	s := append(segmentHeader(0, segTypePageInformation, 1, len(first)), first...)
	s = append(s, segmentHeader(1, segTypePageInformation, 1, len(second))...)
	s = append(s, second...)
	s = append(s, segmentHeader(2, segTypeImmediateLosslessGenericRegion, 1, 26)...)
	s = append(s, regionSegmentData(8, 8)...)
	if len(s) != 97 {
		t.Fatalf("stream is %d bytes; want 97", len(s))
	}

	w, h, err := PageSize(s)
	if err == nil {
		t.Fatalf("PageSize() = %dx%d, err = nil; this stream declares an 8x8 page AND a "+
			"4096x4096 page. Reporting either one is reporting a size the stream does not "+
			"agree on, and %dx%d is what decodeJBIG2Placement compares the image "+
			"dictionary's /Width and /Height against.", w, h, w, h)
	}
	if !strings.Contains(err.Error(), "more than one page information segment") {
		t.Errorf("error = %v; want the refusal to name the duplicate page information "+
			"segment, which is the field that is wrong", err)
	}
	if _, err := DecodeEmbeddedStream(s); err == nil {
		t.Fatal("DecodeEmbeddedStream() accepted a stream with two page information segments")
	}
}

// clipPattern builds a w x h bitmap whose pixel at (x, y) depends on BOTH
// coordinates and on neither of them alone, so a region clipped at the wrong
// offset, transposed, or truncated by a row does not accidentally match.
func clipPattern(w, h int) *Bitmap {
	b := NewBitmap(w, h)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			if (x*31+y*17)%7 < 3 {
				b.Set(x, y, 1)
			}
		}
	}
	return b
}

// TestDecodeAdmitsLegalClipCases is the other jaw of the overdraw rule: the one
// that says a legal document must OPEN.
//
// T.88 6.2.4 makes a region that overhangs its page LEGAL: the part outside the
// page is dropped and the rest is composed. Rule 3 charges those dropped pixels
// as waste and refuses the stream past 4x the page plus overdrawFloorPixels, and
// when that floor was 1,024 it was fitted to exactly one shape -- the 54x44-
// region-on-a-20x20-page stream TestDecodeClipsRegionToPage/clipped decodes,
// which needs 2,376 - 4*400 = 776. Every other overhang of the same kind was
// refused, and a false refusal of a legal document is as much a defect as a
// missed attack. Measured, each of these costs under 2 ms:
//
//	page 20x20,   region 54x44   (2,376 px,  5.9x the page)  776 of floor
//	page 20x20,   region 60x50   (3,000 px,  7.5x the page)  1,400 of floor
//	page 64x64,   region 256x256 (65,536 px, 16x the page)   49,152 of floor
//	page 100x100, region 300x300 (90,000 px, 9x the page)    50,000 of floor
//
// The floor is now 65,536, derived from an absolute cost rather than from any of
// these shapes (see segment_decode.go), and it admits all four with a third to
// spare over the most demanding of them.
//
// So this asserts that they DECODE, and asserts what they decode TO -- the
// region clipped to the page, pixel for pixel and padding included. Asserting
// only that no error came back would be satisfied by a decoder that returned a
// blank page, and the clip is a bounds check: Bitmap.Set does not range-check,
// so getting it wrong writes past Pix rather than drawing in the wrong place.
func TestDecodeAdmitsLegalClipCases(t *testing.T) {
	for _, c := range []struct {
		name                   string
		pageW, pageH           int
		regionW, regionH       int
		x0, y0                 int
		regionPixels, overhang string // stated per case, not computed here
	}{
		// The shape the old floor was fitted to. It was the only one that
		// decoded; it is here so the table pins the floor from below across its
		// whole range, and so a regression that refuses everything is not
		// mistaken for a fix.
		{"20x20-under-54x44", 20, 20, 54, 44, 0, 0, "2,376", "5.9x"},
		// The same shape one step larger. 3,000 > 4*400 + 1,024 = 2,624.
		{"20x20-under-60x50", 20, 20, 60, 50, 0, 0, "3,000", "7.5x"},
		// A thumbnail page under a full-size region. 65,536 > 4*4,096 + 1,024.
		{"64x64-under-256x256", 64, 64, 256, 256, 0, 0, "65,536", "16x"},
		// The most demanding of the four: 90,000 - 4*10,000 = 50,000 pixels of
		// floor, so halving overdrawFloorPixels to 32,768 refuses it again.
		{"100x100-under-300x300", 100, 100, 300, 300, 0, 0, "90,000", "9x"},
		// An overhang produced by POSITION rather than by size: a 54x44 region
		// at (40,50) on a 64x64 page shows only its top-left 24x14 corner. This
		// one was inside rule 3 even at the old floor (2,376 < 4*4,096 + 1,024),
		// and it is here because the offset and the clip interact -- a decoder
		// that clipped against the region's own extent instead of the page's
		// would pass every case above and fail this one.
		{"64x64-under-54x44-at-40-50", 64, 64, 54, 44, 40, 50, "2,376", "0.58x"},
	} {
		t.Run(c.name, func(t *testing.T) {
			region := clipPattern(c.regionW, c.regionH)
			pi := pageInfoSegmentData(c.pageW, c.pageH)
			s := append(segmentHeader(0, segTypePageInformation, 1, len(pi)), pi...)
			d := genericRegionSegmentData(region, c.x0, c.y0, true)
			s = append(s, segmentHeader(1, segTypeImmediateLosslessGenericRegion, 1, len(d))...)
			s = append(s, d...)

			got, err := DecodeEmbeddedStream(s)
			if err != nil {
				t.Fatalf("a %dx%d region at (%d,%d) on a %dx%d page is %s pixels, %s the page, "+
					"and T.88 6.2.4 clips it rather than refusing it -- this is a legal "+
					"document byblos will not open: %v",
					c.regionW, c.regionH, c.x0, c.y0, c.pageW, c.pageH,
					c.regionPixels, c.overhang, err)
			}

			// The page is default-pixel 0 and the region combines with OR, so
			// every page pixel the region covers is the region's and every
			// other is 0. Built from the source bitmap, not from composite().
			want := NewBitmap(c.pageW, c.pageH)
			for y := 0; y < c.regionH; y++ {
				for x := 0; x < c.regionW; x++ {
					px, py := x+c.x0, y+c.y0
					if px < c.pageW && py < c.pageH && region.Get(x, y) != 0 {
						want.Set(px, py, 1)
					}
				}
			}
			assertBitmapsIdentical(t, c.name, got, want)
			if !bytes.Equal(got.Pix, want.Pix) {
				t.Errorf("%s: pixels match but the packed bytes do not; the clip left dirty "+
					"padding in the last byte of a row", c.name)
			}
		})
	}
}

// TestSegmentCountIsBounded is rule 5 from ABOVE, and what it pins is a cost
// that rules 1-4 are structurally unable to see.
//
// Those four all count PACKED BITMAP BYTES. A one-pixel region is one byte of
// bitmap and about 190 bytes of everything else -- a 32-byte descriptor in
// parseSegments's slice and a second one in planStream's, a 48-byte entry in the
// decode slice, a Bitmap header, and the slice growth behind each of those -- so
// on a stream of one-pixel regions the quantity being budgeted is a thousandth
// of the quantity being spent. Measured on this code with the cap removed: a
// 41,222,211-byte stream of 1,114,113 one-pixel regions was refused by rule 3,
// correctly and from the headers with nothing decoded, having allocated 239.8
// MiB to reach that verdict -- against a 16 MiB bitmap budget. One region fewer
// and it was ACCEPTED, at 393.9 MiB.
//
// The refusal has to come from PARSING, not from planStream, which is what makes
// this different from every other budget test here: by the time planStream can
// look at a stream, the slice this rule bounds has already been built. So the
// assertion is on ALLOCATION as much as on the error. The stream below is four
// times the cap; refusing it must cost the cap, not the stream.
func TestSegmentCountIsBounded(t *testing.T) {
	// 262,144 one-pixel regions, four times maxStreamSegments, on a page that
	// is inside every other rule: 1024x65536 is exactly MaxPagePixels, the
	// regions sum to 262,144 pixels, and the packed bytes come to 8,650,752
	// against the 16 MiB budget. Rule 5 is the only thing that refuses it.
	const n = 4 * maxStreamSegments
	s := emptyRegionSegments(1024, 65536, 1, 1, n)
	if want := 30 + n*37; len(s) != want {
		t.Fatalf("stream is %d bytes; want %d", len(s), want)
	}

	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	start := time.Now()
	w, h, err := PageSize(s)
	elapsed := time.Since(start)
	runtime.ReadMemStats(&after)
	grew := after.TotalAlloc - before.TotalAlloc
	t.Logf("%d segments in %d bytes: err = %v, elapsed = %v, TotalAlloc grew by %.1f MiB",
		n+1, len(s), err, elapsed, float64(grew)/(1<<20))

	if err == nil {
		t.Fatalf("PageSize() = %dx%d, err = nil; %d segments is %dx the cap. Every one of "+
			"rules 1-4 is satisfied here -- the page is exactly MaxPagePixels, the regions "+
			"sum to %d pixels and 8,650,752 packed bytes -- because none of them can see "+
			"per-segment overhead at all.", w, h, n+1, (n+1)/maxStreamSegments, n)
	}
	if !strings.Contains(err.Error(), "more than 65536 segments") {
		t.Errorf("error = %v; want the refusal to name the segment cap. Any other refusal "+
			"means the stream got past parsing and this rule is not the one that stopped it", err)
	}
	if errors.Is(err, ErrUnsupportedFeature) {
		t.Errorf("error = %v; a stream with more segments than any encoder writes is a "+
			"resource refusal, not a coding feature byblos has not implemented", err)
	}

	// 10.6 MiB is what parsing exactly maxStreamSegments segments allocates,
	// measured. 24 MiB leaves headroom over that and still sits far below the
	// ~50 MiB the same stream costs with the cap removed, so this separates the
	// two without being pinned to an allocator's growth factor.
	const bound = 24 << 20
	if grew > bound {
		t.Errorf("refusing %d segments allocated %.1f MiB; the limit is %d MiB. The cap has "+
			"to be charged INSIDE parseSegments, where the slice it bounds is built -- "+
			"charged anywhere later, in planStream or in a caller, and the allocation this "+
			"rule exists to bound has already happened. (What this cannot distinguish, and "+
			"an earlier version of this message claimed it could, is the check sitting "+
			"before rather than after the one append: by the cap the slice has spare "+
			"capacity and both placements cost the same to the byte.)",
			n+1, float64(grew)/(1<<20), bound>>20)
	}
}

// TestSegmentCountBoundAdmitsLegitimateStreams is rule 5 from BELOW, and it is
// the half that stops the bound drifting closed.
//
// A resource bound with no legitimacy pin is free to be tightened to anything at
// all, and this package has already shipped that mistake once: byb-riy round 3's
// overdraw floor was fitted to the single clipped fixture in these tests, and it
// then refused every other legal T.88 6.2.4 clip shape and every 600-dpi scan.
// Nothing failed when that happened.
//
// Two pins, because they fail to different things:
//
//   - a REAL striped page, decoded for real and compared bit for bit. This is
//     the shape the bound is derived from -- T.88 7.4.8.2 striping, one
//     immediate generic region per stripe -- at the finest striping the format
//     allows, one row per stripe. It is a genuine round trip through this
//     package's own encoder, so it also fails if a multi-region stream stops
//     composing correctly for some reason that has nothing to do with counting.
//   - the cap's own boundary, EXACTLY maxStreamSegments segments, through
//     PageSize. Any reduction of the constant at all fails here, which is what
//     the first pin cannot do: 301 segments would still be admitted by a cap of
//     1,024.
func TestSegmentCountBoundAdmitsLegitimateStreams(t *testing.T) {
	t.Run("a-page-striped-one-row-per-region", func(t *testing.T) {
		// 200x300, striped into 300 single-row regions. The content is a
		// diagonal plus a border so that a stripe landing at the wrong y is a
		// pixel difference rather than a coincidence.
		const w, h = 200, 300
		page := NewBitmap(w, h)
		for y := 0; y < h; y++ {
			page.Set((y*7)%w, y, 1)
			page.Set(0, y, 1)
			page.Set(w-1, y, 1)
		}

		pi := pageInfoSegmentData(w, h)
		s := append(segmentHeader(0, segTypePageInformation, 1, len(pi)), pi...)
		for y := 0; y < h; y++ {
			stripe := NewBitmap(w, 1)
			copy(stripe.Pix, page.Pix[y*page.Stride:(y+1)*page.Stride])
			d := genericRegionSegmentData(stripe, 0, y, true)
			s = append(s, segmentHeader(uint32(y+1), segTypeImmediateLosslessGenericRegion, 1, len(d))...)
			s = append(s, d...)
		}
		t.Logf("%dx%d page striped one row per region: %d segments, %d bytes", w, h, h+1, len(s))

		got, err := DecodeEmbeddedStream(s)
		if err != nil {
			t.Fatalf("a %dx%d page striped one row per region -- %d segments, the finest "+
				"striping T.88 7.4.8.2 permits and the shape rule 5's bound is derived from "+
				"-- was refused: %v", w, h, h+1, err)
		}
		if got.W != w || got.H != h {
			t.Fatalf("decoded %dx%d; want %dx%d", got.W, got.H, w, h)
		}
		if !bytes.Equal(got.Pix, page.Pix) {
			t.Error("the striped page did not round-trip bit-identically")
		}
	})

	t.Run("exactly-the-segment-cap", func(t *testing.T) {
		// One page information segment plus maxStreamSegments-1 regions is
		// exactly maxStreamSegments segments. The page is 1024x65536, which is
		// the shape the cap is derived from: MaxPagePixels over the narrowest
		// width a scanned sheet can have, so its rows are what the cap counts.
		s := emptyRegionSegments(1024, 65536, 1, 1, maxStreamSegments-1)
		w, h, err := PageSize(s)
		if err != nil {
			t.Fatalf("a stream of exactly %d segments -- the cap itself -- was refused: %v. "+
				"A bound whose own boundary is refused is a bound one lower than it says, "+
				"and the next person to tighten it has nothing telling them they have gone "+
				"too far.", maxStreamSegments, err)
		}
		if w != 1024 || h != 65536 {
			t.Errorf("PageSize() = %dx%d; want 1024x65536", w, h)
		}
	})

	t.Run("byblos-own-encoder-output", func(t *testing.T) {
		// Two segments, and it stays two: the from-below pin that matters most
		// is that the write side's own output is nowhere near the bound.
		s, err := EmbeddedStream(figureH6())
		if err != nil {
			t.Fatalf("EmbeddedStream() error = %v", err)
		}
		segs, err := parseSegments(s)
		if err != nil {
			t.Fatalf("parseSegments() on this package's own output: %v", err)
		}
		if len(segs) != 2 {
			t.Errorf("EmbeddedStream wrote %d segments; want 2. The doc comment on "+
				"DecodeJBIG2Generic states that count as the headroom under the %d-segment "+
				"cap", len(segs), maxStreamSegments)
		}
	})
}

// TestDecodedPixelsCountsTheDecodingItReports is the pin on the MEASURING
// INSTRUMENT, and it has to exist before any test that uses the instrument
// means anything.
//
// Every other assertion on DecodedPixels() in this repository is an UPPER
// bound: jbig2_cost_test.go asserts a decode cost is at most 4x the raster it
// returned, and the three refusal paths there plus
// TestTheBudgetChargesRegionsAfterOneThatDoesNotParse assert the cost is
// exactly zero. A counter that always reads zero satisfies every one of them.
// So the whole budget suite -- the DoS bound, the overdraw rule, the order of
// the checks in decodeJBIG2Placement -- stays green against a decoder that has
// simply stopped counting, and nothing in the tree can tell the difference
// between "the budgets work" and "the instrument is broken".
//
// Verified: changing the accumulator in generic_decode.go from
// decodedPixels.Add(work) to decodedPixels.Add(0*work) builds clean and passes
// the entire suite. That is not a test without kill power, it is the
// instrument without kill power, and it voids a class of tests at once.
//
// The assertion is EXACT rather than a lower bound, because the count is
// deterministic and nothing weaker pins the instrument's units. With TPGDON
// off there is one MQ decision per pixel and nothing else (T.88 6.2.5.7), so a
// w x h region costs exactly w*h; a lower bound of 1 would pass against a
// counter that reported bytes, or rows, or one per region.
//
// Two shapes, because they fail to different things. One region pins the unit.
// Two regions on one page pin that the accumulation is per REGION and summed:
// the counter is a local accumulated inside decodeGenericRegion and added once
// on the way out, so a decoder that dropped the deferred add on all but the
// last region reads plausibly and is wrong.
func TestDecodedPixelsCountsTheDecodingItReports(t *testing.T) {
	// Not a multiple of 8 in either axis, so row padding is live and a counter
	// that had drifted to packed BYTES could not coincide with the pixel count.
	const w, h = 61, 37
	content := func(w, h, seed int) *Bitmap {
		b := NewBitmap(w, h)
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				if (x*7+y*13+seed)%11 < 4 {
					b.Set(x, y, 1)
				}
			}
		}
		return b
	}

	t.Run("one-region", func(t *testing.T) {
		page := content(w, h, 0)
		pi := pageInfoSegmentData(w, h)
		s := append(segmentHeader(0, segTypePageInformation, 1, len(pi)), pi...)
		// TPGDON off: exactly one MQ decision per pixel, no per-row SLTP bit,
		// so the expected count is w*h and not w*h + h.
		d := genericRegionSegmentData(page, 0, 0, false)
		s = append(s, segmentHeader(1, segTypeImmediateLosslessGenericRegion, 1, len(d))...)
		s = append(s, d...)

		before := DecodedPixels()
		got, err := DecodeEmbeddedStream(s)
		spent := DecodedPixels() - before
		if err != nil {
			t.Fatalf("DecodeEmbeddedStream() error = %v", err)
		}
		if !bytes.Equal(got.Pix, page.Pix) {
			t.Fatalf("the page did not round-trip, so the count below would be measuring "+
				"something other than a correct decode of %dx%d", w, h)
		}
		if want := int64(w * h); spent != want {
			t.Errorf("decoding a %dx%d region with TPGDON off reported %d decoded pixels; "+
				"want exactly %d. DecodedPixels is the instrument every decode-cost "+
				"assertion in this repository is measured with, and every one of those "+
				"assertions is an upper bound or a check for zero -- so an instrument "+
				"that under-reports, and a counter stuck at 0 in particular, makes the "+
				"whole budget suite pass while measuring nothing.", w, h, spent, want)
		}
	})

	t.Run("tpgdon-charges-the-per-row-sltp-bit", func(t *testing.T) {
		// TPGDON adds one MQ decision per ROW on top of the per-pixel ones
		// (T.88 6.2.5.7 step 3b), and the accumulator counts it separately.
		// No row here equals the row above -- row y carries exactly the pixels
		// where (x*7+y*13)%11 < 4, and y appears in that residue -- so the
		// encoder never sets LTP, every row is coded in full, and the exact
		// count is w*h + h rather than something between h and w*h + h.
		page := content(w, h, 0)
		for y := 1; y < h; y++ {
			if bytes.Equal(page.Pix[y*page.Stride:(y+1)*page.Stride],
				page.Pix[(y-1)*page.Stride:y*page.Stride]) {
				t.Fatalf("row %d repeats row %d, so this fixture has typical-prediction "+
					"rows and w*h+h is not the count to expect", y, y-1)
			}
		}

		pi := pageInfoSegmentData(w, h)
		s := append(segmentHeader(0, segTypePageInformation, 1, len(pi)), pi...)
		d := genericRegionSegmentData(page, 0, 0, true)
		s = append(s, segmentHeader(1, segTypeImmediateLosslessGenericRegion, 1, len(d))...)
		s = append(s, d...)

		before := DecodedPixels()
		got, err := DecodeEmbeddedStream(s)
		spent := DecodedPixels() - before
		if err != nil {
			t.Fatalf("DecodeEmbeddedStream() error = %v", err)
		}
		if !bytes.Equal(got.Pix, page.Pix) {
			t.Fatalf("the page did not round-trip under TPGDON")
		}
		if want := int64(w*h + h); spent != want {
			t.Errorf("decoding a %dx%d region with TPGDON on reported %d decoded pixels; "+
				"want exactly %d -- %d coded pixels plus one SLTP decision for each of "+
				"the %d rows. The SLTP bit is real MQ work and a stream of all-typical "+
				"rows is nothing BUT SLTP bits, so an instrument that does not charge it "+
				"reports zero for the cheapest way to ask for a whole page.",
				w, h, spent, want, w*h, h)
		}
	})

	t.Run("two-regions-sum", func(t *testing.T) {
		top, bottom := content(w, h, 0), content(w, h, 5)
		pi := pageInfoSegmentData(w, 2*h)
		s := append(segmentHeader(0, segTypePageInformation, 1, len(pi)), pi...)
		for i, r := range []struct {
			b *Bitmap
			y int
		}{{top, 0}, {bottom, h}} {
			d := genericRegionSegmentData(r.b, 0, r.y, false)
			s = append(s, segmentHeader(uint32(i+1), segTypeImmediateLosslessGenericRegion, 1, len(d))...)
			s = append(s, d...)
		}

		before := DecodedPixels()
		got, err := DecodeEmbeddedStream(s)
		spent := DecodedPixels() - before
		if err != nil {
			t.Fatalf("DecodeEmbeddedStream() error = %v", err)
		}
		if got.W != w || got.H != 2*h {
			t.Fatalf("decoded %dx%d; want %dx%d", got.W, got.H, w, 2*h)
		}
		if want := int64(2 * w * h); spent != want {
			t.Errorf("decoding two %dx%d regions onto one page reported %d decoded pixels; "+
				"want exactly %d. Each region accumulates into a local and adds it once on "+
				"the way out, so a total that is right for one region and short for two is "+
				"an instrument that silently under-charges every multi-region stream -- "+
				"which is every striped scan, and every stream the budget tests use.",
				w, h, spent, want)
		}
	})
}

// TestSegmentCountBoundRefusesExactlyOnePastTheCap pins rule 5 at its BOUNDARY
// from above, which is the direction the two tests around it leave open.
//
// TestSegmentCountIsBounded refuses a stream of 262,145 segments -- FOUR TIMES
// the cap -- and TestSegmentCountBoundAdmitsLegitimateStreams accepts a stream
// of exactly 65,536. Between them they pin that the constant is somewhere in
// [65,536, 262,144], and nothing narrows it. Change the check in parseSegments
// from "len(out) == maxStreamSegments" to "== maxStreamSegments+1" and the whole
// suite passes: 65,537 segments in 2,424,862 bytes is then ACCEPTED, decoding
// 65,536 pixels in 79.8 ms and allocating 29.7 MiB, while segment_decode.go's
// own comment states that one segment past the cap is refused.
//
// A resource bound is only as good as its boundary. Both sides of it are
// asserted here, one after the other on streams that differ by a single 37-byte
// region segment, so the two cannot drift apart the way they have.
func TestSegmentCountBoundRefusesExactlyOnePastTheCap(t *testing.T) {
	// The page is the shape the cap is derived from: 1024x65536 is exactly
	// MaxPagePixels over the narrowest width a scanned sheet can have, so its
	// rows are what the cap counts. The regions are 1x1, so rules 1-4 are
	// satisfied by an enormous margin at both counts and rule 5 is the only
	// thing that can distinguish them.
	const pageW, pageH = 1024, 65536

	t.Run("exactly-the-cap-is-accepted", func(t *testing.T) {
		// maxStreamSegments-1 regions plus the page information segment.
		s := emptyRegionSegments(pageW, pageH, 1, 1, maxStreamSegments-1)
		w, h, err := PageSize(s)
		if err != nil {
			t.Fatalf("a stream of exactly %d segments -- the cap itself -- was refused: %v. "+
				"This is the control for the case below; without it, a cap of zero would "+
				"pass that one.", maxStreamSegments, err)
		}
		if w != pageW || h != pageH {
			t.Errorf("PageSize() = %dx%d; want %dx%d", w, h, pageW, pageH)
		}
	})

	t.Run("one-past-the-cap-is-refused", func(t *testing.T) {
		// One more region segment than the case above: maxStreamSegments+1
		// segments in total, and 37 bytes more of stream.
		s := emptyRegionSegments(pageW, pageH, 1, 1, maxStreamSegments)
		if want := 30 + maxStreamSegments*37; len(s) != want {
			t.Fatalf("stream is %d bytes; want %d", len(s), want)
		}

		var before, after runtime.MemStats
		runtime.GC()
		runtime.ReadMemStats(&before)
		start := time.Now()
		w, h, err := PageSize(s)
		elapsed := time.Since(start)
		runtime.ReadMemStats(&after)
		grew := after.TotalAlloc - before.TotalAlloc
		t.Logf("%d segments in %d bytes: err = %v, elapsed = %v, TotalAlloc grew by %.1f MiB",
			maxStreamSegments+1, len(s), err, elapsed, float64(grew)/(1<<20))

		if err == nil {
			t.Fatalf("PageSize() = %dx%d, err = nil; %d segments is one PAST the %d-segment "+
				"cap and must be refused. A bound that admits its own limit plus one is a "+
				"bound one higher than it says, and every measurement written against it -- "+
				"including segment_decode.go's own \"ONE SEGMENT PAST THE CAP is refused\" -- "+
				"is then describing a stream this code accepts.",
				w, h, maxStreamSegments+1, maxStreamSegments)
		}
		if !strings.Contains(err.Error(), "more than 65536 segments") {
			t.Errorf("error = %v; want the refusal to name the segment cap. Any other refusal "+
				"means something else stopped the stream and this boundary is still unpinned", err)
		}

		// The refusal must cost the CAP, not the stream: the whole point of
		// charging rule 5 inside parseSegments is that the parsed-segment slice
		// stops growing there. Measured at this boundary: 10.6 MiB, the same as
		// parsing exactly the cap, and the same as refusing a stream four times
		// the cap. Deleting the check entirely takes the four-times stream to
		// 50.4 MiB, so the bound below separates the two with room to spare.
		const bound = 24 << 20
		if grew > bound {
			t.Errorf("refusing %d segments allocated %.1f MiB; the limit is %d MiB. Rule 5 has "+
				"to be charged INSIDE parseSegments, where the slice it bounds is built; "+
				"anywhere later and the allocation it exists to bound has already happened.",
				maxStreamSegments+1, float64(grew)/(1<<20), bound>>20)
		}
	})
}
