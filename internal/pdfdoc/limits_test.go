package pdfdoc

import (
	"bytes"
	"compress/zlib"
	"context"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"testing"

	"github.com/dobbo-ca/byblos/internal/limits"
)

func TestTrailerSize(t *testing.T) {
	classic := "%PDF-1.4\n1 0 obj\n<< >>\nendobj\nxref\n0 1\n0000000000 65535 f \ntrailer\n<< /Size 424242 /Root 1 0 R >>\nstartxref\n20\n%%EOF\n"
	xrefStream := func(pad int) string {
		head := "%PDF-1.5\n" + strings.Repeat("%", pad)
		return fmt.Sprintf("%s\n7 0 obj\n<< /Type /XRef /Size 777777 /W [1 2 1] /Length 0 >>\nstream\n\nendstream\nendobj\nstartxref\n%d\n%%%%EOF\n", head, len(head)+1)
	}
	incremental := classic + "xref\n0 1\n0000000000 65535 f \ntrailer\n<< /Size 424243 /Prev 20 >>\nstartxref\n99\n%%EOF\n"
	for _, c := range []struct {
		name string
		pdf  string
		want int64
		ok   bool
	}{
		{"classic", classic, 424242, true},
		{"last trailer of an incremental update wins", incremental, 424243, true},
		{"xref stream in the tail", xrefStream(10), 777777, true},
		{"xref stream before the tail", xrefStream(200 << 10), 777777, true},
		{"no startxref", "%PDF-1.4\n garbage", 0, false},
		{"empty", "", 0, false},
	} {
		got, ok := trailerSize(bytes.NewReader([]byte(c.pdf)))
		if got != c.want || ok != c.ok {
			t.Errorf("%s: (%d, %v); want (%d, %v)", c.name, got, ok, c.want, c.ok)
		}
	}
}

func limitsPDF(bodies []string) []byte {
	var b bytes.Buffer
	b.WriteString("%PDF-1.7\n")
	offs := make([]int, len(bodies))
	for i, body := range bodies {
		offs[i] = b.Len()
		fmt.Fprintf(&b, "%d 0 obj\n%s\nendobj\n", i+1, body)
	}
	xref := b.Len()
	fmt.Fprintf(&b, "xref\n0 %d\n0000000000 65535 f \n", len(bodies)+1)
	for _, o := range offs {
		fmt.Fprintf(&b, "%010d 00000 n \n", o)
	}
	fmt.Fprintf(&b, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(bodies)+1, xref)
	return b.Bytes()
}

func limitsZeros(t *testing.T, n int) []byte {
	t.Helper()
	var out bytes.Buffer
	zw := zlib.NewWriter(&out)
	chunk := make([]byte, 1<<20)
	for left := n; left > 0; left -= len(chunk) {
		if left < len(chunk) {
			chunk = chunk[:left]
		}
		if _, err := zw.Write(chunk); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

// imagePDF is a one-page document holding one /DeviceGray image XObject named
// Im0 whose header says w x h and whose data is the given bytes.
func imagePDF(w, h int, data []byte, filter string) []byte {
	return limitsPDF([]string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 100 100] /Contents 4 0 R /Resources << /XObject << /Im0 5 0 R >> >> >>",
		"<< /Length 20 >>\nstream\nq 100 0 0 100 0 0 cm /Im0 Do Q\nendstream",
		fmt.Sprintf("<< /Type /XObject /Subtype /Image /Width %d /Height %d /BitsPerComponent 8 /ColorSpace /DeviceGray %s /Length %d >>\nstream\n%s\nendstream", w, h, filter, len(data), data),
	})
}

func rawImage(t *testing.T, pdf []byte, lim limits.Limits) error {
	t.Helper()
	d, err := openDoc(context.Background(), bytes.NewReader(pdf), lim)
	if err != nil {
		t.Fatal(err)
	}
	p, err := d.Page(1)
	if err != nil {
		t.Fatal(err)
	}
	xo, ok := d.XObject(p.Scope, "Im0")
	if !ok {
		t.Fatal("image not resolved")
	}
	_, _, err = d.RawImage(xo.ID)
	return err
}

// pdfcpu decodes image streams under its own 512 MiB limit unless told
// otherwise (experiment 4); byblos sets it to the image's declared size.
func TestImageDecodeIsBoundedByTheDeclaredSize(t *testing.T) {
	// Declares 10 x 10 and carries 50 MiB of deflated limitsZeros.
	bomb := imagePDF(10, 10, limitsZeros(t, 50<<20), "/Filter /FlateDecode")
	before := limitsTotalAlloc()
	err := rawImage(t, bomb, limits.Limits{})
	delta := limitsTotalAlloc() - before
	var le *limits.LimitError
	if !errors.As(err, &le) || le.Name != "image-bytes" {
		t.Fatalf("err = %v; want *LimitError image-bytes", err)
	}
	if delta > 8<<20 {
		t.Errorf("TotalAlloc delta = %d; the decode should stop at the declared size (~410 bytes), not inflate the 50 MiB", delta)
	}

	// An honest image of the same header decodes.
	ok := imagePDF(10, 10, limitsZeros(t, 100), "/Filter /FlateDecode")
	if err := rawImage(t, ok, limits.Limits{}); err != nil {
		t.Errorf("honest 10 x 10 image: %v", err)
	}
}

func TestImagePixelCapIsCheckedFromTheHeader(t *testing.T) {
	pdf := imagePDF(100_000, 100_000, []byte{0}, "")
	before := limitsTotalAlloc()
	err := rawImage(t, pdf, limits.Limits{})
	delta := limitsTotalAlloc() - before
	var le *limits.LimitError
	if !errors.As(err, &le) || le.Name != "image-pixels" {
		t.Fatalf("err = %v; want *LimitError image-pixels", err)
	}
	if delta > 1<<20 {
		t.Errorf("TotalAlloc delta = %d; want < 1 MiB", delta)
	}
}

func limitsTotalAlloc() uint64 {
	var m runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&m)
	return m.TotalAlloc
}
