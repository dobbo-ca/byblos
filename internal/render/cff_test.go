package render

import (
	"context"
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
	topExtra    []byte // extra Top DICT bytes, e.g. a CID ROS
}

// buildCFF assembles a bare CFF (the exact bytes a /FontFile3 /Subtype
// /Type1C stream carries).
func buildCFF(o cffOpts) []byte {
	header := []byte{1, 0, 4, 2}
	name := cffIndexBytes([][]byte{[]byte("BbOracle")})
	strs := cffIndexBytes(nil)
	gsubrs := cffIndexBytes(o.gsubrs)
	charStrings := cffIndexBytes(o.charstrings)
	lsubrs := []byte(nil)
	private := []byte(nil)
	if o.lsubrs != nil {
		lsubrs = cffIndexBytes(o.lsubrs)
		private = append(cffDictInt(nil, 6), 19) // Subrs at 6 = len(private)
	}

	var charset []byte
	if o.sids != nil {
		charset = []byte{0} // format 0
		for _, sid := range o.sids {
			charset = tbe16(charset, sid)
		}
	}

	dictLen := 6 + 11 + len(o.topExtra) // CharStrings + Private (+ extras)
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

	var topDict []byte
	if o.sids != nil {
		topDict = append(cffDictInt(topDict, int32(charsetOff)), 15)
	}
	if o.encoding != nil {
		topDict = append(cffDictInt(topDict, int32(encodingOff)), 16)
	}
	topDict = append(cffDictInt(topDict, int32(charStringsOff)), 17)
	topDict = cffDictInt(topDict, int32(len(private)))
	topDict = append(cffDictInt(topDict, int32(privateOff)), 18)
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
	return b
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
	if cffToSFNT(p) == nil {
		t.Fatal("cffToSFNT refused the minimal well-formed bare CFF")
	}
}

// TestType1CGlyphOriginsExact is 4c's origin arithmetic re-run through a bare
// CFF program: /F1 20 Tf, Tm origin (10,50) on a 100x100 page puts the 0.5em
// square at x 10..20, device y 40..50, to the pixel.
func TestType1CGlyphOriginsExact(t *testing.T) {
	src := "BT /F1 20 Tf 1 0 0 1 10 50 Tm (A) Tj ET"
	box := content.Box{URX: 100, URY: 100}
	img, err := Page(context.Background(), []byte(src), box, 1, nil, fontsFor(cffSquareFont()))
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
	img, err := Page(context.Background(), []byte(src), box, 1, nil, fontsFor(f))
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
	img, err := Page(context.Background(), []byte(src), box, 1, nil, fontsFor(f))
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
	img, err := Page(context.Background(), []byte(src), box, 1, nil, fontsFor(f))
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
			img, err := Page(context.Background(), show(tc.code), box, 1, nil, fontsFor(tc.f))
			if err != nil {
				t.Fatalf("Page: %v", err)
			}
			assertExactPixels(t, img, 100, 100, []rect{{10, 40, 20, 50}})
		})
	}
}

