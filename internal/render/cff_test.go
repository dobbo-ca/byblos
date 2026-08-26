package render

import (
	"bytes"
	"context"
	"math"
	"strings"
	"testing"

	"github.com/dobbo-ca/byblos/internal/content"
)

// ---- minimal bare-CFF builder ----------------------------------------------
//
// Hand-assembled the same way text_test.go hand-assembles TrueType: no
// fonttools locally, and the format is compact enough that a hermetic builder
// is smaller than a fixture file -- and it can build the HOSTILE variants
// (subr bombs, CID keys) no legitimate tool will emit. Layout follows
// 5176.CFF.pdf; every offset is written as a fixed 5-byte integer (operand
// 29) so section offsets are computable in one pass.

// t2i appends a Type 2 charstring integer operand (5177.Type2.pdf 3.2).
func t2i(b []byte, v int) []byte {
	switch {
	case v >= -107 && v <= 107:
		return append(b, byte(v+139))
	case v >= 108 && v <= 1131:
		v -= 108
		return append(b, byte(247+v/256), byte(v%256))
	case v >= -1131 && v <= -108:
		v = -v - 108
		return append(b, byte(251+v/256), byte(v%256))
	default:
		return append(b, 28, byte(v>>8), byte(v))
	}
}

// t2cs assembles a charstring: ints become operands, strings ("rmoveto") the
// named operator.
func t2cs(items ...any) []byte {
	ops := map[string][]byte{
		"hstem": {1}, "vstem": {3}, "vmoveto": {4}, "rlineto": {5},
		"hlineto": {6}, "vlineto": {7}, "rrcurveto": {8}, "callsubr": {10},
		"return": {11}, "endchar": {14}, "hstemhm": {18}, "hintmask": {19},
		"rmoveto": {21}, "hmoveto": {22}, "callgsubr": {29},
	}
	var b []byte
	for _, it := range items {
		switch v := it.(type) {
		case int:
			b = t2i(b, v)
		case string:
			op, ok := ops[v]
			if !ok {
				panic("t2cs: unknown op " + v)
			}
			b = append(b, op...)
		case []byte: // raw bytes, e.g. hintmask data
			b = append(b, v...)
		default:
			panic("t2cs: bad item")
		}
	}
	return b
}

// cffIndexBytes builds a CFF INDEX with 2-byte offsets (empty INDEX is the
// bare 2-byte zero count).
func cffIndexBytes(items [][]byte) []byte {
	b := tbe16(nil, uint16(len(items)))
	if len(items) == 0 {
		return b
	}
	b = append(b, 2)
	off := 1
	b = tbe16(b, uint16(off))
	for _, it := range items {
		off += len(it)
		b = tbe16(b, uint16(off))
	}
	for _, it := range items {
		b = append(b, it...)
	}
	return b
}

// cffDictInt writes a DICT integer operand in fixed 5-byte form (operand 29).
func cffDictInt(b []byte, v int32) []byte {
	return tbe32(append(b, 29), uint32(v))
}

// cffOpts parameterises buildCFF. charstrings[0] is .notdef.
type cffOpts struct {
	charstrings [][]byte
	sids        []uint16 // charset SIDs for glyphs 1..; nil = ISOAdobe (SID=GID)
	encoding    []byte   // raw Encoding table; nil = predefined standard
	gsubrs      [][]byte
	lsubrs      [][]byte
	strings     [][]byte // String INDEX entries (SIDs 391..)
	topExtra    []byte   // extra Top DICT bytes, e.g. a CID ROS
	private     []byte   // raw Private DICT override (else derived from lsubrs)
	// CID-keyed (byb-8b9.8): a non-nil fdSubrs makes the font CID-keyed --
	// buildCFF emits a ROS (SIDs 391/392, "Adobe"/"Identity" when strings
	// supplies them), an FDArray of one font DICT per entry (each with a
	// Private whose Subrs are that entry, when non-nil), the raw fdSelect
	// table, and NO top-level Private; sids are then CIDs.
	fdSubrs  [][][]byte
	fdSelect []byte
}

