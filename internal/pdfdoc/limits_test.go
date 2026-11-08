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
	"runtime/debug"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

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
		if got := objectBound(strings.NewReader(sb.String())); got != 1000 {
			t.Errorf("%s: bound = %d; want 1000", form, got)
		}
	}
}

// objStmPDF is a one-page file whose xref stream makes `streams` copies of one
// object stream of n entries (a few bytes compressed). The dictionary is dict
// with %N and %F filled in; obj 4 holds n and obj 8 holds the prolog length.
func objStmPDF(t *testing.T, n, streams int, dict string) []byte {
	t.Helper()
	return objStmPDFWith(t, n, streams, dict, "")
}

// objStmPDFWith is objStmPDF with lure, a comment line after the header.
func objStmPDFWith(t *testing.T, n, streams int, dict, lure string) []byte {
	t.Helper()
	prolog := strings.Repeat("7 0 ", n)
	var z bytes.Buffer
	zw := zlib.NewWriter(&z)
	zw.Write([]byte(prolog + "0"))
	zw.Close()
	dict = strings.NewReplacer("%N", strconv.Itoa(n), "%F", strconv.Itoa(len(prolog))).Replace(dict)
	var out bytes.Buffer
	offs := map[int]int{}
	obj := func(num int, body string) {
		offs[num] = out.Len()
		fmt.Fprintf(&out, "%d 0 obj\n%s\nendobj\n", num, body)
	}
	out.WriteString("%PDF-1.5\n" + lure)
	obj(1, "<< /Type /Catalog /Pages 2 0 R >>")
	obj(2, "<< /Type /Pages /Kids [3 0 R] /Count 1 >>")
	obj(3, "<< /Type /Page /Parent 2 0 R /MediaBox [0 0 10 10] >>")
	obj(4, strconv.Itoa(n))
	obj(8, strconv.Itoa(len(prolog)))
	for s := 0; s < streams; s++ {
		obj(9+s, fmt.Sprintf("<< %s /Filter /FlateDecode /Length %d >>\nstream\n%s\nendstream", dict, z.Len(), z.Bytes()))
	}
	x := 9 + streams
	size := x + 1 + streams
	var rows bytes.Buffer
	for i := 0; i < size; i++ {
		typ, a, b := 1, 0, 0
		switch {
		case i == 0:
			typ, b = 0, 65535
		case i == 5 || i == 6 || i == 7:
			typ, b = 0, 0
		case i == x:
			a = out.Len()
		case i > x:
			typ, a = 2, 9+i-x-1
		default:
			a = offs[i]
		}
		rows.Write([]byte{byte(typ), byte(a >> 24), byte(a >> 16), byte(a >> 8), byte(a), byte(b >> 8), byte(b)})
	}
	xo := out.Len()
	fmt.Fprintf(&out, "%d 0 obj\n<< /Type /XRef /Size %d /W [1 4 2] /Root 1 0 R /Length %d >>\nstream\n%s\nendstream\nendobj\nstartxref\n%d\n%%%%EOF\n", x, size, rows.Len(), rows.Bytes(), xo)
	return out.Bytes()
}

// pdfcpu reads /First and /N by its own tokenizer (a reference, a form feed or
// NUL or a comment between name and value, +n, #-escaped names, a repeated key
// where the last wins), so no reading of the dictionary here can be trusted to
// see what it sees. It enforces the bound itself, on the stream it reads.
func TestObjectStreamCountIsHeldByPdfcpu(t *testing.T) {
	defer limits.SetMemoryLimit(limits.SetMemoryLimit(300 << 20))
	for name, dict := range map[string]string{
		"first ref": "/Type /ObjStm /N %N /First 8 0 R",
		"form feed": "/Type /ObjStm /N %N /First\x0c%F",
		"nul":       "/Type /ObjStm /N %N /First\x00%F",
		"comment":   "/Type /ObjStm /N %N /First %c\n%F",
		"plus":      "/Type /ObjStm /N %N /First +%F",
		"hex name":  "/Type /ObjStm /N %N /Fir#73t %F",
		"dup N":     "/Type /ObjStm /N 1 /First %F /N %N",
	} {
		wantObjectsLimit(t, name, objStmPDF(t, 1_000_000, 1, dict), limits.Limits{})
		wantObjectsLimit(t, name+" x5", objStmPDF(t, 1_000_000, 5, dict), limits.Limits{})
	}
}

