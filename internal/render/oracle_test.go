package render

import (
	"bytes"
	"compress/zlib"
	"context"
	"fmt"
	"image"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/dobbo-ca/byblos"
	"github.com/dobbo-ca/byblos/internal/content"
	"github.com/dobbo-ca/byblos/internal/pdfdoc"
)

// wrapPDF numbers objs from 1, appends an xref, and points the trailer at
// object 1 as /Root -- the hand-rolled fixture idiom internal/corpus uses.
func wrapPDF(objs []string) []byte {
	var buf []byte
	buf = append(buf, "%PDF-1.4\n"...)
	var offsets []int
	for _, o := range objs {
		offsets = append(offsets, len(buf))
		buf = append(buf, fmt.Sprintf("%d 0 obj\n%s\nendobj\n", len(offsets), o)...)
	}
	xref := len(buf)
	buf = append(buf, fmt.Sprintf("xref\n0 %d\n0000000000 65535 f \n", len(offsets)+1)...)
	for _, off := range offsets {
		buf = append(buf, fmt.Sprintf("%010d 00000 n \n", off)...)
	}
	buf = append(buf, fmt.Sprintf("trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n",
		len(offsets)+1, xref)...)
	return buf
}

// vectorPDF wraps a content stream in a minimal one-page PDF.
func vectorPDF(contentStream string, w, h float64, rotate int) []byte {
	return wrapPDF([]string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		fmt.Sprintf("<< /Type /Page /Parent 2 0 R /MediaBox [0 0 %g %g] /Rotate %d /Contents 4 0 R >>",
			w, h, rotate),
		fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(contentStream), contentStream),
	})
}

// oracleContent exercises both winding rules, a stroke, a curve, and both
// device colour spaces, placed asymmetrically so a flipped or mirrored
// renderer cannot pass by luck.
const oracleContent = "1 0 0 rg 20 110 80 60 re f " +
	"0 0 1 rg 110 110 70 70 re 130 130 30 30 re f* " +
	"0 g 6 w 20 20 m 90 80 l S " +
	"0.5 g 110 60 m 180 60 l 180 20 110 20 110 60 c h f"

// mismatchFraction counts pixels whose channels differ by more than 40/255
// between the two images -- room for poppler's antialiased edges, none for a
// shape in the wrong place.
func mismatchFraction(t *testing.T, a *image.RGBA, b image.Image) float64 {
	t.Helper()
	if a.Bounds() != b.Bounds() {
		t.Fatalf("size mismatch: %v vs %v", a.Bounds(), b.Bounds())
	}
	diff := func(x, y uint32) int32 {
		d := int32(x>>8) - int32(y>>8)
		if d < 0 {
			d = -d
		}
		return d
	}
	bad, total := 0, 0
	bnd := a.Bounds()
	for y := bnd.Min.Y; y < bnd.Max.Y; y++ {
		for x := bnd.Min.X; x < bnd.Max.X; x++ {
			ar, ag, ab, _ := a.At(x, y).RGBA()
			br, bg, bb, _ := b.At(x, y).RGBA()
			total++
			if diff(ar, br) > 40 || diff(ag, bg) > 40 || diff(ab, bb) > 40 {
				bad++
			}
		}
	}
	return float64(bad) / float64(total)
}

