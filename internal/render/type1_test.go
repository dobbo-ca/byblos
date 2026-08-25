package render

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/dobbo-ca/byblos/internal/content"
)

// ---- minimal Type 1 builder -------------------------------------------------
//
// Hand-assembled like the TrueType and CFF fixtures: the format is old but
// simple, and a hermetic builder can also emit the HOSTILE variants (subr
// bombs, truncated eexec) no legitimate tool will. Layout follows Adobe's
// Type 1 spec (5040): a cleartext PostScript header, an eexec-encrypted
// private section carrying charstring-encrypted Subrs and CharStrings, and a
// 512-zero trailer.

// t1i appends a Type 1 charstring integer operand (5040 section 6.2; unlike
// Type 2, opcode 255 is a full 32-bit integer, not 16.16 fixed).
func t1i(b []byte, v int) []byte {
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
		return append(b, 255, byte(v>>24), byte(v>>16), byte(v>>8), byte(v))
	}
}

// t1cs assembles a Type 1 charstring: ints become operands, strings the named
// operator.
func t1cs(items ...any) []byte {
	ops := map[string][]byte{
		"hstem": {1}, "vstem": {3}, "vmoveto": {4}, "rlineto": {5},
		"hlineto": {6}, "vlineto": {7}, "rrcurveto": {8}, "closepath": {9},
		"callsubr": {10}, "return": {11}, "hsbw": {13}, "endchar": {14},
		"rmoveto": {21}, "hmoveto": {22}, "vhcurveto": {30}, "hvcurveto": {31},
		"dotsection": {12, 0}, "seac": {12, 6}, "div": {12, 12},
		"callothersubr": {12, 16}, "pop": {12, 17}, "setcurrentpoint": {12, 33},
	}
	var b []byte
	for _, it := range items {
		switch v := it.(type) {
		case int:
			b = t1i(b, v)
		case string:
			op, ok := ops[v]
			if !ok {
				panic("t1cs: unknown op " + v)
			}
			b = append(b, op...)
		case []byte:
			b = append(b, v...)
		default:
			panic("t1cs: bad item")
		}
	}
	return b
}

// t1encrypt runs the Type 1 encryption (the inverse of t1Decrypt) over prefix
// then plain; the prefix bytes are the ones decryption discards.
func t1encrypt(key uint16, prefix, plain []byte) []byte {
	data := append(append([]byte{}, prefix...), plain...)
	r := key
	out := make([]byte, len(data))
	for i, p := range data {
		c := p ^ byte(r>>8)
		r = (uint16(c)+r)*52845 + 22719
		out[i] = c
	}
	return out
}

type t1glyph struct {
	name string
	cs   []byte
}

