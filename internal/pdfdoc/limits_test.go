package pdfdoc

import (
	"bytes"
	"compress/zlib"
	"context"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
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
	// Linearized: startxref points at the first-page xref at the top; the main
	// trailer (small /Size) is last.
	first := "%PDF-1.4\n1 0 obj\n<< >>\nendobj\n"
	lin := first + "xref\n0 1\n0000000000 65535 f \ntrailer\n<< /Size 77 /Prev 99 >>\nstartxref\n0\n%%EOF\n"
	linearized := lin + "xref\n0 1\n0000000000 65535 f \ntrailer\n<< /Size 2 >>\nstartxref\n" + fmt.Sprint(len(first)) + "\n%%EOF\n"
	// Leading junk shifts every offset the file states.
	junkXref := "%PDF-1.5\n7 0 obj\n<< /Type /XRef /Size 888 /W [1 2 1] /Length 0 >>\nstream\n\nendstream\nendobj\nstartxref\n9\n%%EOF\n"
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
		{"linearized classic: first-page trailer, not the last", linearized, 77, true},
		{"xref stream after leading junk", strings.Repeat(" ", 20) + junkXref, 888, true},
		{"xref stream after 70 KiB of leading junk", strings.Repeat(" ", 70<<10) + junkXref, 888, true},
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

// A tiny image whose JPEG is larger than its declared pixels, Flate-wrapped as
// ocrmypdf -O1 writes it, is valid.
func TestRawImageFlateWrappedDCTOfATinyImage(t *testing.T) {
	img := image.NewGray(image.Rect(0, 0, 8, 8))
	for i := range img.Pix {
		img.Pix[i] = uint8(i * 37)
	}
	var jb bytes.Buffer
	if err := jpeg.Encode(&jb, img, &jpeg.Options{Quality: 95}); err != nil {
		t.Fatal(err)
	}
	var zb bytes.Buffer
	zw := zlib.NewWriter(&zb)
	zw.Write(jb.Bytes())
	zw.Close()
	if jb.Len() <= 264 {
		t.Fatalf("fixture JPEG is %d bytes; the test needs more than the 264 declared", jb.Len())
	}
	pdf := imagePDF(8, 8, zb.Bytes(), "/Filter [/FlateDecode /DCTDecode]")
	if err := rawImage(t, pdf, limits.Limits{}); err != nil {
		t.Errorf("8 x 8 Flate+DCT: %v", err)
	}
}

// pdfcpu reads a decode limit of 0 as its 512 MiB default and a negative one as
// none, so a header that declares no positive size must not reach it.
func TestRawImageForgedDimensionsAreRefusedBeforeDecoding(t *testing.T) {
	bomb := limitsZeros(t, 300<<20)
	for _, c := range []struct {
		name string
		w, h int
	}{
		{"negative width", -1, 100_000_000},
		{"zero width", 0, 0},
		{"negative both", -1, -1},
	} {
		t.Run(c.name, func(t *testing.T) {
			before := limitsTotalAlloc()
			err := rawImage(t, imagePDF(c.w, c.h, bomb, "/Filter /FlateDecode"), limits.Limits{})
			delta := limitsTotalAlloc() - before
			if err == nil {
				t.Error("err = nil; want a refusal")
			}
			if delta > 64<<20 {
				t.Errorf("TotalAlloc delta = %d; the bomb was inflated", delta)
			}
		})
	}
}

// A trailer that understates /Size is caught by pdfcpu's own object count, and
// surfaces as the typed limit.
func TestUnderReportedSizeIsAnObjectsLimit(t *testing.T) {
	bodies := []string{"<< /Type /Catalog /Pages 2 0 R >>", "<< /Type /Pages /Kids [] /Count 0 >>"}
	for i := 0; i < 2000; i++ {
		bodies = append(bodies, "<< >>")
	}
	pdf := limitsPDF(bodies)
	pdf = bytes.Replace(pdf, []byte(fmt.Sprintf("/Size %d", len(bodies)+1)), []byte("/Size 10"), 1)
	_, err := openDoc(context.Background(), bytes.NewReader(pdf), limits.Limits{MaxObjects: 1000})
	var le *limits.LimitError
	if !errors.As(err, &le) || le.Name != "objects" {
		t.Errorf("err = %v; want *LimitError objects", err)
	}
}

// Parse entry points other than Open (Optimize, Properties, the writer's
// validator) take the package defaults, not pdfcpu's 512 MiB and 10M objects.
func TestDefaultConfigCarriesTheLimits(t *testing.T) {
	c := defaultConfig()
	if c.Limits.MaxDecodeBytes != (limits.Limits{}).StreamBytes() || int64(c.Limits.MaxObjectCount) != (limits.Limits{}).Objects() {
		t.Errorf("MaxDecodeBytes %d, MaxObjectCount %d", c.Limits.MaxDecodeBytes, c.Limits.MaxObjectCount)
	}
}

// A file with no startxref has no readable /Size (pdfcpu rebuilds its table).
// OpenBudgeted must still take a 3 MiB content stream under a 300 MiB budget.
func TestOpenBudgetedWithoutASizeKeepsAUsableStreamCap(t *testing.T) {
	defer limits.SetMemoryLimit(limits.SetMemoryLimit(300 << 20))
	data := limitsZeros(t, 3<<20)
	pdf := limitsPDF([]string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 100 100] /Contents 4 0 R >>",
		fmt.Sprintf("<< /Filter /FlateDecode /Length %d >>\nstream\n%s\nendstream", len(data), data),
	})
	pdf = pdf[:bytes.LastIndex(pdf, []byte("startxref"))]
	d, release, err := OpenBudgeted(context.Background(), bytes.NewReader(pdf), limits.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if _, err := d.Page(1); err != nil {
		t.Errorf("Page(1): %v", err)
	}
}

