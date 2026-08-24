package render

import (
	"context"
	"encoding/binary"
	"image/color"
	"strings"
	"testing"

	"github.com/dobbo-ca/byblos/internal/content"
)

// ---- minimal TrueType builder ----------------------------------------------
//
// The same hand-assembled-sfnt idiom as internal/glyphless/gen.go, extended
// with real glyf outlines: gen.go cannot be imported (//go:build ignore), and
// glyphless.ttf's whole point is that its glyphs are EMPTY, which is exactly
// what a glyph-rendering test cannot use. Every field the two share follows
// gen.go's comments.

// gpt is one glyf contour point in font units. off marks an off-curve
// (quadratic control) point.
type gpt struct {
	x, y int16
	off  bool
}

func tbe16(buf []byte, v uint16) []byte { return append(buf, byte(v>>8), byte(v)) }
func tbe32(buf []byte, v uint32) []byte {
	return append(buf, byte(v>>24), byte(v>>16), byte(v>>8), byte(v))
}

// buildGlyf encodes one simple glyph: contours of absolute points, flags
// uncompressed, every coordinate a full int16 delta.
func buildGlyf(contours [][]gpt) []byte {
	var b []byte
	b = tbe16(b, uint16(int16(len(contours))))
	minX, minY, maxX, maxY := int16(32767), int16(32767), int16(-32768), int16(-32768)
	var pts []gpt
	var ends []uint16
	for _, c := range contours {
		pts = append(pts, c...)
		ends = append(ends, uint16(len(pts)-1))
		for _, p := range c {
			minX, minY = min(minX, p.x), min(minY, p.y)
			maxX, maxY = max(maxX, p.x), max(maxY, p.y)
		}
	}
	b = tbe16(b, uint16(minX))
	b = tbe16(b, uint16(minY))
	b = tbe16(b, uint16(maxX))
	b = tbe16(b, uint16(maxY))
	for _, e := range ends {
		b = tbe16(b, e)
	}
	b = tbe16(b, 0) // instructionLength
	for _, p := range pts {
		if p.off {
			b = append(b, 0x00)
		} else {
			b = append(b, 0x01) // on-curve
		}
	}
	var px, py int16
	for _, p := range pts {
		b = tbe16(b, uint16(p.x-px))
		px = p.x
	}
	for _, p := range pts {
		b = tbe16(b, uint16(p.y-py))
		py = p.y
	}
	for len(b)%2 != 0 { // short loca stores offset/2: glyph data must stay even
		b = append(b, 0)
	}
	return b
}

