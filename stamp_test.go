package byblos

import (
	"bytes"
	"compress/zlib"
	"fmt"
	"image"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/dobbo-ca/byblos/internal/content"
	"github.com/dobbo-ca/byblos/internal/corpus"
	"github.com/dobbo-ca/byblos/internal/glyphless"
	"github.com/dobbo-ca/byblos/internal/pdfdoc"
)

// G: glyphlessTrueTypeFont's Program must be the WHOLE embedded font asset,
// not a truncated or substituted slice of it -- internal/pdfdoc/text_test.go
// pins that AddFontResource embeds whatever Program it is given correctly;
// this pins that glyphlessTrueTypeFont hands it the right bytes in the first
// place.
func TestGlyphlessTrueTypeFontProgramIsTheWholeEmbeddedFont(t *testing.T) {
	f := glyphlessTrueTypeFont()
	if !bytes.Equal(f.Program, glyphless.Font) {
		t.Errorf("glyphlessTrueTypeFont().Program is %d bytes; want %d bytes matching internal/glyphless.Font exactly",
			len(f.Program), len(glyphless.Font))
	}
}

// This file exercises StampTextLayer against the corpus's "scan" document as
// its primary fixture: a single page, page-covering raster, no /Rotate, no
// pre-existing text content and its own (non-inherited) /Resources. That last
// property matters for every pdftotext-based assertion below -- "scan" has no
// text of its own to mix into the extracted output, so what pdftotext reports
// is exactly what StampTextLayer wrote. "born-digital" was considered and
// rejected for this role because its two existing visible lines would show up
// in the same pdftotext run and complicate the reading-order and bbox
// assertions with content unrelated to what is under test.
//
// Two structural shapes the corpus never generates -- a /Contents array and a
// content stream that leaves an unbalanced graphics-state stack -- are built
// with a small hand-rolled writer below, in the same style corpus.go and
// glyphless_test.go use, rather than added to the shared corpus: they exist
// only to pin AppendContent's unwinding logic (design spec section 2) and
// nothing else in the suite needs them.

// stampBytes runs StampTextLayer and fails the test on error.
func stampBytes(t *testing.T, base []byte, tl TextLayer) []byte {
	t.Helper()
	var out bytes.Buffer
	if err := StampTextLayer(&out, bytes.NewReader(base), tl); err != nil {
		t.Fatalf("StampTextLayer: %v", err)
	}
	return out.Bytes()
}

// requireTool skips the test when name is not on PATH, so the suite stays
// green on a machine with no PDF oracles installed.
func requireTool(t *testing.T, name string) {
	t.Helper()
	if _, err := exec.LookPath(name); err != nil {
		t.Skipf("%s not installed", name)
	}
}

