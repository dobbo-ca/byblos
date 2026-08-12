package byblos

// Poppler agreement for byb-e7n's /Decode array, at the level a caller sees:
// the raster ExtractPageRaster hands back must carry the greys poppler paints.
//
// WHY THIS IS NOT COVERED BY WHAT WAS ALREADY HERE. Three tests touch the same
// seam and none of them can catch an inversion introduced on this path:
//
//   - internal/jbig2's TestPDFDecodeArrayWouldInvert asks pdfimages what the
//     FILTER does. It proves /Decode [1 0] inverts the samples, which is the
//     fact grayLevels is built on, and says nothing about whether byblos
//     applies it.
//   - TestDecodeJBIG2PlacementAppliesTheDecodeArray asserts the levels byblos
//     writes. Those numbers came from a render, but they are now constants in
//     a table, so the test agrees with whatever the table says.
//   - The poppler golden in testdata/oracle carries no JBIG2 page with a
//     /Decode array, because internal/corpus builds none.
//
// So this renders. It is the only assertion in the tree that would fail if
// grayLevels and poppler drifted apart.
//
// THE FIXTURE IS BLOCKY ON PURPOSE. Poppler resamples an image onto the device
// grid, and at 72 dpi over a 101x73 raster that is not a pixel-for-pixel map:
// measured, 6,548 of 7,373 pixels differ from byblos's own decode by up to 252
// levels, all of it edge smoothing on a per-pixel pattern. A 4x4 raster over a
// 160-point page puts 40 device pixels on a side of every cell, and the middle
// half of each cell is then flat colour that either agrees or does not.
//
// It skips when pdftoppm is absent, as internal/jbig2's pdfimages tests do.

import (
	"bytes"
	"fmt"
	"image"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// decodeArrayCells is a 4x4 bitmap that is asymmetric under a transpose and
// under a mirror in either axis, so a raster handed back rotated or flipped
// fails as loudly as one handed back inverted.
func decodeArrayCells() *Bitmap {
	b := NewBitmap(4, 4)
	for y, row := range [4][4]uint8{
		{1, 1, 1, 0},
		{1, 0, 0, 0},
		{0, 0, 1, 0},
		{0, 1, 0, 0},
	} {
		for x, v := range row {
			b.Set(x, y, v)
		}
	}
	return b
}

// jbig2DecodePDF builds a one-page PDF whose only content is a page-covering
// JBIG2 raster of w x h cells, each cell cellPt points square, with extra
// spliced into the image dictionary.
//
// It writes the file by hand rather than going through ReplaceImages, for the
// reason TestDecodeJBIG2PlacementGuardsTheDictionary states: /Decode is the
// entry ReplaceImage deletes (internal/pdfdoc/write.go), so a substituted
// document cannot carry one, and splicing the entry in afterwards invalidates
// the cross-reference table.
func jbig2DecodePDF(data []byte, w, h, cellPt int, extra string) []byte {
	var buf bytes.Buffer
	buf.WriteString("%PDF-1.7\n%\xE2\xE3\xCF\xD3\n")
	offsets := make([]int, 0, 8)
	reserve := func() int { offsets = append(offsets, -1); return len(offsets) }
	fill := func(n int, body string) {
		offsets[n-1] = buf.Len()
		fmt.Fprintf(&buf, "%d 0 obj\n%s\nendobj\n", n, body)
	}
	fillStream := func(n int, dict string, payload []byte) {
		offsets[n-1] = buf.Len()
		fmt.Fprintf(&buf, "%d 0 obj\n<< %s /Length %d >>\nstream\n", n, dict, len(payload))
		buf.Write(payload)
		buf.WriteString("\nendstream\nendobj\n")
	}

	pw, ph := w*cellPt, h*cellPt
	cat, pages, page, cont, img := reserve(), reserve(), reserve(), reserve(), reserve()
	fill(cat, fmt.Sprintf("<< /Type /Catalog /Pages %d 0 R >>", pages))
	fill(pages, fmt.Sprintf("<< /Type /Pages /Kids [%d 0 R] /Count 1 >>", page))
	fill(page, fmt.Sprintf("<< /Type /Page /Parent %d 0 R /MediaBox [0 0 %d %d]"+
		" /Resources << /XObject << /Im0 %d 0 R >> >> /Contents %d 0 R >>",
		pages, pw, ph, img, cont))
	fillStream(cont, "", []byte(fmt.Sprintf("q %d 0 0 %d 0 0 cm /Im0 Do Q\n", pw, ph)))
	fillStream(img, fmt.Sprintf("/Type /XObject /Subtype /Image /Width %d /Height %d"+
		" /ColorSpace /DeviceGray /BitsPerComponent 1 /Filter /JBIG2Decode%s", w, h, extra), data)

	start := buf.Len()
	fmt.Fprintf(&buf, "xref\n0 %d\n0000000000 65535 f \n", len(offsets)+1)
	for _, off := range offsets {
		fmt.Fprintf(&buf, "%010d 00000 n \n", off)
	}
	fmt.Fprintf(&buf, "trailer\n<< /Size %d /Root %d 0 R >>\nstartxref\n%d\n%%%%EOF\n",
		len(offsets)+1, cat, start)
	return buf.Bytes()
}

// renderCellGreys renders pdf at 72 dpi and returns the mean grey of the middle
// half of each of the w x h cells, in raster order.
func renderCellGreys(t *testing.T, pdf []byte, w, h int) []float64 {
	t.Helper()
	dir := t.TempDir()
	src := filepath.Join(dir, "in.pdf")
	if err := os.WriteFile(src, pdf, 0o600); err != nil {
		t.Fatalf("writing the fixture: %v", err)
	}
	prefix := filepath.Join(dir, "out")
	cmd := exec.Command("pdftoppm", "-gray", "-r", "72", "-singlefile", src, prefix)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("pdftoppm: %v: %s", err, out)
	}
	im, err := readPGM(prefix + ".pgm")
	if err != nil {
		t.Fatalf("reading poppler's render: %v", err)
	}
	if im.w%w != 0 || im.h%h != 0 {
		t.Fatalf("poppler rendered %dx%d, which is not a whole number of %dx%d cells",
			im.w, im.h, w, h)
	}
	cw, ch := im.w/w, im.h/h
	out := make([]float64, 0, w*h)
	for cy := 0; cy < h; cy++ {
		for cx := 0; cx < w; cx++ {
			var sum, n float64
			for y := cy*ch + ch/4; y < cy*ch+3*ch/4; y++ {
				for x := cx*cw + cw/4; x < cx*cw+3*cw/4; x++ {
					sum += float64(im.px[y*im.w+x])
					n++
				}
			}
			out = append(out, sum/n)
		}
	}
	return out
}