// buildTTF assembles a TrueType font with unitsPerEm 1000: glyphs[i] becomes
// glyph i+1 mapped from rune firstRune+i (one contiguous cmap format-4
// segment, gen.go's idDelta trick), with hmtx advance advances[i]. Glyph 0
// (.notdef) is empty.
func buildTTF(firstRune rune, glyphs [][][]gpt, advances []uint16) []byte {
	const unitsPerEm = 1000
	numGlyphs := len(glyphs) + 1

	var glyf []byte
	loca := tbe16(nil, 0) // .notdef: empty, offset 0
	loca = tbe16(loca, 0)
	for _, g := range glyphs {
		glyf = append(glyf, buildGlyf(g)...)
		loca = tbe16(loca, uint16(len(glyf)/2))
	}

	var head []byte
	head = tbe32(head, 0x00010000)
	head = tbe32(head, 0x00010000)
	head = tbe32(head, 0) // checkSumAdjustment: left 0, no reader here checks it
	head = tbe32(head, 0x5F0F3CF5)
	head = tbe16(head, 0x0003)
	head = tbe16(head, unitsPerEm)
	descent := int16(-200)
	head = append(head, make([]byte, 16)...) // created/modified
	head = tbe16(head, 0)                    // xMin
	head = tbe16(head, uint16(descent))      // yMin
	head = tbe16(head, unitsPerEm)           // xMax
	head = tbe16(head, 800)                  // yMax
	head = tbe16(head, 0)                    // macStyle
	head = tbe16(head, 8)                    // lowestRecPPEM
	head = tbe16(head, 2)                    // fontDirectionHint
	head = tbe16(head, 0)                    // indexToLocFormat: short
	head = tbe16(head, 0)                    // glyphDataFormat

	hhea := buildHheaTable(numGlyphs)

	var maxp []byte
	maxp = tbe32(maxp, 0x00010000)
	maxp = tbe16(maxp, uint16(numGlyphs))
	maxp = tbe16(maxp, 4096) // maxPoints
	maxp = tbe16(maxp, 2048) // maxContours
	maxp = tbe16(maxp, 0)
	maxp = tbe16(maxp, 0)
	maxp = tbe16(maxp, 2) // maxZones
	for i := 0; i < 8; i++ {
		maxp = tbe16(maxp, 0)
	}

	var hmtx []byte
	hmtx = tbe16(hmtx, 0) // .notdef advance
	hmtx = tbe16(hmtx, 0)
	for _, a := range advances {
		hmtx = tbe16(hmtx, a)
		hmtx = tbe16(hmtx, 0)
	}

	lastRune := firstRune + rune(len(glyphs)) - 1
	var sub []byte
	sub = tbe16(sub, 4) // format
	sub = tbe16(sub, 0) // length, patched
	sub = tbe16(sub, 0) // language
	sub = tbe16(sub, 4) // segCountX2 (2 segments)
	sub = tbe16(sub, 4) // searchRange
	sub = tbe16(sub, 1) // entrySelector
	sub = tbe16(sub, 0) // rangeShift
	sub = tbe16(sub, uint16(lastRune))
	sub = tbe16(sub, 0xFFFF)
	sub = tbe16(sub, 0) // reservedPad
	sub = tbe16(sub, uint16(firstRune))
	sub = tbe16(sub, 0xFFFF)
	sub = tbe16(sub, uint16(int16(1-firstRune))) // idDelta: glyphID = rune - firstRune + 1
	sub = tbe16(sub, 1)
	sub = tbe16(sub, 0) // idRangeOffset x2
	sub = tbe16(sub, 0)
	binary.BigEndian.PutUint16(sub[2:4], uint16(len(sub)))
	var cmap []byte
	cmap = tbe16(cmap, 0) // version
	cmap = tbe16(cmap, 1) // numTables
	cmap = tbe16(cmap, 3) // platformID Windows
	cmap = tbe16(cmap, 1) // encodingID Unicode BMP
	cmap = tbe32(cmap, 12)
	cmap = append(cmap, sub...)

	var post []byte
	post = tbe32(post, 0x00030000)
	post = append(post, make([]byte, 28)...)

	tables := []struct {
		tag  string
		data []byte
	}{
		{"cmap", cmap}, {"glyf", glyf}, {"head", head}, {"hhea", hhea},
		{"hmtx", hmtx}, {"loca", loca}, {"maxp", maxp}, {"post", post},
	}
	numTables := uint16(len(tables))
	entrySelector := uint16(0)
	for (1 << (entrySelector + 1)) <= int(numTables) {
		entrySelector++
	}
	searchRange := uint16(1<<entrySelector) * 16

	var out []byte
	out = tbe32(out, 0x00010000)
	out = tbe16(out, numTables)
	out = tbe16(out, searchRange)
	out = tbe16(out, entrySelector)
	out = tbe16(out, numTables*16-searchRange)
	dirStart := len(out)
	out = append(out, make([]byte, int(numTables)*16)...)
	for i, tbl := range tables {
		off := uint32(len(out))
		entry := dirStart + i*16
		copy(out[entry:entry+4], tbl.tag)
		binary.BigEndian.PutUint32(out[entry+8:entry+12], off)
		binary.BigEndian.PutUint32(out[entry+12:entry+16], uint32(len(tbl.data)))
		out = append(out, tbl.data...)
		for len(out)%4 != 0 {
			out = append(out, 0)
		}
	}
	return out
}

func buildHheaTable(numGlyphs int) []byte {
	var b []byte
	descent := int16(-200)
	b = tbe32(b, 0x00010000)
	b = tbe16(b, 800)             // ascender
	b = tbe16(b, uint16(descent)) // descender
	b = tbe16(b, 0)               // lineGap
	b = tbe16(b, 1000)            // advanceWidthMax
	b = tbe16(b, 0)               // minLeftSideBearing
	b = tbe16(b, 0)               // minRightSideBearing
	b = tbe16(b, 0)               // xMaxExtent
	b = tbe16(b, 1)               // caretSlopeRise
	b = tbe16(b, 0)               // caretSlopeRun
	b = tbe16(b, 0)               // caretOffset
	for i := 0; i < 4; i++ {
		b = tbe16(b, 0) // reserved
	}
	b = tbe16(b, 0) // metricDataFormat
	b = tbe16(b, uint16(numGlyphs))
	return b
}

// squareGlyph is a 500x500 square at the glyph origin: axis-aligned, so its
// device footprint under any translation+scale is hand-computable to the
// pixel.
func squareGlyph() [][]gpt {
	return [][]gpt{{{0, 0, false}, {500, 0, false}, {500, 500, false}, {0, 500, false}}}
}

// testFont: 'A' is the square glyph, advance 600 (font units AND the PDF
// /Widths value in thousandths -- unitsPerEm is 1000 so the two scales agree).
func testFont() Font {
	return Font{
		Program:   buildTTF('A', [][][]gpt{squareGlyph()}, []uint16{600}),
		FirstChar: 'A',
		Widths:    []float64{600},
	}
}