// buildCFF assembles a bare CFF (the exact bytes a /FontFile3 /Subtype
// /Type1C stream carries).
func buildCFF(o cffOpts) []byte {
	header := []byte{1, 0, 4, 2}
	name := cffIndexBytes([][]byte{[]byte("BbOracle")})
	strs := cffIndexBytes(o.strings)
	gsubrs := cffIndexBytes(o.gsubrs)
	charStrings := cffIndexBytes(o.charstrings)
	lsubrs := []byte(nil)
	private := []byte(nil)
	if o.lsubrs != nil {
		lsubrs = cffIndexBytes(o.lsubrs)
		private = append(cffDictInt(nil, 6), 19) // Subrs at 6 = len(private)
	}
	if o.private != nil {
		private = o.private
	}

	var charset []byte
	if o.sids != nil {
		charset = []byte{0} // format 0
		for _, sid := range o.sids {
			charset = tbe16(charset, sid)
		}
	}

	dictLen := 6 + len(o.topExtra) // CharStrings (+ extras)
	if o.fdSubrs == nil {
		dictLen += 11 // Private
	} else {
		dictLen += 17 + 7 + 7 // ROS + FDArray + FDSelect
	}
	if o.sids != nil {
		dictLen += 6
	}
	if o.encoding != nil {
		dictLen += 6
	}
	topIndexLen := 2 + 1 + 2 + 2 + dictLen
	charsetOff := len(header) + len(name) + topIndexLen + len(strs) + len(gsubrs)
	encodingOff := charsetOff + len(charset)
	charStringsOff := encodingOff + len(o.encoding)
	privateOff := charStringsOff + len(charStrings)
	fdSelectOff := privateOff + len(private) + len(lsubrs)
	fdArrayOff := fdSelectOff + len(o.fdSelect)

	// FDArray: every font DICT is the fixed 11 bytes of a Private operand
	// pair, so the INDEX length -- which the private offsets inside it depend
	// on -- is known up front.
	var fdArray, fdTail []byte
	if o.fdSubrs != nil {
		pos := fdArrayOff + 2 + 1 + 2*(len(o.fdSubrs)+1) + 11*len(o.fdSubrs)
		var fontDicts [][]byte
		for _, subs := range o.fdSubrs {
			var priv, sub []byte
			if subs != nil {
				priv = append(cffDictInt(nil, 6), 19) // Subrs at 6 = len(priv)
				sub = cffIndexBytes(subs)
			}
			fontDicts = append(fontDicts,
				append(cffDictInt(cffDictInt(nil, int32(len(priv))), int32(pos)), 18))
			fdTail = append(append(fdTail, priv...), sub...)
			pos += len(priv) + len(sub)
		}
		fdArray = cffIndexBytes(fontDicts)
	}

	var topDict []byte
	if o.sids != nil {
		topDict = append(cffDictInt(topDict, int32(charsetOff)), 15)
	}
	if o.encoding != nil {
		topDict = append(cffDictInt(topDict, int32(encodingOff)), 16)
	}
	topDict = append(cffDictInt(topDict, int32(charStringsOff)), 17)
	if o.fdSubrs == nil {
		topDict = cffDictInt(topDict, int32(len(private)))
		topDict = append(cffDictInt(topDict, int32(privateOff)), 18)
	} else {
		topDict = append(cffDictInt(cffDictInt(cffDictInt(topDict, 391), 392), 0), 12, 30) // ROS
		topDict = append(cffDictInt(topDict, int32(fdArrayOff)), 12, 36)
		topDict = append(cffDictInt(topDict, int32(fdSelectOff)), 12, 37)
	}
	topDict = append(topDict, o.topExtra...)
	topIndex := cffIndexBytes([][]byte{topDict})
	if len(topIndex) != topIndexLen {
		panic("buildCFF: top index layout")
	}

	var b []byte
	b = append(b, header...)
	b = append(b, name...)
	b = append(b, topIndex...)
	b = append(b, strs...)
	b = append(b, gsubrs...)
	b = append(b, charset...)
	b = append(b, o.encoding...)
	b = append(b, charStrings...)
	b = append(b, private...)
	b = append(b, lsubrs...)
	b = append(b, o.fdSelect...)
	b = append(b, fdArray...)
	b = append(b, fdTail...)
	return b
}

// fdSelect3 builds a format-3 FDSelect from {firstGID, fd} ranges and the
// numGlyphs sentinel.
func fdSelect3(numGlyphs int, ranges ...[2]int) []byte {
	b := tbe16([]byte{3}, uint16(len(ranges)))
	for _, r := range ranges {
		b = append(tbe16(b, uint16(r[0])), byte(r[1]))
	}
	return tbe16(b, uint16(numGlyphs))
}

// cffSquare is the 500x500 axis-aligned square at the origin as a charstring
// -- the same footprint as text_test.go's squareGlyph, so the 4c pixel
// arithmetic transfers unchanged.
func cffSquare() []byte {
	return t2cs(0, 0, "rmoveto", 500, "hlineto", 500, "vlineto", -500, "hlineto", "endchar")
}

// cffSquareFont: 'A' (SID 34, standard encoding) is the square, mirroring
// testFont exactly, but as a bare CFF.
func cffSquareFont() Font {
	return Font{
		Program:   buildCFF(cffOpts{charstrings: [][]byte{t2cs("endchar"), cffSquare()}, sids: []uint16{34}}),
		FirstChar: 'A',
		Widths:    []float64{600},
	}
}

// TestType1CProgramTakesTheCFFPath pins WHICH path the fixtures exercise: a
// bare CFF is invisible to the TrueType glyf index (so it cannot ride 4c's
// path) and is accepted by the 4d wrapper.
func TestType1CProgramTakesTheCFFPath(t *testing.T) {
	p := cffSquareFont().Program
	if parseGlyfIndex(p) != nil {
		t.Fatal("bare CFF parsed as an indexable TrueType; the fixture does not exercise the 4d path")
	}
	if otf, _, _ := cffToSFNT(p); otf == nil {
		t.Fatal("cffToSFNT refused the minimal well-formed bare CFF")
	}
}

// TestType1CGlyphOriginsExact is 4c's origin arithmetic re-run through a bare
// CFF program: /F1 20 Tf, Tm origin (10,50) on a 100x100 page puts the 0.5em
// square at x 10..20, device y 40..50, to the pixel.
func TestType1CGlyphOriginsExact(t *testing.T) {
	src := "BT /F1 20 Tf 1 0 0 1 10 50 Tm (A) Tj ET"
	box := content.Box{URX: 100, URY: 100}
	img, err := Page(context.Background(), []byte(src), box, 0, 1, nil, fontsFor(cffSquareFont()))
	if err != nil {
		t.Fatalf("Page: %v", err)
	}
	assertExactPixels(t, img, 100, 100, []rect{{10, 40, 20, 50}})
}

// TestType1CCubicOutlineRenders pins the SegmentOpCubeTo hook: the same
// square drawn as four DEGENERATE cubics (control points collinear on each
// edge), which sfnt returns as cubic segments and the 4a flattener must
// reduce to the identical pixel-exact footprint.
func TestType1CCubicOutlineRenders(t *testing.T) {
	edge := func(b []byte, dx, dy int) []byte { // one straight edge as a cubic
		return t2cs(b, dx/2, dy/2, dx/4, dy/4, dx/4, dy/4, "rrcurveto")
	}
	cs := t2cs(0, 0, "rmoveto")
	cs = edge(cs, 500, 0)
	cs = edge(cs, 0, 500)
	cs = edge(cs, -500, 0)
	cs = t2cs(cs, "endchar")
	f := Font{
		Program:   buildCFF(cffOpts{charstrings: [][]byte{t2cs("endchar"), cs}, sids: []uint16{34}}),
		FirstChar: 'A',
		Widths:    []float64{600},
	}
	src := "BT /F1 20 Tf 1 0 0 1 10 50 Tm (A) Tj ET"
	box := content.Box{URX: 100, URY: 100}
	img, err := Page(context.Background(), []byte(src), box, 0, 1, nil, fontsFor(f))
	if err != nil {
		t.Fatalf("Page: %v", err)
	}
	assertExactPixels(t, img, 100, 100, []rect{{10, 40, 20, 50}})
}