func TestExtractedJBIG2RasterCarriesThePopplerGreysForADecodeArray(t *testing.T) {
	if _, err := exec.LookPath("pdftoppm"); err != nil {
		t.Skipf("pdftoppm not installed (brew install poppler): %v", err)
	}
	src := decodeArrayCells()
	data, err := EncodeJBIG2Generic(src.Clone())
	if err != nil {
		t.Fatalf("EncodeJBIG2Generic: %v", err)
	}

	for _, decode := range []string{"", " /Decode [0 1]", " /Decode [1 0]", " /Decode [0.5 1]"} {
		name := "absent"
		if decode != "" {
			name = decode[1:]
		}
		t.Run(name, func(t *testing.T) {
			pdf := jbig2DecodePDF(data, src.Width, src.Height, 40, decode)
			want := renderCellGreys(t, pdf, src.Width, src.Height)

			// Both levels must appear in poppler's own render, or the comparison
			// below holds for a raster that got every pixel the same way round.
			distinct := map[int]bool{}
			for _, v := range want {
				distinct[int(v+0.5)] = true
			}
			if len(distinct) < 2 {
				t.Fatalf("poppler painted one level (%v) for %q; this comparison could not "+
					"tell an inversion from a match", want, decode)
			}

			pr, err := ExtractPageRaster(bytes.NewReader(pdf), 1)
			if err != nil {
				t.Fatalf("ExtractPageRaster: %v", err)
			}
			g, ok := pr.Image.(*image.Gray)
			if !ok {
				t.Fatalf("raster is %T; want *image.Gray", pr.Image)
			}
			if b := g.Bounds(); b.Dx() != src.Width || b.Dy() != src.Height {
				t.Fatalf("raster is %dx%d; want %dx%d", b.Dx(), b.Dy(), src.Width, src.Height)
			}

			// One level of slack, and no more: poppler and graySample both round
			// a component to eight bits, and 0.5 lands on 127.5.
			var wrong []string
			for y := 0; y < src.Height; y++ {
				for x := 0; x < src.Width; x++ {
					got := float64(g.Pix[y*g.Stride+x])
					w := want[y*src.Width+x]
					if got-w > 1 || w-got > 1 {
						wrong = append(wrong, fmt.Sprintf("(%d,%d) byblos %g poppler %g", x, y, got, w))
					}
				}
			}
			if len(wrong) != 0 {
				t.Errorf("byblos disagrees with poppler on %d of %d cells for %q:\n  %s\n"+
					"poppler is the specification here (byb-3jq, byb-62t); if it has changed, "+
					"re-measure grayLevels' table rather than adjusting this.",
					len(wrong), src.Width*src.Height, decode,
					strings.Join(wrong, "\n  "))
			}
		})
	}
}
