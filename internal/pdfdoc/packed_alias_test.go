package pdfdoc_test

// A packed read must not flip the document's own image bytes. An encrypted file
// loads eagerly and an unfiltered 1-bpc image has no filter left to decode, so
// the decoded content is the stream's Raw itself; packing it in place used to
// invert the next read of the same image.

import (
	"bytes"
	"context"
	"fmt"
	"slices"
	"testing"

	"github.com/dobbo-ca/byblos"
	"github.com/dobbo-ca/pdf-go/pkg/api"
	"github.com/dobbo-ca/pdf-go/pkg/pdfcpu/model"
)

func TestPackedReadLeavesTheDocumentsImageBytesAlone(t *testing.T) {
	objs := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R 4 0 R] /Count 2 >>",
	}
	for range 2 {
		n := len(objs) + 1
		objs = append(objs, fmt.Sprintf("<< /Type /Page /Parent 2 0 R /MediaBox [0 0 8 2] /Contents %d 0 R /Resources << /XObject << /Im0 6 0 R >> >> >>", n+2))
	}
	body := "q 8 0 0 2 0 0 cm /Im0 Do Q"
	objs = append(objs,
		fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(body), body),
		fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(body), body),
		"<< /Type /XObject /Subtype /Image /Width 8 /Height 2 /ColorSpace /DeviceGray /BitsPerComponent 1 /Length 2 >>\nstream\n\x0f\xf0\nendstream")
	// object 5 is page 2's page object, so fix the contents refs: pages are 3, 4; contents 5, 6; image 7
	objs[2] = "<< /Type /Page /Parent 2 0 R /MediaBox [0 0 8 2] /Contents 5 0 R /Resources << /XObject << /Im0 7 0 R >> >> >>"
	objs[3] = "<< /Type /Page /Parent 2 0 R /MediaBox [0 0 8 2] /Contents 6 0 R /Resources << /XObject << /Im0 7 0 R >> >> >>"
	var b bytes.Buffer
	b.WriteString("%PDF-1.7\n")
	offs := make([]int, len(objs))
	for i, o := range objs {
		offs[i] = b.Len()
		fmt.Fprintf(&b, "%d 0 obj\n%s\nendobj\n", i+1, o)
	}
	xref := b.Len()
	fmt.Fprintf(&b, "xref\n0 %d\n0000000000 65535 f \n", len(objs)+1)
	for _, o := range offs {
		fmt.Fprintf(&b, "%010d 00000 n \n", o)
	}
	fmt.Fprintf(&b, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(objs)+1, xref)

	var enc bytes.Buffer
	if err := api.Encrypt(context.Background(), bytes.NewReader(b.Bytes()), &enc, model.NewAESConfiguration("", "owner", 128)); err != nil {
		t.Fatal(err)
	}
	doc, err := byblos.OpenDocument(context.Background(), bytes.NewReader(enc.Bytes()), byblos.Limits{PackedBitonal: true})
	if err != nil {
		t.Fatal(err)
	}
	defer doc.Close()
	var first []byte
	for _, p := range []int{1, 2, 1} {
		pr, err := doc.PageRaster(context.Background(), p)
		if err != nil {
			t.Fatal(err)
		}
		if pr.Bitmap == nil {
			t.Fatalf("page %d: no packed bitmap", p)
		}
		got := slices.Clone(pr.Bitmap.Pix)
		clear(pr.Bitmap.Pix) // a caller writing to Pix must not reach the document either
		if first == nil {
			first = got
		} else if !bytes.Equal(got, first) {
			t.Errorf("page %d: bitmap %x, first read was %x", p, got, first)
		}
	}
}