func fontsFor(f Font) FontFor {
	return func(name string) (Font, bool) {
		if name == "F1" {
			return f, true
		}
		return Font{}, false
	}
}

// rect is a device-pixel rectangle, half-open.
type rect struct{ x0, y0, x1, y1 int }

// assertExactPixels requires black exactly inside the union of rects and
// white everywhere else.
func assertExactPixels(t *testing.T, img interface {
	At(x, y int) color.Color
}, w, h int, rects []rect) {
	t.Helper()
	bad := 0
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			want := false
			for _, rc := range rects {
				if x >= rc.x0 && x < rc.x1 && y >= rc.y0 && y < rc.y1 {
					want = true
					break
				}
			}
			r, g, b, _ := img.At(x, y).RGBA()
			got := r == 0 && g == 0 && b == 0
			if got != want {
				if bad < 5 {
					t.Errorf("pixel (%d,%d): inked=%v want %v", x, y, got, want)
				}
				bad++
			}
		}
	}
	if bad > 0 {
		t.Fatalf("%d pixels differ from the hand-computed glyph footprints", bad)
	}
}

// TestTextGlyphOriginsExact is byb-8b9.3's first acceptance clause: the
// device footprint of every glyph in a Tm/Td/TJ sequence matches the
// hand-computed origins exactly. The glyph is an axis-aligned 0.5em square,
// so the scanline fill is pixel-exact and any origin error moves whole
// pixels.
//
// Hand computation, 100x100 page at scale 1 (device y = 100 - user y),
// /F1 at 20pt so the square is 10x10pt with advance 12pt:
//
//	glyph 1: Tm origin (10,50)                    -> x 10..20, user y 50..60
//	glyph 2: +advance 12                          -> x 22..32
//	glyph 3: TJ -500 = +500/1000*20 = +10 further -> x 44..54
//	glyph 4: 0 -30 Td from line start (10,50)     -> (10,20), user y 20..30
func TestTextGlyphOriginsExact(t *testing.T) {
	src := "BT /F1 20 Tf 1 0 0 1 10 50 Tm [(AA) -500 (A)] TJ 0 -30 Td (A) Tj ET"
	box := content.Box{URX: 100, URY: 100}
	img, err := Page(context.Background(), []byte(src), box, 1, nil, fontsFor(testFont()))
	if err != nil {
		t.Fatalf("Page: %v", err)
	}
	assertExactPixels(t, img, 100, 100, []rect{
		{10, 40, 20, 50},
		{22, 40, 32, 50},
		{44, 40, 54, 50},
		{10, 70, 20, 80},
	})
}

// TestTextSpacingParamsExact pins Tc, Tw, Tz and Ts, each hand-computed:
//
//	line 1 (Tm origin (10,80), Tc 2, Tw 4): "A A"
//	  glyph 1 at x 10; advance 12+2=14; space (code 32, outside /Widths,
//	  MissingWidth 0) advances 0+2+4=6 -> glyph 2 at x 30
//	line 2 (Td to (10,40), 50 Tz, 5 Ts): "AA", squares 5pt wide, raised 5
//	  glyph 3 at x 10, user y 45..55; advance (12+2)*0.5=7 -> glyph 4 at x 17
func TestTextSpacingParamsExact(t *testing.T) {
	src := "BT /F1 20 Tf 4 Tw 2 Tc 1 0 0 1 10 80 Tm (A A) Tj 50 Tz 5 Ts 0 -40 Td (AA) Tj ET"
	box := content.Box{URX: 100, URY: 100}
	img, err := Page(context.Background(), []byte(src), box, 1, nil, fontsFor(testFont()))
	if err != nil {
		t.Fatalf("Page: %v", err)
	}
	assertExactPixels(t, img, 100, 100, []rect{
		{10, 10, 20, 20},
		{30, 10, 40, 20},
		{10, 45, 15, 55},
		{17, 45, 22, 55},
	})
}

// TestTextRenderModes: fill modes ink, the deferred stroke-only and clip-only
// modes and invisible mode do not, and every mode still advances (mode 3's
// second glyph proves the advance by switching back to fill mid-string).
func TestTextRenderModes(t *testing.T) {
	box := content.Box{URX: 100, URY: 100}
	for _, tc := range []struct {
		name, src string
		rects     []rect
	}{
		{"fill", "BT /F1 20 Tf 1 0 0 1 10 50 Tm (A) Tj ET", []rect{{10, 40, 20, 50}}},
		{"fill-stroke", "BT /F1 20 Tf 2 Tr 1 0 0 1 10 50 Tm (A) Tj ET", []rect{{10, 40, 20, 50}}},
		{"invisible", "BT /F1 20 Tf 3 Tr 1 0 0 1 10 50 Tm (A) Tj ET", nil},
		{"stroke-only-deferred", "BT /F1 20 Tf 1 Tr 1 0 0 1 10 50 Tm (A) Tj ET", nil},
		{"clip-only-deferred", "BT /F1 20 Tf 7 Tr 1 0 0 1 10 50 Tm (A) Tj ET", nil},
		{"invisible-still-advances",
			"BT /F1 20 Tf 1 0 0 1 10 50 Tm 3 Tr (A) Tj 0 Tr (A) Tj ET",
			[]rect{{22, 40, 32, 50}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			img, err := Page(context.Background(), []byte(tc.src), box, 1, nil, fontsFor(testFont()))
			if err != nil {
				t.Fatalf("Page: %v", err)
			}
			assertExactPixels(t, img, 100, 100, tc.rects)
		})
	}
}