// ISO 32000 lets /N be indirect; such a file is not refused for it.
func TestIndirectObjectStreamCountIsAccepted(t *testing.T) {
	defer limits.SetMemoryLimit(limits.SetMemoryLimit(300 << 20))
	openBudgetedOK(t, objStmPDF(t, 10, 1, "/Type /ObjStm /N 4 0 R /First %F"))
}

// A match that straddles a chunk boundary counts once.
func TestObjectBoundAcrossChunks(t *testing.T) {
	for _, pad := range []int{64<<10 - 40, 64<<10 - 3, 64 << 10, 128<<10 - 300, 128<<10 + 7} {
		pdf := strings.Repeat(" ", pad) + "7 0 obj\n<< /Type /ObjStm /N 4 /First 9 >>\nstream\nendstream\nendobj\n"
		if got := objectBound(strings.NewReader(pdf)); got != 1 {
			t.Errorf("pad %d: bound = %d; want 1", pad, got)
		}
	}
}

// The cumulative limits (pergamon-go v0.16.4-pergamon.1): each object stream
// is under every per-stream cap, so only the sum over the parse refuses them.
// The comment line is a decoy that once raised the pre-scan's bound, and the
// form feed spells /First as no scan here reads it. Both files sat under
// 310 KB and took 600 MB and 2.8 GB to parse.
func TestCumulativeObjectStreamEntriesAreRefused(t *testing.T) {
	const dict, lure = "/Type /ObjStm /N %N /First\x0c%F", "% /Type /ObjStm /N 15000 /First 0 stream\n"
	for name, c := range map[string]struct {
		streams int
		budget  int64
	}{"300 streams under 32 MiB": {300, 32 << 20}, "1500 streams under 300 MiB": {1500, 300 << 20}} {
		t.Run(name, func(t *testing.T) {
			defer limits.SetMemoryLimit(limits.SetMemoryLimit(c.budget))
			pdf := objStmPDFWith(t, 15000, c.streams, dict, lure)
			before := limitsTotalAlloc()
			_, release, err := OpenBudgeted(context.Background(), bytes.NewReader(pdf), limits.Limits{})
			delta := limitsTotalAlloc() - before
			if err == nil {
				release()
			}
			var le *limits.LimitError
			if !errors.As(err, &le) || le.Name != "objects" || !errors.Is(err, limits.ErrLimit) || errors.Is(err, limits.ErrMemoryLimit) {
				t.Fatalf("err = %v; want a permanent *LimitError objects", err)
			}
			if delta > 32<<20 {
				t.Errorf("TotalAlloc delta = %d MiB; want the parse stopped by the first stream's count", delta>>20)
			}
		})
	}
}

// padTreePDF is a page tree of k intermediate Pages nodes, each alone in its own
// Flate object stream that inflates to pad bytes. pdfcpu decodes a stream when
// the first object in it is read, so the tree is what decodes them.
func padTreePDF(k, pad int) []byte {
	var out bytes.Buffer
	offs := map[int]int{}
	obj := func(n int, b string) { offs[n] = out.Len(); fmt.Fprintf(&out, "%d 0 obj\n%s\nendobj\n", n, b) }
	out.WriteString("%PDF-1.5\n")
	first := 100 + k
	kids := ""
	for i := 0; i < k; i++ {
		kids += fmt.Sprintf("%d 0 R ", first+i)
	}
	obj(1, "<< /Type /Catalog /Pages 2 0 R >>")
	obj(2, fmt.Sprintf("<< /Type /Pages /Kids [%s] /Count %d >>", kids, k))
	// page leaf shared
	obj(3, "<< /Type /Page /Parent 2 0 R /MediaBox [0 0 10 10] >>")
	for i := 0; i < k; i++ {
		prolog := fmt.Sprintf("%d 0 ", first+i)
		body := "<< /Type /Pages /Parent 2 0 R /Kids [3 0 R] /Count 1 >>"
		var z bytes.Buffer
		zw := zlib.NewWriter(&z)
		zw.Write([]byte(prolog + body + strings.Repeat(" ", pad)))
		zw.Close()
		obj(4+i, fmt.Sprintf("<< /Type /ObjStm /N 1 /First %d /Filter /FlateDecode /Length %d >>\nstream\n%s\nendstream", len(prolog), z.Len(), z.Bytes()))
	}
	x := 4 + k
	size := first + k
	var rows bytes.Buffer
	for i := 0; i < size; i++ {
		typ, a, b := 1, 0, 0
		switch {
		case i == 0:
			typ, b = 0, 65535
		case i == x:
			a = out.Len()
		case i >= first:
			typ, a = 2, 4+i-first
		case offs[i] == 0:
			typ = 0
		default:
			a = offs[i]
		}
		rows.Write([]byte{byte(typ), byte(a >> 24), byte(a >> 16), byte(a >> 8), byte(a), byte(b >> 8), byte(b)})
	}
	xo := out.Len()
	fmt.Fprintf(&out, "%d 0 obj\n<< /Type /XRef /Size %d /W [1 4 2] /Root 1 0 R /Length %d >>\nstream\n%s\nendstream\nendobj\nstartxref\n%d\n%%%%EOF\n", x, size, rows.Len(), rows.Bytes(), xo)
	return out.Bytes()
}

