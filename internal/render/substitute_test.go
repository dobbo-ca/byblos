package render

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"testing"

	"github.com/dobbo-ca/byblos/internal/content"
	"github.com/dobbo-ca/byblos/internal/pdfdoc"
)

// TestSubstituteFaceMapping pins the standard-14 name mapping, the common
// aliases, the subset-tag strip, the descriptor-flag fallback for unknown
// names, and the DEFERRED faces (Symbol, ZapfDingbats: no metric-compatible
// Liberation face exists; they are a small share of the measured population
// and degrade to widths-only with this test as the record).
func TestSubstituteFaceMapping(t *testing.T) {
	cases := []struct {
		name  string
		flags int
		want  string
	}{
		{"Helvetica", 0, "fonts/LiberationSans-Regular.ttf"},
		{"Helvetica-Bold", 0, "fonts/LiberationSans-Bold.ttf"},
		{"Helvetica-Oblique", 0, "fonts/LiberationSans-Italic.ttf"},
		{"Helvetica-BoldOblique", 0, "fonts/LiberationSans-BoldItalic.ttf"},
		{"Times-Roman", 0, "fonts/LiberationSerif-Regular.ttf"},
		{"Times-Bold", 0, "fonts/LiberationSerif-Bold.ttf"},
		{"Times-Italic", 0, "fonts/LiberationSerif-Italic.ttf"},
		{"Times-BoldItalic", 0, "fonts/LiberationSerif-BoldItalic.ttf"},
		{"Courier", 0, "fonts/LiberationMono-Regular.ttf"},
		{"Courier-Bold", 0, "fonts/LiberationMono-Bold.ttf"},
		{"Courier-Oblique", 0, "fonts/LiberationMono-Italic.ttf"},
		{"Courier-BoldOblique", 0, "fonts/LiberationMono-BoldItalic.ttf"},
		// The two standard-14 faces with no Liberation equivalent: DEFERRED.
		{"Symbol", 0, ""},
		{"ZapfDingbats", 0, ""},
		// Aliases and subset tags seen across the corpus.
		{"Arial", 0, "fonts/LiberationSans-Regular.ttf"},
		{"ArialMT", 0, "fonts/LiberationSans-Regular.ttf"},
		{"Arial-BoldMT", 0, "fonts/LiberationSans-Bold.ttf"},
		{"ABCDEF+Arial-ItalicMT", 0, "fonts/LiberationSans-Italic.ttf"},
		{"TimesNewRoman", 0, "fonts/LiberationSerif-Regular.ttf"},
		{"TimesNewRomanPS-BoldMT", 0, "fonts/LiberationSerif-Bold.ttf"},
		{"CourierNew", 0, "fonts/LiberationMono-Regular.ttf"},
		// Unknown names fall back by descriptor flags: fixed-pitch bit,
		// serif bit, italic bit (ISO 32000-1 table 123), else sans.
		{"Garamond", flagSerif, "fonts/LiberationSerif-Regular.ttf"},
		{"SomeMono", flagFixedPitch, "fonts/LiberationMono-Regular.ttf"},
		{"Whatever", flagItalic, "fonts/LiberationSans-Italic.ttf"},
		{"Whatever", 0, "fonts/LiberationSans-Regular.ttf"},
		{"", 0, "fonts/LiberationSans-Regular.ttf"},
		// An unknown-name SYMBOLIC face (ISO 32000-1 table 123 bit 3) gets no
		// Latin substitute -- its codes mean symbols, and Latin glyphs would
		// misrender them. A recognised standard-14 name still wins over a
		// stray symbolic bit.
		{"Wingdings", flagSymbolic, ""},
		{"Helvetica", flagSymbolic, "fonts/LiberationSans-Regular.ttf"},
		// A proportional Monotype face must NOT be claimed for the fixed-pitch
		// family by name: genuine monospace fonts set the fixed-pitch flag.
		{"MonotypeCorsiva", 0, "fonts/LiberationSans-Regular.ttf"},
	}
	for _, c := range cases {
		if got := substituteFace(c.name, c.flags); got != c.want {
			t.Errorf("substituteFace(%q, %#x) = %q, want %q", c.name, c.flags, got, c.want)
		}
	}
}

// TestSubstituteTakesTheTrueTypePath pins that the bundled faces ride the
// EXISTING 4c sfnt path -- pre-parse glyph gate included -- rather than any
// new machinery: this stage is a resolver fallback plus assets, not a
// renderer.
func TestSubstituteTakesTheTrueTypePath(t *testing.T) {
	p := substituteProgram("Helvetica", 0)
	if p == nil {
		t.Fatal("no program for Helvetica")
	}
	g := parseGlyfIndex(p)
	if g == nil {
		t.Fatal("the bundled face is not an indexable TrueType; it cannot ride the 4c path")
	}
	if !g.boundedBy(maxPathPoints) {
		t.Fatal("the bundled face fails the 4c pre-parse gate")
	}
}