// TestRenderAgreesWithPdftoppm compares this renderer against poppler at 72
// DPI on a page of filled and stroked paths: at most 5% of pixels may
// disagree (antialiased edges), and the do-nothing null -- a blank raster --
// must FAIL the same metric, which pins that the metric can fail at all
// (byb-8b9.1's acceptance).
// pdftoppmPNG renders page 1 of pdf at 72 DPI through poppler, skipping the
// test when pdftoppm is not on PATH.
func pdftoppmPNG(t *testing.T, pdf []byte) image.Image {
	t.Helper()
	pdftoppm, err := exec.LookPath("pdftoppm")
	if err != nil {
		t.Skipf("pdftoppm not on PATH: %v", err)
	}
	dir := t.TempDir()
	pdfPath := filepath.Join(dir, "page.pdf")
	if err := os.WriteFile(pdfPath, pdf, 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(pdftoppm, "-r", "72", "-png", pdfPath, filepath.Join(dir, "oracle")).CombinedOutput()
	if err != nil {
		t.Fatalf("pdftoppm: %v: %s", err, out)
	}
	matches, err := filepath.Glob(filepath.Join(dir, "oracle*.png"))
	if err != nil || len(matches) != 1 {
		t.Fatalf("expected one pdftoppm page, got %v (err %v)", matches, err)
	}
	f, err := os.Open(matches[0])
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	oracle, err := png.Decode(f)
	if err != nil {
		t.Fatalf("decode pdftoppm output: %v", err)
	}
	return oracle
}

func TestRenderAgreesWithPdftoppm(t *testing.T) {
	oracle := pdftoppmPNG(t, vectorPDF(oracleContent, 200, 200, 0))

	box := content.Box{LLX: 0, LLY: 0, URX: 200, URY: 200}
	got, err := Page(context.Background(), []byte(oracleContent), box, 0, 1, nil, nil)
	if err != nil {
		t.Fatalf("Page: %v", err)
	}
	const tolerance = 0.05
	frac := mismatchFraction(t, got, oracle)
	t.Logf("mismatch vs pdftoppm: %.2f%% of pixels", frac*100)
	if frac > tolerance {
		t.Errorf("rendered page disagrees with pdftoppm on %.1f%% of pixels; tolerance %.0f%%",
			frac*100, tolerance*100)
	}

	// The null check: a blank canvas must not pass the metric, or the metric
	// measures nothing.
	blank, err := Page(context.Background(), nil, box, 0, 1, nil, nil)
	if err != nil {
		t.Fatalf("Page(blank): %v", err)
	}
	if frac := mismatchFraction(t, blank, oracle); frac <= tolerance {
		t.Errorf("a BLANK raster is within tolerance of pdftoppm (%.1f%% mismatch); the oracle metric is broken", frac*100)
	}
}

// imagePDF builds a one-page 200x200 PDF placing two solid-colour flate RGB
// image XObjects at different CTMs: red axis-aligned, blue rotated 30
// degrees. Solid colours keep poppler's image smoothing out of the
// comparison, so only PLACEMENT geometry can disagree -- which is what stage
// 4b (byb-8b9.2) adds.
func imagePDF() []byte {
	imgObj := func(r, g, b byte) string {
		return flateImageObj(4, 4, bytes.Repeat([]byte{r, g, b}, 4*4))
	}
	// 51.9615 = 60*cos30, 30 = 60*sin30: a 60pt square rotated 30 degrees CCW.
	const imageContent = "q 80 0 0 60 20 110 cm /Im0 Do Q " +
		"q 51.9615 30 -30 51.9615 100 30 cm /Im1 Do Q"
	return wrapPDF([]string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 200 200]" +
			" /Resources << /XObject << /Im0 5 0 R /Im1 6 0 R >> >> /Contents 4 0 R >>",
		fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(imageContent), imageContent),
		imgObj(255, 0, 0),
		imgObj(0, 0, 255),
	})
}

// flateImageObj builds a flate RGB /Image XObject object body from raw
// 3-byte-per-pixel samples.
func flateImageObj(w, h int, px []byte) string {
	var buf bytes.Buffer
	zw := zlib.NewWriter(&buf)
	if _, err := zw.Write(px); err != nil {
		panic(err)
	}
	if err := zw.Close(); err != nil {
		panic(err)
	}
	return fmt.Sprintf("<< /Type /XObject /Subtype /Image /Width %d /Height %d"+
		" /ColorSpace /DeviceRGB /BitsPerComponent 8 /Filter /FlateDecode /Length %d >>"+
		"\nstream\n%s\nendstream", w, h, buf.Len(), buf.Bytes())
}

// pdfdocImages resolves Do names through the SAME decode seam the extract
// path uses: pdfdoc.XObject -> RawImage -> image.Decode.
func pdfdocImages(d pdfdoc.Doc, p *pdfdoc.Page) ImageFor {
	return func(name string) (Image, bool) {
		xo, ok := d.XObject(p.Scope, name)
		if !ok || !xo.Image {
			return Image{}, false
		}
		data, fileType, err := d.RawImage(xo.ID)
		if err != nil || fileType == "jbig2" || fileType == "jpx" {
			return Image{}, false
		}
		im, _, err := image.Decode(bytes.NewReader(data))
		if err != nil {
			return Image{}, false
		}
		return Image{Data: im}, true
	}
}

// TestImagesAgreeWithPdftoppm is byb-8b9.2's acceptance: a page with two
// images at different CTMs -- one of them rotated -- matches pdftoppm within
// tolerance, with the images decoded through the SAME seam the extract path
// uses (pdfdoc.RawImage, then image.Decode), and the blank null must fail the
// metric.
func TestImagesAgreeWithPdftoppm(t *testing.T) {
	pdf := imagePDF()
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
	got, err := Page(context.Background(), p.Content, box, 0, 1, pdfdocImages(d, p), nil)
	if err != nil {
		t.Fatalf("Page: %v", err)
	}
	const tolerance = 0.05
	frac := mismatchFraction(t, got, oracle)
	t.Logf("image mismatch vs pdftoppm: %.2f%% of pixels", frac*100)
	if frac > tolerance {
		t.Errorf("image page disagrees with pdftoppm on %.1f%% of pixels; tolerance %.0f%%",
			frac*100, tolerance*100)
	}

	blank, err := Page(context.Background(), nil, box, 0, 1, nil, nil)
	if err != nil {
		t.Fatalf("Page(blank): %v", err)
	}
	if frac := mismatchFraction(t, blank, oracle); frac <= tolerance {
		t.Errorf("a BLANK raster is within tolerance of pdftoppm (%.1f%% mismatch); the image oracle metric is broken", frac*100)
	}
}

