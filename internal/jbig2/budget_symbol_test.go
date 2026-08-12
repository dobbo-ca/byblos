package jbig2

// byb-9v0's resource budget, and first the INSTRUMENT it is measured with.
//
// Every cost test in this package and in byblos reads DecodedPixels. Before
// byb-9v0 that counter saw one thing -- MQ decisions in a generic region -- and
// symbol mode adds two more: the decisions that build a symbol bitmap, and the
// pixels a text region writes placing it. A counter blind to either leaves every
// one of those tests green and meaningless, which is the failure mode this file
// exists to make impossible.

import (
	"bytes"
	"encoding/binary"
	"strings"
	"testing"
)

// TestDecodedPixelsCountsSymbolWork pins the counter against work it can SEE
// independently: the symbols in the fixture's dictionary and the instances its
// text region places are both countable from the decoded stream, so the expected
// total is arithmetic rather than a number copied out of a previous run.
func TestDecodedPixelsCountsSymbolWork(t *testing.T) {
	syms := testSymbols()
	insts := []instance{{0, 20, 14}, {1, 34, 14}, {3, 60, 14}, {2, 22, 40}, {0, 34, 40}}
	p := textParams{w: 120, h: 100}
	s := symbolStream(p.w, p.h, buildSymbolDict(syms), buildTextRegion(p, syms, insts))

	var wantSymbol, wantPlacement int64
	for _, sym := range syms {
		wantSymbol += int64(sym.W) * int64(sym.H)
	}
	for _, in := range insts {
		wantPlacement += int64(syms[in.id].W) * int64(syms[in.id].H)
	}

	before := DecodedPixels()
	if _, err := DecodeEmbeddedStream(s); err != nil {
		t.Fatalf("DecodeEmbeddedStream: %v", err)
	}
	got := DecodedPixels() - before

	if want := wantSymbol + wantPlacement; got != want {
		t.Errorf("DecodedPixels counted %d for a stream that decodes %d pixels of symbol bitmap and "+
			"draws %d pixels of symbol instance; want %d.\n"+
			"A counter that misses either half leaves every cost test in this package and in byblos "+
			"asserting a bound on work it cannot see.",
			got, wantSymbol, wantPlacement, want)
	}
	// And each half separately reaches the counter, which the sum alone cannot
	// show: a counter that charged placement twice and symbols not at all would
	// satisfy the total on a fixture where the two happen to be equal.
	if wantSymbol == wantPlacement {
		t.Fatal("the fixture's symbol and placement totals are equal, so the assertion above cannot " +
			"tell one from the other; change the instance list")
	}
}

// A text region that places the SAME symbol many times must be charged every
// time. Charging once per symbol rather than once per instance is the natural
// mistake -- the pixels were decoded once -- and it is exactly wrong: drawing is
// what a text region spends its time on.
func TestPlacementIsChargedPerInstanceNotPerSymbol(t *testing.T) {
	syms := []*Bitmap{glyph(8, 8, 1)}
	one := []instance{{0, 10, 20}}
	many := []instance{{0, 10, 20}, {0, 30, 20}, {0, 50, 20}, {0, 70, 20}}
	p := textParams{w: 120, h: 40}

	cost := func(insts []instance) int64 {
		s := symbolStream(p.w, p.h, buildSymbolDict(syms), buildTextRegion(p, syms, insts))
		before := DecodedPixels()
		if _, err := DecodeEmbeddedStream(s); err != nil {
			t.Fatalf("DecodeEmbeddedStream: %v", err)
		}
		return DecodedPixels() - before
	}
	if got, want := cost(many)-cost(one), int64(3*8*8); got != want {
		t.Errorf("three extra instances of one 8x8 symbol cost %d counted pixels; want %d", got, want)
	}
}

// setUint32 rewrites a big-endian field in a copy of s.
func setUint32(s []byte, off int, v uint32) []byte {
	out := bytes.Clone(s)
	binary.BigEndian.PutUint32(out[off:off+4], v)
	return out
}

// The two HEADER-time symbol rules. Both fields are four bytes, so a 20-byte
// segment asks for four billion of something, and both are refused before a
// coded bit is read.
func TestSymbolHeaderCountsAreBoundedBeforeDecoding(t *testing.T) {
	syms := testSymbols()
	insts := []instance{{0, 20, 14}, {1, 34, 14}}
	p := textParams{w: 120, h: 100}
	dict := buildSymbolDict(syms)
	text := buildTextRegion(p, syms, insts)

	// The dictionary body is a 2-byte flags word, an 8-byte AT field, then
	// SDNUMEXSYMS and SDNUMNEWSYMS.
	if binary.BigEndian.Uint32(dict[14:18]) != uint32(len(syms)) {
		t.Fatalf("offset assumption broken: SDNUMNEWSYMS reads %d, want %d",
			binary.BigEndian.Uint32(dict[14:18]), len(syms))
	}
	// The text region body is 17 bytes of region information, a 2-byte flags
	// word, then SBNUMINSTANCES.
	if binary.BigEndian.Uint32(text[19:23]) != uint32(len(insts)) {
		t.Fatalf("offset assumption broken: SBNUMINSTANCES reads %d, want %d",
			binary.BigEndian.Uint32(text[19:23]), len(insts))
	}

	for _, c := range []struct {
		name, want string
		s          []byte
	}{
		{"symbols", "more than 65536 symbols",
			symbolStream(p.w, p.h, setUint32(dict, 14, 1<<31), text)},
		{"instances", "more than 2097152 symbol instances",
			symbolStream(p.w, p.h, dict, setUint32(text, 19, 1<<31))},
	} {
		t.Run(c.name, func(t *testing.T) {
			before := DecodedPixels()
			_, err := DecodeEmbeddedStream(c.s)
			if err == nil {
				t.Fatal("decoded a stream declaring two billion of something")
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Errorf("error = %v; want it to name the bound (%q)", err, c.want)
			}
			if n := DecodedPixels() - before; n != 0 {
				t.Errorf("%d pixels were decoded before the refusal; the count is in the header, so "+
					"nothing had to be", n)
			}
		})
	}
}