// TestSubstituteAllFacesLoad: every one of the 12 family/style combinations
// must resolve to bytes actually present in the embed FS -- the glob compiles
// even with a file missing, so path-string tests alone would let a face
// silently degrade to widths-only.
func TestSubstituteAllFacesLoad(t *testing.T) {
	for _, name := range []string{
		"Helvetica", "Helvetica-Bold", "Helvetica-Oblique", "Helvetica-BoldOblique",
		"Times-Roman", "Times-Bold", "Times-Italic", "Times-BoldItalic",
		"Courier", "Courier-Bold", "Courier-Oblique", "Courier-BoldOblique",
	} {
		if substituteProgram(name, 0) == nil {
			t.Errorf("no embedded program for %s (face %s)", name, substituteFace(name, 0))
		}
	}
}

// TestSubstituteWinAnsiPunctuation: codes 0x80-0x9f are WinAnsi's smart
// quotes, dashes and euro, NOT Latin-1's C1 controls -- a C1 mapping finds no
// glyph and no advance, so the first curly quote would stack the rest of the
// line on one point. A right single quote (0x92) must both ink and advance.
func TestSubstituteWinAnsiPunctuation(t *testing.T) {
	box := content.Box{URX: 100, URY: 100}
	render := func(text string) *image.RGBA {
		f := Font{BaseFont: "Helvetica"}
		src := fmt.Sprintf("BT /F1 40 Tf 1 0 0 1 5 40 Tm (%s) Tj ET", text)
		img, err := Page(context.Background(), []byte(src), box, 0, 1, nil, fontsFor(f))
		if err != nil {
			t.Fatalf("Page(%q): %v", text, err)
		}
		return img
	}
	one, two := inkCount(render("\x92")), inkCount(render("\x92\x92"))
	if one == 0 {
		t.Fatal("WinAnsi 0x92 (right single quote) inked nothing")
	}
	if two < one*3/2 {
		t.Fatalf("two quotes ink %d vs one %d: the second overlaps the first, so 0x92 has no advance", two, one)
	}
}

// TestNonEmbeddedFontRendersInk: a font dict with NO program and NO widths --
// the standard-14 shape -- must both ink glyphs from the substitute face and
// advance by the face's own metrics (the PDF supplied none). Two 'A's must
// land side by side, not on top of each other.
func TestNonEmbeddedFontRendersInk(t *testing.T) {
	box := content.Box{URX: 100, URY: 100}
	render := func(text string) *image.RGBA {
		f := Font{BaseFont: "Helvetica"}
		src := fmt.Sprintf("BT /F1 40 Tf 1 0 0 1 5 40 Tm (%s) Tj ET", text)
		img, err := Page(context.Background(), []byte(src), box, 0, 1, nil, fontsFor(f))
		if err != nil {
			t.Fatalf("Page(%q): %v", text, err)
		}
		return img
	}
	one, two := inkCount(render("A")), inkCount(render("AA"))
	if one == 0 {
		t.Fatal("substituted Helvetica inked nothing")
	}
	if two < one*3/2 {
		t.Fatalf("two glyphs ink %d vs one glyph %d: the second overlaps the first, so the face's own advance is not applied", two, one)
	}
}

// TestSubstituteWidthsStillWin: where the PDF DOES carry /Widths they drive
// the advance, exactly as 4c pins for embedded fonts -- the substitute
// supplies outlines only. A /Widths of 0 must stack both glyphs.
func TestSubstituteWidthsStillWin(t *testing.T) {
	box := content.Box{URX: 100, URY: 100}
	f := Font{BaseFont: "Helvetica", FirstChar: 'A', Widths: []float64{0}}
	src := "BT /F1 40 Tf 1 0 0 1 5 40 Tm (AA) Tj ET"
	img, err := Page(context.Background(), []byte(src), box, 0, 1, nil, fontsFor(f))
	if err != nil {
		t.Fatalf("Page: %v", err)
	}
	f2 := Font{BaseFont: "Helvetica", FirstChar: 'A', Widths: []float64{0}}
	src2 := "BT /F1 40 Tf 1 0 0 1 5 40 Tm (A) Tj ET"
	img2, err := Page(context.Background(), []byte(src2), box, 0, 1, nil, fontsFor(f2))
	if err != nil {
		t.Fatalf("Page: %v", err)
	}
	if a, b := inkCount(img), inkCount(img2); a != b {
		t.Fatalf("with /Widths [0] two shows ink %d pixels vs one show %d; the PDF width did not drive the advance", a, b)
	}
}