// oracleTTF is a three-glyph TrueType font for the text oracle: 'A' a
// square, 'B' a triangle, 'C' a diamond of four quadratic curves -- three
// distinct asymmetric shapes so a wrong code-to-glyph mapping, a flipped
// outline, or an unflattened curve cannot pass by luck.
func oracleTTF() []byte {
	square := squareGlyph()
	triangle := [][]gpt{{{0, 0, false}, {600, 0, false}, {300, 500, false}}}
	diamond := [][]gpt{{
		{250, 0, false}, {500, 0, true}, {500, 250, false}, {500, 500, true},
		{250, 500, false}, {0, 500, true}, {0, 250, false}, {0, 0, true},
	}}
	return buildTTF(1000, 'A',
		[][]byte{buildGlyf(square), buildGlyf(triangle), buildGlyf(diamond)},
		[]uint16{600, 700, 550})
}

// textOracleContent shows two lines of text at different sizes, with a TJ
// adjustment on the second, so widths, line moves and the adjustment all land
// in the oracle comparison. The sizes are large enough that a blank canvas
// fails the tolerance, which the null check below depends on.
const textOracleContent = "BT /F1 72 Tf 1 0 0 1 10 110 Tm (ABC) Tj " +
	"48 Tf 0 -90 Td [(AB) -400 (C)] TJ ET"

// textPDF embeds oracleTTF as /FontFile2 in a simple TrueType font dict
// showing textOracleContent.
func textPDF() []byte {
	ttf := string(oracleTTF())
	const textContent = textOracleContent
	return wrapPDF([]string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 200 200]" +
			" /Resources << /Font << /F1 5 0 R >> >> /Contents 4 0 R >>",
		fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(textContent), textContent),
		"<< /Type /Font /Subtype /TrueType /BaseFont /BbOracle /FirstChar 65" +
			" /LastChar 67 /Widths [600 700 550] /Encoding /WinAnsiEncoding" +
			" /FontDescriptor 6 0 R >>",
		"<< /Type /FontDescriptor /FontName /BbOracle /Flags 32" +
			" /FontBBox [0 -200 1000 800] /ItalicAngle 0 /Ascent 800" +
			" /Descent -200 /CapHeight 500 /StemV 80 /FontFile2 7 0 R >>",
		fmt.Sprintf("<< /Length %d /Length1 %d >>\nstream\n%s\nendstream", len(ttf), len(ttf), ttf),
	})
}

// TestTextAgreesWithPdftoppm is byb-8b9.3's second acceptance clause: a page
// of embedded-TrueType text rasterises within tolerance of pdftoppm, and the
// blank null must fail the metric. The renderer sees the same /FontFile2
// bytes and /Widths the PDF carries, through the FontFor seam.
func TestTextAgreesWithPdftoppm(t *testing.T) {
	pdf := textPDF()
	oracle := pdftoppmPNG(t, pdf)

	fonts := func(name string) (Font, bool) {
		if name != "F1" {
			return Font{}, false
		}
		return Font{Program: oracleTTF(), FirstChar: 65, Widths: []float64{600, 700, 550}}, true
	}
	box := content.Box{URX: 200, URY: 200}
	got, err := Page(context.Background(), []byte(textOracleContent), box, 0, 1, nil, fonts)
	if err != nil {
		t.Fatalf("Page: %v", err)
	}
	const tolerance = 0.05
	frac := mismatchFraction(t, got, oracle)
	t.Logf("text mismatch vs pdftoppm: %.2f%% of pixels", frac*100)
	if frac > tolerance {
		t.Errorf("text page disagrees with pdftoppm on %.1f%% of pixels; tolerance %.0f%%",
			frac*100, tolerance*100)
	}

	blank, err := Page(context.Background(), nil, box, 0, 1, nil, nil)
	if err != nil {
		t.Fatalf("Page(blank): %v", err)
	}
	if frac := mismatchFraction(t, blank, oracle); frac <= tolerance {
		t.Errorf("a BLANK raster is within tolerance of pdftoppm (%.1f%% mismatch); the text oracle metric is broken", frac*100)
	}
}

