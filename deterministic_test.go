package byblos

// byb-c53: every write that funnels through pdfcpu's writer must be
// byte-deterministic BY DEFAULT. pdfcpu v0.13.0 injects two per-run values on
// every write -- ensureInfoDict stamps CreationDate/ModDate with time.Now()
// (per-second drift) and fileID hashes time.Now() nanoseconds into /ID
// (per-invocation drift) -- so without a pin the same input processed twice
// yields two different files with two different hashes, which breaks
// deduplication and content-addressed storage for the archive byblos serves.
//
// These tests run each pdfcpu-writing operation twice on identical input and
// require identical bytes. The /ID drifts on EVERY invocation, so a plain
// byte-compare catches an unpinned writer immediately; the date pin is
// asserted by value (the documented constant, or the input's own dates) so it
// does not depend on the test straddling a second boundary.

import (
	"bytes"
	"fmt"
	"image"
	"testing"
	"time"
)

// pinnedDateWant is the documented constant internal/pdfdoc pins
// CreationDate/ModDate to when the input document carried none.
const pinnedDateWant = "D:20000101000000+00'00'"

// assertStillOpens runs Inspect over data, so a same-length in-place patch
// that corrupted structure (a shifted xref offset, a broken hex literal)
// fails here rather than in a downstream consumer.
func assertStillOpens(t *testing.T, data []byte) {
	t.Helper()
	if _, err := Inspect(bytes.NewReader(data)); err != nil {
		t.Fatalf("patched output no longer opens: %v", err)
	}
}

// runTwice runs op twice and requires byte-identical output, returning it.
func runTwice(t *testing.T, op func(w *bytes.Buffer) error) []byte {
	t.Helper()
	var a, b bytes.Buffer
	if err := op(&a); err != nil {
		t.Fatalf("first run: %v", err)
	}
	if err := op(&b); err != nil {
		t.Fatalf("second run: %v", err)
	}
	if !bytes.Equal(a.Bytes(), b.Bytes()) {
		i := 0
		for i < len(a.Bytes()) && i < len(b.Bytes()) && a.Bytes()[i] == b.Bytes()[i] {
			i++
		}
		t.Fatalf("two runs differ: %d vs %d bytes, first difference at offset %d", a.Len(), b.Len(), i)
	}
	return a.Bytes()
}

func TestStampTextLayerIsByteDeterministic(t *testing.T) {
	base := corpusDoc(t, "scan")
	tl := TextLayer{Pages: [][]PositionedWord{
		{{Text: "byb-c53", Bounds: image.Rect(72, 700, 140, 712)}},
	}}
	out := runTwice(t, func(w *bytes.Buffer) error {
		return StampTextLayer(w, bytes.NewReader(base), tl)
	})
	// The input has no Info dictionary, so both dates must be the constant --
	// not whatever second the test happened to run in.
	for _, key := range []string{"CreationDate", "ModDate"} {
		if !bytes.Contains(out, []byte("/"+key+"("+pinnedDateWant+")")) {
			t.Errorf("output does not pin /%s to %s", key, pinnedDateWant)
		}
	}
	assertStillOpens(t, out)
}

func TestReplaceImagesIsByteDeterministic(t *testing.T) {
	base := corpusDoc(t, "scan")
	pages := inspect(t, "scan")
	objNr := pages[0].Images[0].ObjNr
	sub := map[int]EncodedImage{objNr: {
		Width: 3, Height: 2, BPC: 8,
		ColorSpace: ColorSpace{Name: "DeviceGray"},
		Filter:     "FlateDecode",
		Data:       flateEncode(t, []byte{0, 64, 128, 192, 255, 32}),
	}}
	out := runTwice(t, func(w *bytes.Buffer) error {
		return ReplaceImages(w, bytes.NewReader(base), sub)
	})
	assertStillOpens(t, out)
}

func TestWriteProvenanceIsByteDeterministic(t *testing.T) {
	base := corpusDoc(t, "born-digital")
	// A fixed record: WriteProvenance stores exactly what it is given, so a
	// caller-supplied ProcessedAt is not a source of drift.
	prov := Provenance{Version: Version, ProcessedAt: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)}
	out := runTwice(t, func(w *bytes.Buffer) error {
		return WriteProvenance(bytes.NewReader(base), w, prov)
	})
	assertStillOpens(t, out)
}

