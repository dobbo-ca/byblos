package byblos

// byb-9v0 at the boundary a caller sees: symbol-mode JBIG2 through
// DecodeJBIG2Generic and through ExtractPageRaster, including the
// /DecodeParms /JBIG2Globals plumbing that most of it needs.
//
// internal/jbig2's symbol_test.go proves the DECODER matches jbig2dec. These
// prove the two things above it that a decoder cannot: that a self-contained
// symbol-mode stream reaches the decoder at all, and that a page whose
// dictionary lives in a separate PDF object reaches it WITH that object.
//
// THE SECOND ONE IS THE WHOLE OF byb-9v0'S PLUMBING and it fails silently if it
// is wrong. A text region with no dictionary places nothing, so a decoder that
// never received the globals returns a blank page of the right size and no
// error -- which passes every size and polarity check byblos makes.

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	"os"
	"path/filepath"
	"testing"
)

func symbolFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "jbig2", name))
	if err != nil {
		t.Fatalf("reading fixture %s: %v", name, err)
	}
	return b
}

// symbolInlineStream is the fixture's two streams concatenated, which is a legal
// self-contained embedded stream and not a splice: the dictionary is associated
// with page 0, which T.88 7.2.6 gives to a segment on no page, and the page
// information and text region segments keep their own numbering and their own
// page 1. It is the shape of the 3,446 corpus pages whose symbol dictionary is
// in the image stream itself, against the 263 whose dictionary is not.
func symbolInlineStream(t *testing.T) []byte {
	t.Helper()
	globals := symbolFixture(t, "symbol-globals.jb2")
	page := symbolFixture(t, "symbol-page.jb2")
	return append(bytes.Clone(globals), page...)
}

// symbolInkCount counts ink in a decoded Bitmap. The fixture carries 45,104 ink
// pixels; a blank page carries none, which is the failure this distinguishes.
func symbolInkCount(b *Bitmap) int {
	n := 0
	for _, v := range b.Pix {
		for ; v != 0; v &= v - 1 {
			n++
		}
	}
	return n
}

const symbolFixtureInk = 45104

func TestDecodeJBIG2GenericDecodesSymbolModeAndDeclinesItsHuffmanVariant(t *testing.T) {
	s := symbolInlineStream(t)

	got, err := DecodeJBIG2Generic(s)
	if err != nil {
		t.Fatalf("DecodeJBIG2Generic on a self-contained symbol-mode stream: %v", err)
	}
	if got.Width != 640 || got.Height != 480 {
		t.Fatalf("decoded %dx%d; want 640x480", got.Width, got.Height)
	}
	if n := symbolInkCount(got); n != symbolFixtureInk {
		t.Errorf("decoded %d ink pixels; want %d", n, symbolFixtureInk)
	}

	// The symbol dictionary's flags word is at bytes 11 and 12: an 11-byte
	// segment header with no referred-to segments, then the two flag bytes. Bit
	// 0 of the low byte is SDHUFF.
	if s[11] != 0 || s[12] != 0 {
		t.Fatalf("fixture symbol dictionary flags are %02X %02X; want 00 00", s[11], s[12])
	}
	huff := bytes.Clone(s)
	huff[12] = 0x01
	bad, err := DecodeJBIG2Generic(huff)
	if err == nil {
		t.Fatalf("DecodeJBIG2Generic returned a %dx%d bitmap for a Huffman symbol dictionary; "+
			"want ErrUnsupportedJBIG2Feature", bad.Width, bad.Height)
	}
	if !errors.Is(err, ErrUnsupportedJBIG2Feature) {
		t.Fatalf("error = %v; want ErrUnsupportedJBIG2Feature. An archive decides whether a page is "+
			"recoverable by a future decoder or is damaged on exactly this difference (jbig2.go)", err)
	}
	if bad != nil {
		t.Error("a bitmap was returned alongside the error")
	}
}