// openBudgetedOK opens pdf under a 300 MiB budget and reads page 1.
func openBudgetedOK(t *testing.T, pdf []byte) {
	t.Helper()
	defer limits.SetMemoryLimit(limits.SetMemoryLimit(300 << 20))
	d, release, err := OpenBudgeted(context.Background(), bytes.NewReader(pdf), limits.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if _, err := d.Page(1); err != nil {
		t.Errorf("Page(1): %v", err)
	}
}

// More than 64 KiB after %%EOF must not change what /Size the budget sees: a
// valid 120k-object file and a 50 MiB content stream pass padded as unpadded.
func TestPaddedFilesPassLikeUnpaddedOnes(t *testing.T) {
	pad := bytes.Repeat([]byte(" "), 70<<10)
	bodies := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 100 100] >>",
	}
	for len(bodies) < 120_000 {
		bodies = append(bodies, "<< >>")
	}
	data := limitsZeros(t, 50<<20)
	big := limitsPDF([]string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 100 100] /Contents 4 0 R >>",
		fmt.Sprintf("<< /Filter /FlateDecode /Length %d >>\nstream\n%s\nendstream", len(data), data),
	})
	for name, pdf := range map[string][]byte{"120k objects": limitsPDF(bodies), "50 MiB stream": big} {
		t.Run(name, func(t *testing.T) {
			openBudgetedOK(t, pdf)
			openBudgetedOK(t, append(append([]byte{}, pdf...), pad...))
		})
	}
}

// pdfcpu's rebuild of a table it cannot read ignores its object cap, so with no
// usable /Size the file's object headers are counted and held to the cap.
func TestRebuiltTableIsHeldToTheObjectCap(t *testing.T) {
	defer limits.SetMemoryLimit(limits.SetMemoryLimit(300 << 20))
	bodies := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 100 100] >>",
	}
	for len(bodies) < 210_000 {
		bodies = append(bodies, "<< >>")
	}
	pdf := limitsPDF(bodies)
	// No xref, no trailer; and a startxref that lands on an object.
	noXref := pdf[:bytes.Index(pdf, []byte("xref\n0 "))]
	badOff := bytes.Replace(pdf, []byte("startxref\n"), []byte("startxref\n9 "), 1)
	for name, f := range map[string][]byte{"no xref": noXref, "startxref on an object": badOff} {
		_, release, err := OpenBudgeted(context.Background(), bytes.NewReader(f), limits.Limits{})
		if err == nil {
			release()
		}
		var le *limits.LimitError
		if !errors.As(err, &le) || le.Name != "objects" {
			t.Errorf("%s: err = %v; want *LimitError objects", name, err)
		}
	}
}

// A file a reader repairs may have lost its /Size; its big stream must still
// fit the stream cap a 300 MiB budget allows.
func TestRepairedFileKeepsItsStreamCap(t *testing.T) {
	bodies := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 100 100] /Contents 4 0 R >>",
	}
	data := limitsZeros(t, 50<<20)
	bodies = append(bodies, fmt.Sprintf("<< /Filter /FlateDecode /Length %d >>\nstream\n%s\nendstream", len(data), data))
	for len(bodies) < 1000 {
		bodies = append(bodies, "<< >>")
	}
	pdf := limitsPDF(bodies)
	i := bytes.LastIndex(pdf, []byte("startxref\n")) + len("startxref\n")
	onObject := append(append([]byte{}, pdf[:i]...), "9\n%%EOF\n"...)
	noNumber := append(append([]byte{}, pdf...), "junk startxref\n"...)
	for name, f := range map[string][]byte{"startxref on an object": onObject, "startxref without a number": noNumber} {
		t.Run(name, func(t *testing.T) { openBudgetedOK(t, f) })
	}
}

// plainObjects is a catalog, a page tree and a page followed by n-3 empty
// objects, with no xref or trailer.
func plainObjects(n int) *bytes.Buffer {
	var b bytes.Buffer
	b.WriteString("%PDF-1.7\n")
	for i := 1; i <= n; i++ {
		body := "<< >>"
		switch i {
		case 1:
			body = "<< /Type /Catalog /Pages 2 0 R >>"
		case 2:
			body = "<< /Type /Pages /Kids [3 0 R] /Count 1 >>"
		case 3:
			body = "<< /Type /Page /Parent 2 0 R /MediaBox [0 0 100 100] >>"
		}
		fmt.Fprintf(&b, "%d 0 obj\n%s\nendobj\n", i, body)
	}
	return &b
}