// TestTextUnresolvedFontSkipsCleanly: a font FontFor cannot supply, an
// unparsable font program, and a nil FontFor must all skip the glyphs without
// erroring the page -- the path fill after the text must still land.
func TestTextUnresolvedFontSkipsCleanly(t *testing.T) {
	src := "BT /F9 20 Tf 1 0 0 1 10 50 Tm (A) Tj ET 30 30 10 10 re f"
	box := content.Box{URX: 100, URY: 100}
	for _, tc := range []struct {
		name  string
		fonts FontFor
	}{
		{"unresolved-name", fontsFor(testFont())},
		{"nil-resolver", nil},
		{"garbage-program", func(string) (Font, bool) {
			return Font{Program: []byte("not an sfnt"), Widths: []float64{600}, FirstChar: 'A'}, true
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			img, err := Page(context.Background(), []byte(src), box, 1, nil, tc.fonts)
			if err != nil {
				t.Fatalf("Page: %v", err)
			}
			assertExactPixels(t, img, 100, 100, []rect{{30, 60, 40, 70}})
		})
	}
}

// TestTextWidthsOverrideFontAdvance: the PDF /Widths array wins over the
// font's own hmtx advance (ISO 32000-1 9.2.4). The font says 600; /Widths
// says 900, so glyph 2 lands at 10+18=28, not 10+12=22.
func TestTextWidthsOverrideFontAdvance(t *testing.T) {
	f := testFont()
	f.Widths = []float64{900}
	src := "BT /F1 20 Tf 1 0 0 1 10 50 Tm (AA) Tj ET"
	box := content.Box{URX: 100, URY: 100}
	img, err := Page(context.Background(), []byte(src), box, 1, nil, fontsFor(f))
	if err != nil {
		t.Fatalf("Page: %v", err)
	}
	assertExactPixels(t, img, 100, 100, []rect{{10, 40, 20, 50}, {28, 40, 38, 50}})
}

// TestHostileGlyphTripsPointBudget: a glyph with an absurd contour count must
// trip the flattened-point budget as an error, not hang or allocate without
// bound -- glyph outlines are budgeted exactly like path operator points.
func TestHostileGlyphTripsPointBudget(t *testing.T) {
	defer func(old int64) { maxPathPoints = old }(maxPathPoints)
	maxPathPoints = 1000

	var contours [][]gpt
	for i := 0; i < 500; i++ {
		x := int16(i % 100)
		contours = append(contours, []gpt{{x, 0, false}, {x + 5, 0, false}, {x, 5, false}})
	}
	f := Font{
		Program:   buildTTF('A', [][][]gpt{contours}, []uint16{600}),
		FirstChar: 'A',
		Widths:    []float64{600},
	}
	src := "BT /F1 20 Tf 1 0 0 1 10 50 Tm (A) Tj ET"
	box := content.Box{URX: 100, URY: 100}
	_, err := Page(context.Background(), []byte(src), box, 1, nil, fontsFor(f))
	if err == nil || !strings.Contains(err.Error(), "points") {
		t.Fatalf("hostile glyph: got err %v, want the path-point budget error", err)
	}
}

// TestTextStateSavedByQ: q/Q save and restore the text state as part of the
// graphics state (ISO 32000-1 table 52): a Tf/Tc inside q..Q must not leak
// out.
func TestTextStateSavedByQ(t *testing.T) {
	// Inside q..Q the size is 40 (square 20pt at x 10); after Q it is back to
	// 20 (square 10pt), shown at Tm origin (50,50).
	src := "BT /F1 20 Tf q /F1 40 Tf 1 0 0 1 10 50 Tm (A) Tj Q 1 0 0 1 50 50 Tm (A) Tj ET"
	box := content.Box{URX: 100, URY: 100}
	img, err := Page(context.Background(), []byte(src), box, 1, nil, fontsFor(testFont()))
	if err != nil {
		t.Fatalf("Page: %v", err)
	}
	assertExactPixels(t, img, 100, 100, []rect{{10, 30, 30, 50}, {50, 40, 60, 50}})
}