// 40 streams of 20 MiB each decode under the 64 MiB stream cap; the sum is what
// the cumulative decoded-byte limit refuses, as the page tree reaches them.
func TestCumulativeDecodedBytesAreRefused(t *testing.T) {
	defer limits.SetMemoryLimit(limits.SetMemoryLimit(300 << 20))
	d, release, err := OpenBudgeted(context.Background(), bytes.NewReader(padTreePDF(40, 20<<20)), limits.Limits{})
	if err == nil {
		defer release()
		for n := 1; n <= d.PageCount() && err == nil; n++ {
			_, err = d.Page(n)
		}
	}
	var le *limits.LimitError
	if !errors.As(err, &le) || le.Name != "stream-bytes" || !errors.Is(err, limits.ErrLimit) || errors.Is(err, limits.ErrMemoryLimit) {
		t.Fatalf("err = %v; want a permanent *LimitError stream-bytes", err)
	}
}

// Both limits are Limits fields, and a file under them opens.
func TestObjectStreamLimitsAreConfigurable(t *testing.T) {
	defer limits.SetMemoryLimit(limits.SetMemoryLimit(300 << 20))
	packed := objStmPDF(t, 10, 1, "/Type /ObjStm /N %N /First %F")
	padded := padTreePDF(3, 1<<20)
	for name, c := range map[string]struct {
		pdf  []byte
		lim  limits.Limits
		want string
	}{
		"entries":   {packed, limits.Limits{MaxObjectStreamEntries: 5}, "objects"},
		"decoded":   {padded, limits.Limits{MaxTotalDecodeBytes: 2 << 20}, "stream-bytes"},
		"roomy":     {packed, limits.Limits{MaxObjectStreamEntries: 10}, ""},
		"roomy too": {padded, limits.Limits{MaxTotalDecodeBytes: 4 << 20}, ""},
	} {
		d, release, err := OpenBudgeted(context.Background(), bytes.NewReader(c.pdf), c.lim)
		if err == nil {
			for n := 1; n <= d.PageCount() && err == nil; n++ {
				_, err = d.Page(n)
			}
			release()
		}
		var le *limits.LimitError
		switch {
		case c.want == "" && err != nil:
			t.Errorf("%s: err = %v; want it to open", name, err)
		case c.want != "" && (!errors.As(err, &le) || le.Name != c.want):
			t.Errorf("%s: err = %v; want *LimitError %s", name, err, c.want)
		}
	}
}