func wantObjectsLimit(t *testing.T, name string, pdf []byte, lim limits.Limits) {
	t.Helper()
	_, release, err := OpenBudgeted(context.Background(), bytes.NewReader(pdf), lim)
	if err == nil {
		release()
	}
	var le *limits.LimitError
	if !errors.As(err, &le) || le.Name != "objects" {
		t.Errorf("%s: err = %v; want *LimitError objects", name, err)
	}
}

// A trailer is only a claim: whatever /Size it makes (a corrupt xref stream
// that says 10, a startxref that lands on an object with a /Size 10 nearby),
// the objects in the file are what pdfcpu rebuilds and holds.
func TestObjectBudgetDoesNotRestOnTheTrailer(t *testing.T) {
	defer limits.SetMemoryLimit(limits.SetMemoryLimit(300 << 20))
	lim := limits.Limits{MaxObjects: 1000}
	xs := plainObjects(5000)
	fmt.Fprintf(xs, "5001 0 obj\n<< /Type /XRef /Size 10 /Root 1 0 R /W [1 4 2] /Filter /FlateDecode /Length 5 >>\nstream\nxxxxx\nendstream\nendobj\nstartxref\n%d\n%%%%EOF\n", xs.Len())
	wantObjectsLimit(t, "corrupt xref stream", xs.Bytes(), lim)

	sz := plainObjects(5000)
	sz.WriteString("%/Size 10\nstartxref\n9\n%%EOF\n")
	wantObjectsLimit(t, "startxref on an object", sz.Bytes(), lim)
}

// pdfcpu reads "<k> 5 0 obj", "<k>.5 0 obj" and "<k>0000000005 0 obj" as
// object k, not 5, so the bound counts tokens, not numbers.
func TestObjectBoundCountsEveryHeaderForm(t *testing.T) {
	for _, form := range []string{"%d 5 0 obj", "%d.5 0 obj", "%dx5 0 obj", "%d0000000005 0 obj"} {
		var sb strings.Builder
		sb.WriteString("%PDF-1.4\n")
		for k := 1; k <= 1000; k++ {
			fmt.Fprintf(&sb, form+"\n<< >>\nendobj\n", k)
		}
		if got := objectBound(strings.NewReader(sb.String()), 200_000); got != 1000 {
			t.Errorf("%s: bound = %d; want 1000", form, got)
		}
	}
}

// Objects packed in object streams have no header; their /N is counted.
func TestObjectStreamsReserveForPackedObjects(t *testing.T) {
	for _, dict := range []string{"/Type /ObjStm /N 5000 /First 10", "/N 5000 /First 10 /Type /ObjStm", "/Type 9 0 R /N 5000 /First 10"} {
		pdf := "%PDF-1.5\n1 0 obj\n<< " + dict + " /Length 0 >>\nstream\n\nendstream\nendobj\n"
		if got := objectBound(strings.NewReader(pdf), 200_000); got != 5001 {
			t.Errorf("%s: bound = %d; want 5001", dict, got)
		}
	}
}

// An object stream whose /N is not a direct integer (pdfcpu resolves it), or
// missing, counts as the per-stream maximum.
func TestObjectStreamsUnreadableCountAsMaximum(t *testing.T) {
	for _, dict := range []string{
		"/Type /ObjStm /N 4 0 R /First 10",
		"/Type /ObjStm /First 10",
		"/Type /ObjStm /First 10 /Pad (" + strings.Repeat("x", 300) + ") /N 4",
	} {
		pdf := "%PDF-1.5\n1 0 obj\n<< " + dict + " /Length 0 >>\nstream\n\nendstream\nendobj\n"
		if got := objectBound(strings.NewReader(pdf), 200_000); got <= 200_000 {
			t.Errorf("%.40s: bound = %d; want over the cap", dict, got)
		}
	}
}

// Outline dictionaries have an indirect /First and no /N.
func TestObjectBoundIgnoresIndirectFirst(t *testing.T) {
	pdf := "%PDF-1.5\n1 0 obj\n<< /Type /Outlines /First 5 0 R /Last 6 0 R /Count 2 >>\nendobj\n"
	if got := objectBound(strings.NewReader(pdf), 200_000); got != 1 {
		t.Errorf("bound = %d; want 1", got)
	}
}

// A match that straddles a chunk boundary counts once.
func TestObjectBoundAcrossChunks(t *testing.T) {
	for _, pad := range []int{64<<10 - 40, 64<<10 - 3, 64 << 10, 128<<10 - 300, 128<<10 + 7} {
		pdf := strings.Repeat(" ", pad) + "7 0 obj\n<< /Type /ObjStm /N 4 /First 9 >>\nstream\nendstream\nendobj\n"
		if got := objectBound(strings.NewReader(pdf), 200_000); got != 5 {
			t.Errorf("pad %d: bound = %d; want 5", pad, got)
		}
	}
}