func writeTemp(t *testing.T, data []byte, name string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// stderrOf runs name with args, fails the test if it exits non-zero, and
// returns whatever it wrote to stderr. Several assertions below care only
// about stderr being empty; the tool's exit code is 0 whether or not it
// silently substituted a broken font, so stdout/exit code prove nothing (see
// design spec section 5a's measured sensitivity table).
func stderrOf(t *testing.T, name string, args ...string) []byte {
	t.Helper()
	cmd := exec.Command(name, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("%s: %v: %s", name, err, stderr.String())
	}
	return stderr.Bytes()
}

// renderGrayPNG rasterizes pdfPath's first page at 150 DPI, 8-bit gray.
func renderGrayPNG(t *testing.T, pdfPath string) []byte {
	t.Helper()
	prefix := filepath.Join(t.TempDir(), "page")
	out, err := exec.Command("pdftoppm", "-r", "150", "-gray", "-png", pdfPath, prefix).CombinedOutput()
	if err != nil {
		t.Fatalf("pdftoppm: %v: %s", err, out)
	}
	matches, err := filepath.Glob(prefix + "*.png")
	if err != nil || len(matches) == 0 {
		t.Fatalf("pdftoppm produced no page image: %v", err)
	}
	png, err := os.ReadFile(matches[0])
	if err != nil {
		t.Fatal(err)
	}
	return png
}

// bboxWordRE parses one <word ...>text</word> element from `pdftotext -bbox`.
var bboxWordRE = regexp.MustCompile(`<word xMin="([\d.]+)" yMin="([\d.]+)" xMax="([\d.]+)" yMax="([\d.]+)">([^<]*)</word>`)

// assertWordBBoxRoundTrips runs `pdftotext -bbox` on path, requires exactly
// the word wantText to appear, converts poppler's top-down box back to PDF
// default user space (origin lower-left, y up) using pageHeight, and requires
// every edge within 0.05 pt of want. See design spec section 5d for why 0.05
// pt is four orders of magnitude of headroom over the measured residual.
func assertWordBBoxRoundTrips(t *testing.T, path string, want image.Rectangle, wantText string, pageHeight float64) {
	t.Helper()
	out, err := exec.Command("pdftotext", "-bbox", path, "-").Output()
	if err != nil {
		t.Fatalf("pdftotext -bbox: %v", err)
	}
	m := bboxWordRE.FindSubmatch(out)
	if m == nil {
		t.Fatalf("pdftotext -bbox found no <word> element for %q:\n%s", wantText, out)
	}
	if got := string(m[5]); got != wantText {
		t.Fatalf("pdftotext -bbox found word %q, want %q", got, wantText)
	}
	xMin, _ := strconv.ParseFloat(string(m[1]), 64)
	yMinTop, _ := strconv.ParseFloat(string(m[2]), 64)
	xMax, _ := strconv.ParseFloat(string(m[3]), 64)
	yMaxTop, _ := strconv.ParseFloat(string(m[4]), 64)

	gotMinX, gotMaxX := xMin, xMax
	gotMinY, gotMaxY := pageHeight-yMaxTop, pageHeight-yMinTop

	const tol = 0.05
	if math.Abs(gotMinX-float64(want.Min.X)) > tol ||
		math.Abs(gotMaxX-float64(want.Max.X)) > tol ||
		math.Abs(gotMinY-float64(want.Min.Y)) > tol ||
		math.Abs(gotMaxY-float64(want.Max.Y)) > tol {
		t.Errorf("bbox round-trip = (%.6f,%.6f)-(%.6f,%.6f), want (%d,%d)-(%d,%d) within %.2f pt",
			gotMinX, gotMinY, gotMaxX, gotMaxY, want.Min.X, want.Min.Y, want.Max.X, want.Max.Y, tol)
	}
}

// --- (a)/A2: the embedded font is one poppler actually loads ---------------
//
// A1 (hermetic: decode the written /FontFile2 back out and compare to
// internal/glyphless.Font) and A3 (api.Validate clean) both need pdfcpu
// internals and belong in internal/pdfdoc/text_test.go, per the architecture
// rule in design spec section 1 -- arch_test.go forbids any package but
// internal/pdfdoc from importing pdfcpu, including from a test file. This is
// the oracle half only: it catches a /FontFile2 poppler cannot recognise as a
// font at all, which A1 cannot claim by itself.
func TestStampTextLayerFontLoadsCleanlyInPoppler(t *testing.T) {
	requireTool(t, "pdftoppm")
	base := corpusDoc(t, "scan")
	tl := TextLayer{Pages: [][]PositionedWord{{{Text: "Scanned", Bounds: image.Rect(72, 700, 149, 712)}}}}
	stamped := stampBytes(t, base, tl)
	path := writeTemp(t, stamped, "stamped.pdf")

	if errOut := stderrOf(t, "pdftoppm", "-png", path, filepath.Join(t.TempDir(), "page")); len(errOut) != 0 {
		t.Errorf("pdftoppm reported a problem with the embedded glyphless font: %s", errOut)
	}
	if _, err := exec.LookPath("pdffonts"); err == nil {
		if errOut := stderrOf(t, "pdffonts", path); len(errOut) != 0 {
			t.Errorf("pdffonts reported a problem with the embedded font: %s", errOut)
		}
	} else {
		t.Log("pdffonts not installed; skipping that half of the font check")
	}
}

// --- (b): the stamped text is genuinely invisible ---------------------------

// B1: rendering the stamped document produces byte-identical pixels to the
// unstamped base. Tolerance is exact, not epsilon -- design spec section 5b
// measured byte-identical rasters on all 27 corpus documents, so an epsilon
// here would hide a real regression rather than tolerate noise. This alone
// cannot distinguish a correct 3 Tr from a missing one, because the glyphless
// font has no outlines either way -- see B3 below, which is what actually
// pins the render mode.
func TestStampedTextLeavesTheRasterUnchanged(t *testing.T) {
	requireTool(t, "pdftoppm")
	base := corpusDoc(t, "scan")
	basePNG := renderGrayPNG(t, writeTemp(t, base, "base.pdf"))

	tl := TextLayer{Pages: [][]PositionedWord{{{Text: "Scanned", Bounds: image.Rect(72, 700, 149, 712)}}}}
	stamped := stampBytes(t, base, tl)
	stampedPNG := renderGrayPNG(t, writeTemp(t, stamped, "stamped.pdf"))

	if !bytes.Equal(basePNG, stampedPNG) {
		t.Error("stamping an invisible text layer changed the rendered raster")
	}
}

// B3: every text-showing operator in the stamped content stream executes
// under Tr==3. This is the test design spec section 5b calls out as the one
// that actually pins the render mode -- oracle-free and version-independent,
// because it does not depend on whether the embedded font happens to have
// outlines.
func TestStampedTextExecutesAtRenderMode3(t *testing.T) {
	base := corpusDoc(t, "scan")
	tl := TextLayer{Pages: [][]PositionedWord{{
		{Text: "Scanned", Bounds: image.Rect(72, 700, 149, 712)},
		{Text: "twice", Bounds: image.Rect(72, 670, 112, 682)},
	}}}
	stamped := stampBytes(t, base, tl)

	d, err := pdfdoc.Open(bytes.NewReader(stamped))
	if err != nil {
		t.Fatalf("pdfdoc.Open(stamped): %v", err)
	}
	p, err := d.Page(1)
	if err != nil {
		t.Fatalf("Page(1): %v", err)
	}

	lex := content.NewLexer(p.Content)
	tr := 0
	var lastNum float64
	sawText := false
	for {
		tok, err := lex.Next()
		if err != nil {
			break
		}
		switch tok.Kind {
		case content.KindNumber:
			lastNum = tok.Num
		case content.KindKeyword:
			switch string(tok.Text) {
			case "Tr":
				tr = int(lastNum)
			case "Tj", "TJ", "'", "\"":
				sawText = true
				if tr != 3 {
					t.Errorf("text-showing operator %q executed at Tr=%d, want 3", tok.Text, tr)
				}
			}
		}
	}
	if !sawText {
		t.Fatal("stamped content stream shows no text at all")
	}
}

// --- (c): reading order is geometric, not stream order ----------------------

// Fixture: "alpha beta" on the upper line, "gamma delta" on the line below,
// deliberately listed in reverse (delta, gamma, beta, alpha) so the test does
// not depend on StampTextLayer emitting operators in caller order -- poppler
// orders words by position, and it is that placement, not stream order, this
// test holds accountable. Word gaps are ~6pt at a 12pt box height, well under
// the ~23pt design spec section 5c measured as enough to make poppler treat
// two words on the same line as separate columns.
func TestStampedWordsReadInGeometricOrder(t *testing.T) {
	requireTool(t, "pdftotext")
	base := corpusDoc(t, "scan")

	words := []PositionedWord{
		{Text: "delta", Bounds: image.Rect(118, 670, 148, 682)},
		{Text: "gamma", Bounds: image.Rect(72, 670, 112, 682)},
		{Text: "beta", Bounds: image.Rect(108, 700, 138, 712)},
		{Text: "alpha", Bounds: image.Rect(72, 700, 102, 712)},
	}
	stamped := stampBytes(t, base, TextLayer{Pages: [][]PositionedWord{words}})
	path := writeTemp(t, stamped, "reading-order.pdf")

	out, err := exec.Command("pdftotext", path, "-").Output()
	if err != nil {
		t.Fatalf("pdftotext: %v", err)
	}
	got := strings.TrimSpace(string(out))
	const want = "alpha beta\ngamma delta"
	if got != want {
		t.Errorf("pdftotext reading order = %q, want %q", got, want)
	}
}

// --- (d): word boxes round-trip through pdftotext -bbox --------------------

// Must use a /Rotate 0 page: /Rotate is a display attribute that rotates the
// stamped text along with page content (see extract.go's own use of the same
// convention around PageInfo.Bounds), so a rotated page's bbox does not equal
// Bounds by design, not by bug. "scan" carries no /Rotate.
func TestStampedWordBoundsRoundTripThroughBBox(t *testing.T) {
	requireTool(t, "pdftotext")
	base := corpusDoc(t, "scan")
	want := image.Rect(200, 400, 260, 420)
	stamped := stampBytes(t, base, TextLayer{Pages: [][]PositionedWord{{{Text: "Byblos", Bounds: want}}}})
	path := writeTemp(t, stamped, "bbox.pdf")

	assertWordBBoxRoundTrips(t, path, want, "Byblos", float64(corpus.PageHeightPt))
}

// --- (e): the whole corpus ---------------------------------------------------

// Stamps every corpus document with one word on page 1. "malformed" must
// fail -- pdfdoc.Open cannot parse it (see internal/pdfdoc's own
// TestEveryCorpusDocumentSurvivesAWriteRoundTrip for the same split) -- every
// other document must succeed, and where pdftotext is available its output
// must contain the stamped word. This does not repeat the B1 raster-identity
// check per document; that is exercised once, above, against the same word
// shape, and running pdftoppm 27 times over would not add coverage the design
// doesn't already claim (section 5e reports it as verified 27/27 in the
// probe, not asserted per-document here).
//
// The word box is (30,30)-(60,42), not a box nearer the top of a Letter page:
// "mrc-inset-base" is the one corpus document with a non-standard MediaBox
// (420x619, see internal/corpus's mrcInsetBase), and a box assuming a 792pt
// page height lands entirely off that page -- pdftotext then correctly
// reports nothing, which looked like a StampTextLayer bug and was actually a
// fixture bug. (30,30)-(60,42) sits inside every corpus page's MediaBox.
func TestStampTextLayerAcrossTheCorpus(t *testing.T) {
	pdftotextAvailable := false
	if _, err := exec.LookPath("pdftotext"); err == nil {
		pdftotextAvailable = true
	}
	for _, d := range corpus.All() {
		t.Run(d.Name, func(t *testing.T) {
			tl := TextLayer{Pages: [][]PositionedWord{{{Text: "OCR", Bounds: image.Rect(30, 30, 60, 42)}}}}
			var out bytes.Buffer
			err := StampTextLayer(&out, bytes.NewReader(d.Data), tl)
			if d.Name == "malformed" {
				if err == nil {
					t.Fatal("StampTextLayer(malformed): want an error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("StampTextLayer(%s): %v", d.Name, err)
			}
			if !pdftotextAvailable {
				return
			}
			path := writeTemp(t, out.Bytes(), "stamped.pdf")
			text, err := exec.Command("pdftotext", path, "-").Output()
			if err != nil {
				t.Fatalf("pdftotext: %v", err)
			}
			if !bytes.Contains(text, []byte("OCR")) {
				t.Errorf("pdftotext output does not contain the stamped word:\n%s", text)
			}
		})
	}
}

// --- (f): regression pins ----------------------------------------------------

// Pins that the write path also goes through pdfdoc.Open's normalizePageTree
// repair, a third time (see write.go's package comment and
// TestIndirectKidsSurvivesAWriteRoundTrip in internal/pdfdoc): stamping a
// document whose /Kids is an indirect reference must reach both pages, not
// silently stamp neither.
func TestStampTextLayerIndirectKids(t *testing.T) {
	base := corpusDoc(t, "indirect-kids")
	tl := TextLayer{Pages: [][]PositionedWord{
		{{Text: "one", Bounds: image.Rect(72, 700, 100, 712)}},
		{{Text: "two", Bounds: image.Rect(72, 700, 100, 712)}},
	}}
	stampBytes(t, base, tl)
}

// minimalPDF builds a one-page, 612x792, /Rotate-absent PDF whose page
// content is the given fragments -- one fragment produces a direct indirect
// /Contents reference, more than one produces a /Contents array. It exists to
// exercise two page shapes internal/corpus's generator never produces: a
// /Contents array, and a content stream that leaves an unbalanced graphics
// state. Modeled on corpus.go's and glyphless_test.go's own minimal writers;
// this one is narrower than either because it only needs a page dict, a
// content stream (or several), and a page tree of depth one.
func minimalPDF(t *testing.T, contents ...string) []byte {
	t.Helper()
	var buf bytes.Buffer
	buf.WriteString("%PDF-1.7\n%\xE2\xE3\xCF\xD3\n")
	offsets := make([]int, 0, 8)
	reserve := func() int { offsets = append(offsets, -1); return len(offsets) }
	fill := func(n int, body string) {
		offsets[n-1] = buf.Len()
		fmt.Fprintf(&buf, "%d 0 obj\n%s\nendobj\n", n, body)
	}
	fillStream := func(n int, payload string) {
		var z bytes.Buffer
		zw := zlib.NewWriter(&z)
		if _, err := zw.Write([]byte(payload)); err != nil {
			t.Fatal(err)
		}
		if err := zw.Close(); err != nil {
			t.Fatal(err)
		}
		offsets[n-1] = buf.Len()
		fmt.Fprintf(&buf, "%d 0 obj\n<< /Filter /FlateDecode /Length %d >>\nstream\n", n, z.Len())
		buf.Write(z.Bytes())
		buf.WriteString("\nendstream\nendobj\n")
	}

	cat, pages, page := reserve(), reserve(), reserve()
	contObjs := make([]int, len(contents))
	for i := range contents {
		contObjs[i] = reserve()
	}

	fill(cat, fmt.Sprintf("<< /Type /Catalog /Pages %d 0 R >>", pages))
	fill(pages, fmt.Sprintf("<< /Type /Pages /Kids [%d 0 R] /Count 1 >>", page))

	var contentsEntry string
	if len(contObjs) == 1 {
		contentsEntry = fmt.Sprintf("%d 0 R", contObjs[0])
	} else {
		refs := make([]string, len(contObjs))
		for i, o := range contObjs {
			refs[i] = fmt.Sprintf("%d 0 R", o)
		}
		contentsEntry = "[" + strings.Join(refs, " ") + "]"
	}
	fill(page, fmt.Sprintf("<< /Type /Page /Parent %d 0 R /MediaBox [0 0 %d %d]"+
		" /Resources << >> /Contents %s >>", pages, corpus.PageWidthPt, corpus.PageHeightPt, contentsEntry))
	for i, c := range contents {
		fillStream(contObjs[i], c)
	}

	start := buf.Len()
	fmt.Fprintf(&buf, "xref\n0 %d\n0000000000 65535 f \n", len(offsets)+1)
	for _, off := range offsets {
		fmt.Fprintf(&buf, "%010d 00000 n \n", off)
	}
	fmt.Fprintf(&buf, "trailer\n<< /Size %d /Root %d 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(offsets)+1, cat, start)
	return buf.Bytes()
}

// F: an array /Contents must round-trip exactly like the single-stream case.
func TestStampTextLayerArrayContents(t *testing.T) {
	requireTool(t, "pdftotext")
	base := minimalPDF(t, "", "")
	want := image.Rect(200, 400, 260, 420)
	stamped := stampBytes(t, base, TextLayer{Pages: [][]PositionedWord{{{Text: "Byblos", Bounds: want}}}})
	path := writeTemp(t, stamped, "array-contents.pdf")

	assertWordBBoxRoundTrips(t, path, want, "Byblos", float64(corpus.PageHeightPt))
}

// F: a content stream that leaves the graphics-state stack at depth 2 (two
// unmatched `q`s, the second under a translate+scale CTM) must be unwound
// before the stamped text is placed, or the text lands off-page. Design spec
// section 2 measured zero <word> elements from pdftotext -bbox on exactly
// this shape without the unwind.
func TestStampTextLayerUnwindsUnbalancedGraphicsState(t *testing.T) {
	requireTool(t, "pdftotext")
	base := minimalPDF(t, "q 1 0 0 1 100 100 cm q 2 0 0 2 0 0 cm\n")
	want := image.Rect(200, 400, 260, 420)
	stamped := stampBytes(t, base, TextLayer{Pages: [][]PositionedWord{{{Text: "Byblos", Bounds: want}}}})
	path := writeTemp(t, stamped, "unbalanced-q.pdf")

	assertWordBBoxRoundTrips(t, path, want, "Byblos", float64(corpus.PageHeightPt))
}

// G: a `cm` issued OUTSIDE any q/Q pair leaves a net transform that a q/Q
// unwind cannot undo -- the graphics-state stack is balanced (depth 0), but
// the CTM itself has moved. AppendContent must counter the transform itself,
// not just the stack depth, or stamped text drifts by however much the
// content moved the page.
func TestStampTextLayerCountersTopLevelCTM(t *testing.T) {
	requireTool(t, "pdftotext")
	base := minimalPDF(t, "1 0 0 1 100 100 cm\n")
	want := image.Rect(200, 400, 260, 420)
	stamped := stampBytes(t, base, TextLayer{Pages: [][]PositionedWord{{{Text: "Byblos", Bounds: want}}}})
	path := writeTemp(t, stamped, "toplevel-cm.pdf")

	assertWordBBoxRoundTrips(t, path, want, "Byblos", float64(corpus.PageHeightPt))
}

// G: the same shape as above but with the y-flip Chrome/Skia's print-to-PDF
// output emits as its very first, unwrapped operator. A page whose content
// starts "1 0 0 -1 0 792 cm" is common enough in the wild that this is not a
// contrived case -- it is the shape that made a 31% real-world misplacement
// rate.
func TestStampTextLayerCountersTopLevelYFlip(t *testing.T) {
	requireTool(t, "pdftotext")
	base := minimalPDF(t, "1 0 0 -1 0 792 cm\n")
	want := image.Rect(72, 700, 132, 720)
	stamped := stampBytes(t, base, TextLayer{Pages: [][]PositionedWord{{{Text: "Byblos", Bounds: want}}}})
	path := writeTemp(t, stamped, "yflip.pdf")

	assertWordBBoxRoundTrips(t, path, want, "Byblos", float64(corpus.PageHeightPt))
}

// G: the bbox round-trip tests above happen to use "Byblos", whose Helvetica
// AFM widths average almost exactly 500/1000 em -- corrupting every /Widths
// entry to a uniform 500 is invisible to them by coincidence. "OCR" (778,
// 722, 722) has no such coincidence: a uniform-500 corruption misplaces it by
// about 19.5 pt at this box width, far outside the 0.05 pt tolerance.
func TestStampedWordBoundsRoundTripThroughBBoxNonUniformWidths(t *testing.T) {
	requireTool(t, "pdftotext")
	base := corpusDoc(t, "scan")
	want := image.Rect(200, 400, 260, 420)
	stamped := stampBytes(t, base, TextLayer{Pages: [][]PositionedWord{{{Text: "OCR", Bounds: want}}}})
	path := writeTemp(t, stamped, "bbox-ocr.pdf")

	assertWordBBoxRoundTrips(t, path, want, "OCR", float64(corpus.PageHeightPt))
}

// minimalPDFIndirectArrayContents builds a one-page PDF whose /Contents is an
// INDIRECT reference to an array of two content streams -- legal per ISO
// 32000-1 table 30, and a shape minimalPDF never produces (its own array case
// always writes /Contents as a direct array of direct refs).
func minimalPDFIndirectArrayContents(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	buf.WriteString("%PDF-1.7\n%\xE2\xE3\xCF\xD3\n")
	offsets := make([]int, 0, 8)
	reserve := func() int { offsets = append(offsets, -1); return len(offsets) }
	fill := func(n int, body string) {
		offsets[n-1] = buf.Len()
		fmt.Fprintf(&buf, "%d 0 obj\n%s\nendobj\n", n, body)
	}
	fillStream := func(n int, payload string) {
		var z bytes.Buffer
		zw := zlib.NewWriter(&z)
		if _, err := zw.Write([]byte(payload)); err != nil {
			t.Fatal(err)
		}
		if err := zw.Close(); err != nil {
			t.Fatal(err)
		}
		offsets[n-1] = buf.Len()
		fmt.Fprintf(&buf, "%d 0 obj\n<< /Filter /FlateDecode /Length %d >>\nstream\n", n, z.Len())
		buf.Write(z.Bytes())
		buf.WriteString("\nendstream\nendobj\n")
	}

	cat, pages, page, arr, c1, c2 := reserve(), reserve(), reserve(), reserve(), reserve(), reserve()

	fill(cat, fmt.Sprintf("<< /Type /Catalog /Pages %d 0 R >>", pages))
	fill(pages, fmt.Sprintf("<< /Type /Pages /Kids [%d 0 R] /Count 1 >>", page))
	fill(page, fmt.Sprintf("<< /Type /Page /Parent %d 0 R /MediaBox [0 0 %d %d]"+
		" /Resources << >> /Contents %d 0 R >>", pages, corpus.PageWidthPt, corpus.PageHeightPt, arr))
	fill(arr, fmt.Sprintf("[%d 0 R %d 0 R]", c1, c2))
	fillStream(c1, "")
	fillStream(c2, "")

	start := buf.Len()
	fmt.Fprintf(&buf, "xref\n0 %d\n0000000000 65535 f \n", len(offsets)+1)
	for _, off := range offsets {
		fmt.Fprintf(&buf, "%010d 00000 n \n", off)
	}
	fmt.Fprintf(&buf, "trailer\n<< /Size %d /Root %d 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(offsets)+1, cat, start)
	return buf.Bytes()
}

// G: /Contents as an indirect reference to a STREAM already round-trips
// (minimalPDF's single-fragment case exercises that). This pins the other
// legal shape of an indirect /Contents: a reference to an ARRAY. Naively
// wrapping that reference in a new outer array produces /Contents pointing
// at an array containing an array, which no reader accepts as page content --
// silently discarding the original content and the stamp both.
func TestStampTextLayerIndirectArrayContents(t *testing.T) {
	requireTool(t, "pdftotext")
	base := minimalPDFIndirectArrayContents(t)
	want := image.Rect(200, 400, 260, 420)
	stamped := stampBytes(t, base, TextLayer{Pages: [][]PositionedWord{{{Text: "Byblos", Bounds: want}}}})
	path := writeTemp(t, stamped, "indirect-array-contents.pdf")

	assertWordBBoxRoundTrips(t, path, want, "Byblos", float64(corpus.PageHeightPt))
}

// minimalPDFInheritedFormResources builds a one-page PDF where the page has
// NO own /Resources -- it inherits one from its Pages ancestor -- and the
// page's own content stream reaches a font only INDIRECTLY, through a Form
// XObject's content. Content-stream-driven resource consolidation only ever
// parses the page's own content stream (pdfcpu's xreftable.go says so in its
// own TODO), so it cannot see that /F1 is required and would prune it as
// unused, even though the form that /Fm0 names still needs it.
func minimalPDFInheritedFormResources(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	buf.WriteString("%PDF-1.7\n%\xE2\xE3\xCF\xD3\n")
	offsets := make([]int, 0, 8)
	reserve := func() int { offsets = append(offsets, -1); return len(offsets) }
	fill := func(n int, body string) {
		offsets[n-1] = buf.Len()
		fmt.Fprintf(&buf, "%d 0 obj\n%s\nendobj\n", n, body)
	}
	fillStream := func(n int, dict, payload string) {
		var z bytes.Buffer
		zw := zlib.NewWriter(&z)
		if _, err := zw.Write([]byte(payload)); err != nil {
			t.Fatal(err)
		}
		if err := zw.Close(); err != nil {
			t.Fatal(err)
		}
		offsets[n-1] = buf.Len()
		fmt.Fprintf(&buf, "%d 0 obj\n<< %s /Filter /FlateDecode /Length %d >>\nstream\n", n, dict, z.Len())
		buf.Write(z.Bytes())
		buf.WriteString("\nendstream\nendobj\n")
	}

	cat, pages, page, cont, font, form := reserve(), reserve(), reserve(), reserve(), reserve(), reserve()

	fill(cat, fmt.Sprintf("<< /Type /Catalog /Pages %d 0 R >>", pages))
	fill(pages, fmt.Sprintf("<< /Type /Pages /Kids [%d 0 R] /Count 1 /Resources"+
		" << /Font << /F1 %d 0 R >> /XObject << /Fm0 %d 0 R >> >> >>", page, font, form))
	// No /Resources on the page itself: it must inherit the Pages node's.
	fill(page, fmt.Sprintf("<< /Type /Page /Parent %d 0 R /MediaBox [0 0 %d %d] /Contents %d 0 R >>",
		pages, corpus.PageWidthPt, corpus.PageHeightPt, cont))
	fillStream(cont, "", "q /Fm0 Do Q\n")
	fill(font, "<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>")
	// The form has no own /Resources either: it falls back to the page's,
	// per ISO 32000-1 8.10.2, and its content is the only thing on this page
	// that names /F1.
	fillStream(form, "/Type /XObject /Subtype /Form /BBox [0 0 612 792] /Matrix [1 0 0 1 0 0]",
		"BT /F1 24 Tf 72 700 Td (INSIDEFORM) Tj ET\n")

	start := buf.Len()
	fmt.Fprintf(&buf, "xref\n0 %d\n0000000000 65535 f \n", len(offsets)+1)
	for _, off := range offsets {
		fmt.Fprintf(&buf, "%010d 00000 n \n", off)
	}
	fmt.Fprintf(&buf, "trailer\n<< /Size %d /Root %d 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(offsets)+1, cat, start)
	return buf.Bytes()
}

// G: stamping a page that inherits its /Resources from its Pages ancestor,
// and whose page-own content reaches a resource only through a Form
// XObject's Do, must not drop that resource. Before the fix, AddFontResource
// asked pdfcpu to consolidate resources to what the page's own content
// stream names, which silently deleted /F1 -- unused by "q /Fm0 Do Q" itself
// -- and the form's text became unrenderable.
func TestStampTextLayerPreservesInheritedResourcesUsedOnlyByAForm(t *testing.T) {
	requireTool(t, "pdftotext")
	base := minimalPDFInheritedFormResources(t)

	// Sanity check on the hand-rolled fixture itself: the form's text must
	// be visible before stamping, or this test would prove nothing.
	baseText, err := exec.Command("pdftotext", writeTemp(t, base, "base.pdf"), "-").Output()
	if err != nil {
		t.Fatalf("pdftotext (base): %v", err)
	}
	if !bytes.Contains(baseText, []byte("INSIDEFORM")) {
		t.Fatalf("fixture is broken: base pdftotext output does not contain INSIDEFORM:\n%s", baseText)
	}

	tl := TextLayer{Pages: [][]PositionedWord{{{Text: "OCR", Bounds: image.Rect(30, 30, 60, 42)}}}}
	stamped := stampBytes(t, base, tl)
	path := writeTemp(t, stamped, "stamped.pdf")

	stampedText, err := exec.Command("pdftotext", path, "-").Output()
	if err != nil {
		t.Fatalf("pdftotext (stamped): %v", err)
	}
	if !bytes.Contains(stampedText, []byte("INSIDEFORM")) {
		t.Errorf("stamping dropped the form's text (inherited /F1 pruned):\n%s", stampedText)
	}
	if !bytes.Contains(stampedText, []byte("OCR")) {
		t.Errorf("stamped word missing from output:\n%s", stampedText)
	}
}