// TestType1CSubrsRender: a glyph whose whole outline lives in one local and
// one global subr still renders -- pinning the standard bias (count < 1240
// biases indices by 107) and that the work gate ADMITS legitimate subrs.
func TestType1CSubrsRender(t *testing.T) {
	lsub := t2cs(0, 0, "rmoveto", 500, "hlineto", "return")
	gsub := t2cs(500, "vlineto", -500, "hlineto", "return")
	cs := t2cs(-107, "callsubr", -107, "callgsubr", "endchar")
	f := Font{
		Program: buildCFF(cffOpts{
			charstrings: [][]byte{t2cs("endchar"), cs},
			sids:        []uint16{34},
			lsubrs:      [][]byte{lsub},
			gsubrs:      [][]byte{gsub},
		}),
		FirstChar: 'A',
		Widths:    []float64{600},
	}
	src := "BT /F1 20 Tf 1 0 0 1 10 50 Tm (A) Tj ET"
	box := content.Box{URX: 100, URY: 100}
	img, err := Page(context.Background(), []byte(src), box, 0, 1, nil, fontsFor(f))
	if err != nil {
		t.Fatalf("Page: %v", err)
	}
	assertExactPixels(t, img, 100, 100, []rect{{10, 40, 20, 50}})
}

// TestType1CHintsSkippedNotExecuted: stem hints and a hintmask before the
// outline must not disturb it -- hints affect grid-fitting, never outlines --
// and the mask's data byte must be skipped as data, not read as an operator.
func TestType1CHintsSkippedNotExecuted(t *testing.T) {
	cs := t2cs(0, 500, "hstemhm", 0, 500, "hintmask", []byte{0xf0},
		0, 0, "rmoveto", 500, "hlineto", 500, "vlineto", -500, "hlineto", "endchar")
	f := Font{
		Program:   buildCFF(cffOpts{charstrings: [][]byte{t2cs("endchar"), cs}, sids: []uint16{34}}),
		FirstChar: 'A',
		Widths:    []float64{600},
	}
	src := "BT /F1 20 Tf 1 0 0 1 10 50 Tm (A) Tj ET"
	box := content.Box{URX: 100, URY: 100}
	img, err := Page(context.Background(), []byte(src), box, 0, 1, nil, fontsFor(f))
	if err != nil {
		t.Fatalf("Page: %v", err)
	}
	assertExactPixels(t, img, 100, 100, []rect{{10, 40, 20, 50}})
}

// TestType1CEncodings covers the three code-to-glyph routes: the builtin
// standard encoding above ASCII (code 180, periodcentered, SID 114), a custom
// format-0 table, and a custom format-1 range with a supplement.
func TestType1CEncodings(t *testing.T) {
	square := cffSquare()
	box := content.Box{URX: 100, URY: 100}
	show := func(code byte) []byte {
		return append(append([]byte("BT /F1 20 Tf 1 0 0 1 10 50 Tm ("), code), []byte(") Tj ET")...)
	}
	for _, tc := range []struct {
		name string
		f    Font
		code byte
	}{
		{"standard-high", Font{
			Program:   buildCFF(cffOpts{charstrings: [][]byte{t2cs("endchar"), square}, sids: []uint16{114}}),
			FirstChar: 180, Widths: []float64{600},
		}, 180},
		{"custom-format0", Font{
			Program: buildCFF(cffOpts{
				charstrings: [][]byte{t2cs("endchar"), square},
				sids:        []uint16{391}, // an arbitrary non-standard SID
				encoding:    []byte{0, 1, 'Q'},
			}),
			FirstChar: 'Q', Widths: []float64{600},
		}, 'Q'},
		{"format1-range-with-supplement", Font{
			Program: buildCFF(cffOpts{
				charstrings: [][]byte{t2cs("endchar"), square},
				sids:        []uint16{391},
				// One empty range (code 200, 0 left = glyph 1 at 200), plus a
				// supplement mapping code 65 to SID 391 = glyph 1 as well.
				encoding: []byte{0x81, 1, 200, 0, 1, 65, 0x01, 0x87},
			}),
			FirstChar: 'A', Widths: []float64{600},
		}, 'A'},
	} {
		t.Run(tc.name, func(t *testing.T) {
			img, err := Page(context.Background(), show(tc.code), box, 0, 1, nil, fontsFor(tc.f))
			if err != nil {
				t.Fatalf("Page: %v", err)
			}
			assertExactPixels(t, img, 100, 100, []rect{{10, 40, 20, 50}})
		})
	}
}

// bombSubrs is the 10-deep local-subr work bomb both hostile-bomb tests
// enter: subr i calls subr i+1 two hundred times and the last returns, so the
// full chain would execute ~200^9 charstring bytes.
func bombSubrs() [][]byte {
	const chain = 10
	var lsubrs [][]byte
	for i := 0; i < chain; i++ {
		var s []byte
		if i == chain-1 {
			s = t2cs("return")
		} else {
			for j := 0; j < 200; j++ {
				s = t2cs(s, i+1-107, "callsubr")
			}
			s = t2cs(s, "return")
		}
		lsubrs = append(lsubrs, s)
	}
	return lsubrs
}

// TestHostileCFFSubrBombRefusedBeforeParse: sfnt's own Type 2 limits (subr
// nesting 10, streams 64 KB) do NOT bound total work -- a chain of ten subrs
// each calling the next hundreds of times is a sub-KB font that would execute
// ~200^9 charstring bytes without ever tripping a depth check. The work gate
// must refuse it in bounded time, the font degrades to widths-only, and the
// rest of the page still renders.
func TestHostileCFFSubrBombRefusedBeforeParse(t *testing.T) {
	bomb := buildCFF(cffOpts{
		charstrings: [][]byte{t2cs("endchar"), t2cs(-107, "callsubr", "endchar")},
		sids:        []uint16{34},
		lsubrs:      bombSubrs(),
	})
	if otf, _, _ := cffToSFNT(bomb); otf != nil {
		t.Fatal("the subr work bomb was not refused before sfnt.Parse")
	}
	// Through the seam: widths-only, the page still renders.
	f := Font{Program: bomb, FirstChar: 'A', Widths: []float64{600}}
	src := "BT /F1 20 Tf 1 0 0 1 10 50 Tm (A) Tj ET 30 30 10 10 re f"
	box := content.Box{URX: 100, URY: 100}
	img, err := Page(context.Background(), []byte(src), box, 0, 1, nil, fontsFor(f))
	if err != nil {
		t.Fatalf("Page: %v", err)
	}
	assertExactPixels(t, img, 100, 100, []rect{{30, 60, 40, 70}})
}