// buildType1 assembles a raw (PFA-structure, binary eexec section) Type 1
// font: exactly the bytes a /FontFile stream carries, with Length1/2/3 the
// three returned section lengths. encoding is the whole /Encoding clause.
func buildType1(glyphs []t1glyph, subrs [][]byte, encoding string) (clear, enc, trailer []byte) {
	clear = []byte("%!PS-AdobeFont-1.0: BbT1 001.001\n" +
		"/FontName /BbT1 def\n" +
		"/FontType 1 def\n" +
		"/FontMatrix [0.001 0 0 0.001 0 0] readonly def\n" +
		"/PaintType 0 def\n" +
		"/FontBBox {0 -200 1000 800} readonly def\n" +
		encoding +
		"currentdict end\ncurrentfile eexec\n")

	var pr []byte
	pr = append(pr, "dup /Private 15 dict dup begin\n"...)
	pr = append(pr, "/RD {string currentfile exch readstring pop} executeonly def\n"...)
	pr = append(pr, "/ND {noaccess def} executeonly def\n"...)
	pr = append(pr, "/NP {noaccess put} executeonly def\n"...)
	pr = append(pr, "/BlueValues [] noaccess def\n"...)
	pr = append(pr, "/MinFeature {16 16} noaccess def\n"...)
	pr = append(pr, "/password 5839 def\n"...)
	pr = append(pr, "/lenIV 4 def\n"...)
	if len(subrs) > 0 {
		pr = append(pr, fmt.Sprintf("/Subrs %d array\n", len(subrs))...)
		for i, s := range subrs {
			e := t1encrypt(4330, []byte("~~~~"), s)
			pr = append(pr, fmt.Sprintf("dup %d %d RD ", i, len(e))...)
			pr = append(pr, e...)
			pr = append(pr, " NP\n"...)
		}
		pr = append(pr, "ND\n"...)
	}
	pr = append(pr, fmt.Sprintf("2 index /CharStrings %d dict dup begin\n", len(glyphs))...)
	for _, g := range glyphs {
		e := t1encrypt(4330, []byte("~~~~"), g.cs)
		pr = append(pr, fmt.Sprintf("/%s %d RD ", g.name, len(e))...)
		pr = append(pr, e...)
		pr = append(pr, " ND\n"...)
	}
	pr = append(pr, "end\nend\nreadonly put\nnoaccess put\ndup /FontName get exch definefont pop\nmark currentfile closefile\n"...)

	// The first four CIPHERTEXT bytes must not all be hex digits, or every
	// consumer (this package's t1Segments included) reads the section as
	// ASCII-hex. Vary the discarded prefix until they are not.
	for p := 0; p < 256; p++ {
		enc = t1encrypt(55665, []byte{'B', 'Y', 'B', byte(p)}, pr)
		allHex := true
		for _, c := range enc[:4] {
			if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F') {
				allHex = false
				break
			}
		}
		if !allHex {
			break
		}
	}

	for i := 0; i < 8; i++ {
		trailer = append(trailer, bytes.Repeat([]byte{'0'}, 64)...)
		trailer = append(trailer, '\n')
	}
	trailer = append(trailer, "cleartomark\n"...)
	return clear, enc, trailer
}

// pfbWrap wraps the three sections as PFB segments (0x80, type, LE length).
func pfbWrap(clear, enc, trailer []byte) []byte {
	seg := func(t byte, d []byte) []byte {
		n := len(d)
		h := []byte{0x80, t, byte(n), byte(n >> 8), byte(n >> 16), byte(n >> 24)}
		return append(h, d...)
	}
	out := seg(1, clear)
	out = append(out, seg(2, enc)...)
	out = append(out, seg(1, trailer)...)
	return append(out, 0x80, 3)
}

const t1StdEnc = "/Encoding StandardEncoding def\n"

// t1SquareFont: 'A' (standard encoding) is the 500x500 square at the origin,
// mirroring testFont and cffSquareFont, as a raw Type 1.
func t1SquareFont() Font {
	square := t1cs(0, 600, "hsbw", 0, 0, "rmoveto", 500, 0, "rlineto",
		0, 500, "rlineto", -500, 0, "rlineto", "closepath", "endchar")
	clear, enc, trailer := buildType1([]t1glyph{
		{".notdef", t1cs(0, 0, "hsbw", "endchar")},
		{"A", square},
	}, nil, t1StdEnc)
	raw := append(append(append([]byte{}, clear...), enc...), trailer...)
	return Font{Program: raw, FirstChar: 'A', Widths: []float64{600}}
}

// TestType1ProgramTakesTheType1Path pins WHICH path the fixtures exercise: a
// classic Type 1 is invisible to both the TrueType glyf index and the 4d CFF
// wrapper, and is accepted by parseType1 -- in raw form and PFB-wrapped.
func TestType1ProgramTakesTheType1Path(t *testing.T) {
	raw := t1SquareFont().Program
	if parseGlyfIndex(raw) != nil {
		t.Fatal("Type 1 parsed as an indexable TrueType; the fixture does not exercise the 4e path")
	}
	if otf, _, _ := cffToSFNT(raw); otf != nil {
		t.Fatal("Type 1 parsed as a bare CFF; the fixture does not exercise the 4e path")
	}
	f := parseType1(raw)
	if f == nil {
		t.Fatal("parseType1 refused the minimal well-formed raw Type 1")
	}
	if f.glyphs["A"] == nil {
		t.Fatal("charstring /A missing after parse")
	}
	clear, enc, trailer := buildType1([]t1glyph{
		{".notdef", t1cs(0, 0, "hsbw", "endchar")},
		{"A", t1cs(0, 600, "hsbw", "endchar")},
	}, nil, t1StdEnc)
	pf := parseType1(pfbWrap(clear, enc, trailer))
	if pf == nil || pf.glyphs["A"] == nil {
		t.Fatal("parseType1 refused the PFB-wrapped form")
	}
}

