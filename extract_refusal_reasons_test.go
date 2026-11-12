package byblos

import (
	"bytes"
	"errors"
	"fmt"
	"testing"
)

// refusalPDF is a one-page PDF whose only content is one 1x1 image with the
// given extra image-dictionary entries and stream bytes, plus any extra objects
// (numbered from 6).
func refusalPDF(img string, data []byte, extra ...string) []byte {
	objs := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 100 100] /Contents 4 0 R /Resources << /XObject << /Im1 5 0 R >> >> >>",
		"<< /Length 28 >>\nstream\nq 100 0 0 100 0 0 cm /Im1 Do Q\nendstream",
		fmt.Sprintf("<< /Type /XObject /Subtype /Image /Width 1 /Height 1 %s /Length %d >>\nstream\n%s\nendstream", img, len(data), data),
	}
	objs = append(objs, extra...)
	var b bytes.Buffer
	b.WriteString("%PDF-1.7\n")
	offs := make([]int, len(objs))
	for i, o := range objs {
		offs[i] = b.Len()
		fmt.Fprintf(&b, "%d 0 obj\n%s\nendobj\n", i+1, o)
	}
	x := b.Len()
	fmt.Fprintf(&b, "xref\n0 %d\n0000000000 65535 f \n", len(objs)+1)
	for _, o := range offs {
		fmt.Fprintf(&b, "%010d 00000 n \n", o)
	}
	fmt.Fprintf(&b, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(objs)+1, x)
	return b.Bytes()
}

// sofJPEG is a JPEG header with a 1x1 frame in the given SOF marker and
// precision, enough for the frame check and no decoder to read.
func sofJPEG(sof, prec byte) []byte {
	return []byte{0xFF, 0xD8, 0xFF, sof, 0, 11, prec, 0, 1, 0, 1, 1, 1, 0x11, 0, 0xFF, 0xDA, 0, 8, 1, 1, 0, 0, 63, 0, 0xFF, 0xD9}
}

func TestExtractRefusesColourSpacesAndDCTVariantsByReason(t *testing.T) {
	const bpc = "/BitsPerComponent 8"
	for _, c := range []struct {
		name, reason string
		pdf          []byte
	}{
		{"lab", "unsupported-colorspace-lab", refusalPDF("/ColorSpace [/Lab << /WhitePoint [0.95 1 1.09] >>] "+bpc, []byte("abc"))},
		{"calgray", "unsupported-colorspace-calgray", refusalPDF("/ColorSpace [/CalGray << /WhitePoint [0.95 1 1.09] >>] "+bpc, []byte("a"))},
		{"indexed-lab", "unsupported-colorspace-indexed-lab", refusalPDF("/ColorSpace [/Indexed [/Lab << /WhitePoint [0.95 1 1.09] >>] 0 <000000>] "+bpc, []byte("a"))},
		{"devicen", "unsupported-colorspace-devicen", refusalPDF("/ColorSpace [/DeviceN [/A /B] [/Lab << /WhitePoint [0.95 1 1.09] >>] 6 0 R] "+bpc, []byte("ab"), "<< /FunctionType 4 /Domain [0 1 0 1] /Range [0 100 -100 100 -100 100] /Length 9 >>\nstream\n{pop pop 0 0 0}\nendstream")},
		{"dct-arith", "unsupported-codec-dct-arith", refusalPDF("/ColorSpace /DeviceGray /Filter /DCTDecode "+bpc, sofJPEG(0xC9, 8))},
		{"dct-lossless", "unsupported-codec-dct-lossless", refusalPDF("/ColorSpace /DeviceGray /Filter /DCTDecode "+bpc, sofJPEG(0xC3, 8))},
		{"dct-12bit", "unsupported-codec-dct-12bit", refusalPDF("/ColorSpace /DeviceGray /Filter /DCTDecode "+bpc, sofJPEG(0xC1, 12))},
	} {
		_, err := ExtractPageRaster(bytes.NewReader(c.pdf), 1)
		var ref *RasterRefusal
		if !errors.As(err, &ref) {
			t.Errorf("%s: err = %v; want a *RasterRefusal", c.name, err)
			continue
		}
		if ref.Reason != c.reason || ref.Class == "" {
			t.Errorf("%s: Reason=%q Class=%q; want %q and a class", c.name, ref.Reason, ref.Class, c.reason)
		}
	}
}