// TestHostileCFFFixedEncodedCallStillGated: sfnt ROUNDS 16.16-fixed (the 255
// encoding) operands to integers, so a subr index can be spelled 255-encoded
// and still call. A gate that mispredicts that operand as an invalid index
// would admit a bomb whose entry call it never followed; the fixed-encoded
// entry into the same work bomb must still refuse.
func TestHostileCFFFixedEncodedCallStillGated(t *testing.T) {
	// -107 (subr 0 under the bias) as 16.16 fixed: -107<<16 = 0xFF950000.
	entry := t2cs([]byte{255, 0xff, 0x95, 0x00, 0x00}, "callsubr", "endchar")
	bomb := buildCFF(cffOpts{
		charstrings: [][]byte{t2cs("endchar"), entry},
		sids:        []uint16{34},
		lsubrs:      bombSubrs(),
	})
	if otf, _, _ := cffToSFNT(bomb); otf != nil {
		t.Fatal("the fixed-encoded entry call hid the subr bomb from the work gate")
	}
}

// TestHostileCFFSelfRecursionCheap: a subr that calls itself forever hits
// sfnt's depth-10 wall almost immediately, so the gate must ADMIT it as
// cheap rather than refuse -- and rendering it must skip the glyph cleanly.
func TestHostileCFFSelfRecursionCheap(t *testing.T) {
	f := Font{
		Program: buildCFF(cffOpts{
			charstrings: [][]byte{t2cs("endchar"), t2cs(-107, "callsubr", "endchar")},
			sids:        []uint16{34},
			lsubrs:      [][]byte{t2cs(-107, "callsubr", "return")},
		}),
		FirstChar: 'A',
		Widths:    []float64{600},
	}
	if otf, _, _ := cffToSFNT(f.Program); otf == nil {
		t.Fatal("depth-limited self-recursion is cheap under sfnt's own wall; the gate must not refuse it")
	}
	src := "BT /F1 20 Tf 1 0 0 1 10 50 Tm (A) Tj ET 30 30 10 10 re f"
	box := content.Box{URX: 100, URY: 100}
	img, err := Page(context.Background(), []byte(src), box, 0, 1, nil, fontsFor(f))
	if err != nil {
		t.Fatalf("Page: %v", err)
	}
	assertExactPixels(t, img, 100, 100, []rect{{30, 60, 40, 70}})
}

// cidSquareFont is the byb-8b9.8 fixture: a Type0/CIDFontType0 whose CID 7
// (GID 1 through the charset-as-CID-map) is the 500x500 square -- drawn
// entirely by FD 1's local subr, while FD 0 has no subrs at all, so a
// renderer or gate that resolves the WRONG font DICT loses the outline.
func cidSquareFont() Font {
	return Font{
		Program: buildCFF(cffOpts{
			charstrings: [][]byte{t2cs("endchar"), t2cs(-107, "callsubr", "endchar")},
			sids:        []uint16{7}, // charset: GID 1 -> CID 7
			strings:     [][]byte{[]byte("Adobe"), []byte("Identity")},
			fdSubrs: [][][]byte{nil, {t2cs(0, 0, "rmoveto",
				500, "hlineto", 500, "vlineto", -500, "hlineto", "return")}},
			fdSelect: fdSelect3(2, [2]int{0, 0}, [2]int{1, 1}),
		}),
		Type0: true,
		W:     map[uint16]float64{7: 600},
		DW:    1000,
	}
}

// TestCFFCIDKeyedRenders is byb-8b9.8's flip of the 4d-era refusal test: a
// CID-keyed CFF (Top DICT ROS, FDArray/FDSelect) is accepted down the CFF
// path -- pinned the same way TestType1CProgramTakesTheCFFPath pins 4d --
// and a Type0 show of 2-byte Identity codes inks the CID's glyph exactly
// where the single-byte square lands in the 4d tests.
func TestCFFCIDKeyedRenders(t *testing.T) {
	f := cidSquareFont()
	if parseGlyfIndex(f.Program) != nil {
		t.Fatal("CID-keyed CFF parsed as an indexable TrueType; the fixture does not exercise the CFF path")
	}
	otf, _, cid2gid := cffToSFNT(f.Program)
	if otf == nil {
		t.Fatal("cffToSFNT refused the minimal well-formed CID-keyed CFF")
	}
	if cid2gid[7] != 1 {
		t.Fatalf("charset-as-CID-map: cid2gid = %v, want 7->1", cid2gid)
	}
	src := "BT /F1 20 Tf 1 0 0 1 10 50 Tm <0007> Tj ET"
	box := content.Box{URX: 100, URY: 100}
	img, err := Page(context.Background(), []byte(src), box, 0, 1, nil, fontsFor(f))
	if err != nil {
		t.Fatalf("Page: %v", err)
	}
	assertExactPixels(t, img, 100, 100, []rect{{10, 40, 20, 50}})
}