// TestType1GlyphOriginsExact re-runs 4c's origin arithmetic through a Type 1
// program: /F1 20 Tf, Tm origin (10,50) on a 100x100 page puts the 0.5em
// square at x 10..20, device y 40..50, to the pixel -- which also pins the
// y-UP orientation of Type 1 charstring space (a flipped glyph lands at
// y 50..60 and fails).
func TestType1GlyphOriginsExact(t *testing.T) {
	src := "BT /F1 20 Tf 1 0 0 1 10 50 Tm (A) Tj ET"
	box := content.Box{URX: 100, URY: 100}
	img, err := Page(context.Background(), []byte(src), box, 0, 1, nil, fontsFor(t1SquareFont()))
	if err != nil {
		t.Fatalf("Page: %v", err)
	}
	assertExactPixels(t, img, 100, 100, []rect{{10, 40, 20, 50}})
}

// TestType1CurveAndSubrRender: the same square drawn with every edge a
// DEGENERATE rrcurveto (control points collinear), the right and top edges
// through a Subr called twice with div-computed operands. One pixel-exact
// footprint pins curves, callsubr/return, and div together.
func TestType1CurveAndSubrRender(t *testing.T) {
	// subr 0 draws one straight 500-long edge as a cubic whose direction is
	// (dx, dy) taken from the stack before the call.
	edge := func(b []byte, dx, dy int) []byte {
		return t1cs(b, dx/2, dy/2, dx/4, dy/4, dx/4, dy/4, "rrcurveto")
	}
	sub := edge(nil, 0, 500) // vertical edge
	sub = append(sub, t1cs("return")...)
	cs := t1cs(0, 600, "hsbw", 0, 0, "rmoveto")
	cs = edge(cs, 500, 0)                        // bottom, inline
	cs = t1cs(cs, 0, "callsubr")                 // right, via subr
	cs = t1cs(cs, -1000, 2, "div", 0, "rlineto") // top edge, dx = -1000/2 via div
	cs = t1cs(cs, "closepath", "endchar")
	clear, enc, trailer := buildType1([]t1glyph{
		{".notdef", t1cs(0, 0, "hsbw", "endchar")},
		{"A", cs},
	}, [][]byte{sub}, t1StdEnc)
	raw := append(append(append([]byte{}, clear...), enc...), trailer...)
	f := Font{Program: raw, FirstChar: 'A', Widths: []float64{600}}
	src := "BT /F1 20 Tf 1 0 0 1 10 50 Tm (A) Tj ET"
	box := content.Box{URX: 100, URY: 100}
	img, err := Page(context.Background(), []byte(src), box, 0, 1, nil, fontsFor(f))
	if err != nil {
		t.Fatalf("Page: %v", err)
	}
	assertExactPixels(t, img, 100, 100, []rect{{10, 40, 20, 50}})
}