// oracleCFF is the bare-CFF (Type1C) counterpart of oracleTTF: 'A' a square,
// 'B' a triangle, 'C' a diamond of four CUBIC curves -- three distinct
// asymmetric shapes, with C pinning the SegmentOpCubeTo path against
// poppler's own rendering of the same charstrings.
func oracleCFF() []byte {
	square := t2cs(0, 0, "rmoveto", 600, "hlineto", 500, "vlineto", -600, "hlineto", "endchar")
	triangle := t2cs(0, 0, "rmoveto", 600, 0, "rlineto", -300, 500, "rlineto", "endchar")
	diamond := t2cs(250, 0, "rmoveto",
		150, 0, 100, 100, 0, 150, "rrcurveto",
		0, 150, -100, 100, -150, 0, "rrcurveto",
		-150, 0, -100, -100, 0, -150, "rrcurveto",
		0, -150, 100, -100, 150, 0, "rrcurveto", "endchar")
	return buildCFF(cffOpts{
		charstrings: [][]byte{t2cs("endchar"), square, triangle, diamond},
		sids:        []uint16{34, 35, 36}, // 'A' 'B' 'C' under the standard encoding
	})
}

// type1CPDF embeds oracleCFF as /FontFile3 /Subtype /Type1C -- a page 4c's
// TrueType-only renderer could not render at all -- showing the same
// textOracleContent as textPDF.
func type1CPDF() []byte {
	cff := string(oracleCFF())
	const textContent = textOracleContent
	return wrapPDF([]string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 200 200]" +
			" /Resources << /Font << /F1 5 0 R >> >> /Contents 4 0 R >>",
		fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(textContent), textContent),
		"<< /Type /Font /Subtype /Type1 /BaseFont /BbOracle /FirstChar 65" +
			" /LastChar 67 /Widths [600 700 550] /FontDescriptor 6 0 R >>",
		"<< /Type /FontDescriptor /FontName /BbOracle /Flags 32" +
			" /FontBBox [0 -200 1000 800] /ItalicAngle 0 /Ascent 800" +
			" /Descent -200 /CapHeight 500 /StemV 80 /FontFile3 7 0 R >>",
		fmt.Sprintf("<< /Subtype /Type1C /Length %d >>\nstream\n%s\nendstream", len(cff), cff),
	})
}

// TestType1CTextAgreesWithPdftoppm is byb-8b9.4's acceptance: a page of
// bare-CFF text -- which stage 4c could not render at all -- rasterises
// within tolerance of pdftoppm, and the blank null must fail the metric. The
// renderer sees the same /FontFile3 bytes and /Widths the PDF carries,
// through the FontFor seam; TestType1CProgramTakesTheCFFPath pins that these
// bytes go down the 4d path and no other.
func TestType1CTextAgreesWithPdftoppm(t *testing.T) {
	pdf := type1CPDF()
	oracle := pdftoppmPNG(t, pdf)

	fonts := func(name string) (Font, bool) {
		if name != "F1" {
			return Font{}, false
		}
		return Font{Program: oracleCFF(), FirstChar: 65, Widths: []float64{600, 700, 550}}, true
	}
	box := content.Box{URX: 200, URY: 200}
	got, err := Page(context.Background(), []byte(textOracleContent), box, 0, 1, nil, fonts)
	if err != nil {
		t.Fatalf("Page: %v", err)
	}
	const tolerance = 0.05
	frac := mismatchFraction(t, got, oracle)
	t.Logf("Type1C text mismatch vs pdftoppm: %.2f%% of pixels", frac*100)
	if frac > tolerance {
		t.Errorf("Type1C text page disagrees with pdftoppm on %.1f%% of pixels; tolerance %.0f%%",
			frac*100, tolerance*100)
	}

	blank, err := Page(context.Background(), nil, box, 0, 1, nil, nil)
	if err != nil {
		t.Fatalf("Page(blank): %v", err)
	}
	if frac := mismatchFraction(t, blank, oracle); frac <= tolerance {
		t.Errorf("a BLANK raster is within tolerance of pdftoppm (%.1f%% mismatch); the Type1C oracle metric is broken", frac*100)
	}
}

// oracleCIDCFF is oracleCFF's CID-keyed twin (byb-8b9.8): the same three
// charstrings behind CIDs 1..3 through an explicit charset, with the square
// drawn via FD 1's local subr under a two-FD FDSelect so the oracle also
// pins per-font-DICT subr resolution against poppler's.
func oracleCIDCFF() []byte {
	square := t2cs(-107, "callsubr", "endchar")
	triangle := t2cs(0, 0, "rmoveto", 600, 0, "rlineto", -300, 500, "rlineto", "endchar")
	diamond := t2cs(250, 0, "rmoveto",
		150, 0, 100, 100, 0, 150, "rrcurveto",
		0, 150, -100, 100, -150, 0, "rrcurveto",
		-150, 0, -100, -100, 0, -150, "rrcurveto",
		0, -150, 100, -100, 150, 0, "rrcurveto", "endchar")
	return buildCFF(cffOpts{
		charstrings: [][]byte{t2cs("endchar"), square, triangle, diamond},
		sids:        []uint16{1, 2, 3}, // charset as CID map
		strings:     [][]byte{[]byte("Adobe"), []byte("Identity")},
		fdSubrs: [][][]byte{nil, {t2cs(0, 0, "rmoveto",
			600, "hlineto", 500, "vlineto", -600, "hlineto", "return")}},
		fdSelect: fdSelect3(4, [2]int{0, 0}, [2]int{1, 1}, [2]int{2, 0}),
	})
}

