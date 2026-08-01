package jbig2

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// buildTestPDF writes a one-page PDF whose only content is a JBIG2 image
// XObject covering the page. decodeEntry is spliced into the image dictionary
// so the test can compare the presence and absence of a /Decode array.
//
// This writer exists only to feed poppler. Real PDF assembly is byb-b1/byb-b5.
func buildTestPDF(b *Bitmap, jb2 []byte, decodeEntry string) []byte {
	var buf bytes.Buffer
	var offsets []int
	obj := func(head string, stream []byte) {
		offsets = append(offsets, buf.Len())
		buf.WriteString(head)
		if stream != nil {
			buf.Write(stream)
			buf.WriteString("\nendstream\nendobj\n")
		}
	}

	buf.WriteString("%PDF-1.4\n")
	obj("1 0 obj\n<< /Type /Catalog /Pages 2 0 R >>\nendobj\n", nil)
	obj("2 0 obj\n<< /Type /Pages /Kids [3 0 R] /Count 1 >>\nendobj\n", nil)
	obj(fmt.Sprintf("3 0 obj\n<< /Type /Page /Parent 2 0 R /MediaBox [0 0 %d %d] "+
		"/Resources << /XObject << /Im0 5 0 R >> >> /Contents 4 0 R >>\nendobj\n", b.W, b.H), nil)
	content := fmt.Sprintf("q %d 0 0 %d 0 0 cm /Im0 Do Q", b.W, b.H)
	obj(fmt.Sprintf("4 0 obj\n<< /Length %d >>\nstream\n", len(content)), []byte(content))
	obj(fmt.Sprintf("5 0 obj\n<< /Type /XObject /Subtype /Image /Width %d /Height %d "+
		"/ColorSpace /DeviceGray /BitsPerComponent 1 %s/Filter /JBIG2Decode /Length %d >>\nstream\n",
		b.W, b.H, decodeEntry, len(jb2)), jb2)

	xref := buf.Len()
	fmt.Fprintf(&buf, "xref\n0 %d\n0000000000 65535 f \n", len(offsets)+1)
	for _, o := range offsets {
		fmt.Fprintf(&buf, "%010d 00000 n \n", o)
	}
	fmt.Fprintf(&buf, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n",
		len(offsets)+1, xref)
	return buf.Bytes()
}

// extractWithPdfimages runs poppler's pdfimages over a PDF and returns the
// single image it extracts, as a bitmap.
func extractWithPdfimages(t *testing.T, bin string, pdf []byte) *Bitmap {
	t.Helper()
	dir := t.TempDir()
	in := filepath.Join(dir, "in.pdf")
	if err := os.WriteFile(in, pdf, 0o644); err != nil {
		t.Fatalf("writing %s: %v", in, err)
	}
	cmd := exec.Command(bin, in, filepath.Join(dir, "img"))
	if combined, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("pdfimages failed: %v\n%s", err, combined)
	}
	matches, err := filepath.Glob(filepath.Join(dir, "img-*.pbm"))
	if err != nil || len(matches) != 1 {
		t.Fatalf("pdfimages produced %v (err %v); want exactly one .pbm", matches, err)
	}
	raw, err := os.ReadFile(matches[0])
	if err != nil {
		t.Fatalf("reading %s: %v", matches[0], err)
	}
	got, err := decodePBM(raw)
	if err != nil {
		t.Fatalf("parsing pdfimages output: %v", err)
	}
	return got
}

// TestPDFEmbeddingIsBitIdentical is the second, independent decoder oracle: the
// stream goes through a real PDF and poppler's own JBIG2 implementation, not
// jbig2dec. Two independent decoders agreeing is far stronger evidence than one.
func TestPDFEmbeddingIsBitIdentical(t *testing.T) {
	bin, err := exec.LookPath("pdfimages")
	if err != nil {
		t.Skipf("pdfimages not installed (brew install poppler): %v", err)
	}
	for name, want := range fixtureBitmaps() {
		t.Run(name, func(t *testing.T) {
			want.MaskPadding()
			jb2, err := EmbeddedStream(want)
			if err != nil {
				t.Fatalf("EmbeddedStream() error = %v", err)
			}
			pdf := buildTestPDF(want, jb2, "")
			assertBitmapsIdentical(t, name, extractWithPdfimages(t, bin, pdf), want)
		})
	}
}

// TestPDFDecodeArrayWouldInvert pins the polarity decision. JBIG2 1 = black and
// DeviceGray 1 = white, so it is not obvious which way round the filter works;
// ISO 32000-1 7.4.7 does not say. This asserts both directions: without /Decode
// the image is correct, and with /Decode [1 0] it is exactly inverted.
//
// If this ever starts failing, the fix is to change EncodeJBIG2Generic's
// documented dictionary entries -- not to "adjust" the encoder, which is pinned
// by the Annex H.1 vector.
func TestPDFDecodeArrayWouldInvert(t *testing.T) {
	bin, err := exec.LookPath("pdfimages")
	if err != nil {
		t.Skipf("pdfimages not installed (brew install poppler): %v", err)
	}
	want := figureH6()
	jb2, err := EmbeddedStream(want)
	if err != nil {
		t.Fatalf("EmbeddedStream() error = %v", err)
	}

	plain := extractWithPdfimages(t, bin, buildTestPDF(want, jb2, ""))
	assertBitmapsIdentical(t, "no /Decode", plain, want)

	inverted := extractWithPdfimages(t, bin, buildTestPDF(want, jb2, "/Decode [1 0] "))
	if inverted.W != want.W || inverted.H != want.H {
		t.Fatalf("/Decode [1 0] produced %dx%d; want %dx%d",
			inverted.W, inverted.H, want.W, want.H)
	}
	var same int
	for y := 0; y < want.H; y++ {
		for x := 0; x < want.W; x++ {
			if inverted.Get(x, y) == want.Get(x, y) {
				same++
			}
		}
	}
	if same != 0 {
		t.Errorf("/Decode [1 0] matched the source in %d of %d pixels; expected a total inversion. "+
			"If the filter's polarity has changed, update EncodeJBIG2Generic's documented image dictionary.",
			same, want.W*want.H)
	}
}