// TestType1FlexCollapsesToCurves: a square whose top edge is drawn by the
// standard flex othersubr protocol (othersubrs 1, 2, 0 called inline), with
// every flex point on the line y=500 -- the two emitted cubics must reproduce
// the exact square footprint, and the trailing pop/pop/setcurrentpoint must
// not disturb the current point.
func TestType1FlexCollapsesToCurves(t *testing.T) {
	cs := t1cs(0, 600, "hsbw", 0, 0, "rmoveto",
		500, 0, "rlineto", 0, 500, "rlineto",
		// flex start
		0, 1, "callothersubr",
		// reference point then six control points, all collected by rmoveto
		-50, 0, "rmoveto", 0, 2, "callothersubr",
		-50, 0, "rmoveto", 0, 2, "callothersubr",
		-100, 0, "rmoveto", 0, 2, "callothersubr",
		-50, 0, "rmoveto", 0, 2, "callothersubr",
		-50, 0, "rmoveto", 0, 2, "callothersubr",
		-100, 0, "rmoveto", 0, 2, "callothersubr",
		-100, 0, "rmoveto", 0, 2, "callothersubr",
		// flex end: flex height, end x, end y
		50, 0, 500, 3, 0, "callothersubr",
		"pop", "pop", "setcurrentpoint",
		"closepath", "endchar")
	clear, enc, trailer := buildType1([]t1glyph{
		{".notdef", t1cs(0, 0, "hsbw", "endchar")},
		{"A", cs},
	}, nil, t1StdEnc)
	raw := append(append(append([]byte{}, clear...), enc...), trailer...)
	f := Font{Program: raw, FirstChar: 'A', Widths: []float64{600}}
	src := "BT /F1 20 Tf 1 0 0 1 10 50 Tm (A) Tj ET"
	box := content.Box{URX: 100, URY: 100}
	img, err := Page(context.Background(), []byte(src), box, 0, 1, nil, fontsFor(f))
	if err != nil {
		t.Fatalf("Page: %v", err)
	}
	assertExactPixels(t, img, 100, 100, []rect{{10, 40, 20, 50}})
}

// TestType1CustomEncoding: a font whose builtin encoding maps code 65 to a
// custom-named glyph through "dup 65 /box put" -- the non-StandardEncoding
// branch of the encoding parser.
func TestType1CustomEncoding(t *testing.T) {
	square := t1cs(0, 600, "hsbw", 0, 0, "rmoveto", 500, 0, "rlineto",
		0, 500, "rlineto", -500, 0, "rlineto", "closepath", "endchar")
	encoding := "/Encoding 256 array\n0 1 255 {1 index exch /.notdef put} for\n" +
		"dup 65 /box put\nreadonly def\n"
	clear, enc, trailer := buildType1([]t1glyph{
		{".notdef", t1cs(0, 0, "hsbw", "endchar")},
		{"box", square},
	}, nil, encoding)
	raw := append(append(append([]byte{}, clear...), enc...), trailer...)
	f := Font{Program: raw, FirstChar: 'A', Widths: []float64{600}}
	src := "BT /F1 20 Tf 1 0 0 1 10 50 Tm (A) Tj ET"
	box := content.Box{URX: 100, URY: 100}
	img, err := Page(context.Background(), []byte(src), box, 0, 1, nil, fontsFor(f))
	if err != nil {
		t.Fatalf("Page: %v", err)
	}
	assertExactPixels(t, img, 100, 100, []rect{{10, 40, 20, 50}})
}

// TestHostileType1SubrBombBudgeted: a 3-level chain of subrs each calling the
// next 200 times executes ~200^3 charstring bytes from a sub-KB font. The
// per-glyph work budget must ABANDON the glyph (interpret reports the skip),
// having charged at most maxCharstringWork -- so the guard's removal makes
// the interpreter run the chain to completion and this test fail, rather
// than hang.
func TestHostileType1SubrBombBudgeted(t *testing.T) {
	const chain = 3
	subrs := make([][]byte, chain)
	for i := 0; i < chain; i++ {
		if i == chain-1 {
			subrs[i] = t1cs("return")
			continue
		}
		var s []byte
		for j := 0; j < 200; j++ {
			s = t1cs(s, i+1, "callsubr")
		}
		subrs[i] = t1cs(s, "return")
	}
	f := &t1Font{subrs: subrs, upem: 1000}
	var work int64
	err := f.interpret(t1cs(0, 600, "hsbw", 0, "callsubr", "endchar"), t1Sink{
		move:  func(x, y float64) error { return nil },
		line:  func(x, y float64) error { return nil },
		curve: func(x1, y1, x2, y2, x3, y3 float64) error { return nil },
	}, &work)
	if err == nil {
		t.Fatal("the subr work bomb ran to completion; the per-glyph budget is not enforced")
	}
	if work > maxCharstringWork+8 {
		t.Fatalf("interpreter charged %d work; the budget must stop it at ~%d", work, maxCharstringWork)
	}
	// Through the seam: the glyph skips cleanly and the rest of the page
	// still renders.
	clear, enc, trailer := buildType1([]t1glyph{
		{"A", t1cs(0, 600, "hsbw", 0, "callsubr", "endchar")},
	}, subrs, t1StdEnc)
	raw := append(append(append([]byte{}, clear...), enc...), trailer...)
	font := Font{Program: raw, FirstChar: 'A', Widths: []float64{600}}
	src := "BT /F1 20 Tf 1 0 0 1 10 50 Tm (A) Tj ET 30 30 10 10 re f"
	box := content.Box{URX: 100, URY: 100}
	img, perr := Page(context.Background(), []byte(src), box, 0, 1, nil, fontsFor(font))
	if perr != nil {
		t.Fatalf("Page: %v", perr)
	}
	assertExactPixels(t, img, 100, 100, []rect{{30, 60, 40, 70}})
}