// cidTextOracleContent is textOracleContent with the codes spelled as the
// 2-byte Identity-H hex strings for CIDs 1..3.
const cidTextOracleContent = "BT /F1 72 Tf 1 0 0 1 10 110 Tm <000100020003> Tj " +
	"48 Tf 0 -90 Td [<00010002> -400 <0003>] TJ ET"

// cidCFFPDF embeds oracleCIDCFF as /FontFile3 /Subtype /CIDFontType0C under a
// Type0 font with /Encoding /Identity-H and /W widths.
func cidCFFPDF() []byte {
	cff := string(oracleCIDCFF())
	const textContent = cidTextOracleContent
	return wrapPDF([]string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 200 200]" +
			" /Resources << /Font << /F1 5 0 R >> >> /Contents 4 0 R >>",
		fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(textContent), textContent),
		"<< /Type /Font /Subtype /Type0 /BaseFont /BbOracle /Encoding /Identity-H" +
			" /DescendantFonts [6 0 R] >>",
		"<< /Type /Font /Subtype /CIDFontType0 /BaseFont /BbOracle" +
			" /CIDSystemInfo << /Registry (Adobe) /Ordering (Identity) /Supplement 0 >>" +
			" /DW 1000 /W [1 [600] 2 [700] 3 [550]] /FontDescriptor 7 0 R >>",
		"<< /Type /FontDescriptor /FontName /BbOracle /Flags 4" +
			" /FontBBox [0 -200 1000 800] /ItalicAngle 0 /Ascent 800" +
			" /Descent -200 /CapHeight 500 /StemV 80 /FontFile3 8 0 R >>",
		fmt.Sprintf("<< /Subtype /CIDFontType0C /Length %d >>\nstream\n%s\nendstream", len(cff), cff),
	})
}

// TestCIDKeyedCFFTextAgreesWithPdftoppm is byb-8b9.8's acceptance: a
// Type0/CIDFontType0 page -- 2-byte Identity-H codes into a CID-keyed CFF,
// which stage 4d refused outright -- rasterises within tolerance of
// pdftoppm, and the blank null must fail the metric. The renderer sees the
// same /FontFile3 bytes and /W widths the PDF carries, through the FontFor
// seam; TestCFFCIDKeyedRenders pins that such bytes take the CFF path.
func TestCIDKeyedCFFTextAgreesWithPdftoppm(t *testing.T) {
	pdf := cidCFFPDF()
	oracle := pdftoppmPNG(t, pdf)

	fonts := func(name string) (Font, bool) {
		if name != "F1" {
			return Font{}, false
		}
		return Font{Program: oracleCIDCFF(), Type0: true, DW: 1000,
			W: map[uint16]float64{1: 600, 2: 700, 3: 550}}, true
	}
	box := content.Box{URX: 200, URY: 200}
	got, err := Page(context.Background(), []byte(cidTextOracleContent), box, 0, 1, nil, fonts)
	if err != nil {
		t.Fatalf("Page: %v", err)
	}
	const tolerance = 0.05
	frac := mismatchFraction(t, got, oracle)
	t.Logf("CID-keyed CFF text mismatch vs pdftoppm: %.2f%% of pixels", frac*100)
	if frac > tolerance {
		t.Errorf("CID-keyed CFF text page disagrees with pdftoppm on %.1f%% of pixels; tolerance %.0f%%",
			frac*100, tolerance*100)
	}

	blank, err := Page(context.Background(), nil, box, 0, 1, nil, nil)
	if err != nil {
		t.Fatalf("Page(blank): %v", err)
	}
	if frac := mismatchFraction(t, blank, oracle); frac <= tolerance {
		t.Errorf("a BLANK raster is within tolerance of pdftoppm (%.1f%% mismatch); the CID oracle metric is broken", frac*100)
	}
}

