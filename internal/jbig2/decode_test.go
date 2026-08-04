package jbig2

import (
	"bytes"
	"encoding/binary"
	"errors"
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
// maxRegionPixels caps each region on its own and the resolved page on its own.
// Neither caps the total: every region is decoded and RETAINED until the page is
// composed, and the page-size check that refuses the lot does not run until the
// last one has been decoded. Eight regions each at the per-region cap is 326
// bytes of input; before the fix that took 38.7 seconds and grew TotalAlloc by
// 512 MiB before returning "jbig2: page is 65536x16384; the limit is 536870912
// pixels" -- an amplification of about 1.6 million to one, LINEAR in segment
// count at 37 bytes per region, so 40 KB asks for 64 GiB and hours of CPU.
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
	// 32768 x 16384 is exactly maxRegionPixels, so every one of these passes the
	// per-region cap. Only their sum is absurd.
	s := emptyRegionSegments(65536, 16384, 32768, 16384, 8)
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
			got.W, got.H, maxRegionPixels)
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
	// 512 MiB was measured before the fix. 4 MiB is far above what parsing 326
	// bytes of headers costs and far below one region's 64 MiB packed bitmap, so
	// nothing can pass this having decoded even one region.
	const bound = 4 << 20
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