// TestHostileType1SelfRecursionBounded: a subr that calls itself forever must
// hit the depth wall almost immediately -- bounded work, glyph skipped, page
// intact.
func TestHostileType1SelfRecursionBounded(t *testing.T) {
	f := &t1Font{subrs: [][]byte{t1cs(0, "callsubr", "return")}, upem: 1000}
	var work int64
	err := f.interpret(t1cs(0, 600, "hsbw", 0, "callsubr", "endchar"), t1Sink{
		move:  func(x, y float64) error { return nil },
		line:  func(x, y float64) error { return nil },
		curve: func(x1, y1, x2, y2, x3, y3 float64) error { return nil },
	}, &work)
	if err == nil {
		t.Fatal("unbounded self-recursion was not stopped")
	}
	if work > 100 {
		t.Fatalf("self-recursion charged %d work before the depth wall; expected a few dozen bytes", work)
	}
}

// TestType1WorkChargedPerShow pins the re-show amplification guard: a glyph
// whose charstring is a ~60000-byte hint sled (zero segments, so the
// per-point charge prices it at nothing) must charge its interpreter work
// against maxFillWork on EVERY show, so a stream re-showing it forever stays
// bounded per Page. Mirrors TestCFFZeroSegmentGlyphChargesFillWork.
func TestType1WorkChargedPerShow(t *testing.T) {
	defer func(old int64) { maxFillWork = old }(maxFillWork)
	maxFillWork = 100_000

	sled := t1cs(0, 600, "hsbw")
	sled = append(sled, bytes.Repeat([]byte{1}, 60_000)...) // hstem sled
	sled = append(sled, t1cs("endchar")...)
	clear, enc, trailer := buildType1([]t1glyph{{"A", sled}}, nil, t1StdEnc)
	raw := append(append(append([]byte{}, clear...), enc...), trailer...)
	f := Font{Program: raw, FirstChar: 'A', Widths: []float64{600}}
	src := "BT /F1 20 Tf 1 0 0 1 10 50 Tm (AA) Tj ET"
	box := content.Box{URX: 100, URY: 100}
	_, err := Page(context.Background(), []byte(src), box, 0, 1, nil, fontsFor(f))
	if err == nil || !strings.Contains(err.Error(), "fill work exceeds") {
		t.Fatalf("two shows of a ~60000-work zero-segment glyph under a 100000 budget must trip fill work, got %v", err)
	}
}

// TestType1MalformedNoPanic: truncations and mutations of a valid font must
// degrade to widths-only or skipped glyphs, never panic -- the same stance as
// TestCFFMalformedNoPanic.
func TestType1MalformedNoPanic(t *testing.T) {
	good := t1SquareFont().Program
	cases := [][]byte{
		nil,
		[]byte("%!"),
		[]byte("%!PS-AdobeFont eexec"),
		good[:40],
		good[:len(good)/2],
		bytes.Replace(good, []byte("/lenIV 4"), []byte("/lenIV 99"), 1),
		bytes.Replace(good, []byte("/FontMatrix [0.001 0 0 0.001 0 0]"),
			[]byte("/FontMatrix [0.001 0.5 0 0.001 0 0]"), 1),
		pfbWrap(nil, nil, nil),
		{0x80, 1, 255, 255, 255, 255},
	}
	src := "BT /F1 20 Tf 1 0 0 1 10 50 Tm (A) Tj ET"
	box := content.Box{URX: 100, URY: 100}
	for i, p := range cases {
		f := Font{Program: p, FirstChar: 'A', Widths: []float64{600}}
		if _, err := Page(context.Background(), []byte(src), box, 0, 1, nil, fontsFor(f)); err != nil {
			t.Fatalf("case %d: Page: %v", i, err)
		}
	}
}