// TestType0IdentityDecoding pins the 2-byte seam arithmetic: /W drives the
// advance between two shows of CID 7 (600 thousandths at 20pt = 12px), a CID
// with NO glyph (9, absent from the charset) still advances by /DW's spec
// default 1000, word spacing does NOT apply to the 2-byte code 0x0020, and a
// dangling odd byte is dropped, not read as half a code.
func TestType0IdentityDecoding(t *testing.T) {
	box := content.Box{URX: 100, URY: 100}
	show := func(hex string) []byte {
		return []byte("BT /F1 20 Tf 1 0 0 1 10 50 Tm <" + hex + "> Tj ET")
	}
	f := cidSquareFont()
	for _, tc := range []struct {
		name string
		src  []byte
		want []rect
	}{
		{"w-advance", show("00070007"), []rect{{10, 40, 20, 50}, {22, 40, 32, 50}}},
		{"dw-default-for-unmapped-cid", show("00090007"), []rect{{30, 40, 40, 50}}},
		{"trailing-odd-byte-dropped", show("000700"), []rect{{10, 40, 20, 50}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			img, err := Page(context.Background(), tc.src, box, 0, 1, nil, fontsFor(f))
			if err != nil {
				t.Fatalf("Page: %v", err)
			}
			assertExactPixels(t, img, 100, 100, tc.want)
		})
	}
	t.Run("word-spacing-never-applies", func(t *testing.T) {
		// 9.3.3: word spacing applies to the SINGLE-byte code 32 only. Code
		// 0x0020 (CID 32, no glyph) must advance by DW alone, putting the
		// following square at 30, not 30+40.
		src := []byte("BT /F1 20 Tf 40 Tw 1 0 0 1 10 50 Tm <00200007> Tj ET")
		img, err := Page(context.Background(), src, box, 0, 1, nil, fontsFor(cidSquareFont()))
		if err != nil {
			t.Fatalf("Page: %v", err)
		}
		assertExactPixels(t, img, 100, 100, []rect{{30, 40, 40, 50}})
	})
	t.Run("dw-500-halves-the-unmapped-advance", func(t *testing.T) {
		// A descendant with /DW 500 (half-width) must advance an unmapped
		// CID by 500, not the 1000 default: square at 20, not 30.
		f := cidSquareFont()
		f.DW = 500
		img, err := Page(context.Background(), show("00090007"), box, 0, 1, nil, fontsFor(f))
		if err != nil {
			t.Fatalf("Page: %v", err)
		}
		assertExactPixels(t, img, 100, 100, []rect{{20, 40, 30, 50}})
	})
}

// TestCFFFDSelectUnsortedFormat3MatchesSfnt pins cffFDSelect's reason to
// exist: it mirrors sfnt's fdSelect.lookup, which bisects the ranges in table
// order WITHOUT checking they are sorted. On this deliberately unsorted table
// ({first, fd}: {0,0} {40,1} {10,2} {60,3}, sentinel 100) the bisection lands
// on FD 2 for gids 20 and 39 where a first-match linear scan would report FD
// 0 -- so a "simplified" validating or scanning rewrite diverges right here,
// and the gate would walk different local subrs than sfnt executes.
func TestCFFFDSelectUnsortedFormat3MatchesSfnt(t *testing.T) {
	p := append([]byte{0xff}, fdSelect3(100, [2]int{0, 0}, [2]int{40, 1}, [2]int{10, 2}, [2]int{60, 3})...)
	sel := cffFDSelect(p, 1, 100, 4)
	if sel == nil {
		t.Fatal("a well-formed (if unsorted) format-3 FDSelect was refused")
	}
	for _, tc := range []struct{ gid, fd int }{
		{0, 0}, {5, 0}, {20, 2}, {39, 2}, {45, 2}, {70, 3}, {99, 3},
	} {
		if got := sel[tc.gid]; got != int16(tc.fd) {
			t.Errorf("gid %d: fd = %d, want %d (sfnt's bisection order)", tc.gid, got, tc.fd)
		}
	}
}

// TestHostileCIDCFFCountsCappedLikeSfnt: sfnt.Parse refuses an FDArray over
// maxNumFontDicts (256) entries and a Subrs INDEX over maxNumSubroutines
// (40000), so the gate must refuse them BEFORE its own per-FD parse -- a tiny
// file declaring 65535 FDs whose font DICTs share one 65535-entry Subrs INDEX
// would otherwise cost numFD x numSubrs slice headers (gigabytes) with no
// budget in sight. One at each cap exactly stays admitted.
func TestHostileCIDCFFCountsCappedLikeSfnt(t *testing.T) {
	cid := func(nFD, nSubrs int) []byte {
		fdSubrs := make([][][]byte, nFD)
		if nSubrs > 0 {
			fdSubrs[0] = make([][]byte, nSubrs) // zero-length subrs: tiny file
		}
		return buildCFF(cffOpts{
			charstrings: [][]byte{t2cs("endchar"), t2cs("endchar")},
			sids:        []uint16{7},
			fdSubrs:     fdSubrs,
			fdSelect:    fdSelect3(2, [2]int{0, 0}),
		})
	}
	if otf, _, _ := cffToSFNT(cid(257, 0)); otf != nil {
		t.Fatal("a 257-entry FDArray (past sfnt's maxNumFontDicts) was not refused")
	}
	if otf, _, _ := cffToSFNT(cid(256, 0)); otf == nil {
		t.Fatal("a 256-entry FDArray (sfnt's cap exactly) must be admitted")
	}
	if otf, _, _ := cffToSFNT(cid(1, 40001)); otf != nil {
		t.Fatal("a 40001-entry Subrs INDEX (past sfnt's maxNumSubroutines) was not refused")
	}
	if otf, _, _ := cffToSFNT(cid(1, 40000)); otf == nil {
		t.Fatal("a 40000-entry Subrs INDEX (sfnt's cap exactly) must be admitted")
	}
}

// TestType0OverPlainCFFNeverInks: a Type0 dict over a program with no CID map
// (here 4d's plain Type1C) must advance by DW but ink NOTHING -- its glyphs
// were work-gated for the 256 single-byte codes only, so letting 2-byte codes
// index them would reach charstrings the gate never walked.
func TestType0OverPlainCFFNeverInks(t *testing.T) {
	f := cffSquareFont()
	f.Type0, f.FirstChar, f.Widths = true, 0, nil
	// 0x0041: were codes ridden through the single-byte path (or CID read as
	// GID), 'A' would ink the square at 10..20.
	src := "BT /F1 20 Tf 1 0 0 1 10 50 Tm <0041> Tj ET 30 30 10 10 re f"
	box := content.Box{URX: 100, URY: 100}
	img, err := Page(context.Background(), []byte(src), box, 0, 1, nil, fontsFor(f))
	if err != nil {
		t.Fatalf("Page: %v", err)
	}
	assertExactPixels(t, img, 100, 100, []rect{{30, 60, 40, 70}})
}