// TestSymbolDeferredSkipsCleanly: Symbol has no bundled face; its shows skip
// (widths still advance) and the rest of the page renders.
func TestSymbolDeferredSkipsCleanly(t *testing.T) {
	f := Font{BaseFont: "Symbol", FirstChar: 'A', Widths: []float64{600}}
	src := "BT /F1 20 Tf 1 0 0 1 10 50 Tm (A) Tj ET 30 30 10 10 re f"
	box := content.Box{URX: 100, URY: 100}
	img, err := Page(context.Background(), []byte(src), box, 0, 1, nil, fontsFor(f))
	if err != nil {
		t.Fatalf("Page: %v", err)
	}
	assertExactPixels(t, img, 100, 100, []rect{{30, 60, 40, 70}})
}

func inkCount(img *image.RGBA) int {
	n := 0
	b := img.Bounds()
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			r, g, bl, _ := img.At(x, y).RGBA()
			if r < 0x8000 && g < 0x8000 && bl < 0x8000 {
				n++
			}
		}
	}
	return n
}

// pdfdocFonts resolves Tf names through the SAME seam a production caller
// will use: pdfdoc.RenderFont supplies program bytes, name, flags and widths,
// and render.Font carries them across.
func pdfdocFonts(d pdfdoc.Doc, p *pdfdoc.Page) FontFor {
	return func(name string) (Font, bool) {
		rf, ok := d.RenderFont(p.Scope, name)
		if !ok {
			return Font{}, false
		}
		return Font{Program: rf.Program, BaseFont: rf.BaseFont, Flags: rf.Flags,
			FirstChar: rf.FirstChar, Widths: rf.Widths,
			Type0: rf.Type0, W: rf.W, DW: rf.DW}, true
	}
}

// TestRenderFontReadsTheFontDict pins pdfdoc.RenderFont against the two
// fixture shapes: an embedded /FontFile2 dict returns the exact program bytes
// and widths, and a bare standard-14 dict returns name and no program.
func TestRenderFontReadsTheFontDict(t *testing.T) {
	d, err := pdfdoc.Open(bytes.NewReader(textPDF()))
	if err != nil {
		t.Fatalf("pdfdoc.Open: %v", err)
	}
	p, err := d.Page(1)
	if err != nil {
		t.Fatalf("Page(1): %v", err)
	}
	rf, ok := d.RenderFont(p.Scope, "F1")
	if !ok {
		t.Fatal("RenderFont refused the embedded fixture font")
	}
	if !bytes.Equal(rf.Program, oracleTTF()) {
		t.Fatalf("program: got %d bytes, want the %d /FontFile2 bytes", len(rf.Program), len(oracleTTF()))
	}
	if rf.BaseFont != "BbOracle" || rf.FirstChar != 65 || rf.Flags != 32 {
		t.Fatalf("dict fields: %+v", rf)
	}
	if len(rf.Widths) != 3 || rf.Widths[0] != 600 {
		t.Fatalf("widths: %v", rf.Widths)
	}

	bare := wrapPDF([]string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 200 200]" +
			" /Resources << /Font << /F1 5 0 R >> >> /Contents 4 0 R >>",
		"<< /Length 0 >>\nstream\n\nendstream",
		"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
	})
	d2, err := pdfdoc.Open(bytes.NewReader(bare))
	if err != nil {
		t.Fatalf("pdfdoc.Open(bare): %v", err)
	}
	p2, err := d2.Page(1)
	if err != nil {
		t.Fatalf("Page(1): %v", err)
	}
	rf2, ok := d2.RenderFont(p2.Scope, "F1")
	if !ok {
		t.Fatal("RenderFont refused the bare standard-14 dict")
	}
	if rf2.BaseFont != "Helvetica" || rf2.Program != nil || rf2.Widths != nil {
		t.Fatalf("bare dict: %+v", rf2)
	}
}

// TestEmbeddedTextThroughPdfdocSeamAgreesWithPdftoppm renders textPDF through
// the FULL production seam -- pdfdoc.Open -> RenderFont -> render.Page -- and
// compares against pdftoppm, pinning that the seam carries the same bytes the
// hand-built FontFor did in TestTextAgreesWithPdftoppm.
func TestEmbeddedTextThroughPdfdocSeamAgreesWithPdftoppm(t *testing.T) {
	pdf := textPDF()
	oracle := pdftoppmPNG(t, pdf)
	d, err := pdfdoc.Open(bytes.NewReader(pdf))
	if err != nil {
		t.Fatalf("pdfdoc.Open: %v", err)
	}
	p, err := d.Page(1)
	if err != nil {
		t.Fatalf("Page(1): %v", err)
	}
	box := content.Box{LLX: p.CropBox.LLX, LLY: p.CropBox.LLY, URX: p.CropBox.URX, URY: p.CropBox.URY}
	got, err := Page(context.Background(), p.Content, box, 0, 1, nil, pdfdocFonts(d, p))
	if err != nil {
		t.Fatalf("Page: %v", err)
	}
	const tolerance = 0.05
	if frac := mismatchFraction(t, got, oracle); frac > tolerance {
		t.Errorf("seam-resolved text disagrees with pdftoppm on %.1f%% of pixels; tolerance %.0f%%",
			frac*100, tolerance*100)
	}
}