// bigObjectPDF is one page whose /Contents is object 5, an array of n empty
// arrays: 2 bytes of input become about 40 of heap, so the decoded-byte limits
// never see it. packed puts the object in a Flate object stream, else it is
// written in place.
func bigObjectPDF(n int, packed bool) []byte {
	body := "[" + strings.Repeat("[]", n) + "]"
	var out bytes.Buffer
	offs := map[int]int{}
	obj := func(nr int, b string) { offs[nr] = out.Len(); fmt.Fprintf(&out, "%d 0 obj\n%s\nendobj\n", nr, b) }
	out.WriteString("%PDF-1.5\n")
	obj(1, "<< /Type /Catalog /Pages 2 0 R >>")
	obj(2, "<< /Type /Pages /Kids [3 0 R] /Count 1 >>")
	obj(3, "<< /Type /Page /Parent 2 0 R /MediaBox [0 0 10 10] /Contents 5 0 R >>")
	if !packed {
		obj(5, body)
		xo := out.Len()
		out.WriteString("xref\n0 6\n0000000000 65535 f \n")
		for i := 1; i < 6; i++ {
			if o, ok := offs[i]; ok {
				fmt.Fprintf(&out, "%010d 00000 n \n", o)
			} else {
				out.WriteString("0000000000 00000 f \n")
			}
		}
		fmt.Fprintf(&out, "trailer\n<< /Size 6 /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", xo)
		return out.Bytes()
	}
	var z bytes.Buffer
	zw := zlib.NewWriter(&z)
	zw.Write([]byte("5 0 " + body))
	zw.Close()
	obj(4, fmt.Sprintf("<< /Type /ObjStm /N 1 /First 4 /Filter /FlateDecode /Length %d >>\nstream\n%s\nendstream", z.Len(), z.Bytes()))
	x := out.Len()
	var rows bytes.Buffer
	for i := 0; i < 7; i++ {
		typ, a := 1, offs[i]
		switch i {
		case 0:
			typ, a = 0, 0
		case 5:
			typ, a = 2, 4
		case 6:
			a = x
		}
		rows.Write([]byte{byte(typ), byte(a >> 24), byte(a >> 16), byte(a >> 8), byte(a), 0, 0})
	}
	fmt.Fprintf(&out, "6 0 obj\n<< /Type /XRef /Size 7 /W [1 4 2] /Root 1 0 R /Length %d >>\nstream\n%s\nendstream\nendobj\nstartxref\n%d\n%%%%EOF\n", rows.Len(), rows.Bytes(), x)
	return out.Bytes()
}

// One 6 MB object (3 000 000 empty arrays) is refused as parsed-bytes under a
// 32 MiB budget, in place or packed, before the parse holds more than the
// limit: unrefused it took 167 MiB of live heap.
func TestOneHugeObjectIsRefused(t *testing.T) {
	for _, packed := range []bool{false, true} {
		t.Run(fmt.Sprint("packed=", packed), func(t *testing.T) {
			defer limits.SetMemoryLimit(limits.SetMemoryLimit(32 << 20))
			pdf := bigObjectPDF(3_000_000, packed)
			before := limitsTotalAlloc()
			d, release, err := OpenBudgeted(context.Background(), bytes.NewReader(pdf), limits.Limits{})
			if err == nil {
				_, err = d.Page(1)
				release()
			}
			delta := limitsTotalAlloc() - before
			var le *limits.LimitError
			if !errors.As(err, &le) || le.Name != "parsed-bytes" || !errors.Is(err, limits.ErrLimit) || errors.Is(err, limits.ErrMemoryLimit) {
				t.Fatalf("err = %v; want a permanent *LimitError parsed-bytes", err)
			}
			if delta > 128<<20 {
				t.Errorf("TotalAlloc delta = %d MiB; want the parse stopped near the limit", delta>>20)
			}
		})
	}
}

// peakHeap runs f with the collector keeping garbage to a tenth of the live
// heap, and returns the most heap in use that a 1 ms sample saw.
func peakHeap(f func()) uint64 {
	defer debug.SetGCPercent(debug.SetGCPercent(10))
	runtime.GC()
	var peak atomic.Uint64
	stop, done := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(done)
		var m runtime.MemStats
		for {
			select {
			case <-stop:
				return
			case <-time.After(time.Millisecond):
				runtime.ReadMemStats(&m)
				peak.Store(max(peak.Load(), m.HeapAlloc))
			}
		}
	}()
	f()
	close(stop)
	<-done
	return peak.Load()
}