// TestHostileCIDCFFSubrBombBehindFDSelect: the 4d work bomb, reachable ONLY
// through FD 1 of a two-FD FDArray while FD 0 is benign. A gate that walks
// the glyph with the wrong font DICT's subrs -- or with the single-Private
// path's -- admits the bomb and hands sfnt the CPU-years LoadGlyph.
func TestHostileCIDCFFSubrBombBehindFDSelect(t *testing.T) {
	bomb := buildCFF(cffOpts{
		charstrings: [][]byte{t2cs("endchar"), t2cs(-107, "callsubr", "endchar")},
		sids:        []uint16{7},
		fdSubrs:     [][][]byte{{t2cs("return")}, bombSubrs()},
		fdSelect:    fdSelect3(2, [2]int{0, 0}, [2]int{1, 1}),
	})
	if otf, _, _ := cffToSFNT(bomb); otf != nil {
		t.Fatal("the subr work bomb behind FDSelect was not refused before sfnt.Parse")
	}
	// Through the seam: widths-only, the page still renders.
	f := Font{Program: bomb, Type0: true, W: map[uint16]float64{7: 600}}
	src := "BT /F1 20 Tf 1 0 0 1 10 50 Tm <0007> Tj ET 30 30 10 10 re f"
	box := content.Box{URX: 100, URY: 100}
	img, err := Page(context.Background(), []byte(src), box, 0, 1, nil, fontsFor(f))
	if err != nil {
		t.Fatalf("Page: %v", err)
	}
	assertExactPixels(t, img, 100, 100, []rect{{30, 60, 40, 70}})
}

// TestHostileCIDCFFTotalGateBudget: in a CID font EVERY glyph is reachable
// (any 2-byte code is a CID), so per-glyph budgets alone would let glyph
// count multiply the gate's own walking cost. The whole-font walk must stop
// at 256 x maxCharstringWork -- the 4d gate's ceiling -- while a font under
// that total is admitted, so the cap is the total and nothing else.
func TestHostileCIDCFFTotalGateBudget(t *testing.T) {
	defer func(old int64) { maxCharstringWork = old }(maxCharstringWork)
	maxCharstringWork = 100

	font := func(n int) []byte {
		sled := append(bytes.Repeat([]byte{1}, 99), t2cs("endchar")...) // 100 units, at the per-glyph cap
		cs, sids := [][]byte{t2cs("endchar")}, []uint16(nil)
		for i := 1; i <= n; i++ {
			cs, sids = append(cs, sled), append(sids, uint16(i))
		}
		return buildCFF(cffOpts{
			charstrings: cs,
			sids:        sids,
			fdSubrs:     [][][]byte{nil},
			fdSelect:    fdSelect3(n+1, [2]int{0, 0}),
		})
	}
	if otf, _, _ := cffToSFNT(font(300)); otf != nil { // 300 x 100 > 25600
		t.Fatal("30000 total walk units under a 25600 ceiling were not refused")
	}
	if otf, _, _ := cffToSFNT(font(200)); otf == nil { // 200 x 100 <= 25600
		t.Fatal("a CID font within the total gate budget must be admitted")
	}
}

// TestCIDZeroSegmentGlyphChargesFillWork is the CID twin of
// TestCFFZeroSegmentGlyphChargesFillWork: an attacker's CID font gets the
// same per-show charge, so re-showing a zero-segment hstem sled through
// 2-byte codes trips maxFillWork instead of re-running the interpreter free.
func TestCIDZeroSegmentGlyphChargesFillWork(t *testing.T) {
	defer func(old int64) { maxFillWork = old }(maxFillWork)
	maxFillWork = 100_000

	sled := append(bytes.Repeat([]byte{1}, 60_000), t2cs("return")...)
	f := Font{
		Program: buildCFF(cffOpts{
			charstrings: [][]byte{t2cs("endchar"), t2cs(-107, "callsubr", "endchar")},
			sids:        []uint16{7},
			fdSubrs:     [][][]byte{{sled}},
			fdSelect:    fdSelect3(2, [2]int{0, 0}),
		}),
		Type0: true,
		W:     map[uint16]float64{7: 600},
	}
	if otf, _, _ := cffToSFNT(f.Program); otf == nil {
		t.Fatal("the hstem sled must be ADMITTED (one show is within budget); the point is the per-show charge")
	}
	src := "BT /F1 20 Tf 1 0 0 1 10 50 Tm <00070007> Tj ET"
	box := content.Box{URX: 100, URY: 100}
	_, err := Page(context.Background(), []byte(src), box, 0, 1, nil, fontsFor(f))
	if err == nil || !strings.Contains(err.Error(), "fill work exceeds") {
		t.Fatalf("two shows of a ~60000-work zero-segment glyph under a 100000 budget must trip fill work, got %v", err)
	}
}

// TestCFFMalformedNoPanic feeds every truncation and a corruption sweep of a
// well-formed CFF -- plain and CID-keyed -- through the wrapper: refuse or
// accept, but never panic and never read out of bounds.
func TestCFFMalformedNoPanic(t *testing.T) {
	for _, good := range [][]byte{cffSquareFont().Program, cidSquareFont().Program} {
		for i := 0; i <= len(good); i++ {
			cffToSFNT(good[:i:i])
		}
		for i := 0; i < len(good); i++ {
			bad := append([]byte(nil), good...)
			bad[i] ^= 0xff
			cffToSFNT(bad)
		}
	}
}

