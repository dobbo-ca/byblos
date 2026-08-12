package jbig2

// A TEST-ONLY symbol-mode encoder, and the reason it exists rather than more
// fixtures.
//
// The jbig2enc fixture proves the decoder reads what one encoder writes. What it
// cannot do is reach the parameters that encoder never uses, and jbig2enc uses
// exactly one setting of each: REFCORNER is always BOTTOMLEFT, TRANSPOSED is
// always 0, SBSTRIPS is always 1, SBDSOFFSET is always 0, every symbol goes in
// one height class, and every symbol is exported. That leaves the whole of T.88
// 6.4.5's coordinate machinery -- four reference corners, the transposed axis
// swap, multi-row strips with their own T deltas -- decoded by code no fixture
// touches. A mistake in any of it does not fail; it shifts glyphs, and a shifted
// glyph is still a glyph.
//
// So this builds streams to order. Being test-only it is allowed to be narrow:
// template 0 with nominal AT, no refinement, no Huffman, which is the subset the
// decoder accepts. It is NOT a second decoder -- it never reads a stream -- so it
// cannot drift into agreeing with a decoder bug by construction. And
// TestSyntheticStreamsAreWhatJBIG2DecSaysTheyAre asks jbig2dec whether the
// streams it writes mean what this file thinks they mean, which is what stops a
// shared misreading of the spec from passing as a round trip.