// tablePDF declares filler null objects, which size the object table the
// reservation holds but use almost none of it, and one object of n copies of
// elem written in place.
func tablePDF(filler, n int, elem string) []byte {
	var out bytes.Buffer
	offs := map[int]int{}
	obj := func(nr int, b string) { offs[nr] = out.Len(); fmt.Fprintf(&out, "%d 0 obj\n%s\nendobj\n", nr, b) }
	out.WriteString("%PDF-1.4\n")
	obj(1, "<< /Type /Catalog /Pages 2 0 R /X 5 0 R >>")
	obj(2, "<< /Type /Pages /Kids [3 0 R] /Count 1 >>")
	obj(3, "<< /Type /Page /Parent 2 0 R /MediaBox [0 0 10 10] >>")
	obj(4, "null")
	obj(5, "["+strings.Repeat(elem, n)+"]")
	size := 6 + filler
	for i := 6; i < size; i++ {
		obj(i, "null")
	}
	xo := out.Len()
	fmt.Fprintf(&out, "xref\n0 %d\n0000000000 65535 f \n", size)
	for i := 1; i < size; i++ {
		fmt.Fprintf(&out, "%010d 00000 n \n", offs[i])
	}
	fmt.Fprintf(&out, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", size, xo)
	return out.Bytes()
}

// The object table is reserved for the objects a document declares, but filler
// objects use almost none of it. One object of 210 000 "<</a 1>>" elements
// (about 80 MB live) must not take that unused allowance: it is refused as
// parsed-bytes under a 32 MiB budget, as it is under 300 MiB with a larger table.
func TestHugeObjectCannotSpendTheUnusedTable(t *testing.T) {
	for _, c := range []struct {
		name          string
		budget        int64
		filler, elems int
	}{
		{"32MiB", 32 << 20, 16_000, 330_000},
		{"300MiB", 300 << 20, 170_000, 3_200_000},
	} {
		t.Run(c.name, func(t *testing.T) {
			defer limits.SetMemoryLimit(limits.SetMemoryLimit(c.budget))
			pdf := tablePDF(c.filler, c.elems, "<</a 1>>")
			var gotErr error
			peak := peakHeap(func() {
				d, release, err := OpenBudgeted(context.Background(), bytes.NewReader(pdf), limits.Limits{})
				if err == nil {
					release()
					_ = d
				}
				gotErr = err
			})
			err := gotErr
			var le *limits.LimitError
			// With the table this large the decode slot is small, so the object
			// cap (object-bytes) can refuse it before the parsed total does.
			if !errors.As(err, &le) || (le.Name != "parsed-bytes" && le.Name != "object-bytes") || !errors.Is(err, limits.ErrLimit) {
				t.Fatalf("err = %v; want a permanent *LimitError parsed-bytes or object-bytes", err)
			}
			t.Logf("peak heap %d MiB", peak>>20)
			if peak > uint64(c.budget) {
				t.Errorf("peak heap = %d MiB; want the parse stopped inside the %d MiB budget", peak>>20, c.budget>>20)
			}
		})
	}
}

// One 4 MB Flate stream named 16 times in /Contents is 64 MB of page content:
// byblos decodes it outside pdfcpu's counters, so the page total is what must
// hold, and under a budget it holds the decode slot, not the 128 MiB default.
func TestRepeatedContentStreamIsRefusedUnderABudget(t *testing.T) {
	defer limits.SetMemoryLimit(limits.SetMemoryLimit(32 << 20))
	var z bytes.Buffer
	zw := zlib.NewWriter(&z)
	zw.Write(bytes.Repeat([]byte(" "), 4_000_000))
	zw.Close()
	var out bytes.Buffer
	offs := map[int]int{}
	obj := func(nr int, b string) { offs[nr] = out.Len(); fmt.Fprintf(&out, "%d 0 obj\n%s\nendobj\n", nr, b) }
	out.WriteString("%PDF-1.7\n")
	obj(1, "<< /Type /Catalog /Pages 2 0 R >>")
	obj(2, "<< /Type /Pages /Kids [3 0 R] /Count 1 >>")
	obj(3, "<< /Type /Page /Parent 2 0 R /MediaBox [0 0 10 10] /Contents ["+strings.Repeat("4 0 R ", 16)+"] >>")
	obj(4, fmt.Sprintf("<< /Filter /FlateDecode /Length %d >>\nstream\n%s\nendstream", z.Len(), z.Bytes()))
	xo := out.Len()
	out.WriteString("xref\n0 5\n0000000000 65535 f \n")
	for i := 1; i < 5; i++ {
		fmt.Fprintf(&out, "%010d 00000 n \n", offs[i])
	}
	fmt.Fprintf(&out, "trailer\n<< /Size 5 /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", xo)

	before := limitsTotalAlloc()
	d, release, err := OpenBudgeted(context.Background(), bytes.NewReader(out.Bytes()), limits.Limits{})
	if err == nil {
		_, err = d.Page(1)
		release()
	}
	delta := limitsTotalAlloc() - before
	var le *limits.LimitError
	if !errors.As(err, &le) || le.Name != "page-stream-bytes" || !errors.Is(err, limits.ErrLimit) || errors.Is(err, limits.ErrMemoryLimit) {
		t.Fatalf("err = %v; want a permanent *LimitError page-stream-bytes", err)
	}
	if delta > 96<<20 {
		t.Errorf("TotalAlloc delta = %d MiB; want the second reference refused", delta>>20)
	}
}

// scalarsPDF is one catalog whose /X lists objects 5.. with the given bodies.
func scalarsPDF(bodies ...string) []byte {
	var out bytes.Buffer
	offs := map[int]int{}
	obj := func(nr int, b string) { offs[nr] = out.Len(); fmt.Fprintf(&out, "%d 0 obj\n%s\nendobj\n", nr, b) }
	var refs strings.Builder
	for i := range bodies {
		fmt.Fprintf(&refs, "%d 0 R ", 5+i)
	}
	out.WriteString("%PDF-1.4\n")
	obj(1, "<< /Type /Catalog /Pages 2 0 R /X ["+refs.String()+"] >>")
	obj(2, "<< /Type /Pages /Kids [3 0 R] /Count 1 >>")
	obj(3, "<< /Type /Page /Parent 2 0 R /MediaBox [0 0 10 10] >>")
	obj(4, "null")
	for i, b := range bodies {
		obj(5+i, b)
	}
	size := 5 + len(bodies)
	xo := out.Len()
	fmt.Fprintf(&out, "xref\n0 %d\n0000000000 65535 f \n", size)
	for i := 1; i < size; i++ {
		fmt.Fprintf(&out, "%010d 00000 n \n", offs[i])
	}
	fmt.Fprintf(&out, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", size, xo)
	return out.Bytes()
}

// A string, hex string or name is heap pdfcpu holds twice over while it reads
// the object (the buffer, then the value), and neither the array element nor
// dictionary entry charge saw its bytes: 60 MB of one string held 126 MiB live
// under a 32 MiB budget, and four 65 MB strings (each under the per-object cap)
// held 313 MB against a 256 MiB reservation. Each shape below, at a size a
// 32 MiB budget cannot take, is a permanent *LimitError with the heap the parse
// added kept inside the budget.
func TestHostileScalarsAreRefusedUnderABudget(t *testing.T) {
	const mib = 1 << 20
	keys := new(strings.Builder)
	for i := 0; i < 240_000; i++ {
		fmt.Fprintf(keys, "/k%07d 1 ", i)
	}
	for _, c := range []struct {
		name, limit string
		bodies      []string
	}{
		{"string", "object-bytes", []string{"(" + strings.Repeat("A", 12*mib) + ")"}},
		{"hex", "object-bytes", []string{"<" + strings.Repeat("41", 6*mib) + ">"}},
		{"name", "object-bytes", []string{"/" + strings.Repeat("a", 12*mib)}},
		{"many keys", "parsed-bytes", []string{"<<" + keys.String() + ">>"}},
		{"strings each under the object cap", "parsed-bytes", []string{
			"(" + strings.Repeat("A", 2*mib) + ")", "(" + strings.Repeat("A", 2*mib) + ")",
			"(" + strings.Repeat("A", 2*mib) + ")", "(" + strings.Repeat("A", 2*mib) + ")",
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			defer limits.SetMemoryLimit(limits.SetMemoryLimit(32 << 20))
			pdf := scalarsPDF(c.bodies...)
			var base runtime.MemStats
			runtime.GC()
			runtime.ReadMemStats(&base)
			var gotErr error
			peak := peakHeap(func() {
				d, release, err := OpenBudgeted(context.Background(), bytes.NewReader(pdf), limits.Limits{})
				if err == nil {
					release()
					_ = d
				}
				gotErr = err
			})
			var le *limits.LimitError
			if !errors.As(gotErr, &le) || le.Name != c.limit || !errors.Is(gotErr, limits.ErrLimit) || errors.Is(gotErr, limits.ErrMemoryLimit) {
				t.Fatalf("err = %v; want a permanent *LimitError %s", gotErr, c.limit)
			}
			added := int64(peak) - int64(base.HeapAlloc)
			t.Logf("heap added %d MiB", added>>20)
			if added > 32*mib {
				t.Errorf("heap added = %d MiB; want the parse stopped inside the 32 MiB budget", added>>20)
			}
		})
	}
}