// TestCIDKeyedCFFTextThroughPdfdocAgreesWithPdftoppm is byb-6z1's acceptance:
// TestCIDKeyedCFFTextAgreesWithPdftoppm above hand-builds Font{Type0: true,
// ...} to pin the RENDERER half; this drives the SAME cidCFFPDF() page
// through pdfdocFonts, the seam byb-6z1 connects, so the Type0 /Encoding,
// /DescendantFonts and /W/DW resolution in pdfdoc.RenderFont is what is
// under test, not a hand-written literal. Kept beside the renderer test
// rather than replacing it, since each pins a different half of the seam.
func TestCIDKeyedCFFTextThroughPdfdocAgreesWithPdftoppm(t *testing.T) {
	pdf := cidCFFPDF()
	oracle := pdftoppmPNG(t, pdf)

	d, err := pdfdoc.Open(bytes.NewReader(pdf))
	if err != nil {
		t.Fatalf("pdfdoc.Open: %v", err)
	}
	p, err := d.Page(1)
	if err != nil {
		t.Fatalf("Page(1): %v", err)
	}
	box := content.Box{URX: 200, URY: 200}
	got, err := Page(context.Background(), []byte(cidTextOracleContent), box, 0, 1, nil, pdfdocFonts(d, p))
	if err != nil {
		t.Fatalf("Page: %v", err)
	}
	const tolerance = 0.05
	frac := mismatchFraction(t, got, oracle)
	t.Logf("CID-keyed CFF text (through pdfdoc) mismatch vs pdftoppm: %.2f%% of pixels", frac*100)
	if frac > tolerance {
		t.Errorf("CID-keyed CFF text page disagrees with pdftoppm on %.1f%% of pixels; tolerance %.0f%%",
			frac*100, tolerance*100)
	}

	blank, err := Page(context.Background(), nil, box, 0, 1, nil, nil)
	if err != nil {
		t.Fatalf("Page(blank): %v", err)
	}
	if frac := mismatchFraction(t, blank, oracle); frac <= tolerance {
		t.Errorf("a BLANK raster is within tolerance of pdftoppm (%.1f%% mismatch); the CID oracle metric is broken", frac*100)
	}
}

// hostileWCIDPDF is cidCFFPDF with its /W replaced by the range form's worst
// case (ISO 32000-1 9.7.4.3): /W [0 2147483647 500] declares every CID from
// 0 to 2^31-1 gets width 500 from ~20 bytes of input. Unbounded, that
// flattens to 2^31 map entries (byb-6z1's bug class 5).
func hostileWCIDPDF() []byte {
	cff := string(oracleCIDCFF())
	const textContent = cidTextOracleContent
	return wrapPDF([]string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 200 200]" +
			" /Resources << /Font << /F1 5 0 R >> >> /Contents 4 0 R >>",
		fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(textContent), textContent),
		"<< /Type /Font /Subtype /Type0 /BaseFont /BbOracle /Encoding /Identity-H" +
			" /DescendantFonts [6 0 R] >>",
		"<< /Type /Font /Subtype /CIDFontType0 /BaseFont /BbOracle" +
			" /CIDSystemInfo << /Registry (Adobe) /Ordering (Identity) /Supplement 0 >>" +
			" /DW 1000 /W [0 2147483647 500] /FontDescriptor 7 0 R >>",
		"<< /Type /FontDescriptor /FontName /BbOracle /Flags 4" +
			" /FontBBox [0 -200 1000 800] /ItalicAngle 0 /Ascent 800" +
			" /Descent -200 /CapHeight 500 /StemV 80 /FontFile3 8 0 R >>",
		fmt.Sprintf("<< /Subtype /CIDFontType0C /Length %d >>\nstream\n%s\nendstream", len(cff), cff),
	})
}

// TestRenderFontBoundsHostileWCIDRange pins byb-6z1's /W range-form bound. A
// /W [0 2147483647 500] descendant must flatten to at most 65536 entries
// (render.Font.W's CID keys are uint16 -- the whole CID space, no more), not
// the 2^31 the declared range asks for -- and this also times the call.
// render.Font.W's uint16 keys already cap the RESULT at 65536 distinct
// entries no matter how the loop is written, so the entry count alone
// cannot tell a bounded scan from one that still walks all 2^31 declared
// CIDs to get there; only the elapsed time can. An unguarded scan of this
// fixture measured ~20s here, so 2s is a ceiling that only a bounded loop
// clears.
func TestRenderFontBoundsHostileWCIDRange(t *testing.T) {
	d, err := pdfdoc.Open(bytes.NewReader(hostileWCIDPDF()))
	if err != nil {
		t.Fatalf("pdfdoc.Open: %v", err)
	}
	p, err := d.Page(1)
	if err != nil {
		t.Fatalf("Page(1): %v", err)
	}
	start := time.Now()
	rf, ok := d.RenderFont(p.Scope, "F1")
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("RenderFont took %v on a /W [0 2147483647 500] fixture; the range-form iteration is not bounded", elapsed)
	}
	if !ok {
		t.Fatal("RenderFont refused the hostile-/W fixture")
	}
	const maxCIDWidthEntries = 65536
	if len(rf.W) != maxCIDWidthEntries {
		t.Fatalf("W has %d entries, want exactly %d (the CID space cap)", len(rf.W), maxCIDWidthEntries)
	}
}