// The DECODE-time rule, and the one that cannot be a header rule. A symbol's
// size is coded as arithmetic deltas inside the data (T.88 6.5.5), so a
// dictionary declaring four symbols can ask for a two-billion-pixel bitmap and
// no field in the header says so.
//
// The stream below is built rather than mutated: it declares one symbol and then
// codes a height and width whose product is past the pixel budget, with no
// bitmap after them at all. A decoder that allocated first and checked afterwards
// would ask the allocator for the bitmap before noticing.
func TestOversizedSymbolIsRefusedBeforeItIsAllocated(t *testing.T) {
	e := newEncoder()
	iadh := intEnc{e, newIntContexts()}
	iadw := intEnc{e, newIntContexts()}
	iadh.write(1<<16, false) // a 65,536-row height class
	iadw.write(1<<16, false) // 65,536 wide: 4,294,967,296 pixels
	coded := e.flush()

	d := make([]byte, 0, 18+len(coded))
	d = binary.BigEndian.AppendUint16(d, 0)
	d = append(d, nominalATTemplate0...)
	d = binary.BigEndian.AppendUint32(d, 1)
	d = binary.BigEndian.AppendUint32(d, 1)
	d = append(d, coded...)

	syms := []*Bitmap{glyph(5, 7, 1)}
	text := buildTextRegion(textParams{w: 60, h: 40}, syms, []instance{{0, 10, 20}})
	s := symbolStream(60, 40, d, text)

	before := DecodedPixels()
	if _, err := DecodeEmbeddedStream(s); err == nil {
		t.Fatal("decoded a symbol declaring 4,294,967,296 pixels")
	} else if !strings.Contains(err.Error(), "the budget for one stream is") {
		t.Errorf("error = %v; want the stream budget to be what refuses it", err)
	}
	if n := DecodedPixels() - before; n != 0 {
		t.Errorf("%d pixels were decoded before the refusal; the charge is made from the dimensions, "+
			"before the bitmap exists", n)
	}
}

// The PLACEMENT rule. Instance count and symbol size are both individually
// modest here; their product is not, and no header field carries it.
func TestPlacementBudgetRefusesDrawingFarMoreThanThePageCanShow(t *testing.T) {
	syms := []*Bitmap{glyph(60, 60, 1)}
	// A 100x100 page admits 4*10,000 + 65,536 = 105,536 placement pixels, which
	// is 29 instances of a 60x60 symbol. Forty is past it and is still a tiny
	// instance count and a tiny symbol.
	insts := make([]instance, 40)
	for i := range insts {
		insts[i] = instance{0, 10, 20}
	}
	p := textParams{w: 100, h: 100}
	s := symbolStream(p.w, p.h, buildSymbolDict(syms), buildTextRegion(p, syms, insts))

	if _, err := DecodeEmbeddedStream(s); err == nil {
		t.Fatal("decoded a stream drawing 144,000 pixels of symbol onto a 10,000-pixel page")
	} else if !strings.Contains(err.Error(), "pixels of symbol onto a page that can show") {
		t.Errorf("error = %v; want the placement budget to be what refuses it", err)
	}

	// And the same shape INSIDE the budget still decodes, so the rule is a bound
	// and not a ban. Twenty-nine instances is 104,400 pixels, just under.
	ok := symbolStream(p.w, p.h, buildSymbolDict(syms), buildTextRegion(p, syms, insts[:29]))
	if _, err := DecodeEmbeddedStream(ok); err != nil {
		t.Errorf("29 instances of a 60x60 symbol on a 100x100 page: %v; that is inside the budget", err)
	}
}

// A legitimate dense page must NOT be refused, which is the other half of any
// budget and the half that is easy to get wrong: the placement rule is separate
// from the pixel rule precisely because charging drawing at the MQ rate would
// refuse a real 600-dpi scan.
//
// This is that shape in miniature and to scale: symbol boxes covering the page
// about once over, which is what a page of text is.
func TestPlacementBudgetAdmitsAPageFullyCoveredByGlyphBoxes(t *testing.T) {
	syms := []*Bitmap{glyph(10, 14, 1)}
	var insts []instance
	for y := 14; y < 200; y += 14 {
		for x := 0; x+10 <= 200; x += 10 {
			insts = append(insts, instance{0, x, y})
		}
	}
	p := textParams{w: 200, h: 200}
	s := symbolStream(p.w, p.h, buildSymbolDict(syms), buildTextRegion(p, syms, insts))
	if _, err := DecodeEmbeddedStream(s); err != nil {
		t.Errorf("a 200x200 page tiled with %d glyph boxes was refused: %v.\n"+
			"That is one page's worth of text and it must decode; if this fails the placement "+
			"budget is priced against MQ decisions rather than against drawing.", len(insts), err)
	}
}