// TestType1SeacDeferred: seac (accented composites) is DEFERRED -- the glyph
// must SKIP, abandoning even outline drawn before the seac (a partial base
// glyph would misrender), and the rest of the page still renders. The square
// drawn before the seac distinguishes the skip from a silent no-op that would
// keep interpreting and fill the partial outline.
func TestType1SeacDeferred(t *testing.T) {
	cs := t1cs(0, 600, "hsbw", 0, 0, "rmoveto", 500, 0, "rlineto",
		0, 500, "rlineto", -500, 0, "rlineto", "closepath",
		0, 65, 65, 193, "seac")
	clear, enc, trailer := buildType1([]t1glyph{{"A", cs}}, nil, t1StdEnc)
	raw := append(append(append([]byte{}, clear...), enc...), trailer...)
	f := Font{Program: raw, FirstChar: 'A', Widths: []float64{600}}
	src := "BT /F1 20 Tf 1 0 0 1 10 50 Tm (A) Tj ET 30 30 10 10 re f"
	box := content.Box{URX: 100, URY: 100}
	img, err := Page(context.Background(), []byte(src), box, 0, 1, nil, fontsFor(f))
	if err != nil {
		t.Fatalf("Page: %v", err)
	}
	assertExactPixels(t, img, 100, 100, []rect{{30, 60, 40, 70}})
}

// t1OraclePDF embeds a three-glyph Type 1 as /FontFile -- 'A' a square, 'B' a
// triangle, 'C' a diamond of four cubics, the same shapes as the TrueType and
// CFF oracles -- showing the shared textOracleContent.
func t1OraclePDF() ([]byte, Font) {
	square := t1cs(0, 600, "hsbw", 0, 0, "rmoveto", 600, 0, "rlineto",
		0, 500, "rlineto", -600, 0, "rlineto", "closepath", "endchar")
	triangle := t1cs(0, 700, "hsbw", 0, 0, "rmoveto", 600, 0, "rlineto",
		-300, 500, "rlineto", "closepath", "endchar")
	diamond := t1cs(0, 550, "hsbw", 250, 0, "rmoveto",
		150, 0, 100, 100, 0, 150, "rrcurveto",
		0, 150, -100, 100, -150, 0, "rrcurveto",
		-150, 0, -100, -100, 0, -150, "rrcurveto",
		0, -150, 100, -100, 150, 0, "rrcurveto",
		"closepath", "endchar")
	clear, enc, trailer := buildType1([]t1glyph{
		{".notdef", t1cs(0, 0, "hsbw", "endchar")},
		{"A", square}, {"B", triangle}, {"C", diamond},
	}, nil, t1StdEnc)
	raw := append(append(append([]byte{}, clear...), enc...), trailer...)
	pdf := wrapPDF([]string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 200 200]" +
			" /Resources << /Font << /F1 5 0 R >> >> /Contents 4 0 R >>",
		fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(textOracleContent), textOracleContent),
		"<< /Type /Font /Subtype /Type1 /BaseFont /BbT1 /FirstChar 65" +
			" /LastChar 67 /Widths [600 700 550] /FontDescriptor 6 0 R >>",
		"<< /Type /FontDescriptor /FontName /BbT1 /Flags 32" +
			" /FontBBox [0 -200 1000 800] /ItalicAngle 0 /Ascent 800" +
			" /Descent -200 /CapHeight 500 /StemV 80 /FontFile 7 0 R >>",
		fmt.Sprintf("<< /Length %d /Length1 %d /Length2 %d /Length3 %d >>\nstream\n%s\nendstream",
			len(raw), len(clear), len(enc), len(trailer), raw),
	})
	return pdf, Font{Program: raw, FirstChar: 65, Widths: []float64{600, 700, 550}}
}