// oneImagePDF: a page whose ONLY content is one 4x4 image drawn 1:1 -- the
// MediaBox is 4x4 points and the CTM `4 0 0 4 0 0`, so at scale 1 every
// device pixel center maps to exactly one source pixel. All 16 pixels are
// distinct, so a flip, transpose or off-by-one cannot pass by luck.
func oneImagePDF() []byte {
	var px []byte
	for i := byte(0); i < 16; i++ {
		px = append(px, i*16, 255-i*16, i*8+40)
	}
	const c = "q 4 0 0 4 0 0 cm /Im0 Do Q"
	return wrapPDF([]string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 4 4]" +
			" /Resources << /XObject << /Im0 5 0 R >> >> /Contents 4 0 R >>",
		fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(c), c),
		flateImageObj(4, 4, px),
	})
}

// TestOneImageMatchesExtractPageRaster pins byb-8b9.2's second acceptance
// clause: a page with one image still matches ExtractPageRaster's existing
// result exactly. Rendering that page 1:1 through the same pdfdoc decode
// seam must reproduce the extracted raster pixel for pixel, so a later stage
// cannot wire render.Page into the extract path with a different orientation
// or placement convention without this failing.
func TestOneImageMatchesExtractPageRaster(t *testing.T) {
	pdf := oneImagePDF()
	pr, err := byblos.ExtractPageRaster(bytes.NewReader(pdf), 1)
	if err != nil {
		t.Fatalf("ExtractPageRaster: %v", err)
	}
	d, err := pdfdoc.Open(bytes.NewReader(pdf))
	if err != nil {
		t.Fatalf("pdfdoc.Open: %v", err)
	}
	p, err := d.Page(1)
	if err != nil {
		t.Fatalf("Page(1): %v", err)
	}
	box := content.Box{LLX: p.CropBox.LLX, LLY: p.CropBox.LLY, URX: p.CropBox.URX, URY: p.CropBox.URY}
	got, err := Page(context.Background(), p.Content, box, 0, 1, pdfdocImages(d, p), nil)
	if err != nil {
		t.Fatalf("render.Page: %v", err)
	}
	eb, gb := pr.Image.Bounds(), got.Bounds()
	if eb.Dx() != gb.Dx() || eb.Dy() != gb.Dy() {
		t.Fatalf("size: extract %v vs render %v", eb, gb)
	}
	for y := 0; y < eb.Dy(); y++ {
		for x := 0; x < eb.Dx(); x++ {
			er, eg, ebl, _ := pr.Image.At(eb.Min.X+x, eb.Min.Y+y).RGBA()
			rr, rg, rb, _ := got.At(gb.Min.X+x, gb.Min.Y+y).RGBA()
			if er != rr || eg != rg || ebl != rb {
				t.Fatalf("pixel (%d,%d): extract (%d,%d,%d) vs render (%d,%d,%d)",
					x, y, er>>8, eg>>8, ebl>>8, rr>>8, rg>>8, rb>>8)
			}
		}
	}
}

// TestRotateAgreesWithPdftoppm is byb-sfo's acceptance: /Rotate turns the
// raster CLOCKWISE for display and swaps the canvas for the quarter turns.
// The page is 240x160, so a canvas that did not swap cannot even reach the
// metric, and the content is asymmetric, so a turn in the WRONG direction
// lands every shape somewhere poppler did not put it.
//
// The null is the mutation check built in: for each rotation, the raster this
// renderer produces for the OPPOSITE turn must FAIL the same tolerance.
// Without it a renderer that ignored /Rotate on a square page would pass.
func TestRotateAgreesWithPdftoppm(t *testing.T) {
	const w, h = 240.0, 160.0
	box := content.Box{LLX: 0, LLY: 0, URX: w, URY: h}
	const tolerance = 0.05
	for _, rot := range []int{0, 90, 180, 270} {
		t.Run(fmt.Sprintf("rotate%d", rot), func(t *testing.T) {
			oracle := pdftoppmPNG(t, vectorPDF(oracleContent, w, h, rot))
			got, err := Page(context.Background(), []byte(oracleContent), box, rot, 1, nil, nil)
			if err != nil {
				t.Fatalf("Page(rotate %d): %v", rot, err)
			}
			frac := mismatchFraction(t, got, oracle)
			t.Logf("rotate %d: mismatch vs pdftoppm %.2f%% of pixels", rot, frac*100)
			if frac > tolerance {
				t.Errorf("rotate %d disagrees with pdftoppm on %.1f%% of pixels; tolerance %.0f%%",
					rot, frac*100, tolerance*100)
			}
			// The null: the OPPOSITE turn must FAIL this oracle. The other
			// two have the transposed canvas and cannot be confused with it.
			wrong := (rot + 180) % 360
			bad, err := Page(context.Background(), []byte(oracleContent), box, wrong, 1, nil, nil)
			if err != nil {
				t.Fatalf("Page(rotate %d): %v", wrong, err)
			}
			if f := mismatchFraction(t, bad, oracle); f <= tolerance {
				t.Errorf("rotate %d passes the rotate-%d oracle at %.1f%% mismatch; the metric is blind to direction",
					wrong, rot, f*100)
			}
		})
	}
}