func TestOptimizeLinearizeIsByteDeterministic(t *testing.T) {
	// Give the input a provenance record first: on a document with none,
	// Optimize itself minting ProcessedAt=time.Now() into the record is
	// byblos-level drift that no writer pin can (or should) remove -- that
	// field exists to say when processing happened. With provenance already
	// present, Optimize's output is a pure function of its input, and this
	// test requires exactly that, through the deepest write path there is:
	// pdfcpu rewrite, provenance rewrite, then the Annex F linearizer.
	var dated bytes.Buffer
	prov := Provenance{Version: Version, ProcessedAt: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)}
	if err := WriteProvenance(bytes.NewReader(corpusDoc(t, nonPassThroughFixture)), &dated, prov); err != nil {
		t.Fatalf("WriteProvenance: %v", err)
	}
	base := dated.Bytes()
	out := runTwice(t, func(w *bytes.Buffer) error {
		return Optimize(w, bytes.NewReader(base), OptimizeOptions{Linearize: true})
	})
	assertStillOpens(t, out)
}

// datedPDF is minimalPDF plus an Info dictionary carrying the given
// CreationDate and ModDate, so the pin's preserve-the-input's-dates half has
// a fixture to bite on (the corpus generators write no Info at all).
func datedPDF(t *testing.T, creation, mod string) []byte {
	t.Helper()
	base := minimalPDF(t, "")
	// Splice an Info object in front of the xref section and rebuild the
	// tail. Offsets of objects 1..N are unchanged because the Info object is
	// appended after them.
	xref := bytes.LastIndex(base, []byte("xref\n"))
	if xref < 0 {
		t.Fatal("minimalPDF has no xref keyword")
	}
	var buf bytes.Buffer
	buf.Write(base[:xref])
	infoNr := 5 // minimalPDF(t, one fragment) emits objects 1..4
	infoOff := buf.Len()
	fmt.Fprintf(&buf, "%d 0 obj\n<< /CreationDate (%s) /ModDate (%s) >>\nendobj\n", infoNr, creation, mod)
	start := buf.Len()
	fmt.Fprintf(&buf, "xref\n0 %d\n0000000000 65535 f \n", infoNr+1)
	for _, off := range objectOffsets(t, base, infoNr-1) {
		fmt.Fprintf(&buf, "%010d 00000 n \n", off)
	}
	fmt.Fprintf(&buf, "%010d 00000 n \n", infoOff)
	fmt.Fprintf(&buf, "trailer\n<< /Size %d /Root 1 0 R /Info %d 0 R >>\nstartxref\n%d\n%%%%EOF\n", infoNr+1, infoNr, start)
	return buf.Bytes()
}

// objectOffsets scans data for its "n 0 obj" headers, 1..count, in object order.
func objectOffsets(t *testing.T, data []byte, count int) []int {
	t.Helper()
	offs := make([]int, count)
	for n := 1; n <= count; n++ {
		i := bytes.Index(data, []byte(fmt.Sprintf("\n%d 0 obj\n", n)))
		if i < 0 {
			t.Fatalf("object %d not found", n)
		}
		offs[n-1] = i + 1
	}
	return offs
}

func TestWriteKeepsTheInputDocumentsDates(t *testing.T) {
	base := datedPDF(t, "D:20190102030405+01'00'", "D:20200607080910-05'00'")
	tl := TextLayer{Pages: [][]PositionedWord{
		{{Text: "dated", Bounds: image.Rect(72, 700, 120, 712)}},
	}}
	out := runTwice(t, func(w *bytes.Buffer) error {
		return StampTextLayer(w, bytes.NewReader(base), tl)
	})
	if !bytes.Contains(out, []byte("/CreationDate(D:20190102030405+01'00')")) {
		t.Errorf("input's CreationDate did not survive the write")
	}
	if !bytes.Contains(out, []byte("/ModDate(D:20200607080910-05'00')")) {
		t.Errorf("input's ModDate did not survive the write")
	}
	assertStillOpens(t, out)
}