// TestType1TextAgreesWithPdftoppm is byb-8b9.5's acceptance: a page of classic
// Type 1 text -- which stages 4c and 4d could not render at all -- rasterises
// within tolerance of pdftoppm, and the blank null must fail the metric.
// TestType1ProgramTakesTheType1Path pins that these bytes go down the 4e path
// and no other.
func TestType1TextAgreesWithPdftoppm(t *testing.T) {
	pdf, font := t1OraclePDF()
	oracle := pdftoppmPNG(t, pdf)

	fonts := func(name string) (Font, bool) {
		if name != "F1" {
			return Font{}, false
		}
		return font, true
	}
	box := content.Box{URX: 200, URY: 200}
	got, err := Page(context.Background(), []byte(textOracleContent), box, 0, 1, nil, fonts)
	if err != nil {
		t.Fatalf("Page: %v", err)
	}
	const tolerance = 0.05
	frac := mismatchFraction(t, got, oracle)
	t.Logf("Type 1 text mismatch vs pdftoppm: %.2f%% of pixels", frac*100)
	if frac > tolerance {
		t.Errorf("Type 1 text page disagrees with pdftoppm on %.1f%% of pixels; tolerance %.0f%%",
			frac*100, tolerance*100)
	}

	blank, err := Page(context.Background(), nil, box, 0, 1, nil, nil)
	if err != nil {
		t.Fatalf("Page(blank): %v", err)
	}
	if frac := mismatchFraction(t, blank, oracle); frac <= tolerance {
		t.Errorf("a BLANK raster is within tolerance of pdftoppm (%.1f%% mismatch); the Type 1 oracle metric is broken", frac*100)
	}
}

// t1CaptureSink records every sink call for coordinate-convention tests.
type t1CaptureSink struct{ calls []string }

func (c *t1CaptureSink) sink() t1Sink {
	f := func(op string) func(x, y float64) error {
		return func(x, y float64) error {
			c.calls = append(c.calls, fmt.Sprintf("%s %g %g", op, x, y))
			return nil
		}
	}
	return t1Sink{
		move: f("move"), line: f("line"),
		curve: func(x1, y1, x2, y2, x3, y3 float64) error {
			c.calls = append(c.calls, fmt.Sprintf("curve %g %g %g %g %g %g", x1, y1, x2, y2, x3, y3))
			return nil
		},
	}
}

// TestType1CurveOperatorConventions pins the operand conventions of vhcurveto
// (dy1 dx2 dy2 dx3: leaves vertically, arrives horizontally) and hvcurveto
// (dx1 dx2 dy2 dy3: the mirror) -- the two curve operators real Type 1 fonts
// use most. Swapping the two branches, or any operand, changes a coordinate.
func TestType1CurveOperatorConventions(t *testing.T) {
	f := &t1Font{upem: 1000}
	var c t1CaptureSink
	var work int64
	cs := t1cs(0, 600, "hsbw", 100, 100, "rmoveto",
		10, 20, 30, 40, "vhcurveto",
		1, 2, 3, 4, "hvcurveto",
		"endchar")
	if err := f.interpret(cs, c.sink(), &work); err != nil {
		t.Fatalf("interpret: %v", err)
	}
	want := []string{
		"move 100 100",
		"curve 100 110 120 140 160 140", // vh: up then across
		"curve 161 140 163 143 163 147", // hv: across then up
	}
	if len(c.calls) != len(want) {
		t.Fatalf("sink calls: got %v, want %v", c.calls, want)
	}
	for i := range want {
		if c.calls[i] != want[i] {
			t.Errorf("call %d: got %q, want %q", i, c.calls[i], want[i])
		}
	}
}