import (
	"bytes"
	"encoding/binary"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// intRanges is intLengths from the other side: the value at which each prefix
// length starts. encodeInt picks the first row the value fits in.
var intRanges = [6]struct{ prefixBits, n, offset int }{
	{1, 2, 0}, {2, 4, 4}, {3, 6, 20}, {4, 8, 84}, {5, 12, 340}, {5, 32, 4436},
}

// intEnc is the inverse of decodeInt: one arithmetic integer written with the
// PREV register updated exactly as the decoder updates it.
type intEnc struct {
	e  *encoder
	cx contexts
}

func (w intEnc) bit(prev *int, b int) {
	w.e.encode(w.cx, *prev, b)
	if *prev < 256 {
		*prev = *prev<<1 | b
	} else {
		*prev = ((*prev<<1 | b) & 511) | 256
	}
}

// write encodes v. oob writes the out-of-band value, which is the negative zero
// of T.88 A.2 step 4 and is what ends a height class or a strip.
func (w intEnc) write(v int, oob bool) {
	prev := 1
	sign, mag := 0, v
	if oob {
		sign, mag = 1, 0
	} else if v < 0 {
		sign, mag = 1, -v
	}
	w.bit(&prev, sign)

	row := intRanges[len(intRanges)-1]
	sel := len(intRanges) - 1
	for i, r := range intRanges[:len(intRanges)-1] {
		if mag < r.offset+1<<r.n {
			row, sel = r, i
			break
		}
	}
	// The prefix: sel ones followed by a zero, except the last row, which is
	// five ones and no terminator.
	for range sel {
		w.bit(&prev, 1)
	}
	if sel != len(intRanges)-1 {
		w.bit(&prev, 0)
	}
	for i := row.n - 1; i >= 0; i-- {
		w.bit(&prev, (mag-row.offset)>>uint(i)&1)
	}
}

// writeIAID is the inverse of decodeIAID.
func writeIAID(e *encoder, cx contexts, v, codeLen int) {
	prev := 1
	for i := codeLen - 1; i >= 0; i-- {
		b := v >> uint(i) & 1
		e.encode(cx, prev, b)
		prev = prev<<1 | b
	}
}

// encodeGenericInto codes b into an existing encoder and context array, which is
// what a symbol dictionary does with every symbol it holds (T.88 6.5.8.1).
// Template 0, nominal AT, TPGDON off -- the parameters a symbol dictionary uses
// and the ones decodeGenericRegionGeneral is handed for them.
func encodeGenericInto(e *encoder, cx contexts, b *Bitmap) {
	for y := 0; y < b.H; y++ {
		for x := 0; x < b.W; x++ {
			e.encode(cx, contextTemplate0(b, x, y), b.Get(x, y))
		}
	}
}

// buildSymbolDict returns a symbol dictionary segment BODY holding syms, which
// must be sorted by non-decreasing height. Every symbol is exported.
func buildSymbolDict(syms []*Bitmap) []byte {
	e := newEncoder()
	gb := make(contexts, 1<<16)
	iadh := intEnc{e, newIntContexts()}
	iadw := intEnc{e, newIntContexts()}
	iaex := intEnc{e, newIntContexts()}

	height := 0
	for i := 0; i < len(syms); {
		j := i
		for j < len(syms) && syms[j].H == syms[i].H {
			j++
		}
		iadh.write(syms[i].H-height, false)
		height = syms[i].H
		width := 0
		for _, s := range syms[i:j] {
			iadw.write(s.W-width, false)
			width = s.W
			encodeGenericInto(e, gb, s)
		}
		iadw.write(0, true) // OOB ends the height class
		i = j
	}
	// Export flags: a run of none, then a run of everything.
	iaex.write(0, false)
	iaex.write(len(syms), false)
	coded := e.flush()

	d := make([]byte, 0, 18+len(coded))
	d = binary.BigEndian.AppendUint16(d, 0) // arithmetic, no refinement, template 0
	d = append(d, nominalATTemplate0...)
	d = binary.BigEndian.AppendUint32(d, uint32(len(syms)))
	d = binary.BigEndian.AppendUint32(d, uint32(len(syms)))
	return append(d, coded...)
}

// instance is one symbol placed at a reference-corner coordinate.
type instance struct {
	id   int
	s, t int
}

// textParams are the coordinate settings a text region can carry, which are
// precisely the ones no fixture reaches.
type textParams struct {
	w, h       int
	refCorner  int
	transposed bool
	logStrips  int
	dsOffset   int
	combOp     int
	defPixel   int
}

// buildTextRegion returns a text region segment BODY placing insts, which must
// be sorted by strip and then by S.
//
// It writes the coordinates the DECODER will read, which is not the same as the
// coordinates the caller thinks in: S is coded as a delta from the previous
// instance's advanced S, and the advance depends on the reference corner. The
// arithmetic here is step 3c of T.88 6.4.5 run forwards, and placeSymbol runs it
// backwards -- a disagreement between the two shows up as a shifted glyph in the
// round trip rather than as an error.
func buildTextRegion(p textParams, syms []*Bitmap, insts []instance) []byte {
	strips := 1 << p.logStrips
	codeLen := symCodeLen(len(syms))
	e := newEncoder()
	iadt := intEnc{e, newIntContexts()}
	iafs := intEnc{e, newIntContexts()}
	iads := intEnc{e, newIntContexts()}
	iait := intEnc{e, newIntContexts()}
	iaid := make(contexts, 1<<(codeLen+1))

	iadt.write(0, false) // the initial strip T, which the decoder negates
	stripT, firstS := 0, 0
	for i := 0; i < len(insts); {
		j := i
		t0 := insts[i].t / strips * strips
		for j < len(insts) && insts[j].t/strips*strips == t0 {
			j++
		}
		iadt.write((t0-stripT)/strips, false)
		stripT = t0

		curS := 0
		for k, in := range insts[i:j] {
			sym := syms[in.id]
			w, h := sym.W, sym.H
			right := p.refCorner == refCornerTopRight || p.refCorner == refCornerBottomRight
			bottom := p.refCorner == refCornerBottomLeft || p.refCorner == refCornerBottomRight
			// The leading edge: what CURS holds when the decoder reads this
			// instance, before its own pre-advance.
			lead := in.s
			if !p.transposed && right {
				lead = in.s - (w - 1)
			}
			if p.transposed && bottom {
				lead = in.s - (h - 1)
			}
			if k == 0 {
				iafs.write(lead-firstS, false)
				firstS = lead
			} else {
				iads.write(lead-curS-p.dsOffset, false)
			}
			curS = lead
			if strips != 1 {
				iait.write(in.t-stripT, false)
			}
			writeIAID(e, iaid, in.id, codeLen)
			// The post-advance the decoder applies before the next delta.
			if !p.transposed {
				curS += w - 1
			} else {
				curS += h - 1
			}
		}
		iads.write(0, true) // OOB ends the strip
		i = j
	}
	coded := e.flush()

	flags := uint16(0)
	flags |= uint16(p.logStrips) << 2
	flags |= uint16(p.refCorner) << 4
	if p.transposed {
		flags |= 1 << 6
	}
	flags |= uint16(p.combOp) << 7
	flags |= uint16(p.defPixel) << 9
	flags |= uint16(p.dsOffset&0x1F) << 10

	d := make([]byte, 0, 23+len(coded))
	d = binary.BigEndian.AppendUint32(d, uint32(p.w))
	d = binary.BigEndian.AppendUint32(d, uint32(p.h))
	d = binary.BigEndian.AppendUint32(d, 0)
	d = binary.BigEndian.AppendUint32(d, 0)
	d = append(d, 0x00) // external combination operator OR
	d = binary.BigEndian.AppendUint16(d, flags)
	d = binary.BigEndian.AppendUint32(d, uint32(len(insts)))
	return append(d, coded...)
}

// symbolStream assembles a complete embedded stream: a page information segment,
// a symbol dictionary, and a text region referring to it.
func symbolStream(pageW, pageH int, dict, text []byte) []byte {
	pi := pageInfoSegmentData(pageW, pageH)
	out := append(segmentHeader(0, segTypePageInformation, 1, len(pi)), pi...)
	out = append(out, segmentHeader(1, segTypeSymbolDictionary, 1, len(dict))...)
	out = append(out, dict...)
	// A referring header, which segmentHeader does not write: one referred-to
	// segment, its number one byte wide because this segment's number is 2.
	h := make([]byte, 0, 12)
	h = binary.BigEndian.AppendUint32(h, 2)
	h = append(h, segTypeImmediateTextRegion, 0x20, 0x01, 0x01)
	h = binary.BigEndian.AppendUint32(h, uint32(len(text)))
	out = append(out, h...)
	return append(out, text...)
}

// glyph returns a small asymmetric bitmap: different under a horizontal flip, a
// vertical flip and a transpose, so a symbol placed at the wrong corner or on
// the wrong axis does not accidentally match.
func glyph(w, h, seed int) *Bitmap {
	b := NewBitmap(w, h)
	s := uint32(seed*2654435761 + 1)
	for y := range h {
		for x := range w {
			s = s*1664525 + 1013904223
			if x == 0 || y == 0 || (x+2*y+seed)%5 == 0 || s>>30 == 0 {
				b.Set(x, y, 1)
			}
		}
	}
	return b
}

// drawExpected paints what the placements mean, independently of the decoder:
// the reference corner rule of T.88 6.4.5 step 3c ix applied directly, with no
// delta arithmetic anywhere near it.
func drawExpected(p textParams, syms []*Bitmap, insts []instance) *Bitmap {
	want := NewBitmap(p.w, p.h)
	if p.defPixel == 1 {
		for i := range want.Pix {
			want.Pix[i] = 0xFF
		}
		want.MaskPadding()
	}
	for _, in := range insts {
		sym := syms[in.id]
		w, h := sym.W, sym.H
		right := p.refCorner == refCornerTopRight || p.refCorner == refCornerBottomRight
		bottom := p.refCorner == refCornerBottomLeft || p.refCorner == refCornerBottomRight
		var x0, y0 int
		if !p.transposed {
			x0, y0 = in.s, in.t
		} else {
			x0, y0 = in.t, in.s
		}
		if right {
			x0 -= w - 1
		}
		if bottom {
			y0 -= h - 1
		}
		composite(want, sym, x0, y0, byte(p.combOp))
	}
	return want
}

func testSymbols() []*Bitmap {
	return []*Bitmap{glyph(5, 7, 1), glyph(9, 7, 2), glyph(4, 11, 3), glyph(12, 11, 4)}
}

// Every reference corner, both axes, and multi-row strips. The expected raster
// is painted by drawExpected, which knows nothing of deltas or strips, so the
// two agree only if the decoder's coordinate arithmetic is right.
func TestTextRegionCoordinatesAcrossEveryCornerAndAxis(t *testing.T) {
	syms := testSymbols()
	insts := []instance{
		{0, 20, 14}, {1, 30, 14}, {3, 46, 14},
		{2, 22, 40}, {0, 30, 40}, {1, 41, 41},
		{3, 24, 70}, {2, 44, 71},
	}
	for _, corner := range []int{refCornerBottomLeft, refCornerTopLeft, refCornerBottomRight, refCornerTopRight} {
		for _, transposed := range []bool{false, true} {
			for _, logStrips := range []int{0, 2} {
				p := textParams{w: 120, h: 100, refCorner: corner,
					transposed: transposed, logStrips: logStrips}
				name := [4]string{"bottomleft", "topleft", "bottomright", "topright"}[corner]
				if transposed {
					name += "-transposed"
				}
				name += [4]string{"-strips1", "", "-strips4"}[logStrips]
				t.Run(name, func(t *testing.T) {
					s := symbolStream(p.w, p.h, buildSymbolDict(syms), buildTextRegion(p, syms, insts))
					got, err := DecodeEmbeddedStream(s)
					if err != nil {
						t.Fatalf("DecodeEmbeddedStream: %v", err)
					}
					want := drawExpected(p, syms, insts)
					if !bytes.Equal(got.Pix, want.Pix) {
						t.Errorf("the decoded page differs from the placements it was built from; "+
							"got %d ink pixels, want %d. A corner or axis rule in placeSymbol "+
							"disagrees with T.88 6.4.5 step 3c.", inkCount(got), inkCount(want))
					}
				})
			}
		}
	}
}

// SBDSOFFSET is a signed five-bit field, so an encoder writing -1 writes 31.
// Reading it unsigned shifts every symbol after the first in a strip by 32
// pixels, which is a page that still looks like a page.
func TestTextRegionAppliesASignedDSOffset(t *testing.T) {
	syms := testSymbols()
	insts := []instance{{0, 10, 12}, {1, 24, 12}, {2, 40, 12}}
	for _, off := range []int{-16, -1, 0, 1, 15} {
		p := textParams{w: 120, h: 40, dsOffset: off}
		t.Run(strconvItoa(off), func(t *testing.T) {
			s := symbolStream(p.w, p.h, buildSymbolDict(syms), buildTextRegion(p, syms, insts))
			got, err := DecodeEmbeddedStream(s)
			if err != nil {
				t.Fatalf("DecodeEmbeddedStream: %v", err)
			}
			if want := drawExpected(p, syms, insts); !bytes.Equal(got.Pix, want.Pix) {
				t.Errorf("SBDSOFFSET %d: decoded page differs from its placements", off)
			}
		})
	}
}

func strconvItoa(v int) string {
	if v == 0 {
		return "0"
	}
	neg := v < 0
	if neg {
		v = -v
	}
	var b []byte
	for v > 0 {
		b = append([]byte{byte('0' + v%10)}, b...)
		v /= 10
	}
	if neg {
		return "-" + string(b)
	}
	return string(b)
}

// A symbol dictionary with SEVERAL HEIGHT CLASSES, which the jbig2enc fixture
// has and this pins directly: the height delta is cumulative across classes and
// the width delta resets inside each one, and swapping those two rules decodes
// every symbol after the first at the wrong size.
func TestSymbolDictionaryHeightClasses(t *testing.T) {
	syms := []*Bitmap{glyph(3, 4, 7), glyph(9, 4, 8), glyph(5, 9, 9), glyph(6, 16, 10), glyph(2, 16, 11)}
	insts := []instance{{0, 4, 20}, {1, 10, 20}, {2, 22, 20}, {3, 30, 20}, {4, 40, 20}}
	p := textParams{w: 60, h: 30}
	s := symbolStream(p.w, p.h, buildSymbolDict(syms), buildTextRegion(p, syms, insts))
	got, err := DecodeEmbeddedStream(s)
	if err != nil {
		t.Fatalf("DecodeEmbeddedStream: %v", err)
	}
	if want := drawExpected(p, syms, insts); !bytes.Equal(got.Pix, want.Pix) {
		t.Errorf("a five-symbol dictionary over three height classes decoded wrongly")
	}
}

// THE CROSS-CHECK THAT STOPS A SHARED MISREADING. Everything above is this
// file's encoder against this package's decoder; if both misread the same field
// the round trip still closes. jbig2dec has no such relationship to either.
func TestSyntheticStreamsAreWhatJBIG2DecSaysTheyAre(t *testing.T) {
	if _, err := exec.LookPath("jbig2dec"); err != nil {
		t.Skipf("jbig2dec not installed (brew install jbig2dec): %v", err)
	}
	syms := testSymbols()
	insts := []instance{{0, 20, 14}, {1, 30, 14}, {3, 46, 14}, {2, 22, 40}, {0, 30, 40}}
	for _, corner := range []int{refCornerBottomLeft, refCornerTopLeft, refCornerBottomRight, refCornerTopRight} {
		for _, transposed := range []bool{false, true} {
			p := textParams{w: 120, h: 100, refCorner: corner, transposed: transposed}
			name := [4]string{"bottomleft", "topleft", "bottomright", "topright"}[corner]
			if transposed {
				name += "-transposed"
			}
			t.Run(name, func(t *testing.T) {
				s := symbolStream(p.w, p.h, buildSymbolDict(syms), buildTextRegion(p, syms, insts))
				dir := t.TempDir()
				in := filepath.Join(dir, "in.jb2")
				out := filepath.Join(dir, "out.pbm")
				if err := os.WriteFile(in, s, 0o600); err != nil {
					t.Fatal(err)
				}
				if b, err := exec.Command("jbig2dec", "-e", "-t", "pbm", "-o", out, in).CombinedOutput(); err != nil {
					t.Fatalf("jbig2dec refused a stream this file built: %v: %s", err, b)
				}
				ref, err := os.ReadFile(out)
				if err != nil {
					t.Fatal(err)
				}
				want := drawExpected(p, syms, insts)
				if got := readPBM(t, ref); !bytes.Equal(got.Pix, want.Pix) {
					t.Errorf("jbig2dec decodes this stream to %d ink pixels and the placements it was "+
						"built from paint %d. This file's encoder and this package's decoder agree "+
						"with each other and not with T.88.", inkCount(got), inkCount(want))
				}
			})
		}
	}
}

// SBDEFPIXEL and SBCOMBOP, which decide what a text region looks like BEFORE any
// symbol lands on it and how each symbol merges with what is there. Neither is
// reachable through the jbig2enc fixture, which writes zero for both, and
// getting either wrong inverts a region or drops every overlap.
//
// jbig2dec adjudicates, because drawExpected and placeSymbol share composite()
// and would agree with each other on a wrong operator.
func TestTextRegionDefaultPixelAndCombinationOperator(t *testing.T) {
	syms := testSymbols()
	// Overlapping placements, so an operator that differs from OR shows.
	insts := []instance{{0, 20, 20}, {1, 23, 20}, {2, 26, 24}, {3, 24, 26}}
	_, decErr := exec.LookPath("jbig2dec")
	for _, defPixel := range []int{0, 1} {
		for _, op := range []int{0, 1, 2, 3} {
			p := textParams{w: 80, h: 60, defPixel: defPixel, combOp: op}
			name := [4]string{"or", "and", "xor", "xnor"}[op] + strconvItoa(defPixel)
			t.Run(name, func(t *testing.T) {
				s := symbolStream(p.w, p.h, buildSymbolDict(syms), buildTextRegion(p, syms, insts))
				got, err := DecodeEmbeddedStream(s)
				if err != nil {
					t.Fatalf("DecodeEmbeddedStream: %v", err)
				}
				want := drawExpected(p, syms, insts)
				if !bytes.Equal(got.Pix, want.Pix) {
					t.Errorf("SBDEFPIXEL %d SBCOMBOP %d: decoded region differs from its placements",
						defPixel, op)
				}
				if decErr != nil {
					t.Skip("jbig2dec not installed; the round trip above cannot tell a shared " +
						"misreading of SBCOMBOP from a correct one")
				}
				dir := t.TempDir()
				in := filepath.Join(dir, "in.jb2")
				out := filepath.Join(dir, "out.pbm")
				if err := os.WriteFile(in, s, 0o600); err != nil {
					t.Fatal(err)
				}
				if b, err := exec.Command("jbig2dec", "-e", "-t", "pbm", "-o", out, in).CombinedOutput(); err != nil {
					t.Fatalf("jbig2dec: %v: %s", err, b)
				}
				raw, err := os.ReadFile(out)
				if err != nil {
					t.Fatal(err)
				}
				if ref := readPBM(t, raw); !bytes.Equal(ref.Pix, want.Pix) {
					t.Errorf("jbig2dec decodes SBDEFPIXEL %d SBCOMBOP %d to %d ink pixels; the "+
						"placements paint %d", defPixel, op, inkCount(ref), inkCount(want))
				}
			})
		}
	}
}