// TestHostileCFFSubrBombRefusedBeforeParse: sfnt's own Type 2 limits (subr
// nesting 10, streams 64 KB) do NOT bound total work -- a chain of ten subrs
// each calling the next hundreds of times is a sub-KB font that would execute
// ~200^9 charstring bytes without ever tripping a depth check. The work gate
// must refuse it in bounded time, the font degrades to widths-only, and the
// rest of the page still renders.
func TestHostileCFFSubrBombRefusedBeforeParse(t *testing.T) {
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
	bomb := buildCFF(cffOpts{
		charstrings: [][]byte{t2cs("endchar"), t2cs(-107, "callsubr", "endchar")},
		sids:        []uint16{34},
		lsubrs:      lsubrs,
	})
	if got := cffToSFNT(bomb); got != nil {
		t.Fatal("the subr work bomb was not refused before sfnt.Parse")
	}
	// Through the seam: widths-only, the page still renders.
	f := Font{Program: bomb, FirstChar: 'A', Widths: []float64{600}}
	src := "BT /F1 20 Tf 1 0 0 1 10 50 Tm (A) Tj ET 30 30 10 10 re f"
	box := content.Box{URX: 100, URY: 100}
	img, err := Page(context.Background(), []byte(src), box, 1, nil, fontsFor(f))
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
	// -107 (subr 0 under the bias) as 16.16 fixed: -107<<16 = 0xFF950000.
	entry := t2cs([]byte{255, 0xff, 0x95, 0x00, 0x00}, "callsubr", "endchar")
	bomb := buildCFF(cffOpts{
		charstrings: [][]byte{t2cs("endchar"), entry},
		sids:        []uint16{34},
		lsubrs:      lsubrs,
	})
	if cffToSFNT(bomb) != nil {
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
	if cffToSFNT(f.Program) == nil {
		t.Fatal("depth-limited self-recursion is cheap under sfnt's own wall; the gate must not refuse it")
	}
	src := "BT /F1 20 Tf 1 0 0 1 10 50 Tm (A) Tj ET 30 30 10 10 re f"
	box := content.Box{URX: 100, URY: 100}
	img, err := Page(context.Background(), []byte(src), box, 1, nil, fontsFor(f))
	if err != nil {
		t.Fatalf("Page: %v", err)
	}
	assertExactPixels(t, img, 100, 100, []rect{{30, 60, 40, 70}})
}

// TestCFFCIDKeyedRefused: a CID-keyed CFF (Top DICT ROS) is stage-4d-deferred
// (byb-8b9.8): its codes arrive through a Type0 CMap the showText seam cannot
// carry yet. It must degrade to widths-only, not misrender via a bogus
// code-to-GID guess.
func TestCFFCIDKeyedRefused(t *testing.T) {
	// ROS: SID SID number, operator 12 30.
	ros := append(cffDictInt(cffDictInt(cffDictInt(nil, 391), 392), 0), 12, 30)
	p := buildCFF(cffOpts{
		charstrings: [][]byte{t2cs("endchar"), cffSquare()},
		sids:        []uint16{34},
		topExtra:    ros,
	})
	if cffToSFNT(p) != nil {
		t.Fatal("CID-keyed CFF must be refused until byb-8b9.8 carries its CMap")
	}
}

// TestCFFMalformedNoPanic feeds every truncation and a corruption sweep of a
// well-formed CFF through the wrapper: refuse or accept, but never panic and
// never read out of bounds.
func TestCFFMalformedNoPanic(t *testing.T) {
	good := cffSquareFont().Program
	for i := 0; i <= len(good); i++ {
		cffToSFNT(good[:i:i])
	}
	for i := 0; i < len(good); i++ {
		bad := append([]byte(nil), good...)
		bad[i] ^= 0xff
		cffToSFNT(bad)
	}
}

// TestCFFWorkGateBudgetTrips: with the per-glyph charstring budget lowered
// below even the plain square, the font must degrade to widths-only -- the
// budget is live, not decorative.
func TestCFFWorkGateBudgetTrips(t *testing.T) {
	defer func(old int64) { maxCharstringWork = old }(maxCharstringWork)
	maxCharstringWork = 3

	f := cffSquareFont()
	if cffToSFNT(f.Program) != nil {
		t.Fatal("lowered charstring budget did not refuse the font; the gate is dead")
	}
	src := "BT /F1 20 Tf 1 0 0 1 10 50 Tm (A) Tj ET 30 30 10 10 re f"
	box := content.Box{URX: 100, URY: 100}
	img, err := Page(context.Background(), []byte(src), box, 1, nil, fontsFor(f))
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
	img, err := Page(context.Background(), []byte(src), box, 1, nil, fontsFor(f))
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
	if _, err := Page(context.Background(), []byte(src), box, 1, nil, fontsFor(f)); err != nil {
		// A tripped budget is acceptable; a hang or panic is not, and the
		// test binary's timeout is the harness for those.
		t.Logf("Page returned %v (budget trips are fine)", err)
	}
}