// TestCIDFDPastFDArraySkipsCleanly: an FDSelect entry pointing past the
// FDArray is exactly what sfnt refuses at that glyph's first callsubr, so
// the font is ADMITTED, the glyph inks nothing, and the rest of the page
// still renders -- no panic from indexing fdSubrs with a hostile FD.
func TestCIDFDPastFDArraySkipsCleanly(t *testing.T) {
	f := cidSquareFont()
	f.Program = buildCFF(cffOpts{
		charstrings: [][]byte{t2cs("endchar"), t2cs(-107, "callsubr", "endchar")},
		sids:        []uint16{7},
		fdSubrs:     [][][]byte{nil, {t2cs(0, 0, "rmoveto", 500, "hlineto", 500, "vlineto", -500, "hlineto", "return")}},
		fdSelect:    fdSelect3(2, [2]int{0, 0}, [2]int{1, 9}),
	})
	if otf, _, _ := cffToSFNT(f.Program); otf == nil {
		t.Fatal("an out-of-range FD refuses only its own glyph's subr calls; the font must be admitted")
	}
	src := "BT /F1 20 Tf 1 0 0 1 10 50 Tm <0007> Tj ET 30 30 10 10 re f"
	box := content.Box{URX: 100, URY: 100}
	img, err := Page(context.Background(), []byte(src), box, 0, 1, nil, fontsFor(f))
	if err != nil {
		t.Fatalf("Page: %v", err)
	}
	assertExactPixels(t, img, 100, 100, []rect{{30, 60, 40, 70}})
}

// TestCFFWorkGateBudgetTrips: with the per-glyph charstring budget lowered
// below even the plain square, the font must degrade to widths-only -- the
// budget is live, not decorative.
func TestCFFWorkGateBudgetTrips(t *testing.T) {
	defer func(old int64) { maxCharstringWork = old }(maxCharstringWork)
	maxCharstringWork = 3

	f := cffSquareFont()
	if otf, _, _ := cffToSFNT(f.Program); otf != nil {
		t.Fatal("lowered charstring budget did not refuse the font; the gate is dead")
	}
	src := "BT /F1 20 Tf 1 0 0 1 10 50 Tm (A) Tj ET 30 30 10 10 re f"
	box := content.Box{URX: 100, URY: 100}
	img, err := Page(context.Background(), []byte(src), box, 0, 1, nil, fontsFor(f))
	if err != nil {
		t.Fatalf("Page: %v", err)
	}
	assertExactPixels(t, img, 100, 100, []rect{{30, 60, 40, 70}})
}

// TestCFFWidthPrefixDoesNotShiftOutline: a leading width operand on the first
// stack-clearing operator (5177.Type2.pdf's optional width) must be consumed
// as the width, not as an outline coordinate.
func TestCFFWidthPrefixDoesNotShiftOutline(t *testing.T) {
	cs := t2cs(600, 0, 0, "rmoveto", 500, "hlineto", 500, "vlineto", -500, "hlineto", "endchar")
	f := Font{
		Program:   buildCFF(cffOpts{charstrings: [][]byte{t2cs("endchar"), cs}, sids: []uint16{34}}),
		FirstChar: 'A',
		Widths:    []float64{600},
	}
	src := "BT /F1 20 Tf 1 0 0 1 10 50 Tm (A) Tj ET"
	box := content.Box{URX: 100, URY: 100}
	img, err := Page(context.Background(), []byte(src), box, 0, 1, nil, fontsFor(f))
	if err != nil {
		t.Fatalf("Page: %v", err)
	}
	assertExactPixels(t, img, 100, 100, []rect{{10, 40, 20, 50}})
}

// TestCFFHugeCoordinatesStayBounded: 16.16-fixed operands (the 255 encoding)
// can push coordinates to +/-2^15 em and beyond through accumulation; however
// they land, the render must terminate within its budgets without a panic --
// the amd64 int64-overflow hang class from the 4a reviews.
func TestCFFHugeCoordinatesStayBounded(t *testing.T) {
	cs := []byte{}
	cs = t2cs(cs, 0, 0, "rmoveto")
	for i := 0; i < 20; i++ {
		cs = append(cs, 255, 0x7f, 0xff, 0xff, 0xff) // +32767.999... em
		cs = append(cs, 255, 0x7f, 0xff, 0xff, 0xff)
		cs = t2cs(cs, "rlineto")
	}
	cs = t2cs(cs, "endchar")
	f := Font{
		Program:   buildCFF(cffOpts{charstrings: [][]byte{t2cs("endchar"), cs}, sids: []uint16{34}}),
		FirstChar: 'A',
		Widths:    []float64{600},
	}
	src := "BT /F1 2000 Tf 1 0 0 1 10 50 Tm (" + strings.Repeat("A", 50) + ") Tj ET"
	box := content.Box{URX: 100, URY: 100}
	if _, err := Page(context.Background(), []byte(src), box, 0, 1, nil, fontsFor(f)); err != nil {
		// A tripped budget is acceptable; a hang or panic is not, and the
		// test binary's timeout is the harness for those.
		t.Logf("Page returned %v (budget trips are fine)", err)
	}
}

// t30 nibble-encodes a DICT real (operand 30) from its decimal spelling.
func t30(s string) []byte {
	var nibs []byte
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case c >= '0' && c <= '9':
			nibs = append(nibs, c-'0')
		case c == '.':
			nibs = append(nibs, 0x0a)
		case c == 'E' && i+1 < len(s) && s[i+1] == '-':
			nibs = append(nibs, 0x0c)
			i++
		case c == 'E':
			nibs = append(nibs, 0x0b)
		case c == '-':
			nibs = append(nibs, 0x0e)
		default:
			panic("t30: bad char")
		}
	}
	nibs = append(nibs, 0x0f)
	if len(nibs)%2 == 1 {
		nibs = append(nibs, 0x0f)
	}
	b := []byte{30}
	for i := 0; i < len(nibs); i += 2 {
		b = append(b, nibs[i]<<4|nibs[i+1])
	}
	return b
}

