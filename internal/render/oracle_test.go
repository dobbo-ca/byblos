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
func vectorPDF(contentStream string, w, h float64) []byte {
	return wrapPDF([]string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		fmt.Sprintf("<< /Type /Page /Parent 2 0 R /MediaBox [0 0 %g %g] /Contents 4 0 R >>", w, h),
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
	oracle := pdftoppmPNG(t, vectorPDF(oracleContent, 200, 200))

	box := content.Box{LLX: 0, LLY: 0, URX: 200, URY: 200}
	got, err := Page(context.Background(), []byte(oracleContent), box, 1, nil, nil)
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
	blank, err := Page(context.Background(), nil, box, 1, nil, nil)
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
	got, err := Page(context.Background(), p.Content, box, 1, pdfdocImages(d, p), nil)
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

	blank, err := Page(context.Background(), nil, box, 1, nil, nil)
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
	return buildTTF('A', [][][]gpt{square, triangle, diamond}, []uint16{600, 700, 550})
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
	got, err := Page(context.Background(), []byte(textOracleContent), box, 1, nil, fonts)
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

	blank, err := Page(context.Background(), nil, box, 1, nil, nil)
	if err != nil {
		t.Fatalf("Page(blank): %v", err)
	}
	if frac := mismatchFraction(t, blank, oracle); frac <= tolerance {
		t.Errorf("a BLANK raster is within tolerance of pdftoppm (%.1f%% mismatch); the text oracle metric is broken", frac*100)
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
	got, err := Page(context.Background(), p.Content, box, 1, pdfdocImages(d, p), nil)
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