// TestRotateNotAMultipleOf90 pins the refusal. pdfcpu writes /Rotate 45 with
// a nil error (see pdfdoc/buildpages.go), so this is a file that exists, not
// a hypothetical: rendering it as if it were upright would be a silent lie.
func TestRotateNotAMultipleOf90(t *testing.T) {
	box := content.Box{LLX: 0, LLY: 0, URX: 100, URY: 100}
	if _, err := Page(context.Background(), nil, box, 45, 1, nil, nil); err == nil {
		t.Error("Page(rotate 45) succeeded; want an error")
	}
	// Negative and over-360 values normalise rather than refuse.
	for _, rot := range []int{-90, 360, 450, -270} {
		if _, err := Page(context.Background(), nil, box, rot, 1, nil, nil); err != nil {
			t.Errorf("Page(rotate %d): %v; want it normalised", rot, err)
		}
	}
}

// TestRotateFractionalCanvasAgreesWithPdftoppm is the case
// TestRotateAgreesWithPdftoppm structurally cannot see. Its page is 240x160 at
// scale 1, so the canvas is exact and the ceil slack is zero. Here the page is
// 240.4x160.6: the canvas rounds up on both axes, and translating the turn by
// the CEILED dimension instead of the exact device extent shifts the whole
// raster one pixel and puts the blank slack on the edge opposite poppler's.
// Almost every real page is this case -- the sample harness renders at
// 400/long, which is integral for essentially nothing.
func TestRotateFractionalCanvasAgreesWithPdftoppm(t *testing.T) {
	const w, h = 240.4, 160.6
	box := content.Box{LLX: 0, LLY: 0, URX: w, URY: h}
	const tolerance = 0.05
	for _, rot := range []int{0, 90, 180, 270} {
		t.Run(fmt.Sprintf("rotate%d", rot), func(t *testing.T) {
			oracle := pdftoppmPNG(t, vectorPDF(oracleContent, w, h, rot))
			got, err := Page(context.Background(), []byte(oracleContent), box, rot, 1, nil, nil)
			if err != nil {
				t.Fatalf("Page(rotate %d): %v", rot, err)
			}
			frac := mismatchFraction(t, got, oracle)
			t.Logf("rotate %d on a fractional canvas: mismatch %.2f%%", rot, frac*100)
			if frac > tolerance {
				t.Errorf("rotate %d disagrees with pdftoppm on %.1f%% of pixels; tolerance %.0f%%",
					rot, frac*100, tolerance*100)
			}
			// REGISTRATION, not just overlap, and this is the assertion that
			// does the work. The defect is a whole-raster TRANSLATION of one
			// device pixel, which is under 1.6% of this page and slips under
			// the tolerance above; only the ink position catches it. The
			// ORIGIN of the ink box must match poppler EXACTLY -- a
			// translation moves it and antialiasing does not. The far corner
			// gets a pixel of slack because poppler carries one more
			// half-covered row there than this unantialiased renderer does,
			// at every rotation including 0.
			gx0, gy0, gx1, gy1 := inkBox(got)
			ox0, oy0, ox1, oy1 := inkBox(oracle)
			if gx0 != ox0 || gy0 != oy0 || abs32(gx1-ox1) > 1 || abs32(gy1-oy1) > 1 {
				t.Errorf("rotate %d ink box (%d,%d)-(%d,%d); poppler (%d,%d)-(%d,%d): the raster is shifted",
					rot, gx0, gy0, gx1, gy1, ox0, oy0, ox1, oy1)
			}
		})
	}
}

// inkBox is the bounding box of every pixel that is not near-white.
func inkBox(img image.Image) (x0, y0, x1, y1 int) {
	b := img.Bounds()
	x0, y0, x1, y1 = b.Max.X, b.Max.Y, b.Min.X, b.Min.Y
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			r, g, bl, _ := img.At(x, y).RGBA()
			if r>>8 > 200 && g>>8 > 200 && bl>>8 > 200 {
				continue
			}
			x0, y0 = min(x0, x), min(y0, y)
			x1, y1 = max(x1, x), max(y1, y)
		}
	}
	return
}

func abs32(v int) int {
	if v < 0 {
		return -v
	}
	return v
}