// TestCFFFontMatrixRealOperands: real Type1C fonts spell FontMatrix (and most
// Private DICT values) with operand 30's nibble reals, which no other fixture
// emits -- buildCFF writes only 5-byte integers. A 1/2048 matrix must parse
// to upem 2048 through every nibble branch (digits, '.', 'E', 'E-', '-'), and
// a non-uniform matrix must refuse: only [s 0 0 s 0 0] maps onto sfnt's
// single unitsPerEm, anything else would render silently at the wrong shape.
func TestCFFFontMatrixRealOperands(t *testing.T) {
	font := func(m3 string) []byte {
		var mtx []byte
		for _, s := range []string{"0.00048828125", "0", "0", m3, "-0", "0E1"} {
			mtx = append(mtx, t30(s)...)
		}
		mtx = append(mtx, 12, 7) // FontMatrix
		return buildCFF(cffOpts{
			charstrings: [][]byte{t2cs("endchar"), cffSquare()},
			sids:        []uint16{34},
			topExtra:    mtx,
		})
	}
	c := parseCFF(font("4.8828125E-4")) // == 0.00048828125 = 1/2048 exactly
	if c == nil || c.upem != 2048 {
		t.Fatalf("uniform real FontMatrix: got %+v, want upem 2048", c)
	}
	if parseCFF(font("9.765625E-4")) != nil {
		t.Fatal("non-uniform FontMatrix (y scale != x scale) must refuse to widths-only")
	}
}

// TestCFFCharsetFormats covers the three charset shapes buildCFF never emits:
// the predefined ISOAdobe charset (offset 0, SID = GID) and the range formats
// 1 and 2 -- where a wrong range walk would ink WRONG glyphs, not refuse --
// plus the refused predefined Expert charsets (offsets 1 and 2).
func TestCFFCharsetFormats(t *testing.T) {
	const off = 10
	pad := make([]byte, off)
	for _, tc := range []struct {
		name string
		data []byte
		off  int
		want []uint16
	}{
		{"isoadobe-predefined", nil, 0, []uint16{0, 1, 2, 3, 4}},
		// format 1: first SID u16, nLeft u8 per range.
		{"format1", []byte{1, 0, 100, 2, 0, 200, 0}, off, []uint16{0, 100, 101, 102, 200}},
		// format 2: first SID u16, nLeft u16 per range. 300 = 0x012C.
		{"format2", []byte{2, 1, 44, 0, 3}, off, []uint16{0, 300, 301, 302, 303}},
	} {
		p := append(append([]byte(nil), pad...), tc.data...)
		got := cffCharsetSIDs(p, tc.off, len(tc.want))
		if len(got) != len(tc.want) {
			t.Fatalf("%s: got %v, want %v", tc.name, got, tc.want)
		}
		for i := range tc.want {
			if got[i] != tc.want[i] {
				t.Fatalf("%s: got %v, want %v", tc.name, got, tc.want)
			}
		}
	}
	for _, expert := range []int{1, 2} {
		if cffCharsetSIDs(pad, expert, 2) != nil {
			t.Fatalf("predefined Expert charset (offset %d) must refuse", expert)
		}
	}
}

// TestCFFIndexHugePosRefused pins cffIndex's overflow-safe bound: a position
// near math.MaxInt64 (reachable when a Private DICT Subrs operand is a
// real-encoded ~2^63 offset) must refuse, not compute pos+2, wrap it negative,
// slip the length check, and panic on p[pos:]. This is the root guard every
// caller routes through.
func TestCFFIndexHugePosRefused(t *testing.T) {
	for _, pos := range []int{math.MaxInt64, math.MaxInt64 - 1, math.MaxInt64 - 2} {
		if _, _, ok := cffIndex(make([]byte, 100), pos); ok {
			t.Fatalf("cffIndex accepted pos=%d past a 100-byte buffer", pos)
		}
	}
}

// TestCFFSubrsOffsetOverflowRefused: the Private DICT Subrs operand is the one
// offset in parseCFF that is RELATIVE (Private start + operand). A real-encoded
// ~2^63 operand must refuse to widths-only rather than reach cffIndex with a
// position that wraps, or force an undefined float->int conversion. The
// corruption sweep in TestCFFMalformedNoPanic cannot reach this shape: it never
// synthesises a 19-digit operand-30 real.
func TestCFFSubrsOffsetOverflowRefused(t *testing.T) {
	// Private DICT: Subrs (19) = real 9223372036854774784 = 2^63-1024,
	// exactly float64-representable.
	private := append(t30("9223372036854774784"), 19)
	p := buildCFF(cffOpts{
		charstrings: [][]byte{t2cs("endchar"), cffSquare()},
		sids:        []uint16{34},
		private:     private,
	})
	if otf, _, _ := cffToSFNT(p); otf != nil {
		t.Fatal("a Subrs offset past the end of the program must refuse")
	}
}

// TestCFFZeroSegmentGlyphChargesFillWork: a glyph can execute ~60 KB of
// charstring -- admitted by the gate, whose bound is per LoadGlyph -- yet
// emit ZERO segments (an hstem sled), so it flattens to no points and the
// per-point charge prices its re-shows at nothing: one Tj per content-stream
// byte would re-run the interpreter forever, under any budget and past any
// deadline (ctx is only checked per lexer token). fillGlyph must charge the
// gate-measured work per SHOW, so re-showing it trips maxFillWork like any
// other expensive operation.
func TestCFFZeroSegmentGlyphChargesFillWork(t *testing.T) {
	defer func(old int64) { maxFillWork = old }(maxFillWork)
	maxFillWork = 100_000

	sled := append(bytes.Repeat([]byte{1}, 60_000), t2cs("return")...)
	f := Font{
		Program: buildCFF(cffOpts{
			charstrings: [][]byte{t2cs("endchar"), t2cs(-107, "callsubr", "endchar")},
			sids:        []uint16{34},
			lsubrs:      [][]byte{sled},
		}),
		FirstChar: 'A',
		Widths:    []float64{600},
	}
	if otf, _, _ := cffToSFNT(f.Program); otf == nil {
		t.Fatal("the hstem sled must be ADMITTED (one show is within budget); the point is the per-show charge")
	}
	src := "BT /F1 20 Tf 1 0 0 1 10 50 Tm (AA) Tj ET"
	box := content.Box{URX: 100, URY: 100}
	_, err := Page(context.Background(), []byte(src), box, 0, 1, nil, fontsFor(f))
	if err == nil || !strings.Contains(err.Error(), "fill work exceeds") {
		t.Fatalf("two shows of a ~60000-work zero-segment glyph under a 100000 budget must trip fill work, got %v", err)
	}
}
