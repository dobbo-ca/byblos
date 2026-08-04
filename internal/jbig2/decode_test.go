package jbig2

import (
	"bytes"
	"encoding/binary"
	"errors"
	"os"
	"testing"
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