// jbig2GlobalsPDF builds a one-page PDF whose JBIG2 image carries its page-0
// segments in a /DecodeParms /JBIG2Globals stream, the way a bulk scanner
// writes a document.
func jbig2GlobalsPDF(page, globals []byte, w, h int) []byte {
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

	cat, pages, pg, cont, img, glob := reserve(), reserve(), reserve(), reserve(), reserve(), reserve()
	fill(cat, fmt.Sprintf("<< /Type /Catalog /Pages %d 0 R >>", pages))
	fill(pages, fmt.Sprintf("<< /Type /Pages /Kids [%d 0 R] /Count 1 >>", pg))
	fill(pg, fmt.Sprintf("<< /Type /Page /Parent %d 0 R /MediaBox [0 0 %d %d]"+
		" /Resources << /XObject << /Im0 %d 0 R >> >> /Contents %d 0 R >>", pages, w, h, img, cont))
	fillStream(cont, "", []byte(fmt.Sprintf("q %d 0 0 %d 0 0 cm /Im0 Do Q\n", w, h)))
	fillStream(img, fmt.Sprintf("/Type /XObject /Subtype /Image /Width %d /Height %d"+
		" /ColorSpace /DeviceGray /BitsPerComponent 1 /Filter /JBIG2Decode"+
		" /DecodeParms << /JBIG2Globals %d 0 R >>", w, h, glob), page)
	fillStream(glob, "", globals)

	start := buf.Len()
	fmt.Fprintf(&buf, "xref\n0 %d\n0000000000 65535 f \n", len(offsets)+1)
	for _, off := range offsets {
		fmt.Fprintf(&buf, "%010d 00000 n \n", off)
	}
	fmt.Fprintf(&buf, "trailer\n<< /Size %d /Root %d 0 R >>\nstartxref\n%d\n%%%%EOF\n",
		len(offsets)+1, cat, start)
	return buf.Bytes()
}

// The page byb-9v0's plumbing exists for: a text region in the image stream and
// its symbol dictionary in another PDF object.
func TestExtractPageRasterReadsAJBIG2GlobalsDictionary(t *testing.T) {
	pdf := jbig2GlobalsPDF(symbolFixture(t, "symbol-page.jb2"),
		symbolFixture(t, "symbol-globals.jb2"), 640, 480)

	pr, err := ExtractPageRaster(bytes.NewReader(pdf), 1)
	if err != nil {
		t.Fatalf("ExtractPageRaster: %v", err)
	}
	g, ok := pr.Image.(*image.Gray)
	if !ok {
		t.Fatalf("raster is %T; want *image.Gray", pr.Image)
	}
	if b := g.Bounds(); b.Dx() != 640 || b.Dy() != 480 {
		t.Fatalf("raster is %dx%d; want 640x480", b.Dx(), b.Dy())
	}
	// Ink is sample 0 with no /Decode array. Counting it is what separates a
	// decoded page from the blank one a missing dictionary would produce, and a
	// blank page is what this test exists to fail on.
	ink := 0
	for _, v := range g.Pix {
		if v == 0 {
			ink++
		}
	}
	if ink != symbolFixtureInk {
		t.Errorf("raster carries %d ink pixels; want %d. Zero means the text region was decoded "+
			"without the symbol dictionary /DecodeParms names, which places nothing and returns a "+
			"blank page of the right size rather than an error", ink, symbolFixtureInk)
	}
}

// The same document with the globals object removed must DIVERT, not hand back
// a blank page.
func TestJBIG2PageWithAMissingGlobalsObjectDiverts(t *testing.T) {
	pdf := jbig2GlobalsPDF(symbolFixture(t, "symbol-page.jb2"), nil, 640, 480)
	pr, err := ExtractPageRaster(bytes.NewReader(pdf), 1)
	if err == nil {
		t.Fatalf("ExtractPageRaster returned a %v raster for a text region with an empty symbol "+
			"dictionary object", pr.Image.Bounds())
	}
	if !errors.Is(err, ErrUnsupportedImageCodec) {
		t.Fatalf("error = %v; want ErrUnsupportedImageCodec", err)
	}
}