// TestType1HintReplacementFeedsPop pins the non-flex callothersubr path
// (othersubr 3, hint replacement -- present in nearly every Adobe-era Type 1):
// the argument must feed the PostScript stack so the following pop retrieves
// it (in production, the subr number that callsubr then executes). Dropping
// the args would make pop push 0 instead.
func TestType1HintReplacementFeedsPop(t *testing.T) {
	f := &t1Font{upem: 1000}
	var c t1CaptureSink
	var work int64
	cs := t1cs(0, 600, "hsbw", 0, 0, "rmoveto",
		42, 1, 3, "callothersubr", "pop", 0, "rlineto", "endchar")
	if err := f.interpret(cs, c.sink(), &work); err != nil {
		t.Fatalf("interpret: %v", err)
	}
	want := []string{"move 0 0", "line 42 0"}
	if len(c.calls) != 2 || c.calls[0] != want[0] || c.calls[1] != want[1] {
		t.Fatalf("sink calls: got %v, want %v (othersubr 3's arg must survive to pop)", c.calls, want)
	}
}

// TestType1HexEexec: the ASCII-hex eexec form (the PFA convention) must parse
// to the same glyphs as the binary form.
func TestType1HexEexec(t *testing.T) {
	square := t1cs(0, 600, "hsbw", 0, 0, "rmoveto", 500, 0, "rlineto",
		0, 500, "rlineto", -500, 0, "rlineto", "closepath", "endchar")
	clear, enc, trailer := buildType1([]t1glyph{
		{".notdef", t1cs(0, 0, "hsbw", "endchar")},
		{"A", square},
	}, nil, t1StdEnc)
	var hexed []byte
	for i, b := range enc {
		hexed = append(hexed, "0123456789abcdef"[b>>4], "0123456789abcdef"[b&0xf])
		if i%32 == 31 {
			hexed = append(hexed, '\n')
		}
	}
	raw := append(append(append([]byte{}, clear...), hexed...), trailer...)
	f := parseType1(raw)
	if f == nil {
		t.Fatal("parseType1 refused the hex-eexec form")
	}
	if !bytes.Equal(f.glyphs["A"], square) {
		t.Fatalf("hex-eexec /A decodes to %d bytes, want the %d-byte charstring", len(f.glyphs["A"]), len(square))
	}
}

// TestHostileType1SubrCountBounded: a /Subrs count no remaining input could
// satisfy (each entry costs at least 8 bytes) must be refused before its
// slice-header preallocation -- a declared count of millions with zero entries
// behind it would otherwise retain ~24 bytes of live heap per declared subr
// for the life of the page.
func TestHostileType1SubrCountBounded(t *testing.T) {
	// 500 declared subrs, zero entries, padded so the count is below len(d)
	// but far above what the remaining bytes could hold.
	hostile := append([]byte("/Subrs 500 "), bytes.Repeat([]byte{' '}, 3000)...)
	if s := t1ParseSubrs(hostile, 4); s != nil {
		t.Fatalf("an unbacked /Subrs 500 preallocated %d slice headers; it must be refused", len(s))
	}
	// A count the input does back still parses.
	s := t1ParseSubrs([]byte("/Subrs 1 dup 0 5 RD aaaaa NP end more padding"), 4)
	if len(s) != 1 || s[0] == nil {
		t.Fatalf("the well-formed 1-entry /Subrs failed to parse: %v", s)
	}
}

// TestType1NoWidthsUsesHsbwAdvance: a /FontFile Type 1 dict with NO /Widths
// must advance by each charstring's own hsbw width (600 here -> 12px at 20pt),
// not 0 -- two 'A's land side by side, not stacked.
func TestType1NoWidthsUsesHsbwAdvance(t *testing.T) {
	f := t1SquareFont()
	f.FirstChar, f.Widths = 0, nil
	src := "BT /F1 20 Tf 1 0 0 1 10 50 Tm (AA) Tj ET"
	box := content.Box{URX: 100, URY: 100}
	img, err := Page(context.Background(), []byte(src), box, 0, 1, nil, fontsFor(f))
	if err != nil {
		t.Fatalf("Page: %v", err)
	}
	assertExactPixels(t, img, 100, 100, []rect{{10, 40, 20, 50}, {22, 40, 32, 50}})
}
